package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ─────────────────────────────────────────────────────────────────────────────
// 赛制推进接线（本批新增）
//
// 厂商对拍：CMD DIE_MONSTER（0x0027=39）→ DungeonCombatHandler.cs:66
// → DungeonKillApplicationService.cs:444 → TournamentDungeonCoordinator.cs:126
// → TournamentDungeonRuntime.cs:257-324。
// 见 dungeon_tournament_flow.go 顶部与交付报告第 1 节。
// ─────────────────────────────────────────────────────────────────────────────

// tournamentFlowFixtureDefinition 造一份可以用合成 monster catalog 铺出来的定义
// （候选 code = 3001，该 code 在 bridgeDungeonPVF 里有定义）。
func tournamentFlowFixtureDefinition(mapID int64) tournamentDefinition {
	definition := tournamentDefinition{
		DungeonID:           120,
		MapID:               mapID,
		BasisLevel:          70,
		PartyLimit:          1,
		ClearRewardGoldRate: 16,
		RewardExperience: map[int]int64{
			0: 0, 1: 30000, 2: 50000, 3: 100000, 4: 200000,
		},
		ResultCards: map[int]tournamentResultCardDefinition{
			0: {ResultKey: 0, EmptyWeight: 100},
			1: {ResultKey: 1, GoldWeight: 70, EmptyWeight: 30},
			2: {ResultKey: 2, GoldWeight: 80, EmptyWeight: 20},
			3: {ResultKey: 3, GoldWeight: 90, EmptyWeight: 10},
			4: {ResultKey: 4, GoldWeight: 100},
			5: {ResultKey: 5, ItemWeight: 100},
		},
		RewardItemRates: []worldmap.TournamentRewardItemRate{
			{ItemID: 3329, Weight: 60, Count: 1},
			{ItemID: 3329, Weight: 35, Count: 2},
			{ItemID: 3329, Weight: 5, Count: 3},
		},
	}
	for index := 0; index < tournamentNpcTeamCount; index++ {
		definition.Candidates = append(definition.Candidates, tournamentActorDefinition{
			PartyCount: 1,
			Code:       3001,
			Strength:   100 + int64(index),
			Level:      70,
		})
	}
	return definition
}

// 状态机的 sequence 空间必须与本仓房间公告的 objectKey 空间完全一致 ——
// 这是"op39 上报的 RuntimeObjectKey 能直接喂给 tryApplyActorDeath"的前提。
func TestTournamentActorSequenceBindingMatchesRoomObjectKeys(t *testing.T) {
	_, _, catalog := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	definition := tournamentFlowFixtureDefinition(102)
	runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(17))
	if !ok {
		t.Fatalf("fixture runtime rejected: %s", reason)
	}
	snapshot := runtime.snapshot()
	spawns := make([]worldmap.MonsterSpawn, 0, len(snapshot.PathActors))
	for _, actor := range snapshot.PathActors {
		spawns = append(spawns, worldmap.MonsterSpawn{
			MonsterID: actor.Code,
			Level:     int64(actor.Level),
		})
	}
	room, _, err := newRuntimeDungeonRoom(worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: 1, Y: 1},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: definition.MapID}},
		Monsters:   spawns,
	}, catalog, firstDungeonMonsterObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	roomSnapshot := room.Snapshot()
	if len(roomSnapshot.Monsters) != tournamentRoundCount*definition.PartyLimit {
		t.Fatalf("room monsters=%d want=%d",
			len(roomSnapshot.Monsters), tournamentRoundCount*definition.PartyLimit)
	}
	for index, monster := range roomSnapshot.Monsters {
		want := firstDungeonMonsterObjectKey + uint32(index)
		if monster.ObjectKey != want {
			t.Fatalf("monster %d object key=%d want=%d (contiguous from firstDungeonMonsterObjectKey)",
				index, monster.ObjectKey, want)
		}
	}
	if !runtime.tryBindFirstActorSequence(uint16(roomSnapshot.Monsters[0].ObjectKey)) {
		t.Fatal("binding the room's first actor object key must succeed")
	}
	if runtime.boundFirstSequence() != uint16(firstDungeonMonsterObjectKey) {
		t.Fatalf("bound first sequence=%d want=%d", runtime.boundFirstSequence(), firstDungeonMonsterObjectKey)
	}
	for index, monster := range roomSnapshot.Monsters {
		wantRound := index/definition.PartyLimit + 1
		if got := runtime.tournamentRoundForSequence(int(monster.ObjectKey)); got != wantRound {
			t.Fatalf("object key %d maps to round %d want %d", monster.ObjectKey, got, wantRound)
		}
	}
	// 按房间下标顺序喂死亡 ⇒ 4 轮依次推进，最后落在 Champion（与厂商 :307-315 同）。
	for index, monster := range roomSnapshot.Monsters {
		transition := runtime.tryApplyActorDeath(int(monster.ObjectKey))
		if !transition.accepted() {
			t.Fatalf("death %d (object key %d) rejected: %+v", index, monster.ObjectKey, transition)
		}
		if index < len(roomSnapshot.Monsters)-1 {
			if !transition.roundCompleted() || transition.tournamentCompleted() {
				t.Fatalf("death %d transition=%+v want round completed", index, transition)
			}
			continue
		}
		if !transition.tournamentCompleted() {
			t.Fatalf("final death transition=%+v want tournament completed", transition)
		}
	}
	if !runtime.isChampion() {
		t.Fatalf("runtime did not reach champion: %+v", runtime.snapshot())
	}
}

