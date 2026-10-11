package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件是操作者缺陷「在开启挑战深渊派对时 副本无法正常结算通关，没有翻牌，右上角也没返回
// 城镇的结算」的修复，**同时**是上一轮"清完深渊房间就结算"那个回归的撤销。
//
// ── 两轮之间的口径变化（本轮的真判据）────────────────────────────────────────
//
//	上一轮（已上线，错误）：判据 = "当前房间就是本局冻结的深渊房间 + 房间被权威判清 +
//	                       本局波次行一只不剩" ⇒ 清完深渊房间（柱子/深渊怪）当场结算。
//	                       现场证据：`game-dungeon-hell-party-final-clear-committed
//	                       ... room=3:0 map_id=60083`（深渊房间，不是 Boss 房），
//	                       紧接着 `game-dungeon-settlement-entry-op31-send ... room=3:0`
//	                       ⇒ 操作者「打死柱子 NPC 后直接弹出结算画面、直接翻牌，
//	                       不能正常进入后面的地图」。
//	本轮（修复）        ：判据 = **与普通最终房间逐字节同一个函数**
//	                       `currentDungeonOrdinaryFinalRoomReady`
//	                       （`dungeon_boss_die_ordinary.go:110-132`）：
//	                         `scene.Cleared`（:114）
//	                       + `currentDungeonStoryFinalStageMatches`（:117）
//	                       + 剧情 AI Boss 双死门（:120-122）
//	                       + `scene.Boss && runtime.BossSet && scene.Coordinate == runtime.BossCoordinate`（:123-124）
//	                       ⇒ 只有迷宫 Boss 房才结算，深渊房间清空**不再**结算。
//
// 因此：
//
//	**保留**：`currentDungeonOrdinaryFinalRoomReady` 整条判据（含 `scene.Cleared`、
//	          `scene.Boss`、`scene.Coordinate == runtime.BossCoordinate`）。
//	**删除**：① "当前房间是本局深渊房间"（`IsHellRoom`）；② "波次行全灭"
//	          （`WaveTotal > 0 && WaveRemaining == 0`）；③ "房间被权威判清即可"
//	          （`RoomCleared` 单独成立不再足够）。
//	**新增**："本局是深渊派对局"（`HellRoomSet && len(HellPartyWaves) > 0`）—— 本文件只
//	          接深渊局的落点，非深渊局一律在状态读取前 `return nil`，逐字节不介入。
//
// ── 为什么 Boss 房一定能走到通关判定（本轮实测证据）──────────────────────────
//
// 深渊波次机制**不碰 Boss 房**：隐藏模板行只在 `scene.Map.Map.ID == runtime.HellRoom.MapID`
// 时追加（`dungeon_runtime_state.go:669-676`、`dungeon_party_instance.go:817-822`），
// NOTI 679 也只在同一个判据下发（`dungeon_hell_party_wave.go:582-584`）。Boss 房因此与
// 普通局完全同形，走的是既有普通最终房间链。现场佐证（本轮从 `packet_log.txt` 重建）：
//
//	game-000021 / dungeon 310 / maze 0：15:34:54 进深渊房 3:0（60083），
//	15:35:22–15:35:31 五只波次怪全部退役，**没有**任何结算；
//	15:36:16.648 `game-dungeon-ordinary-final-clear-committed ... room=5:2 map_id=36765
//	run_status=completed`、15:36:16.665 `game-dungeon-settlement-entry-op31-send ... room=5:2`
//	—— 即"深渊局走到 Boss 房照常结算"这件事**本来就已经成立**，上一轮的提前结算纯属多余。
//
// ── 开关 ──────────────────────────────────────────────────────────────────────
//
// DNFBRIDGE_HELL_PARTY_CLEAR_SETTLEMENT
//
//	（未设置/空/其它任意值，含 `vendor`、`boss`）= 修复档（默认）：只有
//	    `currentDungeonOrdinaryFinalRoomReady` 成立（= 迷宫 Boss 房 + 房间权威判清）才提交通关。
//	`legacy`（一键回退到"本轮修复前**线上正在跑**的行为"）：深渊房间波次全灭 + 房间判清
//	    即在深渊房间提交通关 —— 逐字节等于上一轮那版（提前结算）。
//	`off`（回到更早的历史行为）：深渊房间**永不**由本文件结算（一个字节都不发），
//	    与上一轮之前的线上行为一致。
//
// 只有字面量 `legacy` / `off` 改变行为；拼错/未知取值一律取默认（修复）档。
// 开关每次用到时自读 env（`options.go` 不参与），现场只改 env 重启即回退。
//
// 奖励口径**不受本开关影响**，反过来也不影响本判定：波次奖励投骰/必掉碎片都发生在本判定
// **之前**（`dungeon_combat_runtime.go` 的 `resolveHellPartyReward` /
// `grantAbyssEpicFragmentGuarantee`），且它们的所有失败分支只记日志、不 return。
const dungeonHellPartyClearSettlementEnv = "DNFBRIDGE_HELL_PARTY_CLEAR_SETTLEMENT"

