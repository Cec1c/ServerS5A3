package dnfbridge

import (
	"context"

	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentRosterSpecialChannelPermissions(ctx context.Context, characters []dnfrepo.CharacterRecord) map[string]dnfquest.SpecialChannelPermission {
	permissions := make(map[string]dnfquest.SpecialChannelPermission)
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Quest == nil || len(characters) == 0 {
		return permissions
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		s.logPacketEvent("game-roster-channel-permission-unavailable", "reason", "pvf_source", "error", err)
		return permissions
	}
	rule, err := dnfquest.LoadSpecialChannelRule(source)
	if err != nil {
		s.logPacketEvent("game-roster-channel-permission-unavailable", "reason", "pvf_entry_quest_rule", "error", err)
		return permissions
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		s.logPacketEvent("game-roster-channel-permission-unavailable", "reason", "quest_catalog", "error", err)
		return permissions
	}
	for _, character := range characters {
		record, found, err := repos.Quest.Load(ctx, character.CharacterID)
		if err != nil || !found || record.CharacterID != character.CharacterID {
			if err != nil {
				s.logPacketEvent("game-roster-channel-permission-unavailable", "char_id", character.CharacterID, "reason", "quest_repository", "error", err)
			}
			continue
		}
		permission := catalog.SpecialChannelPermissionForRecord(rule, record)
		permissions[character.CharacterID] = permission
		s.logPacketEvent("game-roster-channel-permission-projected", "char_id", character.CharacterID, "anton_quest_id", rule.AntonQuestID, "luke_quest_id", rule.LukeQuestID, "fiendwar_quest_id", rule.FiendWarQuestID, "anton", permission.Anton, "luke", permission.Luke, "fiendwar", permission.FiendWar, "source", dnfquest.SpecialChannelRulePath)
	}
	return permissions
}
