package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

type currentAntonRewardRecipient struct {
	CharacterID uint16
	AccountID   string
	Group       byte
	Clears      uint32
}

// Pending grants survive lobby removal. They remain in-process intents; asset
// receipts and completed weekly counts are persisted by their domain owners.
type currentAntonRewardFlow struct {
	EntryRefund        *raid.EntryDebit
	FiendWarRetryCount uint32
	InstanceID         string
	RaidKey            uint32
	Phase              byte
	Mode               byte
	Single             bool
	CompletedAt        time.Time
	Configuration      raid.AntonFlowConfiguration
	Rewards            raid.AntonPhaseRewards
	Rank               byte
	Recipients         []currentAntonRewardRecipient
	Party              map[uint16]*currentAntonRewardCard
	Squad              map[uint16]*currentAntonRewardCard
	Weekly             raid.WeeklyClearCommand
	AutoSelectAt       time.Time
	PartyShownAt       time.Time
	PartySelectAfter   map[uint16]time.Time
	PartyCommittedAt   time.Time
	PartyManual        bool
	SquadShownAt       time.Time
	FinishedAt         time.Time
	BreakUntil         time.Time
	NextAttempt        time.Time
	OwnerNextAttempt   map[uint16]time.Time
	TeamDisplayReady   map[uint16]bool
}

func (f *currentAntonRewardFlow) squadRequired() bool {
	// Absence is legal only for Luke's configured first phase. All other
	// flows require the actual squad pool and retain the grant barrier.
	return f.Mode != 2 || f.Phase != 0 || f.Single
}

func cloneCurrentAntonRewardFlow(f *currentAntonRewardFlow) *currentAntonRewardFlow {
	if f == nil {
		return nil
	}
	v := *f
	if f.EntryRefund != nil {
		debit := *f.EntryRefund
		v.EntryRefund = &debit
	}
	v.Recipients = append([]currentAntonRewardRecipient(nil), f.Recipients...)
	v.Rewards.Ranks = append([]raid.AntonRankRange(nil), f.Rewards.Ranks...)
	v.Rewards.Choices = append([]raid.AntonRewardChoice(nil), f.Rewards.Choices...)
	v.Weekly.Members = append([]raid.ClearMember(nil), f.Weekly.Members...)
	clone := func(cards map[uint16]*currentAntonRewardCard) map[uint16]*currentAntonRewardCard {
		out := map[uint16]*currentAntonRewardCard{}
		for id, card := range cards {
			if card != nil {
				c := *card
				c.Plan = cloneCurrentAntonRewardPlan(card.Plan)
				out[id] = &c
			}
		}
		return out
	}
	v.Party, v.Squad = clone(f.Party), clone(f.Squad)
	if f.TeamDisplayReady != nil {
		v.TeamDisplayReady = make(map[uint16]bool, len(f.TeamDisplayReady))
		for id, ready := range f.TeamDisplayReady {
			v.TeamDisplayReady[id] = ready
		}
	}
	if f.OwnerNextAttempt != nil {
		v.OwnerNextAttempt = make(map[uint16]time.Time, len(f.OwnerNextAttempt))
		for id, at := range f.OwnerNextAttempt {
			v.OwnerNextAttempt[id] = at
		}
	}
	if f.PartySelectAfter != nil {
		v.PartySelectAfter = make(map[uint16]time.Time, len(f.PartySelectAfter))
		for id, at := range f.PartySelectAfter {
			v.PartySelectAfter[id] = at
		}
	}
	return &v
}

// NOTI600 constructs the reward-card panel with a 0x3a98 (15000 ms) timer, and
// the matching party-panel tick expires at the same value. This card-choice
// control is independent of MOVIE END TIME. The party subpanel also uses this
// timer in native stage4 before the team panel can replace it. Cinematic
// fallbacks and the final team-display completion still use their own windows.
const currentRaidNativeCardChoiceWindow = 15 * time.Second

