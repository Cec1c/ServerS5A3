package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	oe "longheng.io/server/internal/modules/dnf/onlineevent"
)

// Separate operator opt-in. A nil catalog and the new control's default both
// leave the route OFF. Client permits654/689 require their own release review.
func (s *Service) currentSemesterCumulativeEnabled() bool {
	return s != nil && s.currentEpicBenefitsEnabled() && s.activityControls != nil && s.activityControls.Enabled(216)
}

func (s *Service) sendCurrentSemesterCumulative(session *gameSession, identity boundGameSessionCharacter) error {
	if !s.currentSemesterCumulativeEnabled() || session == nil || identity.session != session || !session.loginPacketSeen || !session.accountFromToken || !s.boundGameSessionCharacterCurrent(identity) {
		return oe.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, err := s.currentSemesterCumulativeCatalog(ctx)
	if err != nil || catalog == nil {
		if err != nil {
			return err
		}
		return oe.ErrOwnerUnavailable
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Character == nil || g.Settings == nil {
		return oe.ErrOwnerUnavailable
	}
	rawAccount := strings.TrimSpace(session.accountID)
	account := s.accountIDForSession(session)
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, rawAccount)
	if err != nil {
		return err
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	// This is a read of an existing campaign, never a first-claim activation.
	campaign, found, err := oe.LoadEpicCampaign(ctx, g.Settings)
	if err != nil {
		return err
	}
	if !found {
		return oe.ErrOwnerUnavailable
	}
	now := s.gameplayNow()
	characterID := strconv.Itoa(int(identity.character))
	ch, found, err := g.Character.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || ch.CharacterID != characterID || ch.AccountID != account {
		return oe.ErrOwnerUnavailable
	}
	state, err := oe.SemesterCharacterSnapshot(ch, campaign, now)
	if err != nil {
		return err
	}
	body, err := oe.BuildSemesterNotification(state)
	if err != nil {
		return err
	}
	// Binding mutations must not wait behind a slow socket. The immutable
	// identity is checked again after releasing the read reservation.
	s.releaseCurrentCharacterAssets(reservation)
	if ctx.Err() != nil || !s.currentSemesterCumulativeEnabled() || !s.boundGameSessionCharacterCurrent(identity) {
		return oe.ErrOwnerUnavailable
	}
	return s.sendGameUpperRawClass(session, 654, body, 0)
}

// Only the real class1 request's event216 prefix is owned here. OFF and other
// events keep their existing dispatcher behavior. Bad framing has no ACK.
func (s *Service) handleCurrentSemesterCumulativeRequest(session *gameSession, body []byte) (bool, error) {
	if !s.currentSemesterCumulativeEnabled() || len(body) < 4 || binary.LittleEndian.Uint32(body) != 216 {
		return false, nil
	}
	if len(body) != 8 {
		return true, nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || session == nil || !session.loginPacketSeen || !session.accountFromToken {
		return true, nil
	}
	index := binary.LittleEndian.Uint32(body[4:])
	state, err := s.claimCurrentSemesterCumulativeForIdentity(session, identity, index)
	if err == nil {
		return true, s.publishCurrentSemesterCumulative(session, identity, state)
	}
	s.logGameEvent(session, "game-semester216-claim-deferred", "error", err)
	if !s.boundGameSessionCharacterCurrent(identity) {
		return true, nil
	}
	// The actual failure body has two opaque bytes; no domain error-code mapping
	// has been proven. A commit error may have committed: reread durable state.
	if err = s.sendGameUpperRawClass(session, 689, buildCurrentSemesterCumulativeACK(false), 1); err != nil {
		return true, err
	}
	if refreshErr := s.sendCurrentSemesterCumulative(session, identity); refreshErr != nil {
		s.logGameEvent(session, "game-semester216-refresh-deferred", "error", refreshErr)
		return true, nil
	}
	if s.boundGameSessionCharacterCurrent(identity) {
		return true, s.sendMailboxAlarmForSession(session, identity.character, "semester216-reconcile")
	}
	return true, nil
}

// Native panel opens consume the cached event53 model locally. No server panel
// open request has been established. Reuse the character event queue to refresh
// successful clear journals while that panel is open, without guessing an opcode.
func (s *Service) armCurrentSemesterCumulativeTick(session *gameSession) error {
	if !s.currentSemesterCumulativeEnabled() || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	charID, generation, err := gameSessionCharacterEventIdentity(session, session.selectedCharacterID)
	if err != nil {
		return err
	}
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return errGameplayTimeQueueUnavailable
	}
	return queue.ScheduleAfter(fmt.Sprintf("dnf-semester216:%s", session.connID), 30*time.Second, func(time.Time) {
		err := s.postGameSessionCharacterEvent(session, "semester216-refresh", charID, generation, func() error {
			identity, ok := s.boundGameSessionCharacterSnapshot(session)
			if ok {
				if err := s.sendCurrentSemesterCumulative(session, identity); err != nil {
					s.logGameEvent(session, "game-semester216-projection-deferred", "error", err)
				}
			}
			return s.armCurrentSemesterCumulativeTick(session)
		})
		if err != nil && !isClosedGameSessionEventError(err) {
			s.logGameEvent(session, "game-semester216-event-submit-failed", "error", err)
		}
	})
}

func (s *Service) cancelCurrentSemesterCumulativeTick(session *gameSession) {
	if s == nil || session == nil {
		return
	}
	s.mu.Lock()
	queue := s.gameplayTimers
	s.mu.Unlock()
	if queue != nil {
		queue.Cancel(fmt.Sprintf("dnf-semester216:%s", session.connID))
	}
}
