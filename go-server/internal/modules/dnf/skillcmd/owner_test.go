package skillcmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

func TestOwnerPlanReadsSkillState(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "31",
		AccountID:   "acc-skill",
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: "31",
		Skills: map[int64]dnfrepo.SkillState{
			7: {Level: 3, Enabled: true},
			8: {Level: 1, Enabled: true},
		},
		Cooldowns: map[int64]time.Time{7: time.Unix(100, 0)},
	}); err != nil {
		t.Fatalf("save skill: %v", err)
	}

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}
	got, err := owner.Plan(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		RawSkillTree: 0,
		Count:        2,
		Entries: []BuySkillEntry{
			{SkillID: 7, LevelDelta: 3},
			{SkillID: 8, RefundFlag: 1, LevelDelta: 1},
		},
	}))
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got.AccountID != "acc-skill" || got.CharacterID != "31" || !got.Known || got.SkillCount != 2 || got.CooldownCount != 1 || got.RefundCount != 1 {
		t.Fatalf("result = %+v", got)
	}
	if len(got.RequestedSkillIDs) != 2 || got.RequestedSkillIDs[0] != 7 || got.RequestedSkillIDs[1] != 8 {
		t.Fatalf("requested skill IDs = %+v", got.RequestedSkillIDs)
	}
}

func TestOwnerApplyTreeSwitchPersistsOppositeTreeAndRejectsReplay(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	unlockSkillOwnerFixture(t, ctx, repos)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	command := NewTreeCommand(alignedRequest("acc-skill", 31), ChangeAnotherSkillTreeRequest{SkillTree: 0})
	result, err := owner.ApplyTreeSwitch(ctx, command)
	if err != nil {
		t.Fatalf("ApplyTreeSwitch() error = %v", err)
	}
	if result.CharacterID != "31" || result.Current != 0 || result.Target != 1 {
		t.Fatalf("result = %+v", result)
	}
	character, ok, err := repos.Character.Load(ctx, "31")
	if err != nil || !ok || character.Stats[currentEXESkillTreeIndexStat] != 1 {
		t.Fatalf("persisted character ok=%t err=%v record=%+v", ok, err, character)
	}
	if _, err := owner.ApplyTreeSwitch(ctx, command); !errors.Is(err, ErrSkillTreeStateMismatch) {
		t.Fatalf("replayed ApplyTreeSwitch() error = %v, want %v", err, ErrSkillTreeStateMismatch)
	}
	character, _, _ = repos.Character.Load(ctx, "31")
	if character.Stats[currentEXESkillTreeIndexStat] != 1 {
		t.Fatalf("replayed switch mutated tree = %d", character.Stats[currentEXESkillTreeIndexStat])
	}
}

func TestOwnerApplyTreeSwitchRequiresOwnedTreeMarker(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	character, _, _ := repos.Character.Load(ctx, "31")
	delete(character.Stats, currentEXESkillTreeIndexStat)
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.ApplyTreeSwitch(ctx, NewTreeCommand(alignedRequest("acc-skill", 31), ChangeAnotherSkillTreeRequest{SkillTree: 0}))
	if !errors.Is(err, ErrSkillTreeUnavailable) {
		t.Fatalf("ApplyTreeSwitch() error = %v, want %v", err, ErrSkillTreeUnavailable)
	}
}

func TestOwnerPlanRejectsMissingCharacter(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}
	_, err = owner.Plan(ctx, NewSlotCommand(alignedRequest("acc-skill", 31), ChangeSkillSlotRequest{SkillTree: 0, From: 1, To: 2}))
	if !errors.Is(err, ErrCharacterNotFound) {
		t.Fatalf("Plan() error = %v, want ErrCharacterNotFound", err)
	}
}

func TestOwnerApplyBuyAllowsProvenTreeZeroAndCommitsAtomically(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		RawSkillTree: 0,
		Count:        2,
		FinalMode:    3,
		Entries: []BuySkillEntry{
			// The dependent skill can appear first because prerequisites are
			// validated against the final state of the current EXE batch.
			{SkillID: 47, LevelDelta: 1},
			{SkillID: 46, LevelDelta: 1},
		},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if result.Points.RemainingSP != 50 || result.SkillTree != 0 || result.FinalMode != 3 || len(result.Entries) != 2 {
		t.Fatalf("mutation result = %+v", result)
	}
	if result.Entries[0].Slot != 1 || result.Entries[1].Slot != 0 {
		t.Fatalf("tree-zero slots = %+v", result.Entries)
	}
	record, ok, err := repos.Skill.Load(ctx, "31")
	if err != nil || !ok {
		t.Fatalf("load skill ok=%t err=%v", ok, err)
	}
	if record.Skills[46].Level != 2 || record.Skills[47].Level != 1 || record.Points.RemainingSP != 50 {
		t.Fatalf("persisted mutation = %+v", record)
	}
	if record.Layouts[0][0] != 46 || record.Layouts[0][1] != 47 {
		t.Fatalf("persisted tree-zero layout = %+v", record.Layouts)
	}
}

