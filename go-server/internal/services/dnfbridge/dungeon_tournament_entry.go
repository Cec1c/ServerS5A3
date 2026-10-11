package dnfbridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ── 锦标赛进场接线 ───────────────────────────────────────────────────────────
//
// 本文件把「PVF 结构化段 → 准入 → 赛制运行时 → 房间投影」接进既有进图链。
//
// **默认档（`DNFBRIDGE_DUNGEON_TOURNAMENT` 未设置/空/未知值）下本文件的所有
// 入口都是 no-op**：不读背包、不解析锦标赛定义、不投影 actor、不发任何包，
// 控制流逐字节等于改动前。

// ── 锦标赛地图**不发**普通 START_MAP 的判据（P8 进图阻塞修复）────────────────
//
// 厂商口径（唯一真源，逐行）：
//
//	`Network\Handlers\Dungeon\DungeonMapHandler.cs:534-539`
//	  `var isTournamentMap = _svc.Tournaments.TryProjectStartMap(run, maze, out var tournamentMaze);
//	   if (isTournamentMap) maze = tournamentMaze;`
//	`:778-779`
//	  `startMapBody = Array.Empty<byte>();
//	   if (!isBloodAltarMap && !isTournamentMap) { … 组装普通 START_MAP … }`
//	  ⇒ **锦标赛地图根本不组装 START_MAP 体**（`pendingStandardMaze` 也只在同一分支里被赋值）。
//	`:943-958`
//	  `if (!sendTournamentProjection) { … 发 START_MAP … }` ⇒ 也不发这一包。
//	`:932-934`
//	  `var sendTournamentProjection = tournament != null && sentMapId == tournament.Definition.MapId;`
//	  ⇒ 判据就是"这张图的 id 等于锦标赛定义的 MapId"。
//	`:1033-1077`
//	  `if (sendTournamentProjection) { 发 TOURNAMENT_INFO(387) ; 发 TOURNAMENT_MAP_INFO(388) }`
//	  ⇒ 房间与 actor 全部由这两条 NOTI 承担（本仓真机号 386/387）。
//	`:1001-1009` 那条 `START_MAP first sent` 日志在锦标赛地图上**不会**出现（因为没发）。
//
// 为什么本仓必须跟着绕开（而不是"给空 rank 编一个含义"）：
//
//  1. 真档 `map/tournament/yellowdragon_00.map` / `bluedragon_00.map` **没有 `[monster]` 段**
//     （P8 取证：全文 dump 里 `^[monster]` 命中 0 次），对手只声明在
//     `[tournament enemies]` —— 语法是 `PartyCount kind Code Strength Name`
//     （厂商 `Tool\PvfLib\Models\MapFile.cs:894-952`），**没有 rank 列**。
//     本仓把它的 `kind == `[monster]“ 翻成厂商的 `actorType = 0`
//     （`GameWorld\TournamentDungeonDefinition.cs:414`，本仓同值见
//     `dungeon_tournament_definition.go:216`），而 0 在 START_MAP 里表示"普通怪"
//     （厂商 `GameWorld\Dungeon.cs:35-36` 的 `MonsterSumInfo.Type` 注释：`0..3 为怪物`）。
//     它投影出来的 `MonsterSpawn` 因此**没有 PVF rank 字符串**（`Rank == ""`），
//     撞上 `dungeon_start_map.go:326-349` 的 rank→actorType 闸门 ⇒
//     `errDungeonStartMapMonsterRank`。这不是"数据缺失"，而是"这些 actor 本来就不该
//     出现在 START_MAP 里"：厂商的 START_MAP 体在锦标赛地图上压根不组装。
//  2. 强行生成会与 386 的路径 actor 段**双份公告**同一批 actor（本仓
//     `sendCurrentTournamentStartMapProjection` 把 4×partyCount 个 sequence 一次性全投），
//     客户端是否去重**本仓零证据**（收尾接线批 §4④ 的未确证项第 1 条）。
//  3. op39 的 owner 校验**不依赖 op29 是否发出**：它读的是
//     `runtime.Room`（`AnnounceAllActors` 建的绑定）与
//     `runtime.Tournament.firstSequence`，两者与 op29 的发送无耦合 ——
//     见 `dungeon_navigation_runtime.go` 的 `announce_start_map_actors` 那一步
//     （无论是否发 op29 都会执行）与 `dungeon_runtime_state.go:470-484` 的
//     `tryBindFirstActorSequence(room.monsters[0].ObjectKey)`。
//     ⇒ 公告者从 op29 换成 386 之后，`sequence` 空间逐值不变：
//     `386 的第 i 条 u16 seq == firstSequence + i == 房间第 i 只怪的 ObjectKey == op39 上报的 RuntimeObjectKey`。
const currentTournamentStartMapSuppressionReason = "vendor_DungeonMapHandler.cs:778-779_tournament_map_has_no_ordinary_start_map_body"

