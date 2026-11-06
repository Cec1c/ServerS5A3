package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"longheng.io/server/internal/platform/eventloop"
)

func seedWeddingCancellationReplay(session *gameSession, characterID, questID uint16) {
	session.suppressCurrentAcceptQuestReplay(currentAcceptQuestReplayKey{characterID: characterID, questID: questID})
	session.suppressCurrentGiveUpQuestReplay(currentGiveUpQuestReplayKey{characterID: characterID, questID: questID})
	trigger := currentSetQuestTriggerReplayKey{characterID: characterID, questID: questID, triggerType: 1}
	session.suppressCurrentSetQuestTriggerReplay(trigger)
	session.markLinkedTriggerSent(trigger, 3)
	session.markCurrentFinishQuestAnswered(currentFinishQuestReplayKey{characterID: characterID, questID: questID})
}

func assertWeddingCancellationReplay(t *testing.T, session *gameSession, characterID, questID uint16, want bool) {
	t.Helper()
	trigger := currentSetQuestTriggerReplayKey{characterID: characterID, questID: questID, triggerType: 1}
	got := []bool{
		session.currentAcceptQuestReplaySuppressed(currentAcceptQuestReplayKey{characterID: characterID, questID: questID}),
		session.currentGiveUpQuestReplaySuppressed(currentGiveUpQuestReplayKey{characterID: characterID, questID: questID}),
		session.currentSetQuestTriggerReplaySuppressed(trigger),
		session.linkedTriggerAlreadySent(trigger, 3),
		session.currentFinishQuestAnswered(currentFinishQuestReplayKey{characterID: characterID, questID: questID}),
	}
	for i, actual := range got {
		if actual != want {
			t.Fatalf("replay family=%d character=%d quest=%d got=%t want=%t", i, characterID, questID, actual, want)
		}
	}
}

func blockWeddingCancellationTransport(t *testing.T, s *Service, session *gameSession, after func()) (func(), <-chan error) {
	t.Helper()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	go func() {
		done <- s.callGameSession(context.Background(), session, "old-wedding-quest-ack", func() error {
			close(entered)
			<-release
			if after != nil {
				after()
			}
			return nil
		})
	}()
	waitTestSignal(t, entered, "old quest event did not start")
	return unblock, done
}

