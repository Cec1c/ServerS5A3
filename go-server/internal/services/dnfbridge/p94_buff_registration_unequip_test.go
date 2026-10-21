package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillswitch"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const p94Item int64 = 100120544
const p94Slot byte = 16

func p94Fixture(t *testing.T) *loginWearAnchorFixture {
	t.Helper()
	setDefaultTier(t)
	t.Setenv(buffSwapReapplyEnv, "on")
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "")
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current real PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Checksum != "25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348" {
		t.Fatal("PVF mismatch")
	}
	items, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	def, err := items.ResolveItem(uint32(p94Item))
	if err != nil {
		t.Fatal(err)
	}
	f := newLoginWearAnchorFixture(t, "1", 1, map[int16]int64{int16(p94Slot): p94Item})
	f.service.initialEquipmentArchive = a
	f.service.pvfItemCatalog = items
	f.service.options.currentClientVersion = "official-24.9"
	f.session.equipLoginWearWindowOpenedAt = time.Now() // manual marker must bypass this live login window
	f.session.itemListMigrationsDone = true
	ctx := context.Background()
	character, _, err := f.group.Character.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	character.Stats = map[string]int64{"grow_type": 33}
	if err := f.group.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	eq, _, err := f.group.Equipment.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	e := eq.Entries["16"]
	e.Extra["pvf_path"] = def.PVFPath
	e.Extra["equipment_type"] = def.EquipmentType
	e.Extra["p94_instance_note"] = "registered-A"
	// This fixture represents a current runtime pants row at actor slot16,
	// not an untouched legacy slot16 (which projects as actor17).
	e.Extra["current_exe_runtime_move"] = "1"
	e.Extra["current_exe_equipment_type"] = "16"
	eq.Entries["16"] = e
	if err := f.group.Equipment.Save(ctx, eq); err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	f.service.skillCatalog = catalog
	skill, ok := catalog.Find(0, 38)
	if !ok {
		t.Fatal("real job0 skill38 missing")
	}
	if err := f.group.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1", Skills: map[int64]repo.SkillState{38: {Level: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("PVF item=%d path=%s type=%s skill38=%s grow_types=%v role_grow=33", p94Item, def.PVFPath, def.EquipmentType, skill.Name, skill.GrowTypes)
	// Preserve a legacy owner sentinel; current1426 must not mutate it.
	if err := f.group.Settings.Save(ctx, repo.SettingsRecord{Scope: skillswitch.Scope("1"), Values: map[string]string{"p94_owner_sentinel": "legacy-unchanged"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.service.stopAvatarAppearanceCoalesce(f.session, "p94_complete") })
	return f
}

func p94Snapshot(t *testing.T, f *loginWearAnchorFixture) (repo.InventoryRecord, repo.EquipmentRecord, repo.SettingsRecord, repo.SettingsRecord) {
	t.Helper()
	ctx := context.Background()
	i, _, e := f.group.Inventory.Load(ctx, "1")
	if e != nil {
		t.Fatal(e)
	}
	w, _, e := f.group.Equipment.Load(ctx, "1")
	if e != nil {
		t.Fatal(e)
	}
	n, _, e := f.group.Settings.Load(ctx, buffswap.Scope("1"))
	if e != nil {
		t.Fatal(e)
	}
	old, _, e := f.group.Settings.Load(ctx, skillswitch.Scope("1"))
	if e != nil {
		t.Fatal(e)
	}
	return i, w, n, old
}

func p94Wire(t *testing.T, wire []byte, wantList byte, wantIndex uint16, present bool, needAck bool) {
	t.Helper()
	original := append([]byte(nil), wire...)
	snapshots, acks := 0, 0
	modeAt, buffAt := -1, -1
	seq := 0
	for len(wire) > 0 {
		p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if p.Header.Classification == 1 && p.Header.MsgID == 19 {
			acks++
		}
		if p.Header.Classification == 0 && p.Header.MsgID == 2 {
			modeAt = seq
		}
		if p.Header.Classification == 0 && p.Header.MsgID == 1368 {
			snapshots++
			buffAt = seq
			// Raw class0 writes retain the fixture's server16 padding under the ordinary13 reader.
			body := p.Body
			if len(body) == 171 && bytes.Equal(body[:3], []byte{0, 0, 0}) {
				body = body[3:]
			}
			if len(body) != 168 || binary.LittleEndian.Uint16(body) != 38 || body[2] != 33 {
				t.Fatalf("invalid current snapshot %x", body)
			}
			row := body[3+int(p94Slot)*5 : 8+int(p94Slot)*5]
			want := []byte{0, p94Slot, 45, 255, 255}
			if present {
				want = []byte{1, p94Slot, wantList, byte(wantIndex), byte(wantIndex >> 8)}
			}
			if !bytes.Equal(row, want) {
				t.Fatalf("current registered row=%x want=%x", row, want)
			}
		}
		seq++
	}
	if snapshots != 1 || (needAck && acks != 1) {
		t.Fatalf("snapshot=%d op19ACK=%d wire=%x", snapshots, acks, original)
	}
	if needAck && modeAt >= 0 && buffAt < modeAt {
		t.Fatal("registration precedes actor replacement")
	}
}

