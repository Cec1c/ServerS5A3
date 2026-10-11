package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func seedOriginalAntonPrerequisite(t *testing.T, f *raidStartGuardFixture, id string, at time.Time) repo.QuestRecord {
	t.Helper()
	// Imported active state intentionally omits quest_type: current PVF must
	// still protect its counters from op33. This never writes the online DB.
	record := repo.QuestRecord{CharacterID: id, States: map[int64]repo.QuestState{12830: {Status: "active", ProgressValue: 5 | 5<<9, UpdatedAt: at.Add(-time.Hour)}}}
	if err := f.repos.Quest.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRealPVFOriginalAntonPrerequisiteFollowsFinalWeeklyReceiptAndSurvivesReconnect(t *testing.T) {
	f, session, state, now := onlineAntonTerminalFixture(t)
	before := seedOriginalAntonPrerequisite(t, f, "19", state.Battle.PhaseCompletedAt)
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("before weekly commit", err)
	}
	stored, _, _ := f.repos.Quest.Load(context.Background(), "19")
	if !reflect.DeepEqual(stored, before) {
		t.Fatal("uncommitted final phase credited")
	}
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = f.repos.Quest.Load(context.Background(), "19")
	if q := stored.States[12830]; q.ProgressValue != 5|4<<9 || q.Status != "active" || q.Extra["anton_prerequisite_instance_v1_1"] != state.Battle.InstanceID {
		t.Fatal("real phase receipt did not advance only the second original counter", q)
	}
	ack, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	if ack.Header.MsgID != 33 || len(ack.Body) != 7 || binary.LittleEndian.Uint16(ack.Body[1:3]) != 12830 || binary.LittleEndian.Uint32(ack.Body[3:7]) != 5|4<<9 {
		t.Fatal("postcommit quest projection", ack)
	}
	active, rest := splitGameServerUpperPacket(t, rest)
	if active.Header.MsgID != currentActiveQuestSnapshotMsgID {
		t.Fatal("missing authoritative active snapshot", active)
	}
	assertAntonResultStream(t, rest, 0, false)
	f.conn.write.Reset()
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil || f.conn.write.Len() != 0 {
		t.Fatal("same session replay", err)
	}
	replacement := f.session()
	replacement.loginPacketSeen, replacement.accountFromToken = true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
	after, _, _ := f.repos.Quest.Load(context.Background(), "19")
	if !reflect.DeepEqual(after, stored) {
		t.Fatal("reconnect double counted original task")
	}
}

func TestRealPVFOriginalAntonPrerequisiteFirstActualPhaseAdvancesOnlyFirstCounter(t *testing.T) {
	f, session, at := firstAntonFlowFixture(t)
	seedOriginalAntonPrerequisite(t, f, "19", at)
	state := cloneRuntimeRaidState(*f.state)
	if !state.Battle.PhaseCompleted || state.Battle.Phase != 0 || len(f.service.raidWeeklyIntents) != 0 {
		t.Fatal("actual first phase fixture unavailable")
	}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
		t.Fatal(err)
	}
	stored, _, _ := f.repos.Quest.Load(context.Background(), "19")
	if q := stored.States[12830]; q.ProgressValue != 4|5<<9 || q.Extra["anton_prerequisite_instance_v1_0"] != state.Battle.InstanceID || q.Extra["anton_prerequisite_day_v1_1"] != "" {
		t.Fatal("first actual phase mixed two objectives", q)
	}
	_, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
	_, rest = splitGameServerUpperPacket(t, rest)
	assertAntonResultStream(t, rest, 0, false)
}

func TestRealPVFOriginalAntonPrerequisitePartialSnapshotClosesAndReconnectDoesNotRecredit(t *testing.T) {
	f, session, state, now := onlineAntonTerminalFixture(t)
	seedOriginalAntonPrerequisite(t, f, "19", state.Battle.PhaseCompletedAt)
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	partial := &antonPartialWriteConn{Conn: session.conn}
	session.conn = partial
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err == nil || !partial.closed || !session.raidProjection.failed || session.raidProjection.resultSent {
		t.Fatal("partial quest snapshot could replay on its socket", err)
	}
	stored, _, _ := f.repos.Quest.Load(context.Background(), "19")
	if stored.States[12830].ProgressValue != 5|4<<9 {
		t.Fatal("transport failure undid the committed counter")
	}
	writes := partial.calls
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err == nil || partial.calls != writes {
		t.Fatal("corrupt projection retried")
	}
	f.conn.write.Reset()
	replacement := f.session()
	replacement.loginPacketSeen, replacement.accountFromToken = true, true
	f.service.bindGameSessionCharacter(replacement, 19)
	if err := f.service.sendCurrentAntonTerminalResult(replacement, state); err != nil {
		t.Fatal(err)
	}
	assertAntonResultStream(t, f.conn.write.Bytes(), 0, false)
	after, _, _ := f.repos.Quest.Load(context.Background(), "19")
	if !reflect.DeepEqual(after, stored) {
		t.Fatal("replacement connection counted twice")
	}
}

