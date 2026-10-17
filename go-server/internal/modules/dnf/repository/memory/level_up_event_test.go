package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func TestMemoryLevelUpHistorySurvivesStaleOrdinaryStatsSave(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	before := repository.CharacterRecord{CharacterID: "77", AccountID: "a", Level: 84, Stats: map[string]int64{"exp": 1}}
	if err := repos.Character.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	pending := repository.CloneCharacter(before)
	pending.Stats[repository.LevelUpEventKey(85)] = 123
	if err := repos.Character.Save(ctx, pending); err != nil {
		t.Fatal(err)
	}
	consumed := repository.CloneCharacter(pending)
	consumed.Stats[repository.LevelUpEventKey(85)] = -123
	consumed.Stats["level_up_reward_mail_85"] = 9
	if err := repos.Character.Save(ctx, consumed); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []repository.CharacterRecord{before, pending} {
		if err := repository.SaveCharacterFields(ctx, repos.Character, stale, repository.CharacterFieldStats); err != nil {
			t.Fatal(err)
		}
		got, _, _ := repos.Character.Load(ctx, "77")
		if got.Stats[repository.LevelUpEventKey(85)] != -123 || got.Stats["level_up_reward_mail_85"] != 9 {
			t.Fatalf("stale snapshot erased receipt=%+v", got)
		}
	}
}

func TestMemoryPhysicalProgressionFailureRollsBackProtectedLevelUpEvent(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	seedCharacterProgression(t, ctx, repos)
	failure := errors.New("physical skill commit failure")
	uow := &memoryCharacterProgressionUnitOfWork{character: repos.Character, skills: &mutatingProgressionSkillStore{SkillRepository: repos.Skill, saveErr: failure}}
	err := uow.WithinCharacterProgression(ctx, "77", func(characters repository.CharacterRepository, skills repository.SkillRepository) error {
		c, _, _ := characters.Load(ctx, "77")
		sk, _, _ := skills.Load(ctx, "77")
		c.Level = 3
		c.Stats[repository.LevelUpEventKey(3)] = 123
		if err := characters.Save(ctx, c); err != nil {
			return err
		}
		return skills.Save(ctx, sk)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("failure=%v", err)
	}
	c, _, _ := repos.Character.Load(ctx, "77")
	if c.Level != 2 || repository.HasPendingLevelUp(c) {
		t.Fatalf("failed transaction preserved uncommitted event=%+v", c)
	}
}

func TestRedisWrapperPreservesLevelUpRecoveryReader(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	cached := repository.NewRedisCachedCharacterRepository(repos.Character, newFakeRedisExecutor(), repository.RedisCacheOptions{})
	if err := repos.Character.Save(ctx, repository.CharacterRecord{CharacterID: "77", Stats: map[string]int64{repository.LevelUpEventKey(85): 123}}); err != nil {
		t.Fatal(err)
	}
	reader, ok := cached.(repository.PendingLevelUpReader)
	if !ok {
		t.Fatal("cache wrapper hides durable recovery")
	}
	ids, err := reader.ListPendingLevelUpCharacters(ctx, "", 128)
	if err != nil || len(ids) != 1 || ids[0] != "77" {
		t.Fatalf("recovery=%v %v", ids, err)
	}
}
