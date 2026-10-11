package dnfbridge

import "strings"

// Only the committed multiplayer Luke binding suppresses monster materials.
// Normal dungeons, single mode, gold/equipment and phase cards keep their owners.
func (s *Service) currentLukeTeamMonsterMaterialSuppressed(runtime *runtimeDungeonState, item dungeonDropItemDefinition) bool {
	if runtime == nil || runtime.raidEntry == nil || item.Kind != dungeonDropItemStackable || !strings.EqualFold(strings.TrimSpace(item.StackableType), "[material]") {
		return false
	}
	e := runtime.raidEntry
	if e.ChannelType != 32 || e.RunID == "" {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	state := s.raids[e.RaidKey]
	if state == nil || state.Battle == nil {
		return false
	}
	b := state.Battle
	return b.Mode == 2 && !b.Single && b.InstanceID == e.InstanceID && b.Phase == e.Phase && b.Anton != nil && b.Anton.Epoch() == e.Attempt && b.ChannelType == e.ChannelType && b.ChannelID == e.ChannelID
}
