package dnfbridge

// Automatic reveals used to select card zero for every session. Reserve before
// sending the ACK so concurrent member queues cannot overwrite the same card.
func currentDungeonAutomaticCardIndex(session *gameSession, runtime *runtimeDungeonState) byte {
	if runtime != nil && runtime.settlementCardSideSelectionKnown[dungeonCardSideFree] {
		return runtime.settlementCardSideMember[dungeonCardSideFree]
	}
	if runtime == nil || runtime.partyInstance == nil || !dungeonPartyCardFlipProjectionModeFromEnv().projects() {
		return 0
	}
	return currentDungeonCardFlipRowStateValue(session)
}

func reserveCurrentDungeonPartyCard(instance *dungeonPartyInstance, side dungeonCardSide, preferred byte, characterID uint16) (byte, bool) {
	if side >= dungeonCardSideCount || preferred >= dungeonCardSlotsPerSide || characterID == 0 {
		return preferred, false
	}
	if instance == nil || !dungeonPartyCardFlipProjectionModeFromEnv().projects() {
		return preferred, true
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	for index, owner := range instance.cardReservations[side] {
		if owner == characterID {
			return byte(index), true
		}
	}
	for step := byte(0); step < dungeonCardSlotsPerSide; step++ {
		index := (preferred + step) % dungeonCardSlotsPerSide
		if instance.cardReservations[side][index] == 0 &&
			(!instance.cardFlipKnown[side][index] || instance.cardFlipOwner[side][index] == characterID) {
			instance.cardReservations[side][index] = characterID
			return index, true
		}
	}
	return preferred, false
}
