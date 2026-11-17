package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfmail "longheng.io/server/internal/modules/dnf/mail"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOnlineMailboxRecipientGetsCurrentClass0Alarm(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Mailbox.Save(ctx, dnfrepo.MailboxRecord{
		CharacterID: "88",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", CreatedAt: time.Now().UTC()},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}
	connection := &bufferConn{}
	recipient := &gameSession{
		conn:                connection,
		connID:              "mailbox-online-recipient",
		channel:             channelcatalog.Channel{ID: 19},
		selectedCharacterID: 88,
	}
	service := &Service{
		options: options{
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		gameSessions:       map[uint16]*gameSession{88: recipient},
	}

	if err := service.sendMailboxAlarmToOnlineRecipient(88); err != nil {
		t.Fatalf("send mailbox alarm: %v", err)
	}
	packet, trailing := splitGameServerUpperPacketWithHeader(t, connection.write.Bytes(), service.gameUpperHeaderSize())
	if len(trailing) != 0 || packet.Header.MsgID != 0x0063 || packet.Header.Classification != 0 ||
		!bytes.Equal(packet.Body, []byte{0x01, 0x00}) {
		t.Fatalf("class0/0x63 packet header=%+v body=% X trailing=% X", packet.Header, packet.Body, trailing)
	}
}

func TestGameAdminMailboxNotificationProjectsExternalCommittedMail(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Mailbox.Save(context.Background(), dnfrepo.MailboxRecord{
		CharacterID: "88",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", CreatedAt: time.Now().UTC()},
		},
	}); err != nil {
		t.Fatalf("save externally committed mailbox: %v", err)
	}
	connection := &bufferConn{}
	recipient := &gameSession{
		conn:                connection,
		connID:              "mailbox-admin-notification",
		channel:             channelcatalog.Channel{ID: 19},
		selectedCharacterID: 88,
		characterGeneration: 7,
	}
	service := &Service{
		options: options{
			playerAdminSecret:  strings.Repeat("x", 32),
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		gameSessions:       map[uint16]*gameSession{88: recipient},
	}
	api := &accountAPIServer{service: service}
	request := httptest.NewRequest("POST", adminapi.Path, strings.NewReader(`{"version":1,"action":"notify_mailbox","characterId":88}`))
	request.Header.Set("Authorization", "Bearer "+service.options.playerAdminSecret)
	response := httptest.NewRecorder()
	api.handleGameAdmin(response, request)
	var result adminapi.Response
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if response.Code != 200 || !result.OK || !result.Online {
		t.Fatalf("admin mailbox notification response=%d %+v", response.Code, result)
	}
	packet, trailing := splitGameServerUpperPacketWithHeader(t, connection.write.Bytes(), service.gameUpperHeaderSize())
	if len(trailing) != 0 || packet.Header.MsgID != dnfmail.MailboxAlarmNotificationMessageID ||
		packet.Header.Classification != 0 || !bytes.Equal(packet.Body, []byte{0x01, 0x00}) {
		t.Fatalf("admin class0/0x63 packet header=%+v body=% X trailing=% X", packet.Header, packet.Body, trailing)
	}
}

func TestGameAdminMailboxNotificationLeavesOfflineRecipientToLoginBootstrap(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Mailbox.Save(context.Background(), dnfrepo.MailboxRecord{
		CharacterID: "88",
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", CreatedAt: time.Now().UTC()},
		},
	}); err != nil {
		t.Fatalf("save externally committed mailbox: %v", err)
	}
	service := &Service{
		options:            options{playerAdminSecret: strings.Repeat("x", 32)},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		gameSessions:       map[uint16]*gameSession{},
	}
	api := &accountAPIServer{service: service}
	request := httptest.NewRequest("POST", adminapi.Path, strings.NewReader(`{"version":1,"action":"notify_mailbox","characterId":88}`))
	request.Header.Set("Authorization", "Bearer "+service.options.playerAdminSecret)
	response := httptest.NewRecorder()
	api.handleGameAdmin(response, request)
	var result adminapi.Response
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if response.Code != 200 || !result.OK || result.Online {
		t.Fatalf("offline admin mailbox notification response=%d %+v", response.Code, result)
	}
}

func TestGameAdminMailboxNotificationRequiresCharacter(t *testing.T) {
	service := &Service{options: options{playerAdminSecret: strings.Repeat("x", 32)}}
	api := &accountAPIServer{service: service}
	request := httptest.NewRequest("POST", adminapi.Path, strings.NewReader(`{"version":1,"action":"notify_mailbox"}`))
	request.Header.Set("Authorization", "Bearer "+service.options.playerAdminSecret)
	response := httptest.NewRecorder()
	api.handleGameAdmin(response, request)
	var result adminapi.Response
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if response.Code != 200 || result.OK || result.Error != "character required" {
		t.Fatalf("missing-character admin mailbox notification response=%d %+v", response.Code, result)
	}
}

func TestMailboxAlarmForSessionClearsEmptyMailboxState(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "mailbox-empty-state",
		channel:             channelcatalog.Channel{ID: 19},
		selectedCharacterID: 88,
	}
	service := &Service{
		options: options{
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}

	if err := service.sendMailboxAlarmForSession(session, 88, "test_empty_mailbox"); err != nil {
		t.Fatalf("send empty mailbox alarm: %v", err)
	}
	packet, trailing := splitGameServerUpperPacketWithHeader(t, connection.write.Bytes(), service.gameUpperHeaderSize())
	if len(trailing) != 0 || packet.Header.MsgID != 0x0063 || packet.Header.Classification != 0 ||
		!bytes.Equal(packet.Body, []byte{0x00, 0x00}) {
		t.Fatalf("empty class0/0x63 packet header=%+v body=% X trailing=% X", packet.Header, packet.Body, trailing)
	}
}

func TestInitialTownSceneTailSendsMailboxAlarmOnce(t *testing.T) {
	service, session := newDeferredTailResumeFixture(t)
	repositories, available := service.repositoryGroup()
	if !available {
		t.Fatal("mailbox repositories are unavailable")
	}
	characterID := "29"
	if err := repositories.Mailbox.Save(context.Background(), dnfrepo.MailboxRecord{
		CharacterID: characterID,
		Mails: map[string]dnfrepo.MailRecord{
			"1": {MailID: "1", CreatedAt: time.Now().UTC()},
			"2": {MailID: "2", Read: true, CreatedAt: time.Now().UTC()},
		},
	}); err != nil {
		t.Fatalf("save mailbox: %v", err)
	}

	if err := service.sendDeferredSelectSceneTail(session, "initial_town_mailbox_alarm"); err != nil {
		t.Fatalf("send initial town scene tail: %v", err)
	}
	packets := splitAllCurrentUpperPackets(t, session.conn.(*bufferConn).write.Bytes())
	alarmCount := 0
	alarmIndex := -1
	rentalIndex := -1
	for index, packet := range packets {
		if packet.Header.Classification != 0 {
			continue
		}
		if packet.Header.MsgID == currentRentalStateMsgID {
			rentalIndex = index
		}
		if packet.Header.MsgID == 0x0063 {
			alarmCount++
			alarmIndex = index
			if !bytes.Equal(packet.Body, []byte{0x01, 0x00}) {
				t.Fatalf("initial mailbox alarm body=% X, want 01 00", packet.Body)
			}
		}
	}
	if alarmCount != 1 || alarmIndex <= rentalIndex {
		t.Fatalf("initial mailbox alarm count=%d index=%d rental_index=%d", alarmCount, alarmIndex, rentalIndex)
	}

	firstWriteLen := session.conn.(*bufferConn).write.Len()
	if err := service.sendDeferredSelectSceneTail(session, "duplicate_initial_town_mailbox_alarm"); err != nil {
		t.Fatalf("repeat initial town scene tail: %v", err)
	}
	if session.conn.(*bufferConn).write.Len() != firstWriteLen {
		t.Fatalf("duplicate initial town scene tail replayed mailbox alarm")
	}
}

func TestMailboxSceneReadyResyncReadsCurrentCountAndRetries(t *testing.T) {
	service, session := newDeferredTailResumeFixture(t)
	repos, _ := service.repositoryGroup()
	save := func(count int) {
		t.Helper()
		record := dnfrepo.MailboxRecord{CharacterID: "29", Mails: map[string]dnfrepo.MailRecord{}}
		for i := 0; i < count; i++ {
			id := strconv.Itoa(i + 1)
			record.Mails[id] = dnfrepo.MailRecord{MailID: id, CreatedAt: time.Now().UTC()}
		}
		if err := repos.Mailbox.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	conn := session.conn.(*bufferConn)
	count := func(want int) {
		t.Helper()
		packets := splitAllCurrentUpperPackets(t, conn.write.Bytes())
		if len(packets) != 1 || packets[0].Header.MsgID != 99 || packets[0].Header.Classification != 0 || !bytes.Equal(packets[0].Body, []byte{byte(want), 0}) {
			t.Fatalf("alarm want=%d packets=%+v", want, packets)
		}
		conn.write.Reset()
	}
	save(1)
	if err := service.sendMailboxAlarmForSession(session, 29, "early_tail"); err != nil {
		t.Fatal(err)
	}
	count(1)
	session.setFinishLoadingGates(finishLoadingGates{})
	service.resyncMailboxAlarmAfterSceneReady(session, "not_ready")
	session.setFinishLoadingGateState(true)
	service.resyncMailboxAlarmAfterSceneReady(session, "state_only")
	if conn.write.Len() != 0 {
		t.Fatal("alarm before scene post-state")
	}
	save(2)
	session.setFinishLoadingGatePostFinish(true)
	session.conn = &titleEnchantFailConn{&bufferConn{}}
	service.resyncMailboxAlarmAfterSceneReady(session, "write_failure")
	if session.mailboxSceneAlarmSent {
		t.Fatal("failed write consumed resync")
	}
	session.conn = conn
	service.resyncMailboxAlarmAfterSceneReady(session, "retry")
	count(2)
	service.resyncMailboxAlarmAfterSceneReady(session, "duplicate")
	if conn.write.Len() != 0 {
		t.Fatal("duplicate scene alarm")
	}
	session.setFinishLoadingGates(finishLoadingGates{})
	save(0)
	session.setFinishLoadingGateState(true)
	session.setFinishLoadingGatePostFinish(true)
	service.resyncMailboxAlarmAfterSceneReady(session, "next_scene_empty")
	count(0)
}
