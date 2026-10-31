package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	dnfpermission "longheng.io/server/internal/modules/dnf/dungeonpermission"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type accountPermissionCountingRepo struct {
	dnfrepo.DungeonPermissionRepository
	loads            int
	failAccountWrite bool
}

func (r *accountPermissionCountingRepo) Load(ctx context.Context, key string) (dnfrepo.DungeonPermissionRecord, bool, error) {
	r.loads++
	return r.DungeonPermissionRepository.Load(ctx, key)
}
func (r *accountPermissionCountingRepo) UpsertMax(ctx context.Context, key string, id uint32, state byte) (dnfrepo.DungeonPermissionEntry, bool, error) {
	if r.failAccountWrite && strings.HasPrefix(key, "account-v1:") {
		return dnfrepo.DungeonPermissionEntry{}, false, errors.New("模拟账号共享写入失败")
	}
	return r.DungeonPermissionRepository.UpsertMax(ctx, key, id, state)
}

func TestAccountDifficultyReadsMigrateOnceAndRemainLive(t *testing.T) {
	ctx := context.Background()
	repos := testRepositoryGroup()
	for i := 0; i < dnfrepo.DefaultCharacterSlotLimit; i++ {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: strconv.Itoa(i + 1), AccountID: "shared", Slot: i}); err != nil {
			t.Fatal(err)
		}
	}
	counter := &accountPermissionCountingRepo{DungeonPermissionRepository: repos.DungeonPermission}
	repos.DungeonPermission = counter
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{accountID: "shared", selectedCharacterID: 1}
	if _, _, err := s.currentAccountDungeonPermissionSnapshot(ctx, session, repos); err != nil {
		t.Fatal(err)
	}
	if counter.loads != 33 {
		t.Fatalf("首次归并次数=%d", counter.loads)
	}
	key, _ := dnfpermission.AccountOwnerKey("shared")
	if _, _, err := repos.DungeonPermission.UpsertMax(ctx, key, 700, 3); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		before := counter.loads
		record, found, err := s.currentAccountDungeonPermissionSnapshot(ctx, session, repos)
		if err != nil || !found || len(record.Entries) != 1 || record.Entries[0].ClearState != 3 {
			t.Fatalf("实时最高状态丢失：%+v %v", record, err)
		}
		if counter.loads-before != 1 {
			t.Fatalf("热读取重新全量归并，读取次数=%d", counter.loads-before)
		}
	}
	// 重连没有完成标记，仍重新归并，能接纳停服导入或前次未同步的个人记录。
	if _, _, err := repos.DungeonPermission.UpsertMax(ctx, "2", 701, 2); err != nil {
		t.Fatal(err)
	}
	reconnected := &gameSession{accountID: "shared", selectedCharacterID: 2}
	before := counter.loads
	record, _, err := s.currentAccountDungeonPermissionSnapshot(ctx, reconnected, repos)
	if err != nil || counter.loads-before != 33 || len(record.Entries) != 2 {
		t.Fatalf("重连归并失败：%+v reads=%d err=%v", record, counter.loads-before, err)
	}
}

func TestAccountDifficultyFailedSharedWriteRetriesMigration(t *testing.T) {
	ctx := context.Background()
	repos := testRepositoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "11", AccountID: "shared"}); err != nil {
		t.Fatal(err)
	}
	storage := &accountPermissionCountingRepo{DungeonPermissionRepository: repos.DungeonPermission}
	repos.DungeonPermission = storage
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{accountID: "shared", selectedCharacterID: 11}
	if _, _, err := s.currentAccountDungeonPermissionSnapshot(ctx, session, repos); err != nil {
		t.Fatal(err)
	}
	storage.failAccountWrite = true
	rt := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 700, Metadata: worldmap.DungeonMetadata{DifficultyLevels: []int64{1, 1, 1}}}}
	if _, _, _, err := s.commitCurrentDungeonPermission(ctx, session, rt); err == nil {
		t.Fatal("共享写入错误被隐瞒")
	}
	if session.dungeonPermissionMigrationAccount != "" {
		t.Fatal("共享失败仍记归并完成")
	}
	storage.failAccountWrite = false
	record, found, err := s.currentAccountDungeonPermissionSnapshot(ctx, session, repos)
	if err != nil || !found || len(record.Entries) != 1 || record.Entries[0].ClearState != 1 {
		t.Fatalf("个人记录未补齐账号汇总：%+v %v", record, err)
	}
}

func TestAccountDifficultyClearPushCapsPVFAndPreservesHistoricalMaximum(t *testing.T) {
	ctx := context.Background()
	repos := testRepositoryGroup()
	for _, c := range []dnfrepo.CharacterRecord{{CharacterID: "11", AccountID: "shared", Slot: 0}, {CharacterID: "12", AccountID: "shared", Slot: 1}} {
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := repos.DungeonPermission.UpsertMax(ctx, "12", 700, 4); err != nil {
		t.Fatal(err)
	}
	s := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{accountID: "shared", selectedCharacterID: 11}
	rt := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 700, Metadata: worldmap.DungeonMetadata{DifficultyLevels: []int64{1, 1, 1}}}}
	body, state, changed, err := s.commitCurrentDungeonPermission(ctx, session, rt)
	if err != nil || !changed || state != 2 || len(body) != 7 || body[6] != 2 {
		t.Fatalf("推送未按PVF上限裁剪：state=%d body=%x err=%v", state, body, err)
	}
	key, _ := dnfpermission.AccountOwnerKey("shared")
	record, found, err := repos.DungeonPermission.Load(ctx, key)
	if err != nil || !found || record.Entries[0].ClearState != 4 {
		t.Fatal("裁剪客户端投影降低了持久历史最大值")
	}
	if body, state, changed, err = s.commitCurrentDungeonPermission(ctx, session, rt); err != nil || changed || len(body) != 0 || state != 2 {
		t.Fatalf("重复通关不一致：state=%d changed=%t err=%v", state, changed, err)
	}
}
