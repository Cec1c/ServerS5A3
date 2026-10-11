package dnfbridge

import (
	"context"
	"strconv"
	"strings"
)

// 本文件实现「深渊派对按钮为什么不会变灰」的服务端那一半。
//
// # 现场缺陷（操作者报障）
//
//	「6 未完成深渊任务的角色深渊还是亮的」
//
// `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 已经把逐槽位判定 + blocked 槽位下发做完了
// （见 dungeon_hell_blocked_slots.go），可按钮依旧是亮的。缺口在**时机**上：
//
//   - blocked 槽位唯一的落点是 class0/op27（= 厂商 `NOTI 0x001B`
//     `ENTER_SELECT_DUNGEON` 下发体）的 `List0`（`u8 条数 + N×u16`）；
//   - 改动前 `evaluateHellEligibilityPerSlot` **只在 op16**（玩家点「挑战深渊派对」）
//     那一跳里跑。而 op27 是**开选择页**（op15 应答之后）那一步就发出去的
//     （线上 source=`upper_15_after_op15_ack_fatigue`，`hell_blocked_slots=` 恒空）
//     ⇒ 客户端拿到按钮状态时服务端**还没判过**，按钮永远是亮的；
//     等 op16 判出来时玩家已经点完了，永远不会看到"灰"。
//
// # 厂商口径（只读引用 S4A21，逐行对照）
//
// `Network\Handlers\Dungeon\DungeonEntryHandler.cs`
//
//	:723-726  var hellPartySelection = isA21TutorialEntry
//	              ? null
//	              : BuildHellPartySelectionState(session.Player);   ← 在 ENTER_SELECT_DUNGEON handler 里
//	:780-786  await session.SendPacketAsync(GamePacketEnvelopeBuilder.Build(
//	              0x00, 0x001B,
//	              EnterSelectDungeonStateBuilder.BuildA21EnterSelectDungeon(
//	                  hellPartySelection.UserIds,
//	                  hellPartySelection.BlockedSlots)));           ← blocked 就在这里下发
//
// 也就是说：**判定发生在"开选择页"那一刻，结果随 NOTI 0x001B 一起发**。
// 本文件把这条时序补回来。
//
// 厂商的"这一页属于哪个区域"也不是从 op16 猜的，而是从**玩家站着的副本门口**读
// （`DungeonEntryHandler.cs:1425-1463 BuildHellPartySelectionState`）：
//
//	Town.TryGetDungeonGateReturnInfo(player.CurTownId, player.CurAreaId, out var gate)
//	&& gate.WorldMapAreaId > 0  ⇒  state.WorldMapArea = WorldMap.GetAreaById(gate.WorldMapAreaId)
//
// 本仓的等价物就是既有城镇目录的 `[dungeon gate] N`（`town.Area.DungeonGate`，
// `internal/modules/dnf/town/parse.go:67-71` 解析、`town_catalog.go:39` 的
// `townArea(townID, areaID)` 读取）：当前所在城镇/区域的 `[dungeon gate]` 那个整数
// **就是 worldmap area id**。随后仍走既有 `resolveHellQuestRequirement`
// （`[hell dungeon]` + `[hell quest]`），判据与 op16 那一跳**同源**，不新造第二套。
//
// # 开关
//
//	`DNFBRIDGE_HELL_SELECT_PAGE_ELIGIBILITY`
//	  - 未设置/空/其它任何值（默认）= `vendor`：开选择页时先按上面那条厂商时序
//	    判一次逐槽位资格，结果写进会话缓存 ⇒ 随这一条 class0/op27 下发；
//	  - `legacy` = 改动前行为（只在 op16 判，页面那条 op27 的 blocked 列表恒空），
//	    一键回退、不需要重新编译。
//
// 这一档还要求 `DNFBRIDGE_HELL_BLOCKED_SLOTS=on`（blocked 列表唯一的投递通道；
// 投影关着时"提前判定"没有任何落点，本函数直接返回、一个字节都不碰）。
// 两条开关各自独立：任一条回到回退档都逐字节等于今天。
const hellSelectPageEligibilityEnv = "DNFBRIDGE_HELL_SELECT_PAGE_ELIGIBILITY"

