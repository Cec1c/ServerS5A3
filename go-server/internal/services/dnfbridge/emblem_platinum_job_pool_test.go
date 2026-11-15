package dnfbridge

import (
	"context"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCurrentEmblemRewardSkillJobsRequiresKnownNumericGenderTags(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		skill bool
		jobs  uint16
		valid bool
	}{
		{"ordinary", "[enchant]\n[physical attack] 1\n[/enchant]\n", false, 0, true},
		{"male_gunner", "[enchant]\n[skill levelup]\n`[gunner]` 1 1\n[/skill levelup]\n[/enchant]\n", true, uint16(1) << 2, true},
		{"female_gunner", "[enchant]\n[skill levelup]\n`[at gunner]` 1 1 `[gunner]` 2 1\n[/skill levelup]\n[/enchant]\n", true, uint16(1)<<2 | uint16(1)<<5, true},
		{"all", "[enchant]\n[skill levelup]\n`[all]` 1 1\n[/skill levelup]\n[/enchant]\n", true, currentEmblemAllBaseJobs, true},
		{"unknown", "[enchant]\n[skill levelup]\n`[unknown job]` 1 1\n[/skill levelup]\n[/enchant]\n", true, 0, false},
		{"incomplete", "[enchant]\n[skill levelup]\n`[gunner]` 1\n[/skill levelup]\n[/enchant]\n", true, 0, false},
		{"nonpositive", "[enchant]\n[skill levelup]\n`[gunner]` 0 1\n[/skill levelup]\n[/enchant]\n", true, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, err := dnfpvf.Parse(test.name+".stk", test.body)
			if err != nil {
				t.Fatal(err)
			}
			skill, jobs, valid := currentEmblemRewardSkillJobs(document)
			if skill != test.skill || jobs != test.jobs || valid != test.valid {
				t.Fatalf("skill=%t jobs=%#x valid=%t want %t/%#x/%t", skill, jobs, valid, test.skill, test.jobs, test.valid)
			}
		})
	}
}

