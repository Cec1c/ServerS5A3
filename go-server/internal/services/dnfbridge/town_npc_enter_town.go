package dnfbridge

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"os"
	"strings"
)

const townNPCEnterTownEnv = "DNFBRIDGE_TOWN_NPC_ENTER_TOWN"

func currentTownNPCEnterTownShape(request currentTownSetUserAreaRequest, character dnfrepo.CharacterRecord) bool {
	town, townOK := character.Stats["town_id"]
	area, areaOK := character.Stats["area_id"]
	return townOK && areaOK && town > 0 && town <= 65535 && area >= 0 && area <= 65535 && int64(request.TownID) != town && request.OpaqueU16A == uint16(town) && request.OpaqueU16B == uint16(area) && request.OpaqueU32 > 0 && request.Direction == 5 && request.OpaqueTailU8 == 0
}

func validateCurrentTownNPCEnterTown(source dnfpvf.Source, request currentTownSetUserAreaRequest, character dnfrepo.CharacterRecord, sourceArea, targetArea dnftown.Area) (bool, error) {
	if !currentTownNPCEnterTownShape(request, character) {
		return false, nil
	}
	return worldmap.NPCEnterTownLanding(source, character.Stats["town_id"], sourceArea.MapPath, targetArea.MapPath, int64(request.OpaqueU32), int64(request.PositionX), int64(request.PositionY))
}

func (s *Service) currentTownNPCEnterTownRequest(session *gameSession, request *currentTownSetUserAreaRequest, character dnfrepo.CharacterRecord, targetArea dnftown.Area) bool {
	if s == nil || request == nil || s.options.currentClientVersion != "official-24.9" || !currentTownNPCEnterTownShape(*request, character) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(townNPCEnterTownEnv))) {
	case "off", "0", "false", "no", "disabled", "legacy":
		return false
	}
	sourceArea, ok := s.townArea(character.Stats["town_id"], character.Stats["area_id"])
	if !ok {
		return false
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return false
	}
	landing, ok, err := worldmap.ResolveNPCEnterTownLanding(archive, character.Stats["town_id"], sourceArea.MapPath, targetArea.MapPath, int64(request.OpaqueU32))
	if err != nil {
		s.logGameEvent(session, "game-town-npc-enter-town-validation-failed", "npc_id", request.OpaqueU32, "source_town_id", request.OpaqueU16A, "source_area_id", request.OpaqueU16B, "target_town_id", request.TownID, "target_area_id", request.AreaID, "error", err)
	}
	if err != nil || !ok || landing.X < 0 || landing.X > 32767 || landing.Y < 0 || landing.Y > 32767 {
		return false
	}
	if int64(request.PositionX) != landing.X || int64(request.PositionY) != landing.Y {
		s.logGameEvent(session, "game-town-npc-enter-town-landing-normalized", "npc_id", request.OpaqueU32,
			"source_town_id", request.OpaqueU16A, "source_area_id", request.OpaqueU16B,
			"target_town_id", request.TownID, "target_area_id", request.AreaID,
			"request_x", request.PositionX, "request_y", request.PositionY, "landing_x", landing.X, "landing_y", landing.Y,
			"reason", "verified_pvf_destination_npc_spawn")
	}
	request.PositionX, request.PositionY = int16(landing.X), int16(landing.Y)
	return true
}
