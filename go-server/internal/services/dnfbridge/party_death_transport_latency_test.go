package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func holdPartyDeathRecipient(t *testing.T, s *Service, peer *gameSession, mutate func()) func() {
	t.Helper()
	if err := s.startGameSessionEvents(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestGameSessionEvents(t, s, peer) })
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unlock)
	if err := s.tryPostGameSessionCharacterEvent(peer, "test-death-recipient-gate", peer.selectedCharacterID, peer.characterGeneration, func() error {
		close(entered)
		<-release
		if mutate != nil {
			mutate()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitTestSignal(t, entered, "recipient owner gate")
	return unlock
}

func drainPartyDeathRecipient(t *testing.T, s *Service, peer *gameSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		peer.partyDeaths.mu.Lock()
		empty := len(peer.partyDeaths.items) == 0 && !peer.partyDeaths.queued
		peer.partyDeaths.mu.Unlock()
		if empty {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("death mailbox did not drain")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// The death callback can enqueue material delivery behind the first marker.
	for i := 0; i < 2; i++ {
		if err := s.callGameSession(ctx, peer, "test-death-drained", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPartyDeathQueuedDeliveryRejectsRetiredScene(t *testing.T) {
	for _, kind := range []string{"character", "room", "instance", "departure", "returning", "transport"} {
		t.Run(kind, func(t *testing.T) {
			s, leader, member, runtime, _ := partyDungeonDeathFixture(t, 3)
			conn := member.conn.(*bufferConn)
			conn.write.Reset()
			release := holdPartyDeathRecipient(t, s, member, func() {
				member.dungeon.mu.Lock()
				defer member.dungeon.mu.Unlock()
				switch kind {
				case "character":
					s.mu.Lock()
					advanceGameSessionCharacterGeneration(member)
					s.mu.Unlock()
				case "room":
					member.dungeon.runtime.Room = &runtimeDungeonRoom{}
				case "instance":
					member.dungeon.runtime.partyInstance = &dungeonPartyInstance{}
				case "departure":
					runtime.partyInstance.mu.Lock()
					delete(runtime.partyInstance.members, member.selectedCharacterID)
					runtime.partyInstance.mu.Unlock()
				case "returning":
					member.dungeon.runtime.townReturnPending = true
				case "transport":
					member.closeGameTransport()
				}
			})
			key := runtime.Room.Snapshot().Monsters[0].ObjectKey
			body, err := buildCurrentDungeonDeathNotificationBody(key, currentDungeonDeathResponseMonster)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.broadcastPartyDungeonInstanceDeathNotification(leader, runtime, key, body, "test"); got != 1 {
				t.Fatalf("queued=%d", got)
			}
			release()
			drainPartyDeathRecipient(t, s, member)
			if conn.write.Len() != 0 {
				t.Fatalf("retired %s received %d bytes", kind, conn.write.Len())
			}
		})
	}
}

func TestPartyDeathQueuedBodiesOwnBytesAndKeepOrder(t *testing.T) {
	s, leader, member, runtime, _ := partyDungeonDeathFixture(t, 3)
	conn := member.conn.(*bufferConn)
	conn.write.Reset()
	release := holdPartyDeathRecipient(t, s, member, nil)
	var expected [][]byte
	for _, monster := range runtime.Room.Snapshot().Monsters[:2] {
		body, err := buildCurrentDungeonDeathNotificationBody(monster.ObjectKey, currentDungeonDeathResponseMonster)
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, append([]byte(nil), partyDungeonDeathBodyForRecipient(runtime, body, currentSceneActorObjectKey(member.selectedCharacterID))...))
		if got := s.broadcastPartyDungeonInstanceDeathNotification(leader, runtime, monster.ObjectKey, body, "test"); got != 1 {
			t.Fatalf("queued=%d", got)
		}
		for i := range body {
			body[i] = 0xff
		}
	}
	release()
	drainPartyDeathRecipient(t, s, member)
	frames := splitUpperFrames(t, conn.write.Bytes())
	if len(frames) != 2 {
		t.Fatalf("frames=%d want 2", len(frames))
	}
	for i, f := range frames {
		if f.msgID != 38 || !bytes.Equal(f.body, expected[i]) {
			t.Fatalf("death %d changed/order differs: %+v", i, f)
		}
	}
}

func TestPartyDeathFullOwnerQueueRetainsNotification(t *testing.T) {
	s, leader, member, runtime, _ := partyDungeonDeathFixture(t, 3)
	release := holdPartyDeathRecipient(t, s, member, nil)
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := s.tryPostGameSessionCharacterEvent(member, "test-filled-queue", member.selectedCharacterID, member.characterGeneration, func() error { return nil }); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	key := runtime.Room.Snapshot().Monsters[0].ObjectKey
	body, err := buildCurrentDungeonDeathNotificationBody(key, currentDungeonDeathResponseMonster)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if got := s.broadcastPartyDungeonInstanceDeathNotification(leader, runtime, key, body, "test"); got != 1 {
		t.Fatalf("queued=%d", got)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("full recipient queue blocked source")
	}
	if member.gameTransportContext().Err() != nil {
		t.Fatal("temporarily full owner queue retired healthy recipient")
	}
	if leader.gameTransportContext().Err() != nil {
		t.Fatal("healthy killer was retired")
	}
	release()
	drainPartyDeathRecipient(t, s, member)
	frames := splitUpperFrames(t, member.conn.(*bufferConn).write.Bytes())
	if len(frames) != 1 || frames[0].msgID != 38 {
		t.Fatalf("retained death missing or duplicated: %d frames", len(frames))
	}
}

func TestPartyDeathMailboxBacklogIsBounded(t *testing.T) {
	for _, byBytes := range []bool{false, true} {
		t.Run(fmt.Sprint(byBytes), func(t *testing.T) {
			s, leader, member, runtime, _ := partyDungeonDeathFixture(t, 3)
			release := holdPartyDeathRecipient(t, s, member, nil)
			key := runtime.Room.Snapshot().Monsters[0].ObjectKey
			body, err := buildCurrentDungeonDeathNotificationBody(key, currentDungeonDeathResponseMonster)
			if err != nil {
				t.Fatal(err)
			}
			n := partyDeathPendingLimit
			if byBytes {
				body = make([]byte, partyDeathPendingBytes/2+1)
				n = 1
			}
			for i := 0; i < n; i++ {
				if s.broadcastPartyDungeonInstanceDeathNotification(leader, runtime, key, body, "test") != 1 {
					t.Fatalf("not admitted at %d", i)
				}
			}
			if s.broadcastPartyDungeonInstanceDeathNotification(leader, runtime, key, body, "test") != 0 {
				t.Fatal("unbounded critical backlog")
			}
			if member.gameTransportContext().Err() == nil || leader.gameTransportContext().Err() != nil {
				t.Fatal("wrong backpressure recipient")
			}
			release()
			drainPartyDeathRecipient(t, s, member)
		})
	}
}

func TestPartyDeathNewGenerationReplacesOldPendingMailbox(t *testing.T) {
	s, leader, member, runtime, _ := partyDungeonDeathFixture(t, 3)
	key := runtime.Room.Snapshot().Monsters[0].ObjectKey
	body, err := buildCurrentDungeonDeathNotificationBody(key, currentDungeonDeathResponseMonster)
	if err != nil {
		t.Fatal(err)
	}
	accepted := false
	release := holdPartyDeathRecipient(t, s, member, func() {
		s.mu.Lock()
		advanceGameSessionCharacterGeneration(member)
		s.mu.Unlock()
		accepted = s.broadcastPartyDungeonInstanceDeathNotification(leader, runtime, key, body, "new-generation") == 1
	})
	for i := 0; i < partyDeathPendingLimit; i++ {
		s.broadcastPartyDungeonInstanceDeathNotification(leader, runtime, key, body, "old-generation")
	}
	release()
	drainPartyDeathRecipient(t, s, member)
	if !accepted || member.gameTransportContext().Err() != nil {
		t.Fatal("old backlog affected new generation")
	}
	frames := splitUpperFrames(t, member.conn.(*bufferConn).write.Bytes())
	if len(frames) != 1 || frames[0].msgID != 38 {
		t.Fatalf("old generation emitted frames: %d", len(frames))
	}
}

// An unread recipient owns a real transport write while the killer processes a
// genuine death request. The source must not inherit that recipient's deadline.
func TestPartyDeathSlowRecipientDoesNotBlockKiller(t *testing.T) {
	s, leader, member, runtime, _ := partyDungeonDeathFixture(t, 2)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	conn := &transportTestConn{Conn: left, started: make(chan struct{})}
	member.conn = conn
	if err := s.startGameSessionEvents(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, member)
	first := make(chan error, 1)
	go func() {
		first <- s.callGameSession(context.Background(), member, "test-unread-peer", func() error {
			return s.sendGameUpperRawClass(member, 1297, []byte{1}, 0)
		})
	}()
	waitTestSignal(t, conn.started, "recipient write did not start")
	defer func() { member.closeGameTransport(); <-first }()
	monster := runtime.Room.Snapshot().Monsters[0]
	request := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(request, monster.ObjectKey)
	binary.LittleEndian.PutUint16(request[4:], leader.selectedCharacterID)
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- s.handleDungeonMonsterDeath(leader, request) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("killer completed in %s with unread recipient", time.Since(started))
	case <-time.After(250 * time.Millisecond):
		member.closeGameTransport()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("killer did not release after peer close")
		}
		t.Fatalf("killer blocked by recipient wire for at least %s", time.Since(started))
	}
}

func TestPartyDeathOwnerQueueKeepsCommittedExperienceIdempotent(t *testing.T) {
	s, leader, member := partyMonsterExperienceFixture(t)
	for _, p := range []*gameSession{leader, member} {
		if err := s.startGameSessionEvents(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		defer stopTestGameSessionEvents(t, s, p)
	}
	runtime := leader.dungeon.runtime
	monster := runtime.Room.Snapshot().Monsters[0]
	resources, err := s.currentDungeonExperienceCommitResources()
	if err != nil {
		t.Fatal(err)
	}
	award, err := currentDungeonMonsterExperienceAwardFor(runtime, monster, resources.Sources)
	if err != nil {
		t.Fatal(err)
	}
	request := make([]byte, dungeoncmd.DieMonsterRequestSize)
	binary.LittleEndian.PutUint32(request, monster.ObjectKey)
	binary.LittleEndian.PutUint16(request[4:], leader.selectedCharacterID)
	if err := s.callGameSession(context.Background(), leader, "test-actual-kill", func() error { return s.handleDungeonMonsterDeath(leader, request) }); err != nil {
		t.Fatal(err)
	}
	drainPartyDeathRecipient(t, s, member)
	binary.LittleEndian.PutUint16(request[4:], member.selectedCharacterID)
	if err := s.callGameSession(context.Background(), member, "test-duplicate-kill", func() error { return s.handleDungeonMonsterDeath(member, request) }); err != nil {
		t.Fatal(err)
	}
	repos, _ := s.repositoryGroup()
	for _, p := range []*gameSession{leader, member} {
		c, found, err := repos.Character.Load(context.Background(), strconv.Itoa(int(p.selectedCharacterID)))
		if err != nil || !found || c.Stats["exp"] != int64(award.Gain) {
			t.Fatalf("recipient %d experience=%d expected=%d err=%v", p.selectedCharacterID, c.Stats["exp"], award.Gain, err)
		}
		var death, experience bool
		for _, f := range splitUpperFrames(t, p.conn.(*bufferConn).write.Bytes()) {
			death = death || f.msgID == 38
			experience = experience || f.msgID == currentDungeonCharacterStateMsgID
		}
		if !death || !experience {
			t.Fatalf("recipient %d death=%v experience=%v", p.selectedCharacterID, death, experience)
		}
	}
}
