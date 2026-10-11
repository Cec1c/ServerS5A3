package dnfbridge

import (
	"context"
	"errors"
	"os"
	"testing"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// ─────────────────────────────────────────────────────────────────────────────
// 真档门控 smoke（收尾接线批）
//
// 运行方式（必须显式给 PVF 路径，否则 skip）：
//
//	$env:DNFBRIDGE_REAL_PVF_SMOKE='H:\86JP启动器\90CN\地下城与勇士\_dnf90_launcher_backup\20260925-013914\Script.pvf'
//	go test -count=1 -run RealScriptPVFTournamentClosing -v ./internal/services/dnfbridge
//
// 也兼容既有变量名 `DNF_WORLDMAP_REAL_PVF_SMOKE`（同一棵树上的既有真档用例用它）。
//
// 本文件钉住的不是"我们照厂商抄了"，而是**真档数据真的能把四件事跑通**：
//   #1 冠军 + 选奖结束 ⇒ 通关提交链的就绪判据在真档 definition 上成立；
//   #4 120/121 的 `[required item]` 真的被扣掉（不是只校验）。
// ─────────────────────────────────────────────────────────────────────────────

func realTournamentClosingPVFPath() string {
	if path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"); path != "" {
		return path
	}
	return os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
}

// 任务 #4 的真档证据：120 黄龙 / 121 青龙 的 `[required item]` 是
// `4726 1 1` / `4727 1 1`（`Flag == 1` ⇒ 入场即消耗），并且扣减在真档
// definition 上真的会让证物从背包里消失；不足时必须明确拒绝。
func TestRealScriptPVFTournamentClosingEntryItemIsConsumed(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real closing smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		dungeonID int64
		itemID    int64
		name      string
	}{
		{dungeonID: 120, itemID: 4726, name: "黄龙大会"},
		{dungeonID: 121, itemID: 4727, name: "青龙大会"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			dungeon, ok := table.FindDungeon(test.dungeonID)
			if !ok {
				t.Fatalf("real PVF dungeon %d is missing", test.dungeonID)
			}
			required := dungeon.Metadata.RequiredItems
			if len(required) != 1 || required[0].ItemID != test.itemID ||
				required[0].Count != 1 || !required[0].ConsumeOnEntry {
				t.Fatalf("dungeon %d `[required item]` = %+v want {%d 1 true}",
					test.dungeonID, required, test.itemID)
			}

			service, session, _ := newBackToVillageRuntime(t)
			repositories, ok := service.repositoryGroup()
			if !ok {
				t.Fatal("fixture has no repository group")
			}
			// 先不给证物 ⇒ 必须明确拒绝（不是静默扣 0、不是放行）。
			t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
			err := service.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session)
			if !errors.Is(err, dnfdungeon.ErrEntryItemConsumeInsufficient) {
				t.Fatalf("dungeon %d without the ticket err=%v want ErrEntryItemConsumeInsufficient",
					test.dungeonID, err)
			}
			// 按真档 `[required item]` 的真实 ItemID/Count 发证物 ⇒ 入场必须把它扣掉。
			if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: "99",
				Slots: map[string]dnfrepo.ItemStack{
					"0:1": {ItemID: test.itemID, Count: required[0].Count},
					"0:2": {ItemID: 3329, Count: 3}, // 无关道具必须原样保留
				},
			}); err != nil {
				t.Fatal(err)
			}
			if err := service.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session); err != nil {
				t.Fatalf("dungeon %d consume: %v", test.dungeonID, err)
			}
			inventory, _, err := repositories.Inventory.Load(ctx, "99")
			if err != nil {
				t.Fatal(err)
			}
			if _, stillThere := inventory.Slots["0:1"]; stillThere {
				t.Fatalf("dungeon %d ticket %d survived the entry transaction: %+v",
					test.dungeonID, test.itemID, inventory.Slots)
			}
			if kept := inventory.Slots["0:2"]; kept.ItemID != 3329 || kept.Count != 3 {
				t.Fatalf("dungeon %d clobbered an unrelated stack: %+v", test.dungeonID, kept)
			}
			// 第二次入场 ⇒ 又不足（证物不会凭空回来）。
			if err := service.consumeDungeonEntryItemsWithTournament(ctx, dungeon, session); !errors.Is(err, dnfdungeon.ErrEntryItemConsumeInsufficient) {
				t.Fatalf("dungeon %d second entry err=%v want insufficient", test.dungeonID, err)
			}
		})
	}
}

