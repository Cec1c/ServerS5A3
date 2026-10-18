package dnfbridge

import (
	"context"
	"encoding/binary"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// 本文件实现「过图 / 开始地图补 class0/op9 kind0 房间槽位表」——厂商 9.16 `packet_order`
// 自述里的最后一条：`…-ff-op36-op9-room-slot-table`。
//
// # 厂商原始证据（只读引用 D:\DNF90-test\server\runtime\logs\packet_log.txt）
//
//	:5408 EVENT kind=game-party-dungeon-start-committed conn_id=game-000061 … actors=4
//	      packet_order=all-members-through-op28-then-op800-all-op29-optional-trap-event-op312-\
//	      ff-op36-op9-room-slot-table
//	:5405 EVENT kind=game-party-dungeon-room-slot-table-send conn_id=game-000061
//	      source=party_start_room_after_op29_actors party_id=2 char_id=2 object_key=2
//	      owner_channel=0 row_count=2 host_slot=0 body_len=68 msg_id=9 classification=0
//	      body_source=current_exe_sub_1D64CA0_kind0_room_slot_table_after_all_party_actors_extended_rows
//	:5407 SEND conn=game-000061 msg_id=9 plain_body_len=68
//	:5411 SEND conn=game-000059 msg_id=9 plain_body_len=68（同形，只有"自己那一行"不同）
//	:5398/:5403 msg_id=36 plain_body_len=10（fatigue，紧邻 op9 之前）
//	:5389/:5393 msg_id=800 plain_body_len=7；:5391/:5395 msg_id=29（op29 本体）
//	（10:41:18 那次**过图**的 start-committed 也是同一条顺序串 + 同形 op9：
//	 :25719-:25725，`object_key=2/1`、`room=0:0`、`map_id=76131`）
//
// 两条 68B body 就是本文件的**逐字节夹具**（回归里 `bytes.Equal`）：
//
//	conn61 (char 2) 01 00 0f 27 02 00 00 00 00 00 00 00 02 00 00 00 74 32 00 04 00 00 00
//	                00 00 00 00 00 00 00 00 02 00 01 00 00 01 00 00 02 00 00 00 74 31 00
//	                64 01 01 02 00 00 01 00 00 02 00 00 00 74 32 00 64 01 00 00 00 00
//	conn59 (char 1) 同形，object_key=01 00、actor 名 = 74 31（"t1"）
//
// # 形状（逐字节对齐厂商，行 16B = 带内联身份块）
//
//	u16 1 | u16 9999 | u16 objectKey | u8 0 | u8 ownerChannel(0)
//	u8 0 | u8 kind(0) | u8 route(0) | u8 context(0)
//	int32 nameLen + name                       ← 记录描述的那个 actor（= 收包者自己）
//	u8 job | u8 actorAux | u32 0 | u8 grow | u16 0 | u8 0 | u8 0 | u16 0
//	u8 rowCount
//	  每行: u8 slot | u16 uid | u8 job | u8 level | u8 grow | u8 0
//	        | int32 nameLen + name | u8 0 | u8 rowHPPercent | u8 rowRoomFlag
//	u8 0 | u8 hostSlot(= 队长所在行) | u8 0 | u8 0
//
// 与既有 `buildCurrentSceneOp9ActorPartyDisplayBodyInContext`（party_online.go 的
// `sendCurrentPartyActorFrameProjection`）**同一族但不同出口**：那一条是城镇/formation 的
// op9 名册（行尾 3 字节是 `0, 用户态, 0`、actor 第 19 字节写等级），本文件是副本里的
// **房间槽位表**（行尾 `0, 0x64, 0x01`、actor 第 19 字节写 `0x04`）。
// 三个"无法从我们现有输入推出"的字节做成显式常量（见 dungeonPartyRoomSlotTableShape），
// 默认值就是厂商夹具里的值。
//
// # 我们缺的是什么
//
// 开始地图（进副本）那一次我们**已经**会发一条 op9 kind0 槽位表
// （`scene_post_start_map.go` 的 `sendCurrentPartyFrameProjection(…"_post_start_map")`），
// 所以本文件在进场那一次默认**是补第二条**；而**过图**（op45 → op29 提交，`dungeon_move`）
// 那一次我们**一条 op9 都不发** —— 这正是"过图后队员卡在等待队员到达"的缺口。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_ROOM_SLOT_TABLE`
//	  - `vendor`（默认，零值）= 每次 start-map（op29）提交都补发本文件这一条
//	    （进场 + 每一次过图各一条；与厂商"每个 start-committed 各一条"同形）；
//	  - `once` = 只在"本次提交还没有任何 kind0 槽位表"时补发（进场那一次已有既有出口 ⇒ 跳过一次，
//	    包数回到厂商的"每提交恰好一条"）；
//	  - `legacy` = 不发（逐字节回到改动前：包序、body、日志全同）。
const dungeonPartyRoomSlotTableEnv = "DNFBRIDGE_PARTY_DUNGEON_ROOM_SLOT_TABLE"

const (
	dungeonPartyRoomSlotTableValueVendor = "vendor"
	dungeonPartyRoomSlotTableValueOnce   = "once"
	dungeonPartyRoomSlotTableValueLegacy = "legacy"
)

// dungeonPartyRoomSlotTableTrigger 标识这次 start-map 提交来自哪条路径。
type dungeonPartyRoomSlotTableTrigger string

const (
	// dungeonPartyRoomSlotTableTriggerEntry = 进副本那一次（op16 → … → op28 → op29）。
	dungeonPartyRoomSlotTableTriggerEntry dungeonPartyRoomSlotTableTrigger = "dungeon_entry"
	// dungeonPartyRoomSlotTableTriggerMove = 过图那一次（op45 → op29）。
	dungeonPartyRoomSlotTableTriggerMove dungeonPartyRoomSlotTableTrigger = "dungeon_move"
)

// dungeonPartyRoomSlotTableMode 是 dungeonPartyRoomSlotTableEnv 的归一化取值。
type dungeonPartyRoomSlotTableMode int

const (
	// dungeonPartyRoomSlotTableVendor 是默认档（零值）= 厂商行为：每个 start-map 提交各一条。
	dungeonPartyRoomSlotTableVendor dungeonPartyRoomSlotTableMode = iota
	// dungeonPartyRoomSlotTableOnce = 只在本次提交还没有 kind0 槽位表时补。
	dungeonPartyRoomSlotTableOnce
	// dungeonPartyRoomSlotTableLegacy = 不发（逐字节回退）。
	dungeonPartyRoomSlotTableLegacy
)

func (mode dungeonPartyRoomSlotTableMode) String() string {
	switch mode {
	case dungeonPartyRoomSlotTableOnce:
		return dungeonPartyRoomSlotTableValueOnce
	case dungeonPartyRoomSlotTableLegacy:
		return dungeonPartyRoomSlotTableValueLegacy
	default:
		return dungeonPartyRoomSlotTableValueVendor
	}
}

// sendsFor 给出该档位在给定触发路径上是否补发。
//
// `once` 档在进场路径上跳过：那一次 start-map 提交里既有的
// `sendCurrentPartyFrameProjection(…"_post_start_map")` 已经发过一条 kind0 槽位表。
func (mode dungeonPartyRoomSlotTableMode) sendsFor(trigger dungeonPartyRoomSlotTableTrigger) bool {
	switch mode {
	case dungeonPartyRoomSlotTableLegacy:
		return false
	case dungeonPartyRoomSlotTableOnce:
		return trigger != dungeonPartyRoomSlotTableTriggerEntry
	default:
		return true
	}
}

// readDungeonPartyRoomSlotTableMode 解析 DNFBRIDGE_PARTY_DUNGEON_ROOM_SLOT_TABLE。
// **默认 vendor（= 厂商行为）**：空/未设置/非法值都是 vendor。
func readDungeonPartyRoomSlotTableMode(value string) dungeonPartyRoomSlotTableMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case dungeonPartyRoomSlotTableValueOnce:
		return dungeonPartyRoomSlotTableOnce
	case dungeonPartyRoomSlotTableValueLegacy:
		return dungeonPartyRoomSlotTableLegacy
	default:
		return dungeonPartyRoomSlotTableVendor
	}
}

