package dnfbridge

// This regression uses synthetic characters 99/100 and existing in-memory
// fixtures. Client movement is outside this server lifecycle test's scope.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	dnfpet "longheng.io/server/internal/modules/dnf/pet"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// TestLeaderHandoffAfterCompletedRunKeepsPartyAndFixedSlots covers the missing
// two-round chain:
//
//	accept -> real op16(700) -> both real op37 -> completed return ->
//	old leader selects slot 1 -> new leader real op16(700) -> both real op37.
//
// Finish-loading errors are hard failures. The loading dispatcher is made
// synchronous only so the test can inspect the exact post-barrier state; it
// does not inject readiness, force-release the cycle, or replace op16/op37.
func TestLeaderHandoffAfterCompletedRunKeepsPartyAndFixedSlots(t *testing.T) {
	f := newLeaderHandoffRoundtripFixture(t)
	f.formPartyThroughClientAccept(t)

	oldLeader := f.leader
	newLeader := f.follower
	formed := leaderHandoffRequireParty(t, f.service, oldLeader, inviteAcceptLeaderCharacterID, 0)
	partyID := uint16(formed.PartyID)

	// First run: the present leader really selects dungeon 700.
	f.selectDungeon(t, oldLeader, 700)
	leaderHandoffRequireHeldForLoading(t, f.service, partyID, oldLeader, newLeader)
	f.finishLeaderHandoffLoading(t, oldLeader, newLeader)
	leaderHandoffRequireLoadingReleased(t, f.service, partyID, oldLeader, newLeader)
	oldInstance := leaderHandoffSharedInstance(t, oldLeader, newLeader)

	// Return each member through the existing completed-run helper semantics.
	// Both are complete independently; only after the second return may the old
	// shared instance disappear.
	returnFixture := completionReturnFixture{service: f.service}
	oldLeaderRuntime := leaderHandoffRequireRuntime(t, oldLeader, "first completed return")
	newLeaderRuntime := leaderHandoffRequireRuntime(t, newLeader, "first completed return")
	returnFixture.completeRunAndReturnToTown(t, oldLeader, oldLeaderRuntime)
	if got := snapshotPartyDungeonLoadingOwner(oldLeader).runtime; got != nil {
		t.Fatalf("old leader runtime was not detached after completed return: got=%p", got)
	}
	if got := oldInstance.memberCount(); got != 1 {
		t.Fatalf("old instance member count after the first completed return=%d want 1", got)
	}
	returnFixture.completeRunAndReturnToTown(t, newLeader, newLeaderRuntime)
	if got := snapshotPartyDungeonLoadingOwner(newLeader).runtime; got != nil {
		t.Fatalf("new leader runtime was not detached after completed return: got=%p", got)
	}
	if got := oldInstance.memberCount(); got != 0 {
		t.Fatalf("old instance member count after both completed returns=%d want 0", got)
	}
	if got := f.service.partyDungeonInstanceCount(); got != 0 {
		t.Fatalf("party dungeon instance count after both returns=%d want 0", got)
	}

	// Both real client position reports restore the town selector origin used by
	// the next op16. This is the same setup convention as the completion tests.
	bindDungeonSelectorOriginForTestAt(t, f.service, oldLeader, 38, 1, 450, 234)
	bindDungeonSelectorOriginForTestAt(t, f.service, newLeader, 38, 1, 450, 234)
	leaderHandoffRequireParty(t, f.service, oldLeader, inviteAcceptLeaderCharacterID, partyID)

	// Slot 1 belongs to synthetic char 100 before and after the transfer.
	if err := f.service.handleOnlineChangePartyHost(oldLeader, []byte{1}); err != nil {
		t.Fatalf("change host through selected slot 1: %v", err)
	}
	leaderHandoffRequireParty(t, f.service, oldLeader, inviteAcceptFollowerCharacterID, partyID)

	// Second run: the newly authoritative leader really selects dungeon 700.
	f.selectDungeon(t, newLeader, 700)
	log := testPacketLogText(t, f.service)
	matchedLeader := false
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "game-party-dungeon-entry-prepared") &&
			strings.Contains(line, "leader_char_id=100 ") {
			matchedLeader = true
			if !strings.Contains(line, "leader_slot=1 ") {
				t.Fatalf("delegated leader diagnostic does not preserve fixed slot: %s", line)
			}
		}
	}
	if !matchedLeader {
		t.Fatal("second entry has no delegated leader diagnostic")
	}
	leaderHandoffRequireHeldForLoading(t, f.service, partyID, oldLeader, newLeader)
	f.finishLeaderHandoffLoading(t, oldLeader, newLeader)
	leaderHandoffRequireLoadingReleased(t, f.service, partyID, oldLeader, newLeader)
	newInstance := leaderHandoffSharedInstance(t, oldLeader, newLeader)
	if newInstance == oldInstance {
		t.Fatalf("second run reused the retired shared instance %p", oldInstance)
	}
	if got := f.service.partyDungeonInstanceCount(); got != 1 {
		t.Fatalf("party dungeon instance count on the second run=%d want 1", got)
	}

	// The re-entry must preserve the party identity, the transferred leader and
	// the two original fixed slot assignments.
	leaderHandoffRequireParty(t, f.service, oldLeader, inviteAcceptFollowerCharacterID, partyID)
}

