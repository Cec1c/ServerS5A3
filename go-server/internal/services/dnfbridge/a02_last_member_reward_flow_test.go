package dnfbridge

import (
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// TestA02LastMemberActualRaidLeaveKeepsFrozenRewardFlowAndGrantsAssets pins
// the existing in-process contract for an already-frozen reward flow. The
// first member leaves through the current op13/op665 handlers. The last member
// then leaves through op665, which removes the raid directory entirely. The
// frozen recipients and both automatic reward pools must still advance and
// grant every owner without depending on a surviving raid or old connection.
func TestA02LastMemberActualRaidLeaveKeepsFrozenRewardFlowAndGrantsAssets(t *testing.T) {
	f, viewer, _ := firstAntonFlowFixture(t)
	flow := f.state.Battle.RewardFlow[0]
	if flow == nil {
		t.Fatal("fixture did not freeze the active reward flow")
	}
	key := currentAntonFlowKey(flow.InstanceID, flow.Phase)
	if f.service.antonRewardFlows[key] != flow {
		t.Fatal("active reward flow is not owned by the service flow registry")
	}
	if len(flow.Recipients) != 2 || flow.Recipients[0].CharacterID != 19 || flow.Recipients[1].CharacterID != 20 {
		t.Fatalf("fixture recipients=%+v, want frozen characters 19 and 20", flow.Recipients)
	}
	frozen := append([]currentAntonRewardRecipient(nil), flow.Recipients...)

	peer, ok := f.service.onlineGameSession(20)
	if !ok {
		t.Fatal("fixture peer 20 is not online")
	}
	a02RewardPartyWithSessionGenerations(t, f.service, viewer, peer)

	// Keep the first departure on the already-covered ordinary-party path.
	handled, err := f.service.handleAlignedGameCommand(
		peer,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil || !handled {
		t.Fatalf("op13 leave party: handled=%t err=%v", handled, err)
	}

	leaveRaid := func(session *gameSession, character uint16) {
		t.Helper()
		handled, err := f.service.handleAlignedGameCommand(
			session,
			byte(dnfenum.GameCmdCommand),
			uint16(dnfenum.CmdPacketLeaveRaid),
			[]byte{byte(character), 0},
		)
		if err != nil || !handled {
			t.Fatalf("op665 leave raid for %d: handled=%t err=%v", character, handled, err)
		}
	}

	leaveRaid(peer, 20)
	if runtimeRaidMemberIndex(*f.state, 20) >= 0 || runtimeRaidMemberIndex(*f.state, 19) < 0 {
		t.Fatal("first op665 did not leave exactly one raid member")
	}
	if f.service.antonRewardFlows[key] != flow {
		t.Fatal("first op665 replaced or removed the frozen reward flow")
	}

	// This is the lifecycle under test: 19 is now the final member. The real
	// handler deletes the empty raid directory, but the frozen flow is separate.
	leaveRaid(viewer, 19)
	if len(f.service.raids) != 0 {
		t.Fatalf("last op665 left %d raid directory entries", len(f.service.raids))
	}
	for _, character := range []uint16{19, 20} {
		if _, found := f.service.runtimeRaidForMember(character); found {
			t.Fatalf("character %d retained raid membership after the last leave", character)
		}
	}
	if f.service.antonRewardFlows[key] != flow {
		t.Fatal("last op665 removed the already-frozen reward flow")
	}
	if !reflect.DeepEqual(flow.Recipients, frozen) {
		t.Fatalf("last op665 changed frozen recipients: got=%+v want=%+v", flow.Recipients, frozen)
	}
	if !flow.FinishedAt.IsZero() {
		t.Fatal("fixture reward flow was already complete before advancement")
	}
	for _, character := range []string{"19", "20"} {
		if got := antonGrantedItemCount(t, f, character, 10304529); got != 0 {
			t.Fatalf("member %s received party reward before advancement: %d", character, got)
		}
		if got := antonGrantedItemCount(t, f, character, 10093974); got != 0 {
			t.Fatalf("member %s received squad reward before advancement: %d", character, got)
		}
	}

	// The raid directory is gone. Advancement must therefore come from the
	// frozen service-owned flow, not from a surviving live raid snapshot.
	states, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt, zeroAntonRoll)
	if len(errs) != 0 || len(states) != 0 {
		t.Fatalf("first frozen grant after last leave: states=%d errs=%v", len(states), errs)
	}
	for _, owner := range []struct {
		character   uint16
		inventoryID string
	}{{19, "19"}, {20, "20"}} {
		card := flow.Party[owner.character]
		if card == nil || !card.Committed {
			t.Fatalf("member %d party card was not committed after last leave", owner.character)
		}
		if got := antonGrantedItemCount(t, f, owner.inventoryID, 10304529); got != 30 {
			t.Fatalf("member %d party reward after last leave=%d, want 30", owner.character, got)
		}
	}
	if len(flow.Squad) != 0 || !flow.SquadShownAt.IsZero() {
		t.Fatal("party-to-squad reward window was skipped after the last leave")
	}

	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(currentAntonMovieWindow(flow)), zeroAntonRoll); len(errs) != 0 {
		t.Fatalf("squad grant after last leave: %v", errs)
	}
	for _, character := range []uint16{19, 20} {
		card := flow.Squad[character]
		if card == nil || !card.Committed {
			t.Fatalf("member %d squad card was not committed after last leave", character)
		}
	}
	for _, character := range []string{"19", "20"} {
		if got := antonGrantedItemCount(t, f, character, 10304529); got != 30 {
			t.Fatalf("member %s party reward changed after squad grant: %d", character, got)
		}
		if got := antonGrantedItemCount(t, f, character, 10093974); got != 19 {
			t.Fatalf("member %s squad reward after last leave=%d, want 19", character, got)
		}
	}

	if _, errs := f.service.advanceCurrentAntonRewardFlows(flow.AutoSelectAt.Add(2*currentAntonMovieWindow(flow)), zeroAntonRoll); len(errs) != 0 || flow.FinishedAt.IsZero() {
		t.Fatalf("frozen flow did not finish after its display window: finished=%t errs=%v", !flow.FinishedAt.IsZero(), errs)
	}
	for _, character := range []string{"19", "20"} {
		if got := antonGrantedItemCount(t, f, character, 10304529); got != 30 {
			t.Fatalf("member %s party reward duplicated: %d", character, got)
		}
		if got := antonGrantedItemCount(t, f, character, 10093974); got != 19 {
			t.Fatalf("member %s squad reward duplicated: %d", character, got)
		}
	}
	if !reflect.DeepEqual(flow.Recipients, frozen) {
		t.Fatalf("completed flow changed frozen recipients: got=%+v want=%+v", flow.Recipients, frozen)
	}
}
