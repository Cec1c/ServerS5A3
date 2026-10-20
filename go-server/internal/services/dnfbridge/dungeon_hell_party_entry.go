package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// 本文件实现「深渊派对（hell party）进图前置条件」的服务端闸门。
//
// # 现场缺陷（操作者报障）
//
//	未完成深渊派对任务的角色可以直接点击挑战深渊派对按钮，
//	尝试进入副本后卡在地下城选择界面。
//
// 复现路径（服务端侧可判读的那一半）：
//
//  1. 客户端「挑战深渊派对」按钮发的是同一条 class-1/op16 选图请求，
//     区别在 body[5]（本仓叫 `EntryOption` 的低字节）非 0；
//  2. `validateCurrentDungeonEntryRequest`（dungeon_entry_packets.go）把
//     `entry_option != 0` 归进 `errDungeonEntryModeUnsupported`；
//  3. `handleDungeonSelectUpperPlanned` 对这个错误**只记一条
//     `game-dungeon-select-blocked reason=dungeon_owner_validation_failed`
//     然后 `return nil`** —— 客户端一个回包都收不到；
//  4. 客户端在选图界面等 op16 应答，于是"卡在地下城选择界面"。
//
// 也就是说：**本服从来没有校验过深渊派对前置条件，也从来没有把拒绝告诉客户端。**
//
// # 厂商/A21 对照（只读引用 `s4a21_master_latest_unzip` 的 ServerS4A21）
//
//   - `Server/DfoServer/Network/Parsers/Dungeon/SelectDungeonRequest.cs:14`
//     `public byte HellPartyRequestFlag => Flag1;`（`Flag1 = body[5]`，
//     `HellPartyDifficultyFlag => Flag2` = body[6]）—— 与本仓
//     `EntryOption = u16 @ body[5:7]` 是同一对字节；
//   - `Server/DfoServer/Game/Dungeon/DungeonEntryAdmissionApplicationService.cs:241`
//     `_costs.CheckHellQuestRequirement(lease.CharacterId, area, out var missingQuestId)`
//     不通过就 `Fail($"hell quest not cleared quest={missingQuestId}",
//     EntryCostFailureKind.MissingPermission)`；
//   - `Server/DfoServer/Game/Dungeon/DungeonEntryCostService.cs` 的
//     `EvaluateHellQuestRequirement` 真源是 worldmap area 的 `[hell quest]` 列表；
//   - `Server/DfoServer/GameWorld/WorldMap.cs:411` 从 area 文件读
//     `[hell dungeon]` / `[hell quest]` / `[hell freepass item]` / `[item condition]`。
//
// 这四段数据本仓**已经解析**（`internal/modules/dnf/worldmap/parse_worldmap.go:41`
// 的 `hell dungeon` / `hell quest`，字段在 `types.go:251` 的 `Area.HellDungeon` /
// `Area.HellQuestIDs`），缺的只是消费者 —— 就是本文件。
//
// A21 的拒绝包体由 `DungeonAdmissionRejectBuilder.Build` 写成
// `{u8 0}{u8 errorCode}{u8 context}`，与本仓已上线的 `sendSelectDungeonFailure`
// （`{0, reason, 0}`，等级不足档 reason=14，见 changlog「Merged the op16 level
// gate」）**逐字节同形**；错误码 `MissingPermission -> 0xAD`、
// `DungeonUnavailable -> 0x01`（`DungeonAdmissionRejectBuilder.cs:16-41`）。
//
// # 开关
//
//	`DNFBRIDGE_HELL_PARTY_ENTRY`
//	  - `vendor`（默认，零值）= 校验 `[hell dungeon]` / `[hell quest]` 前置，
//	    并且**一定**用 `{0, code, 0}` 回包，绝不静默丢弃；
//	  - `legacy` = 改动前行为（不校验、不回包、原样落到 `errDungeonEntryModeUnsupported`
//	    那条静默 `return nil`），供实机一键 A/B。
//
//	`DNFBRIDGE_HELL_BLOCKED_SLOTS`（本文件下面的 `hellBlockedSlotsOff` 档 = 未设置 =
//	逐字节今天）在 `vendor` 之上再加一层：
//	  - `on` = 按厂商口径**逐槽位**判 `[hell quest]`、不合规槽位进 blocked 列表并随进本
//	    选择页（class0/op27）下发，深渊请求照常落到普通进图链（操作者清单第 69 条
//	    「挑战深渊进不去副本」）。逐槽位判定/下发体/开关全文见 dungeon_hell_blocked_slots.go。
//
//	    **blocked 列表是纯 UI 投影，不是准入授权**：厂商在同一份源码里把这两件事分得
//	    很开 —— `Network\Handlers\Dungeon\DungeonEntryHandler.cs:1395-1421` 的
//	    `BuildHellPartySelectionState` 只把不合规槽位加进 `state.BlockedSlots`
//	    （随后随 `NOTI 0x001B` 进本选择页下发），而
//	    `Game\Dungeon\DungeonEntryAdmissionApplicationService.cs:233-250` 的
//	    `TryPrepareHellParty` 才是准入：`area?.HellDungeon != true` 拒，
//	    `CheckHellQuestRequirement(lease.CharacterId, area, out missingQuestId)`
//	    不过就 `MissingPermission` 拒。所以 `on` 档下：
//	      - **发起者本人**有未完成的 `[hell quest]` ⇒ 仍然拒绝（`{0, 0xAD, 0}`）：
//	        绝不放一个没资格的角色进深渊房间（现场缺陷：改动前这条被 blocked 投影
//	        吞成了"照常进图"）；
//	      - 发起者本人合规 ⇒ 照常进图（深渊房间由此产生），队伍里其他不合规槽位
//	        只进 blocked 列表（厂商逐槽位口径，不整队拒绝）。
//	    实现见 enforceHellPartyRequesterQuestGate。
//	    （dungeon_hell_blocked_slots.go 里"不再因为深渊准入拒绝进图"那句，现在只描述
//	    投影层本身；该文件在禁改名单上，准入口径一律以本文件为准。）
//
// `[hell quest]` 为空（整段缺失，或段里一个有效任务号都没有）= **门槛视为已满足**，
// 不是"必须拒绝"：`Game\Dungeon\DungeonEntryCostService.cs:507-508`
// `if (area.HellQuestIds.Count == 0) return true;`。`on` 档因此照常进图，
// `pvf_status` 会写明到底是"缺段落"还是"段里没有有效任务号"。默认档（blocked off）
// 那一支的第 3 条拒绝理由是"本版没有深渊运行时"，它不含"没有任务门槛"这一概念，
// 所以默认档下这类区域仍然照旧拒绝 —— 两个档位的差别就在这一行。
const hellPartyEntryEnv = "DNFBRIDGE_HELL_PARTY_ENTRY"

