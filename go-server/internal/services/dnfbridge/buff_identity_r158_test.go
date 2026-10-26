package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Both records project the same actual pants actor slot16. Legacy PVF rows
// retain storage slot15 until the existing equipment owner migrates them.
func d20Fixture(t *testing.T, legacy bool) *loginWearAnchorFixture {
	t.Helper()
	f := p94Fixture(t)
	e, _, err := f.group.Equipment.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	a := e.Entries["16"]
	if legacy {
		delete(a.Extra, "current_exe_runtime_move")
		delete(a.Extra, "current_exe_equipment_type")
		delete(e.Entries, "16")
		a.SlotIndex = 15
		a.Extra["source"] = "pvf_create_equipment_list"
		a.Extra["equipped_slot"] = "15"
		a.Extra["equipment_slot"] = "15"
		binary.LittleEndian.PutUint16(a.RawEntry, 15)
		e.Entries["15"] = a
	} else {
		a.Extra["current_exe_runtime_move"] = "1"
		a.Extra["current_exe_equipment_type"] = "16"
		e.Entries["16"] = a
	}
	if slot, ok := currentEXEActorEquipmentSlot(a); !ok || slot != 16 {
		t.Fatal("fixture native slot", slot, ok)
	}
	if err := f.group.Equipment.Save(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestD20BuffNativeIdentityAcrossActualEquipmentHandlers(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "current"
		if legacy {
			name = "legacy_pvf"
		}
		t.Run(name, func(t *testing.T) {
			f := d20Fixture(t, legacy)
			ctx := context.Background()
			// Exact client coordinate, deliberately distinct from legacy storage key.
			if err := f.service.handleGameCommand(f.session, 1, 1426, reviewCurrentBuffCommand(38, 16, 3, 16)); err != nil {
				t.Fatal(err)
			}
			state, found, err := buffswap.Load(ctx, f.group, "1", "dnf:10011")
			if err != nil || !found || state.Slots[16].Instance == "" {
				t.Fatalf("native worn16 registration failed: found=%v state=%+v err=%v", found, state, err)
			}
			instance := state.Slots[16].Instance
			p94Wire(t, f.buffer.write.Bytes(), 3, 16, true, false)
			d20Export(t, f, "registered", instance)
			p94Cold(t, f, 3, 16, true)
			inv, eq, before, oldBefore := p94Snapshot(t, f)
			key := "16"
			if legacy {
				key = "15"
			}
			a := eq.Entries[key]
			if a.Extra["native_buff_instance"] != instance {
				t.Fatal("registered wrong physical item")
			}
			b := repo.ItemStack{ItemID: p94Item, Count: 1, RawEntry: append([]byte(nil), a.RawEntry...), Extra: map[string]string{}}
			for k, v := range a.Extra {
				if k != "native_buff_instance" {
					b.Extra[k] = v
				}
			}
			b.Extra["p94_instance_note"] = "unregistered-B"
			binary.LittleEndian.PutUint16(b.RawEntry, 15)
			if inv.Slots == nil {
				inv.Slots = map[string]repo.ItemStack{}
			}
			inv.Slots["0:15"] = b
			if err := f.group.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			// Wear B, leaving registered A in bag15. B must never inherit A's binding.
			p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, int32(p94Item), 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag))
			inv, eq, after, oldAfter := p94Snapshot(t, f)
			if inv.Slots["0:15"].Extra["native_buff_instance"] != instance || eq.Entries["16"].Extra["p94_instance_note"] != "unregistered-B" {
				t.Fatal("actual same-template swap failed")
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(oldBefore, oldAfter) {
				t.Fatal("wear changed durable registration")
			}
			p94AssertResolved(t, f, instance, 0, 15, true)
			p94Wire(t, f.buffer.write.Bytes(), 0, 15, true, true)
			d20Export(t, f, "wear_B", instance)
			p94Cold(t, f, 0, 15, true)
			// Unwear B has no effect on A; then wear A again and resolve the same ID.
			p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 17, 0, 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag))
			p94AssertResolved(t, f, instance, 0, 15, true)
			p94Wire(t, f.buffer.write.Bytes(), 0, 15, true, true)
			d20Export(t, f, "unwear_B", instance)
			p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, int32(p94Item), 3, 16, 0), op19CurrentUIPlayerFlag))
			p94AssertResolved(t, f, instance, 3, 16, true)
			p94Wire(t, f.buffer.write.Bytes(), 3, 16, true, true)
			d20Export(t, f, "rewear_A", instance)
			p94Cold(t, f, 3, 16, true)
		})
	}
}