func p94Register(t *testing.T, f *loginWearAnchorFixture) string {
	t.Helper()
	f.buffer.write.Reset()
	if err := f.service.handleGameCommand(f.session, 1, 1426, reviewCurrentBuffCommand(38, p94Slot, 3, uint16(p94Slot))); err != nil {
		t.Fatal(err)
	}
	p94Wire(t, f.buffer.write.Bytes(), 3, uint16(p94Slot), true, false)
	state, found, err := buffswap.Load(context.Background(), f.group, "1", "dnf:10011")
	if err != nil || !found || state.WireProfile != buffswap.CurrentWireProfile || state.Slots[p94Slot].Instance == "" {
		t.Fatalf("invalid registration %+v %v", state, err)
	}
	return state.Slots[p94Slot].Instance
}

func p94Move(t *testing.T, f *loginWearAnchorFixture, body []byte) {
	t.Helper()
	f.buffer.write.Reset()
	if err := f.service.handleGameCommand(f.session, 1, 19, body); err != nil {
		t.Fatal(err)
	}
	if f.session.hasOp19QuietPending() {
		t.Fatal("manual move was queued as login reassert")
	}
}

func p94AssertResolved(t *testing.T, f *loginWearAnchorFixture, instance string, list byte, index uint16, present bool) {
	t.Helper()
	p, err := buffswap.LoadResolvedOwned(context.Background(), f.group, "1", "dnf:10011")
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := p.State.Slots[p94Slot]
	if ok != present || (present && (binding.Instance != instance || binding.List != list || binding.Index != index || binding.ItemID != p94Item)) {
		t.Fatalf("projection=%+v omitted=%d", p.State, p.Omitted)
	}
	if !present && p.Omitted != 1 {
		t.Fatalf("missing instance omitted=%d", p.Omitted)
	}
}

func p94Cold(t *testing.T, f *loginWearAnchorFixture, list byte, index uint16, present bool) {
	t.Helper()
	conn := &bufferConn{}
	cold := &Service{options: f.service.options, initialEquipmentArchive: f.service.initialEquipmentArchive, pvfItemCatalog: f.service.pvfItemCatalog, skillCatalog: f.service.skillCatalog, repositoryProvider: func() (repo.Group, bool) { return f.group, true }}
	session := &gameSession{conn: conn, connID: "p94-cold", accountID: "dnf:10011", selectedCharacterID: 1, postFinishLoadingPlayerStateSent: true, itemListMigrationsDone: true}
	if err := cold.sendCurrentFinishLoadingCharacterState(session, "p94_cold"); err != nil {
		t.Fatal(err)
	}
	p94Wire(t, conn.write.Bytes(), list, index, present, false)
}

