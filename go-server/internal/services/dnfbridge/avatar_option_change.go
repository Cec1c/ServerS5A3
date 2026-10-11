package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfavataroption "longheng.io/server/internal/modules/dnf/avataroptionchange"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 装扮属性调整箱（ENUM_CMDPACKET_AVATAR_OPTION_CHANGE）。
//
// # 一、本仓 opcode 取证的证据链（为什么是 459，而不是厂商的 0x01CC=460）
//
//  1. **当前 EXE 自己的名字表**：`internal/modules/dnf/dnfenum/cmdpacket.go` 文件头写明
//     "Code generated from NoPack.exe runtime GetPacketName table"，其中
//     `CmdPacketAvatarOptionChange CmdPacket = 459`（:468），
//     `CmdPacketName(459) = "ENUM_CMDPACKET_AVATAR_OPTION_CHANGE"`（:2457）。这张表是
//     **客户端自己的运行期编号**，不是厂商 C# 枚举。
//
//  2. **厂商编号与本仓编号存在 +1 偏移**：厂商 `PacketTypesA21.cs` 里
//     `0x01CC`(460) = AVATAR_OPTION_CHANGE；同表 `0x01C7`(455) 在 CmdPacket 命名空间是
//     INSERT_RANDOM_OPTION_ITEM，而本仓 `CmdPacketInsertRandomOptionItem = 454`。
//     两处都差 1 ⇒ 厂商 460 对本仓就是 459（本仓 `CmdPacketCharacterStatus = 460`，
//     即厂商表里多出的那一条把后面的编号整体推后了一位）。
//     同一偏移在 `vendor_absent_send_block.go:15-19` 已有记录。
//
//  3. **现场抓包逐字节复核**（决定性）：`packet_log.20260926-022031.txt:14569`
//
//     RECV kind=game-legacy conn_id=game-000017 pkt_seq=994 cmd=1 type=459
//     runtime_cmd_name=ENUM_CMDPACKET_AVATAR_OPTION_CHANGE runtime_cmd_known=true
//     header_seq=298 body_len=13 len=26
//     hex=01cb011a000000d0cfb4ff2a0149006498280033001e9f00002f
//
//     按 `protocol.ParseLegacyGamePacket` 的 13 字节 legacy 头解析：cmd=0x01、
//     type=LE(01cb)=459、length=26、checksum=d0cfb4ff、seq=298，**body 恰好 13 字节**
//     （`dispatch_body_len=13`，无 legacy trailer 需要剥离）：
//
//     49 00              i16 sourceSlot = 73   （主背包 list 0）
//     64 98 28 00        i32 sourceItem = 2660452
//     33 00              i16 targetSlot = 51   （装扮栏 list 1）
//     1e 9f 00 00        i32 targetItem = 40734
//     2f                 u8  abilityNo = 47
//
//     与厂商 `AvatarOptionChangeRequestParser.TryParse` 的字段顺序**逐字段一致**；
//     sourceItem 2660452 就是真档 `stackable/cash/avatar_optionchange1.stk`
//     「装扮属性调整箱 (稀有)」的 itemID，targetItem 40734 是真档
//     `equipment/character/swordman/avatar/coat/afparmor_dkknight.equ`
//     「深渊骑士胸甲」`[coat avatar] [grade] 3`。三次右键（:14569/:14868/:15139）
//     包体完全相同，**服务端当时没有任何回包**（客户端随后直接发 op7 退回选角），
//     就是操作者报的"右键无反应"。
//
// # 二、应答（照抄厂商，不自行设计）
//
// 厂商 `InventoryHandler.AvatarOptionChange.cs` 与 `AvatarOptionChangeAckBuilder.cs`：
//
//	成功体 = u8 0x01 | i16 targetSlotIndex | u8 abilityNo     （4 字节）
//	失败体 = u8 0x00                                          （1 字节）
//	opcode = class1/0x01CC（本仓 = class1/op459），随后
//	SendUpdateItemList(Main, sourceSlotIndex)
//
// 本仓回包走 class1（`dnfproto.DefaultChannelClassification`）：这是本仓既有口径，
// 与已上线的同型命令一致 —— 现场抓包 `packet_log.20260926-022031.txt:10508`
// 证明 op81（装备品级调整器）同样以 `kind=game-legacy cmd=1 type=81` 上行、由服务端以
// `kind=game-upper msg_id=81 classification=1` 下行（`gameplay_module_item_grade_adjust.go`）。
//
// **证据边界（如实记录）**：厂商失败体只有 1 字节 `00`，本仓没有"客户端如何解释它"的
// 抓包（服务端从未回过这条包），因此按题目要求先采用厂商形状；一旦实机发现异常，
// 用下面的开关一键回退。成功体 4 字节与厂商逐字节同构，风险仅限于失败分支。
//
// # 三、开关（一键回退）
//
//	DNFBRIDGE_AVATAR_OPTION_CHANGE
//	  - 未设置 / 其它值（**默认 on = 修复档**）：接上上面这条路径；
//	  - `off` / `0` / `false` / `legacy`（以及 `no` / `disabled`）：**逐字节回到改动前**。
//	    这里的"回到改动前"是严格意义上的：处理器直接 `handled=false` 返回，
//	    分发控制流与本改动前**完全一致**——依旧落到 `game_dispatch_legacy.go` 末尾的
//	    `dnfbridge unhandled latest game command` 日志，一个下行字节都不发。
//	    回退只需设环境变量并重启服务端，不需要改代码或回滚二进制。
const (
	currentAvatarOptionChangeOpcode   uint16 = uint16(dnfenum.CmdPacketAvatarOptionChange)
	currentAvatarOptionChangeBodySize        = 13

	// 厂商 InventoryAvatarOptionChangeError 的失败体只有这一个形状。
	currentAvatarOptionChangeFailureByte byte = 0x00
	// 成功体首字节。
	currentAvatarOptionChangeSuccessByte byte = 0x01

	currentAvatarOptionChangeEnv = "DNFBRIDGE_AVATAR_OPTION_CHANGE"
)

