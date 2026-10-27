package guild

import (
	"context"
	"math"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// MemberRankingActivity records committed events for this membership. It is
// independent of spendable mileage, contribution coins and the bounded history.
type MemberRankingActivity struct {
	Month          time.Time `json:"month"`
	RaidClears     [3]uint32 `json:"raid_clears"`
	SameGuildRaids uint32    `json:"same_guild_raids"`
	Mileage        uint64    `json:"mileage"`
	PartyClear     float64   `json:"party_clear"`
}

func (a *MemberRankingActivity) Validate() error {
	if a == nil {
		return nil
	}
	if a.Month.IsZero() || !a.Month.Equal(RankingMonthStart(a.Month)) ||
		a.Mileage > math.MaxInt32 || a.PartyClear < 0 || a.PartyClear > math.MaxInt32 ||
		math.IsNaN(a.PartyClear) || math.IsInf(a.PartyClear, 0) {
		return ErrRankingRules
	}
	var total uint64
	for _, n := range a.RaidClears {
		total += uint64(n)
	}
	if total > math.MaxInt32 || uint64(a.SameGuildRaids) > total {
		return ErrRankingRules
	}
	return nil
}

func memberRankingAt(m Member, now time.Time) (MemberRankingActivity, bool, error) {
	if now.IsZero() || m.JoinedAt.After(now) {
		return MemberRankingActivity{}, false, ErrRankingRules
	}
	if err := m.RankingActivity.Validate(); err != nil {
		return MemberRankingActivity{}, false, err
	}
	month := RankingMonthStart(now)
	if m.RankingActivity != nil {
		if m.RankingActivity.Month.After(month) {
			return MemberRankingActivity{}, false, nil
		}
		if m.RankingActivity.Month.Equal(month) {
			return *m.RankingActivity, true, nil
		}
	}
	return MemberRankingActivity{Month: month}, true, nil
}

func addMemberRankingMileage(g *Guild, member Member, amount uint32, now time.Time) error {
	a, current, err := memberRankingAt(member, now)
	if err != nil || !current {
		return err
	}
	a.Mileage += uint64(amount)
	if err = a.Validate(); err != nil {
		return err
	}
	member.RankingActivity = &a
	g.Members[member.CharacterID] = member
	return nil
}

func addMemberRankingParty(g *Guild, member Member, points float64, now time.Time) error {
	a, current, err := memberRankingAt(member, now)
	if err != nil || !current {
		return err
	}
	a.PartyClear += points
	if err = a.Validate(); err != nil {
		return err
	}
	member.RankingActivity = &a
	g.Members[member.CharacterID] = member
	return nil
}

// CommitRankedRaidClear uses the final raid's existing permanent receipt. The
// registry and all accepted participants commit together, in registry-first
// lock order; replay never awards another member-ranking event.
func CommitRankedRaidClear(ctx context.Context, group r.Group, cmd raid.WeeklyClearCommand) (raid.WeeklyClearResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return raid.CommitWeeklyClearWithGuild(ctx, group, cmd, func(tx r.Group) error {
		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		counts := map[uint32]int{}
		for _, owner := range cmd.Members {
			g, m, ok := reg.GuildForOwner(r.GuildOwner{AccountID: owner.AccountID, CharacterID: owner.CharacterID})
			if ok && !m.JoinedAt.After(cmd.CompletedAt) {
				counts[g.ID]++
			}
		}
		changed := false
		for _, owner := range cmd.Members {
			g, m, ok := reg.GuildForOwner(r.GuildOwner{AccountID: owner.AccountID, CharacterID: owner.CharacterID})
			if !ok || m.JoinedAt.After(cmd.CompletedAt) {
				continue
			}
			a, current, err := memberRankingAt(m, cmd.CompletedAt)
			if err != nil {
				return err
			}
			if !current {
				continue
			}
			if a.RaidClears[cmd.Mode-1] == math.MaxUint32 {
				return ErrRankingRules
			}
			a.RaidClears[cmd.Mode-1]++
			if counts[g.ID] >= 2 {
				a.SameGuildRaids++
			}
			if err = a.Validate(); err != nil {
				return err
			}
			m.RankingActivity = &a
			g.Members[m.CharacterID], g.UpdatedAt = m, cmd.CompletedAt
			changed = true
		}
		if !changed {
			return nil
		}
		return Save(ctx, tx.Settings, reg, cmd.CompletedAt)
	})
}

// MemberRankingPoints returns the three fields consumed by native CMD1505.
func MemberRankingPoints(m Member, rules *MemberRankingRules, now time.Time) ([3]float32, error) {
	var result [3]float32
	if rules == nil || now.IsZero() {
		return result, ErrRankingRules
	}
	if err := m.RankingActivity.Validate(); err != nil {
		return result, err
	}
	if m.RankingActivity == nil || m.RankingActivity.Month.Before(RankingMonthStart(now)) {
		return result, nil
	}
	if m.RankingActivity.Month.After(RankingMonthStart(now)) {
		return result, ErrRankingSnapshotChanged
	}
	a := m.RankingActivity
	raidPoints := float64(a.SameGuildRaids) * rules.SameGuildRaidBonus
	for i, n := range a.RaidClears {
		raidPoints += float64(n) * rules.RaidClears[i]
	}
	for i, value := range [3]float64{raidPoints, float64(a.Mileage) * rules.MileageRate, a.PartyClear} {
		if value < 0 || value > math.MaxInt32 || math.IsNaN(value) || math.IsInf(value, 0) || float64(float32(value)) > math.MaxInt32 {
			return result, ErrRankingRules
		}
		result[i] = float32(value)
	}
	return result, nil
}
