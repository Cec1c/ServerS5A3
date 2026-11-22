package dnfbridge

import "strings"

// An admitted single Luke run retains its material policy through raid retirement.
func singleLukeMaterialSuppressed(runtime *runtimeDungeonState, item dungeonDropItemDefinition) bool {
	return runtime != nil && runtime.raidEntry != nil && runtime.raidEntry.singleLukeMode && isLukeMaterial(item)
}

func isLukeMaterial(item dungeonDropItemDefinition) bool {
	return item.Kind == dungeonDropItemStackable && strings.EqualFold(strings.TrimSpace(item.StackableType), "[material]")
}

func (s *Service) currentLukeTeamMonsterMaterialSuppressed(runtime *runtimeDungeonState, item dungeonDropItemDefinition) bool {
	if runtime == nil || runtime.raidEntry == nil || !isLukeMaterial(item) {
		return false
	}
	e := runtime.raidEntry
	if e.ChannelType != 32 || e.RunID == "" {
		return false
	}
	if e.singleLukeMode {
		return true
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