func TestOwnerApplySlotSwapsPersistedLayoutAtomically(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[47] = dnfrepo.SkillState{Level: 1, Enabled: true}
	record.Layouts = map[int]dnfrepo.SkillLayout{0: {0: 46, 1: 47}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save skill layout: %v", err)
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplySlot(ctx, NewSlotCommand(alignedRequest("acc-skill", 31), ChangeSkillSlotRequest{
		SkillTree: 0, From: 0, To: 1, ContextIndex: -1, Mode: 2,
	}))
	if err != nil {
		t.Fatalf("ApplySlot() error = %v", err)
	}
	if result.FromSkillID != 46 || result.ToSkillID != 47 || !result.ToOccupied {
		t.Fatalf("result = %+v", result)
	}
	persisted, ok, err := repos.Skill.Load(ctx, "31")
	if err != nil || !ok {
		t.Fatalf("load persisted layout ok=%t err=%v", ok, err)
	}
	if persisted.Layouts[0][0] != 47 || persisted.Layouts[0][1] != 46 {
		t.Fatalf("persisted layout = %+v", persisted.Layouts[0])
	}
}

func TestOwnerApplyResetRestoresPVFInitialStateAndRefundsPointsAtomically(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[46] = dnfrepo.SkillState{Level: 3, Enabled: true}
	record.Skills[47] = dnfrepo.SkillState{Level: 1, Enabled: true}
	record.Points.RemainingSP = 10
	record.Points.RemainingTP = 4
	record.Layouts = map[int]dnfrepo.SkillLayout{0: {0: 47, 1: 46}, 1: {0: 47}}
	cooldown := time.Unix(1234, 0).UTC()
	record.Cooldowns = map[int64]time.Time{46: cooldown}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
		PointBaseline: &dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100, TotalTP: 10, RemainingTP: 10, SyncedLevel: 10},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := owner.ApplyReset(ctx, NewResetCommand(alignedRequest("acc-skill", 31), SkillInitRequest{SkillTree: 0, Mode: 0}))
	if err != nil {
		t.Fatalf("ApplyReset() error = %v", err)
	}
	if result.SkillCount != 1 || result.Points.RemainingSP != 100 || result.Points.RemainingTP != 10 {
		t.Fatalf("result = %+v", result)
	}
	persisted, ok, err := repos.Skill.Load(ctx, "31")
	if err != nil || !ok {
		t.Fatalf("load reset record ok=%t err=%v", ok, err)
	}
	if len(persisted.Skills) != 1 || persisted.Skills[46].Level != 1 {
		t.Fatalf("reset skills = %+v", persisted.Skills)
	}
	if persisted.Points != result.Points || len(persisted.Layouts) != 2 || persisted.Layouts[0][0] != 46 || persisted.Layouts[1][0] != 47 {
		t.Fatalf("reset points/layouts = %+v", persisted)
	}
	if !persisted.Cooldowns[46].Equal(cooldown) {
		t.Fatalf("reset overwrote cooldowns = %+v", persisted.Cooldowns)
	}
	if persisted.UpdatedAt.IsZero() {
		t.Fatal("reset did not update mutation timestamp")
	}
}

func TestOwnerApplySlotMovesIntoEmptyQuickSlot(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Layouts = map[int]dnfrepo.SkillLayout{0: {0: 46}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save skill layout: %v", err)
	}
	owner, _ := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}})
	result, err := owner.ApplySlot(ctx, NewSlotCommand(alignedRequest("acc-skill", 31), ChangeSkillSlotRequest{
		SkillTree: 0, From: 0, To: 3, ContextIndex: -1, Mode: 0,
	}))
	if err != nil {
		t.Fatalf("ApplySlot() error = %v", err)
	}
	if result.ToOccupied {
		t.Fatalf("result = %+v, want empty destination", result)
	}
	persisted, _, _ := repos.Skill.Load(ctx, "31")
	if _, exists := persisted.Layouts[0][0]; exists || persisted.Layouts[0][3] != 46 {
		t.Fatalf("persisted layout = %+v", persisted.Layouts[0])
	}
}