// currentTournamentMapSuppressesOrdinaryStartMap 报告"本次进图/换房间不该生成普通
// START_MAP(op29) 体"。判据与厂商 `sendTournamentProjection`（`DungeonMapHandler.cs:932-934`）
// 逐字对齐：`tournament != null && sentMapId == tournament.Definition.MapId`。
//
// **默认档恒为 (\"\", false)**：`dungeonTournamentEnabled()` 假、或 `runtime.Tournament`
// 为空（该句柄只可能由 `prepareTournamentDungeonRun` 在 vendor 档写入）⇒ 直接返回。
// 因此默认档一个字节都不受影响（既有 `bytes.Equal` 用例仍然成立）。
//
// 返回的 reason 是**具名依据**，只用于日志与断言：调用方必须把它写进
// `game-dungeon-tournament-start-map-suppressed` 事件，绝不允许"少发一段然后当成功"。
func currentTournamentMapSuppressesOrdinaryStartMap(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
) (string, bool) {
	if !dungeonTournamentEnabled() || runtime == nil || runtime.Tournament == nil {
		return "", false
	}
	definition := runtime.Tournament.Definition
	if definition == nil || scene.Map.Map.ID != definition.MapID {
		return "", false
	}
	return currentTournamentStartMapSuppressionReason, true
}

// currentTournamentEntryItems 把 DGN 的 `[required item]` 结构化段翻成准入要求。
// 厂商口径：`[event required item]` / `[added required item]` **不进**锦标赛准入
// （TournamentDungeonDefinition.cs:479-480 只吃 `dungeon.RequiredItems`），
// 因此这里同样只取 RequiredItems。
func currentTournamentEntryItems(dungeon worldmap.Dungeon) []dnfdungeon.EntryItemRequirement {
	if len(dungeon.Metadata.RequiredItems) == 0 {
		return nil
	}
	items := make([]dnfdungeon.EntryItemRequirement, 0, len(dungeon.Metadata.RequiredItems))
	for _, item := range dungeon.Metadata.RequiredItems {
		items = append(items, dnfdungeon.EntryItemRequirement{
			ItemID:         item.ItemID,
			Count:          item.Count,
			ConsumeOnEntry: item.ConsumeOnEntry,
		})
	}
	return items
}

// currentCharacterItemCounts 读出角色**背包**（不含仓库）里每个道具 id 的总持有数。
// 只数 `Slots`：本仓仓库是另一个字段，厂商准入读的是 owned inventory；
// 少算只会更严格（拒绝），不会放行不该放行的角色 —— 与"缺项报错"同向。
func (s *Service) currentCharacterItemCounts(ctx context.Context, characterID uint16) (map[int64]int64, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Inventory == nil {
		return nil, dnfdungeon.ErrEntryRequiredItemUnknown
	}
	record, found, err := repositories.Inventory.Load(ctx, strconv.Itoa(int(characterID)))
	if err != nil {
		return nil, err
	}
	if !found {
		return map[int64]int64{}, nil
	}
	counts := make(map[int64]int64, len(record.Slots))
	for _, stack := range record.Slots {
		if stack.ItemID <= 0 || stack.Count <= 0 {
			continue
		}
		counts[stack.ItemID] += stack.Count
	}
	return counts, nil
}

