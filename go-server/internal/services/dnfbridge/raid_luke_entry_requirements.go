package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/raid"
)

// Caller holds raidMu and has already authenticated the frozen member accounts.
// Completed quests are read here; inventory holdings are checked under the same
// row locks as ticket consumption, so a late transfer cannot authorize a start.
func (s *Service) currentRaidHoldingRequirements(ctx context.Context, session *gameSession, state runtimeRaidState) ([]raid.AdmissionRule, error) {
	if s.options.currentClientVersion != "official-24.9" || session.channel.Type != dnfenum.HiddenRaidType {
		return nil, nil
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	rule, err := dnfquest.LoadLukeEntryRequirement(source)
	if err != nil {
		return nil, err
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return nil, err
	}
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Quest == nil {
		return nil, fmt.Errorf("Luke entry quest repository unavailable")
	}
	for _, member := range state.Members {
		id := strconv.Itoa(int(member.CharID))
		record, found, err := repos.Quest.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found || record.CharacterID != id || !catalog.LukeEntryQuestsCompleted(rule, record) {
			return nil, fmt.Errorf("Luke entry quests incomplete for member %s", id)
		}
	}
	return []raid.AdmissionRule{{ItemID: uint32(rule.ItemID), Count: rule.Count}}, nil
}