func TestP94BuffRegistrationExplicitUnequipCurrentPVF(t *testing.T) {
	f := p94Fixture(t)
	instance := p94Register(t, f)
	_, _, before, legacy := p94Snapshot(t, f)
	remove := flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, int16(p94Slot), int32(p94Item)), op19CurrentUIPlayerFlag)
	if !f.service.op19CurrentUIPlayerRequest(remove) {
		t.Fatal("not explicit current UI unequip")
	}
	p94Move(t, f, remove)
	inv, eq, after, old := p94Snapshot(t, f)
	if eq.Entries["16"].ItemID != 0 || inv.Slots["0:15"].ItemID != p94Item || inv.Slots["0:15"].Extra["native_buff_instance"] != instance {
		t.Fatal("real unequip missing or identity lost")
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(legacy, old) {
		t.Fatal("unequip changed registration owner")
	}
	p94AssertResolved(t, f, instance, 0, 15, true)
	p94Wire(t, f.buffer.write.Bytes(), 0, 15, true, true)
	p94Cold(t, f, 0, 15, true)
	// The reverse explicit action must retain the same registered physical instance.
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, int32(p94Item), 3, int16(p94Slot), 0), op19CurrentUIPlayerFlag))
	inv, eq, after, old = p94Snapshot(t, f)
	if eq.Entries["16"].Extra["native_buff_instance"] != instance || inv.Slots["0:15"].ItemID != 0 {
		t.Fatal("re-equip lost instance")
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(legacy, old) {
		t.Fatal("re-equip changed registration")
	}
	p94AssertResolved(t, f, instance, 3, uint16(p94Slot), true)
	p94Wire(t, f.buffer.write.Bytes(), 3, uint16(p94Slot), true, true)
	p94Cold(t, f, 3, uint16(p94Slot), true)
}

type p94FailEquip struct {
	repo.EquipmentRepository
	calls *int
}

func (r p94FailEquip) Save(ctx context.Context, v repo.EquipmentRecord) error {
	*r.calls++
	return errors.New("p94 injected equipment save failure")
}

type p94TrackInventory struct {
	repo.InventoryRepository
	calls *int
}

func (r p94TrackInventory) Save(ctx context.Context, v repo.InventoryRecord) error {
	*r.calls++
	return r.InventoryRepository.Save(ctx, v)
}

type p94FailItems struct {
	repo.CharacterItemUnitOfWork
	calls          *int
	inventoryCalls *int
}

func (u p94FailItems) WithinCharacterItems(ctx context.Context, id string, fn func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterItemUnitOfWork.WithinCharacterItems(ctx, id, func(i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return fn(p94TrackInventory{i, u.inventoryCalls}, p94FailEquip{e, u.calls})
	})
}
func TestP94BuffRegistrationUnequipSaveFailureRollback(t *testing.T) {
	f := p94Fixture(t)
	instance := p94Register(t, f)
	bi, be, bn, bo := p94Snapshot(t, f)
	calls, inventoryCalls := 0, 0
	wrapped := f.group
	wrapped.CharacterItems = p94FailItems{f.group.CharacterItems, &calls, &inventoryCalls}
	f.service.repositoryProvider = func() (repo.Group, bool) { return wrapped, true }
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, int16(p94Slot), int32(p94Item)), op19CurrentUIPlayerFlag))
	ai, ae, an, ao := p94Snapshot(t, f)
	if calls != 1 || inventoryCalls != 1 {
		t.Fatalf("save failure not reached inventory=%d equipment=%d", inventoryCalls, calls)
	}
	if !reflect.DeepEqual(bi, ai) || !reflect.DeepEqual(be, ae) || !reflect.DeepEqual(bn, an) || !reflect.DeepEqual(bo, ao) {
		t.Fatal("failed move partially changed owners")
	}
	p94AssertResolved(t, f, instance, 3, uint16(p94Slot), true)
	p94Cold(t, f, 3, uint16(p94Slot), true)
	// ownerBlockedResult deliberately has ResponseAllowed=false for save errors.
	// Assert its current contract: no invented success ACK or Buff projection.
	if f.buffer.write.Len() != 0 {
		t.Fatalf("failed transaction emitted bytes: %x", f.buffer.write.Bytes())
	}
	t.Log("late Equipment.Save failed after Inventory.Save; both Save calls reached; four persistent snapshots unchanged; no success/projection")
}

