package dnfbridge

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/platform/eventloop"
)

const (
	guildRefreshRequested = iota
	guildRefreshCoalesced
	guildRefreshQueued
	guildRefreshExecuted
	guildRefreshStale
	guildRefreshDeferred
	guildRefreshFailed
	guildSummaryMembers
	guildSummaryRows
	guildSummaryFallback
	guildSummaryFailed
	guildSummaryQueries
	guildSummaryFullLoads
	guildRosterCounterCount
)

var guildRosterCounterNames = [guildRosterCounterCount]string{
	"refresh_requested", "refresh_coalesced", "refresh_queued", "refresh_executed",
	"refresh_stale_dropped", "refresh_deferred", "refresh_failed",
	"summary_members", "summary_rows", "summary_fallback", "summary_failed",
	"summary_queries", "summary_full_loads",
}

func (s *Service) guildRosterCounter(kind int, n int64) {
	if s != nil && s.performance != nil {
		s.performance.guildRoster[kind].Add(n)
	}
}

const (
	guildPresenceCoalesceWindowEnv     = "DNFBRIDGE_GUILD_PRESENCE_COALESCE_MS"
	defaultGuildPresenceCoalesceWindow = 250 * time.Millisecond
	maxGuildPresenceCoalesceWindowMs   = 5000
	// DNF90 客户端关闭公会窗口无关闭协议包；客户端主动打开/刷新公会窗后
	// 仅在活跃窗口期内持续推送全量成员列表，超时后自动清理 memberListRequested，
	// 避免城镇挂机玩家每 250ms 被迫整表重建。玩家再次打开公会窗时会重新发起 140 请求。
	defaultGuildMemberListActiveWindow = 60 * time.Second
)

func (s *Service) guildRosterNow() time.Time {
	if s != nil && s.guildRosterNowFn != nil {
		return s.guildRosterNowFn()
	}
	return time.Now()
}

func (s *Service) guildMemberListActiveDuration() time.Duration {
	if s == nil {
		return 0
	}
	if s.guildMemberListActiveWindow > 0 {
		return s.guildMemberListActiveWindow
	}
	if s.guildMemberListActiveWindow < 0 {
		return 0
	}
	return defaultGuildMemberListActiveWindow
}

func (s *Service) markCurrentGuildMemberListRequested(session *gameSession) {
	if session == nil {
		return
	}
	session.guild.mu.Lock()
	session.guild.memberListRequested = true
	session.guild.memberListRequestedAt = s.guildRosterNow()
	session.guild.mu.Unlock()
}

func (s *Service) touchCurrentGuildMemberListIfRequested(session *gameSession) {
	if session == nil {
		return
	}
	session.guild.mu.Lock()
	if session.guild.memberListRequested {
		session.guild.memberListRequestedAt = s.guildRosterNow()
	}
	session.guild.mu.Unlock()
}

func (s *Service) guildMemberListActiveLocked(session *gameSession) bool {
	if !session.guild.memberListRequested {
		return false
	}
	if window := s.guildMemberListActiveDuration(); window > 0 && !session.guild.memberListRequestedAt.IsZero() {
		if s.guildRosterNow().Sub(session.guild.memberListRequestedAt) >= window {
			session.guild.memberListRequested = false
			session.guild.memberListRequestedAt = time.Time{}
			return false
		}
	}
	return true
}

func (s *Service) isCurrentGuildMemberListActive(session *gameSession) bool {
	if session == nil {
		return false
	}
	session.guild.mu.Lock()
	active := s.guildMemberListActiveLocked(session)
	session.guild.mu.Unlock()
	return active
}

func sessionInDungeon(session *gameSession) bool {
	if session == nil {
		return false
	}
	session.dungeon.mu.Lock()
	inDungeon := session.dungeon.runtime != nil
	session.dungeon.mu.Unlock()
	return inDungeon
}

func readGuildPresenceCoalesceWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv(guildPresenceCoalesceWindowEnv))
	if raw == "" {
		return defaultGuildPresenceCoalesceWindow
	}
	lower := strings.ToLower(raw)
	if lower == "0" || lower == "off" || lower == "false" || lower == "none" {
		return 0
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms < 0 || ms > maxGuildPresenceCoalesceWindowMs {
		return defaultGuildPresenceCoalesceWindow
	}
	return time.Duration(ms) * time.Millisecond
}

func (s *Service) guildPresenceCoalesceWindow() time.Duration {
	if s == nil {
		return 0
	}
	if s.guildPresenceCoalesceDelay > 0 {
		return s.guildPresenceCoalesceDelay
	}
	if s.guildPresenceCoalesceDelay < 0 {
		return 0
	}
	return s.options.guildPresenceCoalesceWindow
}

// One token belongs to one socket/character/generation/guild. All mutable
// fields use guild.mu; no storage, socket write or queue submission holds it.
type currentGuildPresenceRefresh struct {
	peer            boundGameSessionCharacter
	guildID         uint32
	shared          *guildPresenceReadCache
	revision        uint64
	queued, running bool
	retries         int
	timer           *time.Timer // queue-full retry timer
	coalesceTimer   *time.Timer // bounded max-wait burst coalesce timer
	stopContext     func() bool
}

func clearCurrentGuildPresenceRefreshLocked(session *gameSession) {
	if p := session.guild.presenceRefresh; p != nil {
		if p.timer != nil {
			p.timer.Stop()
		}
		if p.coalesceTimer != nil {
			p.coalesceTimer.Stop()
		}
		if p.stopContext != nil {
			p.stopContext()
		}
	}
	session.guild.presenceRefresh = nil
}

func resetCurrentGuildPresenceRefresh(session *gameSession, closed bool) {
	if session == nil {
		return
	}
	session.guild.mu.Lock()
	clearCurrentGuildPresenceRefreshLocked(session)
	clearCurrentGuildMembershipRefreshLocked(session)
	clearCurrentGuildEssenceRefreshLocked(session)
	clearCurrentGuildDungeonRefreshLocked(session)
	session.guild.presenceRefreshClosed = closed
	session.guild.lastPresenceRefreshAt = time.Time{}
	session.guild.lastMembershipRefreshAt = time.Time{}
	session.guild.lastEssenceRefreshAt = time.Time{}
	session.guild.lastDungeonRefreshAt = time.Time{}
	session.guild.weddingPrimedPeers = nil
	session.guild.sameChannelPrimeGuildID = 0
	session.guild.sameChannelPrimeViewerChar = 0
	session.guild.sameChannelPrimeViewerGen = 0
	session.guild.sameChannelPrimeCursor = ""
	session.guild.sameChannelPrimeIncomplete = false
	session.guild.weddingPrimeIncomplete = false
	session.guild.sameChannelPrimeRetryAfter = nil
	session.guild.mu.Unlock()
}

func (s *Service) discardCurrentGuildPresenceRefresh(p *currentGuildPresenceRefresh) {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.presenceRefresh == p {
		clearCurrentGuildPresenceRefreshLocked(session)
		s.guildRosterCounter(guildRefreshStale, 1)
	}
	session.guild.mu.Unlock()
}

