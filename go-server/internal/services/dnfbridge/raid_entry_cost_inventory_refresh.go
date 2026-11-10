package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

// runtimeRaidEntryCostInventoryBaseline is the per-command before-image used
// to decide whether a committed inventory operation actually changed the
// player's usable entry material. It deliberately contains no roster or actor
// state; the post-commit path reloads and revalidates both.
type runtimeRaidEntryCostInventoryBaseline struct {
	tracked     bool
	raidKey     uint32
	characterID uint16
	generation  uint64
	accountID   string
	operation   string
	rule        raid.AdmissionRule
	owned       int64
}

// Inventory, mail extraction and package opens all cross this aligned-command
// transaction boundary. Commands from unrelated domains retain their previous
// behavior and do not pay for an inventory read while a waiting room is open.
func runtimeRaidEntryCostInventoryMutationCommand(opcode uint16) bool {
	command, ok := dnfenum.LookupAlignedCommand(opcode)
	if !ok {
		return false
	}
	switch command.Domain {
	case dnfenum.AlignedDomainInventory, dnfenum.AlignedDomainMail, dnfenum.AlignedDomainPackage:
		return true
	default:
		return false
	}
}

func (s *Service) captureRuntimeRaidEntryCostInventoryBeforeCommand(session *gameSession, opcode uint16) runtimeRaidEntryCostInventoryBaseline {
	if s == nil || !runtimeRaidEntryCostInventoryMutationCommand(opcode) {
		return runtimeRaidEntryCostInventoryBaseline{}
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || s.options.currentClientVersion != "official-24.9" {
		return runtimeRaidEntryCostInventoryBaseline{}
	}
	state, found := s.runtimeRaidForMember(identity.character)
	if !found || state.Started || !s.currentRaidAdmissionVisible(session, state) {
		return runtimeRaidEntryCostInventoryBaseline{}
	}
	rule, err := s.currentRaidAdmissionRule(session)
	if err != nil {
		return runtimeRaidEntryCostInventoryBaseline{}
	}
	return s.captureRuntimeRaidEntryCostInventoryWithRule(session, opcode, rule)
}

// captureRuntimeRaidEntryCostInventoryWithRule is also the deterministic unit
// test seam: production obtains rule from the runtime raid configuration.
func (s *Service) captureRuntimeRaidEntryCostInventoryWithRule(session *gameSession, opcode uint16, rule raid.AdmissionRule) runtimeRaidEntryCostInventoryBaseline {
	if s == nil || session == nil || !runtimeRaidEntryCostInventoryMutationCommand(opcode) || rule.ItemID == 0 || rule.Count <= 0 {
		return runtimeRaidEntryCostInventoryBaseline{}
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || s.options.currentClientVersion != "official-24.9" {
		return runtimeRaidEntryCostInventoryBaseline{}
	}
	state, found := s.runtimeRaidForMember(identity.character)
	if !found || state.Started || !s.currentRaidAdmissionVisible(session, state) {
		return runtimeRaidEntryCostInventoryBaseline{}
	}
	owned, err := s.runtimeRaidEntryCostOwnedForSession(session, rule)
	if err != nil {
		return runtimeRaidEntryCostInventoryBaseline{}
	}
	return runtimeRaidEntryCostInventoryBaseline{
		tracked:     true,
		raidKey:     state.RaidKey,
		characterID: identity.character,
		generation:  identity.generation,
		accountID:   s.accountIDForSession(session),
		operation:   runtimeRaidEntryCostInventoryOperationName(opcode),
		rule:        rule,
		owned:       owned,
	}
}

// The normal dispatcher calls this only after the domain transaction and its
// client inventory projection both succeeded. An unchanged material count
// therefore produces no roster, actor, or NOTI598 traffic.
func (s *Service) refreshRuntimeRaidEntryCostInventoryAfterCommand(session *gameSession, baseline runtimeRaidEntryCostInventoryBaseline) error {
	if s == nil || session == nil || !baseline.tracked {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.character != baseline.characterID || identity.generation != baseline.generation || s.accountIDForSession(session) != baseline.accountID {
		return nil
	}
	state, found := s.runtimeRaidForMember(identity.character)
	if !found || state.RaidKey != baseline.raidKey || state.Started || !s.currentRaidAdmissionVisible(session, state) {
		return nil
	}
	owned, err := s.runtimeRaidEntryCostOwnedForSession(session, baseline.rule)
	if err != nil {
		return err
	}
	if owned == baseline.owned {
		return nil
	}
	s.logGameEvent(session, "game-raid-entry-cost-inventory-committed",
		"raid_key", state.RaidKey,
		"character_id", identity.character,
		"operation", baseline.operation,
		"item_id", baseline.rule.ItemID,
		"owned_before", baseline.owned,
		"owned_after", owned)
	return s.broadcastRuntimeRaidEntryCostInventoryRefresh(state)
}

func (s *Service) runtimeRaidEntryCostOwnedForSession(session *gameSession, rule raid.AdmissionRule) (int64, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || rule.ItemID == 0 {
		return 0, fmt.Errorf("raid entry-cost inventory context unavailable")
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil || repositories.Inventory == nil {
		return 0, fmt.Errorf("raid entry-cost inventory repositories unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil || !found || character.CharacterID != characterID || character.AccountID != s.accountIDForSession(session) {
		return 0, fmt.Errorf("raid entry-cost inventory owner unavailable")
	}
	inventory, found, err := repositories.Inventory.Load(ctx, characterID)
	if err != nil || !found || inventory.CharacterID != characterID {
		return 0, fmt.Errorf("raid entry-cost inventory snapshot unavailable")
	}
	return raidEntryCostOwnedCount(inventory, int64(rule.ItemID)), nil
}

func (s *Service) broadcastRuntimeRaidEntryCostInventoryRefresh(state runtimeRaidState) error {
	if s == nil || state.RaidKey == 0 || state.Started || len(state.Members) == 0 {
		return nil
	}
	// One batch owns one committed inventory read for every member. Each send
	// still rechecks the live roster and the recipient account before writing.
	batch := &currentRaidAdmissionBatch{}
	for _, member := range state.Members {
		identity, online := s.onlineGameSessionCharacterSnapshot(member.CharID)
		if !online || !s.boundGameSessionCharacterCurrent(identity) || identity.character != member.CharID {
			continue
		}
		if !s.currentRaidAdmissionVisible(identity.session, state) {
			continue
		}
		if err := s.sendCurrentRuntimeRaidEntryCostWithBatch(identity.session, state, batch); err != nil {
			s.logGameEvent(identity.session, "game-raid-entry-cost-inventory-refresh-failed",
				"raid_key", state.RaidKey,
				"character_id", member.CharID,
				"error", err)
		}
	}
	return nil
}

func runtimeRaidEntryCostInventoryOperationName(opcode uint16) string {
	if command, ok := dnfenum.LookupAlignedCommand(opcode); ok {
		return string(command.Domain) + ":" + strconv.FormatUint(uint64(opcode), 10)
	}
	return "unknown:" + strconv.FormatUint(uint64(opcode), 10)
}
