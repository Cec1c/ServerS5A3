package dnfbridge

// 本文件修的是**登录/选角路径**上的一处容器下发缺口：list 3（已穿戴 / 装备栏）
// 从来没有作为**容器**下发给客户端，于是客户端的装备栏视图是空的，而它随后会用
// "登录复述穿戴态"那一族 op19 去补，服务端只能靠 equip_login_wear* 的兜底闸门
// 一条条挡；挡住的 noop 又什么都不告诉客户端 ⇒ 客户端永远收敛不了。
//
// ── 一、现场事实（国内服 109.244.63.71，`/opt/dnf90/logs/packet_log.txt`，只读取证）
//
//  1. list 3 **从未**以容器（class0/op13, msg_id=13）下发过：
//     `game-upper-select-inventory-bootstrap-send` 137 次 × 恰好 6 个 list
//     `{0,1,2,7,12,38}`（各 137 次，`list_type=3` 次数为 0）；
//     `game-upper-current-item-list-refresh-send` 41 次 × 恰好 5 个 list
//     `{0,1,2,7,38}`（同样没有 3）。
//     list 3 的全部下行只有**逐槽 op14**：`game-current-socket-equipment-slot-refresh` 75、
//     `game-upper-current-equipped-creature-item-refresh-send` 108、
//     `game-aligned-command-item-slot-refresh-send` 2（list_type=3）等。
//     ⇒ 客户端那份 list 3 管理器**没有任何权威行**，装备栏因此空白。
//
//  2. 客户端的"已穿戴"只从 `userinfo` mode1 的 create 行拿到
//     （`userinfo_current_equipment_projection.go:178`
//     `game-upper-selected-userinfo-mode1-equipment-create-rows`，mcp_handler=sub_1D77560，
//     13:24:19Z 那次角色 6 `entry_count=30`）。那批行创建的是**本地 actor 的装备对象**
//     （纸娃娃/模型，所以截图里"武器还在手上"），**不是** list 3 的物品栏行。
//     ⇒ "模型对、装备栏空"这个组合正是"对象建了、容器没建"的签名。
//
//  3. 客户端随后自己补：13:24:21.465Z `RECV type=19 body_len=28 ... src=(0,16,IV=0)
//     dst=(3,12,IV=0x0605C16D) item=101040493`；12:29:27-28Z 的 conn game-000003 更是一口气
//     复述了 5 个穿戴槽（(0,12)->(3,18)、(0,13)->(3,14)、(0,14)->(3,15)…），
//     点名的背包源槽是**递增的假槽位**且 `SourceInstanceValue=0`
//     —— 客户端手里没有那个背包对象，它是在"把已穿戴物品搬进装备栏"。
//
//  4. ⚠️ 服务端回包自述与实际的矛盾（必须说清）：
//     `internal/modules/dnf/inventory/handler.go:1138` 的 reason 固定写着
//     `opened current-EXE op19 ACK then authoritative item containers and selected actor
//     mode0 appearance refresh`，但它在 noop 分支加的后置动作是
//     `PostActionRefreshSelectedWearContainers`（`handler.go:1118`），而桥侧默认档把它整段压掉
//     （`game_aligned.go:705-717`）。线上原文（13:27:34.013Z）：
//     `EVENT kind=game-aligned-command-post-action-send operation=move_itemspace
//     post_action=refresh_selected_wear_containers char_id=6 msg_id=13 classification=0
//     list_types=[] sequence=suppressed_no_full_container_replay_after_wear_move`。
//     ⇒ **noop 分支一个物品容器都没发**；reason 里那句 "authoritative item containers"
//     是这条动作还没被开关压掉时写的陈话。两个开关各管一处：
//     `DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH` 管的是**穿脱路径的整容器动作**（本轮一个字不动），
//     本文件的开关管的是**登录引导/登录 noop**这两条只有登录才走的路径。
//
// ── 二、changlog.md 里"为什么当初压掉"的原文与scope（本轮逐行核对）
//
//	changlog.md:9986
//	  "Slot-26 equip, swap, and unequip therefore keep `ACK -> mode0 -> op105 -> optional op102`
//	   but no longer replay item-list op13 for lists `0/1/2/7/38`, which rebuilt the list-7
//	   manager and discarded the visible equipped-slot object."
//	changlog.md:1206-1211
//	  "而 op19 后的 post action 顺序把**五个整容器回放**排在前、slot-26 附魔行排在后"
//	  "而普通装备/时装 op19（容器回放已被 `EQUIP_MOVE_CONTAINER_REFRESH=suppressed` 压掉）41-94 ms"
//	changlog.md:4090-4093
//	  "**穿戴时装（条目 10）经查早已修复**（2026-09-25 19:57，`equip_move_refresh.go` 等）"
//	  "⚠️ 该修复的开关 `DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH`（默认 `suppressed`）"
//
// 三条**全部**是"**op19 之后**"的语境：一条是宠物槽 26 的原生 op19 已经把对象搬走、
// 紧接着的 op13 整容器重建了 list-7 管理器才把对象丢掉；另外两条分别量化同一次修复的收益
// 与 op19 之后的投影顺序。它们**没有**任何一句涉及登录引导（角色绑定后、mode1 之前），
// 那里根本不存在"in-flight native op19"，因此这两条路径必须分开看：
//
//   - 登录引导（本文件的 A 段）：写入时机在**客户端第一次发 op19 之前**
//     （`select_character_inventory_bootstrap.go:24-28` 明写 "after the scene has created and
//     bound the selected actor, and before the full equipment-bearing mode1 packet"），
//     用的就是另外六个容器**同一台** op13 机器 ⇒ 这里补 list 3 不触碰上述任何结论。
//   - 穿脱路径：一个字不改（见 D 段开关表与 `equip_move_container_refresh.go`）。
//
// ── 三、改动（两处，各自独立开关 + 一键回退）
//
//	A. 登录/选角引导的容器清单加上 list 3（默认档），走的是既有 op13 容器机器
//	   （`buildCurrentItemListBodyForSession(listType=3)` +
//	    `sendCurrentSceneItemList(msg_id=13)`），不新增任何包格式：
//	   `DNFBRIDGE_LOGIN_EQUIPMENT_CONTAINER=authoritative`（默认）| `suppressed`（回退）。
//
//	B. 登录复述 noop 的权威收敛：命中那族 op19、窗口内、且 ACK 的 moveValue==0 时，
//	   把客户端点名的**两个端点**各补一条权威逐槽行——
//	     源端点（list 0/1，客户端指名的背包槽，多为 0xFFFFFFFF/空行）
//	       → `sendCurrentSocketInventorySlotUpdate`（线上已跑通 22 次，msg_id=14）；
//	     装备端点（list 3 槽）→ 仓储里有这件才发（0x77 单行，与
//	       `sendCurrentSocketEquipmentSlotUpdate` 的"有货"分支逐字节同形，线上 75 次）。
//	   装备槽在仓储里为空时**不发空行**（list 3 的空行形状没有现场证据，不发没证据的包）。
//	   `DNFBRIDGE_LOGIN_EQUIPMENT_SLOT_REFRESH=on`（默认）| `off`（回退）。
//
// 	  为什么它在登录路径安全、又不碰穿脱路径：
//	    · 只在 `loginWearWindowOpen`（`equipLoginWearFamilyWindowOpenForOp19` 判定为
//	      "登录复述穿戴态"那一族、且落在该槽权威纪元内）成立时才规划；
//	    · 只在 ACK 的 moveValue==0（= 真 noop，一个字节都没落库）时才发：
//	      没有原生 op19 搬过对象，所以 changlog.md:9986 那条"把刚放好的对象丢掉"的前提不存在；
//	    · 用的是**逐槽 op14**（厂商 `SendUpdateItemList(Equipment, slots)` 同形），
//	      不是 op13 整容器 ⇒ 不会重建客户端的管理器；
//	    · 真实穿脱（moveValue!=0）与所有非登录窗口的 op19：本文件一行代码都不执行，
//	      穿脱路径的 `equipMoveContainerRefresh=suppressed` 行为逐字节不变（默认与回退档都是）。
//
// ── 四、开关表（都在消费点自读 env，不进 options.go）
//
//	DNFBRIDGE_LOGIN_EQUIPMENT_CONTAINER=authoritative（默认，含未设置）
//	    登录/选角引导的七个容器 = {0,1,2,7,12,38,3}，list 3 排在最后、仍在 item-lock
//	    快照与完整 mode1 之前；事件 `game-login-equipment-container-sent list=3 rows=N switch=authoritative`。
//	suppressed（及 off/0/false/no/legacy/disable/disabled/none）
//	    一键回退到 2026-09-28 之前：六个容器，一个字节都不多
//	    （事件 `game-login-equipment-container-suppressed`）。
//
//	DNFBRIDGE_LOGIN_EQUIPMENT_SLOT_REFRESH=on（默认，含未设置）
//	    登录 noop 补两端点权威行；事件 `game-login-equipment-slot-refresh-sent ... switch=on`。
//	off（及 0/false/no/legacy/disable/disabled/none）
//	    一键回退到今天：noop 只回一条 msg_id=19 的 ACK，不补任何行
//	    （事件 `game-login-equipment-slot-refresh-suppressed ... switch=off`）。
//
// 两个回退字面量一起写 = 线上今天的下行逐字节。

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

