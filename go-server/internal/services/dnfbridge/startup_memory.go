package dnfbridge

import "runtime/debug"

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
