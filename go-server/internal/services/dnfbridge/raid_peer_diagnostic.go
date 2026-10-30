package dnfbridge

// Preserve bounded model/barrier delivery evidence when raw packet logging is
// disabled. Deliberately exclude packet bodies, account data and error strings.
func (s *Service) logRaidPeerDiagnostic(kind string, fields ...any) {
	switch kind {
	case "game-dungeon-party-peer-actor-delivered",
		"game-peer-userinfo-profile-refresh-sent",
		"game-peer-userinfo-profile-refresh-failed",
		"game-dungeon-party-peer-actor-send",
		"game-current-party-frame-op9-send",
		"game-current-party-frame-op9-remove-send",
		"game-party-dungeon-peer-actor-landing-resend",
		"game-dungeon-party-peer-actor-resend",
		"game-raid-volcano-barrier-symbols-sent",
		"game-fiendwar-party-admission",
		"game-party-dungeon-loading-ready",
		"game-party-dungeon-loading-released",
		"game-party-dungeon-post-start-map-placement-deferred",
		"game-party-dungeon-post-start-map-placement-released",
		"game-party-dungeon-post-start-map-placement-skipped",
		"game-party-peer-endpoint-registration-rejected",
		"party-udp-relay-silent-peer-kick",
		"party-udp-relay-silent-peer-kick-failed":
	default:
		return
	}
	args := []any{"kind", kind}
	loadingFailed := false
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		if kind == "game-peer-userinfo-profile-refresh-sent" || kind == "game-peer-userinfo-profile-refresh-failed" {
			switch key {
			case "conn_id", "channel_id", "source_char_id", "target_char_id", "owner_context", "body_len", "reason", "error_kind":
				args = append(args, key, fields[i+1])
			}
			continue
		}
		// These events occur at different points: placement-released is before
		// its send call; only loading-released records a completed release call.
		if isPartyDungeonLoadingDiagnosticKind(kind) {
			if key == "error" {
				loadingFailed = true
				continue
			}
			switch key {
			case "conn_id", "channel_id", "char_id", "party_id",
				"slot", "local_slot", "cycle", "member_count", "first", "release",
				"trigger", "release_tail", "source", "deferred_source", "phase":
				args = append(args, key, fields[i+1])
			}
			continue
		}
		if kind == "game-party-peer-endpoint-registration-rejected" ||
			kind == "party-udp-relay-silent-peer-kick" || kind == "party-udp-relay-silent-peer-kick-failed" {
			switch key {
			case "conn_id", "channel_id", "char_id", "party_id", "member_id", "body_len",
				"ordering", "formation_order", "after_roster", "source", "reason":
				args = append(args, key, fields[i+1])
			}
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
			"mode0_digest", "mode1_digest", "body_digest", "mode0_object_key", "mode1_object_key",
			"mode0_route", "mode0_owner", "mode1_owner", "mode0_body_len", "mode1_body_len",
			"appearance_rows", "appearance_empty_rows", "appearance_link_rows", "mode1_create_rows",
			"peer_finish_state_sent", "peer_finish_completion_sent", "peer_post_finish_sent", "peer_run_key_ready",
			"peer_run_key_party_id", "peer_run_key_map_id", "peer_run_key_seed",
			"object_key", "owner_channel", "party_id", "slot_count", "party_leader_slot", "slot_zero_char_id", "leader_char_id",
			"actor_generation", "receiver_generation", "source", "decision", "delivered", "peer_scene_stable",
			"instance_id", "phase", "projection_version", "projection_fresh",
			"symbol_cursor_before", "symbol_cursor_after", "zero_write_index", "break_write_index",
			"recover_write_index", "native_open_write_index", "native_closed_write_index",
			"barrier_current", "native_open_current", "resent_peer_char_ids",
			"run_key_party_id", "run_key_dungeon_id", "run_key_map_id", "run_key_seed":
			args = append(args, key, fields[i+1])
		}
	}
	switch kind {
	case "game-party-dungeon-loading-ready":
		args = append(args, "event_phase", "member_ready")
	case "game-party-dungeon-loading-released":
		args = append(args, "event_phase", "release_call_returned", "call_ok", !loadingFailed)
	case "game-party-dungeon-post-start-map-placement-deferred":
		args = append(args, "event_phase", "placement_deferred")
	case "game-party-dungeon-post-start-map-placement-released":
		args = append(args, "event_phase", "before_placement_send")
	case "game-party-dungeon-post-start-map-placement-skipped":
		args = append(args, "event_phase", "placement_skipped")
	}
	s.logInfo("dnfbridge raid peer diagnostic", args...)
}

func isPartyDungeonLoadingDiagnosticKind(kind string) bool {
	switch kind {
	case "game-party-dungeon-loading-ready",
		"game-party-dungeon-loading-released",
		"game-party-dungeon-post-start-map-placement-deferred",
		"game-party-dungeon-post-start-map-placement-released",
		"game-party-dungeon-post-start-map-placement-skipped":
		return true
	default:
		return false
	}
}