// 387 的路径 actor 段必须以**绑定的** firstSequence 起算（厂商
// `A21SpecialDungeonProtocolSelfTest.cs:89-93`：`info[224]==1 && u16@225==firstMonsterSequence`）。
func TestTournamentInfoBodyStartsPathActorsAtBoundSequence(t *testing.T) {
	definition := tournamentFlowFixtureDefinition(12000)
	runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(4242))
	if !ok {
		t.Fatalf("fixture runtime rejected: %s", reason)
	}
	if !runtime.tryBindFirstActorSequence(uint16(firstDungeonMonsterObjectKey)) {
		t.Fatal("bind failed")
	}
	body, err := buildTournamentInfoBody(runtime, 2, runtime.boundFirstSequence())
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 260 {
		t.Fatalf("TOURNAMENT_INFO body len=%d want=260 (vendor captured body)", len(body))
	}
	if body[224] != 1 || binary.LittleEndian.Uint16(body[225:227]) != uint16(firstDungeonMonsterObjectKey) {
		t.Fatalf("path actor head = %d/%d want 1/%d",
			body[224], binary.LittleEndian.Uint16(body[225:227]), firstDungeonMonsterObjectKey)
	}
}

// op39 路径的接线判据：默认档（无 Tournament）恒为 false ⇒ 不改控制流。
// vendor 档：非冠军时要压住"到终点即通关"，冠军落定后放行（否则会卡死）。
func TestShouldDeferTournamentDefaultClearContract(t *testing.T) {
	if shouldDeferTournamentDefaultClear(nil) {
		t.Fatal("nil runtime must not defer")
	}
	if shouldDeferTournamentDefaultClear(&runtimeDungeonState{}) {
		t.Fatal("default mode (IgnoreDefaultDungeonClear=false) must not defer")
	}
	championFixture := func(champion bool) *tournamentDungeonRuntime {
		runtime, _, ok := newTournamentDungeonRuntime(
			func() *tournamentDefinition {
				value := tournamentFlowFixtureDefinition(12000)
				return &value
			}(), 1, deterministicTournamentNext(3))
		if !ok {
			t.Fatal("fixture runtime rejected")
		}
		if !runtime.tryBindFirstActorSequence(uint16(firstDungeonMonsterObjectKey)) {
			t.Fatal("bind failed")
		}
		if champion {
			for offset := 0; offset < tournamentRoundCount; offset++ {
				runtime.tryApplyActorDeath(int(firstDungeonMonsterObjectKey) + offset)
			}
		}
		return runtime
	}
	pending := &runtimeDungeonState{IgnoreDefaultDungeonClear: true, Tournament: championFixture(false)}
	if !shouldDeferTournamentDefaultClear(pending) {
		t.Fatal("a tournament that has not decided a champion must defer the default clear")
	}
	decided := &runtimeDungeonState{IgnoreDefaultDungeonClear: true, Tournament: championFixture(true)}
	if shouldDeferTournamentDefaultClear(decided) {
		t.Fatal("a decided championship must not defer the default clear (deadlock guard)")
	}
}

