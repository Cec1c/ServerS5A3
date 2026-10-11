package quest

import (
	"context"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
	"time"
)

func TestHellRegionClearRequiresCompletedAbyssInPVFRegionAndLeavesRewardPending(t *testing.T) {
	catalog := &Catalog{byID: map[int64]Definition{10109: {ID: 10109, Path: "n_quest/Hell/achievement_86_Hell_inferno_1.qst", Type: "[condition under clear]", SubType: 23, IntData: []int64{-2, 39, -1, 1}}}}
	for _, tc := range []struct {
		name   string
		region int64
		hell   bool
		want   bool
	}{
		{"ordinary", 39, false, false}, {"other region", 38, true, false}, {"missing region", 0, true, false}, {"real abyss", 39, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := dnfrepo.QuestRecord{CharacterID: "1", States: map[int64]dnfrepo.QuestState{10109: {Status: "active", ProgressValue: 1}}}
			input := ClearMapCompletionInput{DungeonID: 310, MapID: 44100, WorldMapAreaID: tc.region, HellPartyCleared: tc.hell, HellPartyOnly: true, CompletionKey: "verified-run", CompletedAt: time.Now()}
			plan, err := catalog.PlanClearMapCompletion(record, input)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.want {
				if len(plan.Completions) != 0 || len(plan.ChangedFields) != 0 || plan.Record.States[10109].ProgressValue != 1 {
					t.Fatal("unqualified run completed quest")
				}
				return
			}
			state := plan.Record.States[10109]
			if len(plan.Completions) != 1 || state.ProgressValue != 0 || state.Status != "active" || state.Extra["reward_state"] != "pending" {
				t.Fatalf("plan=%+v state=%+v", plan, state)
			}
			replay, err := catalog.PlanClearMapCompletion(plan.Record, input)
			if err != nil {
				t.Fatal(err)
			}
			if len(replay.ChangedFields) != 0 || len(replay.Completions) != 1 || !replay.Completions[0].Idempotent {
				t.Fatal("completed run replay mutated quest")
			}
			if record.States[10109].ProgressValue != 1 {
				t.Fatal("input record changed")
			}
		})
	}
}

func TestRealPVFHellEP5RegionCondition(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF not configured")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]int64{{5782, 15}, {10103, 17}, {10105, 19}, {10107, 32}, {10109, 39}} {
		def, ok := catalog.byID[pair[0]]
		if !ok {
			t.Fatalf("real abyss quest %d missing", pair[0])
		}
		if !clearCompletionTargetMatches(def, ClearMapCompletionInput{WorldMapAreaID: pair[1], HellPartyCleared: true, HellPartyOnly: true}) {
			t.Fatalf("unsupported actual PVF condition: %+v", def)
		}
		if clearCompletionTargetMatches(def, ClearMapCompletionInput{DungeonID: pair[1], MapID: pair[1]}) {
			t.Fatal("region accidentally matched a dungeon/map number")
		}
		for _, other := range []int64{15, 17, 19, 32, 39} {
			if other != pair[1] && clearCompletionTargetMatches(def, ClearMapCompletionInput{WorldMapAreaID: other, HellPartyCleared: true, HellPartyOnly: true}) {
				t.Fatalf("quest %d completed by wrong region %d", pair[0], other)
			}
		}
	}
}