// 唯一被承认的两个回退取值。
const (
	dungeonHellPartyClearSettlementLegacyValue = "legacy"
	dungeonHellPartyClearSettlementOffValue    = "off"
)

type dungeonHellPartyClearSettlementMode int

const (
	// dungeonHellPartyClearSettlementBossRoom 是默认档，也是修复档（零值 = 修复）：
	// 判据 = 普通最终房间的 Boss 房判据（`currentDungeonOrdinaryFinalRoomReady`）。
	dungeonHellPartyClearSettlementBossRoom dungeonHellPartyClearSettlementMode = iota
	// dungeonHellPartyClearSettlementHellRoom 是上一轮上线的"深渊房间清空即结算"档（提前结算）。
	dungeonHellPartyClearSettlementHellRoom
	// dungeonHellPartyClearSettlementOff 是更早的历史档：本文件完全不结算深渊房间。
	dungeonHellPartyClearSettlementOff
)

// readDungeonHellPartyClearSettlementMode 解析 DNFBRIDGE_HELL_PARTY_CLEAR_SETTLEMENT。
// **只有显式 `legacy` / `off` 才回退**；未设置/空/其它任何值都取默认档（修复），
// 与 `readDungeonHellRoomMode` / `readDungeonHellPartyWaveRowsMode` 同一条约定。
func readDungeonHellPartyClearSettlementMode(value string) dungeonHellPartyClearSettlementMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case dungeonHellPartyClearSettlementLegacyValue:
		return dungeonHellPartyClearSettlementHellRoom
	case dungeonHellPartyClearSettlementOffValue:
		return dungeonHellPartyClearSettlementOff
	default:
		return dungeonHellPartyClearSettlementBossRoom
	}
}

func (mode dungeonHellPartyClearSettlementMode) String() string {
	switch mode {
	case dungeonHellPartyClearSettlementHellRoom:
		return dungeonHellPartyClearSettlementLegacyValue
	case dungeonHellPartyClearSettlementOff:
		return dungeonHellPartyClearSettlementOffValue
	default:
		return "vendor"
	}
}

// settles 报告该档位是否把深渊局接到本局通关链上（`off` 档完全不接）。
func (mode dungeonHellPartyClearSettlementMode) settles() bool {
	return mode != dungeonHellPartyClearSettlementOff
}

// bossRoomOnly 报告该档位是否要求"必须落在迷宫 Boss 房"（默认档 = 是）。
func (mode dungeonHellPartyClearSettlementMode) bossRoomOnly() bool {
	return mode == dungeonHellPartyClearSettlementBossRoom
}

// criterion 是进日志的判据名（下一次实测单看一行就知道用的是哪一条）。
func (mode dungeonHellPartyClearSettlementMode) criterion() string {
	if mode.bossRoomOnly() {
		return "ordinary_final_room_boss_coordinate"
	}
	return "deployed_hell_party_room_cleared"
}

// dungeonHellPartyClearSettlementModeFromEnv 每次用到时读一次 env。
func dungeonHellPartyClearSettlementModeFromEnv() dungeonHellPartyClearSettlementMode {
	return readDungeonHellPartyClearSettlementMode(os.Getenv(dungeonHellPartyClearSettlementEnv))
}

