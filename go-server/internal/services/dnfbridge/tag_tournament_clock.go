package dnfbridge

import (
	"context"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/platform/eventloop"
)

const currentTagTournamentClockName = "tag-tournament:lobby-day"

// 共用进程玩法时间队列，每分钟核对服务日；不为每名玩家创建计时器。
// 回调只投递到原角色世代的会话循环，在循环内重查频道和角色归属。
func (s *Service) startCurrentTagTournamentClock(ctx context.Context) {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return
	}
	if err := s.scheduleCurrentTagTournamentClock(ctx); err != nil {
		s.logWarn("帝国竞技场每日界面调度失败", "error", err)
	}
}

func (s *Service) scheduleCurrentTagTournamentClock(ctx context.Context) error {
	queue := s.ensureGameplayTimeQueue()
	if queue == nil {
		return errGameplayTimeQueueUnavailable
	}
	return queue.ScheduleAfter(currentTagTournamentClockName, time.Minute, func(_ time.Time) {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		s.queueCurrentTagTournamentLobbyRefresh(queue.Now())
		if ctx == nil || ctx.Err() == nil {
			if err := s.scheduleCurrentTagTournamentClock(ctx); err != nil {
				s.logWarn("帝国竞技场每日界面续调失败", "error", err)
			}
		}
	})
}

func (s *Service) queueCurrentTagTournamentLobbyRefresh(now time.Time) {
	s.mu.Lock()
	identities := make([]boundGameSessionCharacter, 0, len(s.gameSessions))
	for id, p := range s.gameSessions {
		if p != nil && id != 0 && p.selectedCharacterID == id {
			identities = append(identities, boundGameSessionCharacter{session: p, character: id, generation: p.characterGeneration})
		}
	}
	s.mu.Unlock()
	for _, identity := range identities {
		identity := identity
		run := func() error {
			if !s.boundGameSessionCharacterCurrent(identity) {
				return nil
			}
			return s.refreshCurrentTagTournamentLobby(identity.session, now)
		}
		var err error
		p := identity.session
		if p.events == nil || p.events.loop == nil {
			err = run()
		} else {
			// 满队列在下分钟重试，不能让一个会话阻塞共用玩法时间队列。
			err = p.events.loop.Submit(eventloop.Event{Type: "帝国竞技场服务日刷新", Context: p.ctx, Priority: eventloop.PriorityLow, Data: gameSessionEventTask{source: "帝国竞技场服务日刷新", characterID: identity.character, characterGeneration: identity.generation, async: true, run: run}})
		}
		if err != nil && !isClosedGameSessionEventError(err) {
			s.logGameEvent(identity.session, "帝国竞技场服务日刷新等待重试", "error", err)
		}
	}
}

func (s *Service) refreshCurrentTagTournamentLobby(p *gameSession, now time.Time) error {
	if s.isCurrentTagTournamentChannel(p) && !now.IsZero() && p.tagTournamentBattle != nil {
		b := p.tagTournamentBattle
		if b.plan.ServiceDay < premium.ServiceDayID(now) {
			if err := s.closeCurrentTagTournamentForReturn(p); err != nil {
				return err
			}
			if p.dungeon.runtime != nil {
				return s.handleDungeonBackToVillage(p, nil)
			}
			p.tagTournamentBattle = nil
		}
		if b.plan.ServiceDay == premium.ServiceDayID(now) && (b.tagProjectionPending || b.settlementPending || b.settlementProjectionPending || b.mailAlarmPending || b.roundProjectionPending || b.completionBody != nil && !b.completionSent) {
			if _, err := s.currentTagTournamentBattleFor(p); err != nil {
				return err
			}
			if b.tagProjectionPending {
				if err := s.sendCurrentTagTournamentTagProjection(p, b); err != nil {
					return err
				}
			}
			if err := s.flushCurrentTagTournamentSettlement(p, b); err != nil {
				return err
			}
		}
	}
	if !s.isCurrentTagTournamentChannel(p) || now.IsZero() || !currentTagTournamentLobbyReady(p) || p.tagTournamentLobbyDay >= premium.ServiceDayID(now) {
		return nil
	}
	if err := s.sendCurrentTagTournamentDailyLobby(p, now); err != nil {
		return err
	}
	return s.sendCurrentTagTournamentWeeklyInfo(p)
}
