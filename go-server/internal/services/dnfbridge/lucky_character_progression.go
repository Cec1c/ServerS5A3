package dnfbridge

import (
	"context"
	"strconv"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// A committed progression notification revokes the projection at the level cap,
// independently of whether reward mail is due.
// A transport failure never rolls back the already committed level or EXP.
func (s *Service) refreshCurrentLuckyCharacterAfterProgression(ctx context.Context, ch repo.CharacterRecord) {
	s.returneeLuckyMu.Lock()
	c := s.luckyCharacterCatalog
	s.returneeLuckyMu.Unlock()
	if c == nil || !c.Level1To89Only || ch.Level < 90 {
		return
	}
	id, err := strconv.ParseUint(ch.CharacterID, 10, 16)
	if err != nil {
		return
	}
	identity, found := s.onlineGameSessionCharacterSnapshot(uint16(id))
	if !found || !identity.session.luckyPreparedActive.Load() {
		return
	}
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, ch.AccountID)
	if err != nil {
		return
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	if !identity.session.luckyPreparedActive.Load() {
		return
	}
	if err := s.sendCurrentLuckyCharacterState(identity.session); err != nil {
		s.logGameEvent(identity.session, "满级幸运角色状态撤销暂缓", "character_id", ch.CharacterID, "error", err)
	}
}