// dungeonHellPartyClearState 是一次深渊局判定用到的全部输入（只读快照）。
type dungeonHellPartyClearState struct {
	// AbyssRun：本局真的冻结了深渊房间与波次（`HellRoomSet && len(HellPartyWaves) > 0`）。
	AbyssRun bool
	// IsHellRoom：当前场景就是本局冻结的深渊房间。
	IsHellRoom bool
	// HellRoomMapID：本局冻结的深渊房间地图 id（`[seal door map index]`）。
	HellRoomMapID int64
	// RoomMapID / Room：当前场景的房图与坐标（日志用）。
	RoomMapID int64
	Room      string
	// BossRoom：当前场景是迷宫 `[boss map]`（`scene.Boss`）。
	BossRoom bool
	// BossCoordinateMatched：`runtime.BossSet && scene.Coordinate == runtime.BossCoordinate`。
	BossCoordinateMatched bool
	// BossRoomReady / BossRoomSource：`currentDungeonOrdinaryFinalRoomReady` 的判定结果
	// （与普通最终房间**同一个函数**，这才是本轮的判据）。
	BossRoomReady  bool
	BossRoomSource string
	// WaveOrders：房间里出现过的波次 order（升序无关，只为点名）。
	WaveOrders []int
	// WaveGroups：房间里出现过的波次组号。
	WaveGroups []int64
	// WaveTotal / WaveRemaining：波次行总数 / 还没退役的只数。
	WaveTotal     int
	WaveRemaining int
	// BlockingRemaining：房间还没清掉的阻挡怪只数。
	BlockingRemaining int
	// RoomCleared：服务端权威清房位（`scene.Cleared`）。
	RoomCleared bool
	// TotalMonsters / RemainingMonsters：房间在册 actor 总数 / 还没死透的数量
	// （波次行 + 房间怪 + 扩展 actor，日志用）。
	TotalMonsters     int
	RemainingMonsters int
}

// roomType 是进日志的"判定发生在哪种房间"：下一次实测里，若结算发生在
// `hell_party_room`，就是提前结算回归复发（开关被写成 legacy 或代码被判据改动带偏）。
func (state dungeonHellPartyClearState) roomType() string {
	switch {
	case state.IsHellRoom && state.BossRoom:
		return "hell_party_room_and_boss_room"
	case state.IsHellRoom:
		return "hell_party_room"
	case state.BossRoom && state.BossCoordinateMatched:
		return "boss_room"
	default:
		return "ordinary_room"
	}
}

// bossRoomBlockedReason 把"Boss 房判据为什么没过"拆成具名理由（日志可判读）。
func (state dungeonHellPartyClearState) bossRoomBlockedReason() string {
	switch {
	case !state.BossRoom:
		return "not_the_maze_boss_room"
	case !state.BossCoordinateMatched:
		return "boss_coordinate_mismatch"
	case !state.RoomCleared:
		return "boss_room_not_authoritatively_cleared"
	case state.BossRoomSource != "":
		return state.BossRoomSource
	default:
		return "ordinary_final_room_criterion_not_ready"
	}
}

// currentDungeonHellPartyAbyssRun 报告本局是不是"冻结了深渊房间与波次的深渊局"。
// 本文件只接这种局；非深渊局（含所有普通房间）在状态读取前就 `return nil`。
func currentDungeonHellPartyAbyssRun(runtime *runtimeDungeonState) bool {
	return runtime != nil && runtime.HellRoomSet && len(runtime.HellPartyWaves) > 0
}

