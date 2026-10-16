package dnfbridge

// Shared owner/rank/receipt checks run before entering this tower-only branch.
// Retry flags are published only after each successful write, as in ordinary
// settlement. This complements the committed EXP/state packets; it does not
// remove their client-side bookkeeping or the op123 statistic handshake.
func (s *Service) sendCurrentDespairSettlementLocked(session *gameSession, runtime *runtimeDungeonState, plan *currentDungeonSettlementPacketPlan) error {
	if len(plan.DespairResultBody) < 7 {
		return errCurrentDungeonSettlementPlanShape
	}
	if !runtime.settlementDespairResultSent {
		if err := s.sendGameUpperRawClass(session, currentDespairResultMsgID, plan.DespairResultBody, 0); err != nil {
			return err
		}
		runtime.settlementDespairResultSent = true
		s.logGameEvent(session, "game-dungeon-despair-result-op348-sent", "char_id", plan.CharacterID, "dungeon_id", runtime.Dungeon.ID, "completion_key", plan.CompletionKey, "body_len", len(plan.DespairResultBody))
	}
	return nil
}
