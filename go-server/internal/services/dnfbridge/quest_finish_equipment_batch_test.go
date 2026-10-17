package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 奖励行对应本机4728的job=11/grow=4；隔离仓储和低等级表用于事务回归，
// 不替换运行PVF或真实玩家数据。另有下方真实PVF测试核对实际目录与奖励。
func TestCurrentFinishQuestDuplicateEquipmentCommitsDistinctInstances(t *testing.T) {
	source := finishBridgePVFSource{
		dnfquest.DefaultList: "4728 `batch.qst`\n",
		"n_quest/batch.qst":  "[type]\n`[meet npc]`\n[difficulty]\n`N`\n[level]\n1 99\n[job]\n`[at swordman]`\n[gold multiple]\n10\n[reward type]\n`[item]`\n[reward int data]\n101040191 `[job]` 11 4 1\n101040191 `[job]` 11 4 1\n100060157 `[job]` 11 4 1\n100110146 `[job]` 11 4 1\n100160110 `[job]` 11 4 1\n100210132 `[job]` 11 4 1\n100260138 `[job]` 11 4 1\n[/reward int data]\n",
	}
	catalog := finishEquipmentBatchQuestCatalog(t, source)
	items := &pvfDungeonDropCatalog{source: source, itemCache: make(map[uint32]dungeonDropItemDefinition)}
	for _, id := range []uint32{101040191, 100060157, 100110146, 100160110, 100210132, 100260138} {
		items.itemCache[id] = dungeonDropItemDefinition{ItemID: id, Kind: dungeonDropItemEquipment, PVFPath: "equipment/batch.equ", SlotStart: 9, SlotEnd: 64, Durability: 30}
	}
	for _, full := range []bool{false, true} {
		name := "提交与重放"
		if full {
			name = "中途背包满整笔回滚"
		}
		t.Run(name, func(t *testing.T) {
			finishEquipmentBatchSettlement(t, catalog, items, finishBridgeProgression(t), 1, full)
		})
	}
}

