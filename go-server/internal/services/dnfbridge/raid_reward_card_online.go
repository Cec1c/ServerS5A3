package dnfbridge

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

type currentAntonRewardCard struct {
	Card      byte
	Group     byte
	Plan      currentAntonRewardPlan
	Committed bool
}

type currentAntonRewardPacket struct {
	opcode uint16
	class  byte
	body   []byte
}

func (s *Service) sendCurrentAntonRewardPackets(session *gameSession, state runtimeRaidState, packets []currentAntonRewardPacket, markScreen bool) error {
	if !s.currentRaidAuthenticated(session) {
		return raid.ErrRewardOwnership
	}
	o := &session.raidProjection
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failed {
		return fmt.Errorf("Anton reward transport previously failed")
	}
	latest, err := s.latestCurrentRaidProjectionState(session, state)
	if err != nil {
		return err
	}
	if f := latest.Battle.RewardFlow[latest.Battle.Phase]; f != nil && !f.SquadShownAt.IsZero() {
		// A delayed click/movie packet must not reopen party masks over the
		// already committed squad/break screen. Preserve only its command ACK.
		filtered := packets[:0]
		for _, packet := range packets {
			if packet.class == 1 {
				filtered = append(filtered, packet)
			}
		}
		packets = filtered
		markScreen = false
	}
	identity := currentRaidProjectionIdentity{InstanceID: state.Battle.InstanceID, Phase: state.Battle.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	if markScreen && o.rewardScreenSent && o.rewardScreenIdentity == identity {
		packets = packets[:1]
	}
	for _, packet := range packets {
		if err := s.sendGameUpperRawClassCodec(session, packet.opcode, packet.body, packet.class, true); err != nil {
			o.failed = true
			if session.conn != nil {
				_ = session.conn.Close()
			}
			return err
		}
	}
	if markScreen {
		o.rewardScreenIdentity, o.rewardScreenSent = identity, true
	}
	if latest.Battle.Mode == 2 && latest.Battle.Phase == 0 && !latest.Battle.Single {
		// Share successful manual party displays with the flow projection.
		// Gold masks must not replace the party display signature.
		for _, packet := range packets {
			if packet.class == 0 && packet.opcode == 600 && len(packet.body) > 0 && packet.body[0] == 1 {
				o.rewardDisplayIdentity = identity
				o.rewardDisplaySignature = sha256.Sum256(packet.body)
			}
		}
	}
	return nil
}

// A client cannot initiate reward selection until this connection received
// the authoritative result. Reconnection must restore that result first.
func currentAntonResultDelivered(session *gameSession, state runtimeRaidState) bool {
	if session == nil || state.Battle == nil {
		return false
	}
	o := &session.raidProjection
	o.mu.Lock()
	defer o.mu.Unlock()
	identity := currentRaidProjectionIdentity{InstanceID: state.Battle.InstanceID, Phase: state.Battle.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	return !o.failed && o.resultSent && o.resultIdentity == identity
}

func (s *Service) currentAntonRewardState(session *gameSession) (runtimeRaidState, error) {
	if !s.currentRaidAuthenticated(session) {
		return runtimeRaidState{}, raid.ErrRewardOwnership
	}
	s.raidMu.Lock()
	live := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	var state runtimeRaidState
	if live != nil {
		state = cloneRuntimeRaidState(*live)
	}
	committed := state.Battle != nil && ((!state.Battle.Single && state.Battle.Phase == 0) || s.currentAntonFinalWeeklyCommittedLocked(state))
	s.raidMu.Unlock()
	b := state.Battle
	if b == nil || !committed || b.ChannelID != session.channel.ID || b.ChannelType != session.channel.Type || b.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) || !currentAntonResultDelivered(session, state) {
		return runtimeRaidState{}, raid.ErrRewardOwnership
	}
	result, err := prepareCurrentAntonResultProjection(state, 0)
	if err != nil || result.Failed {
		return runtimeRaidState{}, raid.ErrRewardIneligible
	}
	return state, nil
}

func (s *Service) handleCurrentAntonMovieCompletion(session *gameSession, op uint16, body []byte) error {
	if _, err := raid.DecodeMovieSkipRequest(body); err != nil {
		return s.sendRaidCommandFailure(session, op)
	}
	state, err := s.currentAntonRewardState(session)
	if err != nil {
		return s.sendRaidCommandFailure(session, op)
	}
	s.raidMu.Lock()
	live := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	valid := live != nil && live.Battle != nil && live.Battle.InstanceID == state.Battle.InstanceID && live.Battle.Phase == state.Battle.Phase
	if valid {
		if live.Battle.RewardCardReady[state.Battle.Phase] == nil {
			live.Battle.RewardCardReady[state.Battle.Phase] = map[uint16]bool{}
		}
		live.Battle.RewardCardReady[state.Battle.Phase][session.selectedCharacterID] = true
	}
	s.raidMu.Unlock()
	if !valid {
		return s.sendRaidCommandFailure(session, op)
	}
	if flow := state.Battle.RewardFlow[state.Battle.Phase]; flow != nil && !flow.SquadShownAt.IsZero() {
		if err := s.sendCurrentAntonRewardPackets(session, state, []currentAntonRewardPacket{{opcode: op, class: 1, body: []byte{1}}}, false); err != nil {
			return err
		}
		return s.sendCurrentAntonRewardFlowProjection(session, state)
	}
	// Successful reward state4 has stateArgument0 in either phase. Argument1
	// is reserved for failure and must not be derived from the phase index.
	packets := []currentAntonRewardPacket{{opcode: op, class: 1, body: []byte{1}}, {opcode: 587, class: 0, body: []byte{4, 0}}}
	if currentAntonPhaseClearCount(state.Battle, session.selectedCharacterID) < state.Battle.AntonRewards.Phases[state.Battle.Phase].MinimumClears {
		return s.sendCurrentAntonRewardPackets(session, state, packets, true)
	}
	member := state.Members[runtimeRaidMemberIndex(state, session.selectedCharacterID)]
	peers := state.Members
	if flow := state.Battle.RewardFlow[state.Battle.Phase]; flow != nil {
		peers = nil
		found := false
		for _, owner := range flow.Recipients {
			if owner.CharacterID == member.CharID {
				member.GroupIndex, found = owner.Group, true
			}
			peers = append(peers, runtimeRaidMemberState{CharID: owner.CharacterID, GroupIndex: owner.Group})
		}
		if !found {
			return s.sendRaidCommandFailure(session, op)
		}
	}
	var cards []raid.RewardDisplayCard
	for _, peer := range peers {
		if peer.GroupIndex == member.GroupIndex && currentAntonPhaseClearCount(state.Battle, peer.CharID) >= state.Battle.AntonRewards.Phases[state.Battle.Phase].MinimumClears {
			card := raid.RewardDisplayCard{CharacterID: peer.CharID, CardType: byte(len(cards) + 1)}
			if selected := state.Battle.RewardCards[state.Battle.Phase][peer.CharID]; selected != nil && selected.Committed {
				card.CardType = selected.Card
				card.Items = currentAntonPartyDisplayItems(selected.Plan)
			}
			cards = append(cards, card)
		}
	}
	if len(cards) == 0 || len(cards) > 4 {
		return fmt.Errorf("Anton reward party exceeds native four-card layout")
	}
	for _, kind := range []byte{1, 0} {
		projection := cards
		if kind == 0 {
			projection = append([]raid.RewardDisplayCard(nil), cards...)
			for i := range projection {
				projection[i].Items = nil
			}
		}
		body, err := raid.BuildRewardListBody(kind, projection)
		if err != nil {
			return err
		}
		packets = append(packets, currentAntonRewardPacket{opcode: 600, body: body})
	}
	return s.sendCurrentAntonRewardPackets(session, state, packets, true)
}

// All actual normal party_card choices have flag0. Ordinary descriptors use
// native mode0; the helper resolves item class/name/icon from the actual PVF.
func currentAntonPartyDisplayItems(plan currentAntonRewardPlan) []raid.RewardDisplayItem {
	var items []raid.RewardDisplayItem
	for _, reward := range plan.Rewards {
		items = append(items, raid.RewardDisplayItem{ObjectKind: 0, ItemID: reward.Definition.ItemID, Quantity: uint16(reward.Count), Flags: plan.Choice.Flags})
	}
	return items
}

// Holds only raidMu while reserving the immutable card/plan; no DB/transport
// runs here. The existing domain asset owner serializes concurrent claims.
func (s *Service) freezeCurrentAntonPartyCard(session *gameSession, captured runtimeRaidState, requested byte, catalog *pvfDungeonDropCatalog, roll func(int64) (int64, error)) (currentAntonRewardCard, raid.RewardCommand, error) {
	if captured.Battle == nil || requested > 3 || !s.currentRaidAuthenticated(session) || !currentAntonResultDelivered(session, captured) {
		return currentAntonRewardCard{}, raid.RewardCommand{}, raid.ErrRewardOwnership
	}
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	live := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if live == nil || live.Battle == nil || live.Battle.InstanceID != captured.Battle.InstanceID || live.Battle.Phase != captured.Battle.Phase || live.Battle.MemberAccounts[session.selectedCharacterID] != s.accountIDForSession(session) {
		return currentAntonRewardCard{}, raid.RewardCommand{}, raid.ErrRewardOwnership
	}
	b := live.Battle
	if b.Phase > 1 || !b.RewardCardReady[b.Phase][session.selectedCharacterID] || requested > 3 {
		return currentAntonRewardCard{}, raid.RewardCommand{}, raid.ErrRewardIneligible
	}
	result, err := prepareCurrentAntonResultProjection(*live, 0)
	if err != nil || result.Failed || (result.FinalWeeklyRequired && !s.currentAntonFinalWeeklyCommittedLocked(*live)) {
		return currentAntonRewardCard{}, raid.RewardCommand{}, raid.ErrRewardIneligible
	}
	member := live.Members[runtimeRaidMemberIndex(*live, session.selectedCharacterID)]
	if flow := b.RewardFlow[b.Phase]; flow != nil {
		found := false
		for _, owner := range flow.Recipients {
			if owner.CharacterID == member.CharID {
				member.GroupIndex, found = owner.Group, true
				break
			}
		}
		if !found {
			return currentAntonRewardCard{}, raid.RewardCommand{}, raid.ErrRewardIneligible
		}
	}
	if member.GroupIndex < 1 || member.GroupIndex > runtimeRaidMaxGroups {
		return currentAntonRewardCard{}, raid.RewardCommand{}, raid.ErrRewardIneligible
	}
	clears := currentAntonPhaseClearCount(b, member.CharID)
	minimum := b.AntonRewards.Phases[b.Phase].MinimumClears
	if clears < minimum {
		return currentAntonRewardCard{}, raid.RewardCommand{}, raid.ErrRewardIneligible
	}
	if b.RewardCards[b.Phase] == nil {
		b.RewardCards[b.Phase] = map[uint16]*currentAntonRewardCard{}
	}
	card := b.RewardCards[b.Phase][member.CharID]
	if card == nil {
		used := [4]bool{}
		for _, old := range b.RewardCards[b.Phase] {
			if old != nil && old.Group == member.GroupIndex && old.Card < 4 {
				used[old.Card] = true
			}
		}
		chosen := requested
		if used[chosen] {
			chosen = 4
			for i, taken := range used {
				if !taken {
					chosen = byte(i)
					break
				}
			}
		}
		if chosen > 3 {
			return currentAntonRewardCard{}, raid.RewardCommand{}, raid.ErrRewardIneligible
		}
		plan, err := resolveCurrentAntonRewardPlan(catalog, b.AntonRewards.Phases[b.Phase], "party_card", result.DomainRank, roll, b.PhaseCompletedAt)
		if err != nil {
			return currentAntonRewardCard{}, raid.RewardCommand{}, err
		}
		if plan.Choice.Flags != 0 {
			return currentAntonRewardCard{}, raid.RewardCommand{}, fmt.Errorf("unsupported nonordinary party-card descriptor")
		}
		card = &currentAntonRewardCard{Card: chosen, Group: member.GroupIndex, Plan: plan}
		b.RewardCards[b.Phase][member.CharID] = card
	}
	copy := *card
	copy.Plan = cloneCurrentAntonRewardPlan(card.Plan)
	command := raid.RewardCommand{AccountID: b.MemberAccounts[member.CharID], CharacterID: strconv.Itoa(int(member.CharID)), InstanceID: b.InstanceID, Mode: b.Mode, Phase: b.Phase, Kind: 1, Card: card.Card, MinimumClears: minimum, OwnedClears: clears, Fingerprint: card.Plan.Fingerprint, CompletedAt: b.PhaseCompletedAt}
	return copy, command, nil
}

func (s *Service) handleCurrentAntonRewardCard(session *gameSession, op uint16, body []byte) error {
	req, err := raid.DecodeRewardCardRequest(body)
	if err != nil {
		return s.sendRaidCommandFailure(session, op)
	}
	state, err := s.currentAntonRewardState(session)
	if err != nil {
		return s.sendRaidCommandFailure(session, op)
	}
	// No server PC-room membership is advertised/owned by this deployment.
	// The client flag alone cannot qualify an additional account asset grant.
	if req.Kind == 2 {
		return s.sendRaidCommandFailure(session, op)
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return s.sendRaidCommandFailure(session, op)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		return s.sendRaidCommandFailure(session, op)
	}
	if req.Kind == 0 {
		s.raidMu.Lock()
		live := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
		var selected *currentAntonRewardCard
		if live != nil && live.Battle != nil && live.Battle.InstanceID == state.Battle.InstanceID && live.Battle.Phase == state.Battle.Phase {
			old := live.Battle.RewardCards[state.Battle.Phase][session.selectedCharacterID]
			if old != nil && old.Committed {
				copy := *old
				selected = &copy
			}
		}
		s.raidMu.Unlock()
		if selected == nil {
			return s.sendRaidCommandFailure(session, op)
		}
		// Actual normal PVF has no gold pool. Complete the client's gold card
		// with no item descriptors; never substitute an invented currency/item.
		display, err := raid.BuildRewardListBody(0, []raid.RewardDisplayCard{{CharacterID: session.selectedCharacterID, CardType: selected.Card}})
		if err != nil {
			return err
		}
		return s.sendCurrentAntonRewardPackets(session, state, []currentAntonRewardPacket{{opcode: 600, body: display}, {opcode: op, class: 1, body: []byte{1}}}, false)
	}
	card, command, err := s.freezeCurrentAntonPartyCard(session, state, req.Card, catalog, secureCurrentBoosterRoll)
	if err != nil {
		return s.sendRaidCommandFailure(session, op)
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return s.sendRaidCommandFailure(session, op)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	result, err := grantCurrentAntonRewardPlan(ctx, repositories, catalog, card.Plan, command)
	cancel()
	if err != nil {
		s.logGameEvent(session, "game-raid-party-card-grant-deferred", "error", err)
		return s.sendRaidCommandFailure(session, op)
	}
	s.raidMu.Lock()
	live := s.runtimeRaidByMemberLocked(session.selectedCharacterID)
	if live != nil && live.Battle != nil && live.Battle.InstanceID == command.InstanceID && live.Battle.Phase == command.Phase {
		selected := live.Battle.RewardCards[command.Phase][session.selectedCharacterID]
		if selected != nil && selected.Plan.Fingerprint == command.Fingerprint && selected.Card == command.Card {
			selected.Committed = true
		}
	}
	s.raidMu.Unlock()
	items := currentAntonPartyDisplayItems(card.Plan)
	display, err := raid.BuildRewardListBody(1, []raid.RewardDisplayCard{{CharacterID: session.selectedCharacterID, CardType: card.Card, Items: items}})
	if err != nil {
		return err
	}
	if err := s.sendCurrentAntonRewardPackets(session, state, []currentAntonRewardPacket{{opcode: 600, body: display}, {opcode: op, class: 1, body: []byte{1}}}, false); err != nil {
		return err
	}
	// Refresh from persisted assets, also on a replay after a prior lost write.
	if err := s.sendSelectedCurrentContainerListsRefresh(session, "raid_party_card_after_commit"); err != nil {
		return err
	}
	s.queueCurrentAntonPartyCardReveal(session, state, card, display)
	s.logGameEvent(session, "game-raid-party-card-committed", "raid_key", state.RaidKey, "phase", command.Phase, "card", card.Card, "replayed", result.Replayed, "at", time.Now().Unix())
	return nil
}

// Other recipients are queued independently of the caller's input loop, so
// simultaneous selections in two sessions cannot wait on each other.
func (s *Service) queueCurrentAntonPartyCardReveal(sender *gameSession, state runtimeRaidState, card currentAntonRewardCard, body []byte) {
	for _, member := range state.Members {
		if member.CharID == sender.selectedCharacterID || member.GroupIndex != card.Group {
			continue
		}
		peer, found := s.onlineGameSession(member.CharID)
		if !found || !currentAntonResultDelivered(peer, state) {
			continue
		}
		generation, id := peer.characterGeneration, member.CharID
		projection := append([]byte(nil), body...)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			err := s.callGameSession(ctx, peer, "raid-owned-party-card-reveal", func() error {
				if peer.characterGeneration != generation || peer.selectedCharacterID != id || !currentAntonResultDelivered(peer, state) {
					return nil
				}
				return s.sendCurrentAntonRewardPackets(peer, state, []currentAntonRewardPacket{{opcode: 600, body: projection}}, false)
			})
			if err != nil {
				s.logGameEvent(peer, "game-raid-party-card-peer-projection-failed", "raid_key", state.RaidKey, "error", err)
			}
		}()
	}
}
