package itemexpiration

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestOwnerReconcileSnapshotReturnsCommittedCopies(t *testing.T) {
	ctx := context.Background()
	repositories := seededRepositories(t, ctx)
	updatedAt := time.Unix(2_000_000_000, 0).UTC()
	inventory := loadInventory(t, ctx, repositories, "19")
	inventory.Slots["0:2"] = dnfrepo.ItemStack{ItemID: 202, Count: 1, ExpireAt: updatedAt.Add(-time.Minute)}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	var projectedInventory *dnfrepo.InventoryRecord
	var projectedAccountInventory *dnfrepo.AccountInventoryRecord
	summary, snapshot, err := owner.ReconcileSnapshot(ctx, Command{
		AccountID: "account-1", CharacterID: "19", UpdatedAt: updatedAt,
		Project: func(assets *Assets) (Summary, error) {
			projectedInventory = assets.Inventory
			projectedAccountInventory = assets.AccountInventory
			return Summary{Inventory: deleteExpiredStacks(assets.Inventory.Slots, updatedAt)}, nil
		},
	})
	if err != nil || summary.Total() != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if !snapshot.Available || snapshot.Character == nil || snapshot.Inventory == nil || snapshot.AccountInventory == nil {
		t.Fatalf("snapshot is incomplete: %+v", snapshot)
	}
	committedCharacter := loadCharacter(t, ctx, repositories, "19")
	committedInventory := loadInventory(t, ctx, repositories, "19")
	committedAccountInventory := loadAccountInventory(t, ctx, repositories, "account-1")
	if !reflect.DeepEqual(*snapshot.Character, committedCharacter) ||
		!reflect.DeepEqual(*snapshot.Inventory, committedInventory) ||
		!reflect.DeepEqual(*snapshot.AccountInventory, committedAccountInventory) {
		t.Fatal("snapshot differs from committed repositories")
	}

	snapshot.Inventory.Slots["0:1"] = dnfrepo.ItemStack{ItemID: 900, Count: 1}
	snapshot.AccountInventory.Slots["0:1"] = dnfrepo.ItemStack{ItemID: 901, Count: 1}
	if got := loadInventory(t, ctx, repositories, "19").Slots["0:1"].ItemID; got != 1 {
		t.Fatalf("snapshot wrote through to inventory: item=%d", got)
	}
	if got := loadAccountInventory(t, ctx, repositories, "account-1").Slots["0:1"].ItemID; got != 2 {
		t.Fatalf("snapshot wrote through to account inventory: item=%d", got)
	}
	snapshot.Character.Name = "mutated"
	if got := loadCharacter(t, ctx, repositories, "19").Name; got != "" {
		t.Fatalf("snapshot wrote through to character: name=%q", got)
	}
	projectedInventory.Slots["0:1"] = dnfrepo.ItemStack{ItemID: 902, Count: 1}
	projectedAccountInventory.Slots["0:1"] = dnfrepo.ItemStack{ItemID: 903, Count: 1}
	if snapshot.Inventory.Slots["0:1"].ItemID != 900 || snapshot.AccountInventory.Slots["0:1"].ItemID != 901 {
		t.Fatal("snapshot aliases projector assets")
	}
}

func TestOwnerReconcileSnapshotReturnsZeroOnProjectFailure(t *testing.T) {
	ctx := context.Background()
	repositories := seededRepositories(t, ctx)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	projectErr := errors.New("projection failed")
	_, snapshot, err := owner.ReconcileSnapshot(ctx, Command{
		AccountID: "account-1", CharacterID: "19",
		Project: func(assets *Assets) (Summary, error) {
			assets.Inventory.Slots["0:1"] = dnfrepo.ItemStack{ItemID: 201, Count: 1}
			return Summary{Inventory: 1}, projectErr
		},
	})
	if !errors.Is(err, projectErr) || !snapshotUnavailable(snapshot) {
		t.Fatalf("err=%v snapshot=%+v", err, snapshot)
	}
	if got := loadInventory(t, ctx, repositories, "19").Slots["0:1"].ItemID; got != 1 {
		t.Fatalf("project failure changed inventory: item=%d", got)
	}
}

type commitErrorAccountAssets struct {
	base dnfrepo.AccountCharacterAssetUnitOfWork
	err  error
}

