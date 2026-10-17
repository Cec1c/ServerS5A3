package dnfbridge

// Two recorded first-floor Grief runs (2026-10-16 06:20/06:23 UTC) complete
// op46/op123 and then request op42, without starting op69/70/71. Waiting for
// ordinary cards there leaves an already committed run unable to return town.
// Grief has its own PVF marker; this does not give it Despair rewards or progress.
func currentGriefCompletedResultExitReady(runtime *runtimeDungeonState, characterID uint16) bool {
	if runtime == nil || runtime.Dungeon.Metadata.IntegerValues["tower of grief"] != 1 ||
		!completedDungeonSettlementResultReady(runtime, characterID) ||
		!runtime.clearMapCompletionPhaseAPersisted || runtime.clearMapCompletionKey == "" ||
		runtime.settlementResultPlan == nil {
		return false
	}
	// A card flow that has actually begun still owns its reward/exit barriers.
	if runtime.settlementCardScrollStateSent || runtime.settlementCardLayoutSent ||
		runtime.settlementCardSelectionSent || runtime.settlementCardRewardCommitted {
		return false
	}
	plan := runtime.settlementResultPlan
	return plan.CharacterID == characterID && plan.CompletionKey == runtime.clearMapCompletionKey && plan.Source != ""
}
