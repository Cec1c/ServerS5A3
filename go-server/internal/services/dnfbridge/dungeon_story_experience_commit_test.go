package dnfbridge

import (
	"context"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestStoryExperienceMonsterBaseAndGrowthCommitTogether(t *testing.T) {
	s, runtime, repos := prepareCurrentDungeonMonsterExperienceTest(t)
	runtime.storyExperience = currentDungeonStoryExperienceProfile{QuestID: 3148, RatePercent: 100}
	runtime.Request.Difficulty = 0
	session := &gameSession{selectedCharacterID: 99, accountID: "account-1", dungeon: dungeonSessionState{runtime: runtime}}
	s.premiumCatalog = &currentPremiumCatalog{effectsByType: map[int64]currentPremiumEffectInfo{premium.TypeBonusExp: {BonusExperiencePercent: 20}}}
	if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1", Metadata: map[string]string{"premium_expire_84": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)}}); err != nil {
		t.Fatal(err)
	}
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	resources, err := currentDungeonMonsterExperienceResourcesForCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	monster := runtime.Room.Snapshot().Monsters[0]
	award, err := currentDungeonMonsterExperienceAwardFor(runtime, monster, resources.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if award.Gain != 260 {
		t.Fatalf("story base=%d want260", award.Gain)
	}
	result, err := s.commitCurrentDungeonMonsterExperience(context.Background(), session, runtime, resources.Progression, currentDungeonMonsterExperienceCommitResult{Award: award})
	if err != nil {
		t.Fatal(err)
	}
	if result.GrowthContractBonus != 52 || result.Character.Stats["exp"] != 312 {
		t.Fatalf("story/growth not committed together: %+v", result)
	}
	stored, _, err := repos.Character.Load(context.Background(), "99")
	if err != nil || stored.Stats["exp"] != 312 {
		t.Fatalf("durable experience=%v err=%v", stored.Stats["exp"], err)
	}
}