func (s *Service) markCurrentRaidPartySelectionShownLocked(instance string, phase byte, character uint16, at time.Time) {
	f := s.antonRewardFlows[currentAntonFlowKey(instance, phase)]
	if f == nil || at.IsZero() {
		return
	}
	for _, owner := range f.Recipients {
		if owner.CharacterID != character {
			continue
		}
		if f.PartySelectAfter == nil {
			f.PartySelectAfter = make(map[uint16]time.Time)
		}
		if _, exists := f.PartySelectAfter[character]; !exists {
			f.PartySelectAfter[character] = at.Add(currentRaidNativeCardChoiceWindow)
		}
		return
	}
}

func currentAntonFlowKey(instance string, phase byte) string {
	return instance + ":" + strconv.Itoa(int(phase))
}

// Normal Anton phase zero has two display stages. An unattended automatic
// selection must leave the committed party cards visible for the native
// stage4 card window before the squad list replaces them. Once any owner has
// manually selected, that party page has already been presented; do not impose
// the unattended display hold a second time after the last manual card commits.
func markCurrentAntonPartyCommittedLocked(f *currentAntonRewardFlow, now time.Time) bool {
	if f == nil || !f.PartyCommittedAt.IsZero() {
		return f != nil && !f.PartyCommittedAt.IsZero()
	}
	for _, owner := range f.Recipients {
		card := f.Party[owner.CharacterID]
		if card == nil || !card.Committed {
			return false
		}
	}
	f.PartyCommittedAt = now
	return true
}

func currentAntonMovieWindow(f *currentAntonRewardFlow) time.Duration {
	var movie uint32
	for _, seconds := range f.Configuration.MovieEndSeconds {
		movie = max(movie, seconds)
	}
	return time.Duration(movie) * time.Second
}

// The phase-clear timestamp starts the server fallback clock, but a client 659
// need not arrive at that instant. Once a card list was actually delivered,
// use the native card-panel timer for its automatic flip. The unattended
// deadline remains the fallback when no screen reached any client.
func currentAntonPartySelectAt(f *currentAntonRewardFlow, movie time.Duration) time.Time {
	if f == nil {
		return time.Time{}
	}
	at := f.AutoSelectAt
	if f.PartyShownAt.IsZero() {
		if !at.IsZero() {
			at = at.Add(movie)
		}
		return at
	}
	if shown := f.PartyShownAt.Add(currentRaidNativeCardChoiceWindow); shown.After(at) {
		at = shown
	}
	return at
}

// Called under raidMu after a party card list was successfully sent. The first
// actual delivery is the only show anchor; later projections must not extend
// the selection window indefinitely.
func (s *Service) markCurrentAntonPartyShownLocked(instance string, phase byte, at time.Time) {
	if at.IsZero() {
		return
	}
	f := s.antonRewardFlows[currentAntonFlowKey(instance, phase)]
	if f == nil || f.Mode != 1 || f.Phase != 0 || f.Single || !f.PartyShownAt.IsZero() {
		return
	}
	f.PartyShownAt = at
}