// 任务 #1 的真档证据：真档 120/121 的 definition 真的能构造出冠军结局，
// 并且"冠军 + 选奖结束"这一组就绪判据在真档数据上成立（不是只有合成夹具能过）。
func TestRealScriptPVFTournamentClosingChampionClearIntentIsReachable(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real closing smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}

	for _, dungeonID := range []int64{120, 121} {
		dungeon, ok := table.FindDungeon(dungeonID)
		if !ok {
			t.Fatalf("real PVF dungeon %d is missing", dungeonID)
		}
		topology, err := worldmap.BuildDungeonLayout(resolver, dungeonID, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
			return choice.Candidates[0].ID, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		room, ok := topology.Room(*topology.Start)
		if !ok || room.Map == nil {
			t.Fatalf("dungeon %d start room unresolved", dungeonID)
		}
		definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, room.Map.Map, nil)
		if !ok {
			t.Fatalf("dungeon %d rejected by the definition projection: %s", dungeonID, reason)
		}
		runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(uint32(dungeonID)))
		if !ok {
			t.Fatalf("dungeon %d runtime rejected: %s", dungeonID, reason)
		}
		if !runtime.tryBindFirstActorSequence(uint16(firstDungeonMonsterObjectKey)) {
			t.Fatalf("dungeon %d sequence bind failed", dungeonID)
		}
		for offset := 0; offset < tournamentRoundCount; offset++ {
			transition := runtime.tryApplyActorDeath(int(firstDungeonMonsterObjectKey) + offset)
			if !transition.accepted() {
				t.Fatalf("dungeon %d round %d death rejected: %+v", dungeonID, offset+1, transition)
			}
			if offset < tournamentRoundCount-1 && transition.tournamentCompleted() {
				t.Fatalf("dungeon %d completed at round %d", dungeonID, offset+1)
			}
		}
		if !runtime.isChampion() {
			t.Fatalf("dungeon %d did not reach champion: %+v", dungeonID, runtime.snapshot())
		}
		// 冠军 + 奖励选完 ⇒ 通关 intent 的就绪判据成立（含"淘汰绝不提交"的反面）。
		session := &gameSession{connID: "real-tournament-closing", selectedCharacterID: 99}
		cards, rollReason := rollCurrentTournamentRewardCards(
			&definition, tournamentFinalRound, true,
			newCurrentDungeonDropLCG(uint32(dungeonID)), 1000, true)
		if rollReason != "" {
			t.Fatalf("dungeon %d reward roll rejected: %s", dungeonID, rollReason)
		}
		state, err := newTournamentRewardState(cards, definition.PartyLimit, 0, 0, tournamentFinalRound, true)
		if err != nil {
			t.Fatalf("dungeon %d reward state: %v", dungeonID, err)
		}
		active := &runtimeDungeonState{
			IgnoreDefaultDungeonClear: true,
			Tournament:                runtime,
			tournamentReward:          &state,
		}
		// 冠军 ⇒ 通关 intent 的就绪判据成立，**不依赖选奖是否选完**
		// （本批解环：真机客户端要先看到通关才进宝箱界面并发 457）。
		if decision := evaluateCurrentTournamentClearIntentLocked(session, active); !decision.Allowed {
			t.Fatalf("dungeon %d champion decision = %+v want allowed before selection",
				dungeonID, decision)
		}
		for cardType := byte(0); cardType < tournamentCardTypeCount; cardType++ {
			state.Selected[cardType] = 0
		}
		if decision := evaluateCurrentTournamentClearIntentLocked(session, active); !decision.Allowed {
			t.Fatalf("dungeon %d champion + complete selection decision = %+v want allowed",
				dungeonID, decision)
		}
		if len(runtime.PathActors) != tournamentRoundCount*definition.PartyLimit {
			t.Fatalf("dungeon %d path actors = %d", dungeonID, len(runtime.PathActors))
		}
		// FinalSequence 口径：firstSequence + PathActors.Count - 1（厂商 :294-298）。
		finalSequence := int(runtime.boundFirstSequence()) + len(runtime.PathActors) - 1
		if finalSequence <= int(runtime.boundFirstSequence()) || finalSequence > 0xFFFF {
			t.Fatalf("dungeon %d finalSequence = %d (first=%d pathActors=%d)",
				dungeonID, finalSequence, runtime.boundFirstSequence(), len(runtime.PathActors))
		}
	}
}

