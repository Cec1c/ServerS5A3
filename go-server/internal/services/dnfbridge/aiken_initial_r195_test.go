package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func r195InitialAikenSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst":                           "",
		"stackable/stackable.lst":                       "",
		"equipment/equipment.lst":                       "9001 `test.equ`\n9002 `normal.equ`",
		"equipment/test.equ":                            "[equipment type] `[pants]`\n[durability] 32\n[custom ability type] `warriors dungeon`\n[custom ability count] 2",
		"equipment/normal.equ":                          "[equipment type] `[pants]`\n[durability] 32",
		"etc/customability/individualcustominfo.etc":    "[custom ability type] `warriors dungeon`\n[group]\n[key] `1`\n[indexes] 1 2\n[/indexes]\n[/group]\n[group]\n[key] `2`\n[indexes] 13\n[/indexes]\n[/group]\n[grade probability] `A` 200 `B` 800 `C` 3000 `D` 6000\n[/grade probability]",
		"etc/customability/individualcustomability.lst": "1 `Options/a.etc`\n2 `Options/b.etc`\n13 `Options/c.etc`",
	}
}

func r195AikenDefinition(t *testing.T) (*pvfDungeonDropCatalog, dungeonDropItemDefinition) {
	t.Helper()
	c, err := newPVFDungeonDropCatalog(r195InitialAikenSource())
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.ResolveItem(9001)
	if err != nil {
		t.Fatal(err)
	}
	return c, d
}

func TestR195AikenInitialGroundPickupAndProjection(t *testing.T) {
	c, definition := r195AikenDefinition(t)
	ground, err := currentDungeonDeathDropItemState(3, definition, 1, 554884773)
	if err != nil {
		t.Fatal(err)
	}
	freezeCurrentAikenInitialDrop(&definition, ground)
	want := definition.AikenInitialState
	if !definition.AikenInitial.Valid(want) {
		t.Fatal("ground has no valid options", want)
	}
	cached, err := c.ResolveItem(9001)
	if err != nil || cached.AikenInitialState != [5]byte{} {
		t.Fatal("instance leaked into static catalog")
	}
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	slot, projected, err := grantCurrentDungeonPickupItem(ctx, g, "19", definition, 1, time.Now(), 554884773)
	if err != nil {
		t.Fatal(err)
	}
	inv, _, _ := g.Inventory.Load(ctx, "19")
	s := inv.Slots[currentDungeonPickupMainSlotKey(int16(slot))]
	state, err := currentAikenState(s)
	if err != nil || state != want || !bytes.Equal(projected.data[0x72:0x77], want[:]) {
		t.Fatalf("ground/pickup state drift: %v %v", state, err)
	}
	for _, list := range []byte{0, 2, 3, 12} {
		cold := currentItemListEntryFromStack(list, int16(slot), s)
		if !bytes.Equal(cold.data[0x72:0x77], want[:]) || binary.LittleEndian.Uint32(cold.data[6:10]) != 554884773 {
			t.Fatal("cold projection changed state", list)
		}
	}
	for n := 0; n < 10; n++ {
		retry, err := buildCurrentDungeonPickupStack(9, definition, 1, 554884773)
		state, readErr := currentAikenState(retry)
		if err != nil || readErr != nil || state != want {
			t.Fatal("retry rerolled ground instance")
		}
	}
	plain, err := c.ResolveItem(9002)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := buildCurrentDungeonPickupStack(10, plain, 1, 554884773)
	if err != nil || !bytes.Equal(stack.RawEntry[0x72:0x77], make([]byte, 5)) {
		t.Fatal("ordinary equipment acquired Aiken state")
	}
	bad := definition
	bad.AikenInitialState[0] = 255
	if _, err := buildCurrentDungeonPickupStack(9, bad, 1, 554884773); err == nil {
		t.Fatal("invalid frozen state accepted")
	}
}

type r195AikenRepairFailure struct {
	repo.AccountCharacterAssetUnitOfWork
}

func (u r195AikenRepairFailure) WithinAccountCharacterAssets(ctx context.Context, a, c string, f func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, a, c, func(ar repo.AccountInventoryRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository) error {
		if err := f(ar, cr, ir, er); err != nil {
			return err
		}
		return errors.New("injected final commit failure")
	})
}

