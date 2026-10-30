package dnfbridge

// These low-frequency scene transitions remain observable without enabling
// raw packet logging. A constructed actor and a delivered pair are distinct;
// server timings do not measure native resource loading or rendered frames.
func (s *Service) logTownLoadingDiagnostic(kind string, fields ...any) {
	switch kind {
	case "game-town-copresence-actor-state-send",
		"game-town-copresence-actor-state-delivered",
		"game-main-finish-loading-state-send",
		"game-town-set-user-area-committed",
		"game-town-presence-published":
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
		case "conn_id", "channel_id", "char_id", "selected_character_id",
			"target_char_id", "actor_char_id", "actor_channel_id", "source",
			"object_key", "remote_owner", "target_local_owner", "owner_channel",
			"town_id", "area_id", "area_instance", "same_area_peers",
			"mode0_body_len", "mode1_body_len", "state", "body_len",
			"construction_us", "send_us":
			args = append(args, key, fields[i+1])
		}
	}
	s.logInfo("dnfbridge town loading diagnostic", args...)
}
