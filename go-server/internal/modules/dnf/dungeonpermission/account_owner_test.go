package dungeonpermission

import (
	"context"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type partialMigrationRepository struct {
	repository.DungeonPermissionRepository
	fail bool
}

func (r *partialMigrationRepository) UpsertMax(ctx context.Context, owner string, dungeon uint32, state byte) (repository.DungeonPermissionEntry, bool, error) {
	if r.fail && strings.HasPrefix(owner, accountOwnerKeyPrefix) && dungeon == 701 {
		return repository.DungeonPermissionEntry{}, false, errors.New("模拟迁移中途存储失败")
	}
	return r.DungeonPermissionRepository.UpsertMax(ctx, owner, dungeon, state)
}

func TestMigrateAccountRetriesPartialMigrationWithoutLosingLegacy(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, repository.CharacterRecord{CharacterID: "11", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{700, 701} {
		if _, _, err := repos.DungeonPermission.UpsertMax(ctx, "11", id, 3); err != nil {
			t.Fatal(err)
		}
	}
	storage := &partialMigrationRepository{DungeonPermissionRepository: repos.DungeonPermission, fail: true}
	repos.DungeonPermission = storage
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.MigrateAccount(ctx, "account"); err == nil {
		t.Fatal("迁移失败被隐瞒")
	}
	storage.fail = false
	snapshot, err := owner.MigrateAccount(ctx, "account")
	if err != nil || len(snapshot.Record.Entries) != 2 || snapshot.MigratedEntries != 1 {
		t.Fatalf("迁移重试=%+v err=%v", snapshot, err)
	}
	legacy, found, err := storage.Load(ctx, "11")
	if err != nil || !found || len(legacy.Entries) != 2 {
		t.Fatal("迁移删除了角色旧记录")
	}
}

func TestAccountOwnerKeyIsNamespacedFixedWidthAndDeterministic(t *testing.T) {
	first, err := AccountOwnerKey(" account-1 ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := AccountOwnerKey("account-1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := AccountOwnerKey("account-2")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == other || !strings.HasPrefix(first, accountOwnerKeyPrefix) || len(first) > 128 {
		t.Fatalf("account owner keys first=%q second=%q other=%q", first, second, other)
	}
}

func TestMigrateAccountMergesLegacyCharacterMaximaAndMigratesOwner(t *testing.T) {
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	for _, character := range []repository.CharacterRecord{
		{CharacterID: "11", AccountID: "account-1", Slot: 0},
		{CharacterID: "12", AccountID: "account-1", Slot: 1},
		{CharacterID: "21", AccountID: "account-2", Slot: 0},
	} {
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
	}
	for _, seed := range []struct {
		owner   string
		dungeon uint32
		state   byte
	}{
		{owner: "11", dungeon: 700, state: 1},
		{owner: "11", dungeon: 701, state: 3},
		{owner: "12", dungeon: 700, state: 4},
		{owner: "21", dungeon: 700, state: 5},
	} {
		if _, _, err := repositories.DungeonPermission.UpsertMax(ctx, seed.owner, seed.dungeon, seed.state); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := owner.MigrateAccount(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LegacyOwners != 2 || snapshot.MigratedEntries != 2 {
		t.Fatalf("migration snapshot=%+v", snapshot)
	}
	want := map[uint32]byte{700: 4, 701: 3}
	if got := entryStates(snapshot.Record); !equalStates(got, want) {
		t.Fatalf("account states=%v want=%v", got, want)
	}
	persisted, found, err := repositories.DungeonPermission.Load(ctx, snapshot.StorageKey)
	if err != nil || !found || !equalStates(entryStates(persisted), want) {
		t.Fatalf("persisted account record=%+v found=%t err=%v", persisted, found, err)
	}
	again, err := owner.MigrateAccount(ctx, "account-1")
	if err != nil || again.MigratedEntries != 0 || !equalStates(entryStates(again.Record), want) {
		t.Fatalf("idempotent account snapshot=%+v err=%v", again, err)
	}
}

func TestAdvanceIsSharedByEveryCharacterAndIsolatesAccounts(t *testing.T) {
	ctx := context.Background()
	repositories := memory.NewMemoryGroup()
	for _, character := range []repository.CharacterRecord{
		{CharacterID: "11", AccountID: "account-1", Slot: 0},
		{CharacterID: "12", AccountID: "account-1", Slot: 1},
		{CharacterID: "21", AccountID: "account-2", Slot: 0},
	} {
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, entry, changed, err := owner.Advance(ctx, "account-1", 700, 3)
	if err != nil || !changed || entry.ClearState != 3 {
		t.Fatalf("advance snapshot=%+v entry=%+v changed=%t err=%v", snapshot, entry, changed, err)
	}
	shared, err := owner.MigrateAccount(ctx, "account-1")
	if err != nil || entryStates(shared.Record)[700] != 3 {
		t.Fatalf("shared snapshot=%+v err=%v", shared, err)
	}
	isolated, err := owner.MigrateAccount(ctx, "account-2")
	if err != nil || entryStates(isolated.Record)[700] != 0 {
		t.Fatalf("isolated snapshot=%+v err=%v", isolated, err)
	}
}

func entryStates(record repository.DungeonPermissionRecord) map[uint32]byte {
	out := make(map[uint32]byte, len(record.Entries))
	for _, entry := range record.Entries {
		out[entry.DungeonID] = entry.ClearState
	}
	return out
}

func equalStates(left, right map[uint32]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
