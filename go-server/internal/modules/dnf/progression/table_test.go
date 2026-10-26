package progression

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPVFTablesDriveExperienceLevelUpsAndSPTP(t *testing.T) {
	tables, err := Load(context.Background(), progressionTestSource())
	if err != nil {
		t.Fatal(err)
	}
	if got := tables.Snapshot(); got.ExperienceThresholds != 4 || got.SkillPointLevels != 5 ||
		got.TechniquePointLevels != 3 || got.QuestExperienceLevels != 5 || got.QuestDifficulties != 2 {
		t.Fatalf("snapshot = %+v", got)
	}

	experience, err := tables.ApplyExperience(1, 90, 180, 5)
	if err != nil {
		t.Fatal(err)
	}
	if experience.NewLevel != 3 || experience.NewExperience != 270 || experience.LevelsGained != 2 || experience.Saturated {
		t.Fatalf("experience result = %+v", experience)
	}

	points := dnfrepo.SkillPointState{
		TotalSP: 40, RemainingSP: 15, TotalTP: 3, RemainingTP: 1, SyncedLevel: 2,
	}
	advance, err := tables.AdvanceSkillPoints(points, experience.NewLevel)
	if err != nil {
		t.Fatal(err)
	}
	if advance.SPGain != 30 || advance.New.TotalSP != 70 || advance.New.RemainingSP != 45 || advance.New.SyncedLevel != 3 {
		t.Fatalf("SP advance = %+v", advance)
	}
	if advance.TPGain != 2 || advance.New.TotalTP != 5 || advance.New.RemainingTP != 3 {
		t.Fatalf("TP advance = %+v", advance)
	}
	if spentBefore, spentAfter := points.TotalSP-points.RemainingSP, advance.New.TotalSP-advance.New.RemainingSP; spentBefore != spentAfter {
		t.Fatalf("spent SP changed: before=%d after=%d", spentBefore, spentAfter)
	}
}

func TestPlanExperienceAndSkillPointsCouplesLevelAndSPTP(t *testing.T) {
	tables, err := Load(context.Background(), progressionTestSource())
	if err != nil {
		t.Fatal(err)
	}
	points := dnfrepo.SkillPointState{TotalSP: 40, RemainingSP: 15, TotalTP: 3, RemainingTP: 1, SyncedLevel: 2}
	plan, err := tables.PlanExperienceAndSkillPoints(2, 240, 300, 5, points)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Experience.NewLevel != 4 || plan.Experience.NewExperience != 540 {
		t.Fatalf("experience = %+v", plan.Experience)
	}
	if plan.SkillPoints.SPGain != 70 || plan.SkillPoints.New.TotalSP != 110 || plan.SkillPoints.New.RemainingSP != 85 || plan.SkillPoints.New.SyncedLevel != 4 {
		t.Fatalf("skill points = %+v", plan.SkillPoints)
	}
	if plan.SkillPoints.TPGain != 3 || plan.SkillPoints.New.TotalTP != 6 || plan.SkillPoints.New.RemainingTP != 4 {
		t.Fatalf("technique points = %+v", plan.SkillPoints)
	}
	if _, err := tables.PlanExperienceAndSkillPoints(2, 240, 300, 5, dnfrepo.SkillPointState{SyncedLevel: 1}); !errors.Is(err, ErrSkillPointLedger) {
		t.Fatalf("stale ledger error = %v", err)
	}
}

