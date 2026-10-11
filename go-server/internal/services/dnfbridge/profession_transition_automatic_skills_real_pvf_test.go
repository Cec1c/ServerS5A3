package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	profession "longheng.io/server/internal/modules/dnf/profession"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// A shared fixed-level parser also feeds profession quest rewards. Exercise
// transitions produced by the real planner: class change enters stage zero;
// awakening enters stage one. Actual PVF grants and interval rules determine
// the expected automatic levels, while unrelated skills and ledgers retain
// their existing transition-specific behavior.
func TestRealPVFProfessionTransitionAutomaticSkills(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run this real-PVF regression")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := profession.LoadProfiles(ctx, archive, catalog)
	if err != nil {
		t.Fatal(err)
	}

	const (
		job            = byte(0) // 鬼剑士
		branch         = byte(1) // 剑魂（真档 [growtype 2]）
		stage          = byte(1) // 一觉
		basicTraining  = uint16(174)
		awakening86    = uint16(86)
		otherAwakening = uint16(87)
		oldSkillID     = int64(2) // 不在 branch1/stage1 授予集合里的普通技能
	)
	classTransition, err := profiles.PlanTransition(job, 0, profession.Request{
		Kind: profession.KindClassChange, ChainType: 1, GrowNumber: branch,
	})
	if err != nil || classTransition.AwakeningStage != 0 {
		t.Fatalf("actual class-change plan=%+v err=%v", classTransition, err)
	}
	awakeningTransition, err := profiles.PlanTransition(job, branch, profession.Request{
		Kind: profession.KindAwakening, ChainType: 2, GrowNumber: stage,
	})
	if err != nil || awakeningTransition.AwakeningStage != stage {
		t.Fatalf("actual awakening plan=%+v err=%v", awakeningTransition, err)
	}
	grants, ok := profiles.FreeGrants(job, branch, stage)
	if !ok {
		t.Fatal("real profile for job0 swordman missing")
	}
	levels := map[uint16]int{}
	for _, g := range grants {
		levels[g.SkillID] = g.Level
	}
	if levels[awakening86] != 1 {
		t.Fatalf("real grant 86=%d want 1 (grants=%v)", levels[awakening86], grants)
	}
	if _, present := levels[otherAwakening]; present {
		t.Fatalf("87 must live on another branch, but branch1 grants %v", grants)
	}
	if _, present := levels[uint16(oldSkillID)]; present {
		t.Fatalf("old skill %d unexpectedly part of the branch-1 grant set", oldSkillID)
	}
	otherBranch := byte(0xff)
	for b := byte(0); b <= 5; b++ {
		g, ok := profiles.FreeGrants(job, b, stage)
		if !ok {
			continue
		}
		for _, item := range g {
			if item.SkillID == otherAwakening {
				otherBranch = b
			}
		}
	}
	if otherBranch == 0xff || otherBranch == branch {
		t.Fatalf("real profile does not place awakening 87 on a different branch (found=%d)", otherBranch)
	}
	defBasic, ok := catalog.Find(job, basicTraining)
	if !ok {
		t.Fatal("catalog missing 174")
	}
	defAwake, ok := catalog.Find(job, awakening86)
	if !ok {
		t.Fatal("catalog missing 86")
	}
	t.Logf("real facts: 86 grant=%d fixed=%t level90=%d | 174 fixed=%t level90=%d | 87 branch=%d",
		levels[awakening86], defAwake.FixedLevelSkill, defAwake.FixedLevelForCharacter(90),
		defBasic.FixedLevelSkill, defBasic.FixedLevelForCharacter(90), otherBranch)

	for _, characterLevel := range []int{50, 90} {
		wantBasic := characterLevel            // 174: 1 + (level-1)/1*1
		wantAwake := 1 + (characterLevel-50)/5 // actual required50 / interval5 / increment1
		if got := defAwake.FixedLevelForCharacter(characterLevel); got != wantAwake {
			t.Errorf("automatic awakening level at character=%d: got %d want %d", characterLevel, got, wantAwake)
		}

		// ---- KindClassChange：授予集替换技能、冷却清空、SP/TP 回上限 ----
		points := repo.SkillPointState{TotalSP: 100, RemainingSP: 20, TotalTP: 10, RemainingTP: 3, SyncedLevel: characterLevel}
		before := repo.SkillRecord{
			CharacterID: "cr41",
			Skills: map[int64]repo.SkillState{
				oldSkillID:         {Level: 5, Enabled: true},
				int64(awakening86): {Level: 30, Enabled: true},
			},
			Cooldowns: map[int64]time.Time{int64(awakening86): time.Unix(1234, 0).UTC()},
			Layouts:   map[int]repo.SkillLayout{0: {0: uint16(oldSkillID)}},
			Points:    points,
		}
		beforeImage := repo.CloneSkill(before)
		classOut, err := profiles.ApplySkillTransition(catalog, job, characterLevel,
			classTransition, before, points)
		if err != nil {
			t.Fatalf("class change level=%d: %v", characterLevel, err)
		}
		if got := classOut.Skills[int64(basicTraining)].Level; got != wantBasic {
			t.Errorf("[shared parser impact] class change level=%d: 174=%d want %d (skill/parse.go fixed-level container feeds ApplySkillTransition)",
				characterLevel, got, wantBasic)
		}
		if _, present := classOut.Skills[int64(awakening86)]; present {
			t.Errorf("stage-zero class change level=%d granted awakening86", characterLevel)
		}
		if _, present := classOut.Skills[int64(otherAwakening)]; present {
			t.Errorf("class change level=%d granted another branch awakening 87", characterLevel)
		}
		if _, present := classOut.Skills[oldSkillID]; present {
			t.Errorf("class change must replace the skill set; old skill %d survived", oldSkillID)
		}
		if classOut.Cooldowns != nil {
			t.Errorf("class change must clear cooldowns, got %v", classOut.Cooldowns)
		}
		if classOut.Points.RemainingSP != classOut.Points.TotalSP || classOut.Points.RemainingTP != classOut.Points.TotalTP {
			t.Errorf("class change points=%+v want SP/TP back to the totals", classOut.Points)
		}
		if len(classOut.Layouts[0]) == 0 {
			t.Errorf("class change layout not rebuilt")
		}
		if !reflect.DeepEqual(before, beforeImage) {
			t.Fatalf("ApplySkillTransition mutated the caller record: %+v", before)
		}

		// ---- KindAwakening：旧技能保留、只升不降、点数不动、冷却保留 ----
		awakenPoints := repo.SkillPointState{TotalSP: 100, RemainingSP: 20, TotalTP: 10, RemainingTP: 3, SyncedLevel: characterLevel}
		awakeBefore := repo.SkillRecord{
			CharacterID: "cr41",
			Skills: map[int64]repo.SkillState{
				oldSkillID:         {Level: 5, Enabled: true},
				int64(awakening86): {Level: 30, Enabled: true},
			},
			Cooldowns: map[int64]time.Time{int64(awakening86): time.Unix(1234, 0).UTC()},
			Layouts:   map[int]repo.SkillLayout{0: {0: uint16(oldSkillID), 1: awakening86}},
			Points:    awakenPoints,
		}
		awakeBeforeImage := repo.CloneSkill(awakeBefore)
		awakeOut, err := profiles.ApplySkillTransition(catalog, job, characterLevel,
			awakeningTransition, awakeBefore, awakenPoints)
		if err != nil {
			t.Fatalf("awakening level=%d: %v", characterLevel, err)
		}
		if got := awakeOut.Skills[int64(basicTraining)].Level; got != wantBasic {
			t.Errorf("[shared parser impact] awakening level=%d: 174=%d want %d", characterLevel, got, wantBasic)
		}
		if got := awakeOut.Skills[int64(awakening86)].Level; got != 30 {
			t.Errorf("awakening level=%d must only raise: 86=%d want 30 (untouched)", characterLevel, got)
		}
		if got := awakeOut.Skills[oldSkillID].Level; got != 5 {
			t.Errorf("awakening level=%d dropped unrelated skill %d to %d", characterLevel, oldSkillID, got)
		}
		if awakeOut.Points.RemainingSP != 20 || awakeOut.Points.RemainingTP != 3 {
			t.Errorf("awakening level=%d must keep the SP/TP ledger, got %+v", characterLevel, awakeOut.Points)
		}
		if len(awakeOut.Cooldowns) != 1 {
			t.Errorf("awakening level=%d must keep cooldowns, got %v", characterLevel, awakeOut.Cooldowns)
		}
		if len(awakeOut.Layouts[0]) == 0 {
			t.Errorf("awakening level=%d must keep a layout", characterLevel)
		}
		if !reflect.DeepEqual(awakeBefore, awakeBeforeImage) {
			t.Fatal("awakening modified its caller record")
		}
	}

	// 只升不降的补证：低于 fixed 等级时，觉醒必须把它抬到真档等级（90 级 -> 9）。
	lowPoints := repo.SkillPointState{TotalSP: 100, RemainingSP: 20, TotalTP: 10, RemainingTP: 3, SyncedLevel: 90}
	lowBefore := repo.SkillRecord{
		CharacterID: "cr41",
		Skills:      map[int64]repo.SkillState{int64(awakening86): {Level: 1, Enabled: true}},
		Layouts:     map[int]repo.SkillLayout{0: {0: awakening86}},
		Points:      lowPoints,
	}
	lowOut, err := profiles.ApplySkillTransition(catalog, job, 90,
		awakeningTransition,
		lowBefore, lowPoints)
	if err != nil {
		t.Fatalf("awakening raise case: %v", err)
	}
	if got := lowOut.Skills[int64(awakening86)].Level; got != 9 {
		t.Errorf("[shared parser impact] awakening raise: 86=%d want 9 at level 90", got)
	}
}
