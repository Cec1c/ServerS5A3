package dnfbridge

// Keep a small result/return trace available on deployments that disable raw
// packet logging. No packet body, account credential, or arbitrary error value
// is forwarded to the application logger.
func (s *Service) logDungeonSettlementDiagnostic(kind string, fields ...any) {
	switch kind {
	case "game-dungeon-settlement-entry-blocked",
		"game-dungeon-settlement-entry-op31-send",
		"game-dungeon-party-instance-peer-final-room-deferred",
		"game-dungeon-giveup-game-blocked",
		"game-dungeon-card-exit-blocked",
		"game-dungeon-card-exit-return-blocked",
		"game-dungeon-back-to-village-request-received",
		"game-dungeon-back-to-village-blocked",
		"game-dungeon-completed-town-route-committed",
		"game-dungeon-select-back-to-village-committed",
		"game-luke-coin-validation",
		"game-dungeon-use-coin-blocked",
		"game-dungeon-use-coin-success":
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
		case "conn_id", "channel_id", "char_id", "runtime_char_id", "peer_char_id",
			"dungeon_id", "object_key", "instance_id", "request_msg_id", "source", "reason",
			"run_status", "settlement_entry_sent", "settlement_play_result_received",
			"settlement_statistic_received", "settlement_clear_reward_sent", "settlement_phase",
			"boss_die_check_accepted", "op115_sent", "completion_persisted", "runtime_detached",
			"has_runtime", "request_value_a", "request_value_b", "body_len", "run_id",
			"phase", "entry_phase", "entry_attempt", "phase_coin_used", "phase_coin_limit",
			"map_coin_used", "map_coin_limit", "target_object_key", "expected_object_key",
			"town_id", "area_id", "route_stage", "scene_tail_deferred", "town_return_pending",
			"enter_select_sent", "enter_select_ack_sent", "enter_select_context_sent", "party_id":
			args = append(args, key, fields[i+1])
		}
	}
	s.logInfo("dnfbridge dungeon settlement diagnostic", args...)
}
