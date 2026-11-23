package dnfbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
)

// gameReadCloseErrorKind classifies a transport terminal without formatting the
// provider error. The summary proves that a socket ended; it does not diagnose
// the player-visible disconnect or claim that the role is offline.
func gameReadCloseErrorKind(err error) string {
	switch {
	case err == nil:
		return "reader_exited"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, net.ErrClosed):
		return "net_closed"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "socket_read_error"
	}
}

func gameReadCloseSuppressed(session *gameSession) bool {
	if session == nil {
		return true
	}
	if session.ctx != nil && session.ctx.Err() != nil {
		return true
	}
	return session.loginRevoked.Load()
}

// logGameReadClose keeps one bounded application-log record per game socket,
// even when raw packet logging is disabled. It deliberately excludes remote
// and local addresses, accounts, payloads and provider error text.
func (s *Service) logGameReadClose(session *gameSession, pendingBytes int, errorKind string) {
	if s == nil || gameReadCloseSuppressed(session) {
		return
	}
	characterID, generation, role, otherConnections := s.gameReadCloseCharacterBinding(session)
	if characterID == 0 {
		// This summary is only useful for a bound role. An unbound close is not
		// treated as an active-logout signal: the current protocol has no such
		// marker, so the caller reports that distinction as an evidence boundary.
		return
	}
	if errorKind == "" {
		errorKind = "reader_exited"
	}
	session.gameReadCloseOnce.Do(func() {
		args := []any{
			"kind", "game-read-close",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"game_port", session.channel.Port,
			"pending_bytes", pendingBytes,
			"error_kind", errorKind,
			"socket_scope", "connection_terminated",
			"disconnect_root_cause", false,
		}
		args = append(args,
			"character_binding", "bound",
			"char_id", characterID,
			"character_generation", generation,
			"character_session_role", role,
			"same_character_other_connections", otherConnections)
		s.logInfo("dnfbridge game read close", args...)
	})
}

func (s *Service) gameReadCloseCharacterBinding(session *gameSession) (uint16, uint64, string, int) {
	if s == nil || session == nil {
		return 0, 0, "unbound", 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	characterID := session.selectedCharacterID
	if characterID == 0 {
		return 0, 0, "unbound", 0
	}
	role := "unregistered"
	canonical := s.gameSessions[characterID]
	switch {
	case canonical == session:
		role = "canonical"
	case canonical != nil:
		role = "auxiliary"
	}
	peers := make(map[*gameSession]struct{}, 2)
	for _, other := range s.liveGameSessions {
		if other != nil && other != session && other.selectedCharacterID == characterID && other.conn != nil && !other.loginRevoked.Load() {
			peers[other] = struct{}{}
		}
	}
	if canonical != nil && canonical != session && canonical.selectedCharacterID == characterID && canonical.conn != nil && !canonical.loginRevoked.Load() {
		peers[canonical] = struct{}{}
	}
	return characterID, session.characterGeneration, role, len(peers)
}
