package dnfbridge

import "longheng.io/server/internal/modules/dnf/raid"

// 入口活动只反映真实PVF依赖是否可用；角色前置、门票和次数仍由入场事务检查。
func (s *Service) currentFiendWarChannelAvailable(session *gameSession) bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return false
	}
	config, err := s.loadCurrentFiendWarConfig()
	if err == nil {
		s.initialEquipmentMu.Lock()
		source, loadErr := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		err = loadErr
		if err == nil {
			_, err = raid.LoadFiendWarRewardConfiguration(source)
		}
		if err == nil {
			var ids []uint32
			for _, area := range config.Battle.Areas {
				if area.Dungeon > 0 {
					ids = append(ids, uint32(area.Dungeon))
				}
			}
			err = raid.ValidateDungeonDependencies(source, ids)
		}
	}
	if err != nil {
		s.logGameEvent(session, "超时空频道开放依赖不可用", "event_id", currentFiendWarChannelEventID, "error", err)
		return false
	}
	return true
}
