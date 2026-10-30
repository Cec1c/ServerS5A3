package quest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestCarnelianWorldMapDeathScopeAndReplay(t *testing.T) {
	c := carnelianCatalog()
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	before := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{13056: {Status: "active", ProgressValue: int64(packTriggerChannels(150, 3))}}}
	base := HuntMonsterKillInput{DungeonID: 310, WorldMapAreaID: 39, MonsterCode: 9600, MonsterKind: WorldMapHuntMonsterOrdinary, CompletionKey: "run/room/actor-1", CompletedAt: at}
	for _, tc := range []struct {
		name           string
		change         func(*HuntMonsterKillInput)
		ordinary, boss int64
	}{
		{"ordinary", func(*HuntMonsterKillInput) {}, 149, 3},
		{"boss", func(i *HuntMonsterKillInput) { i.MonsterKind = WorldMapHuntMonsterBoss }, 150, 2},
		{"other area", func(i *HuntMonsterKillInput) { i.WorldMapAreaID = 32 }, 150, 3},
		{"missing area", func(i *HuntMonsterKillInput) { i.WorldMapAreaID = 0 }, 150, 3},
		{"unknown rank", func(i *HuntMonsterKillInput) { i.MonsterKind = WorldMapHuntMonsterUnknown }, 150, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.change(&in)
			p, err := c.PlanHuntMonsterKill(before, in)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Record.States[13056].ProgressValue; got != int64(packTriggerChannels(tc.ordinary, tc.boss)) {
				t.Fatalf("remaining=%d", got)
			}
			again, err := c.PlanHuntMonsterKill(p.Record, in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Record, again.Record) {
				t.Fatal("duplicate death changed progress")
			}
		})
	}
	d := c.byID[13056]
	d.IntData = append([]int64(nil), d.IntData...)
	d.IntData[2] = 2
	c.byID[d.ID] = d
	p, err := c.PlanHuntMonsterKill(before, base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, p.Record) {
		t.Fatal("below minimum difficulty counted")
	}
	base.Difficulty = 2
	p, err = c.PlanHuntMonsterKill(before, base)
	if err != nil || p.Record.States[13056].ProgressValue != int64(packTriggerChannels(149, 3)) {
		t.Fatalf("minimum difficulty result=%+v err=%v", p, err)
	}
}

func TestCarnelianWorldMapRejectsMalformedTargets(t *testing.T) {
	for _, values := range [][]int64{{-4, 39, -1, -1}, {-4, 39, -1, -1, 0}, {-4, 39, -1, -1, 512}, {-4, 0, -1, -1, 1}, {-4, 39, -2, -1, 1}, {-4, 39, -1, -4, 16}, {-4, 39, -1, -1, 1, 310, -1, 9600, 1}} {
		if _, ok := worldMapHuntTargets(values); ok {
			t.Fatalf("accepted unsupported region target %v", values)
		}
		if _, ok := huntMonsterMatchedChannel(values, HuntMonsterKillInput{DungeonID: 39, WorldMapAreaID: 39, MonsterCode: 9600, MonsterKind: WorldMapHuntMonsterOrdinary}); ok {
			t.Fatalf("malformed target fell back to legacy matching: %v", values)
		}
	}
}

func TestCarnelianExplicitDungeonFallbackIsBounded(t *testing.T) {
	c := carnelianCatalog()
	d := c.byID[13056]
	d.DungeonInfo = []int64{315, -1}
	c.byID[d.ID] = d
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	record := r.QuestRecord{CharacterID: "19", States: map[int64]r.QuestState{d.ID: {Status: "active", ProgressValue: int64(packTriggerChannels(150, 3))}}}
	for _, tc := range []struct {
		name                string
		dungeon, area, want int64
	}{{"explicit member", 315, 0, 1685}, {"not declared", 316, 0, 1686}, {"conflicting known area", 315, 32, 1686}} {
		p, err := c.PlanHuntMonsterKill(record, HuntMonsterKillInput{DungeonID: tc.dungeon, WorldMapAreaID: tc.area, MonsterKind: WorldMapHuntMonsterOrdinary, MonsterCode: 9600, CompletionKey: tc.name, CompletedAt: at})
		if err != nil || p.Record.States[d.ID].ProgressValue != tc.want {
			t.Fatalf("%s remaining=%d err=%v", tc.name, p.Record.States[d.ID].ProgressValue, err)
		}
	}
	d.IntData = append([]int64(nil), d.IntData...)
	d.IntData[6] = 32
	if area := worldMapHuntFallbackArea(d, HuntMonsterKillInput{DungeonID: 315}); area != 0 {
		t.Fatal("ambiguous multi-region quest assigned a region")
	}
}

func carnelianLifecycleFixture(t *testing.T) (r.Group, *Catalog, CharacterEligibility) {
	t.Helper()
	g := finishOwnerSeed(t, context.Background())
	c := carnelianCatalog()
	// Keep the real targets/reward; use a low-level synthetic character so the
	// tiny progression fixture is sufficient. Real grade/level gates are tested
	// separately against the actual PVF.
	d := c.byID[13056]
	d.LevelMin = 1
	d.Difficulty = "N"
	c.byID[d.ID] = d
	c.ordered = []Definition{d}
	return g, c, CharacterEligibility{Level: 1, Job: 2}
}

