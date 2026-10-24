package guild

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// DungeonPolicy permits an explicit operating override. The default matches
// native dstr 48453: monthly day 1 at 06:00, in the service's UTC+8 calendar.
type DungeonPolicy struct {
	ResetDay            int    `json:"reset_day"`
	ResetHour           int    `json:"reset_hour"`
	UTCOffsetMinutes    int    `json:"utc_offset_minutes"`
	ClearUnitsPerMember uint32 `json:"clear_units_per_member"`
}
type DungeonCycle struct {
	Key                string
	StartUnix, EndUnix uint32
	Units              uint32
}
type DungeonProgress struct {
	CycleKey  string            `json:"cycle_key"`
	StartUnix uint32            `json:"start_unix"`
	EndUnix   uint32            `json:"end_unix"`
	Counts    map[uint32]uint32 `json:"counts,omitempty"`
}
type DungeonEntry struct {
	Owner                r.GuildOwner
	GuildID, DungeonID   uint32
	JoinedAt             time.Time
	Cycle                DungeonCycle
	CatalogDigest, RunID string
}
type DungeonClearResult struct {
	GuildID, DungeonID, Units, Count, Contribution uint32
	CycleKey                                       string
}

func (p *DungeonPolicy) Cycle(now time.Time) (DungeonCycle, error) {
	policy := DungeonPolicy{ResetDay: 1, ResetHour: 6, UTCOffsetMinutes: 480, ClearUnitsPerMember: 1}
	if p != nil {
		policy = *p
	}
	if policy.ResetDay < 1 || policy.ResetDay > 28 || policy.ResetHour < 0 || policy.ResetHour > 23 || policy.UTCOffsetMinutes < -720 || policy.UTCOffsetMinutes > 840 || policy.ClearUnitsPerMember < 1 || policy.ClearUnitsPerMember > 4 {
		return DungeonCycle{}, ErrInvalidRegistry
	}
	loc := time.FixedZone("guild-dungeon-policy", policy.UTCOffsetMinutes*60)
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), policy.ResetDay, policy.ResetHour, 0, 0, 0, loc)
	if now.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	end := start.AddDate(0, 1, 0)
	if start.Unix() < 1 || end.Unix() > math.MaxUint32 {
		return DungeonCycle{}, ErrInvalidRegistry
	}
	// StartUnix includes the reset hour. Existing explicit midnight policies
	// retain exactly their former v1 keys and active progress.
	return DungeonCycle{Key: fmt.Sprintf("v1:%d:%d:%d:%d", start.Unix(), policy.ResetDay, policy.UTCOffsetMinutes, policy.ClearUnitsPerMember), StartUnix: uint32(start.Unix()), EndUnix: uint32(end.Unix()), Units: policy.ClearUnitsPerMember}, nil
}

func DungeonCounts(g *Guild, cycle DungeonCycle) (map[uint32]uint32, error) {
	out := map[uint32]uint32{}
	if g == nil || g.Dungeon == nil {
		return out, nil
	}
	p := g.Dungeon
	if p.CycleKey == "" || p.StartUnix >= p.EndUnix {
		return nil, ErrInvalidRegistry
	}
	if p.StartUnix > cycle.StartUnix {
		return nil, ErrInvalidRegistry
	} // clock rollback is not a new cycle
	if p.CycleKey != cycle.Key {
		return out, nil
	}
	if p.StartUnix != cycle.StartUnix || p.EndUnix != cycle.EndUnix {
		return nil, ErrInvalidRegistry
	}
	for id, count := range p.Counts {
		if id == 0 || id > math.MaxInt32 || count > math.MaxInt32 {
			return nil, ErrInvalidRegistry
		}
		out[id] = count
	}
	return out, nil
}

