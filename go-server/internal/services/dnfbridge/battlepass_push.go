package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	bp "longheng.io/server/internal/modules/dnf/battlepass"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
	"sync"
	"time"
)

// 只在游戏内连接订阅一次。事务提交后的计数在内存投影，提醒变化才发送28字节。
// 登录/换角读一次权威状态，领取改账号时只重读账号；跨期/疲劳变化只重读疲劳。
type gameBattlePassPushState struct {
	mu                         sync.Mutex
	closed, scheduled          bool
	pending                    map[string]map[string]int64
	accountDirty, fatigueDirty bool
	retryAttempt               uint8
	unsubscribe                func()
	stopContext                func() bool
	request                    gamePlayerStatsRequest
	bound                      boundGameSessionCharacter
	identity                   localPlayerIdentity
	key                        string
	clock                      gameplayTimeQueue
	// 以下只在会话事件循环访问。
	ready, sent   bool
	account       repo.AccountRecord
	characters    map[string]repo.CharacterRecord
	fatigue       []bp.Counter
	day, revision uint32
	mask          byte
}

func (p *gameBattlePassPushState) stop(s *Service) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.pending = nil
	cancel := p.unsubscribe
	stopContext := p.stopContext
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stopContext != nil {
		stopContext()
	}
	p.clock.Cancel(p.key)
	p.clock.Cancel(p.key + ":boundary")
}
func (p *gameBattlePassPushState) alive() bool { p.mu.Lock(); defer p.mu.Unlock(); return !p.closed }
func battlePassPushCounters(ch repo.CharacterRecord) repo.CharacterRecord {
	out := repo.CharacterRecord{AccountID: ch.AccountID, CharacterID: ch.CharacterID, Stats: map[string]int64{}}
	for k, v := range ch.Stats {
		if _, _, _, ok := repo.ReadBattlePassCounterKey(k); ok {
			out.Stats[k] = v
		}
	}
	return out
}
func mergeBattlePassPushCounters(dst map[string]repo.CharacterRecord, id, account string, values map[string]int64) {
	ch := dst[id]
	if ch.Stats == nil {
		ch = repo.CharacterRecord{CharacterID: id, AccountID: account, Stats: map[string]int64{}}
	}
	// 每服务日计数单调增长；提交回调交错不允许旧值覆盖较新的值。
	for k, v := range values {
		if v > ch.Stats[k] {
			ch.Stats[k] = v
		}
	}
	dst[id] = ch
}
func (s *Service) sendBattlePassPush(session *gameSession, r gamePlayerStatsRequest, status uint16, mask byte, revision uint32) error {
	data := make([]byte, 8)
	data[0] = mask
	binary.LittleEndian.PutUint32(data[4:], revision)
	body := encodeGamePlayerStatsReply(r, status, data)
	copy(body, "BPN2")
	return s.sendGameUpperFixed16Transport(session, battlePassGameOpcode, body, 1, 0, false, "battlepass_notify_fixed16_plain")
}
func (s *Service) scheduleBattlePassPush(session *gameSession, p *gameBattlePassPushState) {
	s.scheduleBattlePassPushAfter(session, p, 100*time.Millisecond)
}
func (s *Service) scheduleBattlePassPushAfter(session *gameSession, p *gameBattlePassPushState, delay time.Duration) {
	p.mu.Lock()
	if p.closed || p.scheduled {
		p.mu.Unlock()
		return
	}
	p.scheduled = true
	p.mu.Unlock()
	callback := func(time.Time) {
		if !p.alive() || session.ctx.Err() != nil {
			return
		}
		events := session.events
		if events == nil || !events.active.Load() {
			p.stop(s)
			return
		}
		e := events.loop.Submit(eventloop.Event{Type: "battlepass-notify", Priority: eventloop.PriorityLow, Context: session.ctx, Data: gameSessionEventTask{source: "battlepass-notify", characterID: p.bound.character, characterGeneration: p.bound.generation, async: true, run: func() error { return s.flushBattlePassPush(session, p) }}})
		if e != nil {
			p.mu.Lock()
			p.scheduled = false
			p.mu.Unlock()
			s.scheduleBattlePassPush(session, p)
		}
	}
	if e := p.clock.ScheduleAfter(p.key, delay, callback); e != nil {
		p.mu.Lock()
		p.scheduled = false
		p.mu.Unlock()
	}
	// 停止可能与登记定时器交错；已结束的订阅不能留下补试定时器。
	if !p.alive() {
		p.clock.Cancel(p.key)
	}
}
func (s *Service) retryBattlePassPush(session *gameSession, p *gameBattlePassPushState, accountDirty, fatigueDirty bool) {
	p.mu.Lock()
	if p.closed || session.ctx.Err() != nil {
		p.mu.Unlock()
		return
	}
	// 账号读取失败时，尚未执行的疲劳刷新也必须保留。
	p.accountDirty = p.accountDirty || accountDirty
	p.fatigueDirty = p.fatigueDirty || fatigueDirty
	attempt := p.retryAttempt
	if attempt >= 5 {
		p.mu.Unlock()
		s.logWarn("战令提醒刷新失败，本轮五次补试已用完", "account_id", p.identity.AccountID)
		return
	}
	p.retryAttempt++
	p.mu.Unlock()
	// 只在失败后退避补试；成功后没有定时查询。新提交可重新开启一轮补试。
	s.scheduleBattlePassPushAfter(session, p, (500*time.Millisecond)<<attempt)
}
func (s *Service) queueBattlePassChange(session *gameSession, p *gameBattlePassPushState, e repo.BattlePassChange) {
	if e.AccountID != p.identity.AccountID {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if e.CharacterID != "" {
		m := p.pending[e.CharacterID]
		if m == nil {
			m = map[string]int64{}
			p.pending[e.CharacterID] = m
		}
		for k, v := range e.Counters {
			if v > m[k] {
				m[k] = v
			}
		}
	}
	p.accountDirty = p.accountDirty || e.Account
	p.fatigueDirty = p.fatigueDirty || e.Fatigue
	p.retryAttempt = 0
	p.mu.Unlock()
	s.scheduleBattlePassPush(session, p)
}
func (s *Service) flushBattlePassPush(session *gameSession, p *gameBattlePassPushState) error {
	if session.battlePassPush != p || !p.alive() || !s.boundGameSessionCharacterCurrent(p.bound) || session.loginRevoked.Load() {
		p.stop(s)
		return nil
	}
	p.mu.Lock()
	p.scheduled = false
	if !p.ready {
		p.mu.Unlock()
		return nil
	}
	pending, accountDirty, fatigueDirty := p.pending, p.accountDirty, p.fatigueDirty
	p.pending = map[string]map[string]int64{}
	p.accountDirty = false
	p.fatigueDirty = false
	p.mu.Unlock()
	for id, values := range pending {
		mergeBattlePassPushCounters(p.characters, id, p.identity.AccountID, values)
	}
	g, ok := s.repositoryGroup()
	if !ok {
		s.retryBattlePassPush(session, p, accountDirty, fatigueDirty)
		return nil
	}
	ctx, cancel := context.WithTimeout(session.ctx, createWriteTimeout)
	defer cancel()
	now := s.gameplayNow()
	policy, e := currentBattlePassPolicy()
	if e != nil {
		s.retryBattlePassPush(session, p, accountDirty, fatigueDirty)
		return nil
	}
	mask := byte(0)
	active := s.currentBattlePassEnabled() && policy.Active(now)
	if active {
		fatigueDirty = fatigueDirty || p.day != bp.DayNumber(now)
		if accountDirty {
			a, found, err := g.Account.Load(ctx, p.identity.AccountID)
			if err != nil || !found {
				s.retryBattlePassPush(session, p, true, fatigueDirty)
				return nil
			}
			p.account = a
		}
		if fatigueDirty {
			values, err := loadBattlePassFatigueCounters(ctx, g, p.identity.AccountID, policy, now)
			if err != nil {
				s.retryBattlePassPush(session, p, false, true)
				return nil
			}
			p.fatigue = values
		}
		p.day = bp.DayNumber(now)
		c, fingerprint, err := s.currentBattlePassCatalog()
		if err != nil {
			s.retryBattlePassPush(session, p, false, false)
			return nil
		}
		chars := make([]repo.CharacterRecord, 0, len(p.characters))
		for _, ch := range p.characters {
			chars = append(chars, ch)
		}
		counters := append(bp.Counters(chars, repo.ActivityDay(policy.Start), repo.ActivityDay(now)), p.fatigue...)
		v, err := bp.Inspect(c, policy, p.account, bp.Command{AccountID: p.identity.AccountID, Now: now, Counters: counters, Fingerprint: fingerprint})
		if err != nil {
			s.retryBattlePassPush(session, p, false, false)
			return nil
		}
		mask = 1 | bp.ClaimNotifications(c, v)
	}
	if !p.sent || mask != p.mask {
		p.revision++
		if p.revision == 0 {
			p.revision++
		}
		if e := s.sendBattlePassPush(session, p.request, 0, mask, p.revision); e != nil {
			return e
		}
		p.mask = mask
		p.sent = true
	}
	p.mu.Lock()
	p.retryAttempt = 0
	p.mu.Unlock()
	if !s.currentBattlePassEnabled() || !now.Before(policy.End) {
		p.stop(s)
		return nil
	}
	s.scheduleBattlePassPushBoundary(session, p, policy, now)
	return nil
}
func (s *Service) scheduleBattlePassPushBoundary(session *gameSession, p *gameBattlePassPushState, policy bp.Policy, now time.Time) {
	// 时间边界是一次性定时，不做空闲轮询；换角/断开时取消。
	if !now.Before(policy.End) {
		return
	}
	local := now.In(time.FixedZone("CN", 8*3600))
	next := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, local.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	if policy.Start.After(now) && policy.Start.Before(next) {
		next = policy.Start
	}
	if policy.End.Before(next) {
		next = policy.End
	}
	_ = p.clock.ScheduleAfter(p.key+":boundary", next.Sub(now), func(time.Time) {
		s.queueBattlePassChange(session, p, repo.BattlePassChange{AccountID: p.identity.AccountID, Fatigue: true})
	})
}
func (s *Service) handleBattlePassPushSubscribe(session *gameSession, body []byte) error {
	if len(body) != 12 || string(body[:4]) != "BPE2" || session == nil {
		return nil
	}
	r := gamePlayerStatsRequest{binary.LittleEndian.Uint32(body[4:]), binary.LittleEndian.Uint32(body[8:])}
	if r.id == 0 || r.epoch == 0 {
		return nil
	}
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	state := &session.battlePassEntry
	now := time.Now()
	if !state.last.IsZero() && now.Sub(state.last) < 5*time.Second {
		return s.sendBattlePassPush(session, r, 2, 0, 0)
	}
	state.last = now
	if !s.playerStatsQueries.acquire() {
		return s.sendBattlePassPush(session, r, 2, 0, 0)
	}
	g, ok := s.repositoryGroup()
	if !ok || g.BattlePassChanges == nil || g.Account == nil || g.Character == nil {
		s.playerStatsQueries.release()
		return s.sendBattlePassPush(session, r, 4, 0, 0)
	}
	if session.battlePassPush != nil {
		session.battlePassPush.stop(s)
	}
	p := &gameBattlePassPushState{clock: s.ensureGameplayTimeQueue(), request: r, bound: bound, identity: identity, key: fmt.Sprintf("battlepass-push:%s:%d:%d", session.connID, bound.generation, r.id), pending: map[string]map[string]int64{}}
	session.battlePassPush = p
	unsubscribe := g.BattlePassChanges.Subscribe(identity.AccountID, func(e repo.BattlePassChange) { s.queueBattlePassChange(session, p, e) })
	p.mu.Lock()
	p.unsubscribe = unsubscribe
	closed := p.closed
	p.mu.Unlock()
	if closed {
		unsubscribe()
	}
	stopContext := context.AfterFunc(session.ctx, func() { p.stop(s) })
	p.mu.Lock()
	p.stopContext = stopContext
	closed = p.closed
	p.mu.Unlock()
	if closed {
		stopContext()
	}
	events := session.events
	go func() {
		defer s.playerStatsQueries.release()
		ctx, cancel := context.WithTimeout(session.ctx, 5*time.Second)
		defer cancel()
		observedAt := s.gameplayNow()
		a, found, e := g.Account.Load(ctx, identity.AccountID)
		var chars []repo.CharacterRecord
		var fatigue []bp.Counter
		var policy bp.Policy
		if e == nil && !found {
			e = bp.ErrUnavailable
		}
		if e == nil {
			chars, e = g.Character.ListByAccount(ctx, identity.AccountID, repo.DefaultCharacterSlotLimit)
		}
		if e == nil {
			policy, e = currentBattlePassPolicy()
		}
		if e == nil {
			fatigue, e = loadBattlePassFatigueCounters(ctx, g, identity.AccountID, policy, observedAt)
		}
		if session.ctx.Err() != nil || !p.alive() || events == nil || !events.active.Load() {
			p.stop(s)
			return
		}
		_ = events.loop.Submit(eventloop.Event{Type: "battlepass-subscribe", Priority: eventloop.PriorityLow, Context: session.ctx, Data: gameSessionEventTask{source: "battlepass-subscribe", characterID: bound.character, characterGeneration: bound.generation, async: true, run: func() error {
			if session.battlePassPush != p || !s.boundGameSessionCharacterCurrent(bound) {
				p.stop(s)
				return nil
			}
			if e != nil {
				p.stop(s)
				return s.sendBattlePassPush(session, r, 4, 0, 0)
			}
			p.account = a
			p.characters = map[string]repo.CharacterRecord{}
			for _, ch := range chars {
				if ch.AccountID != identity.AccountID {
					p.stop(s)
					return s.sendBattlePassPush(session, r, 4, 0, 0)
				}
				p.characters[ch.CharacterID] = battlePassPushCounters(ch)
			}
			p.fatigue = fatigue
			p.day = bp.DayNumber(observedAt)
			p.ready = true
			return s.flushBattlePassPush(session, p)
		}}})
	}()
	return nil
}
