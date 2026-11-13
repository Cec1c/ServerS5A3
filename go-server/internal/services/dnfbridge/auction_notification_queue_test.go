package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/mail"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/platform/eventloop"
)

type auctionNotificationCountingConn struct {
	net.Conn
	writes atomic.Int64
}

func (c *auctionNotificationCountingConn) Write(data []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(data)
}

type auctionNotificationQueueFixture struct {
	service  *Service
	session  *gameSession
	buffer   *bufferConn
	conn     *auctionNotificationCountingConn
	ctx      context.Context
	repoChar uint16
}

func newAuctionNotificationQueueFixture(t *testing.T) *auctionNotificationQueueFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "99",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", CreatedAt: time.Now().UTC()},
		},
	}); err != nil {
		t.Fatal(err)
	}
	buffer := &bufferConn{}
	conn := &auctionNotificationCountingConn{Conn: buffer}
	session := &gameSession{
		conn:    conn,
		ctx:     ctx,
		connID:  "auction-notification-queue",
		channel: channelcatalog.Channel{ID: 19},
	}
	service := &Service{
		options: options{
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		gameSessions:       make(map[uint16]*gameSession),
	}
	if err := service.startGameSessionEvents(ctx, session); err != nil {
		t.Fatal(err)
	}
	service.bindGameSessionCharacter(session, 99)
	buffer.write.Reset()
	conn.writes.Store(0)
	return &auctionNotificationQueueFixture{
		service:  service,
		session:  session,
		buffer:   buffer,
		conn:     conn,
		ctx:      ctx,
		repoChar: 99,
	}
}

func (f *auctionNotificationQueueFixture) replacementSession() (*gameSession, *bufferConn, *auctionNotificationCountingConn) {
	buffer := &bufferConn{}
	conn := &auctionNotificationCountingConn{Conn: buffer}
	session := &gameSession{
		conn:    conn,
		ctx:     f.ctx,
		connID:  "auction-notification-replacement",
		channel: channelcatalog.Channel{ID: 19},
	}
	return session, buffer, conn
}

func (f *auctionNotificationQueueFixture) blockOwner(t *testing.T) (func(), <-chan error) {
	t.Helper()
	started := make(chan struct{})
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseCh) })
	}
	done := make(chan error, 1)
	go func() {
		done <- f.service.callGameSession(f.ctx, f.session, "test-blocked-notification-owner", func() error {
			close(started)
			<-releaseCh
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		release()
		t.Fatal("notification owner did not block")
	}
	return release, done
}

func callAuctionNotificationWithin(t *testing.T, name string, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatalf("%s blocked for more than one second", name)
		return nil
	}
}

