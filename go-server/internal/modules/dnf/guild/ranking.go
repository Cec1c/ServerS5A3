package guild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrRankingSnapshotChanged = errors.New("guild ranking sample source changed")

var rankingLocation = time.FixedZone("DNF UTC+8", 8*3600)

type RankingRow struct {
	Rank    uint32         `json:"rank"`
	Score   float32        `json:"score"`
	Factors RankingFactors `json:"factors"`
}

type RankingPeriod struct {
	Start     time.Time             `json:"start"`
	SampledAt time.Time             `json:"sampled_at"`
	Rows      map[uint32]RankingRow `json:"rows"`
	Online    []r.GuildOwner        `json:"-"`
}

type RankingState struct {
	Current  *RankingPeriod `json:"current,omitempty"`
	Previous *RankingPeriod `json:"previous,omitempty"`
	Older    *RankingPeriod `json:"older,omitempty"`
}

// RankingMonthStart follows the native monthly 1st, 06:00 calendar.
func RankingMonthStart(now time.Time) time.Time {
	local := now.In(rankingLocation)
	start := time.Date(local.Year(), local.Month(), 1, 6, 0, 0, 0, rankingLocation)
	if now.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start
}

func BuildRankingPeriod(at time.Time, factors map[uint32]RankingFactors) (*RankingPeriod, error) {
	if at.IsZero() {
		return nil, ErrRankingRules
	}
	p := &RankingPeriod{Start: RankingMonthStart(at), SampledAt: at, Rows: make(map[uint32]RankingRow, len(factors))}
	ids := make([]uint32, 0, len(factors))
	for id, f := range factors {
		if id == 0 {
			return nil, ErrRankingRules
		}
		score, err := f.Total()
		if err != nil {
			return nil, err
		}
		p.Rows[id] = RankingRow{Score: score, Factors: f}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := p.Rows[ids[i]], p.Rows[ids[j]]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return ids[i] < ids[j]
	})
	var rank uint32
	for i, id := range ids {
		row := p.Rows[id]
		if row.Score == 0 {
			continue
		}
		if i == 0 || row.Score != p.Rows[ids[i-1]].Score {
			rank = uint32(i + 1)
		}
		row.Rank = rank
		p.Rows[id] = row
	}
	return p, nil
}

func (s *RankingState) Validate() error {
	if s == nil {
		return nil
	}
	var newer *RankingPeriod
	for _, p := range []*RankingPeriod{s.Current, s.Previous, s.Older} {
		if p == nil {
			continue
		}
		if !p.Start.Equal(RankingMonthStart(p.SampledAt)) || p.SampledAt.IsZero() || newer != nil && !p.Start.Before(newer.Start) {
			return ErrRankingRules
		}
		factors := make(map[uint32]RankingFactors, len(p.Rows))
		for id, row := range p.Rows {
			factors[id] = row.Factors
		}
		expected, err := BuildRankingPeriod(p.SampledAt, factors)
		if err != nil {
			return err
		}
		for id, row := range expected.Rows {
			if row != p.Rows[id] {
				return ErrRankingRules
			}
		}
		newer = p
	}
	return nil
}

// Publish preserves only actually sampled months. A restart after an empty
// month does not turn an older result into last month's ranking.
func (s *RankingState) Publish(p *RankingPeriod) error {
	if p == nil {
		return ErrRankingRules
	}
	if err := (&RankingState{Current: p}).Validate(); err != nil {
		return err
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Current != nil {
		if p.SampledAt.Before(s.Current.SampledAt) {
			return ErrRankingSnapshotChanged
		}
		if p.Start.After(s.Current.Start) {
			s.Older, s.Previous = s.Previous, s.Current
		}
	}
	copyPeriod := *p
	copyPeriod.Online = nil
	copyPeriod.Rows = make(map[uint32]RankingRow, len(p.Rows))
	for id, row := range p.Rows {
		copyPeriod.Rows[id] = row
	}
	s.Current = &copyPeriod
	return nil
}

func (s *RankingState) View(guildID uint32, now time.Time) [3]RankingRow {
	var rows [3]RankingRow
	if s == nil {
		return rows
	}
	start := RankingMonthStart(now)
	for i := range rows {
		month := start.AddDate(0, -i, 0)
		for _, p := range []*RankingPeriod{s.Current, s.Previous, s.Older} {
			if p != nil && p.Start.Equal(month) {
				rows[i] = p.Rows[guildID]
				break
			}
		}
	}
	return rows
}

// RankingSourceDigest excludes the computed result. Publication checks the
// registry again inside its lock, preserving concurrent membership/progress.
func RankingSourceDigest(reg Registry) (string, error) {
	reg.Ranking = nil
	b, err := json.Marshal(reg)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func PublishRanking(ctx context.Context, uow r.GuildUnitOfWork, owner r.GuildOwner, sourceDigest string, period *RankingPeriod) error {
	if uow == nil || period == nil || len(sourceDigest) != 64 {
		return ErrRepositoryUnavailable
	}
	return Update(ctx, uow, []r.GuildOwner{owner}, period.SampledAt, func(_ r.Group, reg *Registry) error {
		if _, _, ok := reg.GuildForOwner(owner); !ok {
			return ErrNotMember
		}
		digest, err := RankingSourceDigest(*reg)
		if err != nil {
			return err
		}
		if digest != sourceDigest {
			return ErrRankingSnapshotChanged
		}
		if len(period.Rows) != len(reg.Guilds) {
			return ErrRankingSnapshotChanged
		}
		for id := range reg.Guilds {
			if _, ok := period.Rows[id]; !ok {
				return ErrRankingSnapshotChanged
			}
		}
		if reg.Ranking == nil {
			reg.Ranking = &RankingState{}
		}
		for _, online := range period.Online {
			if _, _, ok := reg.GuildForOwner(online); !ok {
				return ErrRankingSnapshotChanged
			}
			reg.ObserveMemberOnline(online, period.SampledAt)
		}
		return reg.Ranking.Publish(period)
	})
}
