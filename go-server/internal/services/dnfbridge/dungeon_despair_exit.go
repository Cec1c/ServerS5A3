package dnfbridge

// The parsed PVF marker identifies the tower. Death Tower, SAO and ordinary
// dungeons must not inherit this policy merely because of their map IDs.
func currentDungeonIsDespairTower(runtime *runtimeDungeonState) bool {
	return runtime != nil && runtime.Dungeon.Metadata.IntegerValues["tower of despair"] == 1
}

// The live current client sends op46, op123 and then op42 after a cleared
// despair floor, but never starts the ordinary op69/70/71 card handshake.
// Keep the result/statistic and durable completion barriers; only that absent
// ordinary-card dependency is excluded. This does not award tower progress.
func currentDespairCompletedResultExitReady(runtime *runtimeDungeonState, characterID uint16) bool {
	if !currentDungeonIsDespairTower(runtime) || !completedDungeonSettlementResultReady(runtime, characterID) ||
		!runtime.clearMapCompletionPhaseAPersisted || runtime.clearMapCompletionKey == "" || runtime.settlementResultPlan == nil {
		return false
	}
	plan := runtime.settlementResultPlan
	return plan.CharacterID == characterID && plan.CompletionKey == runtime.clearMapCompletionKey && plan.Source != ""
}
