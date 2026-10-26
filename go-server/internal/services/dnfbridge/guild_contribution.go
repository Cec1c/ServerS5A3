package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) guildContributionEnabled() bool {
	return strings.TrimSpace(s.options.guildContributionPolicy) != ""
}

func (s *Service) currentGuildContributionContract() (*guild.ContributionContract, error) {
	policy := guild.ContributionPolicy{SkipAttendanceJoinDay: true}
	switch s.options.guildContributionPolicy {
	case "daily-v1":
		policy.GuildClearDailyAwards = 1
	default:
		return nil, guild.ErrContributionState
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	s.guildDungeonCatalogMu.Lock()
	c := s.guildContributionCatalog
	if c == nil {
		c, err = guild.LoadContributionCatalog(items.source)
		if err == nil {
			s.guildContributionCatalog = c
		}
	}
	s.guildDungeonCatalogMu.Unlock()
	if err != nil {
		return nil, err
	}
	item, err := currentDungeonCardItemReward(newCurrentDungeonDropLCG(1), items, uint32(guild.ContributionCoinID), s.gameplayNow())
	if err != nil {
		return nil, err
	}
	if !item.Stackable || !item.ExpireAt.IsZero() {
		return nil, guild.ErrContributionState
	}
	item.Extra["source"] = "guild_contribution"
	definition := dungeon.CardItemReward{Stack: r.ItemStack{ItemID: item.ItemID, Count: 1, Bind: item.Bind, RawEntry: append([]byte(nil), item.RawEntry...), Extra: cloneDungeonCardStringMap(item.Extra)}, Stackable: item.Stackable, SlotStart: item.SlotStart, SlotEnd: item.SlotEnd, StackLimit: item.StackLimit}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, guild.ErrRepositoryUnavailable
	}
	return &guild.ContributionContract{Catalog: c, Policy: policy, Deliver: dungeon.GuildContributionDelivery(repos.Settings, definition, nil)}, nil
}

// The caller owns this character's asset reservation. Chat already reserves
// all recipients in canonical order; it must not reserve the sender again.
func (s *Service) grantCurrentGuildContributionReserved(ctx context.Context, session *gameSession, identity boundGameSessionCharacter, event guild.ContributionEvent) (guild.ContributionIncome, error) {
	if !s.guildContributionEnabled() {
		return guild.ContributionIncome{}, nil
	}
	if identity.session != session || !s.boundGameSessionCharacterCurrent(identity) {
		return guild.ContributionIncome{}, guild.ErrCharacterNotFound
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return guild.ContributionIncome{}, guild.ErrRepositoryUnavailable
	}
	u, ok := repos.CharacterSettlement.(r.GuildSettlementUnitOfWork)
	if !ok {
		return guild.ContributionIncome{}, guild.ErrRepositoryUnavailable
	}
	contract, err := s.currentGuildContributionContract()
	if err != nil {
		return guild.ContributionIncome{}, err
	}
	return guild.GrantDailyContribution(ctx, u, r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}, contract, event, s.gameplayNow(), func() bool { return s.boundGameSessionCharacterCurrent(identity) })
}

func (s *Service) projectCurrentGuildContribution(session *gameSession, income guild.ContributionIncome) error {
	if income.Points == 0 {
		return nil
	}
	if income.Delivery.MailID != "" {
		if err := s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "guild_contribution"); err != nil {
			return err
		}
	} else {
		if income.Delivery.Slot < 0 {
			return guild.ErrContributionState
		}
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "guild_contribution", []alignedcmd.ItemSlotRefresh{{ListType: r.MainInventoryListType, SlotIndex: income.Delivery.Slot}}); err != nil {
			return err
		}
	}
	g, err := s.currentGuildForSelectedCharacter(session)
	if err != nil {
		return err
	}
	if g == nil {
		return nil
	}
	body, err := buildCurrentGuildHongbaoPointsBody(guild.SortedMembers(g))
	if err != nil {
		return err
	}
	return s.sendGameUpperSuccess(session, currentGuildHongbaoPointsOpcode, body)
}

// Login enrichment never propagates a storage or projection failure into the
// mandatory character-selection packet loop. A later login retries the same
// durable event, and native inventory/mail queries read its committed state.
func (s *Service) grantOptionalGuildContributionAttendanceLocked(session *gameSession) {
	if !s.guildContributionEnabled() {
		return
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err == nil {
		defer release()
		var income guild.ContributionIncome
		income, err = s.grantCurrentGuildContributionReserved(ctx, session, identity, guild.ContributionAttendance)
		if err == nil {
			err = s.projectCurrentGuildContribution(session, income)
		}
	}
	if err != nil {
		session.guildContributionLoginRetry = identity
		s.logGameEvent(session, "game-guild-contribution-login-deferred", "error", err)
	} else {
		session.guildContributionLoginRetry = boundGameSessionCharacter{}
	}
}

func (s *Service) retryOptionalGuildContributionAttendanceLocked(ctx context.Context, session *gameSession, identity boundGameSessionCharacter) {
	if !s.guildContributionEnabled() {
		return
	}
	if session.guildContributionLoginRetry != identity {
		session.guildContributionLoginRetry = boundGameSessionCharacter{}
		return
	}
	income, err := s.grantCurrentGuildContributionReserved(ctx, session, identity, guild.ContributionAttendance)
	if err == nil {
		err = s.projectCurrentGuildContribution(session, income)
	}
	if err != nil {
		s.logGameEvent(session, "game-guild-contribution-login-retry-deferred", "error", err)
		return
	}
	session.guildContributionLoginRetry = boundGameSessionCharacter{}
}