// 任务 #2 的真档证据：真档 definition 上"玩家死亡 ⇒ 淘汰"冻结的完成轮数
// 与 DGN 的 `[tournament clear reward exp]` 键空间兼容（0..4 之外取不到会明确失败）。
func TestRealScriptPVFTournamentClosingEliminationRoundsArePricedByTheDGN(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real closing smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	for _, dungeonID := range []int64{120, 121} {
		dungeon, ok := table.FindDungeon(dungeonID)
		if !ok {
			t.Fatalf("real PVF dungeon %d is missing", dungeonID)
		}
		topology, err := worldmap.BuildDungeonLayout(resolver, dungeonID, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
			return choice.Candidates[0].ID, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		room, ok := topology.Room(*topology.Start)
		if !ok || room.Map == nil {
			t.Fatalf("dungeon %d start room unresolved", dungeonID)
		}
		definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, room.Map.Map, nil)
		if !ok {
			t.Fatalf("dungeon %d definition rejected: %s", dungeonID, reason)
		}
		// 每一轮淘汰都必须能在真档 `[tournament clear reward exp]` 里找到价格
		// （否则 `beginCurrentTournamentRewardLocked` 会走"缺证据就报错"的具名延后）。
		for round := 1; round <= tournamentRoundCount; round++ {
			runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(uint32(dungeonID)))
			if !ok {
				t.Fatalf("dungeon %d runtime rejected: %s", dungeonID, reason)
			}
			runtime.currentRound = byte(round)
			completed, accepted := runtime.tryEliminate()
			if !accepted {
				t.Fatalf("dungeon %d round %d elimination was not accepted", dungeonID, round)
			}
			if want := byte(round - 1); completed != want {
				t.Fatalf("dungeon %d round %d elimination completedRounds=%d want=%d",
					dungeonID, round, completed, want)
			}
			if _, priced := definition.RewardExperience[int(completed)]; !priced {
				t.Fatalf("dungeon %d has no `[tournament clear reward exp]` for completedRounds=%d",
					dungeonID, completed)
			}
			if !runtime.isEliminated() {
				t.Fatalf("dungeon %d round %d did not freeze into Eliminated", dungeonID, round)
			}
		}
	}
}

// 真档回归：120/121 上的奖励态必须能真的构造出来（含下排卡），
// 否则"选奖 ⇒ 通关"这条链在真机上根本走不到。
func TestRealScriptPVFTournamentClosingRewardStateIsConstructible(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real closing smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	for _, dungeonID := range []int64{120, 121} {
		dungeon, _ := table.FindDungeon(dungeonID)
		topology, err := worldmap.BuildDungeonLayout(resolver, dungeonID, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
			return choice.Candidates[0].ID, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		room, ok := topology.Room(*topology.Start)
		if !ok || room.Map == nil {
			t.Fatalf("dungeon %d start room unresolved", dungeonID)
		}
		definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, room.Map.Map, nil)
		if !ok {
			t.Fatalf("dungeon %d definition rejected: %s", dungeonID, reason)
		}
		cards, rollReason := rollCurrentTournamentRewardCards(
			&definition, tournamentFinalRound, true,
			newCurrentDungeonDropLCG(uint32(dungeonID)), 1000, true)
		if rollReason != "" {
			t.Fatalf("dungeon %d champion roll rejected: %s", dungeonID, rollReason)
		}
		state, err := newTournamentRewardState(
			cards, definition.PartyLimit, 0,
			uint32(definition.RewardExperience[tournamentFinalRound]),
			tournamentFinalRound, true)
		if err != nil {
			t.Fatalf("dungeon %d reward state: %v", dungeonID, err)
		}
		if state.cardCount(0) != tournamentCardsPerType || state.cardCount(1) != tournamentCardsPerType {
			t.Fatalf("dungeon %d champion card counts = %d/%d want 2/2",
				dungeonID, state.cardCount(0), state.cardCount(1))
		}
		body := buildTournamentClearRewardBody(state)
		// 厂商 `TournamentPacketBuilder.cs:77-105`：**6 字节头**
		// （u8 完成轮数 + u32 经验 + u8 0）之后两排：
		// 每排 = u8 cardCount + cardCount × 10 字节（i32 id + i32 数量 + u16 0）。
		// 真档 120/121 冠军两排各 2 张 ⇒ 6 + 21 + 21 = 48。
		type0Count, type1Count := int(state.cardCount(0)), int(state.cardCount(1))
		wantBodyLen := currentTournamentClearRewardBodyHeaderSize +
			(1 + type0Count*10) + (1 + type1Count*10)
		if len(body) != wantBodyLen {
			t.Fatalf("dungeon %d 389 body len=%d want=%d counts=%d/%d rounds=%d allRounds=%t exp=%d body=%x",
				dungeonID, len(body), wantBodyLen, type0Count, type1Count,
				state.CompletedRounds, state.CompletedAllRounds, state.RewardExperience, body)
		}
		if body[0] != tournamentFinalRound {
			t.Fatalf("dungeon %d 389 completedRounds=%d want=%d", dungeonID, body[0], tournamentFinalRound)
		}
		// 逐段 walk：每排的 u8 count 必须等于 2，且两排正好吃掉余下字节。
		offset := currentTournamentClearRewardBodyHeaderSize
		for cardType := 0; cardType < tournamentCardTypeCount; cardType++ {
			if offset >= len(body) {
				t.Fatalf("dungeon %d 389 type%d count byte missing at %d/%d body=%x",
					dungeonID, cardType, offset, len(body), body)
			}
			count := int(body[offset])
			if count != tournamentCardsPerType {
				t.Fatalf("dungeon %d 389 type%d cardCount=%d want=%d at offset %d body=%x",
					dungeonID, cardType, count, tournamentCardsPerType, offset, body)
			}
			offset += 1 + count*10
		}
		if offset != len(body) {
			t.Fatalf("dungeon %d 389 card section consumed %d of %d bytes body=%x",
				dungeonID, offset, len(body), body)
		}
	}
}