// Called under raidMu exclusively at a genuine completed-run boundary (or an
// idempotent clock retry). A client movie/card request never creates eligibility.
func (s *Service) freezeCurrentAntonRewardFlowLocked(state runtimeRaidState) error {
	b := state.Battle
	if b != nil && b.FiendWar != nil {
		return s.freezeCurrentFiendWarRewardFlowLocked(state)
	}
	if b == nil || (b.Mode != 1 && b.Mode != 2) || !b.PhaseCompleted || b.AntonFlowConfig.PhaseBreakSeconds == 0 {
		return nil
	}
	if b.Phase > 1 {
		return raid.ErrRewardIneligible
	}
	if b.RewardFlow[b.Phase] != nil {
		return nil
	}
	result, err := prepareCurrentAntonResultProjection(state, 0)
	if err != nil || result.Failed {
		return raid.ErrRewardIneligible
	}
	f := &currentAntonRewardFlow{InstanceID: b.InstanceID, RaidKey: state.RaidKey, Phase: b.Phase, Mode: b.Mode, Single: b.Single, CompletedAt: b.PhaseCompletedAt,
		Configuration: b.AntonFlowConfig, Rewards: b.AntonRewards.Clone().Phases[b.Phase], Rank: result.DomainRank,
		Party: b.RewardCards[b.Phase], Squad: map[uint16]*currentAntonRewardCard{}}
	if f.Party == nil {
		f.Party = map[uint16]*currentAntonRewardCard{}
	}
	groups := map[byte]int{}
	for id, account := range b.MemberAccounts {
		clears := currentAntonPhaseClearCount(b, id)
		if clears < f.Rewards.MinimumClears {
			continue
		}
		group := b.MemberGroups[id]
		if index := runtimeRaidMemberIndex(state, id); index >= 0 {
			group = state.Members[index].GroupIndex
		}
		if account == "" || group < 1 || group > runtimeRaidMaxGroups {
			return raid.ErrRewardOwnership
		}
		groups[group]++
		if groups[group] > 4 {
			return fmt.Errorf("Anton qualified reward group exceeds four native cards")
		}
		f.Recipients = append(f.Recipients, currentAntonRewardRecipient{id, account, group, clears})
	}
	if len(f.Recipients) == 0 || len(f.Recipients) > raid.MaxAttackPartyMembers {
		return raid.ErrRewardIneligible
	}
	sort.Slice(f.Recipients, func(i, j int) bool { return f.Recipients[i].CharacterID < f.Recipients[j].CharacterID })
	if f.Single || f.Phase == 1 {
		f.Weekly, err = prepareCurrentRaidFinalWeeklyCommand(state)
		if err != nil {
			return err
		}
	}
	// Movie lengths bound unattended completion. Card-selection grace is a
	// server policy of one additional actual movie-length window, not a claim
	// that an unverified MOVIE END TIME slot describes card selection.
	var movie uint32
	for _, seconds := range f.Configuration.MovieEndSeconds {
		if seconds == 0 {
			return raid.ErrRewardIneligible
		}
		movie = max(movie, seconds)
	}
	f.AutoSelectAt = f.CompletedAt.Add(2 * time.Duration(movie) * time.Second)
	key := currentAntonFlowKey(f.InstanceID, f.Phase)
	if s.antonRewardFlows == nil {
		s.antonRewardFlows = map[string]*currentAntonRewardFlow{}
	}
	if s.antonRewardFlows[key] != nil {
		return raid.ErrRewardReceipt
	}
	s.antonRewardFlows[key], b.RewardFlow[b.Phase], b.RewardCards[b.Phase] = f, f, f.Party
	return nil
}

func (s *Service) currentAntonFlowWeeklyCommittedLocked(f *currentAntonRewardFlow) bool {
	if f.EntryRefund != nil {
		return true
	}
	if !f.Single && f.Phase == 0 && (f.Mode != 3 || f.FiendWarRetryCount > 0) {
		return true
	}
	intent := s.raidWeeklyIntents[f.InstanceID]
	if intent == nil || !intent.Committed || intent.Command.Mode != f.Weekly.Mode || intent.Command.Limit != f.Weekly.Limit || intent.Command.Policy != f.Weekly.Policy || !intent.Command.CompletedAt.Equal(f.Weekly.CompletedAt) || len(intent.Command.Members) != len(f.Weekly.Members) {
		return false
	}
	for i, member := range f.Weekly.Members {
		if intent.Command.Members[i] != member {
			return false
		}
	}
	return true
}

