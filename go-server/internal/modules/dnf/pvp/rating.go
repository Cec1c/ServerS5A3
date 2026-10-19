package pvp

import (
	"fmt"
	"math"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const (
	ExperienceKey = "pvp_experience"
	RankPointKey  = "pvp_rank_point"
	MaxRankKey    = "pvp_max_rank_point"
	GradeKey      = "pvp_grade"
)

type Weight struct{ Threshold, Value int64 }
type AdditionalWeight struct{ Threshold, Win, Loss int64 }

// RatingRules supplies the missing local server calculation. The PVF contains
// its parameters, not the original server's Elo rounding/placement algorithm.
type RatingRules struct {
	GradePoints                           [][2]int64 // loss, win
	MatchExperiencePercent                [2]int64
	ManualExperiencePercent               [2]int64
	InitialRating, InitialRP              int64
	WarmupGames, WarmupWeight             int64
	AfterWarmupBonus                      int64
	Warmup, Rating, WinStreak, LossStreak []Weight
	Additional                            []AdditionalWeight
}

func loadRatingRules(ref, matching *pvf.Document, grades int) (*RatingRules, error) {
	return loadRatingRulesWithPoints(ref, matching, grades, "pvp grade point")
}

func loadRatingRulesWithPoints(ref, matching *pvf.Document, grades int, pointTag string) (*RatingRules, error) {
	r := &RatingRules{}
	points := ref.Ints(pointTag)
	if len(points) != grades*3 {
		return nil, fmt.Errorf("PVP grade points do not cover experience grades")
	}
	for i := 0; i < len(points); i += 3 {
		if points[i] != int64(i/3) || points[i+1] <= 0 || points[i+1] > math.MaxInt32 || points[i+2] > 0 || points[i+2] < math.MinInt32 {
			return nil, fmt.Errorf("invalid PVP grade point row")
		}
		r.GradePoints = append(r.GradePoints, [2]int64{points[i+2], points[i+1]})
	}
	for _, row := range []struct {
		name string
		dst  *[2]int64
	}{
		{"pvp match exp set", &r.MatchExperiencePercent},
		{"pvp manual match etc set", &r.ManualExperiencePercent},
	} {
		v := ref.Ints(row.name)
		if len(v) != 6 || v[0] < 0 || v[0] > 10000 || v[3] < 0 || v[3] > 10000 {
			return nil, fmt.Errorf("invalid PVP experience mode %s", row.name)
		}
		*row.dst = [2]int64{v[3], v[0]}
	}
	for _, row := range []struct {
		name string
		dst  *int64
	}{
		{"initial rating point", &r.InitialRating}, {"pvp initial rating point", &r.InitialRP},
		{"warm up games", &r.WarmupGames}, {"warm up default weight", &r.WarmupWeight},
		{"first after warm up bonus", &r.AfterWarmupBonus},
	} {
		v := matching.Ints(row.name)
		if len(v) != 1 || v[0] < 0 || v[0] > math.MaxInt32 {
			return nil, fmt.Errorf("invalid PVP rating parameter %s", row.name)
		}
		*row.dst = v[0]
	}
	if r.WarmupWeight == 0 || r.WarmupWeight > 10000 || r.WarmupGames > 10000 || r.AfterWarmupBonus > 10000 {
		return nil, fmt.Errorf("invalid PVP warmup bounds")
	}
	for _, row := range []struct {
		name string
		dst  *[]Weight
	}{
		{"warm up weight", &r.Warmup}, {"rating", &r.Rating},
		{"streak win", &r.WinStreak}, {"streak lose", &r.LossStreak},
	} {
		v := matching.Ints(row.name)
		if len(v) == 0 || len(v)%2 != 0 {
			return nil, fmt.Errorf("invalid PVP weight table %s", row.name)
		}
		for i := 0; i < len(v); i += 2 {
			if v[i] < 0 || v[i] > math.MaxInt32 || v[i+1] <= 0 || v[i+1] > 10000 || i > 0 && v[i] >= v[i-2] {
				return nil, fmt.Errorf("invalid PVP weight row %s", row.name)
			}
			*row.dst = append(*row.dst, Weight{v[i], v[i+1]})
		}
	}
	if r.Rating[len(r.Rating)-1].Threshold != 0 {
		return nil, fmt.Errorf("PVP rating weights have no zero floor")
	}
	// The shipped PVF repeats this opening tag in place of its closing tag.
	// Document.Ints returns the first section's values without string rewriting.
	v := matching.Ints("addtional weight")
	if len(v) == 0 || len(v)%3 != 0 {
		return nil, fmt.Errorf("invalid PVP additional weight table")
	}
	for i := 0; i < len(v); i += 3 {
		if v[i] < 0 || v[i] > math.MaxInt32 || v[i+1] < -10000 || v[i+1] > 10000 || v[i+2] < -10000 || v[i+2] > 10000 || i > 0 && v[i] >= v[i-3] {
			return nil, fmt.Errorf("invalid PVP additional weight row")
		}
		r.Additional = append(r.Additional, AdditionalWeight{v[i], v[i+1], v[i+2]})
	}
	return r, nil
}

func weightAt(rows []Weight, value, fallback int64) int64 {
	for _, row := range rows {
		if value >= row.Threshold {
			return row.Value
		}
	}
	return fallback
}

func (c *Catalog) applyRating(stats map[string]int64, match Match, played int64) (int64, error) {
	r := c.RatingRules
	if r == nil {
		return 0, fmt.Errorf("PVP rating rules missing")
	}
	for _, key := range []string{ExperienceKey, RankPointKey, MaxRankKey, GradeKey, "pvp_max_grade", "pvp_win_streak", "pvp_loss_streak"} {
		if stats[key] < 0 || stats[key] > math.MaxInt32 {
			return 0, fmt.Errorf("invalid PVP stat %s", key)
		}
	}
	grade := stats[GradeKey]
	if grade >= int64(len(r.GradePoints)) || match.OpponentRP < 0 || match.OpponentRP > math.MaxInt32 {
		return 0, fmt.Errorf("invalid PVP grade/opponent rating")
	}
	beforeEXP, beforeRP := stats[ExperienceKey], stats[RankPointKey]
	stats["pvp_result_before_exp"], stats["pvp_result_before_rp"], stats["pvp_result_before_grade"] = beforeEXP, beforeRP, grade
	delta := int64(0)
	if !match.Draw {
		outcome := 0
		if match.Won {
			outcome = 1
		}
		// Local compatibility rule: first member of each mode tuple is a
		// percentage of the PVF grade/outcome points; truncate toward zero.
		percent := r.MatchExperiencePercent[outcome]
		if match.Manual {
			percent = r.ManualExperiencePercent[outcome]
		}
		delta = r.GradePoints[grade][outcome] * percent / 100
	}
	stats[ExperienceKey] = min(max(beforeEXP+delta, 0), int64(c.ExperienceBounds[len(c.ExperienceBounds)-1]))
	stats[GradeKey] = int64(min(len(c.ExperienceBounds)-2, int(grade)))
	for stats[GradeKey]+1 < int64(len(c.ExperienceBounds)-1) && stats[ExperienceKey] >= int64(c.ExperienceBounds[stats[GradeKey]+1]) {
		stats[GradeKey]++
	}
	for stats[GradeKey] > 0 && stats[ExperienceKey] < int64(c.ExperienceBounds[stats[GradeKey]]) {
		stats[GradeKey]--
	}
	stats["pvp_max_grade"] = max(stats["pvp_max_grade"], grade, stats[GradeKey])
	stats["pvp_result_experience_delta"] = stats[ExperienceKey] - beforeEXP
	if match.Manual {
		return stats["pvp_result_experience_delta"], nil
	}
	winStreak, lossStreak := stats["pvp_win_streak"], stats["pvp_loss_streak"]
	score := 0.5
	switch {
	case match.Draw:
		winStreak, lossStreak = 0, 0
	case match.Won:
		winStreak, lossStreak, score = min(winStreak+1, math.MaxInt32), 0, 1
	default:
		winStreak, lossStreak, score = 0, min(lossStreak+1, math.MaxInt32), 0
	}
	stats["pvp_win_streak"], stats["pvp_loss_streak"] = winStreak, lossStreak
	self, opponent := beforeRP, match.OpponentRP
	// A zero RP from the old server is unseeded. First settlement seeds the
	// PVF initial Elo; both players use their frozen pre-settlement RP.
	if self == r.InitialRP && stats["pvp_rating_initialized"] == 0 {
		self = r.InitialRating
	}
	if opponent == r.InitialRP && !match.OpponentRated {
		opponent = r.InitialRating
	}
	k := weightAt(r.Rating, self, 0)
	if played < r.WarmupGames {
		k = weightAt(r.Warmup, played, r.WarmupWeight)
	}
	multiplier := int64(100)
	if match.Won && !match.Draw {
		multiplier = weightAt(r.WinStreak, winStreak, 100)
	}
	if !match.Won && !match.Draw {
		multiplier = weightAt(r.LossStreak, lossStreak, 100)
	}
	expected := 1 / (1 + math.Pow(10, float64(opponent-self)/400))
	ratingDelta := int64(math.Round(float64(k) * float64(multiplier) / 100 * (score - expected)))
	if !match.Draw {
		for _, row := range r.Additional {
			if self >= row.Threshold {
				if match.Won {
					ratingDelta += row.Win
				} else {
					ratingDelta += row.Loss
				}
				break
			}
		}
		if match.Won && played == r.WarmupGames {
			ratingDelta += r.AfterWarmupBonus
		}
		if match.Won {
			ratingDelta = max(ratingDelta, 0)
		} else {
			ratingDelta = min(ratingDelta, 0)
		}
	}
	stats[RankPointKey] = min(max(self+ratingDelta, 0), math.MaxInt32)
	stats["pvp_rating_initialized"] = 1
	stats[MaxRankKey] = max(stats[MaxRankKey], beforeRP, stats[RankPointKey])
	return stats["pvp_result_experience_delta"], nil
}