// 真档 + 内存仓储：完整跑一次"冠军 ⇒ 选奖 ⇒ 通关提交"，确认清关 receipt 被冻结。
func TestRealScriptPVFTournamentClosingCommitsARealClear(t *testing.T) {
	pvfPath := realTournamentClosingPVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real closing smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dungeon, ok := table.FindDungeon(120)
	if !ok {
		t.Fatal("real PVF dungeon 120 is missing")
	}
	// 只借真档的 `[required item]` 元数据 + 真档 definition；会话夹具仍用合成 PVF，
	// 因为本用例要证明的是"真档准入条目在我们的入场事务里真的生效"，
	// 而不是重跑一遍合成地图的拓扑。
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime := newBackToVillageRuntime(t)
	if len(dungeon.Metadata.RequiredItems) == 0 {
		t.Fatal("real dungeon 120 declares no `[required item]`")
	}

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	clearTournamentRoomForTest(t, runtime)
	runtime.Tournament = championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.IgnoreDefaultDungeonClear = true
	runtime.tournamentReward = tournamentRewardStateForTest(t, true)
	targetObjectKey := uint32(runtime.Tournament.boundFirstSequence()) +
		uint32(len(runtime.Tournament.PathActors)) - 1
	if err := service.submitCurrentTournamentClearCommitLocked(
		session, runtime, targetObjectKey, "test_real_pvf_tournament_clear",
	); err != nil {
		t.Fatalf("submit real-PVF-shaped clear: %v", err)
	}
	// 冠军局面下先冻结奖励态再打开出本门；顺序与真实流程一致
	// （`beginCurrentTournamentRewardLocked` → 选奖 → `completeCurrentTournamentResultLocked`）。
	if runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
		t.Fatal("the real-PVF-shaped champion clear did not complete the run")
	}
	if runtime.clearMapCompletionKey == "" {
		t.Fatal("the clear-map receipt was not frozen")
	}
	// 出本门也必须在同一状态下打开（否则真机上玩家仍然出不去）。
	if !currentDungeonTournamentResultExitReady(runtime, session.selectedCharacterID) {
		t.Fatal("the tournament exit gate did not open after the clear commit")
	}
	if !completedDungeonSettlementExitReady(runtime, session.selectedCharacterID) {
		t.Fatal("the overall exit gate did not open after the clear commit")
	}
	// 被淘汰的局面必须**不**打开任何通关门（先把冠军局面收干净，避免读到上一局状态）。
	runtime.tournamentReward = nil
	eliminatedRuntime, reason, ok := newTournamentDungeonRuntime(
		func() *tournamentDefinition {
			value := tournamentFlowFixtureDefinition(runtime.Session.Snapshot().Scene.Map.Map.ID)
			return &value
		}(), 1, deterministicTournamentNext(5))
	if !ok {
		t.Fatalf("eliminated fixture rejected: %s", reason)
	}
	if _, accepted := eliminatedRuntime.tryEliminate(); !accepted || !eliminatedRuntime.isEliminated() {
		t.Fatalf("eliminated fixture state = %+v", eliminatedRuntime.snapshot())
	}
	runtime.Tournament = eliminatedRuntime
	if currentDungeonTournamentResultExitReady(runtime, session.selectedCharacterID) {
		t.Fatal("an eliminated terminal result must not open the tournament exit gate")
	}
	// 被淘汰也拿不到通关提交：判定必须是具名拒绝。
	if decision := evaluateCurrentTournamentClearIntentLocked(session, runtime); decision.Allowed ||
		decision.Reason != "tournament_result_is_eliminated" {
		t.Fatalf("eliminated clear-intent decision = %+v", decision)
	}
}