// 现场回归（HK packet_log.20260924-195607.txt:13697）：唯一一次被拒的 op28 是
//
//	type=28 operation=change_skill_slot response_allowed=false
//	reason=… passive skill=27 cannot occupy quick slot=3; success ACK is blocked
//
// 角色 2 把快捷栏 3 号与技能树 54 号互换，54 号上是被动 27（武器奥义）。客户端只在收到
// op28 成功回包时才执行本地互换（owner.go ApplySlot 顶部注释），所以那次拒绝 = 玩家拖不动
// 技能栏。这里用 50 号被动（skillRuleCatalog 的 `[passive]`）复现同一形状：服务端必须接受
// 这次由客户端自己发起的互换、落库，并回出唯一已证实的 op28 成功体。
func TestOwnerApplySlotAcceptsClientSwapOfPassiveIntoQuickSlot(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[50] = dnfrepo.SkillState{Level: 1, Enabled: true}
	record.Layouts = map[int]dnfrepo.SkillLayout{0: {3: 46, 54: 50}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save skill layout: %v", err)
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplySlot(ctx, NewSlotCommand(alignedRequest("acc-skill", 31), ChangeSkillSlotRequest{
		SkillTree: 0, From: 3, To: 54, ContextIndex: -1, Mode: 0,
	}))
	if err != nil {
		t.Fatalf("ApplySlot() error = %v; a client-requested swap that lands a passive in a quick slot must be accepted", err)
	}
	if result.FromSkillID != 46 || result.ToSkillID != 50 || !result.ToOccupied {
		t.Fatalf("result = %+v", result)
	}
	persisted, _, _ := repos.Skill.Load(ctx, "31")
	if persisted.Layouts[0][3] != 50 || persisted.Layouts[0][54] != 46 {
		t.Fatalf("persisted layout = %+v, want slot3=50 slot54=46", persisted.Layouts[0])
	}
	body, err := BuildChangeSkillSlotSuccess(result)
	if err != nil {
		t.Fatalf("BuildChangeSkillSlotSuccess() error = %v", err)
	}
	if want := []byte{1, 0, 3, 54}; !bytes.Equal(body, want) {
		t.Fatalf("op28 ACK body = %x, want %x", body, want)
	}
}

// 移除「被动不得占快捷栏」之后仍然保留的唯一槽位约束：向量必须落在当前 EXE 的 204 项内，
// 否则 op19 序列化会越界。越界行来自历史数据（ensureSkillLayout 允许到 255），必须 fail-closed
// 且不落库。
func TestOwnerApplySlotRejectsOutOfVectorSlotWithoutMutation(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[50] = dnfrepo.SkillState{Level: 1, Enabled: true}
	record.Layouts = map[int]dnfrepo.SkillLayout{0: {3: 46, 204: 50}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save skill layout: %v", err)
	}
	owner, _ := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}})
	before, _, _ := repos.Skill.Load(ctx, "31")

	if _, err := owner.ApplySlot(ctx, NewSlotCommand(alignedRequest("acc-skill", 31), ChangeSkillSlotRequest{
		SkillTree: 0, From: 3, To: 1, ContextIndex: -1, Mode: 0,
	})); !errors.Is(err, ErrSkillSlot) {
		t.Fatalf("ApplySlot() error = %v, want ErrSkillSlot", err)
	}
	after, _, _ := repos.Skill.Load(ctx, "31")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected out-of-vector layout mutated record: before=%+v after=%+v", before, after)
	}
}

