package dnfbridge

import (
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 入场前冻结全部房间和双角色正文。准备阶段不绑定连接、不公告怪物、不扣费。
// 使用独立入口，避免普通副本的疲劳、掉落、经验、任务与翻牌资产处理。
type currentTagTournamentPrepared struct {
	runtime                   *runtimeDungeonState
	layout                    *tagtournament.Layout
	rooms                     map[worldmap.RoomCoordinate]currentTagTournamentPreparedRoom
	characters                [2]currentTagTournamentCharacterProjection
	characterBody, optionBody []byte
	skillState                currentTagTournamentSkillState
	entered                   bool
	creationSent              bool
	initialTagLoadPending     bool
}

type currentTagTournamentPreparedRoom struct {
	scene         worldmap.DungeonRoomScene
	room          *runtimeDungeonRoom
	nextKey, seed uint32
}

func (s *Service) prepareCurrentTagTournamentPlay(p *gameSession, layout *tagtournament.Layout, rows [2]currentTagTournamentCharacterProjection, characterBody, optionBody []byte) (*currentTagTournamentPrepared, error) {
	if layout == nil || layout.Topology == nil || layout.Topology.Start == nil || len(layout.Topology.Bosses) != 1 || len(characterBody) == 0 || len(optionBody) == 0 {
		return nil, fmt.Errorf("帝国竞技场入场准备资料不完整")
	}
	run, err := worldmap.NewDungeonRun(layout.Topology)
	if err != nil {
		return nil, err
	}
	session, err := worldmap.NewDungeonSession(run)
	if err != nil {
		return nil, err
	}
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return nil, err
	}
	dungeon, found := table.FindDungeon(session.Snapshot().Run.DungeonID)
	if !found {
		return nil, fmt.Errorf("帝国竞技场副本资源不存在")
	}
	var current int
	if rows[1].character.CharacterID == strconv.Itoa(int(p.selectedCharacterID)) {
		current = 1
	}
	runtime := &runtimeDungeonState{
		Request: dungeoncmd.SelectDungeonRequest{DungeonID: uint32(dungeon.ID), RuntimeToken: 0xffff},
		Dungeon: dungeon, Character: rows[current].character, AccountID: p.accountID,
		entryCharacterLevel: rows[current].character.Level, Session: session,
		BossCoordinate: layout.Topology.Bosses[0], BossSet: true,
		LayeredMapIndex: -1, StoryStageIndex: -1,
		LayerChains:               make(map[worldmap.RoomCoordinate]*runtimeDungeonLayerChain),
		Rooms:                     make(map[runtimeDungeonRoomKey]*runtimeDungeonRoomVisit),
		IgnoreDefaultDungeonClear: true, startedAt: s.gameplayNow(),
	}
	if err = freezeCurrentDungeonTownReturnOrigin(p, runtime); err != nil {
		return nil, err
	}
	monsters, err := s.dungeonMonsterCatalog()
	if err != nil {
		return nil, err
	}
	prepared := &currentTagTournamentPrepared{runtime: runtime, layout: layout, characters: rows, characterBody: append([]byte(nil), characterBody...), optionBody: append([]byte(nil), optionBody...), rooms: make(map[worldmap.RoomCoordinate]currentTagTournamentPreparedRoom)}
	nextKey := uint32(firstDungeonMonsterObjectKey)
	// Rooms的固定拓扑顺序只用于分配互不重复的对象键，回合顺序由layout.Rounds决定。
	for _, source := range session.Rooms() {
		scene, err := worldmap.NewDungeonRoomScene(source)
		if err != nil {
			return nil, err
		}
		room, next, err := newRuntimeDungeonRoom(scene, monsters, nextKey)
		if err != nil {
			return nil, err
		}
		var ai *pvfDungeonAICharacterCatalog
		if len(scene.AICharacters) != 0 {
			ai, err = s.dungeonAICharacterCatalog()
			if err != nil {
				return nil, err
			}
		}
		extended, err := planRuntimeDungeonExtendedActors(scene, monsters, ai, dungeon.Metadata.BasisLevel, next)
		if err != nil {
			return nil, err
		}
		if err = room.AttachExtendedActors(extended); err != nil {
			return nil, err
		}
		seed, err := s.chooseDungeonSeed()
		if err != nil {
			return nil, err
		}
		item := currentTagTournamentPreparedRoom{scene: scene, room: room, nextKey: extended.NextObjectKey, seed: seed}
		prepared.rooms[source.Coordinate] = item
		nextKey = extended.NextObjectKey
		// 每个回合都先构建真实op29，任何地图或actor缺失都在扣费前失败。
		if _, err = buildCurrentDungeonMoveStartMapBody(runtime, currentDungeonMovePlan{Target: scene, TargetRoom: room, NextObjectKey: item.nextKey, Seed: seed, Operation: currentDungeonStartMapOperationCurrent, PayloadMode: currentDungeonStartMapPayloadBuild}); err != nil {
			return nil, err
		}
	}
	start, ok := prepared.rooms[*layout.Topology.Start]
	if !ok {
		return nil, fmt.Errorf("帝国竞技场缺少起始房")
	}
	runtime.Room, runtime.NextObjectKey, runtime.Seed = start.room, start.nextKey, start.seed
	if _, err = buildCurrentDungeonEntryPackets(runtime, start.scene); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (s *Service) sendCurrentTagTournamentCreation(p *gameSession, b *currentTagTournamentBattle) error {
	if _, err := s.currentTagTournamentBattleFor(p); err != nil {
		return err
	}
	if b.prepared == nil {
		return fmt.Errorf("帝国竞技场缺少已冻结的双角色正文")
	}
	if b.prepared.creationSent {
		return nil
	}
	if len(b.prepared.skillState.commands) == 0 || len(b.prepared.skillState.buffSwap) == 0 {
		return fmt.Errorf("帝国竞技场缺少两名队员独立技能指令或Buff换装资料")
	}
	for _, row := range b.prepared.characters {
		if row.job == 9 && len(b.prepared.skillState.darkKnight) == 0 {
			return fmt.Errorf("帝国竞技场缺少黑暗武士独立连招正文")
		}
	}
	if err := s.sendGameUpperRawClass(p, 1389, b.prepared.characterBody, 0); err != nil {
		return err
	}
	for _, item := range []struct {
		op   uint16
		body []byte
	}{
		{currentTagTournamentCharacterOptionNotification, b.prepared.optionBody},
		{currentTagTournamentSkillCommandNotification, b.prepared.skillState.commands},
		{currentTagTournamentDarkKnightNotification, b.prepared.skillState.darkKnight},
		{currentTagTournamentBuffSwapNotification, b.prepared.skillState.buffSwap},
	} {
		if len(item.body) == 0 {
			continue
		}
		if err := s.sendGameUpperRawClass(p, item.op, item.body, 0); err != nil {
			return err
		}
	}
	if err := s.sendCurrentTagTournamentHotkeys(p, b, b.run.Snapshot().Active); err != nil {
		return err
	}
	b.prepared.creationSent = true
	return nil
}

func (s *Service) handleCurrentTagTournamentDungeon(p *gameSession, request dungeoncmd.SelectDungeonRequest) error {
	refuse := func(err error) error {
		s.logGameEvent(p, "帝国竞技场进图请求已拒绝", "dungeon_id", request.DungeonID, "difficulty", request.Difficulty, "selection_mode", request.SelectionMode, "runtime_state", request.RuntimeState, "error", err)
		return s.sendGameUpperRawClass(p, 16, []byte{0, currentSelectDungeonInvalidSelectionStateReason, 0}, 1)
	}
	if p.tagTournamentBattle == nil {
		b, err := s.resumeCurrentTagTournamentEntry(p, request)
		if err != nil {
			return refuse(err)
		}
		if err = s.sendCurrentTagTournamentEntry(p, b); err != nil {
			return err
		}
		// 兼容直接16的客户端：新建双角色后必须重新建立本场的27，
		// 不能复用创建前或上一场留下的选图上下文。
		p.enterSelectDungeonContextSent = false
		s.logGameEvent(p, "帝国竞技场当日已付费挑战已恢复", "mode", b.plan.Mode, "dungeon_id", request.DungeonID)
	}
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return refuse(err)
	}
	prepared := b.prepared
	if prepared == nil || prepared.entered || p.dungeon.runtime != nil || b.run.Snapshot().Phase != tagtournament.Lobby || request.DungeonID != b.plan.DungeonID || request.Difficulty != 0 || request.EntryOption != 0 || request.SelectionMode != 0 || request.RuntimeState != 0 || request.SpecialMode != 0 {
		return refuse(fmt.Errorf("帝国竞技场选图与已付费挑战不符或已经进图"))
	}
	if err = validateCurrentDungeonEntryRequestForModes(request, currentDungeonEntryStrictValidation, currentDungeonEntrySelectionModeStrict); err != nil {
		return refuse(err)
	}
	runtime := prepared.runtime
	runtime.Request = request
	scene, ok := runtime.Session.Scene()
	if !ok {
		return refuse(fmt.Errorf("帝国竞技场起始场景不存在"))
	}
	packets, err := buildCurrentDungeonEntryPackets(runtime, scene)
	if err != nil {
		return refuse(err)
	}
	// 独立提交只绑定生命周期；不调用普通副本的门票、疲劳和队伍实例提交。
	p.dungeon.mu.Lock()
	if p.dungeon.runtime != nil {
		p.dungeon.mu.Unlock()
		return refuse(errDungeonAlreadyActive)
	}
	p.dungeon.runToken = nextCurrentDungeonDeathGeneration(p.dungeon.runToken)
	runtime.lifecycleToken = p.dungeon.runToken
	p.dungeon.runtime, prepared.entered = runtime, true
	p.dungeon.mu.Unlock()
	resetDungeonEntrySceneGates(p)
	if err = s.sendCurrentSceneOp9PreviewActorRemovalOnce(p, "帝国竞技场实际入场"); err != nil {
		return err
	}
	if err = s.sendGameUpperSuccess(p, 16, buildSelectDungeonBodyForCharacter(p.selectedCharacterID)); err != nil {
		return err
	}
	if err = s.sendCurrentDungeonResourceState(p, runtime, "帝国竞技场入场资源状态"); err != nil {
		return err
	}
	if err = s.sendCurrentPreDungeonContextPlayerState(p, "帝国竞技场入场角色绑定"); err != nil {
		return err
	}
	if !p.enterSelectDungeonContextSent {
		if err = s.sendCurrentDungeonContextOp27(p, "帝国竞技场入场"); err != nil {
			return err
		}
		p.enterSelectDungeonContextSent = true
	}
	if err = s.sendGameUpperRawClass(p, 28, packets.DungeonInfo, 0); err != nil {
		return err
	}
	if err = s.sendPartyDungeonEntryActorSceneSnapshot(p, "帝国竞技场单人入场"); err != nil {
		return err
	}
	if err = s.sendGameUpperRawClass(p, 29, packets.StartMap, 0); err != nil {
		return err
	}
	if _, err = runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		return err
	}
	if err = runtime.cacheCurrentDungeonRoom(); err != nil {
		return err
	}
	if err = s.sendCurrentPostStartMapPlayerPlacement(p, runtime, scene, "帝国竞技场起始房落位"); err != nil {
		return err
	}
	// 1389仅登记两名对象并将活动身份置为-1。普通落位完成后必须用原生
	// 1402明确初始出战者，145D780/1457000才会登记活动身份、隐藏另一名。
	// 此处不调用Run.Tag，不产生一次额外切换或服务端冷却，也不重建1389。
	prepared.initialTagLoadPending = true
	if err = s.sendCurrentTagTournamentTagProjection(p, b); err != nil {
		return err
	}
	s.leaveTownPresenceForDungeon(p)
	s.logGameEvent(p, "帝国竞技场实际进图完成", "mode", b.plan.Mode, "dungeon_id", runtime.Dungeon.ID, "map_id", scene.Map.Map.ID, "round_count", len(b.plan.Encounters))
	return nil
}