const (
	hellPartyEntryValueVendor = "vendor"
	hellPartyEntryValueLegacy = "legacy"
)

// hellPartyEntryMode 是 hellPartyEntryEnv 的归一化取值。
type hellPartyEntryMode int

const (
	// hellPartyEntryVendor 是默认档（零值）：校验前置条件并回拒绝包。
	hellPartyEntryVendor hellPartyEntryMode = iota
	// hellPartyEntryLegacy = 改动前行为（不校验、不回包）。
	hellPartyEntryLegacy
)

func (mode hellPartyEntryMode) String() string {
	if mode == hellPartyEntryLegacy {
		return hellPartyEntryValueLegacy
	}
	return hellPartyEntryValueVendor
}

// enforces 表示是否校验深渊派对前置条件并回拒绝包。
func (mode hellPartyEntryMode) enforces() bool {
	return mode != hellPartyEntryLegacy
}

// readHellPartyEntryMode 解析 DNFBRIDGE_HELL_PARTY_ENTRY。
// **默认 vendor**：空/未设置/非法值都取 vendor；只有显式写 `legacy` 才回退。
func readHellPartyEntryMode(value string) hellPartyEntryMode {
	if strings.EqualFold(strings.TrimSpace(value), hellPartyEntryValueLegacy) {
		return hellPartyEntryLegacy
	}
	return hellPartyEntryVendor
}

// hellPartyEntryGate 返回当前 Service 的归一化档位（零值 options 的测试/探针
// 路径 = vendor = 默认档）。
func (s *Service) hellPartyEntryGate() hellPartyEntryMode {
	if s == nil {
		return hellPartyEntryVendor
	}
	return s.options.hellPartyEntry
}