// currentAvatarOptionChangeDisabled 解析 DNFBRIDGE_AVATAR_OPTION_CHANGE。
// 默认 on：空/未设置/非法值都取修复档，只有显式写 off/0/false/legacy 才回退。
func currentAvatarOptionChangeDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentAvatarOptionChangeEnv))) {
	case "off", "0", "false", "legacy", "no", "disabled":
		return true
	default:
		return false
	}
}

// currentAvatarOptionChangeRequest 是厂商 InventoryAvatarOptionChangeRequest 的本仓视图。
// AbilityNo 在厂商类型里是 ushort，但解析器只读 body[12] 一个字节、回包也只写一个字节，
// 所以本仓按 u8 建模。
type currentAvatarOptionChangeRequest struct {
	SourceSlot   int16
	SourceItemID int32
	TargetSlot   int16
	TargetItemID int32
	AbilityNo    uint8
}

// decodeCurrentAvatarOptionChangeRequest 照抄厂商 TryParse 的边界：体至少 13 字节，
// 小端；不足 13 字节即解析失败（厂商此时同样回失败体，不静默）。
func decodeCurrentAvatarOptionChangeRequest(body []byte) (currentAvatarOptionChangeRequest, error) {
	if len(body) < currentAvatarOptionChangeBodySize {
		return currentAvatarOptionChangeRequest{},
			fmt.Errorf("avatar option change body too short: %d", len(body))
	}
	return currentAvatarOptionChangeRequest{
		SourceSlot:   int16(binary.LittleEndian.Uint16(body[0:2])),
		SourceItemID: int32(binary.LittleEndian.Uint32(body[2:6])),
		TargetSlot:   int16(binary.LittleEndian.Uint16(body[6:8])),
		TargetItemID: int32(binary.LittleEndian.Uint32(body[8:12])),
		AbilityNo:    body[12],
	}, nil
}

// handleAvatarOptionChangeCommand 是 legacy（class0/cmd=1）上行入口。
// 返回 (handled=false, nil) 表示"这条包不归我管"，调用方必须原样继续既有控制流。
func (s *Service) handleAvatarOptionChangeCommand(
	session *gameSession,
	typ uint16,
	body []byte,
) (bool, error) {
	if typ != currentAvatarOptionChangeOpcode {
		return false, nil
	}
	if currentAvatarOptionChangeDisabled() {
		return false, nil
	}
	return true, s.serveAvatarOptionChange(session, body)
}

// handleAvatarOptionChangeUpperCommand 是 class1（upper）上行入口：现场抓包证明当前
// 客户端走的是 legacy，但本仓同型命令（op30/op81/op99/op518…）一律两条传输都登记，
// 这里保持一致；分类必须是默认 channel 分类，与 defaultClassGameplayHandler 同口径。
func (s *Service) handleAvatarOptionChangeUpperCommand(
	session *gameSession,
	msgID uint16,
	classification byte,
	body []byte,
) (bool, error) {
	if msgID != currentAvatarOptionChangeOpcode {
		return false, nil
	}
	if currentAvatarOptionChangeDisabled() {
		return false, nil
	}
	if classification != dnfproto.DefaultChannelClassification {
		s.logGameEvent(session, "game-avatar-option-change-blocked",
			"classification", classification,
			"body_len", len(body),
			"reason", "current_exe_op459_command_class_mismatch")
		return true, nil
	}
	return true, s.serveAvatarOptionChange(session, body)
}