func TestR195AikenInitialRepairPreservesPaidGradeAndIsAtomic(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit-and-repeat", true: "rollback"}[fault], func(t *testing.T) {
			c, d := r195AikenDefinition(t)
			ctx := context.Background()
			g := memory.NewMemoryGroup()
			s, err := buildCurrentDungeonPickupStack(9, d, 1, 554884773)
			if err != nil {
				t.Fatal(err)
			}
			s, err = currentAikenWriteState(s, 9, [5]byte{0, 0, 0, 1, 1})
			if err != nil {
				t.Fatal(err)
			}
			s.Extra["equipment_lock_state"] = "1"
			s.Extra["preserve"] = "yes"
			known, _ := currentAikenWriteState(s, 10, [5]byte{1, 13, 0, 1, 3})
			partial, _ := currentAikenWriteState(s, 11, [5]byte{1, 0, 0, 0, 2})
			if err := g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "a"}); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:9": s, "0:10": known, "0:11": partial}, Warehouse: map[string]repo.ItemStack{"2:1": s}}); err != nil {
				t.Fatal(err)
			}
			if err := g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "19", Entries: map[string]repo.EquipmentEntry{"14": {SlotIndex: 14, ItemID: s.ItemID, RawEntry: s.RawEntry, Extra: s.Extra}}}); err != nil {
				t.Fatal(err)
			}
			if err := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"12:1": s}}); err != nil {
				t.Fatal(err)
			}
			before, _, _ := g.Inventory.Load(ctx, "19")
			beforeW, _, _ := g.Equipment.Load(ctx, "19")
			beforeA, _, _ := g.AccountInventory.Load(ctx, "a")
			provided := g
			if fault {
				provided.AccountAssets = r195AikenRepairFailure{g.AccountAssets}
			}
			o, _ := aiken.NewOwner(provided)
			cmd := aiken.InitialRepairCommand{AccountID: "a", CharacterID: "19", Now: time.Now(), ReadState: currentAikenState, WriteState: currentAikenWriteState, Roll: func(uint32) (uint32, error) { return 0, nil }, Catalog: func(id int64) (*aiken.InitialCatalog, error) {
				v, e := c.ResolveItem(uint32(id))
				return v.AikenInitial, e
			}}
			n, err := o.RepairInitialOptions(ctx, cmd)
			after, _, _ := g.Inventory.Load(ctx, "19")
			afterW, _, _ := g.Equipment.Load(ctx, "19")
			afterA, _, _ := g.AccountInventory.Load(ctx, "a")
			if fault {
				if err == nil || n != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeW, afterW) || !reflect.DeepEqual(beforeA, afterA) {
					t.Fatal("repair did not roll back all assets")
				}
				return
			}
			if err != nil || n != 4 {
				t.Fatal(n, err)
			}
			if !reflect.DeepEqual(known, after.Slots["0:10"]) || !reflect.DeepEqual(partial, after.Slots["0:11"]) {
				t.Fatal("already selected instance changed")
			}
			for _, repaired := range []repo.ItemStack{after.Slots["0:9"], after.Warehouse["2:1"], afterA.Slots["12:1"], {ItemID: s.ItemID, RawEntry: afterW.Entries["14"].RawEntry, Extra: afterW.Entries["14"].Extra}} {
				state, err := currentAikenState(repaired)
				if err != nil || state[0] == 0 || state[1] == 0 || state[3] != 1 || state[4] != 1 || repaired.Extra["equipment_lock_state"] != "1" || repaired.Extra["preserve"] != "yes" {
					t.Fatal("grade/set/lock/extra lost", state, err)
				}
			}
			if n, err := o.RepairInitialOptions(ctx, cmd); err != nil || n != 0 {
				t.Fatal("repeat changed repaired instance", n, err)
			}
			cmd.AccountID = "other"
			if n, err := o.RepairInitialOptions(ctx, cmd); err == nil || n != 0 {
				t.Fatal("wrong account accepted")
			}
		})
	}
}

func TestR195RealAikenInitialDrops(t *testing.T) {
	c, err := newPVFDungeonDropCatalog(aikenR142Archive(t))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, id := range []uint32{100120561, 100220536, 100352922, 100390016} {
		d, err := c.ResolveItem(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, draw := range []uint32{0, 5999, 6000, 8999, 9000, 9799, 9800, 9999} {
			state, err := d.AikenInitial.Roll(func(bound uint32) (uint32, error) { return draw % bound, nil })
			if err != nil || !d.AikenInitial.Valid(state) {
				t.Fatal(state, err)
			}
			d.AikenInitialState = state
			ground, err := currentDungeonDeathDropItemState(3, d, 1, 554884773)
			if err != nil {
				t.Fatal(err)
			}
			stack, err := buildCurrentDungeonPickupStack(9, d, 1, 554884773)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(ground.data[0x72:0x77], stack.RawEntry[0x72:0x77]) {
				t.Fatal("actual PVF state drift")
			}
			g0, ok0 := d.AikenInitial.OptionGroup(state[0])
			g1, ok1 := d.AikenInitial.OptionGroup(state[1])
			if !ok0 || !ok1 {
				t.Fatal("dropdown group absent")
			}
			rows = append(rows, map[string]any{"item": id, "draw": draw, "state": []int{int(state[0]), int(state[1]), int(state[2]), int(state[3]), int(state[4])}, "groups": []int{g0, g1}})
		}
	}
	if out := os.Getenv("R195_AIKEN_STATE_OUT"); out != "" {
		if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(rows, "", "  ")
		if err := os.WriteFile(out, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
