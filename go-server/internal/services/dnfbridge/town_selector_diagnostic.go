package dnfbridge

// Retain selector decisions when packet logging is disabled. The phase names
// describe the existing call sites: a pre-write event is not a successful send,
// and a returned socket write does not prove that the client displayed its UI.
func (s *Service) logTownSelectorDiagnostic(kind string, fields ...any) {
	var phase string
	switch kind {
	case "game-dungeon-select-party-context":
		phase = "entry_request"
	case "game-party-dungeon-entry-blocked":
		phase = "party_preflight_rejected"
	case "game-dungeon-select-blocked":
		phase = "entry_validation_rejected"
	case "game-raid-dungeon-selection-rejected":
		phase = "raid_map_rejected"
	case "game-enter-select-dungeon-state-send":
		phase = "before_selector_ack"
	case "game-upper-town-enter-select-origin-bound":
		phase = "context_write_returned"
	case "game-upper-town-enter-select-context-deferred":
		phase = "context_not_ready"
	case "game-upper-enter-select-dungeon-after-back-to-village-suppressed":
		phase = "request_suppressed"
	case "game-upper-enter-select-dungeon-after-town-transport-window-expired",
		"game-upper-enter-select-dungeon-after-back-to-village-window-expired":
		phase = "suppression_expired"
	case "game-raid-selector-open-rejected", "game-raid-selector-follower-rejected":
		phase = "admission_rejected"
	case "game-upper-enter-select-dungeon-completed-runtime-retire-blocked":
		phase = "completed_runtime_retire_failed"
	case "game-raid-selector-self-projection-restore-failed":
		phase = "selector_projection_failed"
	case "game-party-enter-select-member-skipped":
		phase = "follower_area_mismatch"
	case "game-party-enter-select-non-leader-initiated":
		phase = "member_initiated"
	case "game-party-enter-select-member-self-prepared":
		phase = "member_prepare_returned"
	case "game-party-enter-select-follower-deferred":
		phase = "follower_not_ready"
	case "game-party-enter-select-follower-synchronized":
		phase = "follower_sequence_returned"
	default:
		return
	}
	args := []any{"kind", kind, "phase", phase}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		// This legacy field is emitted before the failure packet is written.
		// Keep its actual meaning; a planned reply is not delivery evidence.
		if kind == "game-dungeon-select-blocked" && key == "reply_sent" {
			key = "reply_planned"
		}
		if ok && townSelectorDiagnosticFieldAllowed(key, fields[i+1]) {
			args = append(args, key, fields[i+1])
		}
	}
	s.logInfo("dnfbridge town selector diagnostic", args...)
}

// Only bounded scalar fields from the selector call sites are retained. Never
// format arbitrary values: Stringer/error implementations may contain payloads
// or credentials even when passed under an otherwise allowed field name.
func townSelectorDiagnosticFieldAllowed(key string, value any) bool {
	switch key {
	case "conn_id", "source", "reason", "response", "failure_class", "reply_suppressed_reason", "town_context_ready_reason", "source_area", "member_area":
		text, ok := value.(string)
		if !ok || len(text) > 256 {
			return false
		}
		for _, c := range text {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':' || c == '/') {
				return false
			}
		}
		return true
	case "channel_id", "char_id", "leader_char_id", "member_char_id", "party_id", "armed_age_ms", "window_ms",
		"town_id", "area_id", "source_channel", "member_channel", "packet_count",
		"dungeon_id", "difficulty", "party_leader_id", "party_member_count", "failure_reason_byte":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		}
	case "request_driven", "transport", "town_context_ready", "position_valid", "origin_bound", "prepared", "reply_planned", "is_leader", "fanout", "leader_driven",
		"session_is_character_directory_owner", "enter_select_sent", "enter_select_ack_sent", "enter_select_context_sent":
		_, ok := value.(bool)
		return ok
	}
	return false
}