func TestOwnerApplySlotRejectsUnprovenWriterFieldsWithoutMutation(t *testing.T) {
	tests := []struct {
		name string
		req  ChangeSkillSlotRequest
		err  error
	}{
		{name: "tree", req: ChangeSkillSlotRequest{SkillTree: 1, From: 0, To: 1, ContextIndex: -1, Mode: 0}, err: ErrSkillTree},
		{name: "slot_count", req: ChangeSkillSlotRequest{SkillTree: 0, From: 0, To: 204, ContextIndex: -1, Mode: 0}, err: ErrSkillSlot},
		{name: "reserved_destination", req: ChangeSkillSlotRequest{SkillTree: 0, From: 0, To: 138, ContextIndex: -1, Mode: 0}, err: ErrSkillSlot},
		{name: "context", req: ChangeSkillSlotRequest{SkillTree: 0, From: 0, To: 1, ContextIndex: 3, Mode: 0}, err: ErrSkillSlotContext},
		{name: "mode", req: ChangeSkillSlotRequest{SkillTree: 0, From: 0, To: 1, ContextIndex: -1, Mode: 1}, err: ErrSkillSlotMode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			repos := seededSkillOwnerRepositories(t, ctx)
			before, _, _ := repos.Skill.Load(ctx, "31")
			unitOfWork := &countingCharacterSkillUnitOfWork{delegate: repos.CharacterSkills}
			repos.CharacterSkills = unitOfWork
			owner, _ := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{46: 1}})
			_, err := owner.ApplySlot(ctx, NewSlotCommand(alignedRequest("acc-skill", 31), tt.req))
			if !errors.Is(err, tt.err) {
				t.Fatalf("ApplySlot() error = %v, want %v", err, tt.err)
			}
			if unitOfWork.calls != 0 {
				t.Fatalf("unit of work calls = %d, want 0", unitOfWork.calls)
			}
			after, _, _ := repos.Skill.Load(ctx, "31")
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected request mutated record: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestOwnerApplyBuyRejectsUnprovenTreesBeforeUnitOfWork(t *testing.T) {
	tests := []struct {
		name string
		tree byte
	}{
		{name: "tree_one", tree: 1},
		{name: "unknown_tree", tree: 2},
		{name: "sentinel", tree: 0xff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			repos := seededSkillOwnerRepositories(t, ctx)
			before, ok, err := repos.Skill.Load(ctx, "31")
			if err != nil || !ok {
				t.Fatalf("load skill before rejection ok=%t err=%v", ok, err)
			}
			unitOfWork := &countingCharacterSkillUnitOfWork{delegate: repos.CharacterSkills}
			repos.CharacterSkills = unitOfWork
			owner, err := NewOwner(repos, OwnerOptions{
				Catalog:       skillRuleCatalog(t),
				InitialLevels: map[uint16]int{46: 1},
			})
			if err != nil {
				t.Fatalf("NewOwner() error = %v", err)
			}

			_, err = owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
				RawSkillTree: tt.tree,
				Count:        1,
				Entries:      []BuySkillEntry{{SkillID: 46, LevelDelta: 1}},
			}))
			if !errors.Is(err, ErrSkillTree) {
				t.Fatalf("ApplyBuy() error = %v, want ErrSkillTree", err)
			}
			if unitOfWork.calls != 0 {
				t.Fatalf("CharacterSkillUnitOfWork calls = %d, want 0", unitOfWork.calls)
			}
			after, ok, loadErr := repos.Skill.Load(ctx, "31")
			if loadErr != nil || !ok {
				t.Fatalf("load skill after rejection ok=%t err=%v", ok, loadErr)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected tree mutated skill state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestOwnerApplyBuyRejectsRefundBelowPVFInitialFloor(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	_, err = owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 46, RefundFlag: 1, LevelDelta: 1}},
	}))
	if !errors.Is(err, ErrSkillLevel) {
		t.Fatalf("ApplyBuy() error = %v, want ErrSkillLevel", err)
	}
	record, _, _ := repos.Skill.Load(ctx, "31")
	if record.Skills[46].Level != 1 || record.Points.RemainingSP != 100 {
		t.Fatalf("rejected mutation changed state: %+v", record)
	}
}

func TestOwnerApplyBuyUsesSpecialPurchaseCostForTPSkill(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	owner, err := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{}})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 48, LevelDelta: 2}},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if result.Points.RemainingTP != 6 || result.Points.RemainingSP != 100 || len(result.Entries) != 1 || !result.Entries[0].TP {
		t.Fatalf("TP mutation result = %+v", result)
	}
	// 113段反馈（2026-10-15 20:00 角色123）：学习TP技能后普通技能快捷栏被自动占用。
	// 48 是 fixture 里 feature skill type 1 + special purchase cost 的 active TP 技能，
	// 必须落在技能页展示槽（active 分组 150..197），不能再拿 0..5 / 198..203 这两段
	// 普通快捷栏。普通主动技能（46/47）的自动填空仍由其它用例钉住。
	if result.Entries[0].Slot != 150 {
		t.Fatalf("TP skill slot = %d, want 150 (skill-page slot, not an ordinary quick slot)", result.Entries[0].Slot)
	}
}