// currentDungeonHellPartyClearState 读一次判定输入。它只读服务端在册表与会话场景，
// 不消费任何随机数、不发任何包，因此回退档与"不触发"档逐字节无副作用。
func currentDungeonHellPartyClearState(
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
) dungeonHellPartyClearState {
	state := dungeonHellPartyClearState{
		RoomMapID:         scene.Map.Map.ID,
		Room:              scene.Coordinate.String(),
		RoomCleared:       scene.Cleared,
		BlockingRemaining: len(scene.BlockingHostiles),
		BossRoom:          scene.Boss,
	}
	if runtime == nil {
		return state
	}
	state.AbyssRun = currentDungeonHellPartyAbyssRun(runtime)
	state.BossCoordinateMatched = runtime.BossSet && scene.Coordinate == runtime.BossCoordinate
	state.BossRoomReady, state.BossRoomSource = currentDungeonOrdinaryFinalRoomReady(runtime, scene)
	state.HellRoomMapID = runtime.HellRoom.MapID
	state.IsHellRoom = currentHellPartySceneIsHellRoom(runtime, scene)
	if runtime.Room == nil {
		return state
	}
	snapshot := runtime.Room.Snapshot()
	seenOrder := make(map[int]struct{}, 2)
	seenGroup := make(map[int64]struct{}, 2)
	for _, monster := range snapshot.Monsters {
		state.TotalMonsters++
		if monster.State != runtimeDungeonMonsterDefeated {
			state.RemainingMonsters++
		}
	}
	for _, actor := range snapshot.ExtendedActors {
		if actor.State != runtimeDungeonMonsterDefeated {
			state.RemainingMonsters++
		}
		state.TotalMonsters++
		// 厂商 `BuildHellPartyGroupRemaining` 只认 `HellPartyGroupId > 0`，这里同口径。
		if actor.Kind != runtimeDungeonActorSpecialMonster || actor.HellPartyGroupID <= 0 {
			continue
		}
		state.WaveTotal++
		if actor.State != runtimeDungeonMonsterDefeated {
			state.WaveRemaining++
		}
		if _, exists := seenOrder[actor.HellPartyOrder]; !exists {
			seenOrder[actor.HellPartyOrder] = struct{}{}
			state.WaveOrders = append(state.WaveOrders, actor.HellPartyOrder)
		}
		if _, exists := seenGroup[actor.HellPartyGroupID]; !exists {
			seenGroup[actor.HellPartyGroupID] = struct{}{}
			state.WaveGroups = append(state.WaveGroups, actor.HellPartyGroupID)
		}
	}
	return state
}

// currentDungeonHellPartyClearDecision 是唯一的判定（纯函数，便于用例逐条钉）。
// 返回值 = 是否提交通关 + 具名理由（进日志，下一次实测单看一行就能判读）。
//
// 默认档（修复）：**只认 `currentDungeonOrdinaryFinalRoomReady`** —— 即
// `dungeon_boss_die_ordinary.go:110-132` 那条"迷宫 Boss 房 + 房间权威判清"的判据，
// 与普通最终房间逐字节同源。深渊房间无论清得多干净都不再结算（这是本轮撤销的回归）。
func currentDungeonHellPartyClearDecision(
	state dungeonHellPartyClearState,
	mode dungeonHellPartyClearSettlementMode,
) (bool, string) {
	if !state.AbyssRun {
		// 本局没有深渊房间/波次：这不是本文件的落点。
		return false, "not_a_hell_party_run"
	}
	switch mode {
	case dungeonHellPartyClearSettlementOff:
		return false, "hell_party_clear_settlement_disabled"
	case dungeonHellPartyClearSettlementHellRoom:
		// 上一轮上线的判据，逐条保留（回退档 = 线上正在跑的提前结算行为）。
		if state.WaveTotal == 0 {
			return false, "no_frozen_hell_party_wave_rows"
		}
		if state.WaveRemaining > 0 {
			return false, "hell_party_wave_monsters_remaining"
		}
		if !state.RoomCleared {
			return false, "room_not_authoritatively_cleared"
		}
		return true, "deployed_all_hell_party_wave_monsters_dead_and_room_cleared"
	default:
		// 修复档：与普通最终房间**同一个**判据。
		if !state.BossRoom {
			return false, "not_the_maze_boss_room"
		}
		if !state.BossCoordinateMatched {
			return false, "boss_coordinate_mismatch"
		}
		if !state.RoomCleared {
			return false, "boss_room_not_authoritatively_cleared"
		}
		if !state.BossRoomReady {
			return false, state.bossRoomBlockedReason()
		}
		if state.BossRoomSource != "" {
			return true, state.BossRoomSource
		}
		return true, "ordinary_final_room_boss_coordinate"
	}
}