func TestCurrentEmblemPlatinumPoolUsesCharacterBaseJob(t *testing.T) {
	_, catalog := mustCurrentPlatinumEmblemTestAssets(t, "2")
	analysis, err := catalog.currentEmblemBoosterAnalysis(9000)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.HasSkill {
		t.Fatal("skill platinum booster was not recognized")
	}
	for _, test := range []struct {
		job  byte
		want []uint32
	}{
		{0, []uint32{9103}},
		{2, []uint32{9101, 9104}},
		{5, []uint32{9102, 9104}},
		{9, []uint32{9105}},
		{10, []uint32{9106}},
	} {
		pool, err := currentEmblemBoosterCandidatesForJob(analysis, test.job, true)
		if err != nil {
			t.Fatalf("job=%d: %v", test.job, err)
		}
		if got := currentEmblemCandidateIDs(pool); !currentEmblemSameIDs(got, test.want) {
			t.Fatalf("job=%d candidates=%v want=%v", test.job, got, test.want)
		}
	}
	if _, err := currentEmblemBoosterCandidatesForJob(analysis, 4, true); err == nil {
		t.Fatal("empty priest platinum pool did not fail closed")
	}
	if _, err := currentEmblemBoosterCandidatesForJob(analysis, 0, false); err == nil {
		t.Fatal("invalid character job did not fail closed")
	}

	ordinary, err := catalog.currentEmblemBoosterAnalysis(9001)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := currentEmblemBoosterCandidatesForJob(ordinary, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := currentEmblemCandidateIDs(pool); len(got) != 1 || got[0] != 9200 {
		t.Fatalf("ordinary platinum pool=%v want unchanged [9200]", got)
	}
}

func TestCurrentEmblemPlatinumSynthesisConsumesOnlyForMatchingBaseJob(t *testing.T) {
	for _, test := range []struct {
		job  string
		want []uint32
	}{
		{"0", []uint32{9103}},
		{"2", []uint32{9101, 9104}},
		{"5", []uint32{9102, 9104}},
		{"9", []uint32{9105}},
		{"10", []uint32{9106}},
	} {
		t.Run("job_"+test.job, func(t *testing.T) {
			repositories, catalog := mustCurrentPlatinumEmblemTestAssets(t, test.job)
			request, err := parseCurrentEmblemCompoundRequest(currentEmblemCompoundTestBody([]currentEmblemCompoundInput{
				{ItemID: 8000, Slot: 70},
				{ItemID: 8000, Slot: 71},
			}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := currentDisjointTestService(repositories, catalog).commitCurrentEmblemCompound(
				context.Background(),
				&gameSession{selectedCharacterID: 19},
				catalog,
				request,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Rewards) != 1 || !currentEmblemContainsID(test.want, result.Rewards[0].ItemID) {
				t.Fatalf("job=%s reward=%+v want one of %v", test.job, result.Rewards, test.want)
			}
		})
	}
}

func TestCurrentEmblemPlatinumRejectPreservesInputs(t *testing.T) {
	for _, job := range []string{"", "4"} {
		t.Run("job_"+job, func(t *testing.T) {
			repositories, catalog := mustCurrentPlatinumEmblemTestAssets(t, job)
			request, err := parseCurrentEmblemCompoundRequest(currentEmblemCompoundTestBody([]currentEmblemCompoundInput{
				{ItemID: 8000, Slot: 70},
				{ItemID: 8000, Slot: 71},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := currentDisjointTestService(repositories, catalog).commitCurrentEmblemCompound(
				context.Background(),
				&gameSession{selectedCharacterID: 19},
				catalog,
				request,
			); err == nil {
				t.Fatalf("job=%q unexpectedly synthesized", job)
			}
			inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("inventory found=%t err=%v", found, err)
			}
			for _, slot := range []string{"0:70", "0:71"} {
				if stack := inventory.Slots[slot]; stack.ItemID != 8000 || stack.Count != 1 {
					t.Fatalf("job=%q source %s mutated: %+v", job, slot, stack)
				}
			}
		})
	}
}

func TestRealPVFEmblemPlatinumPoolsUseExactBaseJobTags(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := catalog.currentEmblemBoosterAnalysis(10095593)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.HasSkill || len(analysis.Candidates) < 900 {
		t.Fatalf("runtime platinum pool has_skill=%t candidates=%d", analysis.HasSkill, len(analysis.Candidates))
	}
	idsByJob := make(map[byte]map[uint32]struct{}, 16)
	for job := byte(0); job <= 15; job++ {
		pool, err := currentEmblemBoosterCandidatesForJob(analysis, job, true)
		if err != nil {
			t.Fatalf("job=%d: %v", job, err)
		}
		idsByJob[job] = make(map[uint32]struct{})
		for _, candidate := range pool {
			if candidate.Skill && candidate.Jobs&(uint16(1)<<uint(job)) == 0 {
				t.Fatalf("job=%d received cross-job candidate=%d mask=%#x", job, candidate.ItemID, candidate.Jobs)
			}
			idsByJob[job][candidate.ItemID] = struct{}{}
		}
		if len(idsByJob[job]) == 0 {
			t.Fatalf("job=%d has an empty runtime platinum pool", job)
		}
		t.Logf("job=%d unique_candidates=%d", job, len(idsByJob[job]))
	}
	if currentEmblemSameIDSet(idsByJob[2], idsByJob[5]) {
		t.Fatal("male and female gunner runtime platinum pools are identical")
	}
}

func mustCurrentPlatinumEmblemTestAssets(t *testing.T, job string) (dnfrepo.Group, *pvfDungeonDropCatalog) {
	t.Helper()
	source := bridgePVFSource{
		"monster/monster.lst":      "",
		"equipment/equipment.lst":  "",
		"equipment/pricetable.tbl": "[rate] 1000 150 30\n",
		"stackable/stackable.lst": "" +
			"3037 `cube.stk`\n" +
			"8000 `platinum_input.stk`\n" +
			"9000 `platinum_booster.stk`\n" +
			"9001 `ordinary_booster.stk`\n" +
			"9101 `platinum_gunner.stk`\n" +
			"9102 `platinum_at_gunner.stk`\n" +
			"9103 `platinum_swordman.stk`\n" +
			"9104 `platinum_shared_gunner.stk`\n" +
			"9105 `platinum_demonic_swordman.stk`\n" +
			"9106 `platinum_creator_mage.stk`\n" +
			"9200 `ordinary_platinum.stk`\n",
		"stackable/cube.stk":                      "[stackable type] `[material]`\n[stack limit] 999\n",
		"stackable/platinum_input.stk":            "[stackable type] `[avatar emblem]`\n[grade] 4\n[stack limit] 999\n",
		"stackable/platinum_booster.stk":          "[stackable type] `[booster]`\n[booster info]\n[stackable]\n1 9101 100 1 9102 100 1 9103 100 1 9104 100 1 9105 100 1 9106 100 1\n[/stackable]\n[/booster info]\n",
		"stackable/ordinary_booster.stk":          "[stackable type] `[booster]`\n[booster info]\n[stackable]\n1 9200 1 1\n[/stackable]\n[/booster info]\n",
		"stackable/platinum_gunner.stk":           "[stackable type]\n`[avatar emblem]`\n[grade] 4\n[enchant]\n[skill levelup]\n`[gunner]` 1 1\n[/skill levelup]\n[/enchant]\n[stack limit] 999\n",
		"stackable/platinum_at_gunner.stk":        "[stackable type]\n`[avatar emblem]`\n[grade] 4\n[enchant]\n[skill levelup]\n`[at gunner]` 1 1\n[/skill levelup]\n[/enchant]\n[stack limit] 999\n",
		"stackable/platinum_swordman.stk":         "[stackable type]\n`[avatar emblem]`\n[grade] 4\n[enchant]\n[skill levelup]\n`[swordman]` 1 1\n[/skill levelup]\n[/enchant]\n[stack limit] 999\n",
		"stackable/platinum_shared_gunner.stk":    "[stackable type]\n`[avatar emblem]`\n[grade] 4\n[enchant]\n[skill levelup]\n`[gunner]` 1 1 `[at gunner]` 2 1\n[/skill levelup]\n[/enchant]\n[stack limit] 999\n",
		"stackable/platinum_demonic_swordman.stk": "[stackable type]\n`[avatar emblem]`\n[grade] 4\n[enchant]\n[skill levelup]\n`[demonic swordman]` 1 1\n[/skill levelup]\n[/enchant]\n[stack limit] 999\n",
		"stackable/platinum_creator_mage.stk":     "[stackable type]\n`[avatar emblem]`\n[grade] 4\n[enchant]\n[skill levelup]\n`[creator mage]` 1 1\n[/skill levelup]\n[/enchant]\n[stack limit] 999\n",
		"stackable/ordinary_platinum.stk":         "[stackable type] `[avatar emblem]`\n[grade] 4\n[stack limit] 999\n",
		"etc/disjoint.etc": "[cube index]\n`[no element]` 3037\n[/cube index]\n" +
			"[cube creation const]\n150 1\n[/cube creation const]\n" +
			"[additional result]\n0\n[/additional result]\n" +
			"[additional result const]\n0\n[/additional result const]\n" +
			"[additional result expand]\n0\n[/additional result expand]\n" +
			"[additional result expand const]\n0\n[/additional result expand const]\n" +
			"[avatar disjoint info]\n[fighter]\n2559000 2559003 2559006 2559009 2559006 2559001 2559004 2559007 2559010 2559007 2559002 2559005 2559008 2559011 2559014\n[/fighter]\n[/avatar disjoint info]\n",
		"etc/emblemcompound.etc": "[emblem compound info]\n4 2 9000\n[/emblem compound info]\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repositories := mustCurrentNPCShopRepositories(t, 0, map[string]dnfrepo.ItemStack{
		"0:70": {ItemID: 8000, Count: 1},
		"0:71": {ItemID: 8000, Count: 1},
	})
	character, found, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("character found=%t err=%v", found, err)
	}
	character.Job = job
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	return repositories, catalog
}

func currentEmblemCandidateIDs(candidates []currentEmblemBoosterCandidate) []uint32 {
	ids := make([]uint32, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ItemID)
	}
	return ids
}

func currentEmblemSameIDs(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[uint32]int, len(left))
	for _, id := range left {
		set[id]++
	}
	for _, id := range right {
		if set[id] == 0 {
			return false
		}
		set[id]--
	}
	return true
}

func currentEmblemContainsID(ids []uint32, want uint32) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func currentEmblemSameIDSet(left, right map[uint32]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for id := range left {
		if _, ok := right[id]; !ok {
			return false
		}
	}
	return true
}
