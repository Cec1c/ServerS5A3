package repository

import "context"

// 团本所有角色门票与账号每日入场次数必须在同一个事务中提交。
type RaidEntryAssetUnitOfWork interface {
	WithinRaidEntryAssets(context.Context, []string, string, func(CharacterRepository, InventoryRepository, SettingsRepository) error) error
}
