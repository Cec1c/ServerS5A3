package dnfbridge

import (
	"strconv"
	"strings"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// Capture only a canonical binding while service.mu is held. Keeping immutable
// owner/time values lets disconnect and character-select persist after removing
// that binding, without reading the socket's next character or holding runtime
// locks during repository IO. An auxiliary or replaced socket produces no event.
type currentGuildPresenceObservation struct {
	owner dnfrepo.GuildOwner
	at    time.Time
}

func (s *Service) currentGuildPresenceObservationLocked(session *gameSession, at time.Time) currentGuildPresenceObservation {
	if session == nil || session.conn == nil || session.selectedCharacterID == 0 ||
		s.gameSessions[session.selectedCharacterID] != session || session.characterGeneration == 0 ||
		strings.TrimSpace(session.accountID) == "" || strings.TrimSpace(session.accountID) != session.accountID {
		return currentGuildPresenceObservation{}
	}
	return currentGuildPresenceObservation{
		owner: dnfrepo.GuildOwner{AccountID: session.accountID, CharacterID: strconv.Itoa(int(session.selectedCharacterID))},
		at:    at,
	}
}

// Lifecycle cleanup must complete even if storage is unavailable. Log a failed
// observation; do not roll back the runtime binding or invent a successful save.
func (s *Service) persistCurrentGuildPresence(observations ...currentGuildPresenceObservation) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Guild == nil {
		return
	}
	for _, observation := range observations {
		if observation.owner.CharacterID == "" {
			continue
		}
		ctx, cancel := s.currentGuildContext()
		var guildID uint32
		err := repos.Guild.WithinGuild(ctx, []dnfrepo.GuildOwner{observation.owner}, func(tx dnfrepo.Group) error {
			if _, found, err := tx.Character.Load(ctx, observation.owner.CharacterID); err != nil {
				return err
			} else if !found {
				return dnfguild.ErrCharacterNotFound
			}
			r, err := dnfguild.Load(ctx, tx.Settings)
			if err != nil {
				return err
			}
			if g, _, found := r.GuildForOwner(observation.owner); found {
				guildID = g.ID
				if r.ObserveMemberOnline(observation.owner, observation.at) {
					return dnfguild.Save(ctx, tx.Settings, r, s.gameplayNow())
				}
			}
			return nil
		})
		if err != nil {
			s.logPacketEvent("game-guild-presence-persist-failed", "char_id", observation.owner.CharacterID, "error", err)
		} else if guildID != 0 {
			// Re-read current membership and binding for each recipient. No source
			// asset reservation or dungeon lock is held while reserving peers.
			s.refreshCurrentGuildText(ctx, guildID, boundGameSessionCharacter{}, true)
			s.refreshCurrentGuildText(ctx, guildID, boundGameSessionCharacter{}, false)
		}
		cancel()
	}
}
