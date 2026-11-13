package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"testing"
	"time"
)

func TestRealPVFIndependentCardsAntonFirst(t *testing.T) { independentRaidCards(t, "anton_phase0", 0) }
func TestRealPVFIndependentCardsAntonFinal(t *testing.T) { independentRaidCards(t, "anton_phase1", 1) }
func TestRealPVFIndependentCardsLukeFirst(t *testing.T)  { independentRaidCards(t, "luke_phase0", 0) }
func TestRealPVFIndependentCardsLukeFinal(t *testing.T)  { independentRaidCards(t, "luke_phase1", 0) }
func TestRealPVFIndependentCardsFiendWar(t *testing.T)   { independentRaidCards(t, "fiendwar", 1) }

// Real PVF/reducer fixtures, authentic 659/660 requests and durable reward
// transactions. Only the clock is controlled. A second client remains in its
// movie while the first must receive every pool before the fallback deadline.
func independentRaidCards(t *testing.T, phase string, fastIndex int) {
	t.Helper()
	f, sessions := r195MovieStageFixture(t, phase)
	warmAntonRewardQuestFixture(t, f)
	ensureRaidRewardAccount(t, f, "account-20")
	fast, slow := sessions[fastIndex], sessions[1-fastIndex]
	flow := f.state.Battle.RewardFlow[f.state.Battle.Phase]
	flow.AutoSelectAt = time.Now().Add(5 * time.Minute)
	flow.PartySelectAfter = nil
	for _, p := range sessions {
		p.conn.(*bufferConn).write.Reset()
	}
	dispatchAntonCardRequest(t, f, fast, 659, []byte{1})
	wire := r195ReadFiendRewardWire(t, fast.conn.(*bufferConn).write.Bytes())
	wantParty := []uint16{fast.selectedCharacterID}
	if f.state.Members[0].GroupIndex == f.state.Members[1].GroupIndex {
		wantParty = []uint16{19, 20}
	}
	for _, kind := range []byte{0, 1} {
		if !reflect.DeepEqual(wire.owners[kind], wantParty) {
			t.Fatalf("party page lost current squad members: %+v want %v", wire, wantParty)
		}
	}
	fast.conn.(*bufferConn).write.Reset()
	dispatchAntonCardRequest(t, f, fast, 660, []byte{1, 0})
	if _, errs := f.service.advanceCurrentAntonRewardFlows(time.Now(), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if !currentRaidOwnerRewardsCommitted(flow, fast.selectedCharacterID) || currentRaidOwnerRewardsCommitted(flow, slow.selectedCharacterID) || !flow.FinishedAt.IsZero() {
		t.Fatal("fast owner still waits for peer, or peer/global completion was fabricated")
	}
	if card := flow.Party[slow.selectedCharacterID]; card != nil && card.Committed {
		t.Fatal("slow owner's reward was auto-selected before its own deadline")
	}
	if f.state.Battle.RewardCardReady[f.state.Battle.Phase][slow.selectedCharacterID] || slow.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("fast flip interrupted the slow owner's movie")
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(fast, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	wire = r195ReadFiendRewardWire(t, fast.conn.(*bufferConn).write.Bytes())
	if flow.squadRequired() {
		if !reflect.DeepEqual(wire.owners[3], []uint16{19, 20}) {
			t.Fatalf("team page must contain the full frozen raid: %+v", wire)
		}
		for _, id := range []uint16{19, 20} {
			if flow.Squad[id] == nil || !flow.Squad[id].Committed {
				t.Fatal("team reward still waits for a peer's party click", id)
			}
		}
	} else {
		found := false
		for _, stage := range wire.sequence {
			found = found || stage == "kind3"
		}
		if !found || len(flow.Squad) != 0 {
			t.Fatal("Luke phase one did not close its personal party-only page")
		}
	}
	before := r195PublicRewardAssetSnapshot(t, f.service, fast)
	shownAt := fast.raidProjection.rewardSquadDisplayShownAt
	fast.conn.(*bufferConn).write.Reset()
	dispatchAntonCardRequest(t, f, fast, 660, []byte{1, 3})
	if after := r195PublicRewardAssetSnapshot(t, f.service, fast); !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate manual request rerolled or granted twice")
	}
	fast.conn.(*bufferConn).write.Reset()
	dispatchAntonCardRequest(t, f, slow, 659, []byte{1})
	dispatchAntonCardRequest(t, f, slow, 660, []byte{1, 0})
	if _, errs := f.service.advanceCurrentAntonRewardFlows(time.Now(), zeroAntonRoll); len(errs) != 0 {
		t.Fatal(errs)
	}
	if !currentRaidOwnerRewardsCommitted(flow, slow.selectedCharacterID) {
		t.Fatal("late owner cannot independently settle")
	}
	// A shared party reveal may update its existing rows, but cannot replay
	// the team list/state4 or move this viewer's actual display deadline.
	if err := f.service.sendCurrentAntonRewardFlowProjection(fast, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	wire = r195ReadFiendRewardWire(t, fast.conn.(*bufferConn).write.Bytes())
	for _, stage := range wire.sequence {
		if stage == "state4" || stage == "kind3" {
			t.Fatalf("late peer reopened fast page: %+v", wire)
		}
	}
	if !fast.raidProjection.rewardSquadDisplayShownAt.Equal(shownAt) {
		t.Fatal("peer flip extended the fast owner's return window")
	}
	if after := r195PublicRewardAssetSnapshot(t, f.service, fast); !reflect.DeepEqual(before, after) {
		t.Fatal("late peer changed fast owner's durable rewards")
	}
}

type independentFailOwnerAssets struct {
	repo.AccountCharacterAssetUnitOfWork
}

func (u independentFailOwnerAssets) WithinAccountCharacterAssets(ctx context.Context, account, character string, apply func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	if character == "19" {
		return newAntonCardFailLateAssets(u.AccountCharacterAssetUnitOfWork).WithinAccountCharacterAssets(ctx, account, character, apply)
	}
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, account, character, apply)
}
func (u independentFailOwnerAssets) WithinAccountCharacterLedgerMailboxAssets(ctx context.Context, account, character string, apply func(repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.MailboxRepository) error) error {
	if character == "19" {
		return newAntonCardFailLateAssets(u.AccountCharacterAssetUnitOfWork).WithinAccountCharacterLedgerMailboxAssets(ctx, account, character, apply)
	}
	return u.AccountCharacterAssetUnitOfWork.(repo.AccountCharacterLedgerMailboxAssetUnitOfWork).WithinAccountCharacterLedgerMailboxAssets(ctx, account, character, apply)
}

func TestRealPVFIndependentCardsGrantFailureDoesNotHoldPeer(t *testing.T) {
	f, sessions, flow := finalTeamReadyFixture(t, false)
	original := f.repos.AccountAssets
	f.repos.AccountAssets = independentFailOwnerAssets{original}
	_, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll)
	f.repos.AccountAssets = original
	if len(errs) != 1 || currentRaidOwnerRewardsCommitted(flow, 19) || !currentRaidOwnerRewardsCommitted(flow, 20) || !flow.FinishedAt.IsZero() {
		t.Fatal("owner19 rollback blocked owner20 or ended global phase", errs)
	}
	p := sessions[1]
	if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	wire := r195ReadFiendRewardWire(t, p.conn.(*publicP14ConcurrentConn).take())
	if !reflect.DeepEqual(wire.owners[3], []uint16{19, 20}) {
		t.Fatal("pending peer omitted from shared team", wire)
	}
	for descriptor := range wire.items {
		if descriptor.owner == 19 {
			t.Fatal("failed grant advertised as awarded", descriptor)
		}
	}
	shownAt := p.raidProjection.rewardSquadDisplayShownAt
	frozen := cloneCurrentAntonRewardFlow(flow)
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(time.Second), zeroAntonRoll); len(errs) != 0 || currentRaidOwnerRewardsCommitted(flow, 19) {
		t.Fatal("owner19 backoff ignored", errs)
	}
	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*time.Second), zeroAntonRoll); len(errs) != 0 || !currentRaidOwnerRewardsCommitted(flow, 19) {
		t.Fatal("owner19 frozen retry failed", errs)
	}
	if !reflect.DeepEqual(frozen.Party[20], flow.Party[20]) || !reflect.DeepEqual(frozen.Squad[20], flow.Squad[20]) || frozen.Party[19].Plan.Fingerprint != flow.Party[19].Plan.Fingerprint {
		t.Fatal("retry rerolled committed or pending plans")
	}
	if err := f.service.sendCurrentAntonRewardFlowProjection(p, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	wire = r195ReadFiendRewardWire(t, p.conn.(*publicP14ConcurrentConn).take())
	if !reflect.DeepEqual(wire.owners[3], []uint16{19, 20}) || !p.raidProjection.rewardSquadDisplayShownAt.Equal(shownAt) {
		t.Fatal("peer retry lost team rows or moved own display deadline", wire)
	}
}