// dungeonPartyRoomSlotTable 返回当前 Service 的归一化档位（零值 options 的测试/探针路径
// = vendor = 默认档）。
func (s *Service) dungeonPartyRoomSlotTable() dungeonPartyRoomSlotTableMode {
	if s == nil {
		return dungeonPartyRoomSlotTableVendor
	}
	return s.options.dungeonPartyRoomSlotTable
}

// dungeonPartyEntrySlotTableEnv 按**相位**收口"进图那一次 start-map 提交里，同一条
// class0/op9 kind0 槽位表被发了两条"这件事（不改任何 body、不动队友 actor 那三条线格式）。
//
// # 现场证据（2026-09-27 p21，组队进图后**队员**客户端自己退队）
//
//	RECV cmd=1 type=13 ENUM_CMDPACKET_LEAVE_PARTY body_len=0
//	EVENT game-party-membership-removed source=client_leave_party_op13 char_id=4
//	      party_id_before=1 member_count_before=2 → member_count_after=1
//
// 退队前该会话收到的完整序列（pkt_seq → msg_id）：
//
//	304:120 305:3 306:2 307:2 308:9 309:37 310:30 311:19 312:120 313:2 314:2 315:9 → op13
//
// 已排除：op9 的 owner 档（zero/channel）、抑制 msg 11+153、抑制 msg 37+30+19、抑制 msg 3、
// 跳过选择器、队友坐标 off —— 都不减少退队次数；而**抑制全部 msg 9 ⇒ 零新增**
// ⇒ 触发点是一条 class0/op9。进一步：把"同队即跳过"提到公共出口、精确跳过副本侧两条
// **队友** op9 之后**仍有 1 次**退队 ⇒ 触发点不在这两条队友 op9 上
// （那两条已被 4 个既有用例钉住线格式，本开关一个字节都不碰）。
//
// # 为什么嫌疑落在"自己那两条 kind0 槽位表"
//
// 进图这一次 start-map 提交里，我们给**同一个 actor（收包者自己）**发了**两条** kind0 槽位表：
//
//	① `scene_post_start_map.go` 的 `sendCurrentPartyFrameProjection(…"_post_start_map")`
//	   （既有出口，落在"落位"里 ⇒ 被全队读条屏障压到 op37 到齐、与 op120/op3 同一批）；
//	② 本文件的厂商形状房间槽位表（`…-ff-op36-op9-room-slot-table`，在 op29 提交尾段）。
//
// 而厂商每个 start-map 提交**只发一条**（:5405 `source=party_start_room_after_op29_actors`
// → :5407 conn61 / :5411 conn59），没有任何"名册帧 + 房间槽位表"双发样本。
// 现场症状恰好是"队员在读条刚结束那一刻收到最后一条 op9 之后立刻退队"：两条描述同一个
// actor 的队伍槽位表声明落在**同一个读条窗口**里（一条在 op36 之后、一条在 op37 之前的
// 落位里）；第二条与客户端此刻手里的队伍状态对不上 ⇒ 客户端判定队伍无效、自己发 op13。
//
// # 开关
//
//	`DNFBRIDGE_PARTY_DUNGEON_ENTRY_SLOT_TABLE`
//	  - `current`（默认，零值）= 今天的行为：上面两条都发（逐字节、逐包序不变）；
//	  - `vendor_only` = 厂商口径：本次 start-map 提交里**厂商形状的房间槽位表已经发出去**时，
//	    落位里那条 `…_post_start_map` kind0 名册帧让位（不发），使"每个 actor 每个提交
//	    恰好一条 kind0 槽位表"，且它落在厂商的相位（op29 提交尾段、op37 之前）。
//	    名册信息不丢：让位的那条与留下那条描述的是**同一个 actor、同一份权威槽位表**
//	    （同样的行、同样的队长槽位；夹具见 dungeonPartyVendorSlotTableFixtures）。
//
// 为什么不是"照搬城镇侧同队即跳过"：副本里的队友 op9 已被 4 个既有用例固化为行为
// （TestDungeonPartyPeerFrameCarriesIdentityBlockForClientChannel /
// TestDungeonPartyOwnerModeZeroKeepsByteIdenticalWire /
// TestDungeonPartyPeerActorStateSendsCreationFramesWithoutPlacement /
// TestDungeonPartyFrameRosterIsSymmetricPerSession），本开关只作用于**自己**那条重复出口。
//
// 反向单变量（不需要改代码）：`DNFBRIDGE_PARTY_DUNGEON_ROOM_SLOT_TABLE=once`（或 `legacy`）
// 留下①、去掉② —— 两个方向合起来正好二分这对重复声明。
//
// 回退：把开关设回 `current`（或删掉该行）重启即可逐字节回到今天。
const dungeonPartyEntrySlotTableEnv = "DNFBRIDGE_PARTY_DUNGEON_ENTRY_SLOT_TABLE"

