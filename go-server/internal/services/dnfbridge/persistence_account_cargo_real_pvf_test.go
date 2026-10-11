package dnfbridge

import (
	"context"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strconv"
	"strings"
	"testing"
)

// This checks real catalog-derived stack contracts, without a database/client.
func TestRealPVFPersistenceAccountCargoStack(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	def, err := catalog.ResolveItem(3227)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual PVF item=%d path=%s kind=%s limit=%d", def.ItemID, def.PVFPath, def.Kind, def.StackLimit)
	if string(def.Kind) != "stackable" {
		t.Fatal("actual item is not stackable")
	}
	extra := map[string]string{"item_kind": string(def.Kind), "pvf_path": def.PVFPath, "stack_limit": strconv.FormatInt(def.StackLimit, 10)}
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"account_cargo_level": "8"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "77", Slots: map[string]repo.ItemStack{"0:65": {ItemID: 3227, Count: 5, Extra: extra}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "a", Slots: map[string]repo.ItemStack{"12:3": {ItemID: 3227, Count: 7, Extra: extra}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfinventory.NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Move(ctx, dnfinventory.Command{AccountID: "a", SelectedCharacterID: 77, SourceListType: 0, SourceSlotIndex: 65, MoveCount: 5, DestinationListType: 12, DestinationSlotIndex: 4})
	if err != nil || result.Mode != "auto_stack" {
		t.Fatal(result, err)
	}
	cargo, _, err := r.AccountInventory.Load(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(cargo.Slots) != 1 || cargo.Slots["12:3"].Count != 12 {
		t.Fatal(cargo)
	}
	skills, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skills.Skills() {
		if strings.Contains(s.Name, "魔灵战士") {
			t.Logf("P54 actual PVF definition=%+v", s)
		}
	}
	for _, s := range skills.SkillsForJob(0) {
		if s.FixedLevelSkill {
			t.Logf("P67 actual fixed id=%d kind=%s path=%s level90=%d grow=%v second=%v", s.ID, s.Kind, s.Path, s.FixedLevelForCharacter(90), s.GrowTypes, s.SecondGrowTypes)
		}
	}
}