func TestOwnerApplyBuyRejectsStalePointLedger(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Points.SyncedLevel = 9
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save stale ledger: %v", err)
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: skillRuleCatalog(t), InitialLevels: map[uint16]int{}})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	_, err = owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 1}},
	}))
	if !errors.Is(err, ErrSkillPointLedger) {
		t.Fatalf("ApplyBuy() error = %v, want ErrSkillPointLedger", err)
	}
}

func TestOwnerApplyBuyHydratesLegacyLedgerFromPVFBaseline(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[46] = dnfrepo.SkillState{Level: 2, Enabled: true}
	record.Points = dnfrepo.SkillPointState{}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save legacy ledger: %v", err)
	}
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
		PointBaseline: &dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100, TotalTP: 10, RemainingTP: 10, SyncedLevel: 10},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 47, LevelDelta: 1}},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if result.Points.RemainingSP != 50 || result.Points.SyncedLevel != 10 {
		t.Fatalf("hydrated points = %+v", result.Points)
	}
	persisted, ok, err := repos.Skill.Load(ctx, "31")
	if err != nil || !ok {
		t.Fatalf("load persisted skill ok=%t err=%v", ok, err)
	}
	if persisted.Points != result.Points || persisted.Layouts[0][0] != 46 || persisted.Layouts[0][1] != 47 {
		t.Fatalf("persisted hydrated record = %+v", persisted)
	}
}

func TestOwnerApplyBuySynchronizesLevelGainFromPVFBaseline(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Points = dnfrepo.SkillPointState{TotalSP: 80, RemainingSP: 40, TotalTP: 8, RemainingTP: 5, SyncedLevel: 9}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatalf("save stale ledger: %v", err)
	}
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
		PointBaseline: &dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100, TotalTP: 10, RemainingTP: 10, SyncedLevel: 10},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 1}},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if result.Points.RemainingSP != 40 || result.Points.RemainingTP != 7 || result.Points.TotalSP != 100 || result.Points.TotalTP != 10 || result.Points.SyncedLevel != 10 {
		t.Fatalf("synchronized points = %+v", result.Points)
	}
}

func TestBuildInitialSkillLayoutLimitsQuickbarToThreeActiveSkills(t *testing.T) {
	catalog := skillRuleCatalog(t)
	states := map[int64]dnfrepo.SkillState{
		46: {Level: 1, Enabled: true},
		47: {Level: 1, Enabled: true},
		48: {Level: 1, Enabled: true},
		49: {Level: 1, Enabled: true},
		50: {Level: 1, Enabled: true},
	}
	layout, err := BuildInitialSkillLayout(catalog, 0, 0, states)
	if err != nil {
		t.Fatal(err)
	}
	// 48 是 fixture 的 active TP 技能：与学习路径同一判据，初始布局也不把它算进
	// 普通快捷栏的前三格（113段反馈的 TP 自动填槽缺口覆盖新建/重建路径）。
	// 48 让位后三格由普通主动技能 46/47/49 填满，48 落在 active 技能页分组槽 150。
	want := dnfrepo.SkillLayout{0: 46, 1: 47, 2: 49, 54: 50, 150: 48}
	if !reflect.DeepEqual(layout, want) {
		t.Fatalf("layout=%v, want %v", layout, want)
	}
	for slot := 3; slot < 6; slot++ {
		if skillID, ok := layout[slot]; ok {
			t.Fatalf("unexpected primary slot %d skill %d", slot, skillID)
		}
	}
	for slot := 0; slot < 6; slot++ {
		if skillID, ok := layout[slot]; ok && skillID == 48 {
			t.Fatalf("TP fixture skill 48 was placed in ordinary quick slot %d: %v", slot, layout)
		}
	}
}

func TestBuildInitialSkillLayoutKeepsRealPVFGrantWithoutSkillDefinition(t *testing.T) {
	catalog := skillRuleCatalog(t)
	states := map[int64]dnfrepo.SkillState{
		46:  {Level: 1, Enabled: true},
		777: {Level: 1, Enabled: true},
	}
	layout, err := BuildInitialSkillLayout(catalog, 0, 0, states)
	if err != nil {
		t.Fatal(err)
	}
	if layout[0] != 46 || layout[6] != 777 {
		t.Fatalf("missing-definition PVF grant layout=%v, want active skill 46 and fallback skill 777", layout)
	}
}