// newLeaderHandoffRoundtripFixture extends the public invite/accept fixture with
// only dependencies already supplied by existing completions and A01 loading
// tests. Missing stores/catalogs fail explicitly; no PVF or DB source is
// invented here.
func newLeaderHandoffRoundtripFixture(t *testing.T) *inviteAcceptDungeonFixture {
	t.Helper()
	f := newInviteAcceptDungeonFixture(t)
	service := f.service
	prepareTestCharacterInitialization(service, 15)
	service.premiumCatalog = &currentPremiumCatalog{
		contractsByItem: make(map[int64]currentPremiumContractInfo),
		devilSlots:      make(map[uint32]currentPremiumDevilSlotInfo),
		crystalCubeIDs:  [6]int64{3033, 3034, 3035, 3036, 3037, 3262},
	}
	service.adventureGroupTable = loadAdventureGroupTestTables(t)
	items, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "",
		"equipment/equipment.lst": "",
	})
	if err != nil {
		t.Fatalf("explicit empty test item catalog: %v", err)
	}
	service.pvfItemCatalog = items
	service.options.currentClientVersion = "official-24.9"
	service.options.partyDungeonEntryBarrier = partyDungeonEntryBarrierVendor
	service.options.partyDungeonLoadingBarrier = partyDungeonLoadingBarrierVendor
	service.options.partyDungeonOwner = partyDungeonOwnerSceneContext
	t.Setenv(dungeonPartyPeerActorLandingResendEnv, "on")

	repositories, ok := service.repositoryGroup()
	if !ok || repositories.Account == nil || repositories.Character == nil ||
		repositories.Inventory == nil || repositories.Skill == nil {
		t.Fatal("leader-handoff fixture prerequisite missing: complete memory account/character/inventory/skill repositories")
	}
	ctx := context.Background()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "account-1",
		State:     "active",
		Metadata:  map[string]string{currentRentalPointMetadataKey: "0"},
	}); err != nil {
		t.Fatalf("prepare synthetic account: %v", err)
	}
	for _, id := range []string{
		fmt.Sprint(inviteAcceptLeaderCharacterID),
		fmt.Sprint(inviteAcceptFollowerCharacterID),
	} {
		character, found, loadErr := repositories.Character.Load(ctx, id)
		if loadErr != nil || !found {
			t.Fatalf("leader-handoff fixture character %s prerequisite: found=%t err=%v", id, found, loadErr)
		}
		character.Job = "15"
		character.Name = "synthetic-" + id
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatalf("save synthetic character %s: %v", id, err)
		}
		// Existing fixture: supplies the real job15 skill projection used by
		// post-finish loading and completed town actor construction.
		enableTownMoveSkillProjection(t, service, repositories, id)
	}

	pets, err := dnfpet.NewPVFCatalog(bridgePetCatalogFixture())
	if err != nil {
		t.Fatalf("existing pet catalog fixture: %v", err)
	}
	service.petCatalog = pets

	// Test-only synchronization: production dispatches member releases in
	// parallel. Running the same callbacks inline lets this regression inspect
	// both cycles deterministically after both real op37 packets have arrived.
	previousDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	t.Cleanup(func() {
		service.partyDungeonLoadingMu.Lock()
		for _, cycle := range service.partyDungeonLoadingCycles {
			if cycle != nil && cycle.Timer != nil {
				cycle.Timer.Stop()
			}
		}
		service.partyDungeonLoadingMu.Unlock()
		partyDungeonLoadingBarrierDispatch = previousDispatch
	})
	return f
}