func (s *Service) handleCurrentTagTournamentMove(p *gameSession, body []byte) error {
	refuse := func(err error) error {
		s.logGameEvent(p, "帝国竞技场下一关请求已拒绝", "error", err)
		return s.sendGameUpperFailure(p, 45, 1)
	}
	request, err := dungeoncmd.DecodeMoveMapRequest(body)
	if err != nil || len(body) != dungeoncmd.MoveMapRequestSize || len(request.OpaqueTail) != 0 || request.MoveKind != 0 {
		return refuse(fmt.Errorf("帝国竞技场过图正文不符"))
	}
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return refuse(err)
	}
	if b.prepared == nil || !b.prepared.entered {
		return refuse(fmt.Errorf("帝国竞技场尚未实际进图"))
	}
	if err = s.flushCurrentTagTournamentSettlement(p, b); err != nil {
		return err
	}
	p.dungeon.mu.Lock()
	defer p.dungeon.mu.Unlock()
	runtime := p.dungeon.runtime
	if runtime != b.prepared.runtime {
		return refuse(fmt.Errorf("帝国竞技场地图运行时已替换"))
	}
	snapshot := b.run.Snapshot()
	next := snapshot.Round + 1
	if snapshot.Phase != tagtournament.Lobby && snapshot.Phase != tagtournament.BetweenRounds && snapshot.Phase != tagtournament.SpecialChoice || next >= len(b.prepared.layout.Rounds) {
		return refuse(fmt.Errorf("帝国竞技场当前回合尚未结束或已完成"))
	}
	want := b.prepared.layout.Rounds[next]
	if int64(request.NextX) != want.Coordinate.X || int64(request.NextY) != want.Coordinate.Y {
		return refuse(fmt.Errorf("帝国竞技场禁止回退或越过回合"))
	}
	target, err := runtime.Session.PreviewMoveByteTransition(request.NextX, request.NextY)
	if err != nil || target.Revisit || target.Scene.Map.Map.ID != int64(want.Encounter.MapID) {
		return refuse(fmt.Errorf("帝国竞技场目标不在可进入的真实房间"))
	}
	room := b.prepared.rooms[want.Coordinate]
	plan := currentDungeonMovePlan{Target: target.Scene, TargetRoom: room.room, NextObjectKey: room.nextKey, Seed: room.seed, Operation: currentDungeonStartMapOperationCurrent, PayloadMode: currentDungeonStartMapPayloadBuild}
	wire, err := buildCurrentDungeonMoveStartMapBody(runtime, plan)
	if err != nil {
		return refuse(err)
	}
	now := b.now()
	encounter, nativeRecovery, err := b.run.PreviewNext(now, snapshot.Phase == tagtournament.SpecialChoice)
	if err != nil {
		return refuse(err)
	}
	if encounter != want.Encounter {
		return refuse(fmt.Errorf("帝国竞技场待进入地图与挑战日计划不一致"))
	}
	if err = validateCurrentTagTournamentNativeRecovery(b.plan.Mode, snapshot, next, encounter, nativeRecovery); err != nil {
		return refuse(err)
	}
	if err = s.sendGameUpperRawClass(p, 29, wire, 0); err != nil {
		return err
	}
	if _, nativeRecovery, err = b.beginRound(now, snapshot.Phase == tagtournament.SpecialChoice); err != nil {
		return err
	}
	if _, err = runtime.Session.MoveByteTransition(request.NextX, request.NextY, nil); err != nil {
		return err
	}
	runtime.Room, runtime.NextObjectKey, runtime.Seed = room.room, room.nextKey, room.seed
	p.currentFinishLoadingStateSent = false
	p.currentFinishLoadingCompletionSent = false
	if _, err = room.room.AnnounceAllActors(runtime.Session); err != nil {
		return err
	}
	if err = runtime.cacheCurrentDungeonRoom(); err != nil {
		return err
	}
	// 王房确认已在客户端执行恢复；过图不重发角色、技能或背包来模拟它。
	// 领域只在地图通知写成功后恢复存活队员，Tag及六道具冷却保持。
	s.logGameEvent(p, "帝国竞技场下一关已进入", "round", next+1, "map_id", want.Encounter.MapID, "monster_id", want.Encounter.MonsterID, "active_character", b.characters[b.run.Snapshot().Active].CharacterID, "native_special_recovery", nativeRecovery)
	return nil
}