// validateDungeonEntryWithTournament 在既有 `validateDungeonEntry` 之外补上
// `[required item]` 准入。**开关关闭时直接返回原函数的结果**：连背包都不读，
// 也不构造 RequiredItems，默认档逐字节不变。
//
// 拒绝走的是既有回包路径：错误从 prepareDungeonRuntime 冒到
// dungeon_navigation_runtime.go:295 的 refuseDungeonSelectEntryValidation，
// 由 sendSelectDungeonFailure 回 `{0, reason, 0}`（不是静默丢弃）。
func (s *Service) validateDungeonEntryWithTournament(
	ctx context.Context,
	character dnfrepo.CharacterRecord,
	dungeon worldmap.Dungeon,
	session *gameSession,
) error {
	if s != nil && s.options.currentClientVersion == "official-24.9" {
		if err := dnfdungeon.ValidateDayDungeonSchedule(dungeon, s.gameplayNow()); err != nil {
			return err
		}
	}
	if s.currentRiftEntryEnabled(dungeon.ID) {
		return s.validateCurrentRiftEntry(ctx, character, dungeon, session)
	}
	if s.currentDayDungeonEntryEnabled(dungeon.ID) {
		return s.validateCurrentDayDungeonEntry(ctx, character, dungeon, session)
	}
	if !dungeonTournamentEnabled() && !s.currentEpicEntryEnabled(dungeon.ID) {
		return validateDungeonEntry(character, dungeon, session)
	}
	required := currentTournamentEntryItems(dungeon)
	if len(required) == 0 {
		return validateDungeonEntry(character, dungeon, session)
	}
	counts, err := s.currentCharacterItemCounts(ctx, sessionSelectedCharacterID(session))
	if err != nil {
		return err
	}
	partyCount := currentDungeonPartyCount(session)
	fatigue, fatigueKnown := dnfdungeon.CharacterStat(character.Stats, "fatigue", "fp", "疲劳")
	return dnfdungeon.ValidateEntry(dnfdungeon.EntryPolicy{
		CharacterLevel:      character.Level,
		MinimumLevel:        dungeon.Metadata.MinimumRequiredLevel.Value,
		MinimumLevelSet:     dungeon.Metadata.MinimumRequiredLevel.Set,
		PartyCount:          partyCount,
		PartyLimit:          dungeon.Metadata.LimitPartyCount.Value,
		PartyLimitSet:       dungeon.Metadata.LimitPartyCount.Set,
		NoFatigue:           dungeon.Metadata.NoFatigue,
		EnterWithoutFatigue: dungeon.Metadata.Flags["enter without fatigue"],
		Fatigue:             fatigue,
		FatigueKnown:        fatigueKnown,
		RequiredItems:       required,
		ItemCounts:          counts,
	})
}

