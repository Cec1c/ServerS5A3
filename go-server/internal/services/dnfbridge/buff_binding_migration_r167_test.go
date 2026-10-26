package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// Current1426 native registrations bind a physical item. The instance marker is
// carried by inventory/equipment rows through op19, so wearing and removing the
// item must move the projection while retaining that identity. The 1428 stream
// is a separate current-client loadout owner and is not a substitute repair.
func TestR167NativeBindingSurvivesActualWearWithout1428Replay(t *testing.T) {
	f := p94Fixture(t)
	t.Setenv(buffSwapReapplyEnv, "on")
	f.service.options.currentClientVersion = "official-24.9"
	// Seed a real loadout row so the op19 boundary can actually reach the
	// projection sender; an empty settings store would make the no-1428 check
	// pass without exercising the gate.
	seedR167Loadout(t, f.service, f.session, f.buffer, f.group)

	instance := p94Register(t, f)
	for _, move := range []struct {
		body  []byte
		list  byte
		index uint16
	}{
		{flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, int16(p94Slot), int32(p94Item)), op19CurrentUIPlayerFlag), 0, 15},
		{flaggedBody(t, loginWearBridgeBody(0, 15, int32(p94Item), 3, int16(p94Slot), 0), op19CurrentUIPlayerFlag), 3, uint16(p94Slot)},
	} {
		p94Move(t, f, move.body)
		for wire := append([]byte(nil), f.buffer.write.Bytes()...); len(wire) > 0; {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if packet.Header.Classification == 0 && packet.Header.MsgID == currentSkillSwitchInventoryOpcode {
				t.Fatal("physical item migration emitted an op1428 loadout replay")
			}
		}
		p94AssertResolved(t, f, instance, move.list, move.index, true)
		p94Wire(t, f.buffer.write.Bytes(), move.list, move.index, true, true)
	}
	p94Cold(t, f, 3, uint16(p94Slot), true)
}

// This is the shape that polluted character 1006: the six-byte handler body
// 00 00 0c 00 36 00 is slot12/mode0/value54 for 1428, but the old native owner
// read it as skill0/list0/index54 and persisted a positional native binding.
func TestR167CurrentClient1428CannotBecomeNativeBinding(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	f := newSkillSwitchLoadoutFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	body := skillSwitchInventoryBody(t, "00000c003600")
	if _, err := f.service.handleSkillSwitchInventoryCommand(f.session, currentSkillSwitchInventoryOpcode, body); err != nil {
		t.Fatal(err)
	}

	state, found, err := skillswitch.Load(context.Background(), f.repos, "1", skillswitch.LayoutV4)
	if err != nil || !found || len(state.Records) != 1 || state.Records[0].Slot != 12 || state.Records[0].Value != 54 {
		t.Fatalf("current1428 not retained by loadout owner: found=%t state=%+v err=%v", found, state, err)
	}
	if _, nativeFound, nativeErr := buffswap.Load(context.Background(), f.repos, "1", "dnf:10001"); nativeErr != nil || nativeFound {
		t.Fatalf("current1428 leaked into native binding state: found=%t err=%v", nativeFound, nativeErr)
	}
	if n := f.connection.write.Len(); n != 0 {
		t.Fatalf("current 1428 record emitted %d bytes while native reapply is on", n)
	}
}

func TestR167HistoricalProfileRetains1428NativeOwner(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	f := newSkillSwitchLoadoutFixture(t)
	f.service.options.currentClientVersion = "legacy"
	if err := f.repos.Inventory.Save(context.Background(), repo.InventoryRecord{
		CharacterID: "1",
		Slots: map[string]repo.ItemStack{
			"0:54": {ItemID: 28309, Count: 1, Extra: map[string]string{"item_kind": "equipment"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.handleSkillSwitchInventoryCommand(
		f.session,
		currentSkillSwitchInventoryOpcode,
		skillSwitchInventoryBody(t, "00000c003600"),
	); err != nil {
		t.Fatal(err)
	}
	state, found, err := buffswap.Load(context.Background(), f.repos, "1", "dnf:10001")
	if err != nil || !found || state.Slots[12].List != 0 || state.Slots[12].Index != 54 {
		t.Fatalf("legacy profile lost its historical native 1428 owner: found=%t state=%+v err=%v", found, state, err)
	}
	if _, loadoutFound, loadoutErr := skillswitch.Load(context.Background(), f.repos, "1", skillswitch.LayoutV4); loadoutErr != nil || loadoutFound {
		t.Fatalf("legacy 1428 unexpectedly became a loadout record: found=%t err=%v", loadoutFound, loadoutErr)
	}
}

// seedR167Loadout persists one binding-bearing record through the loadout
// owner. The record is not a sentinel, so it must not answer immediately.
func seedR167Loadout(t *testing.T, service *Service, session *gameSession, conn *bufferConn, repos repo.Group) {
	t.Helper()
	if _, err := service.handleSkillSwitchInventoryCommand(
		session,
		currentSkillSwitchInventoryOpcode,
		// ModeRegister (slot16 -> bound slot54) is binding-bearing, so the
		// loadout store has a real row at every later boundary.
		skillSwitchInventoryBody(t, "000010003600"),
	); err != nil {
		t.Fatal(err)
	}
	if n := conn.write.Len(); n != 0 {
		t.Fatalf("ordinary loadout record emitted %d bytes", n)
	}
	state, found, err := skillswitch.Load(context.Background(), repos, "1", skillswitch.LayoutV4)
	if err != nil || !found || len(state.Records) == 0 {
		t.Fatalf("loadout seed not persisted: found=%t state=%+v err=%v", found, state, err)
	}
	conn.write.Reset()
}

// Inbound 1428 ownership and outbound 1428 publication are separate claims.
// The routing fix must not re-enable the late-boundary projection while the
// current client's downlink contract is still unproven.
func TestR167Current1428ProjectionSuppressedUnderNativeReapply(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	f := newSkillSwitchLoadoutFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	seedR167Loadout(t, f.service, f.session, f.connection, f.repos)

	if err := f.service.sendSkillSwitchInventoryProjection(f.session, "equip_unequip_op19_aligned_after_ack"); err != nil {
		t.Fatal(err)
	}
	if n := f.connection.write.Len(); n != 0 {
		t.Fatalf("native reapply reissued op1428 on a persistence boundary: %d bytes", n)
	}
}

// A sentinel may be used to commit a registration window, but it must not
// trigger the immediate op1428 answer while native reapply owns 1426/1368.
func TestR167Current1428SentinelDoesNotAnswerUnderNativeReapply(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	f := newSkillSwitchLoadoutFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	seedR167Loadout(t, f.service, f.session, f.connection, f.repos)

	if _, err := f.service.handleSkillSwitchInventoryCommand(
		f.session,
		currentSkillSwitchInventoryOpcode,
		skillSwitchInventoryBody(t, skillSwitchInventorySentinelBody),
	); err != nil {
		t.Fatal(err)
	}
	if n := f.connection.write.Len(); n != 0 {
		t.Fatalf("native reapply answered a 1428 sentinel with %d bytes", n)
	}
}
