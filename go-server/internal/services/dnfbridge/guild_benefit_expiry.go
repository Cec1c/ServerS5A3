package dnfbridge

import (
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
)

func guildBenefitTimerName(session *gameSession) string {
	return fmt.Sprintf("guild-benefit:%p", session)
}

// The shared scheduler is also authoritative for native expiry: it refreshes
// welfare rows and removes expired buffs even across the client's tick wrap.
func (s *Service) scheduleCurrentGuildBenefitExpiry(session *gameSession, g *guild.Guild, now time.Time) error {
	var deadline time.Time
	if g != nil {
		for _, b := range g.Benefits {
			if b.Active(now) && !b.ExpiresAt.IsZero() && (deadline.IsZero() || b.ExpiresAt.Before(deadline)) {
				deadline = b.ExpiresAt
			}
		}
	}
	name := guildBenefitTimerName(session)
	queue := s.ensureGameplayTimeQueue()
	if deadline.IsZero() {
		queue.Cancel(name)
		return nil
	}
	peer, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return nil
	}
	return queue.ScheduleAfter(name, deadline.Sub(now), func(time.Time) { s.expireCurrentGuildBenefit(peer) })
}

func (s *Service) expireCurrentGuildBenefit(peer boundGameSessionCharacter) {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{peer})
	if err != nil {
		return
	}
	defer release()
	retry := func() {
		_ = s.ensureGameplayTimeQueue().ScheduleAfter(guildBenefitTimerName(peer.session), time.Second, func(time.Time) { s.expireCurrentGuildBenefit(peer) })
	}
	if !peer.session.townMu.TryLock() {
		retry()
		return
	}
	defer peer.session.townMu.Unlock()
	if peer.session.channelExitAcknowledged {
		return
	}
	if peer.session.townSceneReadyCharacterID != peer.character {
		retry()
		return
	}
	g, err := s.currentGuildForSelectedCharacter(peer.session)
	if err == nil {
		err = s.writeCurrentGuildManagementState(peer.session, g)
	}
	if err != nil {
		s.logGameEvent(peer.session, "game-guild-benefit-expiry-refresh-failed", "error", err)
		retry()
	}
}