func TestOwnerApplyBuyEnforcesPVFSecondGrowTypeAgainstPackedGrowth(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	character, _, _ := repos.Character.Load(ctx, "31")
	character.Stats["grow_type"] = 0x10
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: secondAwakeningSkillCatalog(t), InitialLevels: map[uint16]int{46: 1}})
	if err != nil {
		t.Fatal(err)
	}
	command := NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{Count: 1, Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 1}}})
	if _, err := owner.ApplyBuy(ctx, command); !errors.Is(err, ErrSkillGrowType) {
		t.Fatalf("first-awakening purchase error = %v", err)
	}
	character, _, _ = repos.Character.Load(ctx, "31")
	character.Stats["grow_type"] = 0x20
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplyBuy(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].SkillID != 46 || result.Points.RemainingSP != 80 {
		t.Fatalf("second-awakening purchase result = %+v", result)
	}
}

func seededSkillOwnerRepositories(t *testing.T, ctx context.Context) dnfrepo.Group {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "31",
		AccountID:   "acc-skill",
		Job:         "0",
		Level:       10,
		Stats:       map[string]int64{"grow_type": 0, currentEXESkillTreeIndexStat: 0},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repos.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: "31",
		Skills:      map[int64]dnfrepo.SkillState{46: {Level: 1, Enabled: true}},
		Points: dnfrepo.SkillPointState{
			TotalSP: 100, RemainingSP: 100,
			TotalTP: 10, RemainingTP: 10,
			SyncedLevel: 10,
		},
	}); err != nil {
		t.Fatalf("save skill: %v", err)
	}
	return repos
}

func skillRuleCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	source := skillRuleTextSource{
		"skill/skilllist.lst":     "0 `SwordmanSkill.lst`\n",
		"skill/SwordmanSkill.lst": "46 `Swordman/Base.skl`\n47 `Swordman/Dependent.skl`\n48 `Swordman/TP.skl`\n49 `Swordman/Fourth.skl`\n50 `Swordman/Passive.skl`\n51 `Swordman/HighLevel.skl`\n52 `Swordman/HighDep.skl`\n",
		"skill/Swordman/Base.skl": `
[type]
` + "`[active]`" + `
[required level]
1
[maximum level]
20
[skill fitness growtype]
0
[/skill fitness growtype]
[purchase cost]
20
[/purchase cost]
`,
		"skill/Swordman/Dependent.skl": `
[type]
` + "`[active]`" + `
[required level]
5
[maximum level]
10
[skill fitness growtype]
0
[/skill fitness growtype]
[pre required skill]
46 2
[/pre required skill]
[purchase cost]
30
[/purchase cost]
`,
		"skill/Swordman/TP.skl": `
[type]
` + "`[active]`" + `
[required level]
1
[maximum level]
3
[skill fitness growtype]
0
[/skill fitness growtype]
[pre required skill]
46 1
[/pre required skill]
[feature skill type]
1
[special purchase cost]
2
[/special purchase cost]
`,
		"skill/Swordman/Fourth.skl": `
[type]
` + "`[active]`" + `
[required level]
1
[maximum level]
20
`,
		"skill/Swordman/Passive.skl": `
[type]
` + "`[passive]`" + `
[required level]
1
[maximum level]
20
`,
		"skill/Swordman/HighLevel.skl": `
[type]
` + "`[active]`" + `
[required level]
12
[maximum level]
5
[skill fitness growtype]
0
[/skill fitness growtype]
[purchase cost]
20
[/purchase cost]
`,
		"skill/Swordman/HighDep.skl": `
[type]
` + "`[active]`" + `
[required level]
1
[maximum level]
5
[skill fitness growtype]
0
[/skill fitness growtype]
[pre required skill]
51 2
[/pre required skill]
[purchase cost]
30
[/purchase cost]
`,
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatalf("build skill index: %v", err)
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatalf("load skill catalog: %v", err)
	}
	return catalog
}

func secondAwakeningSkillCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	source := skillRuleTextSource{
		"skill/skilllist.lst":     "0 `SwordmanSkill.lst`\n",
		"skill/SwordmanSkill.lst": "46 `Swordman/Second.skl`\n",
		"skill/Swordman/Second.skl": `[type]
` + "`[active]`" + `
[required level]
1
[maximum level]
20
[skill fitness growtype]
0
[/skill fitness growtype]
[skill fitness second growtype]
2
[/skill fitness second growtype]
[purchase cost]
20
[/purchase cost]
`,
	}
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

type skillRuleTextSource map[string]string

