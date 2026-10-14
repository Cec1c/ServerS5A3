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

// dailyLifecycleQuestID 用真档安徒恩每日任务的编号与数值（10019 提取安徒恩的
// 神经细胞：交 3 个 10157840 真皮、发 5 个 10157918），定义用合成目录钉住，
// 以便结算能跑真实 ApplyFinishSettlement 而不受真档进度表限制。
const dailyLifecycleQuestID = 10019

func dailyLifecycleCatalog() *Catalog {
	definition := Definition{
		ID: dailyLifecycleQuestID, Path: "n_quest/anton/anton_limit_01.qst",
		Type: "[seeking]", Grade: "[daily mission]", LevelMin: 1, LevelMax: 99, Difficulty: "N",
		// 真档 10019 由 NPC 发布（[npc index] 326），ExposedByNPC 非 0 才有资格进可接面。
		ExposedByNPC: 1,
		IntData:      []int64{10157840, 3}, RewardType: "[item]",
		RewardItems: []RewardItemRule{{ItemID: 10157918, Count: 5}},
	}
	return &Catalog{ordered: []Definition{definition}, byID: map[int64]Definition{definition.ID: definition}}
}

func dailyLifecycleAcceptCommand() Command {
	return NewQuestIDCommand(alignedRequest("acc", 19), "accept_quest", QuestIDRequest{QuestID: dailyLifecycleQuestID})
}

// dailyLifecycleTriggerCommand 是客户端在 NPC 对话里关闭任务步骤的 op33：
// triggerType 0 + 非增量会把已接任务的初始触发（seeking 档为 1）降到 0，
// 之后客户端才发 op34 确认。参考实现同样要求确认前触发为 0。
func dailyLifecycleTriggerCommand() Command {
	return NewTriggerCommand(alignedRequest("acc", 19), SetTriggerRequest{
		QuestID: dailyLifecycleQuestID, TriggerType: 0, IsIncrement: false,
	})
}

func dailyLifecycleFinishInput(t *testing.T, committedAt time.Time) FinishCommitInput {
	t.Helper()
	return FinishCommitInput{
		AccountID: "acc", CharacterID: "19", QuestID: dailyLifecycleQuestID, Multiplier: 1,
		CommittedAt: committedAt, Progression: finishOwnerProgression(t), AllocateItem: finishOwnerAllocator(nil),
	}
}

func dailyLifecycleDayKey(at time.Time) string {
	return strconv.FormatInt(premium.ServiceDayID(at.UTC()), 10)
}

func dailyLifecycleAddMaterial(t *testing.T, ctx context.Context, repos dnfrepo.Group, slotKey string, slot uint16) {
	t.Helper()
	inventory, found, err := repos.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw[0:2], slot)
	binary.LittleEndian.PutUint32(raw[2:6], 10157840)
	binary.LittleEndian.PutUint32(raw[6:10], 3)
	inventory.Slots[slotKey] = dnfrepo.ItemStack{
		ItemID: 10157840, Count: 3, RawEntry: raw, Extra: map[string]string{"count": "3"},
	}
	if err := repos.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
}

func dailyLifecycleLoadQuest(t *testing.T, ctx context.Context, repos dnfrepo.Group) dnfrepo.QuestRecord {
	t.Helper()
	record, found, err := repos.Quest.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	return record
}