// 拒绝码。取值来自 A21 参考实现
// `Network/Builders/Dungeon/DungeonAdmissionRejectBuilder.cs:16-41` 的
// `ResolveErrorCode`；包体形状与本仓已上线的 `{0,14,0}` 同形。
const (
	// currentSelectDungeonUnavailableReason = DungeonUnavailable。
	currentSelectDungeonUnavailableReason byte = 0x01
	// currentSelectDungeonMissingPermissionReason = MissingPermission
	//（A21 用它覆盖 `MemberEntryLimitReached` 与 `MissingPermission` 两种）：
	// 这里的语义就是"前置任务没做完，没资格"。
	currentSelectDungeonMissingPermissionReason byte = 0xad
)

// hellPartySelectRequested 判定这一条 op16 是不是"深渊派对"请求。
//
// A21 的字段名给了唯一可用判据：`HellPartyRequestFlag => Flag1`（body[5]），
// 本仓 `EntryOption = u16 @ body[5:7]`，所以非 0 即"带深渊派对标志"。
// 普通选图请求（现场实测 entry_option=0）不受影响。
func hellPartySelectRequested(request dungeoncmd.SelectDungeonRequest) bool {
	return request.EntryOption != 0
}

// hellPartyEntryDecision 是一次深渊派对进入判定的结果。
type hellPartyEntryDecision struct {
	// HellParty = 这条 op16 带深渊派对标志（不带就完全走原路径）。
	HellParty bool
	// Intercepted = 本闸门已经做出裁决，调用方必须回包并结束，不再往下走。
	Intercepted bool
	// AllowEntry = 允许继续进图（legacy 档；或 vendor/on 档且发起者本人合规）。
	AllowEntry bool
	// FailureReason 是 `{0, reason, 0}` 里的 reason 字节。
	FailureReason byte
	// Reason 是给日志看的稳定判据串。
	Reason string
	// AreaID / RequiredQuestIDs / MissingQuestID 是取证字段。
	AreaID           int64
	RequiredQuestIDs []int64
	MissingQuestID   int64
	// BlockedSlots 是逐槽位判定出的"不合规槽位"（升序，只在
	// DNFBRIDGE_HELL_BLOCKED_SLOTS=on 档出现）。它是**纯 UI 投影**：厂商
	// `DungeonEntryHandler.cs:1395-1421` 只把它写进进本选择页下发体，
	// **不授权** —— 发起者本人不合规时照样 Intercepted（见
	// enforceHellPartyRequesterQuestGate）。见 dungeon_hell_blocked_slots.go。
	BlockedSlots []uint16
	// PvfStatus 是这一区域深渊门槛的读取状态（可 grep，只在 on 档出现）。
	PvfStatus string
}

