package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strconv"
	"testing"
)

// Reuse Hume's real-PVF fixture. Only add a real catalog item at its named
// worn slot; full op19 placement validation is exercised on the re-wear.
func r139PrepareSlot(t *testing.T, f *loginWearAnchorFixture, slot byte, item int64) string {
	t.Helper()
	ctx := context.Background()
	def, err := f.service.pvfItemCatalog.ResolveItem(uint32(item))
	if err != nil {
		t.Fatal(err)
	}
	eq, _, err := f.group.Equipment.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	key := strconv.Itoa(int(slot))
	extra := eq.Entries[key].Extra
	if extra == nil {
		extra = map[string]string{}
	}
	extra["item_kind"] = "equipment"
	extra["pvf_path"] = def.PVFPath
	extra["equipment_type"] = def.EquipmentType
	extra["equipped_slot"] = strconv.Itoa(int(slot))
	extra["equipment_slot"] = strconv.Itoa(int(slot))
	extra["durability"] = "48"
	extra["max_durability"] = "48"
	extra["quality_seed"] = "203004656"
	// Every named slot is a current runtime row. Legacy persistent slot numbers
	// map differently, including pants and waist, and cannot seed this fixture.
	extra["current_exe_runtime_move"] = "1"
	extra["current_exe_equipment_type"] = strconv.Itoa(int(slot))
	eq.Entries[key] = repo.EquipmentEntry{SlotIndex: int16(slot), ItemID: item, RawEntry: r139PublicRawEntry(int16(slot), item, 203004656, 48), Extra: extra}
	if err = f.group.Equipment.Save(ctx, eq); err != nil {
		t.Fatal(err)
	}
	t.Logf("real catalog item=%d slot=%d path=%s type=%s", item, slot, def.PVFPath, def.EquipmentType)
	return r139Register(t, f, slot)
}

func r139Register(t *testing.T, f *loginWearAnchorFixture, slot byte) string {
	t.Helper()
	f.buffer.write.Reset()
	if err := f.service.handleGameCommand(f.session, 1, 1426, reviewCurrentBuffCommand(38, slot, 3, uint16(slot))); err != nil {
		t.Fatal(err)
	}
	state, found, err := buffswap.Load(context.Background(), f.group, "1", "dnf:10011")
	if err != nil || !found || state.Slots[slot].Instance == "" {
		t.Fatal("registration unavailable", err)
	}
	return state.Slots[slot].Instance
}

func r139IsBuffWireID(msg uint16) bool {
	return msg == currentBuffSwapRowsID || msg == currentBuffSwapSnapshotID
}

func r139AssertWire(t *testing.T, f *loginWearAnchorFixture, slot, list byte, index uint16, modeCount int, wantBuffID uint16) {
	t.Helper()
	wire := append([]byte(nil), f.buffer.write.Bytes()...)
	ackAt, rowAt, buffAt := -1, -1, -1
	buffCount, actorCount := 0, 0
	for seq := 0; len(wire) > 0; seq++ {
		p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if p.Header.Classification == 1 && p.Header.MsgID == 19 {
			ackAt = seq
		}
		if p.Header.Classification == 0 && p.Header.MsgID == 14 {
			rowAt = seq
		}
		if p.Header.Classification == 0 && p.Header.MsgID == 2 {
			actorCount++
		}
		if p.Header.Classification == 0 && r139IsBuffWireID(p.Header.MsgID) {
			buffCount++
			buffAt = seq
			if p.Header.MsgID != wantBuffID {
				t.Fatalf("unexpected Buff wire msg=%d want=%d", p.Header.MsgID, wantBuffID)
			}
			row := r195SortBuffRegistrationRow(t, p, slot)
			if !bytes.Equal(row, []byte{1, slot, list, byte(index), byte(index >> 8)}) {
				t.Fatalf("wrong instance projection %x", row)
			}
		}
	}
	if ackAt < 0 || rowAt < ackAt || buffAt < rowAt || buffCount != 1 || actorCount != modeCount {
		t.Fatalf("sequence ack=%d last_item_row=%d buff=%d count=%d actors=%d want=%d", ackAt, rowAt, buffAt, buffCount, actorCount, modeCount)
	}
}

func r139AssertOwner(t *testing.T, f *loginWearAnchorFixture, slot byte, id string, list byte, index uint16) {
	t.Helper()
	p, err := buffswap.LoadResolvedOwned(context.Background(), f.group, "1", "dnf:10011")
	if err != nil {
		t.Fatal(err)
	}
	b := p.State.Slots[slot]
	if b.Instance != id || b.List != list || b.Index != index {
		t.Fatalf("instance/location drift: %+v", b)
	}
}

func TestR139BuffDefaultSlotAndOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slot   byte
		item   int64
		actors int
		buffID uint16
	}{
		{"visible_weapon12", 12, 101040493, 1, currentBuffSwapRowsID}, {"nonvisible_pants16", 16, 100120544, 0, currentBuffSwapRowsID}, {"nonvisible_waist18", 18, 100220519, 0, currentBuffSwapRowsID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := p94Fixture(t)
			instance := r139PrepareSlot(t, f, tc.slot, tc.item)
			_, _, before, legacy := p94Snapshot(t, f)
			p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, int16(tc.slot), int32(tc.item)), op19CurrentUIPlayerFlag))
			r139AssertOwner(t, f, tc.slot, instance, 0, 15)
			r139AssertWire(t, f, tc.slot, 0, 15, tc.actors, tc.buffID)
			p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, int32(tc.item), 3, int16(tc.slot), 0), op19CurrentUIPlayerFlag))
			r139AssertOwner(t, f, tc.slot, instance, 3, uint16(tc.slot))
			r139AssertWire(t, f, tc.slot, 3, uint16(tc.slot), tc.actors, tc.buffID)
			_, _, after, old := p94Snapshot(t, f)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(legacy, old) {
				t.Fatal("move mutated registration owner")
			}
		})
	}
}

func TestR139BuffSameTemplateReregisterFollowsNewInstance(t *testing.T) {
	f := p94Fixture(t)
	a := p94Register(t, f)
	ctx := context.Background()
	inv, eq, _, _ := p94Snapshot(t, f)
	entry := eq.Entries["16"]
	b := repo.ItemStack{ItemID: p94Item, Count: 1, RawEntry: append([]byte(nil), entry.RawEntry...), Extra: map[string]string{}}
	for k, v := range entry.Extra {
		if k != "native_buff_instance" {
			b.Extra[k] = v
		}
	}
	binary.LittleEndian.PutUint16(b.RawEntry, 15)
	if inv.Slots == nil {
		inv.Slots = map[string]repo.ItemStack{}
	}
	inv.Slots["0:15"] = b
	if err := f.group.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	body := flaggedBody(t, loginWearBridgeBody(0, 15, int32(p94Item), 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag)
	p94Move(t, f, body)
	r139AssertOwner(t, f, 16, a, 0, 15)
	bID := r139Register(t, f, 16)
	if bID == a {
		t.Fatal("different physical items got same registration ID")
	}
	p94Move(t, f, body) // A worn again; the newly registered B must follow into the bag.
	r139AssertOwner(t, f, 16, bID, 0, 15)
	r139AssertWire(t, f, 16, 0, 15, 0, currentBuffSwapRowsID)
	p94Move(t, f, loginWearBridgeBody(0, 15, int32(p94Item), 0, 17, 0))
	r139AssertOwner(t, f, 16, bID, 0, 17)
	p94Cold(t, f, 0, 17, true)
}

type r139AbortAfterBothSaves struct {
	repo.CharacterItemUnitOfWork
	reached *bool
}

func (u r139AbortAfterBothSaves) WithinCharacterItems(ctx context.Context, id string, fn func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterItemUnitOfWork.WithinCharacterItems(ctx, id, func(i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := fn(i, e); err != nil {
			return err
		}
		*u.reached = true
		return errors.New("r139 injected transaction abort after callback")
	})
}

func TestR139BuffFinalTransactionAbortNoProjection(t *testing.T) {
	f := p94Fixture(t)
	p94Register(t, f)
	bi, be, bn, bo := p94Snapshot(t, f)
	reached := false
	g := f.group
	g.CharacterItems = r139AbortAfterBothSaves{g.CharacterItems, &reached}
	f.service.repositoryProvider = func() (repo.Group, bool) { return g, true }
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag))
	ai, ae, an, ao := p94Snapshot(t, f)
	if !reached || !reflect.DeepEqual(bi, ai) || !reflect.DeepEqual(be, ae) || !reflect.DeepEqual(bn, an) || !reflect.DeepEqual(bo, ao) || f.buffer.write.Len() != 0 {
		t.Fatal("aborted transaction changed state/emitted projection")
	}
}

type r139FailMessageConn struct {
	bufferConn
	kind byte
	msg  uint16
	hit  bool
}

