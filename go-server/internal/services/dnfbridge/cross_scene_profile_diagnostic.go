package dnfbridge

// Keep bounded profile delivery evidence when raw packet logging is disabled.
// Packet bodies, account information and raw error strings are excluded.
func (s *Service) logCrossSceneProfileDiagnostic(kind string, fields ...any) {
	switch kind {
	case "game-peer-userinfo-profile-refresh-sent", "game-peer-userinfo-profile-refresh-failed":
	default:
		return
	}
	args := []any{"kind", kind}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		switch key {
		case "conn_id", "channel_id", "source_char_id", "target_char_id", "owner_context", "body_len", "reason", "error_kind":
			args = append(args, key, fields[i+1])
		}
	}
	s.logInfo("dnfbridge raid peer diagnostic", args...)
}