func PrepareDungeonEntries(reg Registry, catalog *DungeonCatalog, id uint32, owners []r.GuildOwner, runID string, now time.Time) ([]DungeonEntry, error) {
	if catalog == nil || strings.TrimSpace(runID) == "" || len(runID) > 128 || len(owners) < 1 || len(owners) > 4 {
		return nil, ErrDungeonUnlockRules
	}
	cycle, err := reg.DungeonPolicy.Cycle(now)
	if err != nil {
		return nil, err
	}
	entries := make([]DungeonEntry, 0, len(owners))
	seen := map[string]bool{}
	var guildID uint32
	for _, owner := range owners {
		g, m, ok := reg.GuildForOwner(owner)
		if !ok || seen[owner.CharacterID] {
			return nil, ErrNotMember
		}
		seen[owner.CharacterID] = true
		if guildID != 0 && g.ID != guildID {
			return nil, ErrPermissionDenied
		}
		guildID = g.ID
		counts, err := DungeonCounts(g, cycle)
		if err != nil {
			return nil, err
		}
		open, err := catalog.Rules.Allows(id, counts)
		if err != nil {
			return nil, err
		}
		if !open {
			return nil, ErrPermissionDenied
		}
		entries = append(entries, DungeonEntry{Owner: owner, GuildID: g.ID, DungeonID: id, JoinedAt: m.JoinedAt, Cycle: cycle, CatalogDigest: catalog.digest, RunID: runID})
	}
	return entries, nil
}

func (e DungeonEntry) CompletionKey() string {
	return fmt.Sprintf("guild-dungeon:v1:%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%q:%q:%q:%q", e.GuildID, e.Cycle.Key, e.RunID, e.Owner.AccountID, e.Owner.CharacterID))))
}

// ApplyDungeonClear runs inside GuildSettlementUnitOfWork, after its permanent
// per-character completion receipt check. The caller grants returned items in
// that SAME transaction; no nested owner or independent registry save occurs.
func ApplyDungeonClear(ctx context.Context, settings r.SettingsRepository, catalog *DungeonCatalog, entry DungeonEntry, now time.Time) (DungeonReward, DungeonClearResult, error) {
	reg, err := Load(ctx, settings)
	if err != nil {
		return DungeonReward{}, DungeonClearResult{}, err
	}
	expected, err := PrepareDungeonEntries(reg, catalog, entry.DungeonID, []r.GuildOwner{entry.Owner}, entry.RunID, now)
	if err != nil {
		return DungeonReward{}, DungeonClearResult{}, err
	}
	e := expected[0]
	if e.GuildID != entry.GuildID || e.Cycle != entry.Cycle || e.CatalogDigest != entry.CatalogDigest || !e.JoinedAt.Equal(entry.JoinedAt) {
		return DungeonReward{}, DungeonClearResult{}, ErrPermissionDenied
	}
	g := reg.Guilds[e.GuildID]
	counts, err := DungeonCounts(g, e.Cycle)
	if err != nil {
		return DungeonReward{}, DungeonClearResult{}, err
	}
	count := counts[e.DungeonID]
	reward, err := catalog.Reward(e.DungeonID, count)
	if err != nil {
		return DungeonReward{}, DungeonClearResult{}, err
	}
	// Once native purification is 100%, preserve the finite count; subsequent
	// legitimate clears still receive base rewards and their own durable receipt.
	ratio, err := catalog.Rules.PurificationRatio(e.DungeonID, count)
	if err != nil {
		return DungeonReward{}, DungeonClearResult{}, err
	}
	units := uint32(0)
	if ratio < 100 {
		if count > math.MaxInt32-e.Cycle.Units {
			return DungeonReward{}, DungeonClearResult{}, ErrInvalidRegistry
		}
		count += e.Cycle.Units
		units = e.Cycle.Units
		if _, err = catalog.Rules.PurificationRatio(e.DungeonID, count); err != nil {
			return DungeonReward{}, DungeonClearResult{}, err
		}
	}
	member := g.Members[e.Owner.CharacterID]
	if member.ContributionPoints > math.MaxUint32-reward.Contribution {
		return DungeonReward{}, DungeonClearResult{}, ErrMileageOverflow
	}
	member.ContributionPoints += reward.Contribution
	g.Members[e.Owner.CharacterID] = member
	counts[e.DungeonID] = count
	g.Dungeon = &DungeonProgress{CycleKey: e.Cycle.Key, StartUnix: e.Cycle.StartUnix, EndUnix: e.Cycle.EndUnix, Counts: counts}
	g.UpdatedAt = now
	if err = Save(ctx, settings, reg, now); err != nil {
		return DungeonReward{}, DungeonClearResult{}, err
	}
	return reward, DungeonClearResult{GuildID: g.ID, DungeonID: e.DungeonID, Units: units, Count: count, Contribution: reward.Contribution, CycleKey: e.Cycle.Key}, nil
}