// consumeDungeonEntryItemsWithTournament 是**入场消耗**（`Flag == 1` ⇒ `ConsumeOnEntry`）
// 的接线点：与准入校验同一个函数、同一个事务边界，紧跟在校验通过之后执行。
//
// 厂商对拍：
//
//	`Network\Handlers\Dungeon\DungeonEntryAdmissionApplicationService.cs:170-183`
//	  以 `tournamentDefinition.EntryItems` 收取 —— 校验与扣减在**同一次入场准入**里发生；
//	`Docs\副本架构业务接入规范.md:862`
//	  "入场物品按 Definition 在进图时通过统一背包入口扣除并持久化，失败同时恢复实体主背包槽
//	   与主虚拟槽；回城、失败或放弃不返还。"
//	  ⇒ 扣减失败必须在**入场前**明确拒绝，且**不回滚/不返还**。
//
// 硬性口径（任务书）：
//   - 只扣 `ConsumeOnEntry == true` 的条目（`DungeonFile.cs:938` 的 `values[i+2] == 1`）；
//   - 持仓不足 ⇒ 返回错误 ⇒ 冒到 `refuseDungeonSelectEntryValidation` 的既有拒绝回包；
//   - **不许静默扣 0**：一条都扣不动（NoneToConsume）时既不写库也不报成功；
//   - 开关关闭 / DGN 没声明 `[required item]` ⇒ 一行都不执行、不读背包、不写库。
//
// 表结构变化：扣减会改动主背包槽位，因此**必须**把被改动的槽位刷给客户端
// （否则客户端背包里证物还在，直到下一次全量刷新）。刷新失败只记具名事件，
// 不影响已经提交的入场（厂商同样把"网络投影失败"与"已持久化扣减"分开）。
func (s *Service) consumeDungeonEntryItemsWithTournament(
	ctx context.Context,
	dungeon worldmap.Dungeon,
	session *gameSession,
) error {
	if s.currentEpicEntryEnabled(dungeon.ID) {
		return nil
	}
	if s.currentRiftEntryEnabled(dungeon.ID) {
		// Rift tickets and count commit together after full runtime preparation.
		return nil
	}
	if !dungeonTournamentEnabled() {
		return nil
	}
	required := currentTournamentEntryItems(dungeon)
	consume := make([]dnfdungeon.EntryItemRequirement, 0, len(required))
	for _, requirement := range required {
		if requirement.ConsumeOnEntry {
			consume = append(consume, requirement)
		}
	}
	if len(consume) == 0 {
		return nil
	}
	characterID := sessionSelectedCharacterID(session)
	if characterID == 0 {
		return dnfdungeon.ErrEntryRequiredItemUnknown
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.CharacterItems == nil {
		return dnfdungeon.ErrEntryRequiredItemUnknown
	}
	owner, err := dnfdungeon.NewOwner(repositories)
	if err != nil {
		return err
	}
	var refreshes []alignedcmd.ItemSlotRefresh
	result, err := owner.ConsumeEntryItems(ctx, dnfdungeon.EntryItemConsumeCommand{
		CharacterID:  strconv.FormatUint(uint64(characterID), 10),
		Requirements: consume,
		UpdatedAt:    s.gameplayNow().UTC(),
		Project: func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
			refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{
				ListType:  dnfrepo.MainInventoryListType,
				SlotIndex: slot,
			})
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			return stack, nil
		},
	})
	if err != nil {
		// 不足 / 事务失败都会到这里：**入场必须被拒绝**，不静默放行。
		s.logGameEvent(session, "game-dungeon-tournament-entry-item-consume-rejected",
			"char_id", characterID,
			"dungeon_id", dungeon.ID,
			"requirements", len(consume),
			"reason", "required_item_consume_transaction_failed_or_insufficient",
			"error", err)
		return fmt.Errorf("consume tournament entry items: %w", err)
	}
	consumedSummary := make([]string, 0, len(result.Consumed))
	for _, consumed := range result.Consumed {
		consumedSummary = append(consumedSummary, fmt.Sprintf("%d:%d", consumed.ItemID, consumed.Count))
	}
	s.logGameEvent(session, "game-dungeon-tournament-entry-item-consumed",
		"char_id", characterID,
		"dungeon_id", dungeon.ID,
		"changed", result.Changed,
		"consumed", consumedSummary,
		"slot_refresh_count", len(refreshes),
		"refund_policy", "no_refund_on_failure_or_giveup",
		"source", "vendor_DungeonEntryAdmissionApplicationService.cs:170-183_required_item_consume_on_entry")
	if len(refreshes) != 0 {
		if refreshErr := s.sendSelectedIncrementalItemSlotRefreshes(
			session,
			"dungeon-tournament-entry-item-consume",
			refreshes,
		); refreshErr != nil {
			s.logGameEvent(session, "game-dungeon-tournament-entry-item-refresh-deferred",
				"char_id", characterID,
				"dungeon_id", dungeon.ID,
				"slot_refresh_count", len(refreshes),
				"error", refreshErr)
		}
	}
	return nil
}

// prepareTournamentDungeonRun 在 `BuildDungeonLayout` 之后、`NewDungeonRun` 之前
// 建好赛制运行时，并把**当轮对手**投影成 start 房间的 blocking 怪。
//
// 开关关闭或该 DGN 没有 `[tournament dungeon]` 时返回 (nil, nil)：调用方控制流不变。
//
// 任何硬失败（厂商 TournamentDungeonDefinition.cs:250-510 的 failureReason）
// 都作为 error 返回 —— 由既有进图拒绝路径回给客户端，**不铺半套怪**。
func (s *Service) prepareTournamentDungeonRun(
	topology *worldmap.DungeonTopology,
	dungeon worldmap.Dungeon,
	partyCount int,
) (*tournamentDungeonRuntime, error) {
	if !dungeonTournamentEnabled() {
		return nil, nil
	}
	if topology == nil || topology.Start == nil {
		return nil, nil
	}
	if !dungeon.Metadata.Flags["tournament dungeon"] {
		return nil, nil
	}
	start := *topology.Start
	room, ok := topology.Room(start)
	if !ok || room.Map == nil {
		return nil, fmt.Errorf("tournament start room %s is unresolved", start)
	}
	mapValue := room.Map.Map
	definition, failureReason, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, s.tournamentAPCLevelResolver())
	if !ok {
		return nil, fmt.Errorf("tournament dungeon definition rejected: %s", failureReason)
	}
	runtime, failureReason, ok := newTournamentDungeonRuntime(&definition, partyCount, s.tournamentRandomSource())
	if !ok {
		return nil, fmt.Errorf("tournament runtime rejected: %s", failureReason)
	}
	monsters, err := tournamentRoomMonsters(definition, runtime)
	if err != nil {
		return nil, err
	}
	if err := topology.ProjectTournamentRoomActors(start, monsters); err != nil {
		return nil, fmt.Errorf("project tournament round actors: %w", err)
	}
	return runtime, nil
}

