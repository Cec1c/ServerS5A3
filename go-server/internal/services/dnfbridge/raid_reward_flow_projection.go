package dnfbridge

import (
	"crypto/sha256"
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Restore only committed rewards and the current phase-break deadline. The
// owned result must precede this screen; reconnects never run a reward grant.
func (s *Service) sendCurrentAntonRewardFlowProjection(session *gameSession, captured runtimeRaidState) error {
	if captured.Battle == nil || captured.Battle.Phase > 1 || captured.Battle.RewardFlow[captured.Battle.Phase] == nil {
		return nil
	}
	if !s.currentRaidAuthenticated(session) {
		return raid.ErrRewardOwnership
	}
	o := &session.raidProjection
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failed {
		return fmt.Errorf("Anton flow projection transport previously failed")
	}
	state, err := s.latestCurrentRaidProjectionState(session, captured)
	if err != nil {
		return err
	}
	b := state.Battle
	f := b.RewardFlow[b.Phase]
	if f == nil {
		return nil
	}
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	if !o.resultSent || o.resultIdentity != identity {
		return nil
	}
	var owner *currentAntonRewardRecipient
	for i := range f.Recipients {
		if f.Recipients[i].CharacterID == session.selectedCharacterID {
			owner = &f.Recipients[i]
			break
		}
	}
	var packets []currentAntonRewardPacket
	lukePartyOnly := b.Mode == 2 && b.Phase == 0 && !f.Single
	lukeFinal := b.Mode == 2 && b.Phase == 1 && !f.Single
	fiendWar := b.Mode == 3
	if !f.SquadShownAt.IsZero() && f.squadRequired() {
		var cards []raid.RewardDisplayCard
		for _, recipient := range f.Recipients {
			card := f.Squad[recipient.CharacterID]
			if card == nil || !card.Committed {
				s.logCurrentRaidRewardWire(session, currentAntonRewardPacket{opcode: 600}, b.InstanceID, b.Mode, b.Phase, false, "squad_card_uncommitted")
				return raid.ErrRewardReceipt
			}
			cards = append(cards, raid.RewardDisplayCard{CharacterID: recipient.CharacterID, CardType: 1, Items: currentAntonPartyDisplayItems(card.Plan)})
		}
		body, err := raid.BuildRewardListBody(3, cards)
		if err != nil {
			return err
		}
		packets = append(packets, currentAntonRewardPacket{opcode: 587, body: []byte{4, 0}})
		if (fiendWar || lukeFinal) && owner != nil {
			// Fiend War and final Luke can commit both pools in one advance.
			// Native kind3 populates category2 only; it cannot reveal the
			// committed party items in category0. Restore both displays from
			// their committed plans, without repeating either asset grant.
			visible := s.currentRaidPartyRewardDisplayMembers(session, state)
			var partyCards []raid.RewardDisplayCard
			for _, recipient := range f.Recipients {
				if !visible[recipient.CharacterID] {
					continue
				}
				card := f.Party[recipient.CharacterID]
				if card == nil || !card.Committed {
					return raid.ErrRewardReceipt
				}
				partyCards = append(partyCards, raid.RewardDisplayCard{CharacterID: recipient.CharacterID, CardType: card.Card, Items: currentAntonPartyDisplayItems(card.Plan)})
			}
			partyBody, err := raid.BuildRewardListBody(1, partyCards)
			if err != nil {
				return err
			}
			packets = append(packets, currentAntonRewardPacket{opcode: 600, body: partyBody})
		}
		packets = append(packets, currentAntonRewardPacket{opcode: 600, body: body})
	} else if !time.Now().Before(f.AutoSelectAt) || owner != nil && b.RewardCardReady[b.Phase][owner.CharacterID] {
		packets = append(packets, currentAntonRewardPacket{opcode: 587, body: []byte{4, 0}})
		if owner != nil {
			var cards []raid.RewardDisplayCard
			visible := s.currentRaidPartyRewardDisplayMembers(session, state)
			for _, recipient := range f.Recipients {
				if !visible[recipient.CharacterID] {
					continue
				}
				card := raid.RewardDisplayCard{CharacterID: recipient.CharacterID, CardType: byte(len(cards) + 1)}
				if selected := f.Party[recipient.CharacterID]; selected != nil && selected.Committed {
					card.CardType = selected.Card
					card.Items = currentAntonPartyDisplayItems(selected.Plan)
				}
				cards = append(cards, card)
			}
			body, err := raid.BuildRewardListBody(1, cards)
			if err != nil {
				return err
			}
			packets = append(packets, currentAntonRewardPacket{opcode: 600, body: body})
			if lukePartyOnly && currentLukeLocalPartyDisplayCommitted(f, owner.CharacterID, visible) {
				// The native no-team decision belongs to this displayed party.
				// Another raid group choosing its own cards must not hold this
				// page until the unattended grant deadline. Phase progression
				// still requires every frozen recipient's committed reward.
				// The native no-team decision runs on category2 delivery,
				// after the local party reward has populated category0.
				// This is a UI completion notice, not a squad grant.
				emptyTeam, err := raid.BuildRewardListBody(3, nil)
				if err != nil {
					return err
				}
				packets = append(packets, currentAntonRewardPacket{opcode: 600, body: emptyTeam})
			}
		}
	}
	if !f.FinishedAt.IsZero() {
		if f.Mode != 3 && !f.Single && f.Phase == 0 {
			packets = append(packets, currentAntonRewardPacket{opcode: 587, body: []byte{5, 0}}, currentAntonRewardPacket{opcode: 596, body: raid.BuildTimerBody(0, 0, uint32(f.BreakUntil.Unix()), currentRaidNativeDuration(f.Mode, f.BreakUntil, f.Configuration.PhaseBreakSeconds, time.Now()))})
		}
		// Final/single completion keeps the committed reward screen above.
		// Re-entering state4 after NOTI600 restarts that same screen when the
		// first projection arrives after the display window has already ended.
	}
	// Compare the complete authoritative projection before removing an already
	// delivered card list. Otherwise the filtered second send has a different
	// signature and replays the same standby transition/inventory refresh.
	hash := sha256.New()
	for _, packet := range packets {
		hash.Write([]byte{byte(packet.opcode), byte(packet.opcode >> 8)})
		body := packet.body
		if b.Mode == 2 && packet.opcode == 596 && len(body) == 13 {
			// Luke starts this duration at receive time. The absolute end
			// identifies the clock; elapsed time alone is not a new transition.
			body = body[:9]
		}
		hash.Write(body)
	}
	var signature [32]byte
	copy(signature[:], hash.Sum(nil))
	if o.rewardFlowIdentity == identity && o.rewardFlowSignature == signature {
		return nil
	}
	// The authoritative card list and the later phase-break transition are
	// distinct deliveries. Finishing the display must not replay the same
	// list just because its break/timer packets changed the full signature.
	var displaySignature [32]byte
	var displayBodies [][]byte
	for _, packet := range packets {
		if packet.opcode == 600 {
			displayBodies = append(displayBodies, packet.body)
		}
	}
	if len(displayBodies) == 1 {
		// Manual party sends share this single-body cache contract.
		displaySignature = sha256.Sum256(displayBodies[0])
	} else if len(displayBodies) > 1 {
		displayHash := sha256.New()
		for _, body := range displayBodies {
			// Frame each body so the signature covers both categories,
			// including Luke's empty kind3 completion notice.
			fmt.Fprintf(displayHash, "%d:", len(body))
			displayHash.Write(body)
		}
		copy(displaySignature[:], displayHash.Sum(nil))
	}
	if displaySignature != [32]byte{} && o.rewardDisplayIdentity == identity && o.rewardDisplaySignature == displaySignature {
		filtered := packets[:0]
		for _, packet := range packets {
			if packet.opcode == 600 || packet.opcode == 587 && len(packet.body) == 2 && packet.body[0] == 4 && packet.body[1] == 0 {
				continue
			}
			filtered = append(filtered, packet)
		}
		packets = filtered
	} else if (lukePartyOnly || lukeFinal || fiendWar) && len(displayBodies) == 2 && o.rewardDisplayIdentity == identity && o.rewardDisplaySignature == sha256.Sum256(displayBodies[0]) {
		// A successful manual/earlier party projection already delivered
		// this exact kind1. Append only its missing team list; never
		// replay the flip. A new connection must receive both in order.
		filtered := packets[:0]
		for _, packet := range packets {
			if packet.opcode == 600 && len(packet.body) > 0 && packet.body[0] == 1 || packet.opcode == 587 && len(packet.body) == 2 && packet.body[0] == 4 && packet.body[1] == 0 {
				continue
			}
			filtered = append(filtered, packet)
		}
		packets = filtered
	}
	trackedScreen := lukePartyOnly || lukeFinal || fiendWar || b.Mode == 1 && (f.SquadShownAt.IsZero() || b.Phase == 0 && !f.Single)
	if trackedScreen && o.rewardScreenSent && o.rewardScreenIdentity == identity {
		// Normal Anton phase zero keeps the same open panel for kind3. Its
		// native consumer updates that panel; another state4 has unrelated
		// reset side effects. A fresh connection still needs state4 first.
		// Normal Luke also keeps its current panel when either pool arrives.
		// Final/single Anton and single Luke retain their existing behavior.
		filtered := packets[:0]
		for _, packet := range packets {
			if packet.opcode == 587 && len(packet.body) == 2 && packet.body[0] == 4 && packet.body[1] == 0 {
				continue
			}
			filtered = append(filtered, packet)
		}
		packets = filtered
	}
	if len(packets) == 0 {
		return nil
	}
	if !f.FinishedAt.IsZero() && f.Mode != 3 && !f.Single && f.Phase == 0 {
		remaining := int64(time.Until(f.BreakUntil).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		packets = append(packets, currentAntonRewardPacket{opcode: 597, body: raid.BuildRemainingTimeBody(1, uint32(remaining))})
	}
	if !f.FinishedAt.IsZero() {
		// A legitimate raid runtime binding is required for this notification.
		session.dungeon.mu.Lock()
		runtime := session.dungeon.runtime
		bound := runtime != nil && runtime.raidEntry != nil && runtime.raidEntry.InstanceID == b.InstanceID && runtime.raidEntry.Phase == b.Phase && dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID)
		session.dungeon.mu.Unlock()
		if bound {
			packets = append(packets, currentAntonRewardPacket{opcode: 115, body: []byte{0}})
		}
	}
	for _, packet := range packets {
		if err := s.sendGameUpperRawClassCodec(session, packet.opcode, packet.body, packet.class, true); err != nil {
			s.logCurrentRaidRewardWire(session, packet, b.InstanceID, b.Mode, b.Phase, false, "send_failed")
			o.failed = true
			if session.conn != nil {
				_ = session.conn.Close()
			}
			return err
		}
		s.logCurrentRaidRewardWire(session, packet, b.InstanceID, b.Mode, b.Phase, true, "")
	}
	for _, packet := range packets {
		if packet.opcode == 600 && len(packet.body) > 0 && packet.body[0] == 1 {
			s.raidMu.Lock()
			s.markCurrentAntonPartyShownLocked(b.InstanceID, b.Phase, time.Now())
			if owner != nil && f.SquadShownAt.IsZero() {
				s.markCurrentRaidPartySelectionShownLocked(b.InstanceID, b.Phase, owner.CharacterID, time.Now())
			}
			s.raidMu.Unlock()
			break
		}
	}
	for _, packet := range packets {
		if packet.opcode == 600 && len(packet.body) > 0 && packet.body[0] == 1 {
			s.raidMu.Lock()
			s.markCurrentAntonPartyShownLocked(b.InstanceID, b.Phase, time.Now())
			s.raidMu.Unlock()
			break
		}
	}
	if err := s.sendSelectedCurrentContainerListsRefresh(session, "raid_owned_reward_flow_after_commit"); err != nil {
		o.failed = true
		if session.conn != nil {
			_ = session.conn.Close()
		}
		return err
	}
	if err := s.refreshCurrentRaidFateSnapshot(session); err != nil {
		return err
	}
	for _, card := range []*currentAntonRewardCard{f.Party[session.selectedCharacterID], f.Squad[session.selectedCharacterID]} {
		if card != nil && card.Committed && !currentRaidRewardVouchers(card.Plan).IsZero() {
			if err := s.sendCurrentAccountWallet(session); err != nil {
				o.failed = true
				if session.conn != nil {
					_ = session.conn.Close()
				}
				return err
			}
			break
		}
	}
	if trackedScreen {
		for _, packet := range packets {
			if packet.opcode == 587 && len(packet.body) == 2 && packet.body[0] == 4 && packet.body[1] == 0 {
				o.rewardScreenIdentity, o.rewardScreenSent = identity, true
			}
		}
	}
	o.rewardFlowIdentity, o.rewardFlowSignature = identity, signature
	if displaySignature != [32]byte{} {
		o.rewardDisplayIdentity, o.rewardDisplaySignature = identity, displaySignature
	}
	return nil
}