func freezeCurrentAntonAutomaticCard(f *currentAntonRewardFlow, recipient currentAntonRewardRecipient, kind byte, catalog *pvfDungeonDropCatalog, roll func(int64) (int64, error)) (*currentAntonRewardCard, raid.RewardCommand, error) {
	cards, typ := f.Party, "party_card"
	if kind == 3 {
		cards, typ = f.Squad, "squad_item"
	} else if kind != 1 {
		return nil, raid.RewardCommand{}, raid.ErrRewardIneligible
	}
	card := cards[recipient.CharacterID]
	if card == nil {
		position := byte(0)
		if kind == 1 {
			used := [4]bool{}
			for _, old := range cards {
				if old != nil && old.Group == recipient.Group && old.Card < 4 {
					used[old.Card] = true
				}
			}
			position = 4
			for i, taken := range used {
				if !taken {
					position = byte(i)
					break
				}
			}
			if position > 3 {
				return nil, raid.RewardCommand{}, raid.ErrRewardIneligible
			}
		} else {
			for i, owner := range f.Recipients {
				if owner.CharacterID == recipient.CharacterID {
					position = byte(i)
					break
				}
			}
		}
		var plan currentAntonRewardPlan
		var err error
		if f.Single && kind == 3 {
			switch f.Mode {
			case 1:
				plan, err = resolveCurrentSingleAntonSquadPlan(catalog, f.Rewards, f.Rank, roll, f.CompletedAt)
			case 2:
				plan, err = resolveCurrentSingleLukeSquadPlan(catalog, f.Rewards, f.Rank, roll, f.CompletedAt)
			default:
				return nil, raid.RewardCommand{}, raid.ErrRewardIneligible
			}
		} else {
			plan, err = resolveCurrentAntonRewardPlan(catalog, f.Rewards, typ, f.Rank, roll, f.CompletedAt)
		}
		if err != nil {
			return nil, raid.RewardCommand{}, err
		}
		plan, err = addCurrentAntonTeamTokenBonus(f, kind, catalog, plan, roll)
		if err != nil {
			return nil, raid.RewardCommand{}, err
		}
		card = &currentAntonRewardCard{Card: position, Group: recipient.Group, Plan: plan}
		cards[recipient.CharacterID] = card
	}
	command := raid.RewardCommand{AccountID: recipient.AccountID, CharacterID: strconv.Itoa(int(recipient.CharacterID)), InstanceID: f.InstanceID, Mode: f.Mode, Phase: f.Phase, Kind: kind, Card: card.Card, MinimumClears: f.Rewards.MinimumClears, OwnedClears: recipient.Clears, Fingerprint: card.Plan.Fingerprint, CompletedAt: f.CompletedAt}
	return card, command, nil
}