const (
	dungeonPartyEntrySlotTableValueCurrent    = "current"
	dungeonPartyEntrySlotTableValueVendorOnly = "vendor_only"
)

// dungeonPartyEntrySlotTableMode 是 dungeonPartyEntrySlotTableEnv 的归一化取值。
type dungeonPartyEntrySlotTableMode int

const (
	// dungeonPartyEntrySlotTableCurrent 是默认档（零值）= 今天的行为（两条都发）。
	dungeonPartyEntrySlotTableCurrent dungeonPartyEntrySlotTableMode = iota
	// dungeonPartyEntrySlotTableVendorOnly = 厂商口径（每个提交每个 actor 一条）。
	dungeonPartyEntrySlotTableVendorOnly
)

func (mode dungeonPartyEntrySlotTableMode) String() string {
	if mode == dungeonPartyEntrySlotTableVendorOnly {
		return dungeonPartyEntrySlotTableValueVendorOnly
	}
	return dungeonPartyEntrySlotTableValueCurrent
}

// dropsLegacyPlacementFrame 报告该档位是否让落位里那条既有名册帧让位。
func (mode dungeonPartyEntrySlotTableMode) dropsLegacyPlacementFrame() bool {
	return mode == dungeonPartyEntrySlotTableVendorOnly
}

