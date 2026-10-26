package dnfbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const realPVFDimensionExtraAccount = "real-pvf-dimension-extra"

func openRealPVFDimensionExtraCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func newRealPVFDimensionExtraFixture(t *testing.T, slots map[string]dnfrepo.ItemStack) (dnfrepo.Group, *dnfdungeon.Owner) {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   realPVFDimensionExtraAccount,
		Level:       90,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: slots}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return repos, owner
}

func realPVFDimensionExtraSnapshot(t *testing.T, repos dnfrepo.Group) (dnfrepo.CharacterRecord, dnfrepo.InventoryRecord) {
	t.Helper()
	ctx := context.Background()
	character, found, err := repos.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	inventory, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return dnfrepo.CloneCharacter(character), dnfrepo.CloneInventory(inventory)
}

func realPVFDimensionExtraCommand(now time.Time, ticketID, materialID, materialCount int64) dnfdungeon.DimensionGateExtraEntryCommand {
	return dnfdungeon.DimensionGateExtraEntryCommand{
		CharacterID:    "19",
		AccountID:      realPVFDimensionExtraAccount,
		Slot:           20,
		ItemID:         ticketID,
		Count:          1,
		MaterialItemID: materialID,
		MaterialCount:  materialCount,
		BaseLimit:      5,
		ExtraLimit:     5,
		MinimumLevel:   70,
		Cooldown:       10 * time.Second,
		UpdatedAt:      now,
	}
}

// The real PVF is the source of the material pair: 10158126 declares
// `[need material] 3228 60`. This test parses that actual document, feeds the
// resulting explicit fields into the domain transaction, and proves the
// shortage, success and no-material paths. It is not native packet wiring.
func TestRealPVFDimensionExtraTicketMaterialTransaction(t *testing.T) {
	catalog := openRealPVFDimensionExtraCatalog(t)
	requirements := make(map[int64][2]int64)
	for _, testCase := range []struct {
		itemID     int64
		wantPath   string
		wantItemID int64
		wantCount  int64
	}{
		{10158125, "stackable/10158001/10158125.stk", 0, 0},
		{10158126, "stackable/10158001/10158126.stk", 3228, 60},
		{10158692, "stackable/10158001/10158692.stk", 0, 0},
	} {
		definition, err := catalog.ResolveItem(uint32(testCase.itemID))
		if err != nil {
			t.Fatalf("resolve item=%d: %v", testCase.itemID, err)
		}
		if definition.Kind != dungeonDropItemStackable || definition.PVFPath != testCase.wantPath {
			t.Fatalf("item=%d kind=%s path=%s, want stackable %s", testCase.itemID, definition.Kind, definition.PVFPath, testCase.wantPath)
		}
		materialID, materialCount := dnfdungeon.DimensionGateExtraMaterialRequirement(definition.NeedMaterials)
		requirements[testCase.itemID] = [2]int64{materialID, materialCount}
		if materialID != testCase.wantItemID || materialCount != testCase.wantCount {
			t.Fatalf("item=%d [need material]=%v resolved=%d x %d, want %d x %d", testCase.itemID, definition.NeedMaterials, materialID, materialCount, testCase.wantItemID, testCase.wantCount)
		}
	}

	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	t.Run("shortage", func(t *testing.T) {
		material := requirements[10158126]
		repos, owner := newRealPVFDimensionExtraFixture(t, map[string]dnfrepo.ItemStack{
			"0:20": {ItemID: 10158126, Count: 1},
			"0:30": {ItemID: material[0], Count: material[1] - 1},
		})
		beforeCharacter, beforeInventory := realPVFDimensionExtraSnapshot(t, repos)
		result, err := owner.GrantDimensionGateExtraEntry(context.Background(), realPVFDimensionExtraCommand(now, 10158126, material[0], material[1]))
		if !errors.Is(err, dnfdungeon.ErrEntryItemConsumeInsufficient) {
			t.Fatalf("err=%v, want insufficient material", err)
		}
		if !reflect.DeepEqual(result, dnfdungeon.DimensionGateExtraEntryResult{}) {
			t.Fatalf("result=%+v, want zero result", result)
		}
		afterCharacter, afterInventory := realPVFDimensionExtraSnapshot(t, repos)
		if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
			t.Fatal("shortage changed the real-PVF ticket, material or daily grant")
		}
	})

	t.Run("success", func(t *testing.T) {
		material := requirements[10158126]
		repos, owner := newRealPVFDimensionExtraFixture(t, map[string]dnfrepo.ItemStack{
			"0:20": {ItemID: 10158126, Count: 1},
			"0:30": {ItemID: material[0], Count: material[1]},
		})
		result, err := owner.GrantDimensionGateExtraEntry(context.Background(), realPVFDimensionExtraCommand(now, 10158126, material[0], material[1]))
		if err != nil {
			t.Fatal(err)
		}
		character, inventory := realPVFDimensionExtraSnapshot(t, repos)
		if _, found := inventory.Slots["0:20"]; found {
			t.Fatal("ticket stack was not consumed")
		}
		if _, found := inventory.Slots["0:30"]; found {
			t.Fatal("material stack was not consumed")
		}
		used, granted, total, err := dnfdungeon.DimensionGateDailyAllowance(character, now, 5, 5)
		if err != nil || used != 0 || granted != 1 || total != 6 {
			t.Fatalf("used=%d granted=%d total=%d err=%v, want 0/1/6", used, granted, total, err)
		}
		if result.Granted != 1 || result.Total != 6 || len(result.MaterialSlots) != 1 || !result.MaterialSlots[0].Removed {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("no_material_ticket", func(t *testing.T) {
		material := requirements[10158125]
		if material[0] != 0 || material[1] != 0 {
			t.Fatalf("10158125 material=%v, want none", material)
		}
		repos, owner := newRealPVFDimensionExtraFixture(t, map[string]dnfrepo.ItemStack{
			"0:20": {ItemID: 10158125, Count: 1},
			"0:30": {ItemID: 3228, Count: 60},
		})
		result, err := owner.GrantDimensionGateExtraEntry(context.Background(), realPVFDimensionExtraCommand(now, 10158125, material[0], material[1]))
		if err != nil {
			t.Fatal(err)
		}
		character, inventory := realPVFDimensionExtraSnapshot(t, repos)
		if _, found := inventory.Slots["0:20"]; found {
			t.Fatal("ticket stack was not consumed")
		}
		if inventory.Slots["0:30"].Count != 60 {
			t.Fatalf("no-material ticket consumed unrelated material: count=%d", inventory.Slots["0:30"].Count)
		}
		_, granted, _, err := dnfdungeon.DimensionGateDailyAllowance(character, now, 5, 5)
		if err != nil || granted != 1 || len(result.MaterialSlots) != 0 {
			t.Fatalf("result=%+v granted=%d err=%v", result, granted, err)
		}
	})
}