func (c *r139FailMessageConn) Write(p []byte) (int, error) {
	if len(p) >= 3 && p[0] == c.kind && binary.LittleEndian.Uint16(p[1:3]) == c.msg {
		c.hit = true
		return 0, errors.New("r139 injected message write failure")
	}
	return c.bufferConn.Write(p)
}

func TestR139BuffCommittedWriteFailuresDoNotRollbackOrJumpOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind byte
		msg  uint16
	}{{"ack", 1, 19}, {"item_row", 0, 14}, {"buff_rows", 0, currentBuffSwapRowsID}} {
		t.Run(tc.name, func(t *testing.T) {
			f := p94Fixture(t)
			instance := p94Register(t, f)
			conn := &r139FailMessageConn{kind: tc.kind, msg: tc.msg}
			f.session.conn = conn
			err := f.service.handleGameCommand(f.session, 1, 19, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag))
			if err == nil || !conn.hit {
				t.Fatal("write failure not reached")
			}
			r139AssertOwner(t, f, 16, instance, 0, 15)
			wire := conn.write.Bytes()
			for len(wire) > 0 {
				p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
				wire = rest
				if p.Header.Classification == 0 && r139IsBuffWireID(p.Header.MsgID) {
					t.Fatal("snapshot jumped failed ACK/row or retry duplicated failed snapshot")
				}
			}
			// Explicit read projection recovery only. The test does not retry op19.
			f.session.conn = f.buffer
			f.buffer.write.Reset()
			if err := f.service.sendBuffSwapReapply(f.session, "r139_after_transport_recovery"); err != nil {
				t.Fatal(err)
			}
			p94Wire(t, f.buffer.write.Bytes(), 0, 15, true, false)
		})
	}
	t.Run("buff_snapshot_registration", func(t *testing.T) {
		f := p94Fixture(t)
		conn := &r139FailMessageConn{kind: 0, msg: currentBuffSwapSnapshotID}
		f.session.conn = conn
		err := f.service.handleGameCommand(f.session, 1, 1426, reviewCurrentBuffCommand(38, p94Slot, 3, uint16(p94Slot)))
		if err == nil || !conn.hit {
			t.Fatalf("1368 registration write failure not reached: hit=%v err=%v", conn.hit, err)
		}
		state, found, loadErr := buffswap.Load(context.Background(), f.group, "1", "dnf:10011")
		if loadErr != nil || !found || state.Slots[p94Slot].Instance == "" {
			t.Fatalf("1368 registration failure rolled back committed owner: found=%v err=%v", found, loadErr)
		}
	})
}

func TestR139BuffEmptyMoveAndFeatureOff(t *testing.T) {
	t.Run("empty_noop", func(t *testing.T) {
		f := p94Fixture(t)
		p94Register(t, f)
		bi, be, bn, bo := p94Snapshot(t, f)
		p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, 18, 0), op19CurrentUIPlayerFlag))
		ai, ae, an, ao := p94Snapshot(t, f)
		if !reflect.DeepEqual(bi, ai) || !reflect.DeepEqual(be, ae) || !reflect.DeepEqual(bn, an) || !reflect.DeepEqual(bo, ao) {
			t.Fatal("empty noop changed state")
		}
		wire := f.buffer.write.Bytes()
		for len(wire) > 0 {
			p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if p.Header.Classification == 0 && r139IsBuffWireID(p.Header.MsgID) {
				t.Fatal("noop refreshed Buff")
			}
		}
	})
	t.Run("feature_off", func(t *testing.T) {
		f := p94Fixture(t)
		instance := p94Register(t, f)
		t.Setenv(buffSwapReapplyEnv, "off")
		p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag))
		r139AssertOwner(t, f, 16, instance, 0, 15)
		wire := f.buffer.write.Bytes()
		for len(wire) > 0 {
			p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if p.Header.Classification == 0 && r139IsBuffWireID(p.Header.MsgID) {
				t.Fatal("disabled replay sent snapshot")
			}
		}
	})
}

// Same public fixture wire fields, parameterized for real catalog slots.
func r139PublicRawEntry(slot int16, item int64, seed uint32, durability uint16) []byte {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw, uint16(slot))
	binary.LittleEndian.PutUint32(raw[2:], uint32(item))
	binary.LittleEndian.PutUint32(raw[6:], seed)
	raw[10] = 13
	binary.LittleEndian.PutUint16(raw[11:], durability)
	binary.LittleEndian.PutUint32(raw[14:], uint32(item))
	return raw
}