func TestP94BuffRegistrationSameTemplateMoveAndDeletion(t *testing.T) {
	f := p94Fixture(t)
	instance := p94Register(t, f)
	ctx := context.Background()
	inv, eq, before, legacy := p94Snapshot(t, f)
	// B is a distinct physical instance, never a clone of the registered identity.
	b := repo.ItemStack{ItemID: p94Item, Count: 1, RawEntry: append([]byte(nil), eq.Entries["16"].RawEntry...), Extra: map[string]string{}}
	for k, v := range eq.Entries["16"].Extra {
		if k != "native_buff_instance" {
			b.Extra[k] = v
		}
	}
	b.Extra["p94_instance_note"] = "different-B"
	binary.LittleEndian.PutUint16(b.RawEntry, 15)
	if inv.Slots == nil {
		inv.Slots = map[string]repo.ItemStack{}
	}
	inv.Slots["0:15"] = b
	if err := f.group.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, int32(p94Item), 3, int16(p94Slot), int32(p94Item)), op19CurrentUIPlayerFlag))
	inv, eq, after, old := p94Snapshot(t, f)
	if eq.Entries["16"].Extra["p94_instance_note"] != "different-B" || inv.Slots["0:15"].Extra["native_buff_instance"] != instance {
		t.Fatal("same-template swap did not move registered A")
	}
	p94AssertResolved(t, f, instance, 0, 15, true)
	p94Wire(t, f.buffer.write.Bytes(), 0, 15, true, true)
	p94Cold(t, f, 0, 15, true)
	p94Move(t, f, loginWearBridgeBody(0, 15, int32(p94Item), 0, 17, 0))
	inv, _, after, old = p94Snapshot(t, f)
	if inv.Slots["0:17"].Extra["native_buff_instance"] != instance || inv.Slots["0:15"].ItemID != 0 {
		t.Fatal("normal inventory move did not preserve A")
	}
	p94AssertResolved(t, f, instance, 0, 17, true)
	p94Cold(t, f, 0, 17, true)
	// Controlled repository transaction removes only A. This is a deletion-owner
	// consequence test, not proof of any particular sell/discard protocol handler.
	if err := f.group.CharacterItems.WithinCharacterItems(ctx, "1", func(i repo.InventoryRepository, _ repo.EquipmentRepository) error {
		r, _, e := i.Load(ctx, "1")
		if e != nil {
			return e
		}
		delete(r.Slots, "0:17")
		return i.Save(ctx, r)
	}); err != nil {
		t.Fatal(err)
	}
	p94AssertResolved(t, f, instance, 0, 0, false)
	p94Cold(t, f, 0, 0, false)
	_, eq, after, old = p94Snapshot(t, f)
	if eq.Entries["16"].ItemID != p94Item {
		t.Fatal("replacement B lost")
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(legacy, old) {
		t.Fatal("projection cleared durable binding to hide deleted instance")
	}
	t.Log(fmt.Sprintf("registered A=%s removed; same-template B retained; persisted binding retained but omitted", instance))
}

func TestP94BuffRegistrationAppearanceBoundaryNoDuplicate(t *testing.T) {
	f := p94Fixture(t)
	instance := p94Register(t, f)
	// Force the existing appearance route for this same legitimate item as a
	// controlled comparison; it already owns the Buff replay after mode0.
	f.service.options.equipAppearanceSlotGateOff = true
	p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, int16(p94Slot), int32(p94Item)), op19CurrentUIPlayerFlag))
	p94AssertResolved(t, f, instance, 0, 15, true)
	p94Wire(t, f.buffer.write.Bytes(), 0, 15, true, true)
	mode := 0
	wire := f.buffer.write.Bytes()
	for len(wire) > 0 {
		p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if p.Header.Classification == 0 && p.Header.MsgID == 2 {
			mode++
		}
	}
	if mode != 1 {
		t.Fatalf("comparison did not take existing appearance route: mode=%d", mode)
	}
}

func TestP94BuffRegistrationUnmarkedLoginReassert(t *testing.T) {
	f := p94Fixture(t)
	instance := p94Register(t, f)
	f.service.openEquipLoginWearWindow(f.session, "p94_unmarked")
	bi, be, bn, bo := p94Snapshot(t, f)
	body := loginWearBridgeBody(0, 15, 0, 3, int16(p94Slot), int32(p94Item))
	for repeat := 0; repeat < 3; repeat++ {
		p94Move(t, f, body)
		ai, ae, an, ao := p94Snapshot(t, f)
		if !reflect.DeepEqual(bi, ai) || !reflect.DeepEqual(be, ae) || !reflect.DeepEqual(bn, an) || !reflect.DeepEqual(bo, ao) {
			t.Fatal("unmarked login reassert changed owners")
		}
		p94AssertResolved(t, f, instance, 3, uint16(p94Slot), true)
		wire := f.buffer.write.Bytes()
		for len(wire) > 0 {
			p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
			wire = rest
			if p.Header.Classification == 0 && p.Header.MsgID == 1368 {
				t.Fatal("new committed-move refresh leaked into noop")
			}
		}
	}
}
