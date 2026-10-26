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
		"game-dungeon-completed-town-route-committed":
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
			"boss_die_check_accepted", "op115_sent", "completion_persisted", "runtime_detached":
			args = append(args, key, fields[i+1])
		}
	}
	s.logInfo("dnfbridge dungeon settlement diagnostic", args...)
}
