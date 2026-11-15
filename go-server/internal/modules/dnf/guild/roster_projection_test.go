package guild

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func rosterProjectionFixture(t *testing.T) (*GuildReadMembership, r.GuildOwner) {
	t.Helper()
	_, reg, owner, now := newSnapshotSettingsFixture(t)
	g := reg.Guilds[1]
	g.Level, g.Experience, g.Mileage = 12, 1234, 56
	g.Notice, g.Promotion = "notice", "promotion"
	g.Mark, g.AuraMark, g.AuraFrame = 3, 4, 5
	g.RecommendedRegion, g.RecommendedChannel = 6, 7
	g.WebPromotion, g.JoinMode, g.MemberLimit = true, 2, 1000
	g.Tags = []byte{1, 3}
	g.GradeNames = map[byte]string{1: "Master", 4: "Member"}
	g.GradePermissions = map[byte]uint32{1: GradePermissionMask, 4: 2}
	g.Cargo = &Cargo{}
	g.Placements = &PlacementState{}
	g.Dungeon = &DungeonProgress{}
	g.MileageHistory = make([]MileageHistoryEntry, 2000)
	g.Applications = map[string]Application{"waiting": {CharacterID: "waiting"}}
	points := uint32(15)
	m := g.Members[owner.CharacterID]
	m.DungeonContributionPoints = &points
	m.RankingActivity = &MemberRankingActivity{Month: RankingMonthStart(now), Mileage: 22}
	m.RankingPresence = &RankingPresence{Month: RankingMonthStart(now), Weeks: 1}
	g.Members[owner.CharacterID] = m
	snapshot, err := newGuildReadSnapshot(reg)
	if err != nil {
		t.Fatal(err)
	}
	return &GuildReadMembership{snapshot: snapshot, characterID: owner.CharacterID}, owner
}

func TestGuildRosterProjectionDetachedAndScoped(t *testing.T) {
	same := func(a, b *Guild) bool {
		aa, e1 := json.Marshal(a)
		bb, e2 := json.Marshal(b)
		return e1 == nil && e2 == nil && bytes.Equal(aa, bb)
	}
	membership, owner := rosterProjectionFixture(t)
	full, err := membership.Display(owner)
	if err != nil {
		t.Fatal(err)
	}
	got, err := membership.RosterProjection(owner)
	if err != nil || got == nil {
		t.Fatal(got, err)
	}
	// All remaining Guild fields must agree with the full detached contract.
	// This catches newly added display fields omitted from the projection.
	full.Cargo, full.Placements, full.Dungeon = nil, nil, nil
	full.RankingActivity, full.Guardians, full.Merchants = nil, nil, nil
	full.MileageHistory, full.Applications, full.Invitations = nil, nil, nil
	full.Essence = EssenceBalance{}
	if !same(got, full) {
		t.Fatal("roster metadata or members differ from full display")
	}
	got.Tags[0] = 255
	delete(got.Benefits, 1)
	got.GradeNames[1] = "changed"
	got.GradePermissions[1] = 0
	m := got.Members[owner.CharacterID]
	*m.DungeonContributionPoints = 99
	m.RankingActivity.Mileage = 99
	m.RankingPresence.Weeks = 31
	delete(got.Members, owner.CharacterID)
	again, err := membership.RosterProjection(owner)
	if err != nil || !same(again, full) {
		t.Fatal("projection mutated cached snapshot", err)
	}
	complete, err := membership.Display(owner)
	if err != nil || complete.Cargo == nil || len(complete.MileageHistory) != 2000 || len(complete.Applications) != 1 {
		t.Fatal("full Display contract lost historical fields", err)
	}
	for _, invalid := range []r.GuildOwner{{CharacterID: owner.CharacterID, AccountID: "foreign"}, {CharacterID: "2", AccountID: owner.AccountID}} {
		if g, err := membership.RosterProjection(invalid); err != nil || g != nil {
			t.Fatal("foreign owner admitted", g, err)
		}
	}
	var missing *GuildReadMembership
	if g, err := missing.RosterProjection(owner); err != nil || g != nil {
		t.Fatal(g, err)
	}
}

func TestGuildRosterProjectionRevisionAndOwnershipChange(t *testing.T) {
	store, reg, owner, now := newSnapshotSettingsFixture(t)
	ctx := context.Background()
	first, _, err := ReadGuildMembership(ctx, store, owner.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	m := reg.Guilds[1].Members[owner.CharacterID]
	m.AccountID = "replacement"
	reg.Guilds[1].Members[owner.CharacterID] = m
	if err = Save(ctx, store, reg, now); err != nil {
		t.Fatal(err)
	}
	next, _, err := ReadGuildMembership(ctx, store, owner.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version() == next.Version() {
		t.Fatal("changed content reused old version")
	}
	if g, err := next.RosterProjection(owner); err != nil || g != nil {
		t.Fatal("stale owner admitted", g, err)
	}
	if g, err := first.RosterProjection(owner); err != nil || g == nil {
		t.Fatal("original RR snapshot changed", g, err)
	}
}

var rosterBenchmarkResult *Guild

func BenchmarkGuildRosterProjection(b *testing.B) {
	// Both paths expose all 1000 members. History remains present in the full
	// source so this measures the avoided decoding rather than fewer rows.
	reg := NewRegistry()
	g := &Guild{ID: 1, Name: "Guild", Members: map[string]Member{}, Benefits: map[byte]Benefit{}, GradeNames: map[byte]string{}, GradePermissions: map[byte]uint32{}, MileageHistory: make([]MileageHistoryEntry, 2000)}
	for i := 1; i <= 1000; i++ {
		id := fmt.Sprint(i)
		g.Members[id] = Member{CharacterID: id, AccountID: "a", Name: "member", Level: 90, Grade: 4}
	}
	reg.Guilds[1] = g
	snapshot, err := newGuildReadSnapshot(reg)
	if err != nil {
		b.Fatal(err)
	}
	membership := &GuildReadMembership{snapshot: snapshot, characterID: "1"}
	owner := r.GuildOwner{CharacterID: "1", AccountID: "a"}
	for _, full := range []bool{true, false} {
		b.Run(fmt.Sprint("full=", full), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var err error
				if full {
					rosterBenchmarkResult, err = membership.Display(owner)
				} else {
					rosterBenchmarkResult, err = membership.RosterProjection(owner)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
