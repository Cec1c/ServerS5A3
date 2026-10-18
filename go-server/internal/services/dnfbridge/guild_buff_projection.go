package dnfbridge

import (
	"math"
	"strconv"
	"sync"

	"longheng.io/server/internal/modules/dnf/guild"
)

type guildBuffProjection struct {
	mu          sync.Mutex
	characterID uint16
	buffs       [2]guild.Benefit
}

// V110 class0/487 reads count:u8 and (index,param0,param1,aux):4*u32.
// Guild_Status / Guild_Status1 use expire_time [0, remaining seconds].
// Renewal must remove/recreate: 1B52BE0 changes parameters but not the old tick.
// The caller owns the selected scene/session; no background writer is created.
func (s *Service) writeCurrentGuildBuffProjection(session *gameSession, g *guild.Guild, force bool) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	now := s.gameplayNow()
	var desired [2]guild.Benefit
	if g != nil {
		if m, ok := g.Members[strconv.Itoa(int(session.selectedCharacterID))]; ok && m.AccountID == s.accountIDForSession(session) {
			for i, kind := range []byte{3, 5} {
				if b := g.Benefits[kind]; b.Active(now) {
					desired[i] = b
				}
			}
		} else {
			g = nil
		}
	}
	if err := s.scheduleCurrentGuildBenefitExpiry(session, g, now); err != nil {
		return err
	}
	state := &session.guildBuffs
	state.mu.Lock()
	defer state.mu.Unlock()
	changed := force || state.characterID != session.selectedCharacterID
	for i := range desired {
		if desired[i].Value != state.buffs[i].Value || !desired[i].ExpiresAt.Equal(state.buffs[i].ExpiresAt) {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	// Both share icon 206. Remove all previous instances first, then add both.
	for i, old := range state.buffs {
		if old.Value == 0 {
			continue
		}
		var body packetWriter
		body.writeByte(1)
		body.writeUint32(old.Value)
		if err := s.sendGameUpperRawClass(session, 488, body.bytes(), 0); err != nil {
			return err
		}
		state.buffs[i] = guild.Benefit{}
	}
	for i, b := range desired {
		if b.Value == 0 {
			continue
		}
		seconds := uint32(0)
		if !b.ExpiresAt.IsZero() {
			seconds = uint32(math.Ceil(b.ExpiresAt.Sub(now).Seconds()))
		}
		var body packetWriter
		body.writeByte(1)
		body.writeUint32(b.Value)
		body.writeUint32(0)
		body.writeUint32(seconds)
		body.writeUint32(0)
		if err := s.sendGameUpperRawClass(session, 487, body.bytes(), 0); err != nil {
			return err
		}
		state.buffs[i] = b
	}
	state.characterID = session.selectedCharacterID
	return nil
}

// Rebind after op24 has replaced the selected native actor. The town cursor
// invokes this once after its own successful state/skill reconstruction.
func (s *Service) restoreCurrentGuildBuffProjection(session *gameSession) error {
	if repos, ok := s.repositoryGroup(); !ok || repos.Settings == nil || repos.Character == nil {
		return nil
	}
	g, err := s.currentGuildForSelectedCharacter(session)
	if err != nil {
		return err
	}
	return s.writeCurrentGuildBuffProjection(session, g, true)
}
