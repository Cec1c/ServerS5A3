package quest

import (
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The Elvengard renewal mainline hands the level-15 character to one
// "守护森林的战斗" branch variant per job, and only a completed branch variant
// unlocks 3155 (赛丽亚的决心); the class-change (转职) quests hang off 3155.
//
// DOC-116 (角色 尊, guardian) reported the same shape: after the level-15
// mainline neither the branch mainline nor the follow-up appeared. DOC-123
// reports it for 女圣职者 (job 14 = Priest/ATPriest.chr). The root cause was the
// `[target character]` tuple comparison: every real branch variant carries a
// `-1` component (unrestricted), and the old exact comparison never matched a
// packed grow-type nibble, so only the inert dummy 6929 stayed acceptable and
// the chain dead-ended. Fix #2da7982 treats `-1` as unrestricted. These tests
// pin the corrected chain for the reported job and for every other job so the
// same class of report cannot regress silently.

const (
	level15BranchMinID     = int64(6929)
	level15BranchMaxID     = int64(7002)
	level15BranchDummyID   = int64(6929)
	level15BranchUnlockID  = int64(3155)
	level15BranchVariantOK = "epic"
)

func level15BranchRealPVFCatalog(t *testing.T) *Catalog {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
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
	return catalog
}

func level15BranchRecord(completed ...int64) dnfrepo.QuestRecord {
	states := make(map[int64]dnfrepo.QuestState, len(completed))
	for _, id := range completed {
		states[id] = dnfrepo.QuestState{Status: "completed"}
	}
	return dnfrepo.QuestRecord{CharacterID: "1", States: states}
}

func level15BranchListed(catalog *Catalog, level, job, grow int, record dnfrepo.QuestRecord) map[int64]bool {
	listed := make(map[int64]bool)
	for _, id := range catalog.QuestList(CharacterEligibility{Level: level, Job: job, GrowType: grow}, record).IDs {
		listed[int64(id)] = true
	}
	return listed
}

// level15BranchVariants returns the job-specific (non-dummy) branch variants of
// the job that the acceptable-quest list offers after the level-15 mainline.
func level15BranchVariants(catalog *Catalog, job int, listed map[int64]bool) []int64 {
	variants := make([]int64, 0, 6)
	for id := level15BranchMinID; id <= level15BranchMaxID; id++ {
		if !listed[id] || id == level15BranchDummyID {
			continue
		}
		definition, ok := catalog.Find(id)
		if !ok || normalizeQuestTag(definition.Grade) != level15BranchVariantOK {
			continue
		}
		variants = append(variants, id)
	}
	return variants
}

// level15JobChangeQuestIDs returns the job's ordinary class-change quests: they
// carry [job change quest] 1, grant [grow type] and list 3155 as a prerequisite.
func level15JobChangeQuestIDs(catalog *Catalog, job int) []int64 {
	ids := make([]int64, 0, 4)
	for _, definition := range catalog.ordered {
		if definition.JobChangeQuest != 1 || normalizeQuestTag(definition.RewardType) != "grow type" {
			continue
		}
		if !jobmap.MatchesQuestTag(definition.Job, job) {
			continue
		}
		found := false
		for _, group := range definition.PreRequiredGroups {
			for _, id := range group {
				if id == level15BranchUnlockID {
					found = true
				}
			}
		}
		if !found {
			continue
		}
		ids = append(ids, definition.ID)
	}
	return ids
}

// TestRealPVFAtPriestLevel15BranchReachesClassChange pins the exact DOC-123
// report: 女圣职者 (job 14) after the level-15 mainline must be offered her own
// "守护森林的战斗" branch variants, the branch must unlock 3155, and 3155 must
// offer 转职 - 圣骑士 / 异端审判者 / 巫女 / 诱魔者.
func TestRealPVFAtPriestLevel15BranchReachesClassChange(t *testing.T) {
	catalog := level15BranchRealPVFCatalog(t)
	mainline := []int64{3151, 3152, 3153}

	definition, ok := catalog.Find(6945)
	if !ok || definition.Name != "守护森林的战斗" || len(definition.TargetCharacterRules) != 1 ||
		definition.TargetCharacterRules[0].JobTag != "[at priest]" ||
		definition.TargetCharacterRules[0].FirstGrowType != -1 ||
		definition.TargetCharacterRules[0].AwakeningStage != -1 {
		t.Fatalf("actual at-priest generic branch changed: %+v", definition)
	}
	named, ok := catalog.Find(6946)
	if !ok || named.Name != "守护森林的战斗 - 圣骑士" || len(named.TargetCharacterRules) != 1 ||
		named.TargetCharacterRules[0].FirstGrowType != 0 ||
		named.TargetCharacterRules[0].AwakeningStage != -1 {
		t.Fatalf("actual at-priest paladin branch changed: %+v", named)
	}

	afterMainline := level15BranchListed(catalog, 15, 14, 0, level15BranchRecord(mainline...))
	for _, want := range []int64{6929, 6945, 6946, 6947, 6948, 6949} {
		if !afterMainline[want] {
			t.Fatalf("at-priest level-15 branch quest %d not offered after the mainline; listed=%v", want, sortedQuestIDs(afterMainline))
		}
	}
	if decision, err := catalog.DecideAccept(CharacterEligibility{Level: 15, Job: 14, GrowType: 0}, level15BranchRecord(mainline...), 6945); err != nil || decision != AcceptNew {
		t.Fatalf("at-priest generic branch not acceptable: decision=%v err=%v", decision, err)
	}

	unlock := level15BranchListed(catalog, 15, 14, 0, level15BranchRecord(append(append([]int64{}, mainline...), 6945)...))
	if !unlock[level15BranchUnlockID] {
		t.Fatalf("completed at-priest branch did not unlock %d; listed=%v", level15BranchUnlockID, sortedQuestIDs(unlock))
	}

	transfer := level15BranchListed(catalog, 15, 14, 0, level15BranchRecord(append(append([]int64{}, mainline...), 6945, level15BranchUnlockID)...))
	for _, want := range []int64{2908, 2909, 2910, 2911} {
		if !transfer[want] {
			t.Fatalf("at-priest class-change quest %d not offered after %d; listed=%v", want, level15BranchUnlockID, sortedQuestIDs(transfer))
		}
	}
}

// TestRealPVFEveryJobLevel15BranchReachesClassChange runs the same chain for
// every job the current character.lst exposes, so a job-specific blind spot like
// DOC-123 cannot reappear unnoticed. Dark Knight (9) and Creator (10) are exempt
// from the ordinary transfer family: their realization quests carry no 3155
// prerequisite in this PVF.
func TestRealPVFEveryJobLevel15BranchReachesClassChange(t *testing.T) {
	catalog := level15BranchRealPVFCatalog(t)
	mainline := []int64{3151, 3152, 3153}
	for job := 0; job <= jobmap.MaxID; job++ {
		if !jobmap.Valid(job) {
			continue
		}
		t.Run(jobTagLabel(job), func(t *testing.T) {
			afterMainline := level15BranchListed(catalog, 15, job, 0, level15BranchRecord(mainline...))
			variants := level15BranchVariants(catalog, job, afterMainline)
			if len(variants) == 0 {
				t.Fatalf("job=%d has no level-15 branch variant after the mainline; listed=%v", job, sortedQuestIDs(afterMainline))
			}
			branch := variants[0]
			unlock := level15BranchListed(catalog, 15, job, 0, level15BranchRecord(append(append([]int64{}, mainline...), branch)...))
			if !unlock[level15BranchUnlockID] {
				t.Fatalf("job=%d branch %d did not unlock %d", job, branch, level15BranchUnlockID)
			}
			transfer := level15JobChangeQuestIDs(catalog, job)
			if job == 9 || job == 10 {
				if len(transfer) != 0 {
					t.Fatalf("job=%d should stay outside the ordinary transfer family, found %v", job, transfer)
				}
				return
			}
			if len(transfer) == 0 {
				t.Fatalf("job=%d has no class-change quest after %d", job, level15BranchUnlockID)
			}
			listed := level15BranchListed(catalog, 15, job, 0, level15BranchRecord(append(append([]int64{}, mainline...), branch, level15BranchUnlockID)...))
			for _, want := range transfer {
				if !listed[want] {
					t.Fatalf("job=%d class-change quest %d not offered after %d; listed=%v", job, want, level15BranchUnlockID, sortedQuestIDs(listed))
				}
			}
		})
	}
}

func jobTagLabel(job int) string {
	tags := jobmap.QuestTags(job)
	if len(tags) == 0 {
		return "job"
	}
	label := tags[0]
	if len(label) >= 2 {
		label = label[1 : len(label)-1]
	}
	return label
}

func sortedQuestIDs(set map[int64]bool) []int64 {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
	return ids
}