// buildCurrentAvatarOptionChangeSuccessBody = 厂商 BuildSuccess：
// u8 0x01 | i16 targetSlotIndex | u8 abilityNo。
func buildCurrentAvatarOptionChangeSuccessBody(targetSlot int16, abilityNo uint8) []byte {
	var writer packetWriter
	writer.writeByte(currentAvatarOptionChangeSuccessByte)
	writer.writeUint16(uint16(targetSlot))
	writer.writeByte(abilityNo)
	return writer.bytes()
}

// buildCurrentAvatarOptionChangeFailureBody = 厂商 BuildError：单字节 0x00。
func buildCurrentAvatarOptionChangeFailureBody() []byte {
	return []byte{currentAvatarOptionChangeFailureByte}
}

// serveAvatarOptionChange 执行一次属性调整：解析 → 仓储事务 → 回包 → 刷新。
//
// 失败一律回失败体（厂商语义"失败要回包，不能沉默"），并在日志里留下具名原因，
// 保证排查时能区分"不是这个箱子 / 品级不匹配 / 槽位对不上 / 仓储不可用"。
func (s *Service) serveAvatarOptionChange(session *gameSession, body []byte) error {
	request, err := decodeCurrentAvatarOptionChangeRequest(body)
	if err != nil {
		return s.rejectCurrentAvatarOptionChange(session, request, "request_invalid", err)
	}
	if session == nil || session.selectedCharacterID == 0 {
		return s.rejectCurrentAvatarOptionChange(session, request, "character_not_selected", nil)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return s.rejectCurrentAvatarOptionChange(session, request, "repository_unavailable", nil)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return s.rejectCurrentAvatarOptionChange(session, request, "pvf_catalog_unavailable", err)
	}
	owner, err := dnfavataroption.NewOwner(repos, currentAvatarOptionChangeCatalog{catalog: catalog})
	if err != nil {
		return s.rejectCurrentAvatarOptionChange(session, request, "owner_unavailable", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ChangeAvatarOption(ctx, dnfavataroption.Command{
		AccountID:    s.accountIDForSession(session),
		CharacterID:  strconv.FormatUint(uint64(session.selectedCharacterID), 10),
		SourceSlot:   request.SourceSlot,
		SourceItemID: request.SourceItemID,
		TargetSlot:   request.TargetSlot,
		TargetItemID: request.TargetItemID,
		AbilityNo:    request.AbilityNo,
		ProjectAvatarRow: func(stack *dnfrepo.ItemStack) {
			projectCurrentAvatarOptionChangeRow(request.TargetSlot, stack)
		},
	})
	if err != nil {
		return s.rejectCurrentAvatarOptionChange(
			session, request, currentAvatarOptionChangeFailureReason(err), err)
	}

	// 先回 ACK，再刷主背包源槽 —— 与厂商 handler 的顺序一致
	// （SendPacketAsync(success) 之后才 SendUpdateItemList(Main, sourceSlotIndex)）。
	if err := s.sendGameUpperRawClass(
		session,
		currentAvatarOptionChangeOpcode,
		buildCurrentAvatarOptionChangeSuccessBody(result.TargetSlot, result.TargetAbilityNo),
		dnfproto.DefaultChannelClassification,
	); err != nil {
		return err
	}
	s.logGameEvent(session, "game-avatar-option-change-committed",
		"char_id", result.CharacterID,
		"source_slot", result.SourceSlot,
		"source_item", result.SourceItemID,
		"source_remaining", result.SourceRemaining,
		"source_removed", result.SourceRemoved,
		"target_slot", result.TargetSlot,
		"target_item", result.TargetItemID,
		"ability_no", result.TargetAbilityNo,
		"ack_body", "vendor_op459_success_01_slot_ability",
		"inventory_refresh", "vendor_send_update_item_list_main_source_slot_op14")
	// 厂商随后 SendUpdateItemList(Main, sourceSlotIndex)：只有主背包源槽需要刷新，
	// 目标的属性由 4 字节成功体直接带给客户端（厂商不额外刷装扮栏）。
	return s.sendSelectedIncrementalItemSlotRefreshes(
		session,
		"avatar_option_change",
		[]alignedcmd.ItemSlotRefresh{{
			ListType:  dnfrepo.MainInventoryListType,
			SlotIndex: result.SourceSlot,
		}},
	)
}

// rejectCurrentAvatarOptionChange 交付厂商失败体，并记录可归因的失败分类。
func (s *Service) rejectCurrentAvatarOptionChange(
	session *gameSession,
	request currentAvatarOptionChangeRequest,
	reason string,
	err error,
) error {
	if reason == "" {
		reason = "unclassified"
	}
	s.logGameEvent(session, "game-avatar-option-change-failed",
		"reason", reason,
		"source_slot", request.SourceSlot,
		"source_item", request.SourceItemID,
		"target_slot", request.TargetSlot,
		"target_item", request.TargetItemID,
		"ability_no", request.AbilityNo,
		"ack_body", "vendor_op459_failure_single_byte_00",
		"error", err)
	if session == nil {
		// 没有会话就没有回包对象；正常分流不会走到这里。
		return nil
	}
	return s.sendGameUpperRawClass(
		session,
		currentAvatarOptionChangeOpcode,
		buildCurrentAvatarOptionChangeFailureBody(),
		dnfproto.DefaultChannelClassification,
	)
}

// currentAvatarOptionChangeFailureReason 把 owner 的失败分类映射成日志用短名。
// 分类与厂商 InventoryAvatarOptionChangeError 一一对应，顺序即校验顺序。
func currentAvatarOptionChangeFailureReason(err error) string {
	switch {
	case errors.Is(err, dnfavataroption.ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, dnfavataroption.ErrSourceNotFound):
		return "source_not_found"
	case errors.Is(err, dnfavataroption.ErrSourceItemMismatch):
		return "source_item_mismatch"
	case errors.Is(err, dnfavataroption.ErrNotOptionChangeBox):
		return "not_option_change_box"
	case errors.Is(err, dnfavataroption.ErrTargetNotFound):
		return "target_not_found"
	case errors.Is(err, dnfavataroption.ErrTargetItemMismatch):
		return "target_item_mismatch"
	case errors.Is(err, dnfavataroption.ErrNotAvatar):
		return "target_not_avatar"
	case errors.Is(err, dnfavataroption.ErrAvatarMetadataMissing):
		return "avatar_metadata_missing"
	case errors.Is(err, dnfavataroption.ErrAvatarGradeMismatch):
		return "avatar_grade_mismatch"
	case errors.Is(err, dnfavataroption.ErrCharacterNotFound),
		errors.Is(err, dnfavataroption.ErrInventoryNotFound):
		return "inventory_unavailable"
	case errors.Is(err, dnfavataroption.ErrAccountMismatch):
		return "account_mismatch"
	case errors.Is(err, dnfavataroption.ErrAccountRequired):
		return "account_unavailable"
	case errors.Is(err, dnfavataroption.ErrCharacterRequired):
		return "character_unavailable"
	default:
		return "commit_rejected"
	}
}

// currentAvatarOptionChangeCatalog 把运行时 PVF 目录适配成 owner 需要的视图。
// 装扮判定复用既有的 currentEquipmentPlacementRuleForPVFType，不另造一张表。
type currentAvatarOptionChangeCatalog struct {
	catalog *pvfDungeonDropCatalog
}

func (c currentAvatarOptionChangeCatalog) ResolveItem(itemID uint32) (dnfavataroption.ItemDefinition, error) {
	if c.catalog == nil {
		return dnfavataroption.ItemDefinition{}, dnfavataroption.ErrOwnerUnavailable
	}
	definition, err := c.catalog.ResolveItem(itemID)
	if err != nil {
		return dnfavataroption.ItemDefinition{}, err
	}
	out := dnfavataroption.ItemDefinition{
		Grade:         definition.Grade,
		EquipmentType: definition.EquipmentType,
		AvatarGrades:  append([]int64(nil), definition.AvatarOptionChangeGrades...),
	}
	switch definition.Kind {
	case dungeonDropItemStackable:
		out.Kind = dnfavataroption.KindStackable
	case dungeonDropItemEquipment:
		out.Kind = dnfavataroption.KindEquipment
	}
	if rule, ok := currentEquipmentPlacementRuleForPVFType(definition.EquipmentType); ok &&
		rule.class == currentEquipmentPlacementClassAvatar {
		out.IsAvatar = true
	}
	return out, nil
}

// projectCurrentAvatarOptionChangeRow 在事务内把装扮行的持久化 raw 行重建成客户端
// 真正读的形状。复用既有的 currentItemListEntryFromStack：它按
// avatar_attribute_row.go 的结论把属性字写进 +0x0B（与耐久同字）并把 +0x0A 留 0；
// 此时 extra 的 ext_data0 已由 owner 写好，所以读到的是新属性。
// 装扮栏行按 list 1 投影（该 list 的行后还有两个可选 blob，但持久化只存 0x77 行本身）。
func projectCurrentAvatarOptionChangeRow(targetSlot int16, stack *dnfrepo.ItemStack) {
	if stack == nil || stack.ItemID <= 0 {
		return
	}
	entry := currentItemListEntryFromStack(currentExeInventoryAvatarListType, targetSlot, *stack)
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
}
