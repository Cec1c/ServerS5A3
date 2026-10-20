package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"testing"
	"time"

	dnfhonor "longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func trainingRewardBridgeCatalog(t *testing.T) *dnfquest.Catalog {
	t.Helper()
	source := finishBridgePVFSource{dnfquest.DefaultList: "2674 `pvp/pvp_tookuk_05.qst`\n", "n_quest/pvp/pvp_tookuk_05.qst": "[type]\n`[custom multi condition]`\n[int data]\n1 1 1 1\n[sub reward]\n10158452\n[reward type]\n`[tp]`\n"}
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

func TestPVPTrainingRewardDispatchPersistsAndPublishesCurrentInventory(t *testing.T) {
	s := pvpRoomTestService(t)
	s.questCatalog = trainingRewardBridgeCatalog(t)
	itemCatalog := &pvfDungeonDropCatalog{source: finishBridgePVFSource{"stackable/book.stk": "[stackable type]\n`[material]` 10\n"},
		itemRefs:  map[uint32]dungeonDropItemReference{10158452: {kind: dungeonDropItemStackable, path: "stackable/book.stk"}},
		itemCache: map[uint32]dungeonDropItemDefinition{10158452: {ItemID: 10158452, Kind: dungeonDropItemStackable, PVFPath: "stackable/book.stk", StackLimit: 1000, SlotStart: 121, SlotEnd: 176}}}
	s.questFinishResourceLoader = func(context.Context) (*progression.Tables, *pvfDungeonDropCatalog, error) {
		return nil, itemCatalog, nil
	}
	g := &gameSession{conn: &bufferConn{}, accountID: "pvp-29", selectedCharacterID: 29, channel: s.catalog.Channels()[1]}
	g.townPositionSnapshot = currentTownPositionSnapshot{CharacterID: 29, TownID: 10, AreaID: 4, PositionValid: true}
	g.townSceneReadyCharacterID = 29
	r, _ := s.repositoryGroup()
	ctx := context.Background()
	if err := r.Quest.Save(ctx, repo.QuestRecord{CharacterID: "29", States: map[int64]repo.QuestState{2674: {Status: "active", ProgressValue: 1 << 18, Extra: map[string]string{"pvf_path": "n_quest/pvp/pvp_tookuk_05.qst"}}}}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := r.Skill.Load(ctx, "29")
	for _, invalid := range []struct {
		class   byte
		body    []byte
		outdoor bool
	}{{0, nil, false}, {1, []byte{1}, false}, {1, nil, true}} {
		g.townPositionSnapshot.AreaID = 4
		if invalid.outdoor {
			g.townPositionSnapshot.AreaID = 0
		}
		_, err := currentGameplayModules.DispatchUpper(s, g, 1267, invalid.class, invalid.body)
		if err != nil || g.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("错误分类/负载/场景被接受", err)
		}
	}
	g.townPositionSnapshot.AreaID = 4
	for i := 0; i < 2; i++ {
		g.conn.(*bufferConn).write.Reset()
		handled, err := currentGameplayModules.DispatchLegacy(s, g, 1267, nil)
		if !handled || err != nil {
			t.Fatal("领取请求未分发", err)
		}
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, g.conn.(*bufferConn).write.Bytes())
		if packet.Header.MsgID != 14 || packet.Header.Classification != 0 || len(rest) != 0 {
			t.Fatal("未发送标准背包物品通知")
		}
		inv, found, err := r.Inventory.Load(ctx, "29")
		stack := inv.Slots["0:121"]
		if err != nil || !found || stack.ItemID != 10158452 || stack.Count != 1 || len(stack.RawEntry) != currentItemListEntryWireSize {
			t.Fatalf("秘籍入库错误: %+v %v", inv, err)
		}
		if binary.LittleEndian.Uint32(stack.RawEntry[6:10]) != 1 {
			t.Fatal("通知数量不是持久化数量")
		}
	}
	after, _, _ := r.Skill.Load(ctx, "29")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("发秘籍提前修改技能或TP")
	}
}

