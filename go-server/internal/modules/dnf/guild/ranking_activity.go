package guild

import (
	"context"
	"math"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// RankingActivity contains committed monthly events, independent of the
// bounded mileage history. The settlement's permanent receipt owns deduplication.
type RankingActivity struct {
	Month      time.Time `json:"month"`
	PartyClear float64   `json:"party_clear"`
}

type RankingPartyClear struct {
	DungeonID  uint32
	StartedAt  time.Time
	Members    []r.GuildOwner
	Rules      *RankingRules
	Revalidate func() bool
}

type RankingClearResult struct {
	GuildID uint32
	Month   time.Time
	Points  float64
}

// ApplyRankingPartyClear is called inside GuildSettlementUnitOfWork, after its
// durable receipt check. Only an actual same-guild teammate present in this
// accepted run qualifies; joining the guild after entry does not qualify.
func ApplyRankingPartyClear(ctx context.Context, settings r.SettingsRepository, owner r.GuildOwner, clear RankingPartyClear, now time.Time) (RankingClearResult, error) {
	if clear.Rules == nil || clear.StartedAt.IsZero() || clear.StartedAt.After(now) || len(clear.Members) < 2 || len(clear.Members) > 4 || clear.Revalidate == nil || !clear.Revalidate() {
		return RankingClearResult{}, ErrRankingRules
	}
	points, eligible := clear.Rules.PartyClears[clear.DungeonID]
	if !eligible || points.Points <= 0 || math.IsNaN(points.Points) || math.IsInf(points.Points, 0) {
		return RankingClearResult{}, ErrRankingRules
	}
	seen := make(map[string]bool, len(clear.Members))
	actorPresent := false
	for _, peer := range clear.Members {
		if peer.AccountID == "" || peer.CharacterID == "" || seen[peer.CharacterID] {
			return RankingClearResult{}, ErrRankingRules
		}
		seen[peer.CharacterID] = true
		actorPresent = actorPresent || peer == owner
	}
	if !actorPresent {
		return RankingClearResult{}, ErrCharacterNotFound
	}
	reg, err := Load(ctx, settings)
	if err != nil {
		return RankingClearResult{}, err
	}
	g, member, found := reg.GuildForOwner(owner)
	if !found || member.JoinedAt.After(clear.StartedAt) {
		return RankingClearResult{}, nil
	}
	var sameGuild int
	for _, peer := range clear.Members {
		pg, pm, found := reg.GuildForOwner(peer)
		if found && pg.ID == g.ID && !pm.JoinedAt.After(clear.StartedAt) {
			sameGuild++
		}
	}
	if sameGuild < 2 {
		return RankingClearResult{}, nil
	}
	month := RankingMonthStart(now)
	activity := RankingActivity{Month: month}
	if g.RankingActivity != nil {
		if err := g.RankingActivity.Validate(); err != nil {
			return RankingClearResult{}, err
		}
		if g.RankingActivity.Month.After(month) {
			return RankingClearResult{}, ErrRankingSnapshotChanged
		}
		if g.RankingActivity.Month.Equal(month) {
			activity = *g.RankingActivity
		}
	}
	// No authoritative returning-warrior producer exists yet. Ordinary points
	// are recorded at completion, never retroactively multiplied at query time.
	activity.PartyClear += points.Points
	if err := activity.Validate(); err != nil {
		return RankingClearResult{}, err
	}
	if !clear.Revalidate() {
		return RankingClearResult{}, ErrCharacterNotFound
	}
	g.RankingActivity, g.UpdatedAt = &activity, now
	if err := Save(ctx, settings, reg, now); err != nil {
		return RankingClearResult{}, err
	}
	return RankingClearResult{GuildID: g.ID, Month: month, Points: points.Points}, nil
}

func (a RankingActivity) Validate() error {
	if a.Month.IsZero() || !a.Month.Equal(RankingMonthStart(a.Month)) || a.PartyClear < 0 || a.PartyClear > math.MaxInt32 || math.IsNaN(a.PartyClear) || math.IsInf(a.PartyClear, 0) {
		return ErrRankingRules
	}
	return nil
}

func RankingPartyPoints(g *Guild, now time.Time) (float32, error) {
	if g == nil || now.IsZero() {
		return 0, ErrRankingRules
	}
	if g.RankingActivity == nil {
		return 0, nil
	}
	if err := g.RankingActivity.Validate(); err != nil {
		return 0, err
	}
	month := RankingMonthStart(now)
	if g.RankingActivity.Month.After(month) {
		return 0, ErrRankingSnapshotChanged
	}
	if !g.RankingActivity.Month.Equal(month) {
		return 0, nil
	}
	return float32(g.RankingActivity.PartyClear), nil
}
