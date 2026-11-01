package dnfbridge

import (
	"context"
	"encoding/binary"
	bp "longheng.io/server/internal/modules/dnf/battlepass"
	"longheng.io/server/internal/platform/eventloop"
	"time"
)

// BPE1 请求12字节；BPN1 回包20字节头+4字节标志。沿用0x7f01内部连接。
func decodeBattlePassEntry(body []byte) (gamePlayerStatsRequest, bool) {
	if len(body) != 12 || string(body[:4]) != "BPE1" {
		return gamePlayerStatsRequest{}, false
	}
	r := gamePlayerStatsRequest{binary.LittleEndian.Uint32(body[4:]), binary.LittleEndian.Uint32(body[8:])}
	return r, r.id != 0 && r.epoch != 0
}
func (s *Service) sendBattlePassEntry(session *gameSession, r gamePlayerStatsRequest, status uint16, flags byte) error {
	b := encodeGamePlayerStatsReply(r, status, []byte{flags, 0, 0, 0})
	copy(b, "BPN1")
	return s.sendGameUpperFixed16Transport(session, battlePassGameOpcode, b, 1, 0, false, "battlepass_entry_fixed16_plain")
}
func (s *Service) readBattlePassEntry(ctx context.Context, identity localPlayerIdentity) (byte, error) {
	if !s.currentBattlePassEntryAvailable() {
		return 0, nil
	}
	c, fingerprint, e := s.currentBattlePassCatalog()
	if e != nil {
		return 0, e
	}
	policy, e := currentBattlePassPolicy()
	if e != nil {
		return 0, e
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Account == nil || g.Character == nil {
		return 0, bp.ErrUnavailable
	}
	a, found, e := g.Account.Load(ctx, identity.AccountID)
	if e != nil {
		return 0, e
	}
	if !found {
		return 0, bp.ErrUnavailable
	}
	now := s.gameplayNow()
	counters, e := loadBattlePassCounters(ctx, g, identity.AccountID, policy, now)
	if e != nil {
		return 0, e
	}
	v, e := bp.Inspect(c, policy, a, bp.Command{AccountID: identity.AccountID, Now: now, Counters: counters, Fingerprint: fingerprint})
	if e != nil {
		return 0, e
	}
	flags := byte(1)
	if bp.CanClaim(c, v) {
		flags |= 2
	}
	return flags, nil
}
func (s *Service) handleBattlePassEntry(session *gameSession, body []byte) error {
	r, ok := decodeBattlePassEntry(body)
	if !ok || session == nil {
		return nil
	}
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	state := &session.battlePassEntry
	now := time.Now()
	if !state.last.IsZero() && now.Sub(state.last) < 5*time.Second {
		return s.sendBattlePassEntry(session, r, 2, 0)
	}
	state.last, state.request = now, r.id
	if !state.busy.CompareAndSwap(false, true) {
		return s.sendBattlePassEntry(session, r, 2, 0)
	}
	if !s.playerStatsQueries.acquire() {
		state.busy.Store(false)
		return s.sendBattlePassEntry(session, r, 2, 0)
	}
	events := session.events
	go func() {
		defer state.busy.Store(false)
		defer s.playerStatsQueries.release()
		ctx, cancel := context.WithTimeout(session.ctx, 5*time.Second)
		defer cancel()
		flags, e := s.readBattlePassEntry(ctx, identity)
		status := uint16(0)
		if e != nil {
			status = 4
			flags = 0
		}
		if session.ctx.Err() != nil || session.loginRevoked.Load() || !events.active.Load() {
			return
		}
		_ = events.loop.Submit(eventloop.Event{Type: "battlepass-entry", Priority: eventloop.PriorityLow, Context: session.ctx, Data: gameSessionEventTask{source: "battlepass-entry", characterID: bound.character, characterGeneration: bound.generation, async: true, run: func() error {
			after, _, valid := s.playerStatsGameIdentity(session)
			if !valid || after != identity || !s.boundGameSessionCharacterCurrent(bound) || state.request != r.id {
				return nil
			}
			return s.sendBattlePassEntry(session, r, status, flags)
		}}})
	}()
	return nil
}
