package dnfbridge

import (
	"context"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"

	"longheng.io/server/internal/modules/dnf/buffswap"
)

// An empty native 1426 projection must not reset the current Buff panel from
// an unrelated committed equipment move. The same empty replacement is still
// sent by the existing login/character-switch path.
func TestCurrentCommittedWearSkipsEmptyNativeBuffSnapshot(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	r := newSkillSwitchInventoryRepos(t)
	s, session, conn, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, r)
	s.options.currentClientVersion = "official-24.9"
	if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "aligned_test_after_committed_wear_without_actor_refresh"); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("empty current-client move refresh wrote %x", conn.write.Bytes())
	}

	// An explicitly stored skill-only state must still be refreshed.
	ctx := context.Background()
	if err := buffswap.ApplyCurrent(ctx, r, "1", "dnf:10001", buffswap.Command{Skill: 38, Slot: 33, List: 44, Index: 0xffff}); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "aligned_test_after_committed_wear_without_actor_refresh"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 1 || packets[0].msgID != currentBuffSwapSnapshotID {
		t.Fatalf("registered move refresh=%v", packets)
	}
}

// Losing the bound item is different from never having a native registration.
// Its empty resolved projection must still replace stale native bindings.
func TestCurrentCommittedWearStillClearsUnresolvedNativeRegistration(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	f := p94Fixture(t)
	r, s, session, conn := f.group, f.service, f.session, f.buffer
	s.options.currentClientVersion = "official-24.9"
	ctx := context.Background()
	p94Register(t, f)
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "1", Entries: map[string]repo.EquipmentEntry{}}); err != nil {
		t.Fatal(err)
	}
	projection, err := buffswap.LoadResolvedOwned(ctx, r, "1", "dnf:10011")
	if err != nil || !projection.Found || len(projection.State.Slots) != 0 || projection.Omitted != 1 {
		t.Fatalf("fixture projection=%+v err=%v", projection, err)
	}
	conn.write.Reset()
	if err := s.sendBuffSwapReapplyAfterCommittedWear(session, "committed_wear"); err != nil {
		t.Fatal(err)
	}
	packets := drainUpperPackets(t, conn)
	if len(packets) != 1 {
		t.Fatalf("missing stale-binding clear: %v", packets)
	}
}

func TestCurrentCommittedWearLoadoutSurvivesActualOp19WithoutNativeState(t *testing.T) {
	f := p94Fixture(t)
	t.Setenv(buffSwapReapplyEnv, "on")
	f.service.options.currentClientVersion = "official-24.9"
	seedR167Loadout(t, f.service, f.session, f.buffer, f.group)
	for _, body := range [][]byte{
		flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, int16(p94Slot), int32(p94Item)), op19CurrentUIPlayerFlag),
		flaggedBody(t, loginWearBridgeBody(0, 15, int32(p94Item), 3, int16(p94Slot), 0), op19CurrentUIPlayerFlag),
	} {
		p94Move(t, f, body)
		for wire := append([]byte(nil), f.buffer.write.Bytes()...); len(wire) > 0; {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.Classification == 0 && (packet.Header.MsgID == currentBuffSwapSnapshotID || packet.Header.MsgID == currentSkillSwitchInventoryOpcode) {
				t.Fatal("unrelated wear emitted replacement Buff state")
			}
		}
	}
}
