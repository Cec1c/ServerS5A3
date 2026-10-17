package skillcmd

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 113段反馈回归（2026-10-15 20:00 角色123）：
// 「技能栏有空位的情况下学习了TP技能会增加到技能栏里面，例如：学习了强化上挑」。
//
// 现场路径就是学习（op29 ApplyBuy）后由 allocateSkillSlot 选槽：普通主动技能必须
// 仍然自动填空快捷栏（0..5 / 198..203，wire 0..11），而 TP（feature skill type 或
// special purchase cost）技能不再占用这两段，只拿 currentSlotGroup 给的技能页展示槽。
func TestOwnerApplyBuyKeepsTPSkillOutOfOrdinaryQuickSlots(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	owner, err := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{}})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	// 同批次两支普通主动技能（47 的前置要求 46 到 2 级）：自动填空快捷栏的行为不变。
	ordinary, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count: 2,
		Entries: []BuySkillEntry{
			{SkillID: 46, LevelDelta: 1},
			{SkillID: 47, LevelDelta: 1},
		},
	}))
	if err != nil {
		t.Fatalf("ordinary ApplyBuy() error = %v", err)
	}
	if len(ordinary.Entries) != 2 {
		t.Fatalf("ordinary entries = %+v", ordinary.Entries)
	}
	for _, entry := range ordinary.Entries {
		if !ordinaryQuickSlot(entry.Slot) {
			t.Fatalf("ordinary active skill %d slot = %d, want an empty ordinary quick slot", entry.SkillID, entry.Slot)
		}
	}

	// 48 是 fixture 里 feature skill type 1 + special purchase cost 的 active TP 技能。
	// 此时 0..5 全空，修复前它会被塞进普通快捷栏（旧断言 slot=1），修复后只能拿技能页展示槽。
	tp, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 48, LevelDelta: 1}},
	}))
	if err != nil {
		t.Fatalf("TP ApplyBuy() error = %v", err)
	}
	if len(tp.Entries) != 1 || !tp.Entries[0].TP {
		t.Fatalf("TP entries = %+v, want one TP entry", tp.Entries)
	}
	if ordinaryQuickSlot(tp.Entries[0].Slot) {
		t.Fatalf("TP skill 48 slot = %d, want a skill-page slot outside the ordinary quick bar", tp.Entries[0].Slot)
	}
	if tp.Entries[0].Slot < 6 || tp.Entries[0].Slot >= 198 {
		t.Fatalf("TP skill 48 slot = %d, want the active skill-page group 150..197", tp.Entries[0].Slot)
	}

	record, ok, err := repos.Skill.Load(ctx, "31")
	if err != nil || !ok {
		t.Fatalf("load skill ok=%t err=%v", ok, err)
	}
	for slot, skillID := range record.Layouts[0] {
		if skillID == 48 && ordinaryQuickSlot(slot) {
			t.Fatalf("persisted TP skill 48 landed in ordinary quick slot %d: %+v", slot, record.Layouts[0])
		}
	}
	if record.Layouts[0][tp.Entries[0].Slot] != 48 {
		t.Fatalf("persisted layout = %+v, want skill 48 at slot %d", record.Layouts[0], tp.Entries[0].Slot)
	}
}

// 已有布局与手动绑定必须原样保留：48 已经由玩家（或旧版本）绑在快捷栏 0 号上，
// 再次学习升级时不得把它搬走，ACK 仍然回该槽。
func TestOwnerApplyBuyPreservesExistingTPQuickSlotBinding(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[48] = dnfrepo.SkillState{Level: 2, Enabled: true}
	record.Layouts = map[int]dnfrepo.SkillLayout{0: {0: 48}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save skill layout: %v", err)
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{}})
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 48, LevelDelta: 1}},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Slot != 0 || result.Entries[0].Level != 3 {
		t.Fatalf("result entries = %+v, want skill 48 level 3 kept at its existing slot 0", result.Entries)
	}
	persisted, _, _ := repos.Skill.Load(ctx, "31")
	if persisted.Layouts[0][0] != 48 {
		t.Fatalf("persisted layout = %+v, want the pre-existing binding at slot 0 kept", persisted.Layouts[0])
	}
}

// 手动把 TP 技能拖进普通快捷栏（op28）仍然是玩家的自由，修复只针对服务端自动填空。
func TestOwnerApplySlotStillAcceptsManualTPQuickSlotBinding(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[48] = dnfrepo.SkillState{Level: 2, Enabled: true}
	record.Layouts = map[int]dnfrepo.SkillLayout{0: {150: 48}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save skill layout: %v", err)
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{}})
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.ApplySlot(ctx, NewSlotCommand(alignedRequest("acc-skill", 31), ChangeSkillSlotRequest{
		SkillTree: 0, From: 150, To: 3, ContextIndex: -1, Mode: 2,
	}))
	if err != nil {
		t.Fatalf("ApplySlot() error = %v; a player-requested TP quick-slot binding must stay accepted", err)
	}
	if result.FromSkillID != 48 || result.ToOccupied {
		t.Fatalf("result = %+v, want skill 48 moved into the empty slot 3", result)
	}
	persisted, _, _ := repos.Skill.Load(ctx, "31")
	if persisted.Layouts[0][3] != 48 {
		t.Fatalf("persisted layout = %+v, want skill 48 at slot 3", persisted.Layouts[0])
	}
	if _, moved := persisted.Layouts[0][150]; moved {
		t.Fatalf("persisted layout = %+v, want slot 150 released", persisted.Layouts[0])
	}
}

// ordinaryQuickSlot 是领域侧两段普通快捷栏（0..5、198..203），wire 上对应 0..11。
func ordinaryQuickSlot(slot int) bool {
	return (slot >= 0 && slot < 6) || (slot >= 198 && slot < 204)
}