func d20Export(t *testing.T, f *loginWearAnchorFixture, stage, instance string) {
	t.Helper()
	dir := os.Getenv("DNF90_D20_NATIVE_DIR")
	if dir == "" {
		return
	}
	var body []byte
	for wire := f.buffer.write.Bytes(); len(wire) > 0; {
		p, rest := splitCurrentGameServerUpperPacketAuto(t, wire)
		wire = rest
		if p.Header.MsgID == 1368 {
			body = append([]byte(nil), p.Body...)
			if len(body) == 171 {
				body = body[3:]
			}
		}
	}
	if len(body) != 168 {
		t.Fatal("native export requires actual1368")
	}
	type location struct {
		List     byte   `json:"list"`
		Index    uint16 `json:"index"`
		Identity string `json:"identity"`
	}
	var locations []location
	inv, eq, _, _ := p94Snapshot(t, f)
	for key, stack := range inv.Slots {
		list, index, ok := repo.ParseCurrentInventoryLocation(key)
		if !ok || list != 0 || stack.ItemID != p94Item {
			continue
		}
		id := stack.Extra["native_buff_instance"]
		if id == "" {
			id = "unregistered-B"
		}
		locations = append(locations, location{byte(list), uint16(index), id})
	}
	for _, entry := range eq.Entries {
		if entry.ItemID != p94Item {
			continue
		}
		index, ok := currentEXEActorEquipmentSlot(entry)
		if !ok {
			t.Fatal("unprojectable fixture")
		}
		id := entry.Extra["native_buff_instance"]
		if id == "" {
			id = "unregistered-B"
		}
		locations = append(locations, location{3, uint16(index), id})
	}
	data, err := json.MarshalIndent(map[string]any{"stage": stage, "body_hex": hex.EncodeToString(body), "locations": locations, "expected_identity": instance}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "-")+"-"+stage+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestD20BuffExistingLegacyIdentityColdProjection(t *testing.T) {
	for _, view := range []string{"snapshot1368", "userinfo"} {
		t.Run(view, func(t *testing.T) {
			f := d20Fixture(t, true)
			ctx := context.Background()
			// Canonical-domain registration seeds a previously stored identity. This
			// control is not presented as a current1426 request with storage coordinates.
			if err := buffswap.ApplyCurrent(ctx, f.group, "1", "dnf:10011", buffswap.Command{Skill: 38, Slot: 16, List: 3, Index: 15}); err != nil {
				t.Fatal(err)
			}
			_, _, before, _ := p94Snapshot(t, f)
			if view == "snapshot1368" {
				p94Cold(t, f, 3, 16, true)
			} else {
				reader := csharpLegacyUserInfoReader{ctx: ctx, service: f.service, session: f.session, characterID: "1"}
				var w packetWriter
				reader.writeNativeBuffSwapUserInfo(&w)
				body := w.bytes()
				if len(body) != 126 || body[2] != 1 || body[3] != 16 || body[4] != 3 || binary.LittleEndian.Uint16(body[5:7]) != 16 || binary.LittleEndian.Uint16(body[7:9]) != 16 || binary.LittleEndian.Uint32(body[9:13]) != uint32(p94Item) {
					t.Fatalf("same registered instance omitted/misaddressed in login block: %x", body)
				}
			}
			_, _, after, _ := p94Snapshot(t, f)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("projection rewrote identity")
			}
		})
	}
}

type d20FailSettings struct {
	repo.SettingsRepository
	calls *int
}

func (r d20FailSettings) Save(ctx context.Context, v repo.SettingsRecord) error {
	*r.calls++
	return errors.New("D20 injected settings failure")
}

type d20FailRegistration struct {
	repo.CeraShopAssetUnitOfWork
	stage string
	calls *int
}

