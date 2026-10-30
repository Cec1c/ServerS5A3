package memory

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/moderation"
	"longheng.io/server/internal/modules/dnf/repository"
	"math"
	"sort"
	"strings"
	"time"
)

func (s *memoryCharStore) AppendClear(ctx context.Context, c moderation.Clear) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.clearMu.Lock()
	defer s.clearMu.Unlock()
	if s.clears == nil {
		s.clears = map[string]moderation.Clear{}
	}
	key := c.CharacterID + "\x00" + c.Key
	if _, found := s.clears[key]; !found {
		if c.FatigueSpent != nil {
			v := *c.FatigueSpent
			c.FatigueSpent = &v
		}
		s.clears[key] = c
	}
	return nil
}
func (tx *memoryCharacterTransaction) AppendClear(ctx context.Context, c moderation.Clear) error {
	if err := repository.TransactionKeyError(ctx, tx.characterID, c.CharacterID); err != nil {
		return err
	}
	tx.clear = &c
	return nil
}
func (s *memoryCharStore) QueryClears(ctx context.Context, q moderation.Query) (moderation.ClearPage, error) {
	out := moderation.ClearPage{Rows: []moderation.ClearStats{}}
	if err := q.Normalize(time.Now()); err != nil {
		return out, err
	}
	if err := q.NormalizeSort("clears"); err != nil {
		return out, err
	}
	out.From, out.To = q.From, q.To
	s.clearMu.Lock()
	defer s.clearMu.Unlock()
	groups := map[string]*moderation.ClearStats{}
	sums := map[string]int64{}
	timed := map[string]int64{}
	hours := map[string]int64{}
	for _, c := range s.clears {
		if c.FinishedAt.Before(q.From) || !c.FinishedAt.Before(q.To) || (q.DungeonID != 0 && q.DungeonID != c.DungeonID) {
			continue
		}
		if q.Search != "" && q.Search != c.CharacterID && q.Search != c.AccountID && !strings.Contains(c.CharacterName, q.Search) && !strings.Contains(c.DungeonName, q.Search) {
			continue
		}
		key := fmt.Sprintf("%s/%d/%d", c.CharacterID, c.DungeonID, c.Difficulty)
		g := groups[key]
		if g == nil {
			g = &moderation.ClearStats{AccountID: c.AccountID, CharacterID: c.CharacterID, CharacterName: c.CharacterName, DungeonID: c.DungeonID, DungeonName: c.DungeonName, Difficulty: c.Difficulty, FastestMS: c.DurationMS}
			groups[key] = g
		}
		hour := key + c.FinishedAt.UTC().Format("2006010215")
		hours[hour]++
		g.PeakHourly = max(g.PeakHourly, hours[hour])
		g.Count++
		if c.DurationMS > 0 {
			sums[key] += c.DurationMS
			timed[key]++
			if g.FastestMS == 0 || c.DurationMS < g.FastestMS {
				g.FastestMS = c.DurationMS
			}
			g.AverageMS = int64(math.Round(float64(sums[key]) / float64(timed[key])))
		}
		if c.FatigueSpent != nil {
			if g.FatigueSpent == nil {
				v := int64(0)
				g.FatigueSpent = &v
			}
			*g.FatigueSpent += *c.FatigueSpent
			g.FatigueSamples++
			average := float64(*g.FatigueSpent) / float64(g.FatigueSamples)
			g.AverageFatigue = &average
		}
		g.CharacterName = max(g.CharacterName, c.CharacterName)
		g.DungeonName = max(g.DungeonName, c.DungeonName)
		if c.FinishedAt.After(g.LastClearAt) {
			g.LastClearAt = c.FinishedAt
		}
	}
	for _, g := range groups {
		if q.MaxDurationMS > 0 && (g.FastestMS == 0 || g.FastestMS > q.MaxDurationMS) || q.MinHourlyPeak > g.PeakHourly {
			continue
		}
		out.Rows = append(out.Rows, *g)
	}
	sort.Slice(out.Rows, func(i, j int) bool { return moderation.ClearLess(out.Rows[i], out.Rows[j], q) })
	start := min(q.Offset, len(out.Rows))
	end := min(start+q.Limit, len(out.Rows))
	out.HasMore = end < len(out.Rows)
	out.Rows = out.Rows[start:end]
	return out, ctx.Err()
}