// 投影必须覆盖全部 4 轮（厂商 TournamentDungeonApplicationService.cs:101
// `Monsters = CopyActors(runtime.PathActors)`），且第 4 轮的怪物 Boss 必须在
// `MonsterSpawn` 上表达成 `[boss]` 档（否则 op29/op117 会把它当普通怪）。
func TestTournamentRoomMonstersProjectAllRounds(t *testing.T) {
	definition := tournamentFlowFixtureDefinition(12000)
	definition.StartAreas = []worldmap.TournamentStartAreaInfo{
		{PartyCount: 1, X: 360, Y: 270, Direction: 1},
		{PartyCount: 1, X: 760, Y: 270, Direction: 2},
	}
	runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(9))
	if !ok {
		t.Fatalf("fixture runtime rejected: %s", reason)
	}
	roomActors, err := tournamentRoomActors(definition, runtime)
	if err != nil {
		t.Fatal(err)
	}
	monsters := roomActors.Monsters
	if len(roomActors.AICharacters) != 0 {
		t.Fatalf("ordinary tournament projection unexpectedly contains APCs: %+v", roomActors.AICharacters)
	}
	if len(monsters) != tournamentRoundCount*definition.PartyLimit {
		t.Fatalf("projected monsters=%d want=%d (all rounds, not just the current one)",
			len(monsters), tournamentRoundCount*definition.PartyLimit)
	}
	pathActors := runtime.snapshot().PathActors
	for index, monster := range monsters {
		if monster.MonsterID != pathActors[index].Code {
			t.Fatalf("monster %d code=%d want=%d", index, monster.MonsterID, pathActors[index].Code)
		}
		if monster.Position.X != 760 || monster.Position.Y != 270 {
			t.Fatalf("monster %d position=%+v want the opponent `[tournament start area]` row",
				index, monster.Position)
		}
		wantRank := ""
		if pathActors[index].Type == 3 {
			wantRank = "[boss]"
		}
		if monster.Rank != wantRank {
			t.Fatalf("monster %d rank=%q want=%q", index, monster.Rank, wantRank)
		}
	}
	// 第 4 轮必须真的是 boss 档（`[monster]` 分支升格成 3）。
	last := monsters[len(monsters)-1]
	if last.Rank != "[boss]" {
		t.Fatalf("final round rank=%q want [boss]", last.Rank)
	}
}

// `[apc]` 分支必须保留为真实 hostile AICharacter，尤其第 4 轮 actorType=8
// 不能被降级成普通怪，也不能丢失 blocking。
func TestTournamentRoomActorsProjectBlackDragonAPCBoss(t *testing.T) {
	definition := tournamentFlowFixtureDefinition(12000)
	definition.StartAreas = []worldmap.TournamentStartAreaInfo{
		{PartyCount: 1, X: 360, Y: 270, Direction: 1},
		{PartyCount: 1, X: 760, Y: 270, Direction: 2},
	}
	for index := range definition.Candidates {
		definition.Candidates[index].IsApc = true
		definition.Candidates[index].ActorType = 5
	}
	runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(9))
	if !ok {
		t.Fatalf("fixture runtime rejected: %s", reason)
	}
	roomActors, err := tournamentRoomActors(definition, runtime)
	if err != nil {
		t.Fatalf("project APC tournament: %v", err)
	}
	if len(roomActors.Monsters) != 0 || len(roomActors.AICharacters) != tournamentRoundCount*definition.PartyLimit {
		t.Fatalf("APC projection monsters=%d AI=%d want=(0,%d)",
			len(roomActors.Monsters), len(roomActors.AICharacters),
			tournamentRoundCount*definition.PartyLimit)
	}
	for index, actor := range roomActors.AICharacters {
		wantType := byte(5)
		if index == len(roomActors.AICharacters)-1 {
			wantType = 8
		}
		if actor.Faction != "[monster]" || !actor.Blocking ||
			actor.Code != runtime.snapshot().PathActors[index].Code ||
			actor.Level != int64(runtime.snapshot().PathActors[index].Level) {
			t.Fatalf("APC actor %d=%+v", index, actor)
		}
		wantAIType := "[normal]"
		if wantType == 8 {
			wantAIType = "[boss]"
		}
		if actor.AIType != wantAIType {
			t.Fatalf("APC actor %d AIType=%q want=%q", index, actor.AIType, wantAIType)
		}
	}
}

