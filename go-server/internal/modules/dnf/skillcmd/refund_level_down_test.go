package skillcmd

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件是「技能降级（op29 refund 条目）必须真正生效」的回归。
//
// 现场（2026-09-25 20:31，香港 packet_log.txt:925/931 请求、928/934 应答）：
// 操作者把技能改成更低等级再点「学习」，请求是 refund 条目
// （tree=0 count=1 skill=21 refund=1 delta=1|3 mode=0），服务端回 [00 03]
// （缺遗忘河之水），等级不变，客户端于是继续显示原等级。
//
// 期望（改动后默认档）：refund 条目按请求等级下调、退还差额 SP、回与升级同形的
// 成功体，并由 handler 追加 op19 技能页刷新；越过可学上限、低于 PVF 初始等级、
// 破坏前置、转职方向不符仍然必须被拒；纯升级批次的字节不变。

// levelDownCharacterID 是降级用例的角色 ID。等级取 90（与现场角色同档），
// 这样 [maximum level] 20 的技能可以被学到 20 级。
const levelDownCharacterID = "90"

// seededLevelDownRepositories 造一个"已经学到 skillLevel 级"的角色：
// - 技能 46（PVF: required level 1 / maximum level 20 / purchase cost 20/级）
// - TotalSP 3000、TotalTP 41，RemainingSP 由调用者给出（默认 3000-20*skillLevel）
// - 技能栏 tree0/slot0 已经持有 46，refund 必须能找到槽位。
func seededLevelDownRepositories(t *testing.T, ctx context.Context, skillLevel int, remainingSP int) dnfrepo.Group {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: levelDownCharacterID,
		AccountID:   "acc-skill",
		Job:         "0",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 0, currentEXESkillTreeIndexStat: 0},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	skills := map[int64]dnfrepo.SkillState{}
	layouts := map[int]dnfrepo.SkillLayout{}
	if skillLevel > 0 {
		skills[46] = dnfrepo.SkillState{Level: skillLevel, Enabled: true}
		layouts[0] = dnfrepo.SkillLayout{0: 46}
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: levelDownCharacterID,
		Skills:      skills,
		Layouts:     layouts,
		Points: dnfrepo.SkillPointState{
			TotalSP: 3000, RemainingSP: remainingSP,
			TotalTP: 41, RemainingTP: 41,
			SyncedLevel: 90,
		},
	}); err != nil {
		t.Fatalf("save skill: %v", err)
	}
	return repos
}

func levelDownBuyCommand(entries ...BuySkillEntry) Command {
	return NewBuyCommand(alignedRequest("acc-skill", 90), BuySkillRequest{
		RawSkillTree: 0,
		Count:        byte(len(entries)),
		Entries:      entries,
		FinalMode:    0,
	})
}

// ① 核心：20 → 18 生效且 SP 退回 2 级的差额（本题 2 * 20 = 40）。
func TestOwnerApplyBuyRefundLowersTwentyToEighteenAndRefundsTwoLevels(t *testing.T) {
	ctx := context.Background()
	repos := seededLevelDownRepositories(t, ctx, 20, 2600)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, levelDownBuyCommand(BuySkillEntry{SkillID: 46, RefundFlag: 1, LevelDelta: 2}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if !result.AppliedRefund {
		t.Fatalf("result = %+v, want AppliedRefund", result)
	}
	if result.ConsumedRefundItem {
		t.Fatalf("默认档不得消耗遗忘河之水: %+v", result)
	}
	if result.Points.RemainingSP != 2640 || result.Points.TotalSP != 3000 {
		t.Fatalf("points = %+v, want RemainingSP 2640 / TotalSP 3000", result.Points)
	}
	if len(result.Entries) != 1 || result.Entries[0].SkillID != 46 || result.Entries[0].Level != 18 || result.Entries[0].Slot != 0 {
		t.Fatalf("entries = %+v, want skill 46 at level 18 in slot 0", result.Entries)
	}

	record, ok, loadErr := repos.Skill.Load(ctx, levelDownCharacterID)
	if loadErr != nil || !ok {
		t.Fatalf("load persisted skill ok=%t err=%v", ok, loadErr)
	}
	if record.Skills[46].Level != 18 || record.Points.RemainingSP != 2640 {
		t.Fatalf("persisted = %+v, want level 18 / RemainingSP 2640", record)
	}
	if record.Layouts[0][0] != 46 {
		t.Fatalf("降级必须保留技能栏槽位: %+v", record.Layouts)
	}
}

// ② 同形成功体 + 技能页刷新：成功 ACK 必须带新等级与新 SP，并追加 op19 刷新，
// 否则客户端继续画旧等级（现场"又变成了 20"）。
func TestHandlerBuySkillRefundAnswersLowerLevelAndRequestsSkillPageRefresh(t *testing.T) {
	ctx := context.Background()
	repos := seededLevelDownRepositories(t, ctx, 20, 2600)
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode: utf16Opcode(dnfenum.CmdPacketBuySkill),
		// tree=0 count=1 (skill=46 refund=1 delta=2) mode=0
		Body:                []byte{0, 1, 46, 0, 1, 2, 0},
		AccountID:           "acc-skill",
		SelectedCharacterID: 90,
		Repositories:        repos,
		SkillCatalog:        skillRuleCatalog(t),
		InitialSkillLevels:  map[uint16]int{},
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v, want one real response", got)
	}
	// 与升级同形：success, tree, u16 remainSP, u16 remainTP, count,
	// (slot, u16 skill, level, cmd), finalMode。
	want := []byte{
		1, 0,
		0x50, 0x0A, // 2640
		0x29, 0x00, // 41
		1,
		0, 46, 0, 18, 0,
		0,
	}
	if body := got.UpperResponses[0].Body; string(body) != string(want) {
		t.Fatalf("refund ACK = % X, want % X", body, want)
	}
	if len(got.PostActions) != 1 || got.PostActions[0] != alignedcmd.PostActionRefreshSelectedActorSkills {
		t.Fatalf("post actions = %+v, want exactly the op19 skill refresh", got.PostActions)
	}
	record, _, _ := repos.Skill.Load(ctx, levelDownCharacterID)
	if record.Skills[46].Level != 18 || record.Points.RemainingSP != 2640 {
		t.Fatalf("persisted = %+v, want level 18 / RemainingSP 2640", record)
	}
}

