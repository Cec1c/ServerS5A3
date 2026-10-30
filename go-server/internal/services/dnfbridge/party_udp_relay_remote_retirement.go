package dnfbridge

// Serialize the last lifecycle check with CLOSE and foreground SYNC. A copied
// renew/resync item cannot recreate a room closed since trackedRooms ran, even
// when exactly the same two characters rejoin the same lobby.
func (r *currentPartyUDPRelayRemote) sendTrackedRoom(room partyUDPRelayRemoteTrackedRoom, resync bool) {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.mu.Lock()
	current, found := r.roomMembers[room.roomID]
	if r.closed || !r.connected || !found || r.roomEpoch[room.roomID] != room.epoch || !equalPartyRelayRemoteMembers(current, room.members) {
		r.mu.Unlock()
		return
	}
	_, closing := r.closingRooms[room.roomID]
	_, renewing := r.renewals[room.roomID]
	_, resyncing := r.resyncPending[room.roomID]
	if closing || r.pending[room.roomID] != nil || renewing || resyncing {
		r.mu.Unlock()
		return
	}
	if resync {
		r.resyncPending[room.roomID] = struct{}{}
	} else {
		r.renewals[room.roomID] = append([]uint16(nil), room.members...)
	}
	r.mu.Unlock()
	_ = r.sendFrameLocked(partyRelayOpSync, buildPartyRelaySyncPayload(room.roomID, room.members))
}