// readDungeonPartyEntrySlotTableMode 解析 DNFBRIDGE_PARTY_DUNGEON_ENTRY_SLOT_TABLE。
// **默认 current（= 今天的行为）**：空/未设置/非法值都是 current。
func readDungeonPartyEntrySlotTableMode(value string) dungeonPartyEntrySlotTableMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case dungeonPartyEntrySlotTableValueVendorOnly, "vendor", "single":
		return dungeonPartyEntrySlotTableVendorOnly
	default:
		return dungeonPartyEntrySlotTableCurrent
	}
}

// partyDungeonEntrySlotTable 返回当前 Service 的归一化档位（零值 options = current）。
func (s *Service) partyDungeonEntrySlotTable() dungeonPartyEntrySlotTableMode {
	if s == nil {
		return dungeonPartyEntrySlotTableCurrent
	}
	return s.options.partyDungeonEntrySlotTable
}

// suppressPostStartMapLegacyPartyFrame 判定"落位里那条 `…_post_start_map` kind0 名册帧"
// 这一次要不要让位。
//
// 两个条件缺一不可：
//   - 开关是 `vendor_only`（默认 current ⇒ 恒 false，逐字节保持今天）；
//   - 本会话**本次 start-map 提交确实已经发出过**一条厂商形状的房间槽位表
//     （`dungeonPartySlotTableSentOnStartMap`）。发送方可能因为单人队伍、角色记录缺失、
//     或 `DNFBRIDGE_PARTY_DUNGEON_ROOM_SLOT_TABLE=once/legacy` 而没有发 —— 那种情况下
//     让位会让这名成员**一条槽位表都拿不到**，所以必须原样发出。
func (s *Service) suppressPostStartMapLegacyPartyFrame(session *gameSession, source string) bool {
	if s == nil || session == nil {
		return false
	}
	if !s.partyDungeonEntrySlotTable().dropsLegacyPlacementFrame() {
		return false
	}
	if !session.dungeonPartySlotTableSentOnStartMap {
		return false
	}
	s.logGameEvent(session, "game-party-dungeon-entry-slot-table-legacy-frame-skipped",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"party_id", runtimePartyStateSnapshot(session).PartyID,
		"msg_id", uint16(dnfenum.CmdPacketRecoverStamina),
		"switch", dungeonPartyEntrySlotTableEnv,
		"mode", s.partyDungeonEntrySlotTable().String(),
		"reason", "vendor_sends_exactly_one_kind0_slot_table_per_start_map_commit",
		"keeper_source", "party_start_room_after_op29_actors")
	return true
}

// dungeonPartyRoomSlotTableShape 是三个"无法从我们现有输入推出"的厂商夹具字节。
//
// 证据与推断边界（诚实标注）：
//   - `ActorAux` = actor 段第 19 字节。厂商三个副本样本（conn59/conn61 与 10:41 那次）**恒为 0x04**；
//     同一位置的"等级"读法解释不通——厂商行里 t1/t2 的等级是 1/1（第一场）与 5/2（后续场），
//     而 actor 段恒为 4 ⇒ 它不是等级。我们既有的 op9 出口在那里写的是等级。
//   - `RowHPPercent` = 行尾倒数第二字节，厂商样本恒为 `0x64`(=100)；与 op153 行里的
//     `uid, 0x64, 0, slot` 同值，因此按"满血百分比位"落地。
//   - `RowRoomFlag` = 行尾最后一字节：副本里恒 `0x01`，厂商的城镇/formation 那条 op9 同位置是 `0x00`
//     ⇒ 按"在房间内标记"落地。
//
// 三个值默认就是厂商夹具里的值（默认档 = 厂商行为）；回归里用它们逐字节复现厂商 body。
type dungeonPartyRoomSlotTableShape struct {
	ActorAux     byte
	RowHPPercent byte
	RowRoomFlag  byte
}

