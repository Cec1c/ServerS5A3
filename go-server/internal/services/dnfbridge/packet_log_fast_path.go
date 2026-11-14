package dnfbridge

// These predicates are shared with the diagnostic emitters, so disabling raw
// logging never hides the bounded application diagnostics.
func packetEventHasDiagnostic(kind string) bool {
	return kind == dungeonDeathTimingEventKind || townSelectorDiagnosticPhase(kind) != "" ||
		isTownLoadingDiagnostic(kind) ||
		isEmpireTeamSetupDiagnostic(kind) ||
		isDungeonSettlementDiagnostic(kind) ||
		isCrossSceneProfileDiagnostic(kind) ||
		isRaidPeerDiagnostic(kind)
}