const (
	// loginEquipmentContainerEnv 是"登录引导是否建立 list 3（已穿戴）容器"的开关名。
	loginEquipmentContainerEnv = "DNFBRIDGE_LOGIN_EQUIPMENT_CONTAINER"
	// loginEquipmentSlotRefreshEnv 是"登录复述 noop 是否补两端点权威行"的开关名。
	loginEquipmentSlotRefreshEnv = "DNFBRIDGE_LOGIN_EQUIPMENT_SLOT_REFRESH"

	loginEquipmentContainerAuthoritative = "authoritative"
	loginEquipmentContainerSuppressed    = "suppressed"
	loginEquipmentSlotRefreshOn          = "on"
	loginEquipmentSlotRefreshOff         = "off"
)

// loginEquipmentContainerAuthoritativeEnabled 回答"登录引导要不要把 list 3 也建出来"。
// 只有**显式**写 suppressed（及其别名）才回退；其余取值（含未设置、authoritative、
// 写错的字串）都是修复档 —— 默认即修复，回退必须显式（与
// equipLoginWearReassertNoopRefreshEnabled 同一取舍）。
func loginEquipmentContainerAuthoritativeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginEquipmentContainerEnv))) {
	case loginEquipmentContainerSuppressed, "off", "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// loginEquipmentContainerSwitch 只用于事件/诊断。
func loginEquipmentContainerSwitch() string {
	if loginEquipmentContainerAuthoritativeEnabled() {
		return loginEquipmentContainerAuthoritative
	}
	return loginEquipmentContainerSuppressed
}

// loginEquipmentSlotRefreshEnabled 回答"登录复述 noop 要不要补两端点权威行"。
// 只有**显式**写 off（及其别名）才回退；其余取值（含未设置、on、写错的字串）都是修复档。
func loginEquipmentSlotRefreshEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginEquipmentSlotRefreshEnv))) {
	case loginEquipmentSlotRefreshOff, "0", "false", "no", "legacy", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// loginEquipmentSlotRefreshSwitch 只用于事件/诊断。
func loginEquipmentSlotRefreshSwitch() string {
	if loginEquipmentSlotRefreshEnabled() {
		return loginEquipmentSlotRefreshOn
	}
	return loginEquipmentSlotRefreshOff
}

// loginEquipmentBootstrapListTypes 返回登录/选角引导**实际**要建立的容器清单。
// 默认档 = `currentSelectInventoryBootstrapListTypes`（末项就是 list 3）；
// 回退档 = `currentSelectInventoryBootstrapListTypesRollback`（今天的六个，顺序不变）。
func loginEquipmentBootstrapListTypes() []byte {
	if loginEquipmentContainerAuthoritativeEnabled() {
		return currentSelectInventoryBootstrapListTypes
	}
	return currentSelectInventoryBootstrapListTypesRollback
}

// loginEquipmentNoopConvergence 描述"登录复述 noop"这一次要补的权威行。
type loginEquipmentNoopConvergence struct {
	planned     bool
	sourceList  byte
	sourceSlot  int16
	hasSource   bool
	wornSlot    int16
	hasWorn     bool
	switchValue string
}

// alignedMoveAckIsNoop 从模块返回的响应里读 op19 ACK 的 moveValue。
//
// 形状来自 `internal/modules/dnf/inventory/body.go:87-98`
// （`01 | srcList(u8) | srcSlot(i16) | moveValue(i32) | dstList(u8) | dstSlot(i16)`，共 11 字节），
// 而 `handler.go:1101-1104` 在 `result.Mode == "noop"` 时把 moveValue 写成 0。
// 因此这里**不需要解析任何 reason 文本**：moveValue==0 就是 noop。
func alignedMoveAckIsNoop(result alignedcmd.Result) bool {
	want := uint16(dnfenum.CmdPacketMoveItemspace)
	for _, response := range result.UpperResponses {
		if response.MsgID != want || len(response.Body) != 11 || response.Body[0] != 1 {
			continue
		}
		return int32(binary.LittleEndian.Uint32(response.Body[4:8])) == 0
	}
	return false
}

// planLoginEquipmentNoopConvergence 判定这一次 op19 要不要补两端点权威行，以及补哪两格。
//
// 它**不读库、不发包**，只做形状与档位判定，判据全部来自客户端自己写的字段
// （复用 `dnfinventory.DecodeMoveItemspaceRequest`，与
// `equip_login_wear_family_window.go` 的形状闸门同一份解码器）。
func (s *Service) planLoginEquipmentNoopConvergence(
	session *gameSession,
	body []byte,
	loginWearWindowOpen bool,
	result alignedcmd.Result,
) loginEquipmentNoopConvergence {
	if session == nil {
		return loginEquipmentNoopConvergence{}
	}
	// 2026-09-28 第五轮：**方向判定本身**（equip/worn_slot_reassert_gate.go 的
	// worn_slot_reassert / worn_slot_stale_claim）也会产生"一个字节都没写、但客户端那份
	// 视图需要被权威状态纠正"的 noop，而它**与窗口无关**（窗口各层放行之后由形状判据接管）。
	// 这类 noop 的客户端手里往往正握着一份幻影副本（以为背包那一格还有一件、或以为穿戴格
	// 还穿着），所以端点权威行必须在窗口之外也能补：
	//   - 窗口开：与改动前逐字节相同（下面的档位/形状/noop 三条判据一字未动）；
	//   - 窗口关：只有当这条 op19 是**该族复述形状**（源端 list 0/1 + 实例 0、
	//     目的端 list 3 + 实例 != 0 —— 复用 equip_login_wear_family_window.go 的
	//     形状作用域，同一份判据）**且** ACK 的 moveValue==0 时才规划。
	//     这个组合只可能由方向判定产生：identity_echo 要求源端实例 > 0、
	//     duplicate_endpoint_identity 要求两端同名且非零、身份矛盾闸门要求窗口打开。
	//     因此这里不必解析 reason 串，也不会把别的 noop 拉进来。
	if !loginWearWindowOpen && !(loginWearReassertFamilyOp19Shape(body) && alignedMoveAckIsNoop(result)) {
		// 普通 op19（真穿脱、同容器搬运、宠物搬运…）：本文件完全不介入。
		return loginEquipmentNoopConvergence{}
	}
	if !loginEquipmentSlotRefreshEnabled() {
		s.logPacketEvent("game-login-equipment-slot-refresh-suppressed",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"operation", result.Operation,
			"switch", loginEquipmentSlotRefreshSwitch(),
			"switch_env", loginEquipmentSlotRefreshEnv,
			"reason", "rollback_literal_restores_today")
		return loginEquipmentNoopConvergence{}
	}
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	if err != nil {
		return loginEquipmentNoopConvergence{}
	}
	if request.DestinationListType != currentSocketListEquipment {
		return loginEquipmentNoopConvergence{}
	}
	if !alignedMoveAckIsNoop(result) {
		return loginEquipmentNoopConvergence{}
	}
	out := loginEquipmentNoopConvergence{
		planned:     true,
		wornSlot:    request.DestinationSlotIndex,
		hasWorn:     request.DestinationSlotIndex >= 0,
		switchValue: loginEquipmentSlotRefreshSwitch(),
	}
	if (request.SourceListType == loginWearFamilySourceListMain ||
		request.SourceListType == loginWearFamilySourceListAvatar) &&
		request.SourceSlotIndex >= 0 {
		out.sourceList = request.SourceListType
		out.sourceSlot = request.SourceSlotIndex
		out.hasSource = true
	}
	return out
}

// sendLoginEquipmentNoopSlotRows 在 op19 ACK 之后补两条权威逐槽行：
//
//	源端点（客户端指名的 list 0/1 槽）—— 仓储里没有就是空行，这正是"那个槽是空的"
//	  这句客户端唯一缺的话（现场 src=(0,16,IV=0)）；
//	装备端点（list 3 槽）—— 仓储里那件物品的权威 0x77 行。
//
// 两条都用既有 sender，不新增任何下行格式。失败只记日志：这是一次投影补发，
// 不能把已经成功的 ACK 变成错误（与"投影延后不阻断已提交操作"的既有口径一致）。
func (s *Service) sendLoginEquipmentNoopSlotRows(
	session *gameSession,
	plan loginEquipmentNoopConvergence,
	source string,
) {
	if s == nil || session == nil || session.selectedCharacterID == 0 || !plan.planned {
		return
	}
	if plan.hasSource {
		if err := s.sendCurrentSocketInventorySlotUpdate(
			session,
			plan.sourceList,
			plan.sourceSlot,
			source+"_source_slot",
		); err != nil {
			s.logWarn("dnfbridge login equipment source slot refresh deferred",
				"conn_id", session.connID,
				"char_id", session.selectedCharacterID,
				"list_type", plan.sourceList,
				"slot", plan.sourceSlot,
				"error", err)
		}
		s.logPacketEvent("game-login-equipment-slot-refresh-sent",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"role", "source_slot",
			"list", plan.sourceList,
			"slot", plan.sourceSlot,
			"rows", 1,
			"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
			"list_type", plan.sourceList,
			"switch", plan.switchValue,
			"switch_env", loginEquipmentSlotRefreshEnv,
			"evidence", "login_wear_noop_nominates_a_source_slot_the_client_has_no_object_for")
	}
	if !plan.hasWorn {
		return
	}
	if err := s.sendLoginEquipmentWornSlotRow(session, plan.wornSlot, source+"_worn_slot"); err != nil {
		s.logWarn("dnfbridge login equipment worn slot refresh deferred",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"list_type", currentSocketListEquipment,
			"slot", plan.wornSlot,
			"error", err)
	}
}

// sendLoginEquipmentWornSlotRow 把该装备槽的权威行发成既有的逐槽 op14 载体
// （`buildCurrentItemUpdateBody` + `sendGameUpperRawClass`，与
// `socket_emblem_refresh.go:93` 的"有货"分支逐字节同形）。
//
// 仓储里该槽为空时**不发**：list 3 的空行形状在现场没有证据，
// 宁可不发也不发一个没证据的包（这个函数只负责"把仓储里的真相说出来"）。
func (s *Service) sendLoginEquipmentWornSlotRow(session *gameSession, slot int16, source string) error {
	if session == nil || session.selectedCharacterID == 0 || slot < 0 {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Equipment == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	equipment, found, err := repos.Equipment.Load(ctx, characterID)
	if err != nil {
		return err
	}
	var entry currentItemListEntry
	occupied := false
	if found {
		for _, equipped := range equipment.Entries {
			if equipped.SlotIndex != slot {
				continue
			}
			if row, rowOK := currentItemListEntryFromEquipment(equipped); rowOK {
				entry = row
				occupied = true
			}
			break
		}
	}
	if !occupied {
		s.logPacketEvent("game-login-equipment-slot-refresh-skipped",
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"role", "worn_slot",
			"list", currentSocketListEquipment,
			"slot", slot,
			"rows", 0,
			"switch", loginEquipmentSlotRefreshSwitch(),
			"switch_env", loginEquipmentSlotRefreshEnv,
			"reason", "equipment_slot_absent_in_repository_no_unproven_empty_list3_row")
		return nil
	}
	body := buildCurrentItemUpdateBody(currentSocketListEquipment, []currentItemListEntry{entry})
	s.logPacketEvent("game-login-equipment-slot-refresh-sent",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"role", "worn_slot",
		"list", currentSocketListEquipment,
		"slot", slot,
		"rows", 1,
		"body_len", len(body),
		"msg_id", uint16(dnfenum.CmdPacketWalkoutPartyMember),
		"list_type", currentSocketListEquipment,
		"entry_size", currentItemListEntryWireSize,
		"switch", loginEquipmentSlotRefreshSwitch(),
		"switch_env", loginEquipmentSlotRefreshEnv,
		"evidence", "vendor_send_update_item_list_equipment_equipment_refresh_slots")
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), body, 0)
}
