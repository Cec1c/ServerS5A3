package dnfbridge

import (
	"context"
	"time"
)

// 通用消耗品池首次构建会扫描整份列表；在开放监听前完成，避免挡住死亡通知。
// 必须预载怪物目录实际持有的掉落缓存，不能只预载背包使用的另一份物品目录。
func (s *Service) preloadDungeonGenericDropPools(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	monsters, err := s.dungeonMonsterCatalog()
	var drops *pvfDungeonDropCatalog
	if err == nil {
		drops, err = monsters.DropCatalog()
	}
	if err == nil {
		err = drops.ensureGenericPools()
	}
	if cancelErr := ctx.Err(); cancelErr != nil {
		return cancelErr
	}
	if err != nil {
		// 保留既有缺表降级：实际抽取仍读取同一份已缓存错误，不制造候选或奖励。
		s.logPacketEvent("普通怪通用掉落候选预载未完成", "错误", err, "耗时毫秒", time.Since(started).Milliseconds())
		return nil
	}
	drops.mu.Lock()
	stackableGroups, equipmentGroups := len(drops.genericStackables), len(drops.genericEquipment)
	drops.mu.Unlock()
	s.logPacketEvent("普通怪通用掉落候选预载完成", "消耗品分组", stackableGroups, "装备分组", equipmentGroups, "耗时毫秒", time.Since(started).Milliseconds())
	return nil
}
