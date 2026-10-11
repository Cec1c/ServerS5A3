package dnfbridge

import "longheng.io/server/internal/modules/dnf/raid"

// Availability exposes the real channel, without authorizing a character or
// spending tickets. The normal raid must have both phases and all indexed
// dependencies. The single route retains its own admission dependency check.
func (s *Service) currentLukeNormalChannelAvailable(session *gameSession) bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return false
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err == nil {
		_, err = raid.LoadLukeProgram(source)
	}
	if err == nil {
		_, err = raid.LoadLukeRewardConfiguration(source)
	}
	if err == nil {
		_, err = raid.LoadLukeFlowConfiguration(source)
	}
	if err == nil {
		var config raid.BattleConfiguration
		config, err = raid.LoadBattleConfiguration(source, "etc/raid/luke/luke.etc")
		if err == nil {
			var ids []uint32
			for _, phase := range config.PhaseDungeons {
				for _, d := range phase {
					ids = append(ids, d.ID)
				}
			}
			err = raid.ValidateDungeonDependencies(source, ids)
		}
	}
	if err != nil {
		s.logGameEvent(session, "game-luke-channel-availability", "event_id", currentLukeChannelEventID, "available", false, "error", err)
		return false
	}
	return true
}