// vendorDungeonPartyRoomSlotTableShape 是厂商夹具字节的默认值。
var vendorDungeonPartyRoomSlotTableShape = dungeonPartyRoomSlotTableShape{
	ActorAux:     0x04,
	RowHPPercent: 0x64,
	RowRoomFlag:  0x01,
}

// dungeonPartyRoomSlotTableActor 是本条记录描述的那个 actor（= 收包者自己）。
type dungeonPartyRoomSlotTableActor struct {
	ObjectKey uint16
	Name      string
	Job       byte
	Level     byte
	Grow      byte
}

// dungeonPartyRoomSlotTableRow 是槽位表里的一行（一位队员）。
type dungeonPartyRoomSlotTableRow struct {
	Slot   byte
	UserID uint16
	Job    byte
	Level  byte
	Grow   byte
	Name   string
}

// buildDungeonPartyRoomSlotTableBody 按厂商 68B 夹具的逐字节布局组装 class0/op9 kind0
// 房间槽位表。rowCount 取 rows 的实际长度（调用方负责先过滤/截断到 8 行以内）。
func buildDungeonPartyRoomSlotTableBody(
	actor dungeonPartyRoomSlotTableActor,
	rows []dungeonPartyRoomSlotTableRow,
	hostSlot byte,
	shape dungeonPartyRoomSlotTableShape,
	serverRoute ...byte,
) []byte {
	return buildDungeonPartyRoomSlotTableBodyForProfile(actor, rows, hostSlot, shape, false, serverRoute...)
}

// Official24.9 NOTI9 consumes actor metadata at2016913 before the member
// count and five fixed identity bytes plus two DSTRs at2016BC1..C85. The
// historic 68B vendor fixture belongs to the legacy profile only.
func buildDungeonPartyRoomSlotTableBodyForProfile(
	actor dungeonPartyRoomSlotTableActor,
	rows []dungeonPartyRoomSlotTableRow,
	hostSlot byte,
	shape dungeonPartyRoomSlotTableShape,
	official249 bool,
	serverRoute ...byte,
) []byte {
	objectKey := actor.ObjectKey
	if objectKey == 0 {
		objectKey = currentSceneBootstrapObjectKey
	}
	var writer packetWriter
	writer.writeUint16(1)
	writer.writeUint16(currentSceneOp9StableSceneValue)
	writer.writeUint16(objectKey)
	server := byte(currentSceneObjectRoute)
	if len(serverRoute) != 0 {
		server = serverRoute[0]
	}
	writer.writeByte(server)
	writer.writeByte(currentSceneObjectContext)
	writer.writeByte(0)
	writer.writeByte(currentSceneOp9ActorDisplayKind)
	writer.writeByte(currentSceneObjectRoute)
	writer.writeByte(currentSceneObjectContext)
	writer.writeRawDstr(rosterNameBytes(actor.Name))
	writer.writeByte(actor.Job)
	writer.writeByte(shape.ActorAux)
	writer.writeUint32(0)
	writer.writeByte(actor.Grow)
	writer.writeUint16(0)
	writer.writeByte(0)
	writer.writeByte(0)
	writer.writeUint16(0)
	if official249 {
		writer.writeByte(0) // actor metadata+0x55 before native member count
	}
	writer.writeByte(byte(len(rows)))
	for _, row := range rows {
		writer.writeByte(row.Slot)
		writer.writeUint16(row.UserID)
		// 副本里 owner(0) 永远不等于客户端所选频道，所以每一行都带**跨频道内联身份块**
		// （job/level/grow/0 + DSTR 名字）——厂商两条夹具都是 16B 行。
		writer.writeByte(row.Job)
		writer.writeByte(row.Level)
		writer.writeByte(row.Grow)
		writer.writeByte(0)
		if official249 {
			writer.writeByte(0)
		}
		writer.writeRawDstr(rosterNameBytes(row.Name))
		if official249 {
			writer.writeRawDstr(nil)
			writer.writeByte(0)
		}
		writer.writeByte(0)
		writer.writeByte(shape.RowHPPercent)
		writer.writeByte(shape.RowRoomFlag)
	}
	writer.writeByte(0)
	writer.writeByte(hostSlot)
	writer.writeByte(0)
	writer.writeByte(0)
	return writer.bytes()
}