// tournamentAPCLevelResolver 是厂商
// `DungeonActorTemplateProjector.TryGetAiCharacterLevel` 的等价物
// （TournamentDungeonDefinition.cs:416-428）。`[apc]` 分支要求 code 必须能在
// `AICharacter/AICharacter.lst` 里解出等级，否则整次准备硬失败。
func (s *Service) tournamentAPCLevelResolver() tournamentApcLevelResolver {
	return func(code int64) (byte, bool) {
		catalog, err := s.dungeonAICharacterCatalog()
		if err != nil || catalog == nil {
			return 0, false
		}
		definition, found, err := catalog.Find(code)
		if err != nil || !found {
			return 0, false
		}
		return definition.Level, true
	}
}

// tournamentRoomMonsters 把**全部 4 轮**的对手翻成本仓的 `MonsterSpawn`。
//
// ⚠️ 本批修正（有厂商 file:line 依据）：C 批第一版只投影"当轮对手"
// （`roundIndex := currentRound-1` 取 partyCount 个）。那是**错的**：
//   - 厂商 `TournamentDungeonApplicationService.TryProjectStartMap`（:86-107）投影的是
//     `Monsters = CopyActors(runtime.PathActors)`（**:101**），即 `4*partyCount` 个全部对手；
//   - 只投当轮会让房间在第 1 轮结束后变成 0 阻挡怪 ⇒ `scene.Cleared = true`
//     ⇒ 走普通"房间清空 ⇒ 副本通关"路径，正是"C 批不敢接 shouldClearDungeon"的原因；
//   - 全投之后，房间只有在第 4 轮对手也全灭时才清空，而那一刻赛制刚好是 Champion
//     ⇒ 既不会误判通关，也不会卡死。
//
// 客户端的当轮对手信息由 NOTI 387 的路径 actor 段给出
// （`TournamentPacketBuilder.cs:9-52` 的第二段：每轮 `number` + PartyLimit 个
// `u16 seq / i32 code / u8 level / u8 type`），发放点见
// `dungeon_navigation_runtime.go` 的 op28→op29 之间。
//
// 坐标来源：该 PartyCount 的 `[tournament start area]` 里的**对手侧**出生点
// （第 2 行；第 1 行是玩家侧）。厂商服务端不送坐标（TournamentDungeonApplicationService
// :86-107 只投影 Code/Level/Type/IsBlocking，388 包只送房间格 x/y），坐标由客户端读
// 自己的 PVF 消费；本仓必须给出 `MonsterSpawn.Position`，因此用 PVF 已声明的出生点，
// **不发明坐标**。第 2 个出生点缺失时（定义层已硬失败 ≥2）退回第一个。
//
// 第 4 轮的 `Type` 升格必须在 `MonsterSpawn` 上可表达：厂商 `actorType = 3`
// （怪物 Boss）用 `Rank = "[boss]"` 表达（`dungeon_start_map.go:341-349`
// `currentDungeonMonsterActorType` 正是这么读的）；`actorType = 8`（APC Boss）
// 在 `MonsterSpawn` 上没有等价表达 ⇒ **硬失败**，不静默降级成普通怪
// （`[apc]` 分支本批不实现，见交付报告"未做"第 4 条）。
func tournamentRoomMonsters(
	definition tournamentDefinition,
	runtime *tournamentDungeonRuntime,
) ([]worldmap.MonsterSpawn, error) {
	snapshot := runtime.snapshot()
	partyCount := definition.PartyLimit
	if partyCount <= 0 {
		return nil, fmt.Errorf("tournament party limit %d is invalid", partyCount)
	}
	var opponent *worldmap.TournamentStartAreaInfo
	var fallback *worldmap.TournamentStartAreaInfo
	for index := range definition.StartAreas {
		area := definition.StartAreas[index]
		if area.PartyCount != int64(partyCount) {
			continue
		}
		if fallback == nil {
			copied := area
			fallback = &copied
		}
		if opponent == nil && fallback != nil && area != *fallback {
			copied := area
			opponent = &copied
		}
	}
	position := worldmap.Point3{}
	switch {
	case opponent != nil:
		position = worldmap.Point3{X: opponent.X, Y: opponent.Y}
	case fallback != nil:
		position = worldmap.Point3{X: fallback.X, Y: fallback.Y}
	}
	actors := make([]worldmap.MonsterSpawn, 0, len(snapshot.PathActors))
	for index, actor := range snapshot.PathActors {
		spawn := worldmap.MonsterSpawn{
			MonsterID: actor.Code,
			Level:     int64(actor.Level),
			Position:  position,
		}
		switch actor.Type {
		case 0:
		case 3:
			spawn.Rank = "[boss]"
		case 8:
			return nil, fmt.Errorf(
				"tournament path actor %d needs the extended AICharacter owner for actor type 8 (not implemented)",
				index,
			)
		default:
			return nil, fmt.Errorf(
				"tournament path actor %d has an unsupported actor type %d", index, actor.Type)
		}
		actors = append(actors, spawn)
	}
	return actors, nil
}