// completeCurrentDungeonHellPartyFinalRoomAfterDeathLocked 在深渊局里跑**既有的**
// 最终房间通关链（`Session.Complete()` + `completeCurrentDungeonBossDieCheckLocked`），
// 与普通最终房间逐字节同一条链，判据见 `currentDungeonHellPartyClearDecision`。
//
// 默认档下它在 Boss 房只会看到 `already_accepted`：同一个调用点上普通最终房间链先跑
// （`dungeon_combat_runtime.go:877-892`），完成位已置。真正需要它兜底的是**波次怪退役**
// 那条分支（`dungeon_combat_runtime.go:482-490`）——那条分支不发普通清房判定，
// 若 Boss 房的最后一只在册 actor 走的是特殊怪退役，只有本函数能把它接上通关链。
//
// 幂等：`runtime.ordinaryFinalRoomClearAccepted` / `bossDieCheckAccepted` 一旦置位，
// 之后每一次调用都只记一行 `already_accepted`，不会再发第二条 op31（
// `sendCurrentDungeonSettlementEntryLocked` 自己也有 `settlementEntrySent` 门）。
//
// 调用方持有 session.dungeon.mu（与既有死亡路径一致）。
func (s *Service) completeCurrentDungeonHellPartyFinalRoomAfterDeathLocked(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	objectKey uint32,
	trigger string,
) error {
	if s == nil || session == nil || runtime == nil || runtime.Session == nil ||
		session.dungeon.runtime != runtime {
		return nil
	}
	mode := dungeonHellPartyClearSettlementModeFromEnv()
	// `off` 档：一个字节都不发、场景/在册表都不读，逐字节等于本文件不存在。
	if !mode.settles() {
		return nil
	}
	if runtime.Room == nil || isPVFTutorialDungeonScene(runtime, scene) ||
		!dungeonRuntimeOwnsCharacter(runtime, session.selectedCharacterID) {
		return nil
	}
	// 落点过滤（都在读快照之前，普通局/无关房间的代价与日志噪声为 0）：
	//   - 回退档：逐字节等于线上那版 —— 非深渊房间一律不介入；
	//   - 修复档：只关心本局冻结的深渊房间与迷宫 Boss 房这两处。
	if mode.bossRoomOnly() {
		if !currentDungeonHellPartyAbyssRun(runtime) {
			return nil
		}
		if !scene.Boss && !currentHellPartySceneIsHellRoom(runtime, scene) {
			return nil
		}
	} else if !currentHellPartySceneIsHellRoom(runtime, scene) {
		return nil
	}
	state := currentDungeonHellPartyClearState(runtime, scene)
	settle, reason := currentDungeonHellPartyClearDecision(state, mode)
	accepted := runtime.ordinaryFinalRoomClearAccepted || runtime.bossDieCheckAccepted
	leaving := runtime.townReturnPending || runtime.townReturnOp24Sent
	decision := "defer"
	switch {
	case accepted:
		decision = "already_accepted"
	case leaving:
		decision = "leaving_town"
	case settle:
		decision = "settle"
	}
	// 这一行是"下一次实测一眼可判"的那一条：点名房间类型、判据、波次、剩余怪、清空判定与结算触发。
	s.logGameEvent(session, "game-dungeon-hell-party-clear-decision",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", state.Room,
		"map_id", state.RoomMapID,
		"room_type", state.roomType(),
		"boss_room", state.BossRoom,
		"boss_coordinate_matched", state.BossCoordinateMatched,
		"boss_room_ready", state.BossRoomReady,
		"boss_room_source", state.BossRoomSource,
		"hell_room_map_id", state.HellRoomMapID,
		"hell_room_set", runtime.HellRoomSet,
		"is_hell_party_room", state.IsHellRoom,
		"abyss_run", state.AbyssRun,
		"trigger", trigger,
		"retired_object_key", objectKey,
		"wave_orders", state.WaveOrders,
		"wave_groups", state.WaveGroups,
		"wave_total", state.WaveTotal,
		"wave_remaining", state.WaveRemaining,
		"blocking_remaining", state.BlockingRemaining,
		"tracked_monsters", state.TotalMonsters,
		"remaining_monsters", state.RemainingMonsters,
		"room_cleared", state.RoomCleared,
		"decision", decision,
		"reason", reason,
		"settle_criterion", mode.criterion(),
		"switch", mode.String(),
		"settlement_trigger", "session_complete_then_op115_then_bodyless_op31",
		"vendor_source", "vendor_DungeonKillApplicationService_cs_982_993_clear_condition_or_boss_map")
	if decision != "settle" {
		return nil
	}
	pendingLayer, nextLayerIndex, nextLayerMapID, layerErr := s.currentDungeonPendingLayer(runtime, scene)
	if layerErr != nil {
		s.logGameEvent(session, "game-dungeon-hell-party-final-clear-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"room", state.Room,
			"map_id", state.RoomMapID,
			"retired_object_key", objectKey,
			"trigger", trigger,
			"reason", "pending_layer_resolution_failed",
			"error", layerErr)
		return nil
	}
	if pendingLayer {
		s.logGameEvent(session, "game-dungeon-hell-party-final-clear-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"room", state.Room,
			"map_id", state.RoomMapID,
			"retired_object_key", objectKey,
			"trigger", trigger,
			"next_layer_index", nextLayerIndex,
			"next_layer_map_id", nextLayerMapID,
			"reason", "explicit_pvf_layer_must_be_consumed_before_settlement")
		return nil
	}
	if decision := evaluateCurrentTournamentClearIntentLocked(session, runtime); !decision.Allowed {
		s.logGameEvent(session, "game-dungeon-hell-party-final-clear-deferred",
			"char_id", session.selectedCharacterID,
			"dungeon_id", runtime.Dungeon.ID,
			"room", state.Room,
			"map_id", state.RoomMapID,
			"retired_object_key", objectKey,
			"trigger", trigger,
			"reason", decision.Reason,
			"source", "hell_party_final_room_authoritative_op39")
		return nil
	}
	if err := runtime.Session.Complete(); err != nil {
		return err
	}
	// 与 `completeCurrentDungeonOrdinaryFinalRoomAfterDeathLocked`
	// （`dungeon_boss_die_ordinary.go:83-88`）逐字段同一组标志：本仓的最终房间完成位
	// 只有这一组，复用它们既让结算链认得这一局，也天然挡住重复提交。
	runtime.ordinaryFinalRoomClearAccepted = true
	runtime.bossDieCheckAccepted = true
	runtime.bossDieCheckRelatedActorObjectKey = currentSceneActorObjectKey(session.selectedCharacterID)
	runtime.bossDieCheckTargetObjectKey = uint16(objectKey)
	runtime.bossDieCheckPending = false
	runtime.bossDieCheckPendingRequest = dungeoncmd.BossDieCheckRequest{}
	completed := runtime.Session.Snapshot()
	s.logGameEvent(session, "game-dungeon-hell-party-final-clear-committed",
		"char_id", session.selectedCharacterID,
		"dungeon_id", runtime.Dungeon.ID,
		"maze_index", runtime.MazeIndex,
		"room", completed.Run.Current.String(),
		"map_id", state.RoomMapID,
		"room_type", state.roomType(),
		"boss_room", state.BossRoom,
		"hell_room_map_id", state.HellRoomMapID,
		"retired_object_key", objectKey,
		"trigger", trigger,
		"wave_orders", state.WaveOrders,
		"wave_groups", state.WaveGroups,
		"wave_total", state.WaveTotal,
		"wave_remaining", state.WaveRemaining,
		"blocking_remaining", state.BlockingRemaining,
		"remaining_monsters", state.RemainingMonsters,
		"run_status", completed.Run.Status,
		"defeated_actor_count", len(completed.Scene.DefeatedObjects),
		"completion_source", mode.criterion(),
		"op117_required", false,
		"op115_sent", false,
		"next_stage", "persist_completion_then_bodyless_settlement_op31",
		"vendor_source", "vendor_DungeonKillApplicationService_cs_982_993_clear_condition_or_boss_map")
	return s.completeCurrentDungeonBossDieCheckLocked(
		session,
		runtime,
		"hell_party_final_room_authoritative_op39",
	)
}