func finishEquipmentBatchQuestCatalog(t *testing.T, source dnfpvf.Source) *dnfquest.Catalog {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func finishEquipmentBatchSettlement(t *testing.T, catalog *dnfquest.Catalog, items *pvfDungeonDropCatalog, tables *progression.Tables, level int, full bool) {
	t.Helper()
	ctx := context.Background()
	repos := finishBridgeSeed(t)
	character, _, _ := repos.Character.Load(ctx, "19")
	character.Job = "11"
	character.Level = level
	character.Stats["grow_type"] = 4
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := repos.Skill.Load(ctx, "19")
	skill.Points.SyncedLevel = level
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	accepted := time.Date(2026, 10, 13, 14, 5, 19, 0, time.UTC)
	quests := dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{
		4728: {Status: "active", ProgressValue: 0, UpdatedAt: accepted},
		6939: {Status: "completed"},
	}}
	if err := repos.Quest.Save(ctx, quests); err != nil {
		t.Fatal(err)
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "19")
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	if full {
		// 留两格让两把武器先在事务副本入包，随后护甲无格可放。
		// 既有本地规则允许装备页满时回落快捷格，满包夹具须占满这些格。
		for slot := int16(3); slot <= 8; slot++ {
			inventory.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 1, Count: 1}
		}
		for slot := int16(9); slot <= 62; slot++ {
			inventory.Slots[currentDungeonPickupMainSlotKey(slot)] = dnfrepo.ItemStack{ItemID: 1, Count: 1}
		}
		if err := repos.Inventory.Save(ctx, inventory); err != nil {
			t.Fatal(err)
		}
	}
	beforeChar, _, _ := repos.Character.Load(ctx, "19")
	beforeInv, _, _ := repos.Inventory.Load(ctx, "19")
	beforeQuest, _, _ := repos.Quest.Load(ctx, "19")
	beforeSkill, _, _ := repos.Skill.Load(ctx, "19")
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	input := dnfquest.FinishCommitInput{AccountID: character.AccountID, CharacterID: "19", QuestID: 4728, RewardSelectIndex: ^uint16(0), Multiplier: 1, CommittedAt: accepted.Add(time.Minute), Progression: tables, AllocateItem: currentQuestFinishItemAllocator(items)}
	result, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if full {
		if !errors.Is(err, errDungeonPickupInventoryFull) {
			t.Fatalf("背包满未由实际入包上限拒绝: %v", err)
		}
		afterChar, _, _ := repos.Character.Load(ctx, "19")
		afterInv, _, _ := repos.Inventory.Load(ctx, "19")
		afterQuest, _, _ := repos.Quest.Load(ctx, "19")
		afterSkill, _, _ := repos.Skill.Load(ctx, "19")
		if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeInv, afterInv) || !reflect.DeepEqual(beforeQuest, afterQuest) || !reflect.DeepEqual(beforeSkill, afterSkill) {
			t.Fatalf("奖励失败后状态不一致: character=%t inventory=%t quest=%t skill=%t; 错误=%v; 角色前=%+v 后=%+v; 任务前=%+v 后=%+v", reflect.DeepEqual(beforeChar, afterChar), reflect.DeepEqual(beforeInv, afterInv), reflect.DeepEqual(beforeQuest, afterQuest), reflect.DeepEqual(beforeSkill, afterSkill), err, beforeChar, afterChar, beforeQuest, afterQuest)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !result.AtomicCommitted || result.Replayed || len(result.Items) != 7 {
		t.Fatalf("提交结果=%+v", result)
	}
	body, err := buildCurrentFinishQuestSuccessBody(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 10+19*7 || body[9] != 7 {
		t.Fatalf("奖励ACK行数/长度错误: %x", body)
	}
	seen := make(map[string]bool)
	weapons := 0
	for i, item := range result.Items {
		if seen[item.SlotKey] || item.Delta != 1 || item.PostCount != 1 {
			t.Fatalf("装备回执未逐件分格: %+v", item)
		}
		seen[item.SlotKey] = true
		if item.ItemID == 101040191 {
			weapons++
		}
		row := body[10+i*19 : 10+(i+1)*19]
		if binary.LittleEndian.Uint16(row[:2]) != item.SlotIndex || binary.LittleEndian.Uint32(row[2:6]) != uint32(item.ItemID) || binary.LittleEndian.Uint32(row[6:10]) != item.CountOrSeed {
			t.Fatalf("ACK与真实装备回执不一致: %x %+v", row, item)
		}
		stack := result.PostCommitInventory.Slots[item.SlotKey]
		if stack.Count != 1 || stack.Extra["last_grant_quest_id"] != "4728" {
			t.Fatalf("持久化装备数量/来源错误: %+v", stack)
		}
	}
	if weapons != 2 {
		t.Fatalf("原PVF两把武器被少发: %d", weapons)
	}
	beforeReplay, _, _ := repos.Inventory.Load(ctx, "19")
	replay, err := owner.ApplyFinishSettlement(ctx, catalog, input)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(result.Items, replay.Items) {
		t.Fatalf("重放回执错误: %+v %v", replay, err)
	}
	afterReplay, _, _ := repos.Inventory.Load(ctx, "19")
	if !reflect.DeepEqual(beforeReplay, afterReplay) {
		t.Fatal("重放重复发奖")
	}
}

type finishEquipmentSingleQuestSource struct{ dnfpvf.Source }

func (s finishEquipmentSingleQuestSource) ReadText(path string) (string, error) {
	if path == dnfquest.DefaultList {
		return "4728 `job/job_w_ats01.qst`\n", nil
	}
	return s.Source.ReadText(path)
}

func TestRealPVFCurrentFinishQuest4728DuplicateEquipment(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("设置 DNFBRIDGE_REAL_PVF_SMOKE 核对本机真实4728奖励")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog := finishEquipmentBatchQuestCatalog(t, finishEquipmentSingleQuestSource{archive})
	plan, err := catalog.PlanFinishReward(dnfquest.CharacterEligibility{Level: 90, Job: 11, GrowType: 4}, 4728, ^uint16(0), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 7 || plan.Items[0].ItemID != 101040191 || plan.Items[1].ItemID != 101040191 || plan.Items[0].Count != 1 || plan.Items[1].Count != 1 {
		t.Fatalf("实际4728奖励与故障取证不符: %+v", plan)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range plan.Items {
		definition, err := items.ResolveItem(uint32(rule.ItemID))
		if err != nil || definition.Kind != dungeonDropItemEquipment {
			t.Fatalf("真实奖励不是装备: %+v %v", definition, err)
		}
	}
	tables, err := progression.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	finishEquipmentBatchSettlement(t, catalog, items, tables, 90, false)
	finishEquipmentBatchSettlement(t, catalog, items, tables, 90, true)
}