const hellSelectPageEligibilityValueLegacy = "legacy"

// hellSelectPageEligibilityMode 是 hellSelectPageEligibilityEnv 的归一化取值。
type hellSelectPageEligibilityMode int

const (
	// hellSelectPageEligibilityVendor 是默认档（零值）：开选择页时判定。
	hellSelectPageEligibilityVendor hellSelectPageEligibilityMode = iota
	// hellSelectPageEligibilityLegacy = 改动前（只在 op16 判）。
	hellSelectPageEligibilityLegacy
)

func (mode hellSelectPageEligibilityMode) String() string {
	if mode == hellSelectPageEligibilityLegacy {
		return hellSelectPageEligibilityValueLegacy
	}
	return "vendor"
}

// evaluatesAtPageBuild 报告是否在开选择页时判定并下发 blocked 槽位。
func (mode hellSelectPageEligibilityMode) evaluatesAtPageBuild() bool {
	return mode != hellSelectPageEligibilityLegacy
}

// readHellSelectPageEligibilityMode 解析 DNFBRIDGE_HELL_SELECT_PAGE_ELIGIBILITY。
// **默认 vendor**：空/未设置/非法值都取 vendor；只有显式写 `legacy` 才回退
// （与 `readHellPartyEntryMode` 同一约定）。
func readHellSelectPageEligibilityMode(value string) hellSelectPageEligibilityMode {
	if strings.EqualFold(strings.TrimSpace(value), hellSelectPageEligibilityValueLegacy) {
		return hellSelectPageEligibilityLegacy
	}
	return hellSelectPageEligibilityVendor
}

// hellSelectPageEligibilityGate 返回当前 Service 的归一化档位（零值 options 的
// 测试/探针路径 = vendor = 默认档）。
func (s *Service) hellSelectPageEligibilityGate() hellSelectPageEligibilityMode {
	if s == nil {
		return hellSelectPageEligibilityVendor
	}
	return s.options.hellSelectPageEligibility
}

// dungeonGateHellRequirement 是"这一页属于哪个门口区域、门槛是什么"的取证结果。
type dungeonGateHellRequirement struct {
	// Requirement 是既有的 `[hell dungeon]` / `[hell quest]` 判定输入。
	Requirement hellQuestRequirement
	// GateAreaID / GateFound 是当前城镇区域 `[dungeon gate]` 声明的 worldmap area。
	GateAreaID int64
	GateFound  bool
	// Reason 是"为什么用/不用这条 requirement"的稳定判据串（可 grep）。
	Reason string
}

// currentDungeonGateHellQuestRequirement 解析"玩家现在站的这个副本门口属于哪个
// worldmap area，该 area 的深渊任务门槛是什么"。
//
// 判据链与厂商 `BuildHellPartySelectionState` 逐条对应：当前城镇/区域
// （`currentSceneTransitionLocation`，= 厂商 `player.CurTownId`/`CurAreaId`）
// → 该区域的 `[dungeon gate] N`（= 厂商 `gate.WorldMapAreaId`）
// → `worldmap.FindArea(N)` → `resolveHellQuestRequirement`。
//
// 任何一步读不到都返回"该区域没有门槛"（`Gate=false`）+ 一个可 grep 的 `Reason`，
// **绝不拦人**：这一层的产物只喂 UI 的 blocked 列表，误报比漏报糟（与
// `evaluateHellEligibility` 的 fail-open 口径一致）。
func (s *Service) currentDungeonGateHellQuestRequirement(session *gameSession) dungeonGateHellRequirement {
	result := dungeonGateHellRequirement{
		Requirement: hellQuestRequirement{
			PvfStatus: hellPVFStatusAreaUnresolved,
			Reason:    "hell_party_area_not_indexed",
		},
		Reason: "hell_party_dungeon_gate_area_unresolved",
	}
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return result
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		result.Reason = "hell_party_character_repository_unavailable"
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repositories.Character.Load(
		ctx, strconv.FormatUint(uint64(session.selectedCharacterID), 10))
	if err != nil || !found {
		result.Reason = "hell_party_character_location_unavailable"
		return result
	}
	townID, areaID, err := currentSceneTransitionLocation(character, true)
	if err != nil {
		result.Reason = "hell_party_current_town_unavailable"
		return result
	}
	townArea, areaFound := s.townArea(int64(townID), int64(areaID))
	if !areaFound || townArea.DungeonGate == nil || *townArea.DungeonGate <= 0 {
		// 玩家不在副本门口（在街上/在赛丽亚房间/在酒馆）：这一页与深渊无关。
		result.Reason = "hell_party_current_area_is_not_a_dungeon_gate"
		return result
	}
	result.GateAreaID = *townArea.DungeonGate
	result.GateFound = true
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		result.Requirement.Reason = "hell_party_world_map_unavailable"
		result.Reason = "hell_party_world_map_unavailable"
		return result
	}
	area, found := table.FindArea(result.GateAreaID)
	result.Requirement = resolveHellQuestRequirement(area, found)
	result.Reason = result.Requirement.Reason
	return result
}