func (s *Service) requestCurrentGuildPresenceRefresh(peer boundGameSessionCharacter, guildID uint32, shared ...*guildPresenceReadCache) {
	s.guildRosterCounter(guildRefreshRequested, 1)
	if peer.session == nil {
		return
	}
	s.mu.Lock()
	if s.gameSessions[peer.character] != peer.session || peer.session.selectedCharacterID != peer.character || peer.session.characterGeneration != peer.generation {
		s.mu.Unlock()
		s.guildRosterCounter(guildRefreshStale, 1)
		return
	}
	session := peer.session
	session.guild.mu.Lock()
	s.mu.Unlock()
	if session.guild.presenceRefreshClosed || session.loginRevoked.Load() ||
		(session.events != nil && !session.events.active.Load()) ||
		(session.ctx != nil && session.ctx.Err() != nil) {
		session.guild.mu.Unlock()
		s.guildRosterCounter(guildRefreshStale, 1)
		return
	}
	p := session.guild.presenceRefresh
	if p == nil || p.peer != peer || p.guildID != guildID {
		clearCurrentGuildPresenceRefreshLocked(session)
		p = &currentGuildPresenceRefresh{peer: peer, guildID: guildID}
		session.guild.presenceRefresh = p
		if session.ctx != nil {
			p.stopContext = context.AfterFunc(session.ctx, func() { s.discardCurrentGuildPresenceRefresh(p) })
		}
	}
	if len(shared) > 0 {
		p.shared = shared[0]
	}
	p.revision++
	if p.queued || p.running || p.timer != nil || p.coalesceTimer != nil {
		session.guild.mu.Unlock()
		s.guildRosterCounter(guildRefreshCoalesced, 1)
		return
	}
	if window := s.guildPresenceCoalesceWindow(); window > 0 && session.events != nil && session.events.loop != nil && !session.guild.lastPresenceRefreshAt.IsZero() {
		if remaining := window - time.Since(session.guild.lastPresenceRefreshAt); remaining > 0 {
			p.retries = 0
			p.coalesceTimer = s.armCurrentGuildPresenceCoalesceTimerLocked(session, p, remaining)
			session.guild.mu.Unlock()
			s.guildRosterCounter(guildRefreshCoalesced, 1)
			return
		}
	}
	p.retries, p.queued = 0, true
	session.guild.mu.Unlock()
	s.submitCurrentGuildPresenceRefresh(p)
}

func (s *Service) armCurrentGuildPresenceCoalesceTimerLocked(session *gameSession, p *currentGuildPresenceRefresh, delay time.Duration) *time.Timer {
	return time.AfterFunc(delay, func() {
		session.guild.mu.Lock()
		if session.guild.presenceRefresh != p || session.guild.presenceRefreshClosed {
			session.guild.mu.Unlock()
			return
		}
		p.coalesceTimer = nil
		if p.queued || p.running || p.timer != nil {
			session.guild.mu.Unlock()
			return
		}
		p.retries, p.queued = 0, true
		session.guild.mu.Unlock()
		s.submitCurrentGuildPresenceRefresh(p)
	})
}

func (s *Service) submitCurrentGuildPresenceRefresh(p *currentGuildPresenceRefresh) {
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		s.discardCurrentGuildPresenceRefresh(p)
		return
	}
	session := p.peer.session
	err := s.tryPostGameSessionCharacterEventWithDiscard(session, "guild-presence-refresh", p.peer.character, p.peer.generation,
		func() error { return s.runCurrentGuildPresenceRefresh(p) },
		func() { s.discardCurrentGuildPresenceRefresh(p) })
	if err == nil {
		s.guildRosterCounter(guildRefreshQueued, 1)
		return
	}
	session.guild.mu.Lock()
	defer session.guild.mu.Unlock()
	if session.guild.presenceRefresh != p {
		return
	}
	p.queued = false
	s.guildRosterCounter(guildRefreshDeferred, 1)
	if !errors.Is(err, eventloop.ErrQueueFull) {
		clearCurrentGuildPresenceRefreshLocked(session)
		return
	}
	// At most three delayed retries per burst; a later observation can retry an
	// exhausted dirty token. The timer does no IO and never waits for capacity.
	if p.retries >= 3 {
		return
	}
	delay := 100 * time.Millisecond * time.Duration(1<<p.retries)
	p.retries++
	p.timer = time.AfterFunc(delay, func() {
		session.guild.mu.Lock()
		if session.guild.presenceRefresh != p {
			session.guild.mu.Unlock()
			return
		}
		p.timer, p.queued = nil, true
		session.guild.mu.Unlock()
		s.submitCurrentGuildPresenceRefresh(p)
	})
}

