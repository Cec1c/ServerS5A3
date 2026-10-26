package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