// finishLeaderHandoffLoading submits the real legacy op37 body for both members.
// Unlike releaseSharedLoadingBarrier in the older fixture, every returned error
// is fatal; this test must not pass with one-sided or partial loading.
func (f *inviteAcceptDungeonFixture) finishLeaderHandoffLoading(t *testing.T, sessions ...*gameSession) {
	t.Helper()
	for _, session := range sessions {
		if err := f.service.sendFinishLoadingStatus(
			session,
			make([]byte, currentFinishLoadingLegacyRequestBodySize),
		); err != nil {
			t.Fatalf("real op37 finish loading for synthetic char %d: %v", session.selectedCharacterID, err)
		}
	}
}

func leaderHandoffRequireRuntime(t *testing.T, session *gameSession, stage string) *runtimeDungeonState {
	t.Helper()
	owner := snapshotPartyDungeonLoadingOwner(session)
	if owner.runtime == nil {
		t.Fatalf("synthetic char %d has no runtime at %s", session.selectedCharacterID, stage)
	}
	return owner.runtime
}

func leaderHandoffRequireHeldForLoading(t *testing.T, service *Service, partyID uint16, sessions ...*gameSession) {
	t.Helper()
	for _, session := range sessions {
		if !service.partyDungeonPostStartMapPlacementPending(session) {
			t.Fatalf("synthetic char %d was not held for real op37 in party %d", session.selectedCharacterID, partyID)
		}
		if gates := session.finishLoadingGateSnapshot(); gates != (finishLoadingGates{}) {
			t.Fatalf("synthetic char %d entered loading with preset gates: %+v", session.selectedCharacterID, gates)
		}
	}
}

func leaderHandoffRequireLoadingReleased(t *testing.T, service *Service, partyID uint16, sessions ...*gameSession) {
	t.Helper()
	for _, session := range sessions {
		gates := session.finishLoadingGateSnapshot()
		if !gates.State || !gates.Completion || !gates.PostFinish ||
			service.partyDungeonPostStartMapPlacementPending(session) {
			t.Fatalf("synthetic char %d loading was not released: gates=%+v pending=%t",
				session.selectedCharacterID,
				gates,
				service.partyDungeonPostStartMapPlacementPending(session))
		}
	}

	service.partyDungeonLoadingMu.Lock()
	cycle := service.partyDungeonLoadingCycles[partyID]
	service.partyDungeonLoadingMu.Unlock()
	if cycle == nil || !cycle.Released || len(cycle.membersAt) != 2 {
		t.Fatalf("party %d loading cycle was not released for both members: %+v", partyID, cycle)
	}
	for _, session := range sessions {
		if !cycle.Ready[session.selectedCharacterID] {
			t.Fatalf("party %d loading cycle is missing ready synthetic char %d: %+v", partyID, session.selectedCharacterID, cycle)
		}
	}
}