// dungeonPartyRoomSlotTableMaxRows 是槽位表的行数上限（与 op9 名册的 8 行一致）。
const dungeonPartyRoomSlotTableMaxRows = 8

// dungeonStartMapFatigueEnv 是"start-map 提交后补一条 op36 fatigue 状态"的开关。
//
// 厂商 `packet_order` 里 op36 紧邻 op9 之前（`…-ff-op36-op9-room-slot-table`），
// 实测 body 是 10B / 五个 u16（`:5399` = `01 00 9c 00 00 00 00 00 00 00` ⇒ used=1、limit=156），
// 与我们既有的 `buildCurrentFatigueBody` 同形（`select_character_ack.go:176`）。
// 我们此前只在选图（op15）阶段发过一次 fatigue，op29 提交之后**没有**这一条。
//
//	`vendor`（默认，零值）= 每次 start-map（op29）提交、且本队在 ≥2 人组队副本里时补一条；
//	`legacy` = 不发（逐字节回到改动前）。
//
// 诚实标注：厂商日志里 op36 出现 70 次（单人会话也有），我们**只在组队副本的 start-map
// 收尾**补，单人路径一个字节都不动（没有单人 start-committed 的夹具可依）。
const dungeonStartMapFatigueEnv = "DNFBRIDGE_PARTY_DUNGEON_START_MAP_FATIGUE"

const (
	dungeonStartMapFatigueValueVendor = "vendor"
	dungeonStartMapFatigueValueLegacy = "legacy"
)

// dungeonStartMapFatigueMode 是 dungeonStartMapFatigueEnv 的归一化取值。
type dungeonStartMapFatigueMode int

const (
	// dungeonStartMapFatigueVendor 是默认档（零值）= 厂商行为：start-map 收尾补一条 op36。
	dungeonStartMapFatigueVendor dungeonStartMapFatigueMode = iota
	// dungeonStartMapFatigueLegacy = 不发（逐字节回退）。
	dungeonStartMapFatigueLegacy
)

func (mode dungeonStartMapFatigueMode) String() string {
	if mode == dungeonStartMapFatigueLegacy {
		return dungeonStartMapFatigueValueLegacy
	}
	return dungeonStartMapFatigueValueVendor
}

// readDungeonStartMapFatigueMode 解析 DNFBRIDGE_PARTY_DUNGEON_START_MAP_FATIGUE。
// **默认 vendor（= 厂商行为）**：空/未设置/非法值都是 vendor。
func readDungeonStartMapFatigueMode(value string) dungeonStartMapFatigueMode {
	if strings.EqualFold(strings.TrimSpace(value), dungeonStartMapFatigueValueLegacy) {
		return dungeonStartMapFatigueLegacy
	}
	return dungeonStartMapFatigueVendor
}

// dungeonStartMapFatigue 返回当前 Service 的归一化档位（零值 options = vendor）。
func (s *Service) dungeonStartMapFatigue() dungeonStartMapFatigueMode {
	if s == nil {
		return dungeonStartMapFatigueVendor
	}
	return s.options.dungeonStartMapFatigue
}

