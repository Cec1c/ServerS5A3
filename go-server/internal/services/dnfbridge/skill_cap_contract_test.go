package dnfbridge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 达人契约（premium type 27）让技能可以提前 5 级学（skillcmd 的加分门禁就是按
// 有效等级算的）。登录/技能页投影时的"可学习上限修复"过去用的是**裸角色等级**，
// 于是一个契约期间合法买到 6 级的技能（角色 15 级、[required level] 15）会在
// 下一次投影时被夹回 1 级并把差额 SP 退回去 —— 玩家凭空掉 5 级。
//
// 本用例钉住两侧：契约生效 => 一个字节都不动；契约不在 => 仍然按裸等级夹回（与
// 加分门禁同口径，也是"契约过期后 sweep 掉提前学的等级"的既有语义）。
func TestRepairCurrentSceneSkillLevelCapsUsesContractEffectiveLevel(t *testing.T) {
	ctx := context.Background()
	// skillRuleCatalog 的 fixture 里 47 是 [required level] 15 /
	// [growtype maximum level] 0 50 ... / [purchase cost] 30：grow_type=1 时
	// 裸等级 15 的上限是 1 级，加契约(+5)后的上限是 6 级。
	const (
		characterID = "9"
		accountID   = "account-contract"
		learned     = 6
		remainingSP = 550
	)
	seed := func(t *testing.T) dnfrepo.Group {
		t.Helper()
		repos := dnfrepomemory.NewMemoryGroup()
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
			CharacterID: characterID,
			AccountID:   accountID,
			Job:         "0",
			Level:       15,
			Stats:       map[string]int64{"grow_type": 1},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
			CharacterID: characterID,
			Skills:      map[int64]dnfrepo.SkillState{47: {Level: learned, Enabled: true}},
			Points: dnfrepo.SkillPointState{
				TotalSP: 1000, RemainingSP: remainingSP,
				TotalTP: 10, RemainingTP: 10,
				SyncedLevel: 15,
			},
			Layouts: map[int]dnfrepo.SkillLayout{0: {0: 47}},
		}); err != nil {
			t.Fatal(err)
		}
		return repos
	}
	service := func(repos dnfrepo.Group) *Service {
		return &Service{
			repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			skillCatalog:       skillCommandProjectionCatalog(t),
			initialSkillsByJob: map[byte][]initialSkillEntry{0: {{SkillID: 46, Level: 1}}},
		}
	}
	character := func() dnfrepo.CharacterRecord {
		return dnfrepo.CharacterRecord{
			CharacterID: characterID, AccountID: accountID, Job: "0", Level: 15,
			Stats: map[string]int64{"grow_type": 1},
		}
	}

	t.Run("contract active keeps the early levels", func(t *testing.T) {
		repos := seed(t)
		expire := time.Now().UTC().Add(24 * time.Hour).Unix()
		if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
			AccountID: accountID,
			Metadata:  map[string]string{premium.MetadataKey(premium.TypeOverSkill): fmt.Sprintf("%d", expire)},
		}); err != nil {
			t.Fatal(err)
		}
		record, found, err := repos.Skill.Load(ctx, characterID)
		if err != nil || !found {
			t.Fatalf("load skill found=%t err=%v", found, err)
		}
		got, err := service(repos).repairCurrentSceneSkillLevelCaps(ctx, repos, character(), record)
		if err != nil {
			t.Fatalf("repair error = %v", err)
		}
		if got.Skills[47].Level != learned || got.Points.RemainingSP != remainingSP {
			t.Fatalf("contract-bought level was clamped: level=%d sp=%d", got.Skills[47].Level, got.Points.RemainingSP)
		}
		// The repair must not have persisted anything either.
		saved, _, err := repos.Skill.Load(ctx, characterID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Skills[47].Level != learned || saved.Points.RemainingSP != remainingSP {
			t.Fatalf("persisted record = %+v", saved)
		}
	})

	t.Run("without the contract the raw-level cap still applies", func(t *testing.T) {
		repos := seed(t)
		// The account exists but has no 达人契约 entry (lazy expiry: absent = inactive).
		if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
			AccountID: accountID,
			Metadata:  map[string]string{},
		}); err != nil {
			t.Fatal(err)
		}
		record, _, err := repos.Skill.Load(ctx, characterID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := service(repos).repairCurrentSceneSkillLevelCaps(ctx, repos, character(), record)
		if err != nil {
			t.Fatalf("repair error = %v", err)
		}
		// 15 级角色在 skill 47 上的可学上限是 1 级；5 级 × 30 SP 全部退回。
		if got.Skills[47].Level != 1 || got.Points.RemainingSP != remainingSP+5*30 {
			t.Fatalf("repaired = level %d sp %d, want level 1 sp %d", got.Skills[47].Level, got.Points.RemainingSP, remainingSP+150)
		}
	})
}

