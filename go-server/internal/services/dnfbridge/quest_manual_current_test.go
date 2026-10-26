package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentQuestManualKeepsCompletedPrerequisitesWhenActiveListBecomesEmpty(t *testing.T) {
	s, session, repos := currentQuestManualFixture(t)
	ctx := context.Background()
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{100: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	dispatchCurrentQuestManual(t, s, session, false, 1, 1427, []byte{0, 0, 0, 0})
	session.conn.(*bufferConn).write.Reset()
	dispatchCurrentQuestManual(t, s, session, false, 1, 1454, nil)
	completed := map[int]bool{}
	sawEmpty := false
	sawAcceptable := false
	stream := session.conn.(*bufferConn).write.Bytes()
	for len(stream) > 0 {
		packet, next := splitGameServerUpperPacket(t, stream)
		stream = next
		switch packet.Header.MsgID {
		case currentActiveQuestSnapshotMsgID:
			if binary.LittleEndian.Uint32(packet.Body[:4]) == 0 {
				sawEmpty = true
				clear(completed)
			}
		case currentClearQuestListMsgID:
			plain, err := zlibDecompress(packet.Body[3:])
			if err != nil {
				t.Fatal(err)
			}
			for id, flag := range plain[4:] {
				if flag != 0 {
					completed[id] = true
				}
			}
		case currentAcceptableQuestListMsgID:
			sawAcceptable = true
			if !completed[100] {
				t.Fatal("native completed prerequisite lost before successor list")
			}
		}
	}
	if !sawEmpty || !sawAcceptable || !completed[100] {
		t.Fatal("empty active list erased completed prerequisite")
	}
}

func currentQuestManualFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	s, session, repos := newQuestClearCouponTestService(t, 1)
	s.options.currentClientVersion = "official-24.9"
	session.accountID = "dnf:1"
	session.loginPacketSeen = true
	session.accountFromToken = true
	s.bindGameSessionCharacter(session, 17)
	return s, session, repos
}

func TestCurrentQuestManualReleasesReservationBeforeProjection(t *testing.T) {
	s, session, _ := currentQuestManualFixture(t)
	dispatchCurrentQuestManual(t, s, session, false, 1, 1427, []byte{0, 0, 0, 0})
	written := false
	session.conn = &autoCompleteObserveWriteConn{bufferConn: &bufferConn{}, first: func() {
		s.mu.Lock()
		remaining := len(s.characterAssetReservations)
		s.mu.Unlock()
		if remaining != 0 {
			t.Fatal("manual reservation held during projection")
		}
		written = true
	}}
	dispatchCurrentQuestManual(t, s, session, false, 1, 1454, nil)
	if !written {
		t.Fatal("committed manual completion was not projected")
	}
}

