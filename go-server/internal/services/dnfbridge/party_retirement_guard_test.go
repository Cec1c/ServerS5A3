package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/party"
)

func retireLeaderExitPartyForGuardTest(t *testing.T, f leaderExitFixtureState) party.RuntimePartySnapshot {
	t.Helper()
	manager := f.service.runtimePartyManagerForService()
	retired, ok := manager.SnapshotByUser(f.member.selectedCharacterID, 0)
	if !ok {
		t.Fatal("member party membership is missing")
	}
	if !manager.RetireSnapshot(retired) {
		t.Fatal("exact fixture party did not retire")
	}
	for _, characterID := range []uint16{f.leader.selectedCharacterID, f.member.selectedCharacterID} {
		if current, found := manager.SnapshotByUser(characterID, 0); found {
			t.Fatalf("character %d is still in manager party %d", characterID, current.ID)
		}
	}
	return retired
}

func createReplacementPartyForGuardTest(t *testing.T, manager *party.RuntimePartyManager, retired party.RuntimePartySnapshot) party.RuntimePartySnapshot {
	t.Helper()
	var leader, member party.RuntimePartyMember
	for _, candidate := range retired.Members {
		switch candidate.UserID {
		case 99:
			leader = candidate
		case 100:
			member = candidate
		}
	}
	if leader.UserID == 0 || member.UserID == 0 {
		t.Fatalf("retired party members=%+v; want characters 99 and 100", retired.Members)
	}
	created := manager.Create(leader, retired.Settings)
	if !created.OK {
		t.Fatalf("create replacement party: %s", created.Reason)
	}
	joined := manager.Join(created.Party.ID, member)
	if !joined.OK {
		t.Fatalf("join replacement party: %s", joined.Reason)
	}
	if joined.Party.ID == retired.ID {
		t.Fatalf("replacement party reused retired ID %d", retired.ID)
	}
	return joined.Party
}

func TestPartyRetirementGuardSendsOp9ForRetiredAbsentReceiver(t *testing.T) {
	f := newLeaderExitFixture(t)
	retireLeaderExitPartyForGuardTest(t, f)

	guard := f.service.currentPartyRetirementGuard(f.member, nil)
	if !guard() {
		t.Fatal("retired absent receiver should have a valid retirement guard")
	}
	f.memberConn.write.Reset()
	body := buildCurrentSceneOp9ActorRemovalBody(f.leader.selectedCharacterID)
	sent, err := f.service.sendRetiredPartyPacket(f.member, 9, body, nil, guard, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Fatal("valid retired op9 was not sent")
	}
	frames := splitUpperFrames(t, f.memberConn.write.Bytes())
	if len(frames) != 1 || frames[0].msgID != 9 || len(frames[0].body) == 0 || !bytes.Equal(frames[0].body, body) {
		t.Fatalf("retired receiver frames=%+v want one non-empty op9 body=%x", frames, body)
	}
}

func TestPartyRetirementGuardRejectsReplacementManagerMembership(t *testing.T) {
	f := newLeaderExitFixture(t)
	retired := retireLeaderExitPartyForGuardTest(t, f)
	guard := f.service.currentPartyRetirementGuard(f.member, nil)
	if !guard() {
		t.Fatal("retirement guard must be live before the replacement membership")
	}

	createReplacementPartyForGuardTest(t, f.service.runtimePartyManagerForService(), retired)
	f.memberConn.write.Reset()
	sent, err := f.service.sendRetiredPartyPacket(
		f.member,
		9,
		buildCurrentSceneOp9ActorRemovalBody(f.leader.selectedCharacterID),
		nil,
		guard,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if sent || f.memberConn.write.Len() != 0 {
		t.Fatalf("stale guard sent into replacement membership: sent=%t bytes=%x", sent, f.memberConn.write.Bytes())
	}
}

func TestPartyRetirementGuardRejectsCharacterGenerationChange(t *testing.T) {
	f := newLeaderExitFixture(t)
	before, ok := f.service.runtimePartyManagerForService().SnapshotByUser(f.member.selectedCharacterID, 0)
	if !ok {
		t.Fatal("member party membership is missing")
	}
	expected := before.StateFor(f.member.selectedCharacterID)
	guard := f.service.currentPartyRetirementGuard(f.member, &expected)
	if !guard() {
		t.Fatal("retained membership should have a valid retirement guard")
	}

	f.service.mu.Lock()
	f.member.characterGeneration++
	f.service.mu.Unlock()
	f.memberConn.write.Reset()
	sent, err := f.service.sendRetiredPartyPacket(
		f.member,
		9,
		buildCurrentSceneOp9ActorRemovalBody(f.leader.selectedCharacterID),
		&expected,
		guard,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if sent || f.memberConn.write.Len() != 0 {
		t.Fatalf("stale character generation sent: sent=%t bytes=%x", sent, f.memberConn.write.Bytes())
	}
}

func TestPartyRetirementGuardRejectsDungeonRunSeedChange(t *testing.T) {
	f := newLeaderExitFixture(t)
	before, ok := f.service.runtimePartyManagerForService().SnapshotByUser(f.member.selectedCharacterID, 0)
	if !ok {
		t.Fatal("member party membership is missing")
	}
	runBefore, valid := f.service.currentDungeonPartyRunKey(f.member)
	if !valid {
		t.Fatal("fixture member has no active dungeon run key")
	}
	expected := before.StateFor(f.member.selectedCharacterID)
	guard := f.service.currentPartyRetirementGuard(f.member, &expected)
	if !guard() {
		t.Fatal("retained run should have a valid retirement guard")
	}

	f.member.dungeonPartyRunMu.Lock()
	f.member.dungeonPartyRun.Seed++
	changedRun := f.member.dungeonPartyRun
	f.member.dungeonPartyRunMu.Unlock()
	if changedRun.Seed == runBefore.Seed {
		t.Fatal("dungeon run seed did not change")
	}
	f.memberConn.write.Reset()
	sent, err := f.service.sendRetiredPartyPacket(
		f.member,
		9,
		buildCurrentSceneOp9ActorRemovalBody(f.leader.selectedCharacterID),
		&expected,
		guard,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if sent || f.memberConn.write.Len() != 0 {
		t.Fatalf("stale dungeon run seed sent: sent=%t bytes=%x", sent, f.memberConn.write.Bytes())
	}
}