// 夹回上限是破坏性的（等级被删、只退 SP），所以必须有"只看不动"的 A/B 档：
// DNFBRIDGE_SKILL_CAP_REPAIR=observe 时一个字段都不落库，但报告照旧。
func TestSkillCapRepairObserveModeNeverRewrites(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "9", AccountID: "account-1", Job: "0", Level: 90,
		Stats: map[string]int64{"grow_type": 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: "9",
		Skills:      map[int64]dnfrepo.SkillState{46: {Level: 20, Enabled: true}},
		Points:      dnfrepo.SkillPointState{TotalSP: 1000, RemainingSP: 400, TotalTP: 10, RemainingTP: 10, SyncedLevel: 90},
		Layouts:     map[int]dnfrepo.SkillLayout{0: {0: 46}},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
		skillCatalog:       skillCommandProjectionCatalog(t),
		initialSkillsByJob: map[byte][]initialSkillEntry{0: {{SkillID: 46, Level: 1}}},
	}
	character := dnfrepo.CharacterRecord{CharacterID: "9", AccountID: "account-1", Job: "0", Level: 90, Stats: map[string]int64{"grow_type": 1}}
	record, _, err := repos.Skill.Load(ctx, "9")
	if err != nil {
		t.Fatal(err)
	}
	if mode := currentSkillCapRepairMode(); mode != currentSkillCapRepairClamp {
		t.Fatalf("default repair mode = %q, want %q", mode, currentSkillCapRepairClamp)
	}

	t.Setenv(currentSkillCapRepairEnv, "observe")
	if mode := currentSkillCapRepairMode(); mode != currentSkillCapRepairObserve {
		t.Fatalf("observe mode = %q", mode)
	}
	got, err := service.repairCurrentSceneSkillLevelCaps(ctx, repos, character, record)
	if err != nil {
		t.Fatalf("repair error = %v", err)
	}
	if got.Skills[46].Level != 20 || got.Points.RemainingSP != 400 {
		t.Fatalf("observe mode rewrote the record: level=%d sp=%d", got.Skills[46].Level, got.Points.RemainingSP)
	}
	saved, _, err := repos.Skill.Load(ctx, "9")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Skills[46].Level != 20 || saved.Points.RemainingSP != 400 {
		t.Fatalf("observe mode persisted a clamp: %+v", saved)
	}

	// 同一份数据在默认档下必须真的被夹回上限（20 → 10，退 10 级 × 20 SP）。
	t.Setenv(currentSkillCapRepairEnv, "")
	got, err = service.repairCurrentSceneSkillLevelCaps(ctx, repos, character, record)
	if err != nil {
		t.Fatalf("repair error = %v", err)
	}
	if got.Skills[46].Level != 10 || got.Points.RemainingSP != 600 {
		t.Fatalf("clamp mode = level %d sp %d, want level 10 sp 600", got.Skills[46].Level, got.Points.RemainingSP)
	}
}
