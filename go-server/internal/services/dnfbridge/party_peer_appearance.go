package dnfbridge

import (
	"encoding/binary"
	"os"
	"strconv"
	"strings"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件是「队友（组队同屏/同副本）的外观记录里补上克隆装扮链接字段」这条功能的
// 开关、取值点与判读事件。
//
// # 操作者报障（2026-09-28，两个环境）
//
//	①「队长视角：组队能看到（队员的）模型、看到操作、看到技能，但是时装丢失；
//	   武器装扮在通过一个小房间后恢复，时装没有恢复。」
//	②「队员是能看到队长的时装和技能的，和队长视角的不一样。」
//
// # 根因：同一个 19 字节外观行，两条载体写法不一致（已用线上字节证死）
//
// 队友 actor 的外观只有一条载体：`sendDungeonPartyPeerActorState`
// （dungeon_party_copresence.go:403 的 op2 mode0）与城镇同屏的
// `sendTownRemoteActorState`（online_player_manager.go:481）都走
// `buildCurrentSceneObjectListBodyForSessionInContextStrict`
// → `loadCurrentSelectedActorMode0AppearanceSummary`
// （current_actor_mode0_appearance_body.go:22）
// → `buildCurrentActorMode0AppearanceSummaryFromEquipment`
// （current_actor_mode0_appearance.go:60）。
//
// 该投影把克隆件**显示**成被克隆件（`currentActorAppearanceItemID`，
// current_actor_mode0_appearance.go:112），但**从不写**这一行的 LinkItemId
// ——19 字节行（`writeCurrentSceneObjectEquipSummary`，
// character_scene_object_tail.go:190）里它是 `row.OptionalIDOrExpire`，偏移 +10
// （厂商 `Game/Characters/CharacterAppearanceEntry.cs:68-80` 的 `LinkItemId@+14`
// 减去空扩展块 4 字节，见 clone_avatar.go:132-166）。
//
// 名册/名册摘要那条载体（`equipmentRosterSummary`，
// initial_equipment_roster_projection.go:30-65）**是写了的**
// （initial_equipment_roster_projection.go:59）。于是同一个角色、同一件克隆装扮，
// 两条载体给出两个不同的字节：名册那条带链接、队友投影那条恒 0。
//
// # 线上字节取证（109.244.63.71 packet_log.txt，2026-09-28 13:36/13:45 组队会话）
//
// 角色 6（test78，dnf:10011）穿 8 件稀有克隆装扮（穿戴行 item_id
// 42601/42603/42605/42609/42611/42615/42607/42613，Extra
// `clone_avatar_appearance_id` = 101550581/101560738/101570490/101500759/
// 101510923/101540674/101520562/101530518 —— 与真档 [item category] `clear avatar`
// 的 8 件一致，见 clone_avatar_test.go:18-25）。服务端发给**队长**（角色 3）的那条
// `game-dungeon-party-peer-actor-send receiver_char_id=3 actor_char_id=6`
// （mode0_body_len=498）里，15 行外观行的 LinkItemId **全部为 0**：
//
//	slot= 0 display=101550581 link=0
//	slot= 3 display=101500759 link=0   <-- 穿戴行是克隆件 42609
//	slot=10 display=101600023 link=0   <-- 武器装扮，普通件
//	slot=12 display=101040493 link=0   <-- 武器
//	slot=28 display=2747069   link=0   <-- 名片
//
// 反方向 `receiver_char_id=6 actor_char_id=3`（角色 3 = test2，8 件**普通**装扮
// 415550045…，无克隆件）同一个字段也全是 0，但那些行**不需要**链接：显示件就是
// 穿戴件本身。⇒ 「队员看得见队长的时装」而「队长看不见队员的时装」不是两个方向
// 的投影不同，而是**只有穿了克隆装扮的那一侧会暴露这条缺失**。
//
// 换房间/换场景后队友 actor 会被 mode0 重新创建、外观行重发一遍，但**重发的还是
// 同一份缺链接的行**：普通件（武器装扮槽 10 / 武器槽 12）重画得出来，克隆件永远
// 画不出来 —— 这正是「武器装扮过一个小房间恢复、时装不恢复」。
//
// # 修法
//
// 场景对象外观行与名册行用同一个取值点：`cloneAvatarRowLinkItemID`
// （clone_avatar.go:155），于是两条载体逐字段一致，且受既有两个开关约束
// （DNFBRIDGE_CLONE_AVATAR_APPEARANCE / DNFBRIDGE_CLONE_AVATAR_ROW_CLEAR_ID）。
//
// 非克隆行恒 0 ⇒ 普通装扮/装备/武器/称号的字节一个都不变。
//
// 开关：DNFBRIDGE_PEER_APPEARANCE_CLONE_LINK，默认 = 修复档（on）。
// 回退值 off/0/false/no/legacy 时这个取值点恒 0 ⇒ 场景对象载体恢复成今天的
// 逐字节形态（链接字段恒 0），不需要重编译。
const currentPeerAppearanceCloneLinkEnv = "DNFBRIDGE_PEER_APPEARANCE_CLONE_LINK"

// currentPeerAppearanceCloneLinkSetting 解析 DNFBRIDGE_PEER_APPEARANCE_CLONE_LINK。
//
// **默认 on**（修复档）：未设置/空 = 修复行为；只有显式写 off/0/false/no/legacy
// 才回到改动前。bad=true 表示取值非法（已回退到 on），调用方在启动日志里告警。
// 取值方式与同包 cloneAvatarAppearanceSetting（clone_avatar.go:74）一致：
// **每次调用读一次 env**，因此回退只需要改环境变量 + 重启，而且不经过
// optionsFromConfig 的测试/探针构造路径同样受这个开关约束。
func currentPeerAppearanceCloneLinkSetting() (enabled bool, raw string, bad bool) {
	raw = strings.TrimSpace(os.Getenv(currentPeerAppearanceCloneLinkEnv))
	switch strings.ToLower(raw) {
	case "", cloneAvatarAppearanceOn, "1", "true", "yes", "current", "fix", "fixed":
		return true, raw, false
	case cloneAvatarAppearanceOff, "0", "false", "no", cloneAvatarAppearanceLegacy:
		return false, raw, false
	default:
		return true, raw, true
	}
}

// currentPeerAppearanceCloneLinkEnabled 是场景对象外观行链接字段的唯一判据。
func currentPeerAppearanceCloneLinkEnabled() bool {
	enabled, _, _ := currentPeerAppearanceCloneLinkSetting()
	return enabled
}

// currentPeerAppearanceCloneLinkMode 是启动日志里的档位串（on = 修复档 / off = 回退档）。
func currentPeerAppearanceCloneLinkMode() string {
	if currentPeerAppearanceCloneLinkEnabled() {
		return cloneAvatarAppearanceOn
	}
	return cloneAvatarAppearanceOff
}

// currentSceneObjectAppearanceRowLinkItemID 返回场景对象载体（op2 mode0 的
// 19 字节外观行）该写进 LinkItemId 的值：
//
//   - 有克隆来源的克隆装扮 ⇒ 克隆件自己的 ItemID（真正拥有的那件）；
//   - 其余任何情况 ⇒ 0（= 改动前的字节）；
//   - DNFBRIDGE_PEER_APPEARANCE_CLONE_LINK 取回退值时恒 0。
//
// 取值与名册载体共用 cloneAvatarRowLinkItemID，所以两个开关
// （DNFBRIDGE_CLONE_AVATAR_APPEARANCE / DNFBRIDGE_CLONE_AVATAR_ROW_CLEAR_ID）
// 任一个回退都会让本字段一起回到 0，三条载体同时逐字节回到改动前。
func currentSceneObjectAppearanceRowLinkItemID(equipped dnfrepo.EquipmentEntry) uint32 {
	if !currentPeerAppearanceCloneLinkEnabled() {
		return 0
	}
	return cloneAvatarRowLinkItemID(equipped)
}

// currentSceneAppearanceBodyComposition 是一条 op2 mode0 体里外观表的**实际下发内容**，
// 只从字节里读，不查库：判读事件必须描述"线上真正发了什么"。
type currentSceneAppearanceBodyComposition struct {
	Rows      int
	Slots     []int
	LinkRows  int
	EmptyRows int
}

// currentSceneAppearanceBodyCompositionForLog 解析 mode0 体尾部的外观表。
func currentSceneAppearanceBodyCompositionForLog(body []byte) (currentSceneAppearanceBodyComposition, bool) {
	_, tailStart, ok := currentSceneObjectLevelForLog(body)
	if !ok || tailStart >= len(body) {
		return currentSceneAppearanceBodyComposition{}, false
	}
	tail := body[tailStart:]
	if len(tail) <= 6 {
		return currentSceneAppearanceBodyComposition{}, false
	}
	end, ok := currentSceneObjectEquipSummaryEnd(tail, 6)
	if !ok {
		return currentSceneAppearanceBodyComposition{}, false
	}
	var out currentSceneAppearanceBodyComposition
	out.Rows = int(tail[6])
	pos := 7
	for idx := 0; idx < out.Rows; idx++ {
		if pos+currentSceneObjectEquipSummaryRowBytes > len(tail) || pos+currentSceneObjectEquipSummaryRowBytes > end {
			return currentSceneAppearanceBodyComposition{}, false
		}
		row := tail[pos : pos+currentSceneObjectEquipSummaryRowBytes]
		slot := int(row[0])
		item := binary.LittleEndian.Uint32(row[1:5])
		link := binary.LittleEndian.Uint32(row[10:14])
		out.Slots = append(out.Slots, slot)
		if link != 0 {
			out.LinkRows++
		}
		if item == currentActorMode0AppearanceEmptyItem {
			out.EmptyRows++
		}
		pos += currentSceneObjectEquipSummaryRowBytes
	}
	return out, true
}

// currentSceneAppearanceSlotComponents 把外观槽映射成人能读的部件名，供判读事件使用。
// 槽位口径同 currentEquipmentPlacementRuleForPVFType（equipment_placement.go:313）。
func currentSceneAppearanceSlotComponents(slots []int) []string {
	out := make([]string, 0, len(slots))
	for _, slot := range slots {
		out = append(out, currentSceneAppearanceSlotComponent(slot))
	}
	return out
}

func currentSceneAppearanceSlotComponent(slot int) string {
	switch slot {
	case 0:
		return strconv.Itoa(slot) + ":hat"
	case 1:
		return strconv.Itoa(slot) + ":hair"
	case 2:
		return strconv.Itoa(slot) + ":face"
	case 3:
		return strconv.Itoa(slot) + ":coat"
	case 4:
		return strconv.Itoa(slot) + ":pants"
	case 5:
		return strconv.Itoa(slot) + ":shoes"
	case 6:
		return strconv.Itoa(slot) + ":breast"
	case 7:
		return strconv.Itoa(slot) + ":waist"
	case 8:
		return strconv.Itoa(slot) + ":skin"
	case 9:
		return strconv.Itoa(slot) + ":aura_skin"
	case 10:
		return strconv.Itoa(slot) + ":weapon_avatar"
	case 11:
		return strconv.Itoa(slot) + ":aura"
	case 12:
		return strconv.Itoa(slot) + ":weapon"
	case 13:
		return strconv.Itoa(slot) + ":title"
	case currentNameTagAppearanceSlot:
		return strconv.Itoa(slot) + ":name_tag"
	default:
		return strconv.Itoa(slot) + ":other"
	}
}

// currentUserInfoMode1CreateRowCountForLog 读 mode1 体里的"角色装备对象创建行"条数。
// 偏移依据 userinfo_current_modes.go:22-56 的写入顺序（mode/owner/raw16/objectKey/
// exp/statBlob/extraEquipSlotState 之后就是 sub_1D77560 的创建行计数）。
func currentUserInfoMode1CreateRowCountForLog(body []byte) (int, bool) {
	if len(body) < 31 || body[0] != 1 {
		return 0, false
	}
	statLen := int(binary.LittleEndian.Uint32(body[27:31]))
	pos := 31 + statLen + 1
	if statLen < 0 || pos >= len(body) {
		return 0, false
	}
	return int(body[pos]), true
}

// logCurrentPeerActorAppearanceEvent 在队友 actor 投影真正下发之后记一条判读事件：
// **谁看谁**（receiver/peer）、发了**哪些外观部件**（mode0 外观槽 + mode1 装备创建行 +
// op9 队伍记录）、**哪些没发**（本投影故意不带的东西），以及克隆链接字段实际写了多少行。
//
// 下一次线上复测的判据（`grep -a game-party-peer-actor-appearance packet_log.txt`）：
//
//	appearance_link_rows=8   ⇒ 修复档生效（角色 6 的 8 件克隆装扮都带上了链接）
//	appearance_link_rows=0   ⇒ 回退档/开关被关，队友外观仍会让克隆件画成透明
//
// 事件里的 `clone_link_env` / `clone_link_mode` 直接给出开关本体，不需要对照启动行。
func (s *Service) logCurrentPeerActorAppearanceEvent(
	receiver, actor *gameSession,
	source string,
	mode0Body, mode1Body []byte,
	subtype0Body []byte,
) {
	if s == nil || receiver == nil || actor == nil {
		return
	}
	composition, ok := currentSceneAppearanceBodyCompositionForLog(mode0Body)
	if !ok {
		return
	}
	createRows, createRowsOK := currentUserInfoMode1CreateRowCountForLog(mode1Body)
	peerCharID := actor.selectedCharacterID
	// 第二条载体（被投影角色自己的 USERINFO subtype0 外观列表）的判读字段：
	// **只在开关为修复档时追加**，于是回退档下本事件的整行字节与改动前逐字节相同。
	// 见 party_peer_appearance_subtype0.go 的文件头（厂商 TownHandler.cs:502-515）。
	subtype0Enabled := currentPeerAppearanceSubtype0Enabled()
	subtype0Rows, subtype0LinkRows, subtype0DisplayRows, subtype0ParseOK := currentSubtype0AppearanceBodyCompositionForLog(subtype0Body)
	subtype0Sent := subtype0Enabled && len(subtype0Body) != 0
	included := currentPeerActorAppearanceIncluded()
	omitted := currentPeerActorAppearanceOmitted()
	if subtype0Sent {
		included = currentPeerActorAppearanceIncludedWithSubtype0()
		omitted = currentPeerActorAppearanceOmittedWithoutRoster()
	}
	fields := []any{
		"source", source,
		"conn_id", receiver.connID,
		"receiver_char_id", receiver.selectedCharacterID,
		"peer_char_id", peerCharID,
		"object_key", currentSceneActorObjectKey(peerCharID),
		"mode0_body_len", len(mode0Body),
		"mode1_body_len", len(mode1Body),
		"appearance_rows", composition.Rows,
		"appearance_slots", strings.Join(currentSceneAppearanceSlotComponents(composition.Slots), ","),
		"appearance_components", currentPeerActorAppearanceComponents(),
		"appearance_empty_rows", composition.EmptyRows,
		"appearance_link_rows", composition.LinkRows,
		"clone_link_env", currentPeerAppearanceCloneLinkEnv,
		"clone_link_mode", currentPeerAppearanceCloneLinkMode(),
		"mode1_create_rows", createRows,
		"mode1_create_rows_ok", createRowsOK,
		"included", included,
		"omitted", omitted,
	}
	if subtype0Enabled {
		fields = append(fields,
			"appearance_subtype0_env", currentPeerAppearanceSubtype0Env,
			"appearance_subtype0_mode", currentPeerAppearanceSubtype0Mode(),
			"appearance_subtype0_body_len", len(subtype0Body),
			"appearance_subtype0_rows", subtype0Rows,
			"appearance_subtype0_link_rows", subtype0LinkRows,
			"appearance_subtype0_display_rows", subtype0DisplayRows,
			"appearance_subtype0_parsed", subtype0ParseOK,
			"appearance_subtype0_sent", subtype0Sent,
			"appearance_subtype0_reason", currentPeerActorSubtype0ReasonText(true, subtype0Body, composition.LinkRows),
			"peer_userinfo_carrier", currentPeerActorSubtype0CarrierText(subtype0Sent),
		)
	}
	s.logGameEvent(receiver, "game-party-peer-actor-appearance", fields...)
}

// currentPeerActorAppearanceComponents 是"这条投影里的外观部件全集"的自述串。
func currentPeerActorAppearanceComponents() string {
	return "mode0_appearance_table(slots 0-13+28: hat,hair,face,coat,pants,shoes,breast,waist,skin,aura_skin," +
		"weapon_avatar,aura,weapon,title,name_tag);mode1_equipment_create_rows(all worn slots incl. clone link);" +
		"op9_party_frame_member_identity(job,level,grow,name)"
}

// currentPeerActorAppearanceIncluded 是"实际写入的字段"自述串。
func currentPeerActorAppearanceIncluded() string {
	return "appearance_display_item_id,appearance_link_item_id(clone source link),appearance_packed_flags," +
		"mode1_create_item_id,mode1_create_linked_item_id,op9_member_identity_block"
}

// currentPeerActorAppearanceOmitted 是"这条投影仍然不带的东西"自述串。
// 写清楚是为了让下一次线上复测能一眼判掉"没发的东西"和"发错的东西"。
func currentPeerActorAppearanceOmitted() string {
	return "noti2_roster_appearance_table(login_only_carrier),appearance_aux_value,appearance_aux_flag," +
		"placement_op120_op22_op23(deliberate_no_position_semantics),pet_appearance_op105," +
		"guild_medal_beyond_equipment_slot_32,live_fashion_change_repush_after_peer_keeps_actor"
}

// currentPeerActorAppearanceIncludedWithSubtype0 是补齐第二条载体（被投影角色自己的
// USERINFO subtype0 外观列表）之后的"实际写入字段"自述串。
func currentPeerActorAppearanceIncludedWithSubtype0() string {
	return currentPeerActorAppearanceIncluded() +
		",peer_userinfo_subtype0_appearance_list(23-byte rows: clone DisplayItemId=cloned-from + LinkItemId=clone)"
}

// currentPeerActorAppearanceOmittedWithoutRoster 是补齐第二条载体之后**不再**缺席的那些
// 东西：只有名册外观表这一项被移出 omitted（其余不变）。
func currentPeerActorAppearanceOmittedWithoutRoster() string {
	return strings.Replace(
		currentPeerActorAppearanceOmitted(),
		"noti2_roster_appearance_table(login_only_carrier),",
		"",
		1,
	)
}

// currentPeerActorSubtype0CarrierText 自述这一次到底有没有把第二条载体送出去。
func currentPeerActorSubtype0CarrierText(sent bool) string {
	if sent {
		return "peer_userinfo_subtype0_appearance_list_sent"
	}
	return "peer_userinfo_subtype0_appearance_list_not_sent"
}
