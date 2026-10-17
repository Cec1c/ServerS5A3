package dnfbridge

import (
	"context"
	"encoding/binary"
	repo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"math"
	"os"
	"testing"
)

func goldJarCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	_, source := newMagicBoxTestCatalog(t)
	source["stackable/stackable.lst"] += "900 `gold.stk`\n"
	source["stackable/gold.stk"] = "[stackable type] `[upgradable legacy]` 0\n[minimum level] 30\n[stack limit] 1000\n[int data] 0 3500 120000 0 49 800000\n[/int data]\n"
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
func TestLotteryGoldJarCommitsGoldAndSourceTogether(t *testing.T) {
	ctx := context.Background()
	catalog := goldJarCatalog(t)
	for _, initialGold := range []int64{100, math.MaxInt32} {
		repos := memory.NewMemoryGroup()
		_ = repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": initialGold, "grow_type": 2}})
		_ = repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:75": {ItemID: 900, Count: 1}}})
		service := currentBoosterTestService(catalog, repos)
		service.options.currentClientVersion = "official-24.9"
		session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
		definition, err := service.prepareCurrentBooster(ctx, session, catalog, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75, AllowLotteryGold: true})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.commitCurrentBooster(ctx, session, catalog, definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75, AllowLotteryGold: true})
		character, _, _ := repos.Character.Load(ctx, "19")
		inv, _, _ := repos.Inventory.Load(ctx, "19")
		if initialGold == math.MaxInt32 {
			if err == nil || character.Stats["gold"] != initialGold || inv.Slots["0:75"].Count != 1 {
				t.Fatalf("partial overflow mutation: %v %+v %+v", err, character, inv)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if result.GoldAmount != 120000 && result.GoldAmount != 800000 {
			t.Fatal(result)
		}
		if character.Stats["gold"] != initialGold+int64(result.GoldAmount) || character.Stats["grow_type"] != 2 {
			t.Fatal(character)
		}
		if len(inv.Slots) != 0 || len(result.Rewards) != 0 || result.GoldAfter != uint32(character.Stats["gold"]) {
			t.Fatalf("gold became item: %+v %+v", inv, result)
		}
		body, err := service.buildCurrentLotteryResultBody(ctx, session, catalog, result)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) != 1+2+119+4 || body[0] != 1 || binary.LittleEndian.Uint16(body[1:3]) != 75 || binary.LittleEndian.Uint32(body[122:126]) != result.GoldAmount {
			t.Fatalf("ack=%x", body)
		}
		if _, err = service.commitCurrentBooster(ctx, session, catalog, definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75, AllowLotteryGold: true}); err == nil {
			t.Fatal("reused missing jar")
		}
		after, _, _ := repos.Character.Load(ctx, "19")
		if after.Stats["gold"] != character.Stats["gold"] {
			t.Fatal("repeated missing jar grants gold")
		}
	}
}
func TestLotteryGoldRowsDoNotTurnOrdinaryBoosterPlaceholdersIntoCurrency(t *testing.T) {
	group, err := weightedMagicBoxBoosterGroup([]int64{1, 0, 100, 200}, 3)
	if err != nil || len(group.Entries) != 0 {
		t.Fatalf("ordinary placeholder semantics changed: %+v %v", group, err)
	}
	catalog := goldJarCatalog(t)
	definition, err := resolveCurrentBoosterDefinition(catalog, 900, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	amounts, _, err := resolveCurrentBoosterRewardAmounts(definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, func(int64) (int64, error) { return 0, nil })
	if err != nil || amounts[0] != 120000 {
		t.Fatalf("%v %v", amounts, err)
	}
	definition.StackableType = "[booster]"
	if _, _, err = resolveCurrentBoosterRewardAmounts(definition, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom}, func(int64) (int64, error) { return 0, nil }); err == nil {
		t.Fatal("accepted arbitrary currency booster")
	}
}
func TestRealPVF7616GoldLotteryPool(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("paired PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := resolveCurrentBoosterDefinition(catalog, 7616, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Random.Groups) != 1 {
		t.Fatal(definition)
	}
	group := definition.Random.Groups[0]
	if len(group.Entries) == 0 {
		t.Fatal("currency entries discarded")
	}
	var total int64
	for _, entry := range group.Entries {
		if entry.ItemID != 0 || entry.Count <= 0 || entry.Count > 1000000 || entry.Weight <= 0 {
			t.Fatal(entry)
		}
		total += entry.Weight
	}
	if total <= 0 {
		t.Fatal("empty weighted gold pool")
	}
	t.Logf("7616 currency rows=%d total_weight=%d", len(group.Entries), total)
}

// Inject the failure after the jar write, while the owner's transaction still
// holds both aggregates. A failed character save must restore the jar as well.
type failingGoldCharacterRepository struct{ repo.CharacterRepository }

func (r failingGoldCharacterRepository) Save(context.Context, repo.CharacterRecord) error {
	return os.ErrPermission
}

type failingGoldUnit struct {
	base repo.AccountCharacterAssetUnitOfWork
}

func (u failingGoldUnit) WithinAccountCharacterAssets(ctx context.Context, account, character string, apply func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	return u.base.WithinAccountCharacterAssets(ctx, account, character, func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
		return apply(a, failingGoldCharacterRepository{c}, i, e)
	})
}
func TestLotteryGoldJarRollbackWhenWalletPersistenceFails(t *testing.T) {
	ctx := context.Background()
	catalog := goldJarCatalog(t)
	repos := memory.NewMemoryGroup()
	_ = repos.Character.Save(ctx, repo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90, Stats: map[string]int64{"gold": 100}})
	_ = repos.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "19", Slots: map[string]repo.ItemStack{"0:75": {ItemID: 900, Count: 1}}})
	repos.AccountAssets = failingGoldUnit{repos.AccountAssets}
	s := currentBoosterTestService(catalog, repos)
	s.options.currentClientVersion = "official-24.9"
	def, err := resolveCurrentBoosterDefinition(catalog, 900, currentBoosterRequestRandom)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.commitCurrentBooster(ctx, &gameSession{selectedCharacterID: 19}, catalog, def, currentBoosterOpenRequest{Kind: currentBoosterRequestRandom, SourceSlot: 75, AllowLotteryGold: true})
	if err == nil {
		t.Fatal("missing write failure")
	}
	character, _, _ := repos.Character.Load(ctx, "19")
	inv, _, _ := repos.Inventory.Load(ctx, "19")
	if character.Stats["gold"] != 100 || inv.Slots["0:75"].Count != 1 {
		t.Fatalf("partial commit: %+v %+v", character, inv)
	}
}