func (s *Service) runCurrentGuildPresenceRefresh(p *currentGuildPresenceRefresh) error {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.presenceRefresh != p {
		session.guild.mu.Unlock()
		return nil
	}
	p.queued, p.running = false, true
	revision := p.revision
	shared := p.shared
	session.guild.mu.Unlock()
	s.guildRosterCounter(guildRefreshExecuted, 1)
	executedRealRefresh := false
	defer func() {
		session.guild.mu.Lock()
		if session.guild.presenceRefresh != p {
			session.guild.mu.Unlock()
			return
		}
		p.running = false
		if executedRealRefresh {
			session.guild.lastPresenceRefreshAt = time.Now()
		}
		followup := p.revision != revision
		submitNow := false
		if followup {
			p.retries = 0
			if window := s.guildPresenceCoalesceWindow(); executedRealRefresh && window > 0 && session.events != nil && session.events.loop != nil {
				p.coalesceTimer = s.armCurrentGuildPresenceCoalesceTimerLocked(session, p, window)
			} else {
				p.queued = true
				submitNow = true
			}
		} else {
			clearCurrentGuildPresenceRefreshLocked(session)
		}
		session.guild.mu.Unlock()
		// One followup goes to the tail; never loop and starve ordinary events.
		if submitNow {
			s.submitCurrentGuildPresenceRefresh(p)
		}
	}()
	// Presence is already durable. A background management/roster snapshot
	// must not occupy the combat packet queue or rebuild guild actors mid-run.
	// Check at execution time: the player may have entered after enqueueing.
	// Explicit window requests and later town refreshes read current storage.
	if sessionInDungeon(session) {
		s.guildRosterCounter(guildRefreshDeferred, 1)
		return nil
	}
	executedRealRefresh = true
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		return nil
	}
	requested := s.isCurrentGuildMemberListActive(session)
	g, err := s.readCurrentGuildDisplayShared(ctx, p.peer, false, requested, shared)
	if err != nil {
		s.guildRosterCounter(guildRefreshFailed, 1)
		return nil
	}
	if g == nil || g.ID != p.guildID || !s.boundGameSessionCharacterCurrent(p.peer) {
		return nil
	}
	if requested {
		var build, write time.Duration
		err = s.sendCurrentGuildRosterDisplayShared(ctx, p.peer, g, uint16(dnfenum.CmdPacketGuildAllMemberList), true, true, "presence", false, shared, &build, &write)
	} else {
		onlineCount, sampledAt := shared.sharedOnlineCount(s, g)
		err = s.sendCurrentGuildManagementProjectionWithOnline(session, g, onlineCount, sampledAt, false)
	}
	if err != nil {
		s.guildRosterCounter(guildRefreshFailed, 1)
	}

	return nil
}

const maxGuildMembershipRefreshRetries = 5

var (
	guildMembershipRetryBaseDelay        = 50 * time.Millisecond
	testGuildMembershipBeforeExhaustHook func()
)

// 成员变更广播令牌：每个会话/角色/世代最多保留一个在途或待重试的 membership 刷新，
// 与 presence 一致纳入 250ms 防抖合并与副本战斗期延迟跳过，并在队列满时做有界异步重试；
// 主动退避耗尽后保留待刷新标记，待事件循环释放队列槽位后自动恢复投递。
type currentGuildMembershipRefresh struct {
	peer            boundGameSessionCharacter
	shared          *guildPresenceReadCache
	revision        uint64
	queued, running bool
	exhausted       bool
	retries         int
	timer           *time.Timer
	coalesceTimer   *time.Timer
	stopContext     func() bool
}

