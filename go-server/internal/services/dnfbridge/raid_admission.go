package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid"
	"strconv"
)

func (s *Service) currentRaidAdmissionRule(session *gameSession) (raid.AdmissionRule, error) {
	if s == nil || session == nil {
		return raid.AdmissionRule{}, fmt.Errorf("raid admission context unavailable")
	}
	path := ""
	switch session.channel.Type {
	case dnfenum.RaidChannelType:
		path = "etc/raid/anton.etc"
	case dnfenum.HiddenRaidType:
		path = "etc/raid/luke/luke.etc"
	default:
		return raid.AdmissionRule{}, fmt.Errorf("unsupported raid channel type=%d", session.channel.Type)
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return raid.AdmissionRule{}, err
	}
	return raid.LoadAdmissionRule(source, path)
}

func (s *Service) handleOnlineRaidEntryCostInfo(session *gameSession, opcode uint16, body []byte) error {
	if session == nil || len(body) != 1 || body[0] > 1 {
		return s.sendRaidCommandFailure(session, opcode)
	}
	s.raidMu.Lock()
	var state runtimeRaidState
	for _, stored := range s.raids {
		if stored != nil && runtimeRaidMemberIndex(*stored, selectedCharacterID(session)) >= 0 {
			state = cloneRuntimeRaidState(*stored)
			break
		}
	}
	s.raidMu.Unlock()
	if state.RaidKey == 0 {
		return s.sendRaidCommandFailure(session, opcode)
	}
	if s.options.currentClientVersion == "official-24.9" && !s.currentRaidAdmissionVisible(session, state) {
		return s.sendRaidCommandFailure(session, opcode)
	}
	statuses, rule, err := s.runtimeRaidEntryCostStatuses(session, state)
	if err != nil {
		s.logGameEvent(session, "game-raid-entry-cost-rejected", "reason", err)
		return s.sendRaidCommandFailure(session, opcode)
	}
	if s.options.currentClientVersion == "official-24.9" && !s.currentRaidAdmissionVisible(session, state) {
		return nil
	}
	if err := s.sendGameUpperRawClassCodec(session, opcode, []byte{1}, dnfproto.DefaultChannelClassification, true); err != nil {
		return err
	}
	return s.sendRuntimeRaidEntryCostStatuses(session, state, statuses, rule)
}

// Only the currently bound member may receive admission data. A stale roster
// must not publish old members or a pre-start inventory state after repository IO.
func (s *Service) currentRaidAdmissionVisible(session *gameSession, state runtimeRaidState) bool {
	if !s.currentRaidInfoVisible(session, state) || runtimeRaidMemberIndex(state, selectedCharacterID(session)) < 0 {
		return false
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	stored := s.raids[state.RaidKey]
	if stored == nil || stored.Started != state.Started || len(stored.Members) != len(state.Members) {
		return false
	}
	for i, member := range stored.Members {
		if member.CharID != state.Members[i].CharID {
			return false
		}
	}
	return stored.Battle == nil || stored.Battle.MemberAccounts[selectedCharacterID(session)] == s.accountIDForSession(session)
}

func (s *Service) runtimeRaidEntryCostStatuses(session *gameSession, state runtimeRaidState) ([]raid.EntryCostStatus, raid.AdmissionRule, error) {
	rule, err := s.currentRaidAdmissionRule(session)
	if err != nil {
		return nil, rule, err
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil || repos.Character == nil {
		return nil, rule, fmt.Errorf("raid admission repositories unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	statuses := make([]raid.EntryCostStatus, 0, len(state.Members))
	for _, member := range state.Members {
		id := strconv.FormatUint(uint64(member.CharID), 10)
		character, found, err := repos.Character.Load(ctx, id)
		if err != nil || !found || character.CharacterID != id {
			return nil, rule, fmt.Errorf("raid admission character unavailable")
		}
		if member.CharID == session.selectedCharacterID && character.AccountID != s.accountIDForSession(session) {
			return nil, rule, fmt.Errorf("raid admission owner mismatch")
		}
		inventory, found, err := repos.Inventory.Load(ctx, id)
		if err != nil || !found {
			return nil, rule, fmt.Errorf("raid admission inventory unavailable")
		}
		count := raidEntryCostOwnedCount(inventory, int64(rule.ItemID))
		statuses = append(statuses, raid.EntryCostStatus{CharacterID: member.CharID, Ready: state.Started || count >= rule.Count, OwnedCount: uint16(count)})
	}
	return statuses, rule, nil
}

func (s *Service) sendRuntimeRaidEntryCostStatuses(session *gameSession, state runtimeRaidState, statuses []raid.EntryCostStatus, rule raid.AdmissionRule) error {
	s.logGameEvent(session, "game-raid-entry-cost-sent", "raid_key", state.RaidKey, "item_id", rule.ItemID, "required", rule.Count, "members", len(statuses))
	return s.sendGameUpperRawClassCodec(session, 598, raid.BuildEntryCostStatusBody(statuses), 0, true)
}

// NOTI598 is independently parsed by the current client. Send it after the
// roster is installed, including approval refreshes that do not trigger CMD658.
func (s *Service) sendCurrentRuntimeRaidEntryCost(session *gameSession, state runtimeRaidState) error {
	if !s.currentRaidAdmissionVisible(session, state) {
		return nil
	}
	statuses, rule, err := s.runtimeRaidEntryCostStatuses(session, state)
	if err != nil {
		s.logGameEvent(session, "game-current-raid-entry-cost-refresh-unavailable", "raid_key", state.RaidKey)
		return nil
	}
	if !s.currentRaidAdmissionVisible(session, state) {
		return nil
	}
	return s.sendRuntimeRaidEntryCostStatuses(session, state, statuses, rule)
}
