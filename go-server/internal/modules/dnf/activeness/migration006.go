package activeness

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// Exact 011 -> 006 reward-only transition. Preserve points/progress/claim receipts.
// No retroactive grant, reward reset, or wildcard acceptance of unknown catalogs.
const rewards006Fingerprint int64 = 7935271219541804260

func migrateRewards006(ch *repo.CharacterRecord, c *Catalog, now time.Time) error {
	if c.Fingerprint != rewards006Fingerprint {
		return migrateRules011(ch, c, now)
	}
	if ch.Stats[prefix+"fingerprint"] != rules011NewFingerprint && ch.Stats[prefix+"fingerprint"] != rules011OldFingerprint {
		return ErrCatalogChanged
	}
	old := *c
	old.Fingerprint = rules011NewFingerprint
	old.Rewards[0].InvitationID = 0
	old.Rewards[0].InvitationCount = 0
	prior := repo.CloneCharacter(*ch)
	if err := initState(&prior, &old, now); err != nil {
		return err
	}
	prior.Stats[prefix+"fingerprint"] = c.Fingerprint
	// initState retains a reference to this map; commit validated values in place.
	for key, value := range prior.Stats {
		ch.Stats[key] = value
	}
	return nil
}