func TestRealPVFIndependentCardsReturnBeforePeerFinishes(t *testing.T) {
	f, sessions := teamAntonRewardReturnFixture(t, 1)
	flow := f.state.Battle.RewardFlow[1]
	// This boundary fixture removes only the peer's completion. The target
	// keeps its authentic run journal, committed grants and display receipt.
	flow.Party[20].Committed, flow.Squad[20].Committed = false, false
	flow.FinishedAt, flow.SquadShownAt = time.Time{}, time.Time{}
	p := sessions[0]
	r := p.dungeon.runtime
	if err := f.service.sendCurrentAntonRewardReturn(p, cloneRuntimeRaidState(*f.state)); err != nil {
		t.Fatal(err)
	}
	if !r.townReturnOp24Sent || p.dungeon.runtime != nil || sessions[1].dungeon.runtime == nil {
		t.Fatal("personal return waited for or returned the peer")
	}
}

func TestIndependentRewardClockAndOwnerBoundaries(t *testing.T) {
	at := time.Unix(100, 0)
	f := &currentAntonRewardFlow{Mode: 1, AutoSelectAt: at.Add(time.Minute), Recipients: []currentAntonRewardRecipient{{CharacterID: 1}, {CharacterID: 2}},
		Party: map[uint16]*currentAntonRewardCard{}, Squad: map[uint16]*currentAntonRewardCard{}, PartySelectAfter: map[uint16]time.Time{1: at.Add(15 * time.Second), 2: at.Add(55 * time.Second)}}
	f.Party[1] = &currentAntonRewardCard{}
	markCurrentRaidCardCommitted(f.Party[1], at, true)
	if !currentRaidOwnerSquadReady(f, 1, at) || currentRaidOwnerSquadReady(f, 2, at) {
		t.Fatal("manual owner's readiness depends on a peer")
	}
	f.Party[2] = &currentAntonRewardCard{}
	markCurrentRaidCardCommitted(f.Party[2], at, false)
	if currentRaidOwnerSquadReady(f, 2, at.Add(14*time.Second)) || !currentRaidOwnerSquadReady(f, 2, at.Add(15*time.Second)) {
		t.Fatal("unattended owner's native hold changed")
	}
	if currentRaidOwnerPartySelectAt(f, 1) != at.Add(15*time.Second) || currentRaidOwnerPartySelectAt(f, 2) != at.Add(55*time.Second) {
		t.Fatal("screen clocks leaked across owners")
	}
	deferCurrentRaidRewardOwner(f, 1, at)
	if !f.OwnerNextAttempt[1].After(at) || !f.OwnerNextAttempt[2].IsZero() || !f.NextAttempt.IsZero() {
		t.Fatal("one owner's failure deferred the raid")
	}
	f.Squad[1] = &currentAntonRewardCard{Committed: true}
	if !currentRaidOwnerRewardsCommitted(f, 1) || currentRaidOwnerRewardsCommitted(f, 2) || currentRaidOwnerRewardsCommitted(f, 3) {
		t.Fatal("commit boundary lost ownership")
	}
	copy := cloneCurrentAntonRewardFlow(f)
	copy.OwnerNextAttempt[1] = at
	copy.Party[1].Manual = false
	if !f.Party[1].Manual || f.OwnerNextAttempt[1] == at {
		t.Fatal("snapshot aliases live progress")
	}
}
