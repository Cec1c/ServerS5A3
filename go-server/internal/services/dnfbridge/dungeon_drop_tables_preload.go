package dnfbridge

import (
	"context"
	"time"
)

// 这些表只依赖当前PVF；在监听前装入怪物目录的原缓存，不消费随机数。
func (s *Service) preloadDungeonMonsterDropTables(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		s.logPacketEvent("普通怪静态掉落表预载未完成", "错误", err)
		return nil
	}
	stages := []struct {
		name string
		load func() error
	}{
		{"概率及品质规则", func() error { _, err := currentDungeonMonsterDropRules(catalog); return err }},
		{"金币参考", func() error { _, err := currentDungeonGoldReferences(catalog); return err }},
		{"独立及世界掉落", func() error { _, err := currentDungeonSpecialDrops(catalog); return err }},
		{"异界掉落", func() error { _, err := currentDungeonDimensionDrops(catalog); return err }},
		{"深渊碎片等级索引", func() error {
			if !abyssItemLevelBandFromEnv().Active {
				return nil
			}
			_, err := currentAbyssEpicPieceLevelIndexFor(catalog)
			return err
		}},
	}
	for _, stage := range stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		started := time.Now()
		if err := stage.load(); err != nil {
			// 不扩大既有可选表错误的影响；实际掉落继续读取原缓存及降级。
			s.logPacketEvent("普通怪静态掉落表预载未完成", "阶段", stage.name, "错误", err)
		} else {
			s.logPacketEvent("普通怪静态掉落表预载完成", "阶段", stage.name, "耗时毫秒", time.Since(started).Milliseconds())
		}
	}
	return ctx.Err()
}