// Reward resolution uses the same immutable PVF drop catalog that startup
// already preloads for ordinary dungeon drops. Rebuilding that catalog on the
// card clock is observable: the status tick and a 660 request can otherwise
// spend several seconds rereading the three item lists before they reach the
// wire. Synthetic fixtures that do not install a monster catalog keep the
// existing on-demand construction path.
func (s *Service) currentAntonRewardDropCatalog() (*pvfDungeonDropCatalog, error) {
	if table, err := s.dungeonMonsterCatalog(); err == nil {
		if catalog, dropErr := table.DropCatalog(); dropErr == nil {
			return catalog, nil
		}
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	return newPVFDungeonDropCatalog(archive)
}

// No raid mutex is held during repository work. A lost response is replayed
// using the same frozen plan/receipt; changing membership cannot reroll it.
func (s *Service) advanceCurrentAntonRewardFlows(now time.Time, roll func(int64) (int64, error)) ([]runtimeRaidState, []error) {
	if now.IsZero() {
		return nil, nil
	}
	catalog, err := s.currentAntonRewardDropCatalog()
	if err != nil {
		return nil, []error{err}
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, []error{raid.ErrRewardOwnerUnavailable}
	}
	s.raidMu.Lock()
	keys := make([]string, 0, len(s.antonRewardFlows))
	for key := range s.antonRewardFlows {
		keys = append(keys, key)
	}
	s.raidMu.Unlock()
	sort.Strings(keys)
	var updated []runtimeRaidState
	var errors []error
	for _, key := range keys {
		s.raidMu.Lock()
		f := s.antonRewardFlows[key]
		if f == nil || !f.FinishedAt.IsZero() || now.Before(f.NextAttempt) || !s.currentAntonFlowWeeklyCommittedLocked(f) {
			s.raidMu.Unlock()
			continue
		}
		owners := append([]currentAntonRewardRecipient(nil), f.Recipients...)
		movie := currentAntonMovieWindow(f)
		s.raidMu.Unlock()
		if f.EntryRefund != nil {
			if err := s.advanceCurrentRaidEntryRefundFlow(f, catalog, repos, now); err != nil {
				errors = append(errors, err)
			}
			continue
		}
		if f.FiendWarRetryCount > 0 {
			if err := s.advanceCurrentFiendWarRetryFlow(f, catalog, repos, now); err != nil {
				errors = append(errors, err)
			}
			continue
		}
		// Failures and native choice clocks belong to the recipient. A slow
		// movie, absent connection or failed grant must not hold another page.
		for _, owner := range owners {
			s.raidMu.Lock()
			deferred := now.Before(f.OwnerNextAttempt[owner.CharacterID])
			s.raidMu.Unlock()
			if deferred {
				continue
			}
			if privateActivityFinalRaid(f) && owner.Clears >= f.Rewards.MinimumClears {
				ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
				err := s.persistPrivateActivity(ctx, owner.AccountID, strconv.Itoa(int(owner.CharacterID)), 4, now, f.CompletedAt)
				cancel()
				if err != nil {
					errors = append(errors, err)
					s.raidMu.Lock()
					deferCurrentRaidRewardOwner(f, owner.CharacterID, now)
					s.raidMu.Unlock()
					continue
				}
			}
			for _, kind := range []byte{1, 3} {
				s.raidMu.Lock()
				if kind == 3 && currentRaidOwnerSquadReady(f, owner.CharacterID, now) {
					if f.TeamDisplayReady == nil {
						f.TeamDisplayReady = make(map[uint16]bool)
					}
					f.TeamDisplayReady[owner.CharacterID] = true
				}
				if kind == 1 && now.Before(currentRaidOwnerPartySelectAt(f, owner.CharacterID)) ||
					kind == 3 && (!f.squadRequired() || !currentRaidTeamRewardGrantReady(f, now)) {
					s.raidMu.Unlock()
					continue
				}
				card, command, prepareErr := freezeCurrentAntonAutomaticCard(f, owner, kind, catalog, roll)
				if prepareErr != nil {
					deferCurrentRaidRewardOwner(f, owner.CharacterID, now)
					s.raidMu.Unlock()
					errors = append(errors, prepareErr)
					break
				}
				if card.Committed {
					s.raidMu.Unlock()
					continue
				}
				plan := cloneCurrentAntonRewardPlan(card.Plan)
				s.raidMu.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
				granted, grantErr := grantCurrentAntonRewardPlan(ctx, repos, catalog, plan, command)
				cancel()
				s.raidMu.Lock()
				if grantErr == nil {
					markCurrentRaidCardCommitted(card, now, false)
					card.Plan.FateGranted = granted.Plan.FateGranted
				} else {
					deferCurrentRaidRewardOwner(f, owner.CharacterID, now)
				}
				s.raidMu.Unlock()
				if grantErr != nil {
					errors = append(errors, grantErr)
					break
				}
			}
		}
		s.raidMu.Lock()
		markCurrentAntonPartyCommittedLocked(f, now)
		all := true
		for _, owner := range owners {
			p, q := f.Party[owner.CharacterID], f.Squad[owner.CharacterID]
			if p == nil || !p.Committed || f.squadRequired() && (q == nil || !q.Committed || !currentRaidOwnerSquadReady(f, owner.CharacterID, now)) {
				all = false
			}
		}
		if all && f.SquadShownAt.IsZero() {
			f.SquadShownAt = now
		}
		// Luke phase zero has no squad display. Its already-settled party
		// rewards can enter the break immediately; other modes still retain
		// the full squad movie window after all asset commits.
		if all && (!f.squadRequired() || !now.Before(f.SquadShownAt.Add(movie))) {
			f.FinishedAt = now
			if f.Mode != 3 && !f.Single && f.Phase == 0 {
				f.BreakUntil = now.Add(time.Duration(f.Configuration.PhaseBreakSeconds) * time.Second)
			}
		}
		if live := s.raids[f.RaidKey]; live != nil && live.Battle != nil && live.Battle.InstanceID == f.InstanceID && live.Battle.Phase == f.Phase {
			updated = append(updated, cloneRuntimeRaidState(*live))
		}
		s.raidMu.Unlock()
	}
	return updated, errors
}

func (s *Service) queueCurrentAntonRewardFlows() {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	s.raidMu.Lock()
	if s.antonRewardWorkRunning || len(s.antonRewardFlows) == 0 {
		s.raidMu.Unlock()
		return
	}
	s.antonRewardWorkRunning = true
	s.raidMu.Unlock()
	go func() {
		defer func() { s.raidMu.Lock(); s.antonRewardWorkRunning = false; s.raidMu.Unlock() }()
		states, errors := s.advanceCurrentAntonRewardFlows(time.Now(), secureCurrentBoosterRoll)
		for _, err := range errors {
			s.logWarn("Anton reward flow deferred", "error", err)
		}
		for _, state := range states {
			s.queueCurrentRaidDungeonProgress(state)
		}
	}()
}
