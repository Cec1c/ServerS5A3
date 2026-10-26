package dnfbridge

// This decision belongs to the admitted run, not the mutable current raid.
// Single Anton's item rewards use the durable raid card transaction; opening
// a second ordinary card flow can replay its animation and offer unrelated
// per-map gold. Ordinary dungeons and single Luke keep their existing policy.
func currentSingleAntonUsesRaidRewards(runtime *runtimeDungeonState) bool {
	return runtime != nil && runtime.raidEntry != nil && runtime.raidEntry.singleAntonMode
}