// tournamentRandomSource 返回厂商 `Func<int,int> next` 的等价物：返回 [0,max) 的随机数。
// 复用本仓既有的 `chooseDungeonIndex`（`s.dungeonChoice` 可注入，测试才可复现），
// 它本来就是这个语义，且已在 op16 选图上被验证过。
func (s *Service) tournamentRandomSource() func(int) int {
	return func(exclusiveMaximum int) int {
		if exclusiveMaximum <= 0 {
			return 0
		}
		index, err := s.chooseDungeonIndex(exclusiveMaximum)
		if err != nil {
			return -1
		}
		return index
	}
}

// handleCurrentTournamentDieAICharacter 是上行 `DIE_AI_CHARACTER`（op144）的入口。
//
// ⚠️ **未确证**：本仓没有任何 op144 抓包/逆向证据，客户端 body 字段布局未证实。
// 因此本 handler 在打开开关时**只做只读观测**（原样记 hex），把这条 op 从匿名的
// `unhandled latest game command` 桶里提出来，**绝不按猜的布局去推状态机**
// （纪律：不许发明数据）。开关关闭时返回 (false, nil)，控制流逐字节不变。
func (s *Service) handleCurrentTournamentDieAICharacter(
	session *gameSession,
	typ uint16,
	body []byte,
) (bool, error) {
	if typ != a21CmdDieAICharacter {
		return false, nil
	}
	if !dungeonTournamentEnabled() {
		return false, nil
	}
	s.logPacketEvent("game-dungeon-tournament-die-ai-character-observed",
		"conn_id", session.connID,
		"char_id", sessionSelectedCharacterID(session),
		"msg_id", typ,
		"body_len", len(body),
		"body_hex", hex.EncodeToString(body),
		"behavior", "read_only_observed_no_state_transition",
		"reason", "op144_body_layout_not_proven_in_this_repo")
	return true, nil
}

// tournamentRuntimeForSession 取当前会话活动 runtime 上的锦标赛句柄（可能为 nil）。
func tournamentRuntimeForSession(session *gameSession) *tournamentDungeonRuntime {
	if session == nil {
		return nil
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.dungeon.runtime == nil {
		return nil
	}
	return session.dungeon.runtime.Tournament
}

// tournamentInfoBodyForSession 是"给客户端发 NOTI 387"时用的纯构体函数。
// 发放时机由包序列 owner 决定（本批未接线，见交付报告"未做"）。
func tournamentInfoBodyForSession(session *gameSession, difficulty byte) ([]byte, bool) {
	runtime := tournamentRuntimeForSession(session)
	if runtime == nil {
		return nil, false
	}
	body, err := buildTournamentInfoBody(runtime, difficulty, runtime.snapshot().FirstSequence)
	if err != nil {
		return nil, false
	}
	return body, true
}

// tournamentMapInfoBodyForSession 是"给客户端发 NOTI 388"时用的纯构体函数。
func tournamentMapInfoBodyForSession(session *gameSession, x, y byte, revisit bool) ([]byte, bool) {
	runtime := tournamentRuntimeForSession(session)
	if runtime == nil {
		return nil, false
	}
	return buildTournamentMapInfoBody(x, y, sessionSeed(session), runtime.Definition.MapID, revisit), true
}

func sessionSeed(session *gameSession) uint32 {
	if session == nil {
		return 0
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.dungeon.runtime == nil {
		return 0
	}
	return session.dungeon.runtime.Seed
}