func TestWeddingCurrentPVFCancellationOrdersLateQuestReceiptsAndAllowsSameSessionReengagementAccept(t *testing.T) {
	s, peers, repos := weddingCancelNPCFixture(t)
	aux := &gameSession{conn: &bufferConn{}, connID: "cancel-replay-aux", accountID: peers[1].accountID,
		selectedCharacterID: peers[1].selectedCharacterID, characterGeneration: peers[1].characterGeneration + 1, channel: peers[1].channel}
	s.registerLiveGameSession(aux)
	transports := append(append([]*gameSession{}, peers...), aux)
	for _, p := range transports {
		if err := s.startGameSessionEvents(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		defer stopTestGameSessionEvents(t, s, p)
		for _, qid := range []uint16{20737, 20738, 1234} {
			seedWeddingCancellationReplay(p, p.selectedCharacterID, qid)
		}
		seedWeddingCancellationReplay(p, 654, 20737)
	}
	// The partner's durable accept could have committed before cancellation,
	// while its receipt/mark is still in flight. A reserved event must follow it.
	unblock, oldDone := blockWeddingCancellationTransport(t, s, peers[1], func() {
		for _, qid := range []uint16{20737, 20738} {
			seedWeddingCancellationReplay(peers[1], peers[1].selectedCharacterID, qid)
		}
	})
	defer unblock()
	if err := s.callGameSession(context.Background(), peers[0], "cancel-engagement", func() error {
		return s.handleCurrentWeddingCancellation(peers[0], currentWeddingRequest{Operation: 5})
	}); err != nil {
		t.Fatal(err)
	}
	// The source does not wait for its blocked partner's event to finish.
	actor, _, _ := repos.Character.Load(context.Background(), fmt.Sprint(peers[0].selectedCharacterID))
	state, _ := wedding.Read(actor)
	if state.Phase != wedding.Single || actor.Stats["gold"] != 400000 {
		t.Fatalf("cancellation did not commit independently: %+v %v", state, actor.Stats)
	}
	assertWeddingCancellationReplay(t, peers[0], peers[0].selectedCharacterID, 20737, false)
	unblock()
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	for _, p := range transports {
		if err := s.callGameSession(context.Background(), p, "cancel-delivery-barrier", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		for _, qid := range []uint16{20737, 20738} {
			assertWeddingCancellationReplay(t, p, p.selectedCharacterID, qid, false)
		}
		assertWeddingCancellationReplay(t, p, p.selectedCharacterID, 1234, true)
		assertWeddingCancellationReplay(t, p, 654, 20737, true)
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Engage(context.Background(), wedding.PairRequest{
		Actor:   wedding.Actor{AccountID: peers[0].accountID, CharacterID: fmt.Sprint(peers[0].selectedCharacterID)},
		Partner: wedding.Actor{AccountID: peers[1].accountID, CharacterID: fmt.Sprint(peers[1].selectedCharacterID)}, Now: s.gameplayNow(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cargoReplies(t, peers[0])
	body := make([]byte, 4)
	binary.LittleEndian.PutUint16(body, 31)
	binary.LittleEndian.PutUint16(body[2:], 20737)
	if err := s.callGameSession(context.Background(), peers[0], "new-engagement-accept", func() error {
		sendCargoWire(t, s, peers[0], true, 31, 3, body)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	q, found, err := repos.Quest.Load(context.Background(), actor.CharacterID)
	ops, _ := cargoReplies(t, peers[0])
	if err != nil || !found || q.States[20737].Status != "active" || q.States[20737].ProgressValue != 5 || len(ops) == 0 || ops[0] != 31 {
		t.Fatalf("same-session reaccept was gated: quest=%+v found=%t err=%v ops=%v", q, found, err, ops)
	}
}

func TestWeddingCurrentPVFCancellationFullPartnerQueueRejectsBeforeMutationOrReplayClear(t *testing.T) {
	s, peers, repos := weddingCancelNPCFixture(t)
	p := peers[1]
	if err := s.startGameSessionEvents(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, s, p)
	unblock, oldDone := blockWeddingCancellationTransport(t, s, p, nil)
	defer unblock()
	for i := 0; i < gameSessionEventQueueSize; i++ {
		if err := s.tryPostGameSessionCharacterEvent(p, "fill-cancel-test", p.selectedCharacterID, p.characterGeneration, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.tryPostGameSessionCharacterEvent(p, "confirm-full", p.selectedCharacterID, p.characterGeneration, func() error { return nil }); !errors.Is(err, eventloop.ErrQueueFull) {
		t.Fatalf("queue not full: %v", err)
	}
	before := make([]r.CharacterRecord, 2)
	quests := make([]r.QuestRecord, 2)
	for i, peer := range peers {
		id := fmt.Sprint(peer.selectedCharacterID)
		before[i], _, _ = repos.Character.Load(context.Background(), id)
		quests[i], _, _ = repos.Quest.Load(context.Background(), id)
		for _, qid := range []uint16{20737, 20738} {
			seedWeddingCancellationReplay(peer, peer.selectedCharacterID, qid)
		}
	}
	if err := s.handleCurrentWeddingCancellation(peers[0], currentWeddingRequest{Operation: 5}); err != nil {
		t.Fatal(err)
	}
	ops, bodies := cargoReplies(t, peers[0])
	if len(ops) != 1 || ops[0] != 1032 || int32(binary.LittleEndian.Uint32(bodies[0][1:])) != -1 {
		t.Fatalf("queue rejection ACK %v %x", ops, bodies)
	}
	for i, peer := range peers {
		after, _, _ := repos.Character.Load(context.Background(), before[i].CharacterID)
		q, _, _ := repos.Quest.Load(context.Background(), before[i].CharacterID)
		if !reflect.DeepEqual(before[i], after) || !reflect.DeepEqual(quests[i], q) {
			t.Fatal("full queue changed fee, relationship or quest state")
		}
		for _, qid := range []uint16{20737, 20738} {
			assertWeddingCancellationReplay(t, peer, peer.selectedCharacterID, qid, true)
		}
	}
	unblock()
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
}

func TestWeddingCancellationReplayClearRejectsChangedTransportIdentity(t *testing.T) {
	for _, change := range []string{"generation", "account", "canonical"} {
		t.Run(change, func(t *testing.T) {
			s, peers, _, _ := weddingCancellationProjectionFixture(t)
			p := peers[0]
			pin, valid := s.currentWeddingClearTransport(p, p.accountID)
			if !valid {
				t.Fatal("unbound test owner")
			}
			seedWeddingCancellationReplay(p, p.selectedCharacterID, 20737)
			switch change {
			case "generation":
				p.characterGeneration++
			case "account":
				p.accountID = "changed"
			case "canonical":
				delete(s.gameSessions, p.selectedCharacterID)
			}
			if s.clearCurrentWeddingPreparationReplayForTransport(pin) {
				t.Fatal("retired owner cache changed")
			}
			assertWeddingCancellationReplay(t, p, p.selectedCharacterID, 20737, true)
		})
	}
}
