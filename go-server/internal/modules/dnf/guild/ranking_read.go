package guild

import (
	"context"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// RankingReadView exposes computed values from one exact source version. No
// mutable registry, member history or ranking map escapes to packet handlers.
type RankingReadView struct{ hall *HallReadView }

func ReadRankingView(ctx context.Context, settings r.SettingsRepository) (*RankingReadView, error) {
	view, err := ReadHallView(ctx, settings)
	if err != nil {
		return nil, err
	}
	return &RankingReadView{hall: view}, nil
}

func (view *RankingReadView) Rows(owner r.GuildOwner, now time.Time, maxAge time.Duration) ([3]RankingRow, error) {
	var empty [3]RankingRow
	if view == nil || view.hall == nil {
		return empty, ErrNotMember
	}
	g := view.hall.owned(owner)
	if g == nil {
		return empty, ErrNotMember
	}
	state := view.hall.snapshot.registry.Ranking
	if state == nil || state.Current == nil || state.Current.SampledAt.After(now) ||
		!state.Current.Start.Equal(RankingMonthStart(now)) || now.Sub(state.Current.SampledAt) >= maxAge {
		return empty, ErrRankingSnapshotChanged
	}
	if err := state.Validate(); err != nil {
		return empty, err
	}
	return state.View(g.ID, now), nil
}

// RankingBoardProjection carries just the fields consumed by native CMD1504.
type RankingBoardProjection struct {
	ID          uint32
	Name        string
	MasterName  string
	Mark        byte
	MemberCount int
	RankingRow
}

func (view *RankingReadView) MonthlyBoard(now time.Time, podium bool, page uint16) ([]RankingBoardProjection, error) {
	if view == nil || view.hall == nil || view.hall.snapshot == nil {
		return nil, ErrRepositoryUnavailable
	}
	rows, err := view.hall.snapshot.registry.MonthlyRankingBoard(now, podium, page)
	if err != nil {
		return nil, err
	}
	result := make([]RankingBoardProjection, len(rows))
	for i, row := range rows {
		g := row.Guild
		result[i] = RankingBoardProjection{ID: g.ID, Name: g.Name, MasterName: g.MasterName, Mark: g.Mark, MemberCount: len(g.Members), RankingRow: row.RankingRow}
	}
	return result, nil
}

func (view *RankingReadView) MemberPoints(owner r.GuildOwner, guildID uint32, serverGroup byte, name string, rules *MemberRankingRules, now time.Time) ([3]float32, error) {
	var empty [3]float32
	if view == nil || view.hall == nil {
		return empty, ErrNotMember
	}
	g := view.hall.owned(owner)
	if g == nil || g.ID != guildID {
		return empty, ErrNotMember
	}
	var target Member
	for _, member := range g.Members {
		if member.ServerGroup == serverGroup && member.Name == name {
			if target.CharacterID != "" {
				return empty, ErrInvalidRegistry
			}
			target = member
		}
	}
	if target.CharacterID == "" {
		return empty, ErrCharacterNotFound
	}
	return MemberRankingPoints(target, rules, now)
}
