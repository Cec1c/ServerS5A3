package dnfbridge

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// 与正式启动相同的目录预载顺序；不打开监听或连接玩家数据库。
// 采样Go运行时保留量，不把它称为Linux RSS、Windows工作集或整机峰值。
func TestRealPVFLowMemoryStartupPreloads(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	oldGC := debug.SetGCPercent(25)
	defer debug.SetGCPercent(oldGC)
	oldLimit := debug.SetMemoryLimit(1152 << 20)
	defer debug.SetMemoryLimit(oldLimit)
	runtime.GC()
	debug.FreeOSMemory()
	stop, done := make(chan struct{}), make(chan struct{})
	var peak uint64
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if used := m.Sys - m.HeapReleased; used > peak {
					peak = used
				}
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
		t.Logf("低内存启动目录Go运行时采样峰值MiB=%.1f；非进程RSS或整机峰值", float64(peak)/(1<<20))
	}()
	s := New(nil)
	s.options.lowMemory = true
	s.options.mapDefinitionCache = true
	s.options.pvfPath = path
	s.options.currentClientVersion = "official-24.9"
	stages := []struct {
		name string
		load func(context.Context) error
	}{
		{"初始装备", s.preloadInitialEquipmentIndex}, {"物品目录", s.preloadPVFItemCatalog}, {"副职业", s.preloadExpertJobCatalog},
		{"冒险团", s.preloadAdventureGroupTable}, {"荣誉", s.preloadHonorTable}, {"城镇", s.preloadTownCatalog},
		{"技能", s.preloadSkillCatalog}, {"职业", s.preloadProfessionProfiles}, {"任务", s.preloadQuestCatalog},
		{"地图及路由", s.preloadDungeonWorldMap}, {"角色属性", s.preloadCharacterStatTable}, {"装备属性", s.preloadEquipmentStatIndex},
		{"战斗力", s.preloadCombatPowerAffixCatalog}, {"装备合成", s.preloadEquipmentRegenerationCatalog},
	}
	for _, stage := range stages {
		if err := stage.load(context.Background()); err != nil {
			t.Fatalf("低内存预载%s失败：%v", stage.name, err)
		}
		s.releaseStartupPVFStageCaches()
	}
	s.releaseStartupPVFReadCaches()
	if s.worldMapTable == nil || !s.worldMapTable.MapCacheSnapshot().Enabled {
		t.Fatal("低内存预载没有实际启用地图缓存")
	}
	snapshot := s.initialEquipmentArchive.BoundedReadCacheSnapshot()
	if snapshot == nil || snapshot.TextBudget != 8<<20 || snapshot.ChunkBudget != 8<<20 || snapshot.TextBytes > snapshot.TextBudget || snapshot.ChunkBytes > snapshot.ChunkBudget {
		t.Fatalf("共享PVF读取预算错误：%+v", snapshot)
	}
	t.Logf("低内存目录预载通过；地图=%+v；PVF预算=%+v", s.worldMapTable.Snapshot(), snapshot)
}

func TestRealPVFLowMemoryEntryReplay(t *testing.T) { realMapCacheFullEntryReplay(t, true) }
