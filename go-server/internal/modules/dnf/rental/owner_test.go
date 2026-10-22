package rental

import (
	"context"
	"errors"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

type rentalGuideOwnerTestSource map[string]string

func (s rentalGuideOwnerTestSource) ReadText(path string) (string, error) {
	return s[path], nil
}

func rentalGuideOwnerTestCatalog(t *testing.T, questType string) *dnfquest.Catalog {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), rentalGuideOwnerTestSource{
		dnfquest.DefaultList: "8581 `common/chn_system_10_starpoint_guide.qst`\n",
		"n_quest/common/chn_system_10_starpoint_guide.qst": "[grade]\n`[system]`\n" +
			"[type]\n`" + questType + "`\n" +
			"[sub type]\n0\n" +
			"[int data]\n1 0 1\n" +
			"[level]\n10 99\n" +
			"[job]\n`[all]`\n" +
			"[grow type]\n-1\n" +
			"[reward type]\n`[item]`\n" +
			"[reward int data]\n0 0\n" +
			"[gold multiple]\n10\n",
	}, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func seedRentalGuideOwnerQuest(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "77",
		States: map[int64]dnfrepo.QuestState{
			dnfquest.RentalGuideQuestID: {
				Status:        "active",
				ProgressValue: 1,
				Extra: map[string]string{
					"keep":       "metadata",
					"pvf_path":   "n_quest/common/chn_system_10_starpoint_guide.qst",
					"quest_type": "[custom quest]",
				},
			},
			2901: {Status: "active", ProgressValue: 7},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerChargeCommitsPointsAndGold(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedRentalOwner(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}

	result, err := owner.Charge(ctx, ChargeCommand{
		AccountID:    "dnf:1",
		CharacterID:  "77",
		Count:        2,
		Limit:        20,
		GoldPerPoint: 100,
	})
	if err != nil {
		t.Fatalf("Charge error = %v", err)
	}
	if result.Points != 7 || result.Gold != 800 {
		t.Fatalf("Charge result = %+v", result)
	}
	account, _, _ := repositories.Account.Load(ctx, "dnf:1")
	character, _, _ := repositories.Character.Load(ctx, "77")
	if account.Metadata[PointMetadataKey] != "7" || character.Stats["gold"] != 800 {
		t.Fatalf("persisted account=%+v character=%+v", account, character)
	}
}

func TestOwnerRentCommitsWalletAndProjectedItemsAtomically(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedRentalOwner(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}

	result, err := owner.Rent(ctx, RentCommand{
		AccountID:   "dnf:1",
		CharacterID: "77",
		PointCost:   3,
		Project: func(assets *Assets) (Changes, error) {
			assets.Inventory.Slots["0:9"] = dnfrepo.ItemStack{ItemID: 416000000, Count: 1}
			assets.Equipment.Entries["weapon"] = dnfrepo.EquipmentEntry{ItemID: 416000001, SlotIndex: 0}
			return Changes{Inventory: true, Equipment: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("Rent error = %v", err)
	}
	if result.Points != 2 || result.Gold != 1000 {
		t.Fatalf("Rent result = %+v", result)
	}
	account, _, _ := repositories.Account.Load(ctx, "dnf:1")
	inventory, _, _ := repositories.Inventory.Load(ctx, "77")
	equipment, _, _ := repositories.Equipment.Load(ctx, "77")
	if account.Metadata[PointMetadataKey] != "2" ||
		inventory.Slots["0:9"].ItemID != 416000000 ||
		equipment.Entries["weapon"].ItemID != 416000001 {
		t.Fatalf("persisted account=%+v inventory=%+v equipment=%+v", account, inventory, equipment)
	}
}

func TestOwnerRentRollsBackProjectionAndRejectsCrossAccount(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedRentalOwner(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	wantErr := errors.New("reject rental projection")
	errResult, err := owner.Rent(ctx, RentCommand{
		AccountID:   "dnf:1",
		CharacterID: "77",
		PointCost:   3,
		Project: func(assets *Assets) (Changes, error) {
			assets.Inventory.Slots["0:9"] = dnfrepo.ItemStack{ItemID: 416000000, Count: 1}
			return Changes{Inventory: true}, wantErr
		},
	})
	if !errors.Is(err, wantErr) || errResult != (WalletResult{}) {
		t.Fatalf("Rent result=%+v error=%v", errResult, err)
	}
	account, _, _ := repositories.Account.Load(ctx, "dnf:1")
	inventory, _, _ := repositories.Inventory.Load(ctx, "77")
	if account.Metadata[PointMetadataKey] != "5" || len(inventory.Slots) != 0 {
		t.Fatalf("rollback account=%+v inventory=%+v", account, inventory)
	}

	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "dnf:2",
		Metadata:  map[string]string{PointMetadataKey: "5"},
	}); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = owner.Rent(ctx, RentCommand{
		AccountID:   "dnf:2",
		CharacterID: "77",
		PointCost:   1,
		Project: func(*Assets) (Changes, error) {
			called = true
			return Changes{}, nil
		},
	})
	if !errors.Is(err, ErrOwnerMismatch) || called {
		t.Fatalf("cross-account Rent error=%v called=%t", err, called)
	}
}

func TestOwnerRentAdvancesRentalGuideWithAssetsAtomically(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedRentalOwner(t, ctx, repositories)
	seedRentalGuideOwnerQuest(t, ctx, repositories)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := time.Unix(1_700_000_000, 0).UTC()
	result, err := owner.Rent(ctx, RentCommand{
		AccountID:    "dnf:1",
		CharacterID:  "77",
		PointCost:    3,
		UpdatedAt:    updatedAt,
		QuestCatalog: rentalGuideOwnerTestCatalog(t, "[custom quest]"),
		Project: func(assets *Assets) (Changes, error) {
			assets.Inventory.Slots["0:9"] = dnfrepo.ItemStack{ItemID: 416000000, Count: 1}
			return Changes{Inventory: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("Rent error = %v", err)
	}
	if result.Points != 2 || !result.QuestAdvanced {
		t.Fatalf("Rent result = %+v", result)
	}
	account, _, _ := repositories.Account.Load(ctx, "dnf:1")
	inventory, _, _ := repositories.Inventory.Load(ctx, "77")
	quests, _, _ := repositories.Quest.Load(ctx, "77")
	guide := quests.States[dnfquest.RentalGuideQuestID]
	if account.Metadata[PointMetadataKey] != "2" ||
		inventory.Slots["0:9"].ItemID != 416000000 ||
		guide.Status != "active" ||
		guide.ProgressValue != 0 ||
		!guide.UpdatedAt.Equal(updatedAt) ||
		guide.Extra["keep"] != "metadata" ||
		quests.States[2901].ProgressValue != 7 {
		t.Fatalf("atomic rental state account=%+v inventory=%+v quests=%+v", account, inventory, quests)
	}
}

func TestOwnerRentRejectionsLeaveRentalGuideAndAssetsUnchanged(t *testing.T) {
	t.Run("insufficient points", func(t *testing.T) {
		ctx := context.Background()
		repositories := dnfrepomemory.NewMemoryGroup()
		seedRentalOwner(t, ctx, repositories)
		seedRentalGuideOwnerQuest(t, ctx, repositories)
		owner, _ := NewOwner(repositories)
		_, err := owner.Rent(ctx, RentCommand{
			AccountID:    "dnf:1",
			CharacterID:  "77",
			PointCost:    6,
			QuestCatalog: rentalGuideOwnerTestCatalog(t, "[custom quest]"),
			Project: func(*Assets) (Changes, error) {
				t.Fatal("projector ran after point rejection")
				return Changes{}, nil
			},
		})
		if !errors.Is(err, ErrPointsInsufficient) {
			t.Fatalf("Rent error = %v", err)
		}
		assertRentalGuideRejectedState(t, ctx, repositories)
	})

	t.Run("projection failure", func(t *testing.T) {
		ctx := context.Background()
		repositories := dnfrepomemory.NewMemoryGroup()
		seedRentalOwner(t, ctx, repositories)
		seedRentalGuideOwnerQuest(t, ctx, repositories)
		owner, _ := NewOwner(repositories)
		wantErr := errors.New("reject guide rental projection")
		_, err := owner.Rent(ctx, RentCommand{
			AccountID:    "dnf:1",
			CharacterID:  "77",
			PointCost:    3,
			QuestCatalog: rentalGuideOwnerTestCatalog(t, "[custom quest]"),
			Project: func(assets *Assets) (Changes, error) {
				assets.Inventory.Slots["0:9"] = dnfrepo.ItemStack{ItemID: 416000000, Count: 1}
				return Changes{Inventory: true}, wantErr
			},
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("Rent error = %v", err)
		}
		assertRentalGuideRejectedState(t, ctx, repositories)
	})
}

func TestOwnerRentDoesNotAdvanceGuideAgainAtZero(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedRentalOwner(t, ctx, repositories)
	seedRentalGuideOwnerQuest(t, ctx, repositories)
	owner, _ := NewOwner(repositories)
	catalog := rentalGuideOwnerTestCatalog(t, "[custom quest]")
	project := func(assets *Assets) (Changes, error) {
		assets.Inventory.Slots["0:9"] = dnfrepo.ItemStack{ItemID: 416000000, Count: 1}
		return Changes{Inventory: true}, nil
	}
	first, err := owner.Rent(ctx, RentCommand{
		AccountID: "dnf:1", CharacterID: "77", PointCost: 1,
		QuestCatalog: catalog, Project: project,
	})
	if err != nil || !first.QuestAdvanced {
		t.Fatalf("first Rent result=%+v error=%v", first, err)
	}
	second, err := owner.Rent(ctx, RentCommand{
		AccountID: "dnf:1", CharacterID: "77", PointCost: 1,
		QuestCatalog: catalog, Project: project,
	})
	if err != nil || second.QuestAdvanced {
		t.Fatalf("replayed Rent result=%+v error=%v", second, err)
	}
	quests, _, _ := repositories.Quest.Load(ctx, "77")
	if got := quests.States[dnfquest.RentalGuideQuestID]; got.Status != "active" || got.ProgressValue != 0 {
		t.Fatalf("replayed guide state = %+v", got)
	}
}

func assertRentalGuideRejectedState(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	account, _, _ := repositories.Account.Load(ctx, "dnf:1")
	inventory, _, _ := repositories.Inventory.Load(ctx, "77")
	quests, _, _ := repositories.Quest.Load(ctx, "77")
	guide := quests.States[dnfquest.RentalGuideQuestID]
	if account.Metadata[PointMetadataKey] != "5" ||
		len(inventory.Slots) != 0 ||
		guide.Status != "active" ||
		guide.ProgressValue != 1 ||
		guide.Extra["keep"] != "metadata" ||
		quests.States[2901].ProgressValue != 7 {
		t.Fatalf("rejected rental changed state account=%+v inventory=%+v quests=%+v", account, inventory, quests)
	}
}

func TestOwnerCleanupPersistsOnlyProjectedContainers(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedRentalOwner(t, ctx, repositories)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{"0:9": {ItemID: 416000000, Count: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatalf("NewOwner error = %v", err)
	}
	err = owner.Cleanup(ctx, CleanupCommand{
		AccountID:   "dnf:1",
		CharacterID: "77",
		Project: func(assets *Assets) (Changes, error) {
			delete(assets.Inventory.Slots, "0:9")
			return Changes{Inventory: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("Cleanup error = %v", err)
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "77")
	account, _, _ := repositories.Account.Load(ctx, "dnf:1")
	if len(inventory.Slots) != 0 || account.Metadata[PointMetadataKey] != "5" {
		t.Fatalf("cleanup inventory=%+v account=%+v", inventory, account)
	}
}

func seedRentalOwner(t *testing.T, ctx context.Context, repositories dnfrepo.Group) {
	t.Helper()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "dnf:1",
		Metadata:  map[string]string{PointMetadataKey: "5"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "dnf:1",
		Stats:       map[string]int64{"gold": 1000},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries:     map[string]dnfrepo.EquipmentEntry{},
	}); err != nil {
		t.Fatal(err)
	}
}