func (u *commitErrorAccountAssets) WithinAccountCharacterAssets(ctx context.Context, accountID, characterID string, apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	if err := u.base.WithinAccountCharacterAssets(ctx, accountID, characterID, apply); err != nil {
		return err
	}
	return u.err
}

func TestOwnerReconcileSnapshotReturnsZeroOnCommitFailure(t *testing.T) {
	ctx := context.Background()
	repositories := seededRepositories(t, ctx)
	commitErr := errors.New("commit failed")
	repositories.AccountAssets = &commitErrorAccountAssets{base: repositories.AccountAssets, err: commitErr}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	projected := false
	_, snapshot, err := owner.ReconcileSnapshot(ctx, Command{
		AccountID: "account-1", CharacterID: "19",
		Project: func(*Assets) (Summary, error) {
			projected = true
			return Summary{}, nil
		},
	})
	if !projected || !errors.Is(err, commitErr) || !snapshotUnavailable(snapshot) {
		t.Fatalf("projected=%t err=%v snapshot=%+v", projected, err, snapshot)
	}
}

func TestOwnerReconcileSnapshotReturnsVerifiedCharacterAndWalletWithNoChanges(t *testing.T) {
	ctx := context.Background()
	repositories := seededRepositories(t, ctx)
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	summary, snapshot, err := owner.ReconcileSnapshot(ctx, Command{
		AccountID: "account-1", CharacterID: "19",
		Project: func(*Assets) (Summary, error) { return Summary{}, nil },
	})
	if err != nil || summary.Total() != 0 || !snapshot.Available {
		t.Fatalf("summary=%+v snapshot=%+v err=%v", summary, snapshot, err)
	}
	if snapshot.Character == nil || snapshot.Character.AccountID != "account-1" ||
		snapshot.Inventory == nil || snapshot.AccountInventory == nil {
		t.Fatalf("verified owners missing from snapshot: %+v", snapshot)
	}
	if snapshot.Inventory.Slots["0:1"].ItemID != 1 || snapshot.AccountInventory.Slots["0:1"].ItemID != 2 {
		t.Fatalf("snapshot data differs: %+v", snapshot)
	}
}

func TestOwnerReconcileSnapshotMarksMissingInventoryAvailable(t *testing.T) {
	ctx := context.Background()
	repositories := seededRepositories(t, ctx)
	deleter := repositories.Inventory.(interface {
		Delete(context.Context, string) error
	})
	if err := deleter.Delete(ctx, "19"); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := owner.ReconcileSnapshot(ctx, Command{
		AccountID: "account-1", CharacterID: "19",
		Project: func(assets *Assets) (Summary, error) {
			if assets.Inventory != nil {
				t.Fatal("missing inventory was not represented as nil")
			}
			return Summary{}, nil
		},
	})
	if err != nil || !snapshot.Available || snapshot.Inventory != nil ||
		snapshot.Character == nil || snapshot.AccountInventory == nil {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if _, found, err := repositories.Inventory.Load(ctx, "19"); err != nil || found {
		t.Fatalf("inventory unexpectedly exists: found=%t err=%v", found, err)
	}
}

func snapshotUnavailable(snapshot Snapshot) bool {
	return !snapshot.Available && snapshot.Character == nil && snapshot.Inventory == nil && snapshot.AccountInventory == nil
}

func deleteExpiredStacks(stacks map[string]dnfrepo.ItemStack, now time.Time) int {
	removed := 0
	for key, stack := range stacks {
		if !stack.ExpireAt.IsZero() && !stack.ExpireAt.After(now) {
			delete(stacks, key)
			removed++
		}
	}
	return removed
}

func loadCharacter(t *testing.T, ctx context.Context, repositories dnfrepo.Group, id string) dnfrepo.CharacterRecord {
	t.Helper()
	record, found, err := repositories.Character.Load(ctx, id)
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	return record
}

func loadInventory(t *testing.T, ctx context.Context, repositories dnfrepo.Group, id string) dnfrepo.InventoryRecord {
	t.Helper()
	record, found, err := repositories.Inventory.Load(ctx, id)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return record
}

func loadAccountInventory(t *testing.T, ctx context.Context, repositories dnfrepo.Group, id string) dnfrepo.AccountInventoryRecord {
	t.Helper()
	record, found, err := repositories.AccountInventory.Load(ctx, id)
	if err != nil || !found {
		t.Fatalf("load account inventory found=%t err=%v", found, err)
	}
	return record
}