// ③ 上限没有被放松：越过可学上限的上升仍然被拒且不落库。
func TestOwnerApplyBuyRiseAboveLearnableCapStillRejectedAfterRefundRepair(t *testing.T) {
	ctx := context.Background()
	repos := seededLevelDownRepositories(t, ctx, 20, 2600)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	_, err = owner.ApplyBuy(ctx, levelDownBuyCommand(BuySkillEntry{SkillID: 46, LevelDelta: 1}))
	if !errors.Is(err, ErrSkillLevel) {
		t.Fatalf("ApplyBuy() error = %v, want ErrSkillLevel (maximum level 20)", err)
	}
	record, _, _ := repos.Skill.Load(ctx, levelDownCharacterID)
	if record.Skills[46].Level != 20 || record.Points.RemainingSP != 2600 {
		t.Fatalf("rejected rise changed state: %+v", record)
	}
}

// ③b 上限没有被放松（另一侧）：低于 PVF 初始等级的降级仍然被拒。
func TestOwnerApplyBuyRefundBelowPVFFloorStillRejectedAfterRefundRepair(t *testing.T) {
	ctx := context.Background()
	repos := seededLevelDownRepositories(t, ctx, 2, 2960)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	_, err = owner.ApplyBuy(ctx, levelDownBuyCommand(BuySkillEntry{SkillID: 46, RefundFlag: 1, LevelDelta: 2}))
	if !errors.Is(err, ErrSkillLevel) {
		t.Fatalf("ApplyBuy() error = %v, want ErrSkillLevel (floor 1)", err)
	}
	record, _, _ := repos.Skill.Load(ctx, levelDownCharacterID)
	if record.Skills[46].Level != 2 || record.Points.RemainingSP != 2960 {
		t.Fatalf("rejected refund changed state: %+v", record)
	}
}

