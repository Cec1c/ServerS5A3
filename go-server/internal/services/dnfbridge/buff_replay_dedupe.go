package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"sync"
)

// buffReplayDedupeState is session-local. Keeping it on gameSession ties the
// entry's lifetime to the connection and avoids a global map strongly retaining
// disconnected sessions.
type buffReplayDedupeState struct {
	mu         sync.Mutex
	valid      bool
	character  uint16
	generation uint64
	accountID  string
	msgID      uint16
	body       []byte
}

// The current client's row-only 1367 consumer preserves selected skills. Use
// it only after this owner successfully received a full state with that skill;
// login, a changed skill and a new character generation still require 1368.
func (s *Service) buffReplaySkillCurrent(session *gameSession, identity buffOwnedSendIdentity, skill uint16) bool {
	if session == nil {
		return false
	}
	state := &session.buffReplayDedupe
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.valid && state.character == identity.character &&
		state.generation == identity.generation && state.accountID == identity.accountID &&
		state.msgID == currentBuffSwapSnapshotID && len(state.body) >= 3 &&
		binary.LittleEndian.Uint16(state.body) == skill
}

// buffReplayUnchanged compares against the last snapshot successfully written
// by this exact session and character generation. A reconnect creates a new
// session; a character switch changes character/generation. Neither can
// inherit another owner's cache entry.
func (s *Service) buffReplayUnchanged(session *gameSession, identity buffOwnedSendIdentity, msgID uint16, body []byte) bool {
	if session == nil {
		return false
	}
	state := &session.buffReplayDedupe
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.valid &&
		state.character == identity.character &&
		state.generation == identity.generation &&
		state.accountID == identity.accountID &&
		state.msgID == msgID &&
		bytes.Equal(state.body, body)
}

// rememberBuffReplay is called only after the socket write succeeded. A failed
// send leaves the previous entry intact, so a later move that resolves to the
// failed body is still sent rather than skipped.
func (s *Service) rememberBuffReplay(session *gameSession, identity buffOwnedSendIdentity, msgID uint16, body []byte) {
	if session == nil {
		return
	}
	state := &session.buffReplayDedupe
	state.mu.Lock()
	defer state.mu.Unlock()
	state.valid = true
	state.character = identity.character
	state.generation = identity.generation
	state.accountID = identity.accountID
	state.msgID = msgID
	state.body = append(state.body[:0], body...)
}
