package dnfbridge

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// 本文件是 P0「组队深渊两边都不掉碎片」的根因修复。
//
// ── 现场（`packet_log.txt`，2026-09-28T17:52，`instance_id=1`，房间 3:0 / map 60083）──
//
//	EVENT kind=game-dungeon-abyss-epic-fragment-skipped conn_id=game-000012 … monster_object_key=436
//	      group_id=214 final_wave_order=2 final_wave_groups=212 wave_count=2 is_final_wave=false
//	      … reason=not_group_last group_alive_count=3 t=2026-09-28T17:52:05.886769777Z
//	EVENT … monster_object_key=437 group_id=214 final_wave_groups=212 … reason=not_group_last group_alive_count=2 t=17:52:06.491633548Z
//	EVENT … monster_object_key=435 group_id=214 final_wave_groups=212 … reason=not_group_last group_alive_count=1 t=17:52:07.952202544Z
//	EVENT … monster_object_key=434 group_id=214 final_wave_groups=212 wave_count=2 is_final_wave=false
//	      … reason=not_final_wave t=2026-09-28T17:52:13.5205315Z      ← 房间里那一波的**最后一只**，死了
//	EVENT kind=game-dungeon-hell-party-clear-decision conn_id=game-000012 … room=3:0 map_id=60083
//	      … wave_groups=[126 214] wave_total=5 wave_remaining=0 … t=17:52:13.520863103Z   ← **房间**的波次组是 {126,214}
//
// `436/437/435/434` 全部由 **conn game-000012 = char 6（队员）** 击杀；那一局里那名队员
// 自己的 `runtime.HellPartyWaves`（**按会话各自抽签**，见 `dungeon_hell_party_wave.go`
// 与上一轮报告 §3.3）说"最后一波 = 组 **212**"，而**共享房间**里的扩展 actor 行是组 **{126,214}**。
// 于是 `finalWave.containsGroup(214) == false` ⇒ `reason=not_final_wave` ⇒ 保底整支不发。
// 掉落账本是**实例共享**的，所以"击杀方拿不到"就等于**全队都拿不到**：
// 这一局的 `wave-peer-notify` 全部 `body_len=7 drop_rows=0`（见 `_dist/abyss-round3.md` §8）。
//
// ── 这不是本轮（第三轮）引入的回归 ────────────────────────────────────────────
//
//  1. 那四条 `-skipped` 事件里**没有** `item_level_band=` / `catalog_candidates_*` 字段 ——
//     本轮新加的等级地板严格在这条 `return` **之后**，根本没跑到；
//  2. `is_final_wave=false` 是**同一段旧代码**（`dungeonHellPartyFinalWave`）算出来的，
//     它读的是会话冻结波次表，本轮**一行未改**；
//  3. 上一轮报告 §3.3 已经把这个"两名成员各自抽签"记成已知缺陷并留了排期
//     （当时判定"不是卡白怪的直接成因"，故未修）。
//
// ── 修复口径：**最后一波以"房间"为准，不以"我自己抽到的那张表"为准** ─────────────
//
//	房间的扩展 actor 行是**共享**的（`shareOrRegisterDungeonRoom`），每行都带
//	`HellPartyOrder` 与 `HellPartyGroupID`（`dungeon_extended_actor_runtime.go`），
//	所以由它推出的"最后一波"对**两名成员逐字段相同**：谁打死房间里那一波的最后一只，
//	谁就触发（且只触发一次）保底。这正是操作者口径「击杀深渊最后一波 npc 怪物最后一个怪物」。
//
// 开关 `DNFBRIDGE_ABYSS_EPIC_FRAGMENT_FINAL_WAVE`：
//
//	未设置/空/`room`/任意其它值 = 修复档：以**房间**的波次行为准；
//	字面量 `session`（也接受 `legacy`）= 逐字节回到今天：以**本会话冻结的波次表**为准。
//	房间行读不到时**回落**到会话表（绝不猜、也不因此不发），并在事件里点名用的是哪一档。
const abyssEpicFragmentFinalWaveEnv = "DNFBRIDGE_ABYSS_EPIC_FRAGMENT_FINAL_WAVE"

const (
	abyssEpicFragmentFinalWaveRoomValue    = "room"
	abyssEpicFragmentFinalWaveSessionValue = "session"
)

// abyssEpicFragmentFinalWaveSource 报告"最后一波"是从哪读出来的（进事件，可判读）。
type abyssEpicFragmentFinalWaveSource int

const (
	abyssEpicFragmentFinalWaveFromRoom abyssEpicFragmentFinalWaveSource = iota
	abyssEpicFragmentFinalWaveFromSession
)

type abyssEpicFragmentFinalWaveMode int

const (
	// abyssEpicFragmentFinalWaveRoom 是默认档，也是修复档（零值 = 修复）。
	abyssEpicFragmentFinalWaveRoom abyssEpicFragmentFinalWaveMode = iota
	// abyssEpicFragmentFinalWaveSession 是改动前的会话档。
	abyssEpicFragmentFinalWaveSession
)

func readAbyssEpicFragmentFinalWaveMode(value string) abyssEpicFragmentFinalWaveMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case abyssEpicFragmentFinalWaveSessionValue, abyssEpicFragmentLevelLegacyValueForFinalWave(),
		"off", "legacy":
		return abyssEpicFragmentFinalWaveSession
	default:
		return abyssEpicFragmentFinalWaveRoom
	}
}

func abyssEpicFragmentLevelLegacyValueForFinalWave() string {
	return abyssItemLevelLegacyValue
}

func (mode abyssEpicFragmentFinalWaveMode) String() string {
	if mode == abyssEpicFragmentFinalWaveSession {
		return abyssEpicFragmentFinalWaveSessionValue
	}
	return abyssEpicFragmentFinalWaveRoomValue
}

