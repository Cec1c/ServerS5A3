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
	PartyCommittedAt   time.Time
	SquadShownAt       time.Time
	FinishedAt         time.Time
	BreakUntil         time.Time
	NextAttempt        time.Time
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
	return &v
}

func currentAntonFlowKey(instance string, phase byte) string {
	return instance + ":" + strconv.Itoa(int(phase))
}

// Normal Anton phase zero has two display stages. Automatic selection must
// leave the committed party cards visible for one native movie window before
// the squad list replaces them. Manual party selections preserve their own
// completion time, so a fully manual group is not delayed a second time.
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

// The phase-clear timestamp starts the server clock, but a client 659 need
// not arrive at that instant. Automatic party selection must leave
// a full movie window after the card list was actually delivered. If no client
// reached the card screen, the base deadline still opens that screen and one
// movie window is left before the unattended fallback commits it.
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
	if shown := f.PartyShownAt.Add(movie); shown.After(at) {
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
		card = &currentAntonRewardCard{Card: position, Group: recipient.Group, Plan: plan}
		cards[recipient.CharacterID] = card
	}
	command := raid.RewardCommand{AccountID: recipient.AccountID, CharacterID: strconv.Itoa(int(recipient.CharacterID)), InstanceID: f.InstanceID, Mode: f.Mode, Phase: f.Phase, Kind: kind, Card: card.Card, MinimumClears: f.Rewards.MinimumClears, OwnedClears: recipient.Clears, Fingerprint: card.Plan.Fingerprint, CompletedAt: f.CompletedAt}
	return card, command, nil
}

// No raid mutex is held during repository work. A lost response is replayed
// using the same frozen plan/receipt; changing membership cannot reroll it.
func (s *Service) advanceCurrentAntonRewardFlows(now time.Time, roll func(int64) (int64, error)) ([]runtimeRaidState, []error) {
	if now.IsZero() {
		return nil, nil
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, []error{err}
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
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
		if f.FiendWarRetryCount > 0 {
			if err := s.advanceCurrentFiendWarRetryFlow(f, catalog, repos, now); err != nil {
				errors = append(errors, err)
			}
			continue
		}
		failed := false
		for _, kind := range []byte{1, 3} {
			if kind == 3 && !f.squadRequired() {
				continue
			}
			if kind == 3 {
				s.raidMu.Lock()
				if markCurrentAntonPartyCommittedLocked(f, now) && f.Mode == 1 && !f.Single && f.Phase == 0 && now.Before(f.PartyCommittedAt.Add(movie)) {
					s.raidMu.Unlock()
					continue
				}
				s.raidMu.Unlock()
			}
			for _, owner := range owners {
				s.raidMu.Lock()
				if kind == 1 {
					selectAt := f.AutoSelectAt
					if f.Mode == 1 && f.Phase == 0 && !f.Single {
						selectAt = currentAntonPartySelectAt(f, movie)
					}
					if now.Before(selectAt) {
						s.raidMu.Unlock()
						continue
					}
				}
				if kind == 3 {
					allParty := true
					for _, peer := range owners {
						card := f.Party[peer.CharacterID]
						if card == nil || !card.Committed {
							allParty = false
							break
						}
					}
					if !allParty {
						s.raidMu.Unlock()
						continue
					}
				}
				card, command, prepareErr := freezeCurrentAntonAutomaticCard(f, owner, kind, catalog, roll)
				if prepareErr != nil {
					s.raidMu.Unlock()
					errors = append(errors, prepareErr)
					failed = true
					break
				}
				if card.Committed {
					s.raidMu.Unlock()
					continue
				}
				plan := cloneCurrentAntonRewardPlan(card.Plan)
				s.raidMu.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
				_, grantErr := grantCurrentAntonRewardPlan(ctx, repos, catalog, plan, command)
				cancel()
				s.raidMu.Lock()
				if grantErr == nil {
					card.Committed = true
				} else {
					f.NextAttempt = now.Add(2 * time.Second)
				}
				s.raidMu.Unlock()
				if grantErr != nil {
					errors = append(errors, grantErr)
					failed = true
					break
				}
			}
			if failed {
				break
			}
		}
		s.raidMu.Lock()
		all := !failed
		for _, owner := range owners {
			p, q := f.Party[owner.CharacterID], f.Squad[owner.CharacterID]
			if p == nil || !p.Committed || f.squadRequired() && (q == nil || !q.Committed) {
				all = false
			}
		}
		if all && f.SquadShownAt.IsZero() {
			f.SquadShownAt = now
		}
		// Keep the full squad display available for one actual movie window.
		if all && !now.Before(f.SquadShownAt.Add(movie)) {
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