func (s skillRuleTextSource) ReadText(relativePath string) (string, error) {
	want := strings.ToLower(strings.ReplaceAll(relativePath, "\\", "/"))
	for key, value := range s {
		if strings.ToLower(strings.ReplaceAll(key, "\\", "/")) == want {
			return value, nil
		}
	}
	return "", dnfpvf.ErrDocNotFound
}

func alignedRequest(accountID string, selectedCharacterID uint16) alignedcmd.Request {
	return alignedcmd.Request{AccountID: accountID, SelectedCharacterID: selectedCharacterID}
}

type countingCharacterSkillUnitOfWork struct {
	delegate dnfrepo.CharacterSkillUnitOfWork
	calls    int
}

func (u *countingCharacterSkillUnitOfWork) WithinCharacterSkill(ctx context.Context, characterID string, apply func(dnfrepo.SkillRepository) error) error {
	u.calls++
	return u.delegate.WithinCharacterSkill(ctx, characterID, apply)
}

func TestOwnerApplyBuyOverSkillContractRaisesEffectiveLevel(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	buy := BuySkillRequest{Count: 1, Entries: []BuySkillEntry{{SkillID: 51, LevelDelta: 1}}}
	if _, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), buy)); !errors.Is(err, ErrSkillLevel) {
		t.Fatalf("without contract ApplyBuy() error = %v, want ErrSkillLevel", err)
	}

	future := time.Now().Add(24 * time.Hour).Unix()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "acc-skill",
		Metadata:  map[string]string{"premium_expire_27": strconv.FormatInt(future, 10)},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), buy))
	if err != nil {
		t.Fatalf("with over-skill contract ApplyBuy() error = %v", err)
	}
	if result.Points.RemainingSP != 80 {
		t.Fatalf("remaining SP = %d, want 80", result.Points.RemainingSP)
	}
	record, _, _ := repos.Skill.Load(ctx, "31")
	if record.Skills[51].Level != 1 {
		t.Fatalf("skill 51 level = %d, want 1", record.Skills[51].Level)
	}
}

func TestOwnerApplyBuyRefundConsumesForgetRiverWaterAtomically(t *testing.T) {
	// 回退档：只有 DNFBRIDGE_SKILL_REFUND_REQUIRE_WATER=1 才恢复"退技能必须
	// 消耗一个遗忘河之水"的改动前行为（默认档按 refund_water.go 免费退款）。
	t.Setenv(skillRefundRequireWaterEnv, "1")
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[46] = dnfrepo.SkillState{Level: 2, Enabled: true}
	record.Points.RemainingSP = 80
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "31",
		Slots:       map[string]dnfrepo.ItemStack{"0:5": {ItemID: forgetRiverWaterItemID, Count: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 46, RefundFlag: 1, LevelDelta: 1}},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if !result.ConsumedRefundItem || result.ConsumedRefundItemSlot != 5 {
		t.Fatalf("result = %+v, want consumed water slot 5", result)
	}
	if result.Points.RemainingSP != 100 {
		t.Fatalf("remaining SP = %d, want refunded 100", result.Points.RemainingSP)
	}
	skill, _, _ := repos.Skill.Load(ctx, "31")
	if skill.Skills[46].Level != 1 {
		t.Fatalf("skill 46 level = %d, want 1", skill.Skills[46].Level)
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "31")
	if _, exists := inventory.Slots["0:5"]; exists {
		t.Fatalf("water stack still exists: %+v", inventory.Slots)
	}
}

func TestOwnerApplyBuyRefundWithoutWaterFailsClosedUnderLegacyWaterSwitch(t *testing.T) {
	// 回退档钉住改动前的失败口径：没有遗忘河之水必须整体拒绝且不落库
	// （ErrSkillRefundConsumableRequired → op29 失败码 3）。
	t.Setenv(skillRefundRequireWaterEnv, "1")
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[46] = dnfrepo.SkillState{Level: 2, Enabled: true}
	record.Points.RemainingSP = 80
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "31", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	_, err = owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 46, RefundFlag: 1, LevelDelta: 1}},
	}))
	if !errors.Is(err, ErrSkillRefundConsumableRequired) {
		t.Fatalf("ApplyBuy() error = %v, want ErrSkillRefundConsumableRequired", err)
	}
	skill, _, _ := repos.Skill.Load(ctx, "31")
	if skill.Skills[46].Level != 2 || skill.Points.RemainingSP != 80 {
		t.Fatalf("rejected refund changed skill state: %+v", skill)
	}
}