func (f *auctionNotificationQueueFixture) drainQueuedNotifications(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if err := f.service.callGameSession(ctx, f.session, "test-notification-queue-barrier", func() error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func queueAuctionNotificationPair(t *testing.T, service *Service, characterID uint16) {
	t.Helper()
	if err := callAuctionNotificationWithin(t, "mailbox alarm", func() error {
		return service.sendMailboxAlarmToOnlineRecipient(characterID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := callAuctionNotificationWithin(t, "auction notice", func() error {
		return service.sendCurrentAuctionNoticeToOnlineRecipient(
			characterID,
			currentAuctionNoticeRouteItem,
			currentAuctionNoticeKindBuyerWon,
			100170220,
		)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAuctionNotificationQueueIsNonBlockingAndOrdered(t *testing.T) {
	fixture := newAuctionNotificationQueueFixture(t)
	defer stopTestGameSessionEvents(t, fixture.service, fixture.session)
	release, ownerDone := fixture.blockOwner(t)
	defer release()

	queueAuctionNotificationPair(t, fixture.service, fixture.repoChar)
	if got := fixture.conn.writes.Load(); got != 0 {
		t.Fatalf("blocked event owner received %d writes before release", got)
	}

	release()
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	fixture.drainQueuedNotifications(t)

	packets := splitAllCurrentGameServerUpperPackets(t, fixture.buffer.write.Bytes())
	if len(packets) != 2 {
		t.Fatalf("packet count=%d want alarm+banner", len(packets))
	}
	if packets[0].Header.Classification != 0 || packets[0].Header.MsgID != mail.MailboxAlarmNotificationMessageID ||
		!bytes.Equal(packets[0].Body, mail.BuildAlarmNotification(1)) {
		t.Fatalf("mailbox alarm header=%+v body=%x", packets[0].Header, packets[0].Body)
	}
	if packets[1].Header.Classification != 0 || packets[1].Header.MsgID != currentAuctionNoticeMsgID {
		t.Fatalf("auction banner header=%+v", packets[1].Header)
	}
	assertAuctionNoticeOptionTail(t, packets[1].Body, currentAuctionNoticeRouteItem, currentAuctionNoticeKindBuyerWon, 100170220)
}

func TestAuctionNotificationQueueDropsOldCharacterGeneration(t *testing.T) {
	fixture := newAuctionNotificationQueueFixture(t)
	defer stopTestGameSessionEvents(t, fixture.service, fixture.session)
	release, ownerDone := fixture.blockOwner(t)
	defer release()

	queueAuctionNotificationPair(t, fixture.service, fixture.repoChar)
	if got := fixture.conn.writes.Load(); got != 0 {
		t.Fatalf("blocked event owner received %d writes before release", got)
	}
	oldGeneration := fixture.session.characterGeneration
	fixture.service.bindGameSessionCharacter(fixture.session, 100)
	if fixture.session.selectedCharacterID != 100 || fixture.session.characterGeneration == oldGeneration {
		t.Fatalf("character switch selected=%d generation=%d old=%d",
			fixture.session.selectedCharacterID, fixture.session.characterGeneration, oldGeneration)
	}
	fixture.buffer.write.Reset()
	fixture.conn.writes.Store(0)

	release()
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	fixture.drainQueuedNotifications(t)
	if fixture.buffer.write.Len() != 0 || fixture.conn.writes.Load() != 0 {
		t.Fatalf("old generation wrote after switch: bytes=%d writes=%d",
			fixture.buffer.write.Len(), fixture.conn.writes.Load())
	}
}

func TestAuctionNotificationQueueDropsEventsAfterSessionRebind(t *testing.T) {
	fixture := newAuctionNotificationQueueFixture(t)
	defer stopTestGameSessionEvents(t, fixture.service, fixture.session)
	release, ownerDone := fixture.blockOwner(t)
	defer release()

	queueAuctionNotificationPair(t, fixture.service, fixture.repoChar)
	if got := fixture.conn.writes.Load(); got != 0 {
		t.Fatalf("blocked event owner received %d writes before release", got)
	}
	replacement, replacementBuffer, replacementConn := fixture.replacementSession()
	fixture.service.bindGameSessionCharacter(replacement, fixture.repoChar)
	fixture.buffer.write.Reset()
	fixture.conn.writes.Store(0)
	replacementBuffer.write.Reset()
	replacementConn.writes.Store(0)

	release()
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	fixture.drainQueuedNotifications(t)
	if fixture.buffer.write.Len() != 0 || fixture.conn.writes.Load() != 0 {
		t.Fatalf("old session wrote after rebind: bytes=%d writes=%d",
			fixture.buffer.write.Len(), fixture.conn.writes.Load())
	}
	if replacementBuffer.write.Len() != 0 || replacementConn.writes.Load() != 0 {
		t.Fatalf("replacement session received stale events: bytes=%d writes=%d",
			replacementBuffer.write.Len(), replacementConn.writes.Load())
	}
}

func TestAuctionNotificationQueueFullReturnsWithoutWaiting(t *testing.T) {
	fixture := newAuctionNotificationQueueFixture(t)
	defer stopTestGameSessionEvents(t, fixture.service, fixture.session)
	release, ownerDone := fixture.blockOwner(t)
	defer release()

	identity, ok := fixture.service.onlineGameSessionCharacterSnapshot(fixture.repoChar)
	if !ok {
		t.Fatal("fixture character is not online")
	}
	full := false
	for attempt := 0; attempt < gameSessionEventQueueSize+8; attempt++ {
		err := fixture.service.tryPostGameSessionCharacterEvent(
			identity.session,
			"test-fill-auction-notification-queue",
			identity.character,
			identity.generation,
			func() error { return nil },
		)
		if errors.Is(err, eventloop.ErrQueueFull) {
			full = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !full {
		t.Fatal("session event queue did not fill")
	}

	alarmErr := callAuctionNotificationWithin(t, "mailbox alarm on full queue", func() error {
		return fixture.service.sendMailboxAlarmToOnlineRecipient(fixture.repoChar)
	})
	if !errors.Is(alarmErr, eventloop.ErrQueueFull) {
		t.Fatalf("mailbox alarm error=%v want %v", alarmErr, eventloop.ErrQueueFull)
	}
	auctionErr := callAuctionNotificationWithin(t, "auction notice on full queue", func() error {
		return fixture.service.sendCurrentAuctionNoticeToOnlineRecipient(
			fixture.repoChar,
			currentAuctionNoticeRouteItem,
			currentAuctionNoticeKindBuyerWon,
			100170220,
		)
	})
	if !errors.Is(auctionErr, eventloop.ErrQueueFull) {
		t.Fatalf("auction notice error=%v want %v", auctionErr, eventloop.ErrQueueFull)
	}
	if got := fixture.conn.writes.Load(); got != 0 {
		t.Fatalf("full queue notification wrote %d packets", got)
	}

	release()
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	fixture.drainQueuedNotifications(t)
}
