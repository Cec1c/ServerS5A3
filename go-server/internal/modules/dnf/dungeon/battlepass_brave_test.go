package dungeon

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"testing"
	"time"
)

func TestBattlePassBraveActualGainAtomicAndReplay(t *testing.T) {
	for _, fail := range []bool{false, true} {
		ctx := context.Background()
		g := memory.NewMemoryGroup()
		now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		for _, err := range []error{g.Account.Save(ctx, repo.AccountRecord{AccountID: "a"}), g.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a", Level: 90, Stats: map[string]int64{"exp": 0}}), g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1", Points: repo.SkillPointState{SyncedLevel: 90}})} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if fail {
			g.CharacterSettlement = experienceAwardFailSettlement{g.CharacterSettlement}
		}
		owner, _ := NewOwner(g)
		cfg := adventuregroup.RuntimeConfig{ShopCategories: []adventuregroup.ShopCategory{{Index: adventuregroup.ShopPointBrave, MaxPoint: 2, ExperiencePerPoint: 10, PointPerExperience: 1}}}
		cmd := ExperienceAwardCommand{UpdatedAt: now, MaximumLevelGains: []uint32{11, 22}, AdventureRuntime: cfg, Plan: progression.CommitPlan{CharacterID: "1", AccountID: "a", Expected: progression.CommitState{Level: 90, SkillPoints: repo.SkillPointState{SyncedLevel: 90}}, Next: progression.CommitState{Level: 90, Experience: 33, SkillPoints: repo.SkillPointState{SyncedLevel: 90}}}}
		if _, err := owner.CommitExperienceAward(ctx, cmd); (err != nil) != fail {
			t.Fatal(fail, err)
		}
		if !fail {
			if out, err := owner.CommitExperienceAward(ctx, cmd); err != nil || !out.Idempotent {
				t.Fatal(out, err)
			}
		}
		ch, _, _ := g.Character.Load(ctx, "1")
		want := int64(2)
		if fail {
			want = 0
		}
		if ch.Stats[fmt.Sprintf("%s%s:26:-1", repo.BattlePassCounterPrefix, repo.ActivityDay(now))] != want {
			t.Fatal("骁勇结晶按实际入账及上限计数", fail, ch.Stats)
		}
	}
}
