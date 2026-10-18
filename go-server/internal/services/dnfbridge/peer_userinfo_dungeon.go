package dnfbridge

import (
	"context"
	"strconv"
	"strings"
)

// currentDungeonPeerUserInfoAllowed is the narrow dungeon-only exception to the
// town same-area gate. It accepts only the two currently bound, same-channel
// sessions of the same non-zero runtime party whose complete current dungeon
// run keys match. Both characters must still belong to their session accounts.
func (s *Service) currentDungeonPeerUserInfoAllowed(
	ctx context.Context,
	source *gameSession,
	target *gameSession,
	sourceID uint16,
	targetID uint16,
) bool {
	if s == nil || source == nil || target == nil || source == target ||
		sourceID == 0 || targetID == 0 || sourceID == targetID {
		return false
	}
	if !s.sessionHasActiveDungeonRuntime(source) || !s.sessionHasActiveDungeonRuntime(target) {
		return false
	}
	sourceBound, ok := s.boundGameSessionCharacterSnapshot(source)
	if !ok || sourceBound.character != sourceID {
		return false
	}
	targetBound, ok := s.boundGameSessionCharacterSnapshot(target)
	if !ok || targetBound.character != targetID {
		return false
	}
	channelID := currentTownPresenceChannelID(source)
	if channelID == 0 || channelID != currentTownPresenceChannelID(target) {
		return false
	}
	sourceParty := runtimePartyStateSnapshot(source)
	targetParty := runtimePartyStateSnapshot(target)
	if sourceParty.PartyID <= 0 || sourceParty.PartyID > 0xFFFF ||
		targetParty.PartyID <= 0 || targetParty.PartyID > 0xFFFF ||
		sourceParty.PartyID != targetParty.PartyID {
		return false
	}
	if _, ok := runtimePartyMemberIndex(sourceID, sourceParty); !ok {
		return false
	}
	if _, ok := runtimePartyMemberIndex(targetID, targetParty); !ok {
		return false
	}
	sourceKey, ok := s.currentDungeonPartyRunKey(source)
	if !ok || sourceKey.PartyID == 0 || sourceKey.PartyID != uint16(sourceParty.PartyID) {
		return false
	}
	targetKey, ok := s.currentDungeonPartyRunKey(target)
	if !ok || targetKey.PartyID == 0 || targetKey.PartyID != uint16(targetParty.PartyID) {
		return false
	}
	if sourceKey != targetKey {
		return false
	}
	sourceAccount := strings.TrimSpace(s.accountIDForSession(source))
	targetAccount := strings.TrimSpace(s.accountIDForSession(target))
	if sourceAccount == "" || targetAccount == "" {
		return false
	}
	repositories, ok := s.repositoryGroupFor(ctx)
	if !ok || repositories.Character == nil {
		return false
	}
	sourceCharacter, sourceFound, sourceErr := repositories.Character.Load(ctx, strconv.Itoa(int(sourceID)))
	if sourceErr != nil || !sourceFound || strings.TrimSpace(sourceCharacter.AccountID) != sourceAccount {
		return false
	}
	targetCharacter, targetFound, targetErr := repositories.Character.Load(ctx, strconv.Itoa(int(targetID)))
	if targetErr != nil || !targetFound || strings.TrimSpace(targetCharacter.AccountID) != targetAccount {
		return false
	}
	return s.boundGameSessionCharacterCurrent(sourceBound) && s.boundGameSessionCharacterCurrent(targetBound)
}