func clearCurrentGuildMembershipRefreshLocked(session *gameSession) {
	if p := session.guild.membershipRefresh; p != nil {
		if p.timer != nil {
			p.timer.Stop()
		}
		if p.coalesceTimer != nil {
			p.coalesceTimer.Stop()
		}
		if p.stopContext != nil {
			p.stopContext()
		}
	}
	session.guild.membershipRefresh = nil
	session.guild.membershipRefreshPending.Store(false)
}

func (s *Service) discardCurrentGuildMembershipRefresh(p *currentGuildMembershipRefresh) {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.membershipRefresh == p {
		clearCurrentGuildMembershipRefreshLocked(session)
	}
	session.guild.mu.Unlock()
}

func (s *Service) requestCurrentGuildMembershipRefresh(peer boundGameSessionCharacter, shared *guildPresenceReadCache) {
	if peer.session == nil {
		return
	}
	s.invalidateTownMode0Character(peer.character)
	s.mu.Lock()
	if s.gameSessions[peer.character] != peer.session || peer.session.selectedCharacterID != peer.character || peer.session.characterGeneration != peer.generation {
		s.mu.Unlock()
		return
	}
	session := peer.session
	session.guild.mu.Lock()
	s.mu.Unlock()
	if session.guild.presenceRefreshClosed || session.loginRevoked.Load() ||
		(session.events != nil && !session.events.active.Load()) ||
		(session.ctx != nil && session.ctx.Err() != nil) {
		session.guild.mu.Unlock()
		return
	}
	p := session.guild.membershipRefresh
	if p == nil || p.peer != peer {
		clearCurrentGuildMembershipRefreshLocked(session)
		p = &currentGuildMembershipRefresh{peer: peer}
		session.guild.membershipRefresh = p
		if session.ctx != nil {
			p.stopContext = context.AfterFunc(session.ctx, func() { s.discardCurrentGuildMembershipRefresh(p) })
		}
	}
	p.shared = shared
	p.revision++
	if p.queued || p.running || p.timer != nil || p.coalesceTimer != nil {
		session.guild.mu.Unlock()
		s.guildRosterCounter(guildRefreshCoalesced, 1)
		return
	}
	if window := s.guildPresenceCoalesceWindow(); window > 0 && session.events != nil && session.events.loop != nil && !session.guild.lastMembershipRefreshAt.IsZero() {
		if elapsed := s.guildRosterNow().Sub(session.guild.lastMembershipRefreshAt); elapsed >= 0 && elapsed < window {
			p.exhausted = false
			session.guild.membershipRefreshPending.Store(false)
			p.retries = 0
			p.coalesceTimer = s.armCurrentGuildMembershipCoalesceTimerLocked(session, p, window-elapsed)
			session.guild.mu.Unlock()
			s.guildRosterCounter(guildRefreshCoalesced, 1)
			return
		}
	}
	p.exhausted = false
	session.guild.membershipRefreshPending.Store(false)
	p.retries, p.queued = 0, true
	session.guild.mu.Unlock()
	s.submitCurrentGuildMembershipRefresh(p)
}

func (s *Service) armCurrentGuildMembershipCoalesceTimerLocked(session *gameSession, p *currentGuildMembershipRefresh, delay time.Duration) *time.Timer {
	return time.AfterFunc(delay, func() {
		session.guild.mu.Lock()
		if session.guild.membershipRefresh != p || session.guild.presenceRefreshClosed ||
			session.loginRevoked.Load() ||
			(session.events != nil && !session.events.active.Load()) ||
			(session.ctx != nil && session.ctx.Err() != nil) {
			if session.guild.membershipRefresh == p {
				clearCurrentGuildMembershipRefreshLocked(session)
			}
			session.guild.mu.Unlock()
			return
		}
		p.coalesceTimer = nil
		if p.queued || p.running || p.timer != nil {
			session.guild.mu.Unlock()
			return
		}
		p.retries, p.queued = 0, true
		session.guild.mu.Unlock()
		s.submitCurrentGuildMembershipRefresh(p)
	})
}

