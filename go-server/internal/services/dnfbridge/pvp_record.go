package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"

	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// handleCurrentPVPRecord serves the native Fair/PVP2Record.xui query (375).
// The client only enables loss/draw controls when the full-detail byte is 1.
func (s *Service) handleCurrentPVPRecord(session *gameSession, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 || len(body) != 8 {
		return nil
	}
	targetID, self, view, slot := binary.LittleEndian.Uint16(body), body[2], body[3], binary.LittleEndian.Uint32(body[4:])
	if targetID == 0 || self > 1 || view < 2 || view > 5 || (slot > 2 && slot != math.MaxUint32) ||
		(self == 1) != (targetID == session.selectedCharacterID) {
		return s.sendGameUpperFailure(session, 375, 1)
	}
	targetSession := session
	if targetID != session.selectedCharacterID {
		if s.onlinePlayers == nil || !s.onlinePlayers.PeerInSameArea(session.selectedCharacterID, targetID) {
			return s.sendGameUpperFailure(session, 375, 1)
		}
		var online bool
		targetSession, online = s.onlineGameSession(targetID)
		if !online {
			return s.sendGameUpperFailure(session, 375, 1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	loadedID, _, character, found := s.characterForEnter(ctx, targetSession, targetID)
	if !found || loadedID != targetID || targetSession.selectedCharacterID != targetID || character.AccountID != targetSession.accountID {
		return s.sendGameUpperFailure(session, 375, 1)
	}
	response := buildCurrentPVPRecord(targetID, view, character)
	s.logGameEvent(session, "game-pvp-record-sent", "target_id", targetID, "view", view,
		"wins", statU32(character, dnfpvp.WinsKey, 0), "losses", statU32(character, dnfpvp.LossesKey, 0))
	return s.sendGameUpperSuccess(session, 375, response)
}

// sub_3449C40 reads this 62-byte body after the success byte. UI control5110
// is the current-season 1:1 record; previous seasons have no saved history.
func buildCurrentPVPRecord(targetID uint16, view byte, character dnfrepo.CharacterRecord) []byte {
	var wins, losses, draws, streak, maximum uint32
	var grade byte
	var recent [3]byte
	if view == 5 {
		wins, losses, draws = statU32(character, dnfpvp.WinsKey, 0), statU32(character, dnfpvp.LossesKey, 0), statU32(character, dnfpvp.DrawsKey, 0)
		streak = statU32(character, "pvp_win_streak", 0)
		maximum = max(streak, statU32(character, dnfpvp.MaxWinStreakKey, 0))
		grade = byte(min(statU32(character, dnfpvp.GradeKey, 0), 255))
		for results, i := statU32(character, dnfpvp.RecentResultsKey, 0), 0; i < 10; results, i = results>>2, i+1 {
			if outcome := results & 3; outcome != 0 {
				recent[outcome-1]++
			}
		}
	}
	var w packetWriter
	w.writeUint16(targetID)
	w.writeByte(view)
	w.writeByte(1)           // full detail enables losses and draws
	w.writeByte(grade)       // state+88, rank icon via sub_26943B0
	w.writeByte(0)           // state+92, previous team-mode grade
	w.writeUint32(wins)      // state+4, 1:1 wins
	w.writeUint32(0)         // state+16, team-mode wins
	w.writeBytes(recent[:])  // state+40/44/48
	w.writeUint32(streak)    // state+52
	w.writeUint32(0)         // state+60
	w.writeUint32(0)         // state+64
	w.writeUint32(maximum)   // state+56
	w.writeUint32(losses)    // state+8
	w.writeUint32(draws)     // state+12
	for i := 0; i < 5; i++ { // state+20/24/28/32/36, unrecorded modes
		w.writeUint32(0)
	}
	w.writeByte(0) // no fabricated per-opponent history
	return w.bytes()
}