func TestApplyExperienceSaturatesCurrentEXEU32(t *testing.T) {
	tables, err := Load(context.Background(), progressionTestSource())
	if err != nil {
		t.Fatal(err)
	}
	result, err := tables.ApplyExperience(4, math.MaxUint32-3, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Arithmetic still saturates before the proven cap is applied. The raw
	// gain remains intact, while character EXP cannot cross the cap entry.
	if result.NewExperience != 900 || result.NewLevel != 5 || !result.Saturated || result.Gain != 10 {
		t.Fatalf("result = %+v", result)
	}
}

func TestProgressionRejectsMissingSPLevelInsteadOfUnderpaying(t *testing.T) {
	source := progressionTestSource()
	source[SkillPointTablePath] = "[sp table]\n1 10\n3 30\n[/sp table]\n[tp table]\n3 1\n[/tp table]\n"
	tables, err := Load(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tables.TotalSkillPoints(3); !errors.Is(err, ErrTableMalformed) {
		t.Fatalf("TotalSkillPoints error = %v, want ErrTableMalformed", err)
	}
	points := dnfrepo.SkillPointState{TotalSP: 10, RemainingSP: 10, SyncedLevel: 1}
	if _, err := tables.AdvanceSkillPoints(points, 3); !errors.Is(err, ErrTableMalformed) {
		t.Fatalf("AdvanceSkillPoints error = %v, want ErrTableMalformed", err)
	}
}

// 公式对齐参考实现 `QuestParameterTable.ComputeExp`
// （ServerS4A21 `GameWorld/QuestRewardProjector.cs:175-199`）之后的钉子：
//
//	weighted = base * (1000 + weight) / 1000
//	value    = weighted * penalty / 100
//
// 夹具（`progressionTestSource`）：`[difficulty] `N` 100 / `E` 200`、
// `[exp reward table] 100 200 300 400 500`、green 80 / grey 30。
// want 列同时给出**改动前**的读数，方便审计这次修正的方向和幅度。
func TestQuestExperienceUsesPVFDifficultyAndPenalty(t *testing.T) {
	tables, err := Load(context.Background(), progressionTestSource())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		playerLevel int
		questLevel  int
		difficulty  string
		grade       string
		ignore      bool
		want        uint32
		oldValue    uint32
	}{
		// base 100、weight 100（N）⇒ 100*1100/1000 = 110（旧：100）。
		{name: "same level", playerLevel: 1, questLevel: 1, difficulty: "N", want: 110, oldValue: 100},
		// 等级差 7 ⇒ green 80%：110*80/100 = 88（旧：100*100/10000*80 = 80）。
		{name: "green penalty", playerLevel: 8, questLevel: 1, difficulty: "N", want: 88, oldValue: 80},
		// 等级差 12 ⇒ grey 30%：100*1200/1000 = 120；120*30/100 = 36（旧：60）。
		{name: "grey penalty", playerLevel: 13, questLevel: 1, difficulty: "E", want: 36, oldValue: 60},
		// ignore level 时查玩家行（base 500）、罚则 100：500*1200/1000 = 600（旧：1000）。
		{name: "ignore level uses player row", playerLevel: 5, questLevel: 1, difficulty: "E", ignore: true, want: 600, oldValue: 1000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := tables.QuestExperience(test.playerLevel, test.questLevel, test.difficulty, test.grade, test.ignore)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("QuestExperience = %d, want %d (pre-fix reading %d)", got, test.want, test.oldValue)
			}
		})
	}
	if _, err := tables.QuestExperience(1, 1, "X", "", false); !errors.Is(err, ErrQuestDifficulty) {
		t.Fatalf("unknown difficulty error = %v", err)
	}
}