func (u d20FailRegistration) WithinCeraShopAssets(ctx context.Context, a, c, s string, fn func(repo.AccountRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository, repo.SettingsRepository) error) error {
	return u.CeraShopAssetUnitOfWork.WithinCeraShopAssets(ctx, a, c, s, func(ar repo.AccountRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository, sr repo.SettingsRepository) error {
		if u.stage == "save" {
			return fn(ar, cr, ir, er, d20FailSettings{sr, u.calls})
		}
		if err := fn(ar, cr, ir, er, sr); err != nil {
			return err
		}
		*u.calls++
		return errors.New("D20 injected final transaction failure")
	})
}
func TestD20BuffRegistrationRollback(t *testing.T) {
	for _, stage := range []string{"save", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := d20Fixture(t, true)
			bi, be, bn, bo := p94Snapshot(t, f)
			calls := 0
			wrapped := f.group
			wrapped.CeraShopAssets = d20FailRegistration{f.group.CeraShopAssets, stage, &calls}
			f.service.repositoryProvider = func() (repo.Group, bool) { return wrapped, true }
			if err := f.service.handleGameCommand(f.session, 1, 1426, reviewCurrentBuffCommand(38, 16, 3, 16)); err != nil {
				t.Fatal(err)
			}
			ai, ae, an, ao := p94Snapshot(t, f)
			if calls != 1 || !reflect.DeepEqual(bi, ai) || !reflect.DeepEqual(be, ae) || !reflect.DeepEqual(bn, an) || !reflect.DeepEqual(bo, ao) || f.buffer.write.Len() != 0 {
				t.Fatalf("registration rollback failed calls=%d bytes=%d", calls, f.buffer.write.Len())
			}
		})
	}
}

func TestD20BuffWornSourceAliasRejected(t *testing.T) {
	f := d20Fixture(t, true)
	ctx := context.Background()
	eq, _, _ := f.group.Equipment.Load(ctx, "1")
	copyRecord := repo.CloneEquipment(eq)
	b := copyRecord.Entries["15"]
	b.SlotIndex = 16
	b.Extra["current_exe_runtime_move"] = "1"
	b.Extra["current_exe_equipment_type"] = "16"
	eq.Entries["16"] = b
	if err := f.group.Equipment.Save(ctx, eq); err != nil {
		t.Fatal(err)
	}
	bi, be, bn, bo := p94Snapshot(t, f)
	if err := f.service.handleGameCommand(f.session, 1, 1426, reviewCurrentBuffCommand(38, 16, 3, 16)); err != nil {
		t.Fatal(err)
	}
	ai, ae, an, ao := p94Snapshot(t, f)
	if !reflect.DeepEqual(bi, ai) || !reflect.DeepEqual(be, ae) || !reflect.DeepEqual(bn, an) || !reflect.DeepEqual(bo, ao) || f.buffer.write.Len() != 0 {
		t.Fatal("ambiguous native slot selected an arbitrary physical item")
	}
}

type d20FailMove struct {
	repo.CharacterItemUnitOfWork
	calls *int
}

func (u d20FailMove) WithinCharacterItems(ctx context.Context, id string, fn func(repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.CharacterItemUnitOfWork.WithinCharacterItems(ctx, id, func(i repo.InventoryRepository, e repo.EquipmentRepository) error {
		if err := fn(i, e); err != nil {
			return err
		}
		*u.calls++
		return errors.New("D20 final move failure")
	})
}
func TestD20BuffLegacyMoveRollbackKeepsIdentity(t *testing.T) {
	for _, stage := range []string{"save", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := d20Fixture(t, true)
			if err := f.service.handleGameCommand(f.session, 1, 1426, reviewCurrentBuffCommand(38, 16, 3, 16)); err != nil {
				t.Fatal(err)
			}
			state, found, err := buffswap.Load(context.Background(), f.group, "1", "dnf:10011")
			if err != nil || !found || state.Slots[16].Instance == "" {
				t.Fatal("valid registration prerequisite", err)
			}
			bi, be, bn, bo := p94Snapshot(t, f)
			calls, inventoryCalls := 0, 0
			wrapped := f.group
			if stage == "save" {
				wrapped.CharacterItems = p94FailItems{f.group.CharacterItems, &calls, &inventoryCalls}
			} else {
				wrapped.CharacterItems = d20FailMove{f.group.CharacterItems, &calls}
			}
			f.service.repositoryProvider = func() (repo.Group, bool) { return wrapped, true }
			p94Move(t, f, flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, 16, int32(p94Item)), op19CurrentUIPlayerFlag))
			ai, ae, an, ao := p94Snapshot(t, f)
			if calls != 1 || !reflect.DeepEqual(bi, ai) || !reflect.DeepEqual(be, ae) || !reflect.DeepEqual(bn, an) || !reflect.DeepEqual(bo, ao) || f.buffer.write.Len() != 0 {
				t.Fatalf("move rollback failed calls=%d inventorySave=%d bytes=%d", calls, inventoryCalls, f.buffer.write.Len())
			}
			f.service.repositoryProvider = func() (repo.Group, bool) { return f.group, true }
			p94Cold(t, f, 3, 16, true)
		})
	}
}
