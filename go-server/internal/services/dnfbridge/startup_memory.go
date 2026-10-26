package dnfbridge

import (
	platformpvf "longheng.io/server/internal/platform/pvf"
	"runtime/debug"
)

// 低内存档只在启动阶段丢弃可重建缓存，不在玩家请求中强制GC。
func (s *Service) releaseStartupPVFStageCaches() {
	if s == nil || !s.options.lowMemory {
		return
	}
	s.initialEquipmentMu.Lock()
	defer s.initialEquipmentMu.Unlock()
	if s.initialEquipmentArchive != nil {
		s.initialEquipmentArchive.ReleaseReadCaches()
	}
}

// 业务目录已把所需规则保存为结构化数据。一次性全量扫描形成的解码缓存
// 可以在开放监听前释放，之后只有实际用到的脚本会重新进入缓存。
func (s *Service) releaseStartupPVFReadCaches() {
	if s == nil {
		return
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	if archive != nil {
		archive.ReleaseReadCaches()
	}
	s.initialEquipmentMu.Unlock()
	if archive != nil {
		// 只在启动预载结束时回收一次，归还扫描期的空闲页；不放到玩家请求路径。
		debug.FreeOSMemory()
	}
}

// 框架传入的已有归档和按路径新开的归档都经过同一预算入口。
func (s *Service) prepareLowMemoryPVFReadCaches(a *platformpvf.Archive) error {
	if !s.options.lowMemory || a == nil || a.BoundedReadCacheSnapshot() != nil {
		return nil
	}
	return a.EnableBoundedReadCaches(8<<20, 8<<20)
}