// DOC-121 补证：`[difficulty]` 的两位数档位 `10`..`17` 必须按整 token 命中权重表。
// 旧实现按单 rune 建表，`10`..`17` 从未进入表；任务侧也把两位数难度静默退回
// 默认 `G`(30)，378 个真实主线因此少给约 40% 经验。
func TestQuestExperienceUsesMultiCharacterDifficultyKey(t *testing.T) {
	source := progressionTestSource()
	source[QuestParameterPath] = "[difficulty]\n`G` 30\n`10` 708\n`17` 734\n[/difficulty]\n" +
		"[exp reward table]\n1000 -1\n2000 -1\n" +
		"[green level penalty]\n80\n[grey level penalty]\n30\n"
	tables, err := Load(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if got := tables.quest.difficultyWeight["10"]; got != 708 {
		t.Fatalf("difficulty `10` weight = %d, want 708", got)
	}
	if got := tables.quest.difficultyWeight["17"]; got != 734 {
		t.Fatalf("difficulty `17` weight = %d, want 734", got)
	}
	// base 1000（等级 1）、weight 708、等级差 0 ⇒ 1000*1708/1000 = 1708。
	got, err := tables.QuestExperience(1, 1, "10", "[epic]", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1708 {
		t.Fatalf("QuestExperience(difficulty=10) = %d, want 1708 (pre-fix G reading 1030)", got)
	}
	// 小写单字符同样归一：`g` 读 `G`(30) ⇒ 1000*1030/1000 = 1030。
	if lower, err := tables.QuestExperience(1, 1, "g", "", false); err != nil || lower != 1030 {
		t.Fatalf("QuestExperience(difficulty=g) = %d err=%v, want 1030", lower, err)
	}
}

// 厂商 self-test 的逐字锚点：`A21TutorialProtocolSelfTest.cs:1710-1721`
// `ComputeExp(playerLevel: 1, rewardLevel: 1, difficulty: '1', questGrade: "epic", ignoreLevel: false) == 4719`
// 用的是 `[difficulty] `1` 964` + `[exp reward table] 2403 / 3856`：
// 2403 * (1000+964) / 1000 = 4719.492 → 4719（整数除法），等级差 0 ⇒ 罚则 100。
func TestQuestExperienceMatchesVendorPermilleAnchor(t *testing.T) {
	source := progressionTestSource()
	source[QuestParameterPath] = "[difficulty]\n`1` 964\n[/difficulty]\n" +
		"[exp reward table]\n2403 -1\n3856 -1\n" +
		"[green level penalty]\n80\n[grey level penalty]\n30\n"
	tables, err := Load(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tables.QuestExperience(1, 1, "1", "[epic]", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4719 {
		t.Fatalf("vendor permille anchor = %d, want 4719", got)
	}
}

func TestQuestGoldUsesPVFBaseMultiplierBeforeIntegerDivision(t *testing.T) {
	tables, err := Load(context.Background(), progressionTestSource())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		playerLevel  int
		questLevel   int
		goldMultiple int
		ignore       bool
		want         uint32
	}{
		{name: "default hundred percent keeps low base", playerLevel: 1, questLevel: 1, want: 10},
		{name: "explicit hundred fifty percent", playerLevel: 1, questLevel: 1, goldMultiple: 150, want: 15},
		{name: "green penalty applies after multiplier", playerLevel: 8, questLevel: 1, want: 8},
		{name: "ignore level uses player row", playerLevel: 1, questLevel: 1, goldMultiple: 150, ignore: true, want: 15},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := tables.QuestGold(test.playerLevel, test.questLevel, test.goldMultiple, test.ignore)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("QuestGold = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRealScriptPVFProgressionTables(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify real progression tables")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	tables, err := Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := tables.Snapshot()
	if snapshot.ExperienceThresholds < 90 || snapshot.SkillPointLevels < 90 || snapshot.TechniquePointLevels < 41 ||
		snapshot.QuestExperienceLevels < 90 || snapshot.QuestDifficulties == 0 {
		t.Fatalf("real progression snapshot = %+v", snapshot)
	}
	thresholds := []uint32{1000, 2035, 3315}
	for level, want := range thresholds {
		got, err := tables.ThresholdToNext(level + 1)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("real threshold level %d = %d, want %d", level+1, got, want)
		}
	}
	if value, ok := tables.SkillPointsAtLevel(1); !ok || value != 0 {
		t.Fatalf("real level-1 SP = %d,%t, want 0,true", value, ok)
	}
	if value, ok := tables.SkillPointsAtLevel(2); !ok || value != 30 {
		t.Fatalf("real level-2 SP = %d,%t, want 30,true", value, ok)
	}
	advance, err := tables.AdvanceSkillPoints(dnfrepo.SkillPointState{SyncedLevel: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if advance.SPGain != 30 || advance.New.TotalSP != 30 || advance.New.RemainingSP != 30 {
		t.Fatalf("real SP advance = %+v", advance)
	}
	if totalTP, err := tables.TotalTechniquePoints(90); err != nil || totalTP != 41 {
		t.Fatalf("real level-90 TP total=%d err=%v, want 41", totalTP, err)
	}
	t.Logf("real progression snapshot=%+v level2_threshold=%d level2_sp=%d", snapshot, thresholds[0], advance.SPGain)
}

type progressionMemorySource map[string]string

func (s progressionMemorySource) ReadText(path string) (string, error) {
	value, ok := s[path]
	if !ok {
		return "", fmt.Errorf("%w: %s", platformpvf.ErrFileNotFound, path)
	}
	return value, nil
}

func progressionTestSource() progressionMemorySource {
	return progressionMemorySource{
		ExperienceTablePath: "100 250 500 900\n",
		SkillPointTablePath: "[sp table]\n1 10\n2 30\n3 30\n4 40\n5 50\n[/sp table]\n[tp table]\n3 2\n4 1\n5 1\n[/tp table]\n",
		QuestParameterPath: `[difficulty]
` + "`N` 100\n`E` 200\n" + `[/difficulty]
[exp reward table]
100 -1
200 -1
300 -1
400 -1
500 -1
[gold reward table]
10 -1
[green level penalty]
80
[grey level penalty]
30
`,
	}
}
