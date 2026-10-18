package quest

import (
	"context"
	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFQuestFinishLevelCapAndHonor(t *testing.T) {
	ctx := context.Background()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify capped quest settlement")
	}
	source, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	def := catalog.byID[2941]
	threshold89, err := tables.ThresholdToNext(89)
	if err != nil {
		t.Fatal(err)
	}
	threshold90, err := tables.ThresholdToNext(90)
	if err != nil {
		t.Fatal(err)
	}
	threshold91, err := tables.ThresholdToNext(91)
	if err != nil {
		t.Fatal(err)
	}
	honorTables, err := dnfhonor.LoadTables(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("quest=%d name=%s type=%s grade=%s path=%s thresholds89/90/91=%d/%d/%d honorRows=%d", def.ID, def.Name, def.Type, def.Grade, def.Path, threshold89, threshold90, threshold91, len(honorTables.ExpertExperienceRows()))
	repos := finishOwnerSeed(t, ctx)
	character, _, _ := repos.Character.Load(ctx, "19")
	character.Level = 90
	character.Stats["grow_type"] = 36
	character.Stats["exp"] = 1961381243
	character.Stats[dnfhonor.ExpertLevelStatKey] = 58
	character.Stats[dnfhonor.ExpertProgressExperienceStatKey] = 6338510
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := repos.Skill.Load(ctx, "19")
	skill.Points = dnfrepo.SkillPointState{TotalSP: 11970, RemainingSP: 270, TotalTP: 41, SyncedLevel: 90}
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		2941: {Status: "active", Extra: map[string]string{"completion_key": "seeker-level91-repro", "completion_kind": "clear_map", "reward_state": "pending"}},
	}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := finishOwnerInput(tables, finishOwnerAllocator(nil))
	input.QuestID = 2941
	input.ExpectedCompletionKey = "seeker-level91-repro"
	input.ExperienceBonusPercent = 20
	input.LoadHonorTables = func(context.Context) (*dnfhonor.Tables, error) { return honorTables, nil }
	result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	gotSkill, _, err := repos.Skill.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	honorGain, err := dnfhonor.CalculateHonorExperienceGain(90, 1961381243, result.ExperienceDelta, dnfhonor.CharacterExperienceCap{MaxLevel: 90, MaxLevelEntryExperience: threshold89})
	if err != nil {
		t.Fatal(err)
	}
	expectedHonor, err := honorTables.AdvanceExpert(dnfhonor.ExpertProgress{Level: 58, CurrentLevelExperience: 6338510}, uint64(honorGain))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual: base=%d bonus=%d total=%d level=%d->%d exp=%d->%d SPdelta=%d TPdelta=%d skill=%+v persistedLevel=%d honor=%d/%d; existingHonorRuleGain=%d nextHonor=%+v", result.BaseExperienceDelta, result.ExperienceBonusDelta, result.ExperienceDelta, result.PreviousLevel, result.NewLevel, result.PreviousExperience, result.NewExperience, result.SPDelta, result.TPDelta, gotSkill.Points, got.Level, got.Stats[dnfhonor.ExpertLevelStatKey], got.Stats[dnfhonor.ExpertProgressExperienceStatKey], honorGain, expectedHonor)
	replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	t.Logf("replayed=true replayLevel=%d", replay.NewLevel)
	if result.NewLevel != 90 || got.Level != 90 || gotSkill.Points != skill.Points || result.SPDelta != 0 || result.TPDelta != 0 {
		t.Fatalf("max-level quest settlement escaped cap90: level=%d SP=%d TP=%d", got.Level, result.SPDelta, result.TPDelta)
	}
	if result.ExperienceDelta != 995256 || result.HonorExpertGain != result.ExperienceDelta ||
		got.Stats[dnfhonor.ExpertLevelStatKey] != int64(expectedHonor.Level) ||
		got.Stats[dnfhonor.ExpertProgressExperienceStatKey] != int64(expectedHonor.CurrentLevelExperience) ||
		replay.HonorExpertGain != result.HonorExpertGain {
		t.Fatalf("honor settlement differs: result=%+v stats=%+v", result, got.Stats)
	}
}
