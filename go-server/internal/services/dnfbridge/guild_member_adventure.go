package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) buildCurrentGuildMemberAdventure(ctx context.Context, a currentGuildMemberProfileAccess, character dnfrepo.CharacterRecord) ([]byte, error) {
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Account == nil {
		return nil, dnfguild.ErrRepositoryUnavailable
	}
	account, found, err := repos.Account.Load(ctx, a.actorAccount)
	if err != nil {
		return nil, err
	}
	if !found || account.AccountID != a.actorAccount {
		return nil, dnfguild.ErrCharacterNotFound
	}
	state, err := s.currentAccountAdventureGroupState(ctx, character, true, a.actor.session)
	if err != nil {
		return nil, err
	}
	days, err := adventuregroup.RecordedDailyLoginDays(account)
	if err != nil {
		return nil, err
	}
	state.Projection = s.projectCurrentAdventureActivity(days, state.Characters)
	state, err = s.projectCurrentAdventureAccountState(state, account)
	if err != nil {
		return nil, err
	}
	created, _, err := adventureGroupCreatedDisplayDate(account)
	if err != nil {
		return nil, err
	}
	return buildCurrentAdventureInfoBodyWithState(a.actor.character, state.Summary, state.Characters, account.RepresentAccountName, created, state.Projection), nil
}
