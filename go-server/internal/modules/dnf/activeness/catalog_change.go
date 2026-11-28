package activeness

import (
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math/bits"
	"strconv"
	"time"
)

const carryVersionKey = prefix + "carry_version"
const carryPointsKey = prefix + "carry_points"
const carryDoneKey = prefix + "carry_done"
const carryClaimedKey = prefix + "carry_claimed"

func earnedKey(id int) string { return prefix + "carry_earned_" + strconv.Itoa(id) }

// Keep an earned-points ledger independent of subsequent task weights. Task IDs
// are stable gameplay events; each task and each reward stage can pay once/day.
// Unknown historical catalogs have no stored rule snapshot. Validate their
// persisted shape, preserve earned facts, and never invent the missing rules.
func validateCarriedActivity(v map[string]int64) error {
	if v[carryVersionKey] != 1 || v[carryPointsKey] < 0 || v[carryPointsKey] > 65535 ||
		v[carryDoneKey] < 0 || v[carryDoneKey] > 255 || v[carryDoneKey] & ^v[prefix+"done"] != 0 ||
		v[carryClaimedKey] < 0 || v[carryClaimedKey] > 7 || v[carryClaimedKey] & ^v[prefix+"claimed"] != 0 {
		return ErrState
	}
	points := v[carryPointsKey]
	if points < int64(bits.OnesCount8(uint8(v[carryDoneKey]))) || (v[carryDoneKey] == 0 && points != 0) {
		return ErrState
	}
	for i := 1; i <= 8; i++ {
		bit := int64(1 << (i - 1))
		earned := v[earnedKey(i)]
		if v[prefix+"done"]&bit != 0 && v[carryDoneKey]&bit == 0 {
			if earned <= 0 || earned > 65535 {
				return ErrState
			}
		} else if earned != 0 {
			return ErrState
		}
		points += earned
	}
	if points != v[prefix+"points"] || points > 8*65535 {
		return ErrState
	}
	return nil
}

func validateActivityShape(v map[string]int64) error {
	if v[prefix+"fingerprint"] <= 0 || v[prefix+"points"] < 0 || (v[prefix+"claimed"] != 0 && v[prefix+"points"] == 0) ||
		v[prefix+"claimed"] < 0 || v[prefix+"claimed"] > 7 ||
		v[prefix+"done"] < 0 || v[prefix+"done"] > 255 {
		return ErrState
	}
	for i := 1; i <= 8; i++ {
		n := v[progressKey(i)]
		if n < 0 || n > 65535 || (v[prefix+"done"]&(1<<(i-1)) != 0 && n == 0) {
			return ErrState
		}
	}
	prior := v[prefix+"rules011_prior_claimed"]
	if prior < 0 || prior > 7 || prior & ^v[prefix+"claimed"] != 0 {
		return ErrState
	}
	if _, carried := v[carryVersionKey]; carried {
		return validateCarriedActivity(v)
	}
	if v[prefix+"points"] < int64(bits.OnesCount8(uint8(v[prefix+"done"]))) || v[prefix+"points"] > 65535 || (v[prefix+"done"] == 0 && v[prefix+"points"] != 0) {
		return ErrState
	}
	return nil
}

func migrateActivityCatalog(ch *repo.CharacterRecord, c *Catalog, now time.Time) error {
	// Known legacy catalogs permit semantic validation before importing their
	// earned totals. Unknown catalogs can only be structurally validated.
	candidate := repo.CloneCharacter(*ch)
	if old := legacyActivityCatalog(candidate.Stats[prefix+"fingerprint"]); old != nil {
		if err := initState(&candidate, old, now); err != nil {
			return err
		}
	}

	v := candidate.Stats
	if err := validateActivityShape(v); err != nil {
		return err
	}
	if _, carried := v[carryVersionKey]; !carried {
		v[carryVersionKey] = 1
		v[carryPointsKey] = v[prefix+"points"]
		v[carryDoneKey] = v[prefix+"done"]
		for i := 1; i <= 8; i++ {
			v[earnedKey(i)] = 0
		}
	}
	v[carryClaimedKey] = v[prefix+"claimed"]
	v[prefix+"rules011_prior_claimed"] = 0
	for i, task := range c.Tasks {
		bit := int64(1 << i)
		if v[prefix+"done"]&bit == 0 && v[progressKey(i+1)] >= int64(task.Target) {
			v[prefix+"done"] |= bit
			v[earnedKey(i+1)] = int64(task.Points)
			v[prefix+"points"] += int64(task.Points)
		}
	}
	v[prefix+"fingerprint"] = c.Fingerprint
	if err := validateCarriedActivity(v); err != nil {
		return err
	}
	for key, value := range v {
		ch.Stats[key] = value
	}
	return nil
}

func resetActivityCarry(v map[string]int64) {
	for _, key := range []string{carryVersionKey, carryPointsKey, carryDoneKey, carryClaimedKey} {
		delete(v, key)
	}
	for i := 1; i <= 8; i++ {
		delete(v, earnedKey(i))
	}
}
