package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestMission4874LegacyRealPVFSourceReportResolved(t *testing.T) {
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "legacy")
	source, table, resolver := loadRealStoryPVF(t)
	ctx := context.Background()
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	definition, found := catalog.Find(mission4874QuestID)
	if !found || definition.Type != "[seeking]" ||
		!reflect.DeepEqual(definition.IntData, []int64{int64(mission4874ItemID), 10}) {
		t.Fatalf("当前PVF不是已核对的毒草任务：定义=%+v 存在=%t", definition, found)
	}
	for _, difficulty := range []int64{1, 2} {
		matches := currentQuestDropMatchEnemy(catalog, []int64{mission4874QuestID}, mission4874DungeonID,
			difficulty, currentDungeonQuestEnemyTypePassiveObject, mission4874ObjectID)
		if len(matches) != 1 || matches[0].ItemID != mission4874ItemID || matches[0].Count != 1 ||
			matches[0].DropRate != 20 || matches[0].MaxStack != 10 {
			t.Fatalf("真实毒草奖励规则变化：难度=%d 规则=%+v", difficulty, matches)
		}
	}
	progress, err := progression.Load(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := newPVFDungeonMonsterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	repositories := qst006Repositories(t, ctx)
	states := map[int64]dnfrepo.QuestState{}
	if len(definition.PreRequiredGroups) == 0 {
		t.Fatal("真实毒草任务缺少预期前置")
	}
	for _, questID := range definition.PreRequiredGroups[0] {
		states[questID] = dnfrepo.QuestState{Status: "completed"}
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: qst006Character, States: states}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := owner.ApplyAccept(ctx, catalog, dnfquest.CharacterEligibility{Level: 65, Job: 2},
		dnfquest.NewQuestIDCommand(alignedcmd.Request{AccountID: qst006AccountID, SelectedCharacterID: 19},
			"accept_quest", dnfquest.QuestIDRequest{QuestID: uint16(mission4874QuestID)}))
	if err != nil || accepted.InitTrigger != 1 {
		t.Fatalf("正常接取毒草任务失败：结果=%+v 错误=%v", accepted, err)
	}
	conn, service, session := qst006Bridge(repositories, catalog, progress, items)
	service.worldMapTable, service.worldMapResolver = table, resolver
	service.dungeonMonsterTable, service.dungeonAICharacterTable = monsters, ai
	service.pvfItemCatalog = items
	service.dungeonChoice = func(int) (int, error) { return 0, nil }
	service.dungeonSeed = func() (uint32, error) { return 1, nil }
	runtime, _, err := service.prepareDungeonRuntime(ctx, session,
		dungeoncmd.SelectDungeonRequest{DungeonID: uint32(mission4874DungeonID), Difficulty: 2})
	if err != nil {
		t.Fatal(err)
	}
	session.dungeon.runtime = runtime
	scene, ok := runtime.Session.Scene()
	if !ok || runtime.MazeIndex != 11 || scene.Map.Map.ID != 400233 {
		t.Fatalf("任务迷宫/起点不符：maze=%d scene=%+v", runtime.MazeIndex, scene)
	}
	layout, err := worldmap.BuildDungeonLayout(resolver, mission4874DungeonID, runtime.MazeIndex,
		func(choice worldmap.DungeonMapChoice) (int64, error) { return choice.Candidates[0].ID, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := scene.Coordinate
	found = false
	for _, room := range runtime.Session.Rooms() {
		if room.Map.Map.ID == 58916 {
			target, found = room.Coordinate, true
			break
		}
	}
	if !found {
		t.Fatal("任务迷宫没有现场毒草房58916")
	}
	// 中间清房仅作移动前提；任务接取、选图、过门、legacy分派和结算使用生产入口。
	for _, next := range realDungeonPathToRoom(t, layout, scene.Coordinate, target)[1:] {
		clearCurrentRealPVFSmokeRoom(t, runtime)
		move := make([]byte, dungeoncmd.MoveMapRequestSize)
		move[0], move[1] = byte(next.X), byte(next.Y)
		if err := service.handleDungeonMoveMap(session, move); err != nil {
			t.Fatal(err)
		}
	}
	scene, ok = runtime.Session.Scene()
	if !ok || scene.Map.Map.ID != 58916 || currentDungeonPassiveObjectSourceCount(scene, mission4874ObjectID) != 12 {
		t.Fatalf("现场房间未保留12个静态毒草来源：scene=%+v", scene)
	}
	beforeRoom := runtime.Room.Snapshot()
	conn.write.Reset()
	raw := mission4874LiveLegacyFrame(t)
	if err := service.handleLegacyGamePacket(session, raw); err != nil {
		t.Fatal(err)
	}
	sequence, initialized := session.guild.purchaseSequence.currentHigh()
	if !initialized || uint16(sequence) != 411 {
		t.Fatalf("真实入口未观测现场序号：%d", sequence)
	}
	receipt := mission4874AssertResolvedReceipt(t, session, runtime, sequence)
	if mission4874PassiveUsage(t, runtime) != 1 || len(runtime.passiveQuestDropReceipts) != 1 {
		t.Fatal("真实报告未登记单个来源事件")
	}
	inventory, found, err := repositories.Inventory.Load(ctx, qst006Character)
	if err != nil || !found {
		t.Fatalf("读取毒草库存失败：存在=%t 错误=%v", found, err)
	}
	held := currentQuestDropHeldCount(inventory, int64(mission4874ItemID))
	if held < 0 || held > 1 || receipt.Granted != (held == 1) || receipt.Granted != receipt.Refreshed {
		t.Fatalf("20%%真实概率与库存/刷新不符：回执=%+v 持有=%d", receipt, held)
	}
	if got := seekerPassiveDropRefreshCount(t, conn.write.Bytes()); got != int(held) {
		t.Fatalf("库存刷新=%d，期望=%d", got, held)
	}
	conn.write.Reset()
	if err := service.handleLegacyGamePacket(session, raw); err != nil {
		t.Fatal(err)
	}
	after, found, err := repositories.Inventory.Load(ctx, qst006Character)
	if err != nil || !found || currentQuestDropHeldCount(after, int64(mission4874ItemID)) != held ||
		conn.write.Len() != 0 || mission4874PassiveUsage(t, runtime) != 1 ||
		!reflect.DeepEqual(receipt, mission4874AssertResolvedReceipt(t, session, runtime, sequence)) ||
		!reflect.DeepEqual(beforeRoom, runtime.Room.Snapshot()) {
		t.Fatal("真实20%报告重放改变奖励、来源或房间状态")
	}
	t.Logf("真实任务4874正常接取、maze11/58916及legacy首报已结算；概率保持20%%，本次发放=%t，未代替游戏实测", receipt.Granted)
}