// TestDailyMissionTwoServiceDayLifecycleGrantsOncePerDay 是两个服务日的完整生命周期：
// 日1 接取→达成条件→完成发奖；日2 刷新（完成行过期）→重新接取→达成条件→再发奖一次；
// 日2 重放不二次发奖；日3 再次刷新。
//
// 两种存档布局都跑：
//   - completion_in_states：完成行在规范的 States 里；
//   - legacy_completion_in_progress：完成行只留在旧列 Progress（States 为空），
//     日2 接取后仍把这条昨日完成回执留在 Progress —— MySQL 的字段级保存只写
//     states 列，Progress 旧列不会被清掉，所以这是现场旧存档的真实形态。
//
// 关键断言是 finish 取的是当日 States 的 active 行：日2 的结算必须是新回执
// （Replayed=false 且 completion_key 与日1 不同），不能“只可见却仍返回昨日 receipt”。
func TestDailyMissionTwoServiceDayLifecycleGrantsOncePerDay(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "completion_in_states"
		if legacy {
			name = "legacy_completion_in_progress"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := finishOwnerSeed(t, ctx)
			catalog := dailyLifecycleCatalog()
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			eligibility := CharacterEligibility{Level: 1, Job: 2}
			day1 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			day2 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			day3 := day2.Add(24 * time.Hour)

			// ── 日1：接取 → 关闭 NPC 步骤 → 完成发奖 ──
			dailyLifecycleAddMaterial(t, ctx, repos, "0:8", 8)
			accept1, err := owner.ApplyAcceptAt(ctx, catalog, eligibility, dailyLifecycleAcceptCommand(), day1)
			if err != nil || accept1.Idempotent || accept1.InitTrigger != 1 {
				t.Fatalf("day1 accept=%+v err=%v", accept1, err)
			}
			trigger1, err := owner.ApplySetTrigger(ctx, dailyLifecycleTriggerCommand())
			if err != nil || !trigger1.StateChanged || trigger1.ProgressValue != 0 {
				t.Fatalf("day1 trigger=%+v err=%v", trigger1, err)
			}
			finish1, err := owner.ApplyFinishSettlement(ctx, catalog, dailyLifecycleFinishInput(t, day1))
			if err != nil {
				t.Fatalf("day1 finish: %v", err)
			}
			if finish1.Replayed || finish1.CompletionKey == "" {
				t.Fatalf("day1 finish=%+v", finish1)
			}
			if len(finish1.Items) != 1 || finish1.Items[0].Delta != 5 || finish1.Items[0].PostCount != 5 {
				t.Fatalf("day1 grant=%+v", finish1.Items)
			}
			record := dailyLifecycleLoadQuest(t, ctx, repos)
			day1Completion, found := record.States[dailyLifecycleQuestID]
			if !found || day1Completion.Status != "completed" ||
				day1Completion.Extra[QuestDailyCompletionDayKey] != dailyLifecycleDayKey(day1) {
				t.Fatalf("day1 completion present=%t status=%q completionDay=%q want %q",
					found, day1Completion.Status, day1Completion.Extra[QuestDailyCompletionDayKey], dailyLifecycleDayKey(day1))
			}

			if legacy {
				// 旧存档：完成行只在 Progress 列。
				record.Progress = map[int64]dnfrepo.QuestState{dailyLifecycleQuestID: day1Completion}
				delete(record.States, dailyLifecycleQuestID)
				if err := repos.Quest.Save(ctx, record); err != nil {
					t.Fatal(err)
				}
				record = dailyLifecycleLoadQuest(t, ctx, repos)
				if _, canonical := record.States[dailyLifecycleQuestID]; canonical {
					t.Fatal("legacy layout must not keep the day1 completion in States")
				}
			}

			// ── 日2：刷新 ──
			if !containsQuestID(catalog.QuestListAt(eligibility, record, day2).IDs, dailyLifecycleQuestID) {
				t.Fatalf("day2 refresh must relist the daily mission (%s)", name)
			}
			if decision, err := catalog.DecideAcceptAt(eligibility, record, dailyLifecycleQuestID, day2); err != nil || decision != AcceptNew {
				t.Fatalf("day2 decide=%v err=%v", decision, err)
			}

			// ── 日2：接取 ──
			accept2, err := owner.ApplyAcceptAt(ctx, catalog, eligibility, dailyLifecycleAcceptCommand(), day2)
			if err != nil || accept2.Idempotent || accept2.InitTrigger != 1 {
				t.Fatalf("day2 accept=%+v err=%v", accept2, err)
			}
			record = dailyLifecycleLoadQuest(t, ctx, repos)
			active, found := record.States[dailyLifecycleQuestID]
			if !found || !isActiveQuestStatus(active.Status) || active.ProgressValue != 1 {
				t.Fatalf("day2 accepted state present=%t status=%q progress=%d", found, active.Status, active.ProgressValue)
			}
			if active.Extra["reward_state"] != "" || active.Extra["completion_key"] != "" ||
				active.Extra[finishSettlementReceiptKey] != "" {
				t.Fatalf("day2 accept inherited the previous day's receipt: reward_state=%q completion_key=%q",
					active.Extra["reward_state"], active.Extra["completion_key"])
			}
			if legacy {
				// 还原被字段级保存留在旧列里的昨日回执，确保 finish 真的在
				// “Progress 有旧 receipt、States 有当日 active”两种来源共存时做选择。
				if record.Progress == nil {
					record.Progress = make(map[int64]dnfrepo.QuestState, 1)
				}
				record.Progress[dailyLifecycleQuestID] = day1Completion
				if err := repos.Quest.Save(ctx, record); err != nil {
					t.Fatal(err)
				}
				record = dailyLifecycleLoadQuest(t, ctx, repos)
				if _, kept := record.Progress[dailyLifecycleQuestID]; !kept {
					t.Fatal("legacy Progress completion must be present so precedence is really tested")
				}
			}

			// ── 日2：关闭 NPC 步骤 → 完成发奖一次 ──
			dailyLifecycleAddMaterial(t, ctx, repos, "0:9", 9)
			trigger2, err := owner.ApplySetTrigger(ctx, dailyLifecycleTriggerCommand())
			if err != nil || !trigger2.StateChanged || trigger2.ProgressValue != 0 {
				t.Fatalf("day2 trigger=%+v err=%v", trigger2, err)
			}
			finish2, err := owner.ApplyFinishSettlement(ctx, catalog, dailyLifecycleFinishInput(t, day2))
			if err != nil {
				t.Fatalf("day2 finish: %v", err)
			}
			// 不能是“只可见、领取时仍返回昨日 receipt”。completion_key 内嵌 op33 的
			// 墙上时钟，同一时钟刻度可能撞值，所以这里比较结算语义而不是 key 字符串：
			// 日2 必须从日1 结算后的等级/经验继续算，并再发一次奖。
			if finish2.Replayed ||
				finish2.ExperienceDelta != finish1.ExperienceDelta ||
				finish2.PreviousExperience != finish1.NewExperience ||
				finish2.NewExperience != finish1.NewExperience+finish2.ExperienceDelta {
				t.Fatalf("day2 settlement=%+v, want a fresh grant continuing from day1=%+v", finish2, finish1)
			}
			if len(finish2.Items) != 1 || finish2.Items[0].Delta != 5 || finish2.Items[0].PostCount != 10 {
				t.Fatalf("day2 grant=%+v", finish2.Items)
			}
			record = dailyLifecycleLoadQuest(t, ctx, repos)
			completed2, found := record.States[dailyLifecycleQuestID]
			if !found || completed2.Status != "completed" ||
				completed2.Extra[QuestDailyCompletionDayKey] != dailyLifecycleDayKey(day2) {
				t.Fatalf("day2 completion present=%t status=%q completionDay=%q want %q",
					found, completed2.Status, completed2.Extra[QuestDailyCompletionDayKey], dailyLifecycleDayKey(day2))
			}
			// 日2 必须改写当日的 States 行，并写下日2 自己的回执；旧回执不得复用。
			// 回执内嵌本次消耗的槽位，两天必然不同（0:8 vs 0:9）。
			if completed2.Extra[finishSettlementReceiptKey] == "" ||
				completed2.Extra[finishSettlementReceiptKey] == day1Completion.Extra[finishSettlementReceiptKey] {
				t.Fatalf("day2 receipt len=%d, want a non-empty receipt distinct from day1's len=%d",
					len(completed2.Extra[finishSettlementReceiptKey]), len(day1Completion.Extra[finishSettlementReceiptKey]))
			}
			if legacy {
				// 旧列 Progress 仍留昨日完成行（MySQL 字段级保存只写 states 列），
				// 上面的断言证明结算取的是当日 States 行，而不是这条旧回执。
				if stale := record.Progress[dailyLifecycleQuestID]; stale.Status != "completed" ||
					stale.Extra[QuestDailyCompletionDayKey] != dailyLifecycleDayKey(day1) {
					t.Fatalf("legacy Progress row status=%q completionDay=%q, want the untouched day1 completion",
						stale.Status, stale.Extra[QuestDailyCompletionDayKey])
				}
			}
			inventory, found, err := repos.Inventory.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			if _, exists := inventory.Slots["0:9"]; exists {
				t.Fatalf("day2 material was not consumed: %+v", inventory.Slots["0:9"])
			}

			// ── 日2 重放：不二次发奖 ──
			replay, err := owner.ApplyFinishSettlement(ctx, catalog, dailyLifecycleFinishInput(t, day2))
			if err != nil {
				t.Fatalf("day2 replay: %v", err)
			}
			if !replay.Replayed || replay.CompletionKey != finish2.CompletionKey {
				t.Fatalf("day2 replay=%+v", replay)
			}
			inventory, found, err = repos.Inventory.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("reload inventory found=%t err=%v", found, err)
			}
			if got := inventory.Slots["0:65"].Count; got != 10 {
				t.Fatalf("reward slot=%d, want 10 (5 per service day, no third grant)", got)
			}

			// ── 日3：再次刷新 ──
			record = dailyLifecycleLoadQuest(t, ctx, repos)
			if !containsQuestID(catalog.QuestListAt(eligibility, record, day3).IDs, dailyLifecycleQuestID) {
				t.Fatal("day3 refresh must relist the daily mission again")
			}
		})
	}
}
