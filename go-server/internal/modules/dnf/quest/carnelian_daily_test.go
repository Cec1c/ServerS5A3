package quest

import (
	"context"
	"os"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func carnelianCatalog() *Catalog {
	d := Definition{ID: 13056, Path: "n_quest/quest_renewal/mission/daily/metrocenter_dailymission_01.qst", Grade: "[auto daily mission]", Type: "[hunt monster]", ExposedByNPC: -1, LevelMin: 87, LevelMax: 99, IntData: []int64{-4, 39, -1, -1, 150, -4, 39, -1, -3, 3}, RewardType: "[item]", RewardItems: []RewardItemRule{{ItemID: 10163959, Count: 15}}}
	return &Catalog{ordered: []Definition{d}, byID: map[int64]Definition{d.ID: d}}
}

func TestCarnelianDailyCatalogAndTrigger(t *testing.T) {
	c := carnelianCatalog()
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	e := CharacterEligibility{Level: 90}
	if !containsQuestID(c.QuestListAt(e, r.QuestRecord{}, at).IDs, 13056) {
		t.Error("level 90 carnelian daily mission missing from acceptable list")
	}
	p, err := c.PlanAccept(e, r.QuestRecord{}, 13056)
	if err != nil {
		t.Errorf("accept: %v", err)
	} else if p.InitTrigger != packTriggerChannels(150, 3) {
		t.Errorf("initial trigger=%d, want remaining 150 ordinary and 3 boss", p.InitTrigger)
	}
	if got := definitionInitialTrigger(c.byID[13056], nil); got != packTriggerChannels(150, 3) {
		t.Errorf("five-field trigger=%d", got)
	}
	if !isDailyQuestGrade(c.byID[13056].Grade) {
		t.Error("automatic daily mission is not classified as daily")
	}
	for _, level := range []int{86, 100} {
		if containsQuestID(c.QuestListAt(CharacterEligibility{Level: level}, r.QuestRecord{}, at).IDs, 13056) {
			t.Fatalf("out-of-range level %d offered daily mission", level)
		}
	}
	claimed := r.QuestRecord{States: map[int64]r.QuestState{13056: {Status: "completed", UpdatedAt: at}}}
	if containsQuestID(c.QuestListAt(e, claimed, at).IDs, 13056) {
		t.Fatal("same-day completion offered again")
	}
	if !containsQuestID(c.QuestListAt(e, claimed, at.Add(24*time.Hour)).IDs, 13056) {
		t.Fatal("next-day daily mission missing")
	}
}

func TestRealPVFCarnelianDailyMissions(t *testing.T) {
	p := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if p == "" {
		t.Skip("real PVF required")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	i, err := dnfpvf.Build(context.Background(), a, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ id, min, max, area, count int64 }{{13031, 40, 45, 6, 100}, {13033, 46, 49, 7, 100}, {13036, 50, 53, 21, 100}, {13037, 54, 57, 22, 100}, {13040, 58, 62, 24, 100}, {13041, 63, 70, 12, 100}, {13044, 71, 74, 15, 110}, {13047, 75, 80, 17, 120}, {13050, 81, 84, 19, 130}, {13053, 85, 86, 32, 140}, {13056, 87, 99, 39, 150}}
	for _, tc := range cases {
		d, ok := c.Find(tc.id)
		if !ok {
			t.Fatalf("missing registered quest %d", tc.id)
		}
		if d.Grade != "[auto daily mission]" || int64(d.LevelMin) != tc.min || int64(d.LevelMax) != tc.max || len(d.IntData) != 10 || d.IntData[1] != tc.area {
			t.Fatalf("quest %d definition=%+v", tc.id, d)
		}
		e := CharacterEligibility{Level: int(tc.min)}
		plan, err := c.PlanAccept(e, r.QuestRecord{}, tc.id)
		if err != nil || plan.InitTrigger != packTriggerChannels(tc.count, 3) {
			t.Errorf("quest %d accept=%+v err=%v", tc.id, plan, err)
		}
		reward, err := c.PlanFinishReward(e, tc.id, 0, false)
		if err != nil || len(reward.Items) != 1 || reward.Items[0].ItemID != 10163959 || reward.Items[0].Count != 15 {
			t.Errorf("quest %d reward=%+v err=%v", tc.id, reward.Items, err)
		}
	}
}
