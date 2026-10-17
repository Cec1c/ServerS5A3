// Package experiencebuff owns timed PVF experience bonuses. Clear experience
// and quest experience have separate ledgers; neither changes monster rewards.
package experiencebuff

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	Clear = "clear"
	Quest = "quest"
	scale = int64(10000)
)

var ErrEffect = errors.New("invalid experience potion effect")
var ErrCooldown = errors.New("experience potion is on cooldown")

type Effect struct {
	Kind           string
	BasisPoints    int64
	DurationMillis int64
	CooldownMillis int64
	MinimumLevel   int
	MaximumLevel   int
	Jobs           []int
}

// Parse uses the numeric PVF bonus, not translated names or tooltip multipliers.
// For example [exp bonus rate] 2.00 means base + 200%, i.e. triple EXP.
func Parse(doc *pvf.Document) (Effect, error) {
	var effect Effect
	for _, entry := range []struct{ tag, kind string }{{"exp bonus rate", Clear}, {"quest exp bonus rate", Quest}} {
		tokens, found := doc.Section(entry.tag)
		if !found {
			continue
		}
		if effect.Kind != "" || len(tokens) != 1 {
			return Effect{}, ErrEffect
		}
		var rate float64
		switch tokens[0].Kind {
		case pvf.TokenFloat:
			rate = tokens[0].Float
		case pvf.TokenInt:
			rate = float64(tokens[0].Int)
		default:
			return Effect{}, ErrEffect
		}
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > 100 {
			return Effect{}, ErrEffect
		}
		effect.Kind = entry.kind
		effect.BasisPoints = int64(math.Round(rate * float64(scale)))
	}
	if effect.Kind == "" {
		return effect, nil
	}
	effect.DurationMillis, _ = doc.Int("stat change duration")
	effect.CooldownMillis, _ = doc.Int("cool time")
	minLevel, _ := doc.Int("minimum level")
	effect.MinimumLevel = int(minLevel)
	maxLevel, _ := doc.Int("maximum level")
	effect.MaximumLevel = int(maxLevel)
	for _, tag := range doc.Texts("usable job") {
		if strings.EqualFold(strings.TrimSpace(tag), "[all]") {
			effect.Jobs = nil
			break
		}
		matched := false
		for job := 0; job <= jobmap.MaxID; job++ {
			if jobmap.MatchesQuestTag(tag, job) {
				effect.Jobs = append(effect.Jobs, job)
				matched = true
			}
		}
		if !matched {
			return Effect{}, ErrEffect
		}
	}
	if effect.DurationMillis <= 0 || effect.DurationMillis > int64(30*24*time.Hour/time.Millisecond) || effect.CooldownMillis < 0 || effect.CooldownMillis > int64(30*24*time.Hour/time.Millisecond) || effect.BasisPoints <= 0 {
		return Effect{}, ErrEffect
	}
	return effect, nil
}

func prefix(kind string) string { return "experience_potion_" + kind + "_" }

// Record runs inside the same transaction as consumption. One family cannot
// stack with itself or bypass its shared cooldown by using another item ID.
func Record(character *repo.CharacterRecord, effect Effect, itemID int64, now time.Time) error {
	if character == nil || (effect.Kind != Clear && effect.Kind != Quest) || effect.BasisPoints <= 0 || effect.BasisPoints > 100*scale || effect.DurationMillis <= 0 || effect.DurationMillis > int64(30*24*time.Hour/time.Millisecond) || effect.CooldownMillis < 0 || effect.CooldownMillis > int64(30*24*time.Hour/time.Millisecond) || now.IsZero() || itemID <= 0 {
		return ErrEffect
	}
	if character.Level < effect.MinimumLevel || (effect.MaximumLevel > 0 && character.Level > effect.MaximumLevel) {
		return ErrEffect
	}
	if len(effect.Jobs) > 0 {
		job, err := strconv.Atoi(character.Job)
		allowed := false
		for _, candidate := range effect.Jobs {
			allowed = allowed || candidate == job
		}
		if err != nil || !allowed {
			return ErrEffect
		}
	}
	p := prefix(effect.Kind)
	if character.Stats[p+"cooldown_until_ms"] > now.UnixMilli() {
		return ErrCooldown
	}
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	character.Stats[p+"basis_points"] = effect.BasisPoints
	character.Stats[p+"started_ms"] = now.UnixMilli()
	character.Stats[p+"until_ms"] = now.UnixMilli() + effect.DurationMillis
	character.Stats[p+"cooldown_until_ms"] = now.UnixMilli() + effect.CooldownMillis
	character.Stats[p+"item_id"] = itemID
	return nil
}

func Bonus(character repo.CharacterRecord, kind string, base uint32, now time.Time) uint32 {
	if (kind != Clear && kind != Quest) || now.IsZero() {
		return 0
	}
	p := prefix(kind)
	rate := character.Stats[p+"basis_points"]
	if character.Stats[p+"started_ms"] > now.UnixMilli() || character.Stats[p+"until_ms"] <= now.UnixMilli() || rate <= 0 || rate > 100*scale {
		return 0
	}
	bonus := uint64(base) * uint64(rate) / uint64(scale)
	return uint32(min(bonus, uint64(math.MaxUint32-base)))
}
