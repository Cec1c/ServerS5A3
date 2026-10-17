package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"testing"

	chronicle "longheng.io/server/internal/modules/dnf/chronicle"
	repo "longheng.io/server/internal/modules/dnf/repository"
	pvf "longheng.io/server/internal/platform/pvf"
)

// This is an isolated replay of the captured 00:37:58 request and the two
// read-only inventory rows. It never uses or mutates the player's database.
func TestChronicleRealPVFLv70RingCapturedRequest(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("paired PVF required")
	}
	archive, err := pvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("catalog", func(t *testing.T) {
		e, err := items.currentChronicleCatalog().Equipment(425064)
		if err != nil {
			t.Fatalf("real level-70 ring without durability rejected: %v", err)
		}
		m, err := items.currentChronicleCatalog().Material(1255)
		if err != nil || !m.Allows(0, 1, e, 14) {
			t.Fatalf("captured legitimate blue-aura option rejected: %+v %v", e, err)
		}
	})
	t.Run("handler", func(t *testing.T) {
		s, session, conn, g := chronicleFixture(t, chronicleFixtureSource(), 1255)
		s.pvfItemCatalog = items
		if err := g.Settings.Save(context.Background(), repo.SettingsRecord{Scope: repo.CharacterContainerStateScope("2"), Values: map[string]string{"main_list_param16": "24", "avatar_list_param16": "56", "personal_cargo_list_param16": "200"}}); err != nil {
			t.Fatal(err)
		}
		if err := g.Character.Save(context.Background(), repo.CharacterRecord{CharacterID: "2", AccountID: "account-1", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 33}}); err != nil {
			t.Fatal(err)
		}
		if err := g.Inventory.Save(context.Background(), repo.InventoryRecord{CharacterID: "2", Slots: map[string]repo.ItemStack{
			"0:34": {ItemID: 425064, Count: 1, Extra: map[string]string{"item_kind": "equipment", "equipment_type": "[ring]", "pvf_path": "equipment/character/common/ring/ring_3choro65.equ", "quality_seed": "492210289"}},
			"0:98": {ItemID: 1255, Count: 20, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/consumption_1255.stk", "stack_limit": "1000", "stackable_type": "[etc]"}},
		}}); err != nil {
			t.Fatal(err)
		}
		body, _ := hex.DecodeString("6200e7040000002200687c06000e")
		if err := s.handleCurrentChronicleRefine(session, body); err != nil {
			t.Fatal(err)
		}
		ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{1, 98, 0, 19, 0, 1, 0, 34, 0, 0}) || len(rest) == 0 {
			t.Fatalf("captured legitimate ring request rejected or unrefreshed: %x", ack.Body)
		}
		inv, _, err := g.Inventory.Load(context.Background(), "2")
		if err != nil {
			t.Fatal(err)
		}
		state, err := currentChronicleItemState(inv.Slots["0:34"])
		want := chronicle.Option{AuraID: 1255, Job: 0, Grow: 1, EquipmentType: 21, Number: 14}
		if err != nil || len(state.Options) != 1 || state.Options[0] != want || inv.Slots["0:98"].Count != 19 {
			t.Fatalf("captured ring mutation incorrect: %+v %v", state, err)
		}
	})
}