func (mode abyssEpicFragmentFinalWaveMode) roomScoped() bool {
	return mode == abyssEpicFragmentFinalWaveRoom
}

func abyssEpicFragmentFinalWaveModeFromEnv() abyssEpicFragmentFinalWaveMode {
	return readAbyssEpicFragmentFinalWaveMode(os.Getenv(abyssEpicFragmentFinalWaveEnv))
}

func (source abyssEpicFragmentFinalWaveSource) String() string {
	if source == abyssEpicFragmentFinalWaveFromSession {
		return "session_frozen_wave_table"
	}
	return "room_shared_extended_actor_rows"
}

// currentDungeonHellPartyRoomFinalWave 从**共享房间**的扩展 actor 行推"最后一波"：
// `Order` 最大的那一档 order 上出现过的全部组号。
//
// 与 `currentDungeonHellPartyClearState` 读的是同一张表、同一组字段
// （`Kind == special_monster && HellPartyGroupID > 0`），所以它与
// `game-dungeon-hell-party-clear-decision` 打出来的 `wave_groups=` 必然一致。
//
// 房间行里**一个波次行都没有**时返回 false（调用方回落会话表，绝不猜）。
func currentDungeonHellPartyRoomFinalWave(runtime *runtimeDungeonState) (abyssEpicFragmentFinalWave, bool) {
	result := abyssEpicFragmentFinalWave{}
	if runtime == nil || runtime.Room == nil {
		return result, false
	}
	snapshot := runtime.Room.Snapshot()
	orders := make(map[int]struct{}, 2)
	maxOrder := 0
	for _, actor := range snapshot.ExtendedActors {
		if actor.Kind != runtimeDungeonActorSpecialMonster || actor.HellPartyGroupID <= 0 {
			continue
		}
		if actor.HellPartyOrder <= 0 {
			// order 非法的行不参与"哪一波最大"的判定（与 `dungeonHellPartyFinalWave` 同口径）。
			continue
		}
		orders[actor.HellPartyOrder] = struct{}{}
		if actor.HellPartyOrder > maxOrder {
			maxOrder = actor.HellPartyOrder
		}
	}
	if maxOrder <= 0 {
		return result, false
	}
	seen := make(map[int64]struct{}, 2)
	for _, actor := range snapshot.ExtendedActors {
		if actor.Kind != runtimeDungeonActorSpecialMonster || actor.HellPartyGroupID <= 0 ||
			actor.HellPartyOrder != maxOrder {
			continue
		}
		if _, duplicate := seen[actor.HellPartyGroupID]; duplicate {
			continue
		}
		seen[actor.HellPartyGroupID] = struct{}{}
		result.GroupIDs = append(result.GroupIDs, actor.HellPartyGroupID)
	}
	if len(result.GroupIDs) == 0 {
		return result, false
	}
	sort.Slice(result.GroupIDs, func(left, right int) bool { return result.GroupIDs[left] < result.GroupIDs[right] })
	sortedOrders := make([]int, 0, len(orders))
	for order := range orders {
		sortedOrders = append(sortedOrders, order)
	}
	sort.Ints(sortedOrders)
	result.Order = int64(maxOrder)
	result.WaveCount = len(sortedOrders)
	result.Index = len(sortedOrders) - 1
	for index, order := range sortedOrders {
		if order == maxOrder {
			result.Index = index
			break
		}
	}
	return result, true
}

// abyssEpicFragmentSessionWaveGroupList 把**本会话冻结的**波次组压成一行（日志用），
// 用于与 `room_wave_groups=` 逐字对照（两者不一致就是"各自抽签"那一类）。
func abyssEpicFragmentSessionWaveGroupList(runtime *runtimeDungeonState) string {
	if runtime == nil || len(runtime.HellPartyWaves) == 0 {
		return "-"
	}
	seen := make(map[int64]struct{}, len(runtime.HellPartyWaves))
	groups := make([]int64, 0, len(runtime.HellPartyWaves))
	for _, wave := range runtime.HellPartyWaves {
		if wave.GroupID <= 0 {
			continue
		}
		if _, duplicate := seen[wave.GroupID]; duplicate {
			continue
		}
		seen[wave.GroupID] = struct{}{}
		groups = append(groups, wave.GroupID)
	}
	if len(groups) == 0 {
		return "-"
	}
	sort.Slice(groups, func(left, right int) bool { return groups[left] < groups[right] })
	parts := make([]string, 0, len(groups))
	for _, groupID := range groups {
		parts = append(parts, strconv.FormatInt(groupID, 10))
	}
	return strings.Join(parts, ",")
}

// abyssEpicFragmentFinalWaveFor 按生效档位取"最后一波"，并报告来源。
//
// 修复档先读房间；房间行读不到（例如刚清空/热更）时**回落**会话表 —— 两种来源都以事件字段
// `final_wave_source=` 点名，所以"这一局用的是哪一档"在日志里一眼可见。
func abyssEpicFragmentFinalWaveFor(
	runtime *runtimeDungeonState,
	mode abyssEpicFragmentFinalWaveMode,
) (abyssEpicFragmentFinalWave, abyssEpicFragmentFinalWaveSource, bool) {
	if mode.roomScoped() {
		if wave, ok := currentDungeonHellPartyRoomFinalWave(runtime); ok {
			return wave, abyssEpicFragmentFinalWaveFromRoom, true
		}
	}
	wave, ok := dungeonHellPartyFinalWave(runtime)
	if !ok {
		return abyssEpicFragmentFinalWave{}, abyssEpicFragmentFinalWaveFromSession, false
	}
	return wave, abyssEpicFragmentFinalWaveFromSession, true
}