// ③c 降级到 0 仍然允许（PVF 初始等级 0 的技能可以完全退掉），并释放技能栏槽位。
func TestOwnerApplyBuyRefundToZeroUnlearnsAndFreesSlot(t *testing.T) {
	ctx := context.Background()
	repos := seededLevelDownRepositories(t, ctx, 1, 2980)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, levelDownBuyCommand(BuySkillEntry{SkillID: 46, RefundFlag: 1, LevelDelta: 1}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if !result.AppliedRefund || result.Points.RemainingSP != 3000 {
		t.Fatalf("result = %+v, want full SP refund 3000", result)
	}
	if len(result.Entries) != 1 || result.Entries[0].Level != 0 {
		t.Fatalf("entries = %+v, want level 0 entry", result.Entries)
	}
	record, _, _ := repos.Skill.Load(ctx, levelDownCharacterID)
	if _, exists := record.Skills[46]; exists {
		t.Fatalf("fully refunded skill still learned: %+v", record.Skills)
	}
	for slot, id := range record.Layouts[0] {
		if id == 46 {
			t.Fatalf("fully refunded skill keeps layout slot %d", slot)
		}
	}
}

// ④ 前置与转职方向限制仍然生效。
func TestOwnerApplyBuyRefundStillRespectsPrerequisiteAndGrowTypeDirection(t *testing.T) {
	ctx := context.Background()

	t.Run("prerequisite", func(t *testing.T) {
		repos := seededLevelDownRepositories(t, ctx, 2, 2950)
		record, _, _ := repos.Skill.Load(ctx, levelDownCharacterID)
		// 47 的 PVF 前置是 46 >= 2；把 46 退到 1 会打断它。
		record.Skills[47] = dnfrepo.SkillState{Level: 1, Enabled: true}
		if err := repos.Skill.Save(ctx, record); err != nil {
			t.Fatalf("save dependent skill: %v", err)
		}
		owner, err := NewOwner(repos, OwnerOptions{
			Catalog:       skillRuleCatalog(t),
			InitialLevels: map[uint16]int{},
		})
		if err != nil {
			t.Fatalf("NewOwner() error = %v", err)
		}
		_, err = owner.ApplyBuy(ctx, levelDownBuyCommand(BuySkillEntry{SkillID: 46, RefundFlag: 1, LevelDelta: 1}))
		if !errors.Is(err, ErrSkillPrerequisite) {
			t.Fatalf("ApplyBuy() error = %v, want ErrSkillPrerequisite", err)
		}
		after, _, _ := repos.Skill.Load(ctx, levelDownCharacterID)
		if after.Skills[46].Level != 2 || after.Skills[47].Level != 1 || after.Points.RemainingSP != 2950 {
			t.Fatalf("rejected refund changed state: %+v", after)
		}
	})

	t.Run("growtype direction", func(t *testing.T) {
		repos := seededLevelDownRepositories(t, ctx, 2, 2960)
		owner, err := NewOwner(repos, OwnerOptions{
			Catalog:       secondAwakeningSkillCatalog(t),
			InitialLevels: map[uint16]int{},
		})
		if err != nil {
			t.Fatalf("NewOwner() error = %v", err)
		}
		// grow_type 0 的角色不能持有只对二次觉醒方向开放的技能；PVF 的
		// maximum-level 表在该方向为 0，降级同样被拒。
		_, err = owner.ApplyBuy(ctx, levelDownBuyCommand(BuySkillEntry{SkillID: 46, RefundFlag: 1, LevelDelta: 1}))
		if !errors.Is(err, ErrSkillGrowType) {
			t.Fatalf("ApplyBuy() error = %v, want ErrSkillGrowType", err)
		}
		after, _, _ := repos.Skill.Load(ctx, levelDownCharacterID)
		if after.Skills[46].Level != 2 || after.Points.RemainingSP != 2960 {
			t.Fatalf("rejected refund changed state: %+v", after)
		}
	})
}

// ⑤ 升级路径逐字节不变：同一 learn 请求在默认档与回退档下必须给出同一份
// 13 字节成功体，且不追加 op19 刷新（只有降级才追加）。
func TestBuySkillLearnPathBytesUnchangedByRefundPolicy(t *testing.T) {
	ctx := context.Background()
	want := []byte{
		1, 0,
		0x50, 0x00, // 100 - 20 = 80
		0x0A, 0x00, // TP 10
		1,
		0, 46, 0, 2, 0,
		0,
	}

	run := func(t *testing.T) ([]byte, []alignedcmd.PostAction, dnfrepo.SkillRecord) {
		t.Helper()
		repos := seededSkillOwnerRepositories(t, ctx)
		got, err := NewHandler().Handle(ctx, alignedcmd.Request{
			Opcode:              utf16Opcode(dnfenum.CmdPacketBuySkill),
			Body:                []byte{0, 1, 46, 0, 0, 1, 0},
			AccountID:           "acc-skill",
			SelectedCharacterID: 31,
			Repositories:        repos,
			SkillCatalog:        skillRuleCatalog(t),
			InitialSkillLevels:  map[uint16]int{46: 1},
		})
		if err != nil {
			t.Fatalf("Handle error = %v", err)
		}
		if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
			t.Fatalf("result = %+v, want one real response", got)
		}
		record, _, loadErr := repos.Skill.Load(ctx, "31")
		if loadErr != nil {
			t.Fatalf("load skill record: %v", loadErr)
		}
		// UpdatedAt 是写入时刻，逐字节对比只关心业务字段。
		record.UpdatedAt = time.Time{}
		return got.UpperResponses[0].Body, got.PostActions, record
	}

	defaultBody, defaultActions, defaultRecord := run(t)
	t.Setenv(skillRefundRequireWaterEnv, "1")
	legacyBody, legacyActions, legacyRecord := run(t)

	if string(defaultBody) != string(want) {
		t.Fatalf("learn ACK (default) = % X, want % X", defaultBody, want)
	}
	if string(legacyBody) != string(defaultBody) {
		t.Fatalf("learn ACK changed with the refund switch: % X vs % X", legacyBody, defaultBody)
	}
	if len(defaultActions) != 0 || len(legacyActions) != 0 {
		t.Fatalf("learn path must not add post actions: default=%+v legacy=%+v", defaultActions, legacyActions)
	}
	if !reflect.DeepEqual(defaultRecord, legacyRecord) {
		t.Fatalf("learn persisted state changed with the refund switch: %+v vs %+v", defaultRecord, legacyRecord)
	}
	if defaultRecord.Skills[46].Level != 2 || defaultRecord.Points.RemainingSP != 80 {
		t.Fatalf("learn persisted state = %+v, want level 2 / RemainingSP 80", defaultRecord)
	}
}

// utf16Opcode 只是让请求构造读起来和 handler 分派一致。
func utf16Opcode(value dnfenum.CmdPacket) uint16 { return uint16(value) }
