package dnfbridge

import (
	"context"
	"strconv"
	"strings"
)

// currentCrossSceneProfileAllowed authorizes the candidate cross-scene profile
// projection. Character ownership comes from the repository; party/raid
// membership comes only from the central in-memory owners. Scene, squad and
// dungeon run identity are deliberately not part of this permission.
func (s *Service) currentCrossSceneProfileAllowed(ctx context.Context, source, target *gameSession, sourceID, targetID uint16) bool {
	if !s.currentCrossSceneProfileMembership(source, target, sourceID, targetID) {
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
	channel := currentTownPresenceChannelID(source)
	sourceAccount, targetAccount := source.accountID, target.accountID
	if channel == 0 || channel != currentTownPresenceChannelID(target) ||
		strings.TrimSpace(sourceAccount) == "" || strings.TrimSpace(targetAccount) == "" {
		return false
	}
	repositories, ok := s.repositoryGroupFor(ctx)
	if !ok || repositories.Character == nil {
		return false
	}
	sourceKey, targetKey := strconv.Itoa(int(sourceID)), strconv.Itoa(int(targetID))
	sourceCharacter, found, err := repositories.Character.Load(ctx, sourceKey)
	if err != nil || !found || sourceCharacter.CharacterID != sourceKey || sourceCharacter.AccountID != sourceAccount {
		return false
	}
	targetCharacter, found, err := repositories.Character.Load(ctx, targetKey)
	if err != nil || !found || targetCharacter.CharacterID != targetKey || targetCharacter.AccountID != targetAccount {
		return false
	}
	// Loads may yield while membership, account, channel or the canonical
	// connection changes. Recheck both the central membership and the exact
	// identities whose ownership was just read, including binding generations.
	return s.currentCrossSceneProfileMembership(source, target, sourceID, targetID) &&
		s.boundGameSessionCharacterCurrent(sourceBound) && s.boundGameSessionCharacterCurrent(targetBound) &&
		currentTownPresenceChannelID(source) == channel && currentTownPresenceChannelID(target) == channel &&
		source.accountID == sourceAccount && target.accountID == targetAccount
}

// currentCrossSceneProfileMembership is the memory-only recheck for callers
// immediately before sending a previously authorized profile. It does not
// replace the repository ownership check above or preserve that call's binding
// generations; callers retaining a prepared body must retain those identities
// too. Call without s.mu or raidMu held. No repository/PVF work or sends occur
// under these checks, and no session party cache is imported as authority.
func (s *Service) currentCrossSceneProfileMembership(source, target *gameSession, sourceID, targetID uint16) bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" ||
		source == nil || target == nil || source == target ||
		sourceID == 0 || targetID == 0 || sourceID == targetID {
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
	channel := currentTownPresenceChannelID(source)
	sourceAccount, targetAccount := source.accountID, target.accountID
	// Do not turn a missing session account into the shared fallback account.
	if channel == 0 || channel != currentTownPresenceChannelID(target) ||
		strings.TrimSpace(sourceAccount) == "" || strings.TrimSpace(targetAccount) == "" {
		return false
	}

	sameParty := false
	if snapshot, found := s.runtimePartyManagerForService().SnapshotByUser(sourceID, 0); found && snapshot.ID != 0 {
		var hasSource, hasTarget bool
		for _, member := range snapshot.Members {
			hasSource = hasSource || member.UserID == sourceID
			hasTarget = hasTarget || member.UserID == targetID
		}
		sameParty = hasSource && hasTarget
	}
	sameRaid := false
	if !sameParty {
		// One live aggregate under one lock, including both presence contexts.
		// In particular Luke members may have different GroupIndex/run values.
		s.raidMu.Lock()
		state := s.runtimeRaidByMemberLocked(sourceID)
		sameRaid = state != nil && state.RaidKey != 0 &&
			runtimeRaidMemberIndex(*state, sourceID) >= 0 && runtimeRaidMemberIndex(*state, targetID) >= 0 &&
			s.raidPresenceContext(source, *state, sourceAccount) && s.raidPresenceContext(target, *state, targetAccount)
		s.raidMu.Unlock()
	}
	return (sameParty || sameRaid) &&
		s.boundGameSessionCharacterCurrent(sourceBound) && s.boundGameSessionCharacterCurrent(targetBound) &&
		currentTownPresenceChannelID(source) == channel && currentTownPresenceChannelID(target) == channel &&
		source.accountID == sourceAccount && target.accountID == targetAccount
}
