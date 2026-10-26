package dnfbridge

// Preserve bounded model/barrier delivery evidence when raw packet logging is
// disabled. Deliberately exclude packet bodies, account data and error strings.
func (s *Service) logRaidPeerDiagnostic(kind string, fields ...any) {
	switch kind {
	case "game-dungeon-party-peer-actor-delivered",
		"game-party-dungeon-peer-actor-landing-resend",
		"game-dungeon-party-peer-actor-resend",
		"game-raid-volcano-barrier-symbols-sent",
		"game-fiendwar-party-admission":
	default:
		return
	}
	args := []any{"kind", kind}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		if kind == "game-fiendwar-party-admission" {
			switch key {
			case "phase", "char_id", "peer_char_id", "mode", "reason", "party_id", "peer_party_id", "party_members", "peer_party_members",
				"raid_key", "peer_raid_key", "raid_instance_id", "peer_raid_instance_id",
				"run_id", "peer_run_id", "participants", "peer_participants",
				"scene_key", "peer_scene_key", "run_key", "peer_run_key",
				"post_start", "peer_post_start", "finish", "peer_finish",
				"post_finish", "peer_post_finish", "ready", "peer_ready",
				"party_instance_id", "peer_party_instance_id", "had_invite":
				args = append(args, key, fields[i+1])
			}
			continue
		}
		switch key {
		case "conn_id", "channel_id", "char_id", "peer_char_id", "receiver_char_id", "actor_char_id",
			"actor_generation", "receiver_generation", "source", "decision", "delivered", "peer_scene_stable",
			"instance_id", "phase", "projection_version", "projection_fresh",
			"symbol_cursor_before", "symbol_cursor_after", "zero_write_index", "break_write_index",
			"recover_write_index", "native_open_write_index", "native_closed_write_index",
			"barrier_current", "native_open_current", "resent_peer_char_ids",
			"run_key_party_id", "run_key_dungeon_id", "run_key_map_id", "run_key_seed":
			args = append(args, key, fields[i+1])
		}
	}
	s.logInfo("dnfbridge raid peer diagnostic", args...)
}