// resumePendingGuildMembershipRefresh 在接收方事件循环处理完任意事件、释放队列槽位后，
// 唤醒因队列长期饱和而耗尽主动退避的 membership 待刷新令牌。
func (s *Service) resumePendingGuildMembershipRefresh(session *gameSession) {
	if s == nil || session == nil || !session.guild.membershipRefreshPending.Load() {
		return
	}
	session.guild.mu.Lock()
	p := session.guild.membershipRefresh
	if p == nil || !p.exhausted || p.queued || p.running || p.timer != nil || p.coalesceTimer != nil {
		if p == nil || !p.exhausted {
			session.guild.membershipRefreshPending.Store(false)
		}
		session.guild.mu.Unlock()
		return
	}
	if session.guild.presenceRefreshClosed || session.loginRevoked.Load() ||
		(session.events != nil && !session.events.active.Load()) ||
		(session.ctx != nil && session.ctx.Err() != nil) {
		clearCurrentGuildMembershipRefreshLocked(session)
		session.guild.mu.Unlock()
		return
	}
	p.exhausted = false
	session.guild.membershipRefreshPending.Store(false)
	p.retries, p.queued = 0, true
	session.guild.mu.Unlock()
	s.submitCurrentGuildMembershipRefresh(p)
}

func (s *Service) submitCurrentGuildMembershipRefresh(p *currentGuildMembershipRefresh) {
	s.submitCurrentGuildMembershipRefreshWithRecheck(p, true)
}

func (s *Service) submitCurrentGuildMembershipRefreshWithRecheck(p *currentGuildMembershipRefresh, allowImmediateRecheck bool) {
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		s.discardCurrentGuildMembershipRefresh(p)
		return
	}
	session := p.peer.session
	seqBefore := session.guild.eventLoopCompletedSeq.Load()
	err := s.tryPostGameSessionCharacterEventWithDiscard(session, "guild-membership-refresh", p.peer.character, p.peer.generation,
		func() error { return s.runCurrentGuildMembershipRefresh(p) },
		func() { s.discardCurrentGuildMembershipRefresh(p) })
	if err == nil {
		return
	}
	if errors.Is(err, eventloop.ErrQueueFull) && testGuildMembershipBeforeExhaustHook != nil {
		session.guild.mu.Lock()
		isExhaustAttempt := session.guild.membershipRefresh == p && p.retries >= maxGuildMembershipRefreshRetries
		session.guild.mu.Unlock()
		if isExhaustAttempt {
			testGuildMembershipBeforeExhaustHook()
		}
	}
	session.guild.mu.Lock()
	if session.guild.membershipRefresh != p {
		session.guild.mu.Unlock()
		return
	}
	p.queued = false
	if !errors.Is(err, eventloop.ErrQueueFull) ||
		session.guild.presenceRefreshClosed || session.loginRevoked.Load() ||
		(session.events != nil && !session.events.active.Load()) ||
		(session.ctx != nil && session.ctx.Err() != nil) {
		clearCurrentGuildMembershipRefreshLocked(session)
		session.guild.mu.Unlock()
		s.logGameEvent(session, "game-guild-member-refresh-failed", "error", err)
		return
	}
	if p.retries >= maxGuildMembershipRefreshRetries {
		// 先发布待唤醒状态再核对 eventLoopCompletedSeq（Dekker Store-Load 屏障）：
		// - 若 seq 未变化，队列在 pending 发布前无任何事件完成，队列中必有未完成事件在结束时唤醒；
		// - 若 seq 已变化，队列可能在发布前已排空，立即回收并再试一次（若仍满则挂单次可取消兜底定时器防自旋）。
		p.exhausted = true
		session.guild.membershipRefreshPending.Store(true)
		if session.guild.eventLoopCompletedSeq.Load() != seqBefore {
			p.exhausted = false
			session.guild.membershipRefreshPending.Store(false)
			if allowImmediateRecheck {
				p.queued = true
				session.guild.mu.Unlock()
				s.submitCurrentGuildMembershipRefreshWithRecheck(p, false)
				return
			}
			fallbackDelay := guildMembershipRetryBaseDelay * time.Duration(1<<maxGuildMembershipRefreshRetries)
			p.timer = time.AfterFunc(fallbackDelay, func() {
				session.guild.mu.Lock()
				if session.guild.membershipRefresh != p || session.guild.presenceRefreshClosed ||
					session.loginRevoked.Load() ||
					(session.events != nil && !session.events.active.Load()) ||
					(session.ctx != nil && session.ctx.Err() != nil) {
					if session.guild.membershipRefresh == p {
						clearCurrentGuildMembershipRefreshLocked(session)
					}
					session.guild.mu.Unlock()
					return
				}
				p.timer, p.queued = nil, true
				session.guild.mu.Unlock()
				s.submitCurrentGuildMembershipRefreshWithRecheck(p, true)
			})
			session.guild.mu.Unlock()
			return
		}
		session.guild.mu.Unlock()
		s.logGameEvent(session, "game-guild-member-refresh-deferred", "error", err)
		return
	}
	delay := guildMembershipRetryBaseDelay * time.Duration(1<<p.retries)
	p.retries++
	p.timer = time.AfterFunc(delay, func() {
		session.guild.mu.Lock()
		if session.guild.membershipRefresh != p || session.guild.presenceRefreshClosed ||
			session.loginRevoked.Load() ||
			(session.events != nil && !session.events.active.Load()) ||
			(session.ctx != nil && session.ctx.Err() != nil) {
			if session.guild.membershipRefresh == p {
				clearCurrentGuildMembershipRefreshLocked(session)
			}
			session.guild.mu.Unlock()
			return
		}
		p.timer, p.queued = nil, true
		session.guild.mu.Unlock()
		s.submitCurrentGuildMembershipRefresh(p)
	})
	session.guild.mu.Unlock()
}

