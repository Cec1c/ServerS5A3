package guild

import (
	"sort"
	"time"
)

// RankingBoardEntry joins settled scores to the current public directory.
// Metadata is current: the persisted score snapshot contains no old names.
type RankingBoardEntry struct {
	Guild *Guild
	RankingRow
}

// MonthlyRankingBoard only exposes the immediately preceding calendar month.
// It does not sample, settle an invented month, or renumber ties/deleted guilds.
func (r Registry) MonthlyRankingBoard(now time.Time, podium bool, page uint16) ([]RankingBoardEntry, error) {
	if now.IsZero() || page == 0 || podium && page != 1 {
		return nil, ErrRankingRules
	}
	if err := r.Ranking.Validate(); err != nil {
		return nil, err
	}
	if r.Ranking == nil {
		return nil, nil
	}
	month := RankingMonthStart(now).AddDate(0, -1, 0)
	var rows []RankingBoardEntry
	for _, p := range []*RankingPeriod{r.Ranking.Current, r.Ranking.Previous, r.Ranking.Older} {
		if p == nil || !p.Start.Equal(month) {
			continue
		}
		for id, row := range p.Rows {
			g := r.Guilds[id]
			if g != nil && row.Rank != 0 && (!podium || row.Rank <= 3) {
				rows = append(rows, RankingBoardEntry{Guild: g, RankingRow: row})
			}
		}
		break
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Rank != rows[j].Rank {
			return rows[i].Rank < rows[j].Rank
		}
		return rows[i].Guild.ID < rows[j].Guild.ID
	})
	if podium {
		// Native cache has one slot per rank. Keep the lowest live guild ID
		// for a tie; list pages retain every tied guild and the original rank.
		out := rows[:0]
		for _, row := range rows {
			if len(out) == 0 || out[len(out)-1].Rank != row.Rank {
				out = append(out, row)
			}
		}
		return out, nil
	}
	start := (int(page) - 1) * 5 // V171 monthly UI has five row widgets.
	if start >= len(rows) {
		return nil, nil
	}
	return rows[start:min(start+5, len(rows))], nil
}