// evaluateHellPartyDungeonEntry 校验"深渊派对"进图前置条件。
//
// 结论只有三种：
//
//  1. 不是深渊派对请求 → `{HellParty:false}`，调用方原样继续；
//  2. legacy 档 → `{HellParty:true, AllowEntry:true}`，原样交给旧路径（静默丢弃）；
//  3. vendor 档 → 一律 `Intercepted`，并且一定给出 `FailureReason`：
//     - `[hell dungeon] != 1`：这个区域根本不是深渊区域 → DungeonUnavailable；
//     - `[hell quest]` 里有未完成的 → MissingPermission（现场那条报障）；
//     - 前置任务都做完了：本版**没有**深渊房间/封印门/深渊怪/深渊掉落的运行时
//     （`worldmap` 只把 `[hellparty]` 挂在 special passive object 上做元数据），
//     所以仍然拒绝（DungeonUnavailable）—— 但一定是"客户端收得到的拒绝"，
//     而不是把它挂在选图界面。
//
// `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 档多出两步（见 evaluateHellEligibilityPerSlot 与
// enforceHellPartyRequesterQuestGate）：
//
//  4. on 档的**逐槽位投影** → `AllowEntry`：逐槽位判定，不合规槽位写进
//     `BlockedSlots` 并随进本选择页下发；投影本身不拒绝任何一次进图；
//  5. on 档的**发起者准入**（只在 `clientRequest=true` 时）：投影之上再过一遍厂商
//     admission 层那道"发起者本人"闸门：有未完成的 `[hell quest]` 就 `Intercepted`
//     （MissingPermission），合规才把请求交给普通进图链（第 57 条的"必回包"因此只会
//     更强：op16 成功体照发，绝不会退回静默丢弃）。
//
// `clientRequest` 是"这一条 op16 到底是谁发的"：客户端自己发的那一条为 true；队长带
// 队友进图时，服务端把队长那条 op16 **内部扇出**给每个队友会话（`prepared != nil`，
// 见 `dungeon_navigation_runtime.go` 的 `handleDungeonSelectUpperPlanned(follower,
// body, member, false)`），那些会话不是发起者 —— 它们照旧只做逐槽位投影（队友自己那条
// op27 里也要带 blocked 槽位），绝不能被队友自己的任务状态当成"发起者"拒绝，
// 否则一个没做完任务的队友会把整队的进图序列从中间打断。
func (s *Service) evaluateHellPartyDungeonEntry(
	ctx context.Context,
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
	clientRequest bool,
) hellPartyEntryDecision {
	if !hellPartySelectRequested(request) {
		return hellPartyEntryDecision{}
	}
	decision := hellPartyEntryDecision{HellParty: true}
	if !s.hellPartyEntryGate().enforces() {
		decision.AllowEntry = true
		decision.Reason = "hell_party_gate_legacy_mode"
		return decision
	}
	if s.hellBlockedSlotsGate().deliversBlockedSlots() {
		// 顺序不能反：先把逐槽位投影（blocked 列表 + 缓存 + 日志）做出来，
		// 再由发起者闸门决定这次进图到底放不放行。投影永远不被"降级"成放行。
		projection := s.evaluateHellEligibilityPerSlot(ctx, session, request)
		if !clientRequest {
			return projection
		}
		return s.enforceHellPartyRequesterQuestGate(ctx, session, projection)
	}
	decision.Intercepted = true
	decision.FailureReason = currentSelectDungeonUnavailableReason
	decision.Reason = "hell_party_dungeon_unavailable"

	if session == nil || session.selectedCharacterID == 0 {
		decision.Reason = "hell_party_selected_character_unavailable"
		return decision
	}
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		decision.Reason = "hell_party_world_map_unavailable"
		return decision
	}
	area, found := table.FindAreaByDungeon(int64(request.DungeonID))
	if !found {
		decision.Reason = "hell_party_area_not_indexed"
		return decision
	}
	decision.AreaID = area.ID
	if area.HellDungeon.Value != 1 {
		decision.Reason = "hell_party_area_is_not_hell_dungeon"
		return decision
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, _, err := s.dungeonQuestRecord(ctx, session.selectedCharacterID)
	if err != nil {
		decision.Reason = "hell_party_quest_record_unavailable"
		return decision
	}
	_, cleared := dungeonQuestStateSets(record)
	required := make([]int64, 0, len(area.HellQuestIDs))
	for _, questID := range area.HellQuestIDs {
		if questID <= 0 {
			continue
		}
		required = append(required, questID)
		if _, ok := cleared[questID]; !ok {
			decision.FailureReason = currentSelectDungeonMissingPermissionReason
			decision.Reason = "hell_party_quest_required"
			decision.MissingQuestID = questID
			decision.RequiredQuestIDs = required
			return decision
		}
	}
	decision.RequiredQuestIDs = required
	// 前置任务全部完成，但本版没有深渊派对的副本运行时（深渊房间/封印门/深渊怪）。
	// 仍然拒绝，但必须是"客户端收得到的拒绝"，并且把原因写清楚，便于以后接运行时。
	decision.Reason = "hell_party_runtime_not_supported_by_this_build"
	return decision
}

