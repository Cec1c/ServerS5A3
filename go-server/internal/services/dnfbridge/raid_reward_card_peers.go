package dnfbridge

// A normal raid admission can put four independently travelling players in
// the same planned group. They each own a one-card native party page, so one
// owner's card must not consume the next solo owner's first slot. Legacy
// synthetic flows and Fiend War retain their existing frozen-group rule.
func currentAntonRewardCardPeer(flow *currentAntonRewardFlow, owner, other uint16, otherGroup, ownerGroup byte) bool {
	if flow != nil && flow.PartyCardPeers != nil {
		for _, id := range flow.PartyCardPeers[owner] {
			if id == other {
				return true
			}
		}
		return false
	}
	return otherGroup == ownerGroup
}
