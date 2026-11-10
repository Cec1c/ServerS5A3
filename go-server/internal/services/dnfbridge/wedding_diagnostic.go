package dnfbridge

// Keep NPC request/result diagnostics visible with packet_log disabled. These
// fields deliberately exclude the opaque native prefix, names and accounts.
func (s *Service) currentWeddingDiagnosticFields(session *gameSession) []any {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	var connID string
	var channelID int
	var selectedCharacter, sceneReadyCharacter uint16
	var position currentTownPositionSnapshot
	if session != nil {
		connID = session.connID
		channelID = int(session.channel.ID)
		s.mu.Lock()
		selectedCharacter = session.selectedCharacterID
		s.mu.Unlock()
		// Never hold Service.mu and townMu together, or take dungeon.mu here:
		// cancellation results are also logged while holding wedding.mu.
		session.townMu.Lock()
		position = session.townPositionSnapshot
		sceneReadyCharacter = session.townSceneReadyCharacterID
		session.townMu.Unlock()
	}
	return []any{"conn_id", connID, "channel_id", channelID, "char_id", identity.character,
		"generation", identity.generation, "bound", bound, "selected_char_id", selectedCharacter,
		"town_id", position.TownID, "area_id", position.AreaID,
		"scene_ready_char_id", sceneReadyCharacter, "position_valid", position.PositionValid}
}

func (s *Service) logCurrentWeddingRequestDiagnostic(session *gameSession, bodyLen int, request currentWeddingRequest, decoded bool) {
	fields := s.currentWeddingDiagnosticFields(session)
	fields = append(fields, "event", "game-wedding-request-received", "body_len", bodyLen, "decoded", decoded)
	if decoded {
		fields = append(fields, "op", request.Operation, "target", request.Target, "kind", request.Kind, "duration", request.Duration)
	} else {
		fields = append(fields, "reason", "decode_failed")
	}
	s.logInfo("dnfbridge wedding request diagnostic", fields...)
}

func (s *Service) logCurrentWeddingCancellationDiagnostic(session *gameSession, request currentWeddingRequest, reason string, result int32, ackSent bool) {
	fields := s.currentWeddingDiagnosticFields(session)
	fields = append(fields, "event", "game-wedding-cancellation-result", "op", request.Operation,
		"target", request.Target, "kind", request.Kind, "duration", request.Duration,
		"reason", reason, "result", result, "committed", reason == "committed", "ack_sent", ackSent)
	s.logInfo("dnfbridge wedding cancellation diagnostic", fields...)
}