func carnelianAcceptAndHunt(t *testing.T, g r.Group, c *Catalog, e CharacterEligibility, at time.Time) {
	t.Helper()
	ctx := context.Background()
	o, err := NewOwner(g)
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: 13056})
	accepted, err := o.ApplyAcceptAt(ctx, c, e, cmd, at)
	if err != nil || accepted.Idempotent || accepted.InitTrigger != packTriggerChannels(150, 3) {
		t.Fatalf("accept=%+v err=%v", accepted, err)
	}
	before := dailyLifecycleLoadQuest(t, ctx, g)
	// Neither scalar nor packed client updates may substitute for real kills.
	for _, kind := range []byte{0, 1, 16, 32, 64} {
		for _, inc := range []bool{false, true} {
			got, err := o.ApplySetTrigger(ctx, NewTriggerCommand(alignedRequest("acc", 19), SetTriggerRequest{QuestID: 13056, TriggerType: kind, IsIncrement: inc}))
			if err != nil || got.StateChanged {
				t.Fatalf("client trigger=%+v err=%v", got, err)
			}
		}
	}
	if !reflect.DeepEqual(before, dailyLifecycleLoadQuest(t, ctx, g)) {
		t.Fatal("client changed hunt counters")
	}
	for n := 0; n < 153; n++ {
		kind := WorldMapHuntMonsterOrdinary
		if n >= 150 {
			kind = WorldMapHuntMonsterBoss
		}
		in := HuntMonsterKillInput{DungeonID: 310, WorldMapAreaID: 39, MonsterCode: 9600, MonsterKind: kind, CompletionKey: fmt.Sprintf("%d/room/actor-%d", at.Unix(), n), CompletedAt: at}
		if _, err := o.ApplyHuntMonsterKill(ctx, c, "19", in); err != nil {
			t.Fatal(err)
		}
		if n == 0 || n == 152 {
			if _, err := o.ApplyHuntMonsterKill(ctx, c, "19", in); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := dailyLifecycleLoadQuest(t, ctx, g).States[13056]
	if state.ProgressValue != 0 || state.Extra["reward_state"] != "pending" {
		t.Fatalf("after kills=%+v", state)
	}
}

func carnelianFinishInput(t *testing.T, at time.Time) FinishCommitInput {
	t.Helper()
	return FinishCommitInput{AccountID: "acc", CharacterID: "19", QuestID: 13056, Multiplier: 1, CommittedAt: at, Progression: finishOwnerProgression(t), AllocateItem: finishOwnerAllocator(nil)}
}

func TestCarnelianTwoDayRewardLifecycle(t *testing.T) {
	ctx := context.Background()
	g, c, e := carnelianLifecycleFixture(t)
	day := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for n := 0; n < 2; n++ {
		at := day.Add(time.Duration(n) * 24 * time.Hour)
		carnelianAcceptAndHunt(t, g, c, e, at)
		// A reconstructed owner reads the persisted kill progress and receipt.
		o, _ := NewOwner(g)
		in := carnelianFinishInput(t, at)
		first, err := o.ApplyFinishSettlement(ctx, c, in)
		if err != nil || first.Replayed || len(first.Items) != 1 || first.Items[0].ItemID != 10163959 || first.Items[0].Delta != 15 {
			t.Fatalf("finish=%+v err=%v", first, err)
		}
		o, _ = NewOwner(g)
		again, err := o.ApplyFinishSettlement(ctx, c, in)
		if err != nil || !again.Replayed {
			t.Fatalf("replay=%+v err=%v", again, err)
		}
		bag, _, err := g.Inventory.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		if got := bag.Slots["0:65"]; got.ItemID != 10163959 || got.Count != int64((n+1)*15) {
			t.Fatalf("bag=%+v", got)
		}
		record := dailyLifecycleLoadQuest(t, ctx, g)
		if state := record.States[13056]; state.Extra[QuestDailyCompletionDayKey] != dailyLifecycleDayKey(at) {
			t.Fatalf("daily receipt=%+v", state)
		}
		if _, err := c.PlanAcceptAt(e, record, 13056, at); !errors.Is(err, ErrQuestNotAcceptable) {
			t.Fatalf("same-day accept err=%v", err)
		}
	}
}

func TestCarnelianRewardAllocatorFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	g, c, e := carnelianLifecycleFixture(t)
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	carnelianAcceptAndHunt(t, g, c, e, at)
	q := dailyLifecycleLoadQuest(t, ctx, g)
	bag, _, _ := g.Inventory.Load(ctx, "19")
	o, _ := NewOwner(g)
	in := carnelianFinishInput(t, at)
	fail := errors.New("material bag full")
	in.AllocateItem = finishOwnerAllocator(fail)
	if _, err := o.ApplyFinishSettlement(ctx, c, in); !errors.Is(err, fail) {
		t.Fatalf("finish err=%v", err)
	}
	got, _, _ := g.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(q, dailyLifecycleLoadQuest(t, ctx, g)) || !reflect.DeepEqual(bag, got) {
		t.Fatal("failed reward changed inventory/quest")
	}
	in.AllocateItem = finishOwnerAllocator(nil)
	if result, err := o.ApplyFinishSettlement(ctx, c, in); err != nil || result.Replayed {
		t.Fatalf("retry=%+v err=%v", result, err)
	}
}
