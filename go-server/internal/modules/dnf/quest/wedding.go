package quest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// Wedding eligibility is derived from both persisted owners; callers cannot
// enable this quest family by passing a client-controlled phase or partner ID.
func WithWeddingEligibility(ctx context.Context, chars r.CharacterRepository, c r.CharacterRecord, e CharacterEligibility) (CharacterEligibility, error) {
	e.weddingPartner = ""
	state, err := wedding.Read(c)
	if err != nil || state.Phase != wedding.Engaged || chars == nil {
		return e, nil
	}
	id := strconv.FormatInt(state.PartnerID, 10)
	partner, found, err := chars.Load(ctx, id)
	if err != nil {
		return e, err
	}
	if found && partner.CharacterID == id {
		if _, _, err = wedding.ValidatePair(c, partner); err == nil {
			e.weddingPartner = id
		}
	}
	return e, nil
}

func weddingPreparationDefinition(d Definition) bool {
	return normalizeQuestTag(d.Grade) == "wedding" && (d.ID == 20737 || d.ID == 20738)
}

// The current PVF's second preparation quest has an empty acceptance-item
// section. It grants no asset on accept; collection and reward stay in the
// ordinary atomic finish owner. Other quests and populated/malformed sections
// still require the event-item acceptance transaction.
func weddingEmptyAcceptanceItems(d Definition) bool {
	return d.ID == 20738 && weddingPreparationDefinition(d) &&
		normalizeQuestTag(d.Type) == "seeking" && d.emptyDependGiveItem
}

func weddingClearDefinition(d Definition) bool {
	v := d.IntData
	return weddingPreparationDefinition(d) && normalizeQuestTag(d.Type) == "condition under clear" && d.SubType == 22 &&
		len(v) == 3 && v[0] == -1 && v[1] == -1 && v[2] > 0 && v[2] <= 511
}

// Quest metadata keys are VARCHAR(128). Native run receipts can exceed that
// limit, so hash the complete receipt instead of truncating it. The original
// receipt remains unchanged in the completion_key LONGTEXT value.
func weddingClearRunMarkerKey(receipt string) string {
	digest := sha256.Sum256([]byte(receipt))
	return "wedding_clear_run_v2:" + hex.EncodeToString(digest[:])
}

func weddingClearRunCounted(extra map[string]string, receipt string) bool {
	// Older deployments could persist short raw-key receipts. Keep their
	// replay fence without writing any new unbounded metadata key.
	return extra[weddingClearRunMarkerKey(receipt)] == "1" || extra["wedding_clear_run:"+receipt] == "1"
}