// sendDungeonStartMapFatigue 在 start-map（op29）提交之后补发一条 class0/op36 fatigue 状态。
// 与厂商同序：它落在 op9 房间槽位表**之前**。body 完全复用既有 `buildCurrentFatigueBody`。
func (s *Service) sendDungeonStartMapFatigue(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if s.dungeonStartMapFatigue() != dungeonStartMapFatigueVendor {
		s.logGameEvent(session, "game-party-dungeon-start-map-fatigue-skipped",
			"source", source,
			"char_id", session.selectedCharacterID,
			"mode", s.dungeonStartMapFatigue().String(),
			"reason", "mode_suppresses_the_start_map_fatigue_refresh")
		return nil
	}
	state := s.runtimePartyStateForSession(session)
	if state.PartyID <= 0 || len(runtimePartyMembers(state)) < 2 {
		session.dungeon.mu.Lock()
		managed := session.dungeon.runtime != nil && session.dungeon.runtime.fatigueRooms != nil
		session.dungeon.mu.Unlock()
		if !managed {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	charID, _, character, hasCharacter := s.selectedCharacterForEnter(ctx, session)
	if charID == 0 {
		charID = session.selectedCharacterID
	}
	body := buildCurrentFatigueBody(character, hasCharacter)
	s.logGameEvent(session, "game-party-dungeon-start-map-fatigue-send",
		"conn_id", session.connID,
		"source", source,
		"party_id", state.PartyID,
		"char_id", charID,
		"msg_id", currentFatigueMsgID,
		"classification", 0,
		"body_len", len(body),
		"fatigue_used", binary.LittleEndian.Uint16(body[0:2]),
		"fatigue_limit", binary.LittleEndian.Uint16(body[2:4]),
		"position", "op29_then_op36_then_op9_room_slot_table",
		"body_source", "current_exe_sub_1D7ABE0_fatigue_five_u16",
		"packet_order", dungeonPartyRoomSlotTableVendorPacketOrder,
		"vendor_packet_order", dungeonPartyRoomSlotTableVendorPacketOrder)
	return s.sendGameUpperRawClass(session, currentFatigueMsgID, body, 0)
}

// dungeonPartyRoomSlotTableVendorPacketOrder 是厂商写下的 start-map 顺序串（对照字段）。
const dungeonPartyRoomSlotTableVendorPacketOrder = "all-members-through-op28-then-op800-all-op29-optional-trap-event-op312-ff-op36-op9-room-slot-table"

// dungeonPartyRoomSlotTableVendorBodySource 是厂商日志里 op9 的 body_source 字面量。
const dungeonPartyRoomSlotTableVendorBodySource = "current_exe_sub_1D64CA0_kind0_room_slot_table_after_all_party_actors_extended_rows"

// sendDungeonPartyRoomSlotTable 在 start-map（op29）提交之后，给**本会话**补发一条
// class0/op9 kind0 房间槽位表（形状 = 厂商夹具）。
//
// 每个成员各发一条、各写自己的 object_key / 名字 / 自己那一行（与厂商 conn59/conn61 同形）；
// 只对 ≥2 人队伍发（厂商的 `game-party-dungeon-start-committed` 只出现在组队副本里，
// 单人副本没有夹具，不去猜）。
func (s *Service) sendDungeonPartyRoomSlotTable(
	session *gameSession,
	runtime *runtimeDungeonState,
	scene worldmap.DungeonRoomScene,
	trigger dungeonPartyRoomSlotTableTrigger,
	source string,
) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	mode := s.dungeonPartyRoomSlotTable()
	if !mode.sendsFor(trigger) {
		s.logGameEvent(session, "game-party-dungeon-room-slot-table-skipped",
			"source", source,
			"trigger", string(trigger),
			"char_id", session.selectedCharacterID,
			"mode", mode.String(),
			"reason", "mode_suppresses_this_trigger")
		return nil
	}
	state := s.runtimePartyStateForSession(session)
	members := runtimePartyMembers(state)
	if state.PartyID <= 0 || len(members) < 2 {
		s.logGameEvent(session, "game-party-dungeon-room-slot-table-skipped",
			"source", source,
			"trigger", string(trigger),
			"char_id", session.selectedCharacterID,
			"party_id", state.PartyID,
			"member_count", len(members),
			"mode", mode.String(),
			"reason", "party_below_two_members_has_no_room_slot_table_sample")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	actorIdentity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		// 同角色的第二条连接（辅助/替换 socket）不是角色目录 owner，但这仍然是这个角色
		// 的一次合法进图：按会话自己的角色取数据，不再因为目录归属而整张表跳过。
		// 现场症状"组队进图少一包"里就有这一条。
		if session.selectedCharacterID == 0 {
			s.logGameEvent(session, "game-party-dungeon-room-slot-table-skipped",
				"source", source,
				"trigger", string(trigger),
				"char_id", session.selectedCharacterID,
				"mode", mode.String(),
				"reason", "receiving_session_character_not_bound")
			return nil
		}
		actorIdentity = boundGameSessionCharacter{
			session:    session,
			character:  session.selectedCharacterID,
			generation: session.characterGeneration,
		}
	}
	charID, charName, character, hasCharacter := s.characterForEnter(ctx, actorIdentity.session, actorIdentity.character)
	if charID == 0 {
		charID = session.selectedCharacterID
	}
	rows := make([]dungeonPartyRoomSlotTableRow, 0, len(members))
	hostSlot := byte(0)
	for _, member := range members {
		if member.UserID == 0 {
			continue
		}
		memberIdentity, found := actorIdentity, true
		if member.UserID != charID {
			memberIdentity, found = s.onlineGameSessionCharacterSnapshot(member.UserID)
		}
		if !found {
			continue
		}
		memberID, memberName, memberCharacter, memberHasCharacter := s.characterForEnter(
			ctx,
			memberIdentity.session,
			memberIdentity.character,
		)
		if memberID != member.UserID {
			continue
		}
		memberLevel := byte(1)
		memberJob := byte(0)
		memberGrow := byte(0)
		if memberHasCharacter {
			if memberCharacter.Level > 0 && memberCharacter.Level < 256 {
				memberLevel = byte(memberCharacter.Level)
			}
			memberJob = byte(numericCharacterStat(memberCharacter.Job))
			memberGrow = characterGrowTypeByte(memberCharacter)
		}
		if member.UserID == state.UserID {
			hostSlot = member.Slot
		}
		rows = append(rows, dungeonPartyRoomSlotTableRow{
			Slot:   member.Slot,
			UserID: member.UserID,
			Job:    memberJob,
			Level:  memberLevel,
			Grow:   memberGrow,
			Name:   memberName,
		})
		if len(rows) == dungeonPartyRoomSlotTableMaxRows {
			break
		}
	}
	if len(rows) < 2 {
		s.logGameEvent(session, "game-party-dungeon-room-slot-table-skipped",
			"source", source,
			"trigger", string(trigger),
			"char_id", charID,
			"party_id", state.PartyID,
			"resolved_rows", len(rows),
			"mode", mode.String(),
			"reason", "fewer_than_two_resolved_party_rows")
		return nil
	}
	actorJob := byte(0)
	actorGrow := byte(0)
	if hasCharacter {
		actorJob = byte(numericCharacterStat(character.Job))
		actorGrow = characterGrowTypeByte(character)
	}
	objectKey := currentSceneActorObjectKey(charID)
	body := buildDungeonPartyRoomSlotTableBodyForProfile(
		dungeonPartyRoomSlotTableActor{
			ObjectKey: objectKey,
			Name:      charName,
			Job:       actorJob,
			Grow:      actorGrow,
		},
		rows,
		hostSlot,
		vendorDungeonPartyRoomSlotTableShape,
		s.options.currentClientVersion == "official-24.9",
		s.currentSceneActorRoute(),
	)
	room := ""
	mapID := int64(0)
	if runtime != nil && runtime.Session != nil {
		if current, ok := runtime.Session.Scene(); ok {
			room = current.Coordinate.String()
			mapID = current.Map.Map.ID
		}
	}
	if room == "" {
		mapID = scene.Map.Map.ID
		if mapID != 0 || scene.Coordinate.X != 0 || scene.Coordinate.Y != 0 {
			room = scene.Coordinate.String()
		}
	}
	s.logGameEvent(session, "game-party-dungeon-room-slot-table-send",
		"conn_id", session.connID,
		"source", "party_start_room_after_op29_actors",
		"trigger", string(trigger),
		"caller_source", source,
		"party_id", state.PartyID,
		"char_id", charID,
		"object_key", objectKey,
		"owner_channel", currentSceneObjectContext,
		"row_count", len(rows),
		"host_slot", hostSlot,
		"body_len", len(body),
		"msg_id", uint16(dnfenum.CmdPacketRecoverStamina),
		"classification", 0,
		"body_source", dungeonPartyRoomSlotTableVendorBodySource,
		"mode", mode.String(),
		"actor_aux", vendorDungeonPartyRoomSlotTableShape.ActorAux,
		"row_hp_percent", vendorDungeonPartyRoomSlotTableShape.RowHPPercent,
		"row_room_flag", vendorDungeonPartyRoomSlotTableShape.RowRoomFlag,
		"room", room,
		"map_id", mapID,
		"packet_order", dungeonPartyRoomSlotTableVendorPacketOrder,
		"vendor_packet_order", dungeonPartyRoomSlotTableVendorPacketOrder,
		"reason", "vendor_commits_the_start_map_with_one_kind0_room_slot_table_per_member")
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketRecoverStamina), body, 0); err != nil {
		return err
	}
	// 记下"本次 start-map 提交，这个 actor 已经有一条 kind0 槽位表上线了"：进图那一次
	// 提交里既有的 `…_post_start_map` 名册帧据此让位（见 dungeonPartyEntrySlotTableEnv）。
	session.dungeonPartySlotTableSentOnStartMap = true
	return nil
}