func TestOwnerApplyBuyRefundFreeWhileLetheContractActive(t *testing.T) {
	// 遗忘河水契约在**回退档**下也必须让退款免费（契约优先级高于消耗品门槛）。
	t.Setenv(skillRefundRequireWaterEnv, "1")
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[46] = dnfrepo.SkillState{Level: 2, Enabled: true}
	record.Points.RemainingSP = 80
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(24 * time.Hour).Unix()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "acc-skill",
		Metadata:  map[string]string{"premium_expire_33": strconv.FormatInt(future, 10)},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 46, RefundFlag: 1, LevelDelta: 1}},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if result.ConsumedRefundItem {
		t.Fatalf("lethe contract refund must not consume water: %+v", result)
	}
	if result.Points.RemainingSP != 100 {
		t.Fatalf("remaining SP = %d, want refunded 100", result.Points.RemainingSP)
	}
	skill, _, _ := repos.Skill.Load(ctx, "31")
	if skill.Skills[46].Level != 1 {
		t.Fatalf("skill 46 level = %d, want 1", skill.Skills[46].Level)
	}
}

func seedOverLevelContractSkills(t *testing.T, ctx context.Context, repos dnfrepo.Group) {
	t.Helper()
	record, _, _ := repos.Skill.Load(ctx, "31")
	record.Skills[46] = dnfrepo.SkillState{Level: 1, Enabled: true}
	record.Skills[51] = dnfrepo.SkillState{Level: 2, Enabled: true}
	record.Skills[52] = dnfrepo.SkillState{Level: 1, Enabled: true}
	// 51 costs 20x2 and 52 costs 30: 100-40-30=30 remaining.
	record.Points.RemainingSP = 30
	record.Layouts = map[int]dnfrepo.SkillLayout{0: {0: 46, 1: 51, 2: 52}}
	if err := repos.Skill.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerApplyBuySweepsExpiredOverSkillContractSkillsWithCascade(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	seedOverLevelContractSkills(t, ctx, repos)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	// No active 达人契约: skill 51 (requires 12 > char 10) is swept, and skill
	// 52 (depends on 51>=2) cascades out; both refund their SP.
	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 46, LevelDelta: 1}},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if !result.ExpiredContractSkillsReset {
		t.Fatalf("result = %+v, want ExpiredContractSkillsReset", result)
	}
	record, _, _ := repos.Skill.Load(ctx, "31")
	if _, exists := record.Skills[51]; exists {
		t.Fatalf("over-level skill 51 still learned: %+v", record.Skills)
	}
	if _, exists := record.Skills[52]; exists {
		t.Fatalf("broken dependent skill 52 still learned: %+v", record.Skills)
	}
	if record.Skills[46].Level != 2 {
		t.Fatalf("skill 46 level = %d, want 2", record.Skills[46].Level)
	}
	// 30 + 40 (51) + 30 (52) = 100, then -20 for the buy = 80.
	if record.Points.RemainingSP != 80 {
		t.Fatalf("remaining SP = %d, want 80", record.Points.RemainingSP)
	}
	for slot, id := range record.Layouts[0] {
		if id == 51 || id == 52 {
			t.Fatalf("layout slot %d still holds swept skill %d", slot, id)
		}
	}
}

func TestOwnerApplyBuyKeepsOverLevelSkillsWhileOverSkillContractActive(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	seedOverLevelContractSkills(t, ctx, repos)
	future := time.Now().Add(24 * time.Hour).Unix()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "acc-skill",
		Metadata:  map[string]string{"premium_expire_27": strconv.FormatInt(future, 10)},
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog:       skillRuleCatalog(t),
		InitialLevels: map[uint16]int{46: 1},
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}

	result, err := owner.ApplyBuy(ctx, NewBuyCommand(alignedRequest("acc-skill", 31), BuySkillRequest{
		Count:   1,
		Entries: []BuySkillEntry{{SkillID: 51, LevelDelta: 1}},
	}))
	if err != nil {
		t.Fatalf("ApplyBuy() error = %v", err)
	}
	if result.ExpiredContractSkillsReset {
		t.Fatalf("active contract must not sweep: %+v", result)
	}
	record, _, _ := repos.Skill.Load(ctx, "31")
	if record.Skills[51].Level != 3 || record.Skills[52].Level != 1 {
		t.Fatalf("contract-learned skills changed: %+v", record.Skills)
	}
}