func TestCurrentQuestManualRevalidatesAfterTransactionWait(t *testing.T) {
	s, session, repos := currentQuestManualFixture(t)
	dispatchCurrentQuestManual(t, s, session, false, 1, 1427, []byte{0, 0, 0, 0})
	session.conn.(*bufferConn).write.Reset()
	before, _, err := repos.Quest.Load(context.Background(), "17")
	if err != nil {
		t.Fatal(err)
	}
	replaced := false
	repos.CharacterSettlement = autoCompleteReplaceSettlement{CharacterSettlementUnitOfWork: repos.CharacterSettlement, before: func() {
		s.mu.Lock()
		s.gameSessions[17] = &gameSession{selectedCharacterID: 17, characterGeneration: session.characterGeneration + 1}
		s.mu.Unlock()
		replaced = true
	}}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	dispatchCurrentQuestManual(t, s, session, false, 1, 1454, nil)
	after, _, err := repos.Quest.Load(context.Background(), "17")
	if !replaced || err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("manual transaction used a stale binding", err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 || len(s.characterAssetReservations) != 0 {
		t.Fatal("stale manual projection or reservation leak")
	}
}

func dispatchCurrentQuestManual(t *testing.T, s *Service, session *gameSession, upper bool, class byte, opcode uint16, body []byte) {
	t.Helper()
	var err error
	if upper {
		frame, buildErr := dnfproto.BuildChannelPacket(opcode, body, 1, class)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		// The builder treats zero as its default command class; preserve
		// explicit class0 for the negative real-dispatch test.
		frame[0] = class
		err = s.handleGameUpper(session, frame)
	} else {
		err = s.handleGameCommand(session, class, opcode, body)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestCurrentQuestManualTwoStepRealDispatchNoRewardAndReplay(t *testing.T) {
	for _, upper := range []bool{false, true} {
		s, session, repos := currentQuestManualFixture(t)
		ctx := context.Background()
		beforeQuest, _, _ := repos.Quest.Load(ctx, "17")
		beforeCharacter, _, _ := repos.Character.Load(ctx, "17")
		beforeInventory, _, _ := repos.Inventory.Load(ctx, "17")
		dispatchCurrentQuestManual(t, s, session, upper, 1, 1427, []byte{0, 0, 0, 0})
		checked, _, _ := repos.Quest.Load(ctx, "17")
		if !reflect.DeepEqual(beforeQuest, checked) {
			t.Fatal("preflight mutated quests")
		}
		ack, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
		if ack.Header.MsgID != 1427 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1}) || len(rest) != 0 {
			t.Fatal("current native preflight requires one boolean", ack)
		}
		session.conn.(*bufferConn).write.Reset()
		dispatchCurrentQuestManual(t, s, session, upper, 1, 1454, nil)
		stored, _, _ := repos.Quest.Load(ctx, "17")
		for _, id := range []int64{100, 101, 106, 150} {
			if stored.States[id].Status != "completed" {
				t.Fatalf("quest %d not completed", id)
			}
		}
		if stored.States[102].Status != "active" {
			t.Fatal("level90 main story was swept")
		}
		afterCharacter, _, _ := repos.Character.Load(ctx, "17")
		afterInventory, _, _ := repos.Inventory.Load(ctx, "17")
		if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
			t.Fatal("free task button granted rewards or consumed inventory")
		}
		stream := session.conn.(*bufferConn).write.Bytes()
		if len(stream) == 0 {
			t.Fatal("committed quest snapshots absent")
		}
		for len(stream) > 0 {
			packet, next := splitGameServerUpperPacket(t, stream)
			stream = next
			if packet.Header.MsgID == 1429 || packet.Header.MsgID == 1454 {
				t.Fatal("unverified legacy ACK or unrelated native counter sent")
			}
		}
		session.conn.(*bufferConn).write.Reset()
		dispatchCurrentQuestManual(t, s, session, upper, 1, 1454, nil)
		replayed, _, _ := repos.Quest.Load(ctx, "17")
		if !reflect.DeepEqual(stored, replayed) || session.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("bodyless replay wrote state or packets")
		}
	}
}

func TestCurrentQuestManualRejectsMissingMalformedExpiredAndWrongOwner(t *testing.T) {
	for _, variant := range []string{"without_check", "expired", "stale", "unauthenticated", "wrong_account", "wrong_class", "malformed_check", "nonzero_selector", "malformed_commit"} {
		t.Run(variant, func(t *testing.T) {
			s, session, repos := currentQuestManualFixture(t)
			before, _, _ := repos.Quest.Load(context.Background(), "17")
			if variant != "without_check" && variant != "unauthenticated" && variant != "wrong_account" && variant != "wrong_class" && variant != "malformed_check" && variant != "nonzero_selector" {
				dispatchCurrentQuestManual(t, s, session, false, 1, 1427, []byte{0, 0, 0, 0})
			}
			switch variant {
			case "expired":
				session.questManualClearReadyAt = s.gameplayNow().Add(-2 * time.Minute)
			case "stale":
				s.bindGameSessionCharacter(&gameSession{conn: &bufferConn{}}, 17)
			case "unauthenticated":
				session.accountFromToken = false
				dispatchCurrentQuestManual(t, s, session, false, 1, 1427, []byte{0, 0, 0, 0})
			case "wrong_account":
				session.accountID = "dnf:2"
				dispatchCurrentQuestManual(t, s, session, false, 1, 1427, []byte{0, 0, 0, 0})
			case "wrong_class":
				dispatchCurrentQuestManual(t, s, session, true, 0, 1427, []byte{0, 0, 0, 0})
			case "malformed_check":
				dispatchCurrentQuestManual(t, s, session, false, 1, 1427, []byte{0})
			case "nonzero_selector":
				dispatchCurrentQuestManual(t, s, session, false, 1, 1427, []byte{100, 0, 0, 0})
			}
			body := []byte(nil)
			if variant == "malformed_commit" {
				body = []byte{0}
			}
			dispatchCurrentQuestManual(t, s, session, false, 1, 1454, body)
			after, _, _ := repos.Quest.Load(context.Background(), "17")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected two-step request mutated quest")
			}
		})
	}
}