// enforceHellPartyRequesterQuestGate 是 `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 档的**最终
// 准入裁决**：在逐槽位投影（纯 UI）之上，把厂商 admission 层那道"**发起者本人**"的
// 闸门补回来。投影绝不授权 —— 现场缺陷就是"投影被当成了放行"。
//
// 厂商依据（同一份源码里两处**独立**调用点，本仓照抄同一结构）：
//
//	Game\Dungeon\DungeonEntryAdmissionApplicationService.cs:233-250
//	  TryPrepareHellParty:
//	    area?.HellDungeon != true                        ⇒ Fail(Unavailable)
//	    !CheckHellQuestRequirement(lease.CharacterId,    ⇒ Fail(MissingPermission)
//	        area, out missingQuestId)                       "hell quest not cleared quest=…"
//
//	Network\Handlers\Dungeon\DungeonEntryHandler.cs:1395-1421
//	  BuildHellPartySelectionState: 只把不合规槽位 Add 进 state.BlockedSlots +
//	  state.BlockReasons（随后随进本选择页下发），**不做任何拒绝/放行决定**。
//
// 判据与逐槽位投影**同源**：`dungeonQuestRecord`（权威任务记录，落库 `dnf_quest_states`）
// + `dungeonQuestStateSets` 的 cleared 集；绝不采信客户端说法，也不新造第二套口径。
//
// 触发条件只有一条：**区域确实声明了深渊任务门槛**
// （`decision.RequiredQuestIDs` 非空 —— 它是 `resolveHellQuestRequirement` 在
// `Gate=true` 那一支里的唯一产出）。以下四种"读不到"一律不拦，与
// dungeon_hell_blocked_slots.go 的 `resolveHellQuestRequirement` / `pvf_status` 口径
// 逐条一致：区域没解析到、`[hell dungeon] != 1`、没有 `[hell quest]` 段、
// 段里没有有效任务号。最后两种的厂商依据是
// `Game\Dungeon\DungeonEntryCostService.cs:507-508`
// `if (area.HellQuestIds.Count == 0) return true;` —— **没有任务号 = 门槛已满足**。
//
// 任务记录读不到时是 **fail-closed**（拒绝、`Unavailable`），与默认档 vendor 路径的
// `hell_party_quest_record_unavailable` 同一口径：无法证明资格就不放人进深渊房间。
// （注意别和逐槽位投影的 fail-open 混起来：那一层只影响客户端 UI 提示，误报比漏报糟；
// 这一层决定真的要不要开一个深渊房间，漏报比误报糟。）
func (s *Service) enforceHellPartyRequesterQuestGate(
	ctx context.Context,
	session *gameSession,
	decision hellPartyEntryDecision,
) hellPartyEntryDecision {
	// 不是深渊派对请求，或这一区域根本没有深渊任务门槛：一个字节都不拦。
	if !decision.HellParty || len(decision.RequiredQuestIDs) == 0 {
		return decision
	}
	refuse := func(reason string, failureReason byte) hellPartyEntryDecision {
		decision.Intercepted = true
		decision.AllowEntry = false
		decision.FailureReason = failureReason
		decision.Reason = reason
		return decision
	}
	if session == nil || session.selectedCharacterID == 0 {
		return refuse("hell_party_selected_character_unavailable", currentSelectDungeonUnavailableReason)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, _, err := s.dungeonQuestRecord(ctx, session.selectedCharacterID)
	if err != nil {
		return refuse("hell_party_quest_record_unavailable", currentSelectDungeonUnavailableReason)
	}
	_, cleared := dungeonQuestStateSets(record)
	for _, questID := range decision.RequiredQuestIDs {
		if _, ok := cleared[questID]; ok {
			continue
		}
		// 现场那条报障：没通关 `[hell quest]` 的角色点了「挑战深渊派对」。
		// 这里必须拒绝（`{0, 0xAD, 0}`），而不是把"不合规"降级成 UI 提示。
		decision.MissingQuestID = questID
		return refuse("hell_party_quest_required", currentSelectDungeonMissingPermissionReason)
	}
	// 发起者本人合规：保留逐槽位投影（其他槽位照旧进 blocked 列表），照常进图。
	return decision
}

// evaluateHellEligibilityPerSlot 是 `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 档的裁决：
// **逐槽位判定 + blocked 槽位下发 + 照常进图**（操作者清单第 69 条）。
//
// 与默认档的三处差别，逐条对齐厂商（`DungeonEntryHandler.cs:1465-1500` 的
// `ENTER_SELECT_DUNGEON hell eligibility`）：
//
//  1. 判据是**每个槽位各自的**任务完成状态，不是"发起者过不过"；
//  2. 不合规只进 `BlockedSlots`（本仓 = 会话缓存 → class0/op27 的 `List0`），
//     **不拒绝任何一次进图**（既不整队拒绝也不拒发起者）：请求照常落到普通进图链，
//     也就是说点了「挑战深渊派对」真的进得去副本；
//  3. 区域没有深渊门槛（`[hell dungeon] != 1` / 没有 `[hell quest]` 段 / 段里没有任务号）
//     时一个槽位都不 block，并把 `pvf_status` 记清楚。
func (s *Service) evaluateHellEligibilityPerSlot(
	ctx context.Context,
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
) hellPartyEntryDecision {
	mode := s.hellBlockedSlotsGate().String()
	decision := hellPartyEntryDecision{
		HellParty:  true,
		AllowEntry: true,
	}
	requirement := hellQuestRequirement{
		PvfStatus: hellPVFStatusAreaUnresolved,
		Reason:    "hell_party_area_not_indexed",
	}
	switch {
	case session == nil || session.selectedCharacterID == 0:
		requirement.Reason = "hell_party_selected_character_unavailable"
	default:
		table, _, err := s.dungeonWorldMap()
		switch {
		case err != nil:
			requirement.Reason = "hell_party_world_map_unavailable"
		default:
			area, found := table.FindAreaByDungeon(int64(request.DungeonID))
			// 读不到的段落/区域一律按"该区域没有深渊门槛"处理（不拦），
			// 但 resolveHellQuestRequirement 会把到底是哪一种写进 pvf_status。
			requirement = resolveHellQuestRequirement(area, found)
		}
	}
	evaluation := s.evaluateHellEligibility(ctx, session, requirement)
	// 整体覆盖缓存：这次判定之后，下发体里的 blocked 列表只可能来自这一次判定。
	recordHellBlockedSlots(session, evaluation)
	for _, blocked := range evaluation.Blocked {
		s.logHellEligibilityBlocked(session, evaluation, blocked, mode, "dungeon_select_hell_party")
	}
	s.logHellEligibilityEvaluated(session, evaluation, mode, "dungeon_select_hell_party")

	decision.AreaID = requirement.AreaID
	decision.RequiredQuestIDs = requirement.RequiredQuestIDs
	decision.PvfStatus = requirement.PvfStatus
	decision.BlockedSlots = hellBlockedSlotsOf(evaluation)
	decision.Reason = evaluation.Reason
	if evaluation.RequesterBlocked {
		for _, blocked := range evaluation.Blocked {
			if blocked.CharacterID != session.selectedCharacterID {
				continue
			}
			decision.MissingQuestID = blocked.MissingQuest
			break
		}
	} else if len(evaluation.Blocked) > 0 {
		decision.MissingQuestID = evaluation.Blocked[0].MissingQuest
	}
	return decision
}

// hellBlockedSlotsOf 把判定结果里的 blocked 槽位提成下发体要用的 u16 列表（升序）。
func hellBlockedSlotsOf(evaluation hellEligibilityEvaluation) []uint16 {
	if len(evaluation.Blocked) == 0 {
		return nil
	}
	slots := make([]uint16, 0, len(evaluation.Blocked))
	for _, blocked := range evaluation.Blocked {
		slots = append(slots, blocked.Slot)
	}
	sortUint16Ascending(slots)
	return slots
}

// refuseHellPartyDungeonEntry 把裁决结果回给客户端。
//
// 关键点：**必须回包**。旧路径在 `dungeon_owner_validation_failed` 上是静默
// `return nil`，这正是"卡在地下城选择界面"的直接原因。
func (s *Service) refuseHellPartyDungeonEntry(
	session *gameSession,
	request dungeoncmd.SelectDungeonRequest,
	decision hellPartyEntryDecision,
) error {
	if session == nil {
		return nil
	}
	fields := []any{
		"char_id", session.selectedCharacterID,
		"dungeon_id", request.DungeonID,
		"difficulty", request.Difficulty,
		"hell_party_request_flag", request.EntryOption,
		"area_id", decision.AreaID,
		"required_hell_quest_ids", formatQuestIDList(decision.RequiredQuestIDs),
		"missing_hell_quest_id", decision.MissingQuestID,
		"reason", decision.Reason,
		"failure_reason_byte", decision.FailureReason,
		// 两个现场判读位：模式告诉你"这次拒绝是默认档还是 on 档的发起者闸门"，
		// pvf_status 告诉你"区域门槛读到没有"（默认档不做逐槽位判定，故为空）。
		"hell_blocked_slots_mode", s.hellBlockedSlotsGate().String(),
		"requester_quest_gate", "a21_try_prepare_hell_party_requester_only",
		"failure_body_source", "a21_dungeon_admission_reject_0_success_1_reason_2_context",
	}
	if decision.PvfStatus != "" {
		fields = append(fields, "pvf_status", decision.PvfStatus)
	}
	s.logGameEvent(session, "game-dungeon-hell-party-entry-blocked", fields...)
	return s.sendSelectDungeonFailure(session, decision.FailureReason)
}

// formatQuestIDList 把 [hell quest] 列表写成日志里可读的一段。
func formatQuestIDList(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, ",")
}