// refreshHellSelectPageEligibility 是开选择页那一刻的**唯一**新调用点
// （对应厂商 `DungeonEntryHandler.cs:723-726`）。默认档下它会：
//
//  1. 按厂商口径把这一页对应的 worldmap area 解出来（门口 → `[dungeon gate]`）；
//  2. 走既有的 `evaluateHellEligibility` 逐槽位判定（判据 = `[hell quest]`
//     + 持久化任务记录，与 op16 那一跳同源）；
//  3. **整体覆盖**会话上的 blocked 缓存，于是紧随其后的 class0/op27 就会带上
//     它 —— 客户端在按钮渲染前就拿到了"这一格不能打深渊"。
//
// 回退档（`legacy`）或 `DNFBRIDGE_HELL_BLOCKED_SLOTS` 未开时**一个字节都不碰**。
func (s *Service) refreshHellSelectPageEligibility(session *gameSession, source string) {
	if s == nil || !s.hellSelectPageEligibilityGate().evaluatesAtPageBuild() {
		return
	}
	if !s.hellBlockedSlotsGate().deliversBlockedSlots() {
		// blocked 列表唯一的下发通道关着 ⇒ 提前判定没有任何落点，不读库不写缓存。
		return
	}
	if session == nil || session.selectedCharacterID == 0 {
		return
	}
	requirement := s.currentDungeonGateHellQuestRequirement(session)
	evaluation := s.evaluateHellEligibility(context.Background(), session, requirement.Requirement)
	recordHellBlockedSlots(session, evaluation)
	mode := s.hellBlockedSlotsGate().String()
	for _, blocked := range evaluation.Blocked {
		s.logHellEligibilityBlocked(session, evaluation, blocked, mode, source)
	}
	s.logHellEligibilityEvaluated(session, evaluation, mode, source)
	s.logGameEvent(session, "game-hell-select-page-eligibility-evaluated",
		"conn_id", session.connID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"select_page_eligibility_mode", s.hellSelectPageEligibilityGate().String(),
		"hell_blocked_slots_mode", mode,
		"dungeon_gate_area", requirement.GateAreaID,
		"dungeon_gate_area_found", requirement.GateFound,
		"hell_dungeon", evaluation.Requirement.HellDungeon,
		"hell_quest_gate", evaluation.Requirement.Gate,
		"pvf_status", evaluation.Requirement.PvfStatus,
		"blocked_count", len(evaluation.Blocked),
		"blocked_slots", formatHellBlockedSlots(evaluation.Blocked),
		"requester_slot", evaluation.RequesterSlot,
		"requester_blocked", evaluation.RequesterBlocked,
		"reason", requirement.Reason,
		"body_source", "a21_build_hell_party_selection_state_at_enter_select_dungeon",
		"vendor_evidence", "DungeonEntryHandler.cs:723-726+780-786+1425-1463",
	)
}
