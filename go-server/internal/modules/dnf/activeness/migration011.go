package activeness

// These historical rule snapshots validate saved states written before generic
// catalog migration existed. Their earned points are no longer reweighted.
const rules011OldFingerprint int64 = 7377709129766840788
const rules011NewFingerprint int64 = 6495276099522868119

func legacyActivityCatalog(fingerprint int64) *Catalog {
	switch fingerprint {
	case rules011OldFingerprint, rules011NewFingerprint, rewards006Fingerprint:
	default:
		return nil
	}
	c := &Catalog{Fingerprint: fingerprint,
		Tasks:   [8]Task{{1, 1, 20}, {2, 1, 20}, {3, 1, 20}, {4, 1, 100}, {5, 1, 20}, {6, 100, 20}, {7, 1, 20}, {8, 1, 20}},
		Rewards: [3]Reward{{1, 30, 500, 990027401, 1, 0, 0}, {2, 50, 2500, 990027402, 1, 3330, 500}, {3, 100, 3500, 990027403, 1, 3330, 500}},
	}
	if fingerprint != rules011OldFingerprint {
		for i, points := range []uint16{10, 10, 50, 100, 10, 20, 10, 10} {
			c.Tasks[i].Points = points
		}
		c.Rewards[1].HappyTokens = 500
		c.Rewards[2].HappyTokens = 5500
	}
	if fingerprint == rewards006Fingerprint {
		c.Rewards[0].InvitationID = 3330
		c.Rewards[0].InvitationCount = 500
	}
	return c
}
