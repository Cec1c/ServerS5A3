package quest

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// TestOwnerFinishUnclaimedDailyMissionFromEarlierServiceDay 覆盖 DOC-122 的“未领取”
// 分支：10.13 达成任务条件（NPC 步骤已归零）但当天没有确认，10.14 再确认。
//
// 这里是真实 ApplyFinishSettlement 路径（与 op34 确认结算同一个入口）：固定真档
// 10019「提取安徒恩的神经细胞」的数值（交 3 个 10157840，发 5 个 10157918），
// 但用合成目录把定义钉住，因此不依赖真档资源。
//
// 断言的是本次修复真正负责的部分：隔天确认必须成功、材料只扣一次、奖励只发一次，
// 且完成行落库时写下“今天”的服务日键（下一天才能重新刷新）。
// 完成日键用字面量，保证本用例在修复前的源码上也能编译并失败。
func TestOwnerFinishUnclaimedDailyMissionFromEarlierServiceDay(t *testing.T) {
	ctx := context.Background()
	repos := finishOwnerSeed(t, ctx)
	acceptedAt := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	// 条件已达成、NPC 步骤归零，但没有确认：active + ProgressValue 0 且没有 reward_state。
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		10019: {Status: "active", ProgressValue: 0, UpdatedAt: acceptedAt},
	}}); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw[0:2], 8)
	binary.LittleEndian.PutUint32(raw[2:6], 10157840)
	binary.LittleEndian.PutUint32(raw[6:10], 3)
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:8": {ItemID: 10157840, Count: 3, RawEntry: raw, Extra: map[string]string{"count": "3"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	definition := Definition{
		ID: 10019, Path: "n_quest/anton/anton_limit_01.qst", Type: "[seeking]", Grade: "[daily mission]",
		LevelMin: 1, LevelMax: 99, Difficulty: "N", IntData: []int64{10157840, 3},
		RewardType: "[item]", RewardItems: []RewardItemRule{{ItemID: 10157918, Count: 5}},
	}
	catalog := &Catalog{ordered: []Definition{definition}, byID: map[int64]Definition{definition.ID: definition}}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	committedAt := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	input := FinishCommitInput{
		AccountID: "acc", CharacterID: "19", QuestID: definition.ID, Multiplier: 1,
		CommittedAt: committedAt, Progression: finishOwnerProgression(t), AllocateItem: finishOwnerAllocator(nil),
	}
	first, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatalf("next-day confirm of an unclaimed daily mission: %v", err)
	}
	if !first.AtomicCommitted || first.Replayed {
		t.Fatalf("first settlement=%+v", first)
	}
	if len(first.ConsumedItems) != 1 {
		t.Fatalf("consumed=%+v", first.ConsumedItems)
	}
	consumed := first.ConsumedItems[0]
	if consumed.ItemID != 10157840 || consumed.Delta != 3 || consumed.PostCount != 0 {
		t.Fatalf("consumed daily material=%+v", consumed)
	}
	if len(first.Items) != 1 || first.Items[0].ItemID != 10157918 || first.Items[0].Delta != 5 || first.Items[0].PostCount != 5 {
		t.Fatalf("granted daily reward=%+v", first.Items)
	}

	stored, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload quest found=%t err=%v", found, err)
	}
	state := stored.States[10019]
	if state.Status != "completed" {
		t.Fatalf("finished daily status=%q, want completed", state.Status)
	}
	if got, want := state.Extra["quest_daily_completed_day_v1"], strconv.FormatInt(premium.ServiceDayID(committedAt), 10); got != want {
		t.Fatalf("finished daily completion day=%q, want %q (the confirming service day)", got, want)
	}

	// 重放同一次确认：返回回执，不重复扣料、不重复发奖。
	second, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || len(second.Items) != 1 || second.Items[0].Delta != 5 {
		t.Fatalf("replay settlement=%+v", second)
	}
	inventory, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("reload inventory found=%t err=%v", found, err)
	}
	if _, exists := inventory.Slots["0:8"]; exists {
		t.Fatalf("replay left the consumed daily material: %+v", inventory.Slots["0:8"])
	}
	if got := inventory.Slots["0:65"].Count; got != 5 {
		t.Fatalf("reward slot count=%d, want 5 granted exactly once", got)
	}
}
