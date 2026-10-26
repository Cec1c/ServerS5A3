package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/buffswap"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestAlignedMainBagSortRefreshesExistingBuffAfterOp13(t *testing.T) {
	t.Setenv(buffSwapReapplyEnv, "on")
	f := newRefreshCostFixture(t)
	f.warmUp(t)
	f.service.options.currentClientVersion = "official-24.9"
	ctx := context.Background()

	inv, found, err := f.group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	// The cost fixture deliberately fills slots beyond the default equipment
	// capacity with material rows. Keep this behavioral fixture to one legal
	// equipment row so sort exercises rebinding, not over-capacity rejection.
	inv.Slots = make(map[string]dnfrepo.ItemStack)
	inv.Slots["0:20"] = dnfrepo.ItemStack{
		ItemID: 9001,
		Count:  1,
		Extra: map[string]string{
			"item_kind":      "equipment",
			"pvf_path":       "equipment/weapon/test_sword.equ",
			"equipment_type": "[weapon]",
		},
	}
	if err := f.group.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.ApplyCurrent(ctx, f.group, "19", "dnf:1", buffswap.Command{
		Skill: 38,
		Slot:  12,
		List:  0,
		Index: 20,
	}); err != nil {
		t.Fatal(err)
	}
	persistedBefore, _, err := f.group.Settings.Load(ctx, buffswap.Scope("19"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := buffswap.LoadResolvedOwned(ctx, f.group, "19", "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	instance := before.State.Slots[12].Instance
	if instance == "" || before.State.Slots[12].Index != 20 {
		t.Fatalf("fixture registration=%+v", before.State)
	}
	if err := f.service.sendBuffSwapReapply(f.session, "sort_fixture_initial_registration"); err != nil {
		t.Fatal(err)
	}

	sortRequest := alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketSortItem),
		Body:                []byte{0, 1},
		AccountID:           "dnf:1",
		SelectedCharacterID: 19,
		Repositories:        f.group,
	}
	first, err := dnfinventory.NewHandler().Handle(ctx, sortRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Handled || !first.ResponseAllowed || !first.RefreshSelectedBuffRegistration {
		t.Fatalf("changed sort result=%+v", first)
	}
	f.buffer.write.Reset()
	if err := f.service.sendAlignedUpperResponses(f.session, first); err != nil {
		t.Fatal(err)
	}
	frames := splitSortLockFrames(t, f.buffer.write.Bytes())
	listAt, buffAt := -1, -1
	var buffRow []byte
	for index, frame := range frames {
		if frame.Header.Classification == 0 && frame.Header.MsgID == 13 && len(frame.Body) > 0 && frame.Body[0] == 0 {
			listAt = index
		}
		if frame.Header.Classification == 0 && (frame.Header.MsgID == currentBuffSwapSnapshotID || frame.Header.MsgID == currentBuffSwapRowsID) {
			if buffAt >= 0 {
				t.Fatalf("sort emitted duplicate Buff refreshes: %v", sortFrameMsgIDs(frames))
			}
			buffAt = index
			buffRow = r195SortBuffRegistrationRow(t, frame, 12)
		}
	}
	if listAt < 0 || buffAt < 0 || listAt >= buffAt {
		t.Fatalf("op13/Buff order: list=%d buff=%d frames=%v", listAt, buffAt, sortFrameMsgIDs(frames))
	}
	if frames[buffAt].Header.MsgID != currentBuffSwapRowsID {
		t.Fatal("known skill must retain row-only refresh after replacing bag")
	}
	if want := []byte{1, 12, 0, 9, 0}; !reflect.DeepEqual(buffRow, want) {
		t.Fatalf("Buff row=%x want=%x", buffRow, want)
	}
	after, err := buffswap.LoadResolvedOwned(ctx, f.group, "19", "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	binding := after.State.Slots[12]
	if after.State.Skill != 38 || binding.Instance != instance || binding.List != 0 || binding.Index != 9 {
		t.Fatalf("resolved registration after refresh=%+v", after.State)
	}
	persistedAfter, _, err := f.group.Settings.Load(ctx, buffswap.Scope("19"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persistedBefore, persistedAfter) {
		t.Fatal("sort refresh created or rewrote the Buff registration")
	}

	second, err := dnfinventory.NewHandler().Handle(ctx, sortRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Handled || !second.ResponseAllowed || !second.RefreshSelectedBuffRegistration {
		t.Fatalf("unchanged repeated sort result=%+v", second)
	}
	f.buffer.write.Reset()
	if err := f.service.sendAlignedUpperResponses(f.session, second); err != nil {
		t.Fatal(err)
	}
	repeatedFrames := splitSortLockFrames(t, f.buffer.write.Bytes())
	repeatedBuffCount := 0
	for _, frame := range repeatedFrames {
		if frame.Header.Classification == 0 && (frame.Header.MsgID == currentBuffSwapSnapshotID || frame.Header.MsgID == currentBuffSwapRowsID) {
			repeatedBuffCount++
			if frame.Header.MsgID != currentBuffSwapRowsID || !reflect.DeepEqual(r195SortBuffRegistrationRow(t, frame, 12), buffRow) {
				t.Fatal("unchanged list replacement must rebind the same registered item without resetting skill")
			}
		}
	}
	if repeatedBuffCount != 1 {
		t.Fatalf("repeated replacing list refresh count=%d", repeatedBuffCount)
	}
	repeated, err := buffswap.LoadResolvedOwned(ctx, f.group, "19", "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	if repeated.State.Skill != 38 || repeated.State.Slots[12] != binding {
		t.Fatalf("repeated sort changed registration: before=%+v after=%+v", binding, repeated.State.Slots[12])
	}
	// Without a successfully sent authoritative replacing bag, row rebinding
	// must wait. The old client objects still own the old coordinates.
	missing := second
	missing.UpperResponses = missing.UpperResponses[:1]
	f.buffer.write.Reset()
	if err := f.service.sendAlignedUpperResponses(f.session, missing); err != nil {
		t.Fatal(err)
	}
	for _, frame := range splitSortLockFrames(t, f.buffer.write.Bytes()) {
		if frame.Header.MsgID == currentBuffSwapRowsID || frame.Header.MsgID == currentBuffSwapSnapshotID {
			t.Fatal("Buff sent without replacing bag")
		}
	}
}

func r195SortBuffRegistrationRow(t *testing.T, frame dnfproto.ChannelPacket, slot byte) []byte {
	t.Helper()
	body := frame.Body
	if len(body) >= 3 && body[0] == 0 && body[1] == 0 && body[2] == 0 {
		body = body[3:]
	}
	var rows []byte
	switch frame.Header.MsgID {
	case currentBuffSwapSnapshotID:
		if len(body) != 3+buffswap.SlotCount*5 || binary.LittleEndian.Uint16(body) != 38 || body[2] != buffswap.SlotCount {
			t.Fatalf("1368 body length/header=%d/%x", len(body), body[:min(len(body), 3)])
		}
		rows = body[3:]
	case currentBuffSwapRowsID:
		if len(body) != 1+buffswap.SlotCount*5 || body[0] != buffswap.SlotCount {
			t.Fatalf("1367 body length/count=%d/%d", len(body), body[0])
		}
		rows = body[1:]
	default:
		t.Fatalf("unexpected Buff packet %d", frame.Header.MsgID)
	}
	offset := int(slot) * 5
	row := rows[offset : offset+5]
	return append([]byte(nil), row...)
}

func sortFrameMsgIDs(frames []dnfproto.ChannelPacket) []uint16 {
	ids := make([]uint16, len(frames))
	for index, frame := range frames {
		ids[index] = frame.Header.MsgID
	}
	return ids
}