func TestRealPVFImportedRaidQuestOp33OnlyEchoesPVFAuthority(t *testing.T) {
	f, session, state, _ := onlineAntonTerminalFixture(t)
	before := seedOriginalAntonPrerequisite(t, f, "19", state.Battle.PhaseCompletedAt)
	for _, kind := range []byte{0, 16, 32, 64} {
		for _, increment := range []byte{0, 1} {
			f.conn.write.Reset()
			body := []byte{0x21, 0, 0x1e, 0x32, kind, increment}
			if err := f.service.handleCurrentSetQuestTrigger(session, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, f.conn.write.Bytes())
			if ack.Header.MsgID != 33 || len(rest) != 0 || binary.LittleEndian.Uint32(ack.Body[3:7]) != 5|5<<9 {
				t.Fatal("raid client trigger mutated or feedback loop emitted", ack, rest)
			}
			f.conn.write.Reset()
			if err := f.service.handleCurrentSetQuestTrigger(session, body); err != nil || f.conn.write.Len() != 0 {
				t.Fatal("raid echo replay", err)
			}
		}
	}
	after, _, _ := f.repos.Quest.Load(context.Background(), "19")
	if !reflect.DeepEqual(after, before) {
		t.Fatal("imported quest bypassed actual raid objective")
	}
}

type raidPrerequisiteLateFailure struct {
	base repo.CharacterSettlementUnitOfWork
}

func (r raidPrerequisiteLateFailure) WithinCharacterSettlement(ctx context.Context, id string, fn func(repo.Group) error) error {
	return r.base.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("injected prerequisite commit failure")
	})
}

func TestRealPVFOriginalAntonPrerequisiteRollbackBlocksPublicationAndRetryWorks(t *testing.T) {
	f, session, state, now := onlineAntonTerminalFixture(t)
	before := seedOriginalAntonPrerequisite(t, f, "19", state.Battle.PhaseCompletedAt)
	if done, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(done) != 1 || len(errs) != 0 {
		t.Fatal(done, errs)
	}
	base := f.repos.CharacterSettlement
	f.repos.CharacterSettlement = raidPrerequisiteLateFailure{base}
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err == nil || f.conn.write.Len() != 0 || session.raidProjection.resultSent {
		t.Fatal("failed quest commit published a result", err)
	}
	stored, _, _ := f.repos.Quest.Load(context.Background(), "19")
	if !reflect.DeepEqual(stored, before) {
		t.Fatal("late quest commit did not roll back")
	}
	f.repos.CharacterSettlement = base
	if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil || !session.raidProjection.resultSent {
		t.Fatal("same frozen result cannot retry", err)
	}
	stored, _, _ = f.repos.Quest.Load(context.Background(), "19")
	if stored.States[12830].ProgressValue != 5|4<<9 {
		t.Fatal("retry lost progress", stored)
	}
}

func TestRealPVFOriginalAntonPrerequisiteRejectsLukeSingleTimeoutAndIneligibleMember(t *testing.T) {
	for _, name := range []string{"luke", "single", "timeout", "not-completed", "observer"} {
		t.Run(name, func(t *testing.T) {
			f, session, state, now := onlineAntonTerminalFixture(t)
			before := seedOriginalAntonPrerequisite(t, f, "19", state.Battle.PhaseCompletedAt)
			switch name {
			case "luke":
				state.Battle.Mode = 2
			case "single":
				state.Battle.Single = true
			case "timeout":
				state.Battle.TimedOut = true
			case "not-completed":
				state.Battle.PhaseCompleted = false
			case "observer":
				before = seedOriginalAntonPrerequisite(t, f, "21", state.Battle.PhaseCompletedAt)
				if done, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(done) != 1 || len(errs) != 0 {
					t.Fatal(done, errs)
				}
				session.selectedCharacterID, session.accountID = 21, "account-21"
				f.service.bindGameSessionCharacter(session, 21)
				if err := f.service.sendCurrentAntonTerminalResult(session, state); err != nil {
					t.Fatal(err)
				}
				assertAntonResultStream(t, f.conn.write.Bytes(), 1, false)
			}
			if name != "observer" {
				if err := f.service.applyCurrentAntonPrerequisiteQuestResult(session, state); err != nil {
					t.Fatal(err)
				}
			}
			after, _, _ := f.repos.Quest.Load(context.Background(), before.CharacterID)
			if !reflect.DeepEqual(after, before) {
				t.Fatal("ineligible result advanced original prerequisite", name)
			}
		})
	}
}