func TestTournamentRoomActorsPreserveEveryNativeAPCType(t *testing.T) {
	definition := tournamentDefinition{
		PartyLimit: 1,
		StartAreas: []worldmap.TournamentStartAreaInfo{
			{PartyCount: 1, X: 360, Y: 270, Direction: 1},
			{PartyCount: 1, X: 760, Y: 270, Direction: 2},
		},
	}
	runtime := &tournamentDungeonRuntime{
		Definition: &definition,
		PathActors: []tournamentPathActor{
			{Code: 101, Level: 71, Type: 5},
			{Code: 102, Level: 72, Type: 6},
			{Code: 103, Level: 73, Type: 7},
			{Code: 104, Level: 74, Type: 8},
		},
	}
	roomActors, err := tournamentRoomActors(definition, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(roomActors.Monsters) != 0 || len(roomActors.AICharacters) != 4 {
		t.Fatalf("projection monsters=%d AI=%d want=(0,4)",
			len(roomActors.Monsters), len(roomActors.AICharacters))
	}
	wantAITypes := []string{"[normal]", "[champion]", "[super champion]", "[boss]"}
	for index, actor := range roomActors.AICharacters {
		if actor.Code != int64(101+index) || actor.Level != int64(71+index) ||
			actor.Position != (worldmap.Point{X: 760, Y: 270}) ||
			actor.Direction != 2 || actor.Faction != "[monster]" ||
			actor.AIType != wantAITypes[index] || !actor.Blocking {
			t.Fatalf("native APC type %d projection=%+v", index+5, actor)
		}
	}
}

// 抽卡：冠军 ⇒ 上下两排各 2 张（第 2 类启用）；非冠军 ⇒ 下排两张是空卡。
func TestRollTournamentRewardCardsHonoursChampionRow(t *testing.T) {
	definition := tournamentFlowFixtureDefinition(12000)
	champion, reason := rollCurrentTournamentRewardCards(
		&definition, tournamentFinalRound, true,
		newCurrentDungeonDropLCG(0x1234), 1000, true)
	if reason != "" {
		t.Fatalf("champion roll rejected: %s", reason)
	}
	if len(champion) != tournamentCardTypeCount*tournamentCardsPerType {
		t.Fatalf("champion cards=%d want 4", len(champion))
	}
	state, err := newTournamentRewardState(champion, 1, 0, 200000, tournamentFinalRound, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.cardCount(0) != 2 || state.cardCount(1) != 2 {
		t.Fatalf("champion card counts=%d/%d want 2/2", state.cardCount(0), state.cardCount(1))
	}

	partial, reason := rollCurrentTournamentRewardCards(
		&definition, 2, false,
		newCurrentDungeonDropLCG(0x1234), 1000, true)
	if reason != "" {
		t.Fatalf("partial roll rejected: %s", reason)
	}
	if len(partial) != 4 {
		t.Fatalf("partial cards=%d want 4", len(partial))
	}
	for _, card := range partial[tournamentCardsPerType:] {
		if !currentTournamentRewardIsEmpty(card) {
			t.Fatalf("non champion lower row card=%+v want the vendor empty reward", card)
		}
	}
	// 上排若不吃金币权重，则整次抽卡不再需要金币基数（下排是空卡，不查表）。
	noGold := definition
	noGold.ResultCards[2] = tournamentResultCardDefinition{ResultKey: 2, ItemWeight: 100}
	withoutGold, reason := rollCurrentTournamentRewardCards(
		&noGold, 2, false,
		newCurrentDungeonDropLCG(0x1234), 0, false)
	if reason != "" {
		t.Fatalf("a non-gold result row must not need the gold amount: %s", reason)
	}
	if len(withoutGold) != 4 {
		t.Fatalf("non-gold roll cards=%d want 4", len(withoutGold))
	}
}

// 金币权重被抽到时若金币基数取不到（PVF owner 失败）⇒ 整次抽卡报错，
// **不写 0 金币**（缺证据就报错）。
func TestRollTournamentRewardCardsRefusesUnresolvedGold(t *testing.T) {
	definition := tournamentFlowFixtureDefinition(12000)
	definition.ResultCards[2] = tournamentResultCardDefinition{ResultKey: 2, GoldWeight: 100}
	if _, reason := rollCurrentTournamentRewardCards(
		&definition, 2, false,
		newCurrentDungeonDropLCG(7), 0, false); reason == "" {
		t.Fatal("an unresolved gold amount must fail the roll instead of writing a zero gold card")
	}
	// 权重表本身坏掉（权重全 0）也必须报错。
	definition.ResultCards[2] = tournamentResultCardDefinition{ResultKey: 2}
	if _, reason := rollCurrentTournamentRewardCards(
		&definition, 2, false,
		newCurrentDungeonDropLCG(7), 1000, true); reason == "" {
		t.Fatal("a zero-weight result card must fail the roll")
	}
}

// 奖励 bundle 只走既有背包 owner 的输入形状：金币卡给 Gold，道具卡给
// ItemID/Count/Stackable **+ 解析出来的 PVF 真实落点段**，空卡不产生任何 bundle。
//
// 旧版这里只断言 `ItemID/Count/Stackable`，于是"bundle 里 SlotStart=SlotEnd=0"
// 这条现场根因被用例放过去了（见 `dungeon_tournament_reward_delivery_test.go` 的说明）。
func TestCurrentTournamentRewardBundleShape(t *testing.T) {
	if !currentTournamentRewardIsEmpty(tournamentCardReward{ItemID: tournamentEmptyRewardItemID}) {
		t.Fatal("the vendor empty reward must be recognised")
	}
	gold, err := currentTournamentRewardBundle(tournamentCardReward{IsGold: true, GoldAmount: 4321}, nil)
	if err != nil || gold.Gold != 4321 || len(gold.Items) != 0 {
		t.Fatalf("gold bundle=%+v err=%v", gold, err)
	}
	// `[material]` → list 0 121..176（真源：dungeonDropStackableSlotRange）。
	material := dungeonDropItemDefinition{
		ItemID:        3329,
		Kind:          dungeonDropItemStackable,
		PVFPath:       "stackable/material/material_3329.stk",
		StackableType: "[material]",
		StackLimit:    999,
		SlotStart:     currentPVFMainMaterialStart,
		SlotEnd:       currentPVFMainMaterialEnd,
	}
	item, err := currentTournamentRewardBundle(tournamentCardReward{ItemID: 3329, StackCount: 3}, &material)
	if err != nil || item.Gold != 0 || len(item.Items) != 1 {
		t.Fatalf("item bundle=%+v err=%v", item, err)
	}
	if item.Items[0].ItemID != 3329 || item.Items[0].Count != 3 || !item.Items[0].Stackable ||
		item.Items[0].SlotStart != currentPVFMainMaterialStart || item.Items[0].SlotEnd != currentPVFMainMaterialEnd {
		t.Fatalf("item reward=%+v want the resolved PVF slot range %d..%d",
			item.Items[0], currentPVFMainMaterialStart, currentPVFMainMaterialEnd)
	}
	if item.Items[0].Extra["pvf_path"] != material.PVFPath || item.Items[0].Extra["item_kind"] != string(dungeonDropItemStackable) {
		t.Fatalf("item reward identity=%+v", item.Items[0].Extra)
	}
	// 装备按真实 Kind 判非堆叠（数量必须为 1，交给 owner 的具名校验）。
	equipment := dungeonDropItemDefinition{
		ItemID: 3329, Kind: dungeonDropItemEquipment, PVFPath: "equipment/weapon/test_sword.equ",
		EquipmentType: "[weapon]", SlotStart: currentPVFMainEquipmentSlotStart, SlotEnd: currentPVFMainEquipmentSlotEnd,
	}
	got, err := currentTournamentRewardBundle(tournamentCardReward{ItemID: 3329, StackCount: 1}, &equipment)
	if err != nil || len(got.Items) != 1 || got.Items[0].Stackable {
		t.Fatalf("equipment reward=%+v err=%v", got, err)
	}
	// **没有真实 PVF 定义 ⇒ 拒绝**（旧代码在这里发出一条 SlotStart=0 的行，
	// 正是现场"道具卡永远发不出去"的根因）。
	if _, err := currentTournamentRewardBundle(tournamentCardReward{ItemID: 3329, StackCount: 3}, nil); err == nil {
		t.Fatal("an item card without a resolved PVF definition must not produce a bundle")
	}
	if _, err := currentTournamentRewardBundle(tournamentCardReward{ItemID: -1}, nil); err == nil {
		t.Fatal("an empty reward must not be convertible into a bundle")
	}
}

// 上行 458/459 在"没有等待选择的锦标赛奖励态"时必须完全 inert（默认档与
// vendor 档未进锦标赛时都是这条路径）。
func TestTournamentRewardCommandIsInertWithoutPendingReward(t *testing.T) {
	session := &gameSession{connID: "tournament-reward-inert", selectedCharacterID: 99}
	for _, value := range []string{"", "off", "sausages"} {
		t.Setenv(dungeonTournamentEnvKey, value)
		for _, typ := range []uint16{a21CmdTournamentRewardSelectState, a21CmdTournamentRewardSelect} {
			handled, err := (&Service{}).handleCurrentTournamentRewardCommand(session, typ, []byte{0, 0})
			if handled || err != nil {
				t.Fatalf("switch=%q typ=%d handled=%t err=%v", value, typ, handled, err)
			}
		}
	}
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	for _, typ := range []uint16{a21CmdTournamentRewardSelectState, a21CmdTournamentRewardSelect} {
		handled, err := (&Service{}).handleCurrentTournamentRewardCommand(session, typ, nil)
		if handled || err != nil {
			t.Fatalf("vendor mode without a pending reward handled=%t err=%v typ=%d", handled, err, typ)
		}
	}
	// body 形状与厂商 parser 不一致时也必须放行给既有路径（不是我们的包）。
	session.dungeon.runtime = &runtimeDungeonState{}
	handled, err := (&Service{}).handleCurrentTournamentRewardCommand(session, 458, nil)
	if handled || err != nil {
		t.Fatalf("a runtime without a frozen tournament reward handled=%t err=%v", handled, err)
	}
}

// NOTI 387/388 的发送点：op28 之后、op29 之前（本仓接线点见
// dungeon_navigation_runtime.go 的 sendPartyDungeonEntryTailAfterOp28）。
// 这里直接驱动发送函数，钉住"顺序 + 逐字节体 + 默认档零字节"。
func TestTournamentStartMapProjectionSendsInfoThenMapInfo(t *testing.T) {
	definition := tournamentFlowFixtureDefinition(17100)
	rt, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(2024))
	if !ok {
		t.Fatalf("fixture runtime rejected: %s", reason)
	}
	if !rt.tryBindFirstActorSequence(uint16(firstDungeonMonsterObjectKey)) {
		t.Fatal("bind failed")
	}
	state := &runtimeDungeonState{
		Dungeon:    worldmap.Dungeon{ID: 120},
		Tournament: rt,
		Seed:       0xDD23D90E,
		Request:    dungeoncmd.SelectDungeonRequest{Difficulty: 2},
	}
	scene := worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: 0, Y: 0},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: definition.MapID}},
	}

	connection := &bufferConn{}
	session := &gameSession{conn: connection, selectedCharacterID: 99}
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}

	// 默认档：一个字节都不发。
	for _, value := range []string{"", "off", "on", "true", "sausages"} {
		t.Setenv(dungeonTournamentEnvKey, value)
		connection.write.Reset()
		service.sendCurrentTournamentStartMapProjection(session, state, scene, false)
		if connection.write.Len() != 0 {
			t.Fatalf("switch=%q emitted %d bytes", value, connection.write.Len())
		}
	}

	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	connection.write.Reset()
	service.sendCurrentTournamentStartMapProjection(session, state, scene, false)
	stream := connection.write.Bytes()
	info, rest := splitGameServerUpperPacket(t, stream)
	if info.Header.Classification != 0 || info.Header.MsgID != a21NotiTournamentInfo {
		t.Fatalf("first packet = class%d/op%d want class0/op%d",
			info.Header.Classification, info.Header.MsgID, a21NotiTournamentInfo)
	}
	if len(info.Body) != 260 {
		t.Fatalf("TOURNAMENT_INFO body len=%d want 260", len(info.Body))
	}
	if binary.LittleEndian.Uint32(info.Body[0:4]) != 120 ||
		info.Body[4] != 2 || info.Body[5] != 1 {
		t.Fatalf("TOURNAMENT_INFO head = %x", info.Body[:6])
	}
	if binary.LittleEndian.Uint16(info.Body[225:227]) != uint16(firstDungeonMonsterObjectKey) {
		t.Fatalf("TOURNAMENT_INFO first path sequence=%d want=%d",
			binary.LittleEndian.Uint16(info.Body[225:227]), firstDungeonMonsterObjectKey)
	}
	mapInfo, rest := splitGameServerUpperPacket(t, rest)
	if mapInfo.Header.Classification != 0 || mapInfo.Header.MsgID != a21NotiTournamentMapInfo {
		t.Fatalf("second packet = class%d/op%d want class0/op%d",
			mapInfo.Header.Classification, mapInfo.Header.MsgID, a21NotiTournamentMapInfo)
	}
	if len(mapInfo.Body) != 15 {
		t.Fatalf("TOURNAMENT_MAP_INFO body len=%d want 15", len(mapInfo.Body))
	}
	if binary.LittleEndian.Uint32(mapInfo.Body[2:6]) != 0xDD23D90E ||
		mapInfo.Body[6] != 0 || mapInfo.Body[7] != 1 ||
		binary.LittleEndian.Uint32(mapInfo.Body[8:12]) != 17100 {
		t.Fatalf("TOURNAMENT_MAP_INFO body=%x", mapInfo.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes after TOURNAMENT_MAP_INFO: %x", rest)
	}

}

// 非锦标赛地图（sentMapId 对不上 definition.MapId）必须一个字节都不发 ——
// 厂商 `DungeonMapHandler.cs:850-860` 的同一条判据。
func TestTournamentStartMapProjectionSkipsForeignMap(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	definition := tournamentFlowFixtureDefinition(17100)
	rt, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(5))
	if !ok {
		t.Fatalf("fixture runtime rejected: %s", reason)
	}
	if !rt.tryBindFirstActorSequence(uint16(firstDungeonMonsterObjectKey)) {
		t.Fatal("bind failed")
	}
	state := &runtimeDungeonState{Dungeon: worldmap.Dungeon{ID: 120}, Tournament: rt}
	scene := worldmap.DungeonRoomScene{
		Coordinate: worldmap.RoomCoordinate{X: 0, Y: 0},
		Map:        worldmap.ResolvedMap{Map: worldmap.Map{ID: 17101}},
	}
	connection := &bufferConn{}
	session := &gameSession{conn: connection, selectedCharacterID: 99}
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	service.sendCurrentTournamentStartMapProjection(session, state, scene, false)
	if connection.write.Len() != 0 {
		t.Fatalf("a foreign map emitted %d bytes", connection.write.Len())
	}
}