func (s *Service) runCurrentGuildMembershipRefresh(p *currentGuildMembershipRefresh) error {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.membershipRefresh != p {
		session.guild.mu.Unlock()
		return nil
	}
	p.queued, p.running = false, true
	revision := p.revision
	shared := p.shared
	session.guild.mu.Unlock()
	executedRealRefresh := false
	defer func() {
		session.guild.mu.Lock()
		if session.guild.membershipRefresh != p {
			session.guild.mu.Unlock()
			return
		}
		p.running = false
		if executedRealRefresh {
			session.guild.lastMembershipRefreshAt = s.guildRosterNow()
		}
		followup := p.revision != revision
		submitNow := false
		if followup && !session.guild.presenceRefreshClosed && !session.loginRevoked.Load() &&
			(session.events == nil || session.events.active.Load()) &&
			(session.ctx == nil || session.ctx.Err() == nil) {
			p.retries = 0
			if window := s.guildPresenceCoalesceWindow(); executedRealRefresh && window > 0 && session.events != nil && session.events.loop != nil {
				p.coalesceTimer = s.armCurrentGuildMembershipCoalesceTimerLocked(session, p, window)
			} else {
				p.queued = true
				submitNow = true
			}
		} else {
			clearCurrentGuildMembershipRefreshLocked(session)
		}
		session.guild.mu.Unlock()
		if submitNow {
			s.submitCurrentGuildMembershipRefresh(p)
		}
	}()
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		return nil
	}
	if sessionInDungeon(session) {
		s.guildRosterCounter(guildRefreshDeferred, 1)
		return nil
	}
	if !s.isCurrentGuildMemberListActive(session) {
		return nil
	}
	executedRealRefresh = true
	if err := s.handleCurrentGuildMemberListObservedShared(session, uint16(dnfenum.CmdPacketGuildAllMemberList), "membership", shared); err != nil {
		s.logGameEvent(session, "game-guild-member-refresh-failed", "error", err)
	}
	return nil
}
