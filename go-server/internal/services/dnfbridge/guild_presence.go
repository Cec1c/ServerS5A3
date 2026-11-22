package dnfbridge

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	guildPresencePersistMinIntervalEnv    = "DNFBRIDGE_GUILD_PRESENCE_PERSIST_MIN_INTERVAL_SEC"
	maxGuildPresencePersistMinIntervalSec = 3600
)

func readGuildPresencePersistMinInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(guildPresencePersistMinIntervalEnv))
	if raw == "" {
		return 0
	}
	lower := strings.ToLower(raw)
	if lower == "0" || lower == "off" || lower == "false" || lower == "none" {
		return 0
	}
	sec, err := strconv.Atoi(raw)
	if err != nil || sec < 0 || sec > maxGuildPresencePersistMinIntervalSec {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func (s *Service) guildPresencePersistMinInterval() time.Duration {
	if s == nil {
		return 0
	}
	return s.options.guildPresencePersistMinInterval
}

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
		// 权威只读预检：通过 WithinGuildRead（定向主/写库、无写锁）校验成员资格；
		// 1）未加入任何公会的角色无需争抢全局 GuildRegistryScope 写锁；
		// 2）若启用了最小落库间隔，且本周排行在场位已记录、距上次落库未满间隔、
		//    且角色归属/未删除核验通过，则跳过本次整包 JSON 重写，但仍立即广播在线变化。
		// 若只读事务不可用或失败，则退回原有写事务路径以保持故障日志和桩仓储语义。
		if reader, ok := repos.Guild.(dnfrepo.GuildReadUnitOfWork); ok {
			var memberFound bool
			var throttledGuildID uint32
			minInterval := s.guildPresencePersistMinInterval()
			preErr := reader.WithinGuildRead(ctx, func(view dnfrepo.GuildReadView) error {
				membership, found, err := dnfguild.ReadGuildMembership(ctx, view.Settings, observation.owner.CharacterID)
				if err != nil || !found {
					return err
				}
				g, err := membership.RosterProjection(observation.owner)
				if err != nil {
					return err
				}
				if g == nil {
					return nil
				}
				memberFound = true
				if minInterval > 0 {
					m, ok := g.Members[observation.owner.CharacterID]
					if ok && !observation.at.IsZero() && !observation.at.Before(m.JoinedAt) &&
						!m.LastOnlineAt.IsZero() && m.HasRankingPresenceFor(observation.at) &&
						observation.at.Sub(m.LastOnlineAt) < minInterval {
						chars, charErr := view.Characters.LoadGuildCharacters(ctx, []string{observation.owner.CharacterID})
						if charErr != nil {
							return charErr
						}
						c, exists := chars[observation.owner.CharacterID]
						if exists && c.CharacterID == observation.owner.CharacterID &&
							c.AccountID == observation.owner.AccountID && c.DeleteFlag == 0 {
							throttledGuildID = g.ID
						}
					}
				}
				return nil
			})
			if preErr == nil && !memberFound {
				cancel()
				continue
			}
			if preErr == nil && throttledGuildID != 0 {
				s.queueCurrentGuildPresenceRefresh(ctx, throttledGuildID)
				cancel()
				continue
			}
		}
		var guildID uint32
		applyPresence := func(tx dnfrepo.Group) error {
			identity, found, err := dnfrepo.LoadCharacterIdentity(ctx, tx.Character, observation.owner.CharacterID)
			if err != nil {
				return err
			}
			if !found || identity.CharacterID != observation.owner.CharacterID || identity.AccountID != observation.owner.AccountID {
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
		}
		var err error
		if presenceUOW, ok := repos.Guild.(dnfrepo.GuildPresenceUnitOfWork); ok {
			err = presenceUOW.WithinGuildPresence(ctx, observation.owner, applyPresence)
		} else {
			err = repos.Guild.WithinGuild(ctx, []dnfrepo.GuildOwner{observation.owner}, applyPresence)
		}
		if err != nil {
			s.logPacketEvent("game-guild-presence-persist-failed", "char_id", observation.owner.CharacterID, "error", err)
		} else if guildID != 0 {
			s.queueCurrentGuildPresenceRefresh(ctx, guildID)
		}
		cancel()
	}
}

// Last-seen is already durable. Optional projections belong to each recipient's
// event loop: a member with a slow roster query or socket must not hold another
// member's login/logout ACK. The callback reloads membership and rejects a
// replaced character generation; no saved registry snapshot is replayed.
func (s *Service) queueCurrentGuildPresenceRefresh(ctx context.Context, guildID uint32) {
	_, registry, err := s.readCurrentGuildDirectory(ctx)
	if err != nil {
		s.logInfo("dnfbridge guild presence refresh unavailable", "error", err)
		return
	}
	shared := &guildPresenceReadCache{}
	for _, member := range registry.Members(guildID) {
		id, err := strconv.ParseUint(member.CharacterID, 10, 16)
		if err != nil || id == 0 {
			continue
		}
		peer, online := s.onlineGameSessionCharacterSnapshot(uint16(id))
		if !online {
			continue
		}
		s.requestCurrentGuildPresenceRefresh(peer, guildID, shared)
	}
}
