package dungeon

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/progression"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

type experienceAwardFailAccount struct{ repo.AccountRepository }

func (r experienceAwardFailAccount) Save(context.Context, repo.AccountRecord) error {
	return errors.New("隔离账号写入失败")
}

func TestExperienceAwardBatchConcurrentCharactersPreserveAccountDeltas(t *testing.T) {
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	if err := r.Account.Save(ctx, repo.AccountRecord{AccountID: "shared"}); err != nil {
		t.Fatal(err)
	}
	o, err := NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 1; i <= 4; i++ {
		id := strconv.Itoa(i)
		if err := r.Character.Save(ctx, repo.CharacterRecord{CharacterID: id, AccountID: "shared", Level: 90, Stats: map[string]int64{"exp": 0}}); err != nil {
			t.Fatal(err)
		}
		if err := r.Skill.Save(ctx, repo.SkillRecord{CharacterID: id, Points: repo.SkillPointState{SyncedLevel: 90}}); err != nil {
			t.Fatal(err)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := o.CommitExperienceAward(ctx, ExperienceAwardCommand{
				Plan:              progression.CommitPlan{CharacterID: id, AccountID: "shared", Expected: progression.CommitState{Level: 90, SkillPoints: repo.SkillPointState{SyncedLevel: 90}}, Next: progression.CommitState{Level: 90, Experience: 23, SkillPoints: repo.SkillPointState{SyncedLevel: 90}}},
				MaximumLevelGains: []uint32{11, 12}, AdventureRuntime: adventuregroup.RuntimeConfig{Capsule: adventuregroup.CapsuleConfig{MaximumExperience: 1000}},
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	a, _, _ := r.Account.Load(ctx, "shared")
	state, err := adventuregroup.ParseRuntimeState(a, adventuregroup.RuntimeConfig{Capsule: adventuregroup.CapsuleConfig{MaximumExperience: 1000}}, time.Now())
	if err != nil || a.HonorExp != 92 || state.GrowthExperience != 92 {
		t.Fatalf("并发覆盖账号增量：%+v %+v %v", a, state, err)
	}
}

type experienceAwardFailSettlement struct {
	repo.CharacterSettlementUnitOfWork
}

func (u experienceAwardFailSettlement) WithinCharacterSettlement(ctx context.Context, id string, apply func(repo.Group) error) error {
	return u.CharacterSettlementUnitOfWork.WithinCharacterSettlement(ctx, id, func(tx repo.Group) error {
		tx.Account = experienceAwardFailAccount{tx.Account}
		return apply(tx)
	})
}

func TestExperienceAwardBatchCommitsCapsuleAndCharacterAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "提交并重放", true: "账号失败全部回滚"}[fail], func(t *testing.T) {
			ctx := context.Background()
			r := memory.NewMemoryGroup()
			now := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)
			for _, err := range []error{
				r.Account.Save(ctx, repo.AccountRecord{AccountID: "a", Metadata: map[string]string{"保留": "原值"}}),
				r.Character.Save(ctx, repo.CharacterRecord{CharacterID: "1", AccountID: "a", Level: 90, Stats: map[string]int64{"exp": 0}}),
				r.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1", Points: repo.SkillPointState{SyncedLevel: 90}}),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			config := adventuregroup.RuntimeConfig{Capsule: adventuregroup.CapsuleConfig{MinimumExperience: 10, MaximumExperience: 20}}
			cmd := ExperienceAwardCommand{UpdatedAt: now, AdventureRuntime: config, MaximumLevelGains: []uint32{11, 12},
				Plan: progression.CommitPlan{CharacterID: "1", AccountID: "a",
					Expected:               progression.CommitState{Level: 90, SkillPoints: repo.SkillPointState{SyncedLevel: 90}},
					Next:                   progression.CommitState{Level: 90, Experience: 23, SkillPoints: repo.SkillPointState{SyncedLevel: 90}},
					ExpectedCharacterStats: map[string]int64{"honor_expert_progress_experience": 0}, NextCharacterStats: map[string]int64{"honor_expert_progress_experience": 23}}}
			if fail {
				r.CharacterSettlement = experienceAwardFailSettlement{r.CharacterSettlement}
			}
			o, err := NewOwner(r)
			if err != nil {
				t.Fatal(err)
			}
			got, err := o.CommitExperienceAward(ctx, cmd)
			if fail != (err != nil) {
				t.Fatalf("事务结果：%+v %v", got, err)
			}
			if !fail {
				replay, err := o.CommitExperienceAward(ctx, cmd)
				if err != nil || !replay.Idempotent {
					t.Fatalf("重放重复入账：%+v %v", replay, err)
				}
			}
			c, _, _ := r.Character.Load(ctx, "1")
			a, _, _ := r.Account.Load(ctx, "a")
			state, err := adventuregroup.ParseRuntimeState(a, config, now)
			if err != nil {
				t.Fatal(err)
			}
			wantExp, wantCapsule := int64(23), uint64(20)
			if fail {
				wantExp, wantCapsule = 0, 0
			}
			if c.Stats["exp"] != wantExp || c.Stats["honor_expert_progress_experience"] != wantExp || a.HonorExp != uint64(wantExp) || state.GrowthExperience != wantCapsule || a.Metadata["保留"] != "原值" {
				t.Fatalf("经验、熟练度和胶囊不一致：char=%+v account=%+v capsule=%+v", c, a, state)
			}
		})
	}
}