func leaderHandoffSharedInstance(t *testing.T, sessions ...*gameSession) *dungeonPartyInstance {
	t.Helper()
	var instance *dungeonPartyInstance
	for _, session := range sessions {
		runtime := leaderHandoffRequireRuntime(t, session, "shared instance assertion")
		if runtime.partyInstance == nil {
			t.Fatalf("synthetic char %d runtime has no shared party instance", session.selectedCharacterID)
		}
		if instance == nil {
			instance = runtime.partyInstance
			continue
		}
		if runtime.partyInstance != instance {
			t.Fatalf("synthetic char %d uses %p; peer uses %p", session.selectedCharacterID, runtime.partyInstance, instance)
		}
	}
	if instance == nil {
		t.Fatal("shared party instance is nil")
	}
	if got := instance.memberCount(); got != len(sessions) {
		t.Fatalf("shared party instance=%p memberCount=%d want=%d", instance, got, len(sessions))
	}
	return instance
}

func leaderHandoffRequireParty(
	t *testing.T,
	service *Service,
	anyMember *gameSession,
	wantLeader uint16,
	wantPartyID uint16,
) alignedcmd.PartyState {
	t.Helper()
	state := runtimePartyStateSnapshot(anyMember)
	if state.PartyID <= 0 || state.UserID != wantLeader {
		t.Fatalf("session projection party/leader=%d/%d want positive/%d: %+v",
			state.PartyID, state.UserID, wantLeader, state)
	}
	if wantPartyID != 0 && state.PartyID != int(wantPartyID) {
		t.Fatalf("session projection party changed: got=%d want=%d", state.PartyID, wantPartyID)
	}
	wantPartyID = uint16(state.PartyID)
	if err := leaderHandoffAssertMemberSlots(runtimePartyMembers(state), inviteAcceptLeaderCharacterID, inviteAcceptFollowerCharacterID); err != nil {
		t.Fatalf("session projection slots: %v; state=%+v", err, state)
	}

	snapshot, found := service.runtimePartyManagerForService().SnapshotByUser(
		inviteAcceptLeaderCharacterID,
		0,
	)
	if !found || snapshot.ID != wantPartyID || snapshot.Leader != wantLeader || len(snapshot.Members) != 2 {
		t.Fatalf("central party: found=%t id=%d leader=%d members=%d want id=%d leader=%d members=2",
			found, snapshot.ID, snapshot.Leader, len(snapshot.Members), wantPartyID, wantLeader)
	}
	if err := leaderHandoffAssertRuntimeMemberSlots(snapshot.Members); err != nil {
		t.Fatalf("central party slots: %v; snapshot=%+v", err, snapshot)
	}
	return state
}

func leaderHandoffAssertMemberSlots(members []alignedcmd.PartyMemberState, leaderID, followerID uint16) error {
	if len(members) != 2 {
		return fmt.Errorf("member count=%d want 2", len(members))
	}
	want := map[uint16]byte{leaderID: 0, followerID: 1}
	seen := make(map[uint16]bool, len(members))
	for _, member := range members {
		slot, ok := want[member.UserID]
		if !ok || seen[member.UserID] {
			return fmt.Errorf("unexpected/duplicate member=%d", member.UserID)
		}
		if member.Slot != slot {
			return fmt.Errorf("member=%d slot=%d want=%d", member.UserID, member.Slot, slot)
		}
		seen[member.UserID] = true
	}
	return nil
}

func leaderHandoffAssertRuntimeMemberSlots(members []party.RuntimePartyMember) error {
	if len(members) != 2 {
		return fmt.Errorf("member count=%d want 2", len(members))
	}
	want := map[uint16]byte{
		inviteAcceptLeaderCharacterID:   0,
		inviteAcceptFollowerCharacterID: 1,
	}
	seen := make(map[uint16]bool, len(members))
	for _, member := range members {
		slot, ok := want[member.UserID]
		if !ok || seen[member.UserID] {
			return fmt.Errorf("unexpected/duplicate member=%d", member.UserID)
		}
		if member.Slot != slot {
			return fmt.Errorf("member=%d slot=%d want=%d", member.UserID, member.Slot, slot)
		}
		seen[member.UserID] = true
	}
	return nil
}
