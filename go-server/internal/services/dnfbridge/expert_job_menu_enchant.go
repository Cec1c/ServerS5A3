package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
)

// resolveCurrentExpertJobMenuEnchantStore accepts the native no-store self menu
// request. The caller holds expertJobStoreOpMu until the existing enchant
// transaction completes. This snapshot is never registered as a public store.
// A placed store or visitor link must keep using the entered-store validation;
// the self path must not bypass a stale or mismatched store context.
func (s *Service) resolveCurrentExpertJobMenuEnchantStore(ctx context.Context, session *gameSession, ownerID uint16) (*currentExpertJobStore, error) {
	if s == nil || session == nil || ownerID == 0 || ownerID != session.selectedCharacterID || s.onlinePlayers == nil || currentExpertJobSessionInDungeon(session) {
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	accountID := strings.TrimSpace(session.accountID)
	if accountID == "" {
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	player, found := s.onlinePlayers.PlayerForCharacter(ownerID)
	if !found || player.Session != session || player.TownID == 0 {
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	s.expertJobStoreMu.Lock()
	_, visiting := s.expertJobVisitors[session]
	_, placed := s.expertJobStores[ownerID]
	s.expertJobStoreMu.Unlock()
	if visiting || placed {
		return nil, dnfexpertjob.ErrMachineInvalid
	}
	repositories, available := s.repositoryGroup()
	if !available || repositories.Character == nil {
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	characterID := strconv.FormatUint(uint64(ownerID), 10)
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return nil, err
	}
	if !found || character.CharacterID != characterID || accountID == "" || character.AccountID != accountID {
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	jobType, experience, err := currentExpertJobCharacterState(&character)
	if err != nil || jobType != dnfexpertjob.EnchanterType {
		return nil, dnfexpertjob.ErrJobUnsupported
	}
	catalog, err := s.currentExpertJobCatalog()
	if err != nil {
		return nil, err
	}
	config, configured := catalog.Enchanter()
	if !configured {
		return nil, dnfexpertjob.ErrJobUnsupported
	}
	// Another connection can replace presence while repository/catalog reads
	// wait. Recheck the accepted identity and scene after that I/O; do not hold
	// the online manager's lock across repository operations.
	current, online := s.onlinePlayers.PlayerForCharacter(ownerID)
	if session.selectedCharacterID != ownerID || strings.TrimSpace(session.accountID) != accountID ||
		currentExpertJobSessionInDungeon(session) || !online || current.Session != session || current.TownID == 0 ||
		current.TownID != player.TownID || current.AreaID != player.AreaID ||
		current.ChannelID != player.ChannelID || current.Instance != player.Instance {
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	// Preserve the existing transaction's persisted-endurance equality check,
	// material/card validation, self-fee semantics and account-owned asset lock.
	return &currentExpertJobStore{
		OwnerCharacterID: ownerID, OwnerSession: session, Kind: currentExpertJobEnchantStoreKind,
		TownID: player.TownID, AreaID: player.AreaID, ChannelID: player.ChannelID, Instance: player.Instance,
		Endurance:      currentExpertJobStatDefault(character.Stats, currentExpertJobMachineEnduranceStat, config.InitialEndurance),
		Qualifications: config.Qualifications(experience),
	}, nil
}
