package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

// 本地扩展协议v1，须使用配套EXE；不复用公会编号，不提供资产写入能力。
const (
	playerStatsGameOpcode     uint16 = 0x7f10
	playerStatsGameMaxData           = 64 * 1024
	playerStatsGameMaxWorkers        = 4
	playerStatsGameCacheTTL          = 5 * time.Second
	playerStatsGameCooldown          = time.Second
)

type gamePlayerStatsQueries struct {
	mu     sync.Mutex
	active int
}

func (q *gamePlayerStatsQueries) acquire() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active >= playerStatsGameMaxWorkers {
		return false
	}
	q.active++
	return true
}
func (q *gamePlayerStatsQueries) release() { q.mu.Lock(); q.active--; q.mu.Unlock() }

type gamePlayerStatsState struct {
	busy          atomic.Bool
	last          time.Time
	request       uint32
	cache         []byte
	cacheAt       time.Time
	cacheOverview bool
	cacheGold     bool
	cacheDay      string
	identity      localPlayerIdentity
}
type gamePlayerStatsRequest struct{ id, epoch uint32 }

func decodeGamePlayerStatsRequest(body []byte) (gamePlayerStatsRequest, bool) {
	if len(body) != 12 || (string(body[:4]) != "PSQ1" && string(body[:4]) != "PSQ2" && string(body[:4]) != "PSQ3") {
		return gamePlayerStatsRequest{}, false
	}
	r := gamePlayerStatsRequest{binary.LittleEndian.Uint32(body[4:8]), binary.LittleEndian.Uint32(body[8:12])}
	return r, r.id != 0 && r.epoch != 0
}
func encodeGamePlayerStatsReply(r gamePlayerStatsRequest, status uint16, data []byte) []byte {
	if len(data) > playerStatsGameMaxData {
		status = 5
		data = nil
	}
	if status != 0 {
		data = nil
	}
	b := make([]byte, 20+len(data))
	copy(b, "PSR1")
	binary.LittleEndian.PutUint32(b[4:8], r.id)
	binary.LittleEndian.PutUint32(b[8:12], r.epoch)
	binary.LittleEndian.PutUint16(b[12:14], status)
	binary.LittleEndian.PutUint32(b[16:20], uint32(len(data)))
	copy(b[20:], data)
	return b
}

// 此入口只能在有效会话事件循环内调用；绝不使用共享账号/PID/IP兜底。
func (s *Service) playerStatsGameIdentity(session *gameSession) (localPlayerIdentity, boundGameSessionCharacter, bool) {
	if session == nil || session.events == nil || session.events.loop == nil || !session.events.active.Load() || session.loginRevoked.Load() || session.ctx == nil || session.ctx.Err() != nil || !session.accountFromToken || session.accountID == "" {
		return localPlayerIdentity{}, boundGameSessionCharacter{}, false
	}
	bound, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || bound.generation == 0 {
		return localPlayerIdentity{}, boundGameSessionCharacter{}, false
	}
	return localPlayerIdentity{AccountID: session.accountID, CharacterID: strconv.Itoa(int(bound.character)), Generation: bound.generation}, bound, true
}

func (s *Service) sendPlayerStatsGame(session *gameSession, r gamePlayerStatsRequest, status uint16, data []byte) error {
	return s.sendGameUpperFixed16Transport(session, playerStatsGameOpcode, encodeGamePlayerStatsReply(r, status, data), 1, 0, false, "player_stats_v1_fixed16_plain")
}

func (s *Service) handlePlayerStatsGame(session *gameSession, body []byte) error {
	r, ok := decodeGamePlayerStatsRequest(body)
	if !ok || session == nil {
		return nil
	}
	gold := string(body[:4]) == "PSQ3"
	overview := string(body[:4]) == "PSQ2" || gold
	state := &session.playerStats
	now := time.Now()
	// 即使恶意连续请求，也不排查询、不为每个请求再发错误包。
	if !state.last.IsZero() && now.Sub(state.last) < playerStatsGameCooldown {
		return nil
	}
	state.last = now
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	state.request = r.id
	if state.identity == identity && state.cacheOverview == overview && state.cacheGold == gold && len(state.cache) > 0 && now.Sub(state.cacheAt) < playerStatsGameCacheTTL && repo.ActivityDay(s.gameplayNow()) == state.cacheDay {
		return s.sendPlayerStatsGame(session, r, 0, state.cache)
	}
	if !state.busy.CompareAndSwap(false, true) {
		return s.sendPlayerStatsGame(session, r, 2, nil)
	}
	if !s.playerStatsQueries.acquire() {
		state.busy.Store(false)
		return s.sendPlayerStatsGame(session, r, 2, nil)
	}
	events := session.events
	go func() {
		defer state.busy.Store(false)
		defer s.playerStatsQueries.release()
		ctx, cancel := context.WithTimeout(session.ctx, 3*time.Second)
		defer cancel()
		at := s.gameplayNow()
		snap, err := s.buildPlayerActivitySnapshotVersion(ctx, identity, at, overview)
		var data []byte
		status := uint16(0)
		if err == nil {
			data, err = encodeNativePlayerActivityDetails(snap, true, overview, gold)
		}
		if err != nil {
			status = 4
			data = nil
		} else if len(data) > playerStatsGameMaxData {
			status = 5
			data = nil
		}
		if session.ctx.Err() != nil || session.loginRevoked.Load() || !events.active.Load() {
			return
		}
		// 非阻塞入队；队列满仅舍弃统计结果，客户端超时后可手动重试。
		_ = events.loop.Submit(eventloop.Event{Type: "player-stats-result", Priority: eventloop.PriorityLow, Context: session.ctx, Data: gameSessionEventTask{
			source: "player-stats-result", characterID: bound.character, characterGeneration: bound.generation, async: true,
			run: func() error {
				after, _, valid := s.playerStatsGameIdentity(session)
				if !valid || after != identity || !s.boundGameSessionCharacterCurrent(bound) || state.request != r.id {
					return nil
				}
				if status == 0 {
					state.cacheOverview = overview
					state.cacheGold = gold
					state.identity = identity
					state.cache = append([]byte(nil), data...)
					state.cacheAt = time.Now()
					state.cacheDay = repo.ActivityDay(at)
				}
				return s.sendPlayerStatsGame(session, r, status, data)
			},
		}})
	}()
	return nil
}
