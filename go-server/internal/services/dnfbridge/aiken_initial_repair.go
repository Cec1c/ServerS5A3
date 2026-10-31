package dnfbridge

import (
	"context"
	"math"
	"strconv"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
)

func (s *Service) repairCurrentAikenInitialOptions(ctx context.Context, session *gameSession) (int, error) {
	if s.options.currentClientVersion != "official-24.9" || session == nil || session.selectedCharacterID == 0 {
		return 0, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.AccountAssets == nil {
		return 0, nil
	}
	owner, err := aiken.NewOwner(repos)
	if err != nil {
		return 0, err
	}
	return owner.RepairInitialOptions(ctx, aiken.InitialRepairCommand{
		AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID)), Now: s.gameplayNow(),
		ReadState: currentAikenState, WriteState: currentAikenWriteState, Roll: currentAikenInitialRoll,
		Catalog: func(id int64) (*aiken.InitialCatalog, error) {
			if id <= 0 || id > math.MaxUint32 {
				return nil, nil
			}
			catalog, err := s.currentPVFItemCatalog()
			if err != nil {
				return nil, err
			}
			definition, err := catalog.ResolveItem(uint32(id))
			return definition.AikenInitial, err
		},
	})
}
