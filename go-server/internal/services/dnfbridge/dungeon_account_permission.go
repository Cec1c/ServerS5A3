package dnfbridge

import (
	"context"
	"fmt"
	"strings"

	dnfpermission "longheng.io/server/internal/modules/dnf/dungeonpermission"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 账号保存最高难度，角色旧记录继续保留，供现有个人通关前置使用。
// 首次读取按同账号现存角色迁移；部分失败可重试，任何较低记录都不能降级。
func (s *Service) currentAccountDungeonPermissionSnapshot(ctx context.Context, session *gameSession, repos dnfrepo.Group) (dnfrepo.DungeonPermissionRecord, bool, error) {
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return dnfrepo.DungeonPermissionRecord{}, false, fmt.Errorf("副本难度共享缺少账号归属")
	}
	owner, err := dnfpermission.NewOwner(repos)
	if err != nil {
		return dnfrepo.DungeonPermissionRecord{}, false, err
	}
	snapshot, migrated, err := migrateCurrentAccountDungeonPermission(ctx, session, owner, accountID)
	if err == nil && !migrated {
		snapshot, err = owner.LoadAccount(ctx, accountID)
	}
	if err != nil {
		return dnfrepo.DungeonPermissionRecord{}, false, err
	}
	return snapshot.Record, len(snapshot.Record.Entries) != 0, nil
}

func (s *Service) advanceCurrentAccountDungeonPermission(ctx context.Context, session *gameSession, repos dnfrepo.Group, dungeonID uint32, state byte) (dnfrepo.DungeonPermissionEntry, bool, error) {
	owner, err := dnfpermission.NewOwner(repos)
	if err != nil {
		if session != nil {
			session.dungeonPermissionMigrationAccount = ""
		}
		return dnfrepo.DungeonPermissionEntry{}, false, err
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	snapshot, _, err := migrateCurrentAccountDungeonPermission(ctx, session, owner, accountID)
	if err != nil {
		return dnfrepo.DungeonPermissionEntry{}, false, err
	}
	_, entry, changed, err := owner.Advance(ctx, accountID, dungeonID, state)
	if err != nil {
		// 个人记录已经成功写入。取消完成标记，让下次读取从旧记录补齐账号汇总。
		session.dungeonPermissionMigrationAccount = ""
		return dnfrepo.DungeonPermissionEntry{}, false, err
	}
	return entry, changed || snapshot.MigratedEntries > 0, nil
}

// 会话事件循环串行维护完成标记。重连会重新归并，失败不记完成；热路径实时读账号行。
func migrateCurrentAccountDungeonPermission(ctx context.Context, session *gameSession, owner *dnfpermission.Owner, accountID string) (dnfpermission.Snapshot, bool, error) {
	if session == nil || accountID == "" {
		return dnfpermission.Snapshot{}, false, fmt.Errorf("副本难度共享缺少会话或账号归属")
	}
	if session.dungeonPermissionMigrationAccount == accountID {
		return dnfpermission.Snapshot{}, false, nil
	}
	session.dungeonPermissionMigrationAccount = ""
	snapshot, err := owner.MigrateAccount(ctx, accountID)
	if err != nil {
		return dnfpermission.Snapshot{}, false, err
	}
	session.dungeonPermissionMigrationAccount = accountID
	return snapshot, true, nil
}