func TestRealPVFTrainingSecretBookMetadataAndMaterialPlacement(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	quests, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	d, found := quests.Find(2674)
	if !found || len(d.SubReward) != 1 || d.SubReward[0] != 10158452 || !reflect.DeepEqual(d.IntData, []int64{1, 1, 1, 1}) {
		t.Fatalf("最后一步的真实PVF条件/奖励不符: %+v", d)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	inv := repo.InventoryRecord{CharacterID: "test"}
	items, err := currentQuestFinishItemAllocator(catalog)(&inv, dnfquest.FinishItemGrantRequest{QuestID: 2674, CompletionKey: "isolated-test", Source: d.Path, ItemID: d.SubReward[0], Count: 1})
	if err != nil || len(items) != 1 || items[0].SlotIndex < 121 || items[0].SlotIndex > 176 || items[0].PostCount != 1 {
		t.Fatalf("秘籍未按真实材料栏配置入库: %+v %v", items, err)
	}
	t.Logf("真实末步任务=%s，秘籍=%d，材料栏槽位=%d；只操作隔离内存", d.Path, d.SubReward[0], items[0].SlotIndex)
	// 使用同一真实任务/物品/等级表，覆盖最后条件、最终领奖及重放，绝不写线上DB。
	ctx := context.Background()
	r := memory.NewMemoryGroup()
	points, err := parseInitialSkillPointsFromSource(archive, 90)
	if err != nil {
		t.Fatal(err)
	}
	character := repo.CharacterRecord{CharacterID: "1", AccountID: "test", Job: "14", Level: 90, Stats: map[string]int64{"exp": 0, "grow_type": 36}}
	if err := r.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := r.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1", Points: repo.SkillPointState{TotalSP: points.TotalSP, RemainingSP: points.TotalSP, TotalTP: points.TotalTP, RemainingTP: points.TotalTP, SyncedLevel: 90}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Quest.Save(ctx, repo.QuestRecord{CharacterID: "1", States: map[int64]repo.QuestState{2674: {Status: "active", ProgressValue: 1 << 18, Extra: map[string]string{"pvf_path": d.Path}}}}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyPVPTrainingReward(ctx, quests, "test", "1", time.Now(), currentQuestFinishItemAllocator(catalog)); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplySetTrigger(ctx, dnfquest.Command{Operation: "set_quest_trigger", SelectedCharacterID: 1, QuestID: 2674, TriggerType: 64}); err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	input := dnfquest.FinishCommitInput{AccountID: "test", CharacterID: "1", QuestID: 2674, Multiplier: 1, CommittedAt: time.Now().UTC(), Progression: tables}
	input.LoadHonorTables = func(context.Context) (*dnfhonor.Tables, error) { return dnfhonor.LoadTables(archive) }
	for i := 0; i < 2; i++ {
		got, err := owner.ApplyFinishSettlement(ctx, quests, input)
		if err != nil || got.TPDelta != 2 || got.Replayed != (i == 1) || got.PostCommitCharacter.Stats["bonus_tp"] != 2 || got.PostCommitSkill.Points.TotalTP != points.TotalTP+2 || got.PostCommitQuest.States[2674].Status != "completed" {
			t.Fatalf("真实末步任务结算失败: %+v %v", got, err)
		}
		body, err := buildCurrentFinishQuestSuccessBody(got)
		if err != nil || len(body) != 10 {
			t.Fatal("末步提交回包形状错误", err)
		}
	}
}

func TestPVPTrainingTPOnlyFinishPublishesSkillPointsWithoutLevelOrSPGain(t *testing.T) {
	r := finishBridgeSeed(t)
	ctx := context.Background()
	character, _, _ := r.Character.Load(ctx, "19")
	skill, _, _ := r.Skill.Load(ctx, "19")
	skill.Points.TotalTP, skill.Points.RemainingTP = 9, 5
	quests := repo.QuestRecord{CharacterID: "19", States: map[int64]repo.QuestState{2674: {Status: "completed"}}}
	if err := r.Quest.Save(ctx, quests); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, questCatalog: trainingRewardBridgeCatalog(t), repositoryProvider: func() (repo.Group, bool) { return r, true }}
	g := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	result := dnfquest.FinishCommitResult{AtomicCommitted: true, CharacterID: "19", QuestID: 2674, PreviousLevel: character.Level, NewLevel: character.Level, TPDelta: 2, PostCommitCharacter: character, PostCommitSkill: skill, PostCommitQuest: quests}
	if err := s.sendCurrentFinishQuestPostCommitSnapshots(g, result); err != nil {
		t.Fatal(err)
	}
	found := false
	want, _, err := buildCurrentSceneSkillInfoBody(skill, skill.Layouts[repo.ActiveSkillTree(skill)], nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, packet := range splitAllFinishBridgePackets(t, g.conn.(*bufferConn).write.Bytes()) {
		if packet.Header.MsgID == 19 {
			if !reflect.DeepEqual(packet.Body, want) {
				t.Fatal("TP刷新不是已提交技能点")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("只有TP变化时遗漏在线技能刷新")
	}
}
