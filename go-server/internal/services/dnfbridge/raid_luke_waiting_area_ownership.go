package dnfbridge

import "strings"

// Fixed 369d PVF: luke.etc [WAITING ROOM] = 32 4, Jelva_raid.twn
// area4 = 4_Jelva_G.map, dungeon gate40 = worldmap/Luke_Raid.wdm.
// Area2's misleading Waiting filename belongs to gate32/CastleoftheDead;
// it is not the program's waiting room and must retain its ordinary route.
func (s *Service) currentLukeWaitingArea(townID, areaID byte) bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" || townID != 32 || areaID != 4 {
		return false
	}
	area, found := s.townArea(int64(townID), int64(areaID))
	return found && strings.EqualFold(strings.ReplaceAll(area.MapPath, "\\", "/"), "Town/Jelva_raid/4_Jelva_G.map") && area.DungeonGate != nil && *area.DungeonGate == 40
}
