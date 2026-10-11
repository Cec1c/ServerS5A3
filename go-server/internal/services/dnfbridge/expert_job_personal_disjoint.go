package dnfbridge

import (
	"context"
	"strconv"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
)

// The native profession panel sends CMD235 for the owner's own machine even
// without opening a public shop. Build that private operation context from
// persisted profession state; do not register/project a shop or admit peers.
func (s *Service) currentPersonalDisjointerContext(session *gameSession, ownerID uint16) (*currentExpertJobStore, error) {
	if session == nil || ownerID == 0 || ownerID != session.selectedCharacterID || currentExpertJobSessionInDungeon(session) {
		return nil, dnfexpertjob.ErrExtractionInvalid
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil, dnfexpertjob.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(ownerID)))
	if err != nil {
		return nil, err
	}
	if !found || character.AccountID != s.accountIDForSession(session) {
		return nil, dnfexpertjob.ErrAccountMismatch
	}
	job, _, err := currentExpertJobCharacterState(&character)
	if err != nil || job != dnfexpertjob.DisjointerType {
		return nil, dnfexpertjob.ErrJobUnsupported
	}
	catalog, err := s.currentExpertJobCatalog()
	if err != nil {
		return nil, err
	}
	config, ok := catalog.Disjointer()
	if !ok {
		return nil, dnfexpertjob.ErrJobUnsupported
	}
	return &currentExpertJobStore{OwnerCharacterID: ownerID, OwnerSession: session, Kind: currentExpertJobDisjointStoreKind, MachineGrade: int(currentExpertJobStatDefault(character.Stats, currentExpertJobMachineGradeStat, 1)), Endurance: currentExpertJobStatDefault(character.Stats, currentExpertJobMachineEnduranceStat, config.InitialEndurance)}, nil
}
