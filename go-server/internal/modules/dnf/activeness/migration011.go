package activeness

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

// Exact approved 017 -> 011 configuration transition. Unknown changes still
// fail closed. No player assets or existing claim receipts are altered.
const rules011OldFingerprint int64 = 7377709129766840788
const rules011NewFingerprint int64 = 6495276099522868119

func migrateRules011(ch *repo.CharacterRecord, c *Catalog, now time.Time) error {
	if c.Fingerprint != rules011NewFingerprint || ch.Stats[prefix+"fingerprint"] != rules011OldFingerprint {
		return ErrCatalogChanged
	}
	old := *c
	old.Fingerprint = rules011OldFingerprint
	old.Rewards[1].HappyTokens = 2500
	old.Rewards[2].HappyTokens = 3500
	for i := range old.Tasks {
		old.Tasks[i].Points = 20
	}
	old.Tasks[3].Points = 100
	prior := repo.CloneCharacter(*ch)
	if err := initState(&prior, &old, now); err != nil {
		return err
	}
	points := int64(0)
	for i, t := range c.Tasks {
		if ch.Stats[prefix+"done"]&(1<<i) != 0 {
			points += int64(t.Points)
		}
	}
	ch.Stats[prefix+"points"] = points
	ch.Stats[prefix+"rules011_prior_claimed"] = ch.Stats[prefix+"claimed"]
	ch.Stats[prefix+"fingerprint"] = c.Fingerprint
	return nil
}
