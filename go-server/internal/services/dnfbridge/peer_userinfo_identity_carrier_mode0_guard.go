package dnfbridge

import (
	"encoding/binary"
	"os"
	"strings"
)

// 本文件是「查看他人信息（城镇交互菜单 → GET_USERINFO mode=3）时客户端闪退」这一轮的
// **服务端落点**。它不改 mode3 体的任何一个字节，只在**发送身份载体之前**做一次
// 「客户端会怎么读这条体」的预演，把会读越界的那条载体**扣下不发**。
//
// ── 现场证据（2026-09-30 复现链，全部来自本机 90CN_trace.log / 服务端 packet_log.txt）──
//
//  1. 被查看方是 char 4（t3）时，**查看方**客户端在服务端发出响应后 0.12–1.3 s 崩：
//
//     exception code=0xC0000005 address=03B10747 access_kind=1
//     access_address=0x001B0000 eip=0x03B10747 esp=0x001AE6DC
//     eax=0x09A429E2 ecx=0x00010007 edx=0x00000003 esi=0x09A029C3 edi=0x001B0000
//     exception stack=000418D3 09A0110F 001AE700 034E3AD9 001AE74C ... 03457C71 ...
//
//     eip 0x03B10747 是 DNF.exe 的 `rep movsd`（memcpy 内联体）；返回地址 0x03457C71 =
//     `sub_3457C50+0x21`，而本仓注释早已记明 `sub_3457C50` 就是「读 u32 长度 + 拷 raw」
//     （userinfo_current_equipment_wire.go:52-55、userinfo_current_modes.go:216-217）；
//     0x034E3AD9 是同一族「定长读行」调用点（current_item_list_page_framing_test.go:13
//     记的正是同一条 eip/返回地址）。⇒ 崩溃形态 = 从包里读到一个长度，然后按那个长度
//     做一次 memcpy，走出了缓冲区。
//
//  2. 崩溃前客户端**一定**走完这一串（9/9 次捕获一致）：
//
//     town-copresence-dispatch phase=before opcode=2 mode=0 owner=0 key=0
//     mode0-owner-compare scene_channel=10 local_owner_channel=0 branch=current-scene-context0-remap
//     local-actor-create phase=before object_key=0
//     local-actor-create phase=after  object_key=0 result=...
//     game-options-apply trigger=local-actor-create ...
//     exception code=0xC0000005 ...
//
//     `opcode=2 mode=0` 表示客户端把一条 **class0 / msg_id=2、首字节 0x00** 的体当成
//     「mode0 场景对象体」处理（钩子源码 client-patch/90CN.cpp:1675-1684：mode=body[0]、
//     owner=body[4]、objectKey=u16@body+0x4C）。
//
//  3. 服务端在这条链上**唯一**会发出「首字节 0x00 的 class0/msg_id=2 体」的地方，就是
//     `handleCurrentPeerUserInfoRequest` 先发的那条 subtype0 **身份载体**
//     （peer_userinfo.go:61-97 → peer_userinfo_identity_carrier.go）。它由
//     userinfo_selected_subtype0.go:69-86 写出：
//
//     u8 0x00 | u16 version | u16 charID | u32 nameLen | name | job | grow | level | ...
//
//     与真正的 mode0 体（character_scene_object.go:56-63 + character_scene_object_tail.go:18-125）
//     逐字段对照 ⇒ **前 5 个字节形状相同、语义完全不同**：载体只被客户端读成
//     「mode0 体」时才危险，而这正是现场 9/9 次崩溃的入口。
//
//  4. 为什么只有**部分角色**崩（现场：target=char 4「t3」崩、target=char 6「test78」不崩）：
//     客户端把载体当 mode0 读时用到的两个偏移落在**第 3 条外观行**（23 字节/行）里：
//
//     body[0x4C..0x4D] = 外观行[13..14]（= state / linkItemID 低字节）→ objectKey
//     body[0x4E..0x51] = 外观行[14..17]（= linkItemID）              → 名字 u32 长度
//
//     · char 4（t3）：外观行 state=0、linkItemID=0 ⇒ 名字长度 0 ⇒ 客户端**继续往下走**
//     到 19 字节/行的「装备摘要」表，把外观字节当成行数与 u32 raw 长度 ⇒ 越读越远
//     ⇒ 按垃圾长度 memcpy ⇒ 崩（现场捕获的 ecx=0x00010007 就是那次拷贝的剩余计数）。
//     · char 6（test78）：外观行 linkItemID≠0/state≠0 ⇒ 名字长度是个越界的大值
//     ⇒ 客户端自己的 dstr 读端当场停手 ⇒ 不崩（现场 425 字节载体 0 次崩溃）。
//
//     这条 A/B 就是本档的判据：**预演同一条 walk，看它会不会走出这条体**。
//
// ── 开关（消费点自读 env，不进 options.go，与同包既有档位同口径）────────────────
//
//	DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER_MODE0_GUARD
//	    off（默认，含未设置）= **逐字节等于现状**：不预演、不扣包、不写事件。
//	    on   = 预演 mode0 walk；只有「名字长度读得下、但装备摘要表会读出体」时才扣包
//	           （= char 4 那一种）。名字长度本身越界的载体（char 6 那一种）照旧发出，
//	           因为现场证明那种载体不崩。
//	    strict = 只要 mode0 walk 有任何一步落在体外就扣包（更保守：把 char 6 那一种也扣下）。
//
// 扣包时写具名事件 `game-peer-userinfo-identity-carrier-mode0-guard-skip`，字段里带
// offset/name_len/row_count/raw_len/body_len，让「这次为什么没发载体」在同一行可判读。
//
// **一行回退**：把该 env 删掉（或写 off）+ `systemctl restart dnf90`。
const peerUserInfoIdentityCarrierMode0GuardEnv = "DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER_MODE0_GUARD"

const (
	peerUserInfoIdentityCarrierMode0GuardOff    = "off"
	peerUserInfoIdentityCarrierMode0GuardOn     = "on"
	peerUserInfoIdentityCarrierMode0GuardStrict = "strict"
)

// mode0 视角下的固定偏移。全部来自本仓自己的 mode0 写出顺序（**不是**反汇编猜测）：
//
//	character_scene_object.go:56-63   u8 0 | u16 1 | u8 route | u8 owner | entry
//	character_scene_object.go:102-108 raw 0x47 字节 | u16 objectKey | 名字 dstr
//	character_scene_object_tail.go:27-55 job/grow/level + 3 字节 + 装备摘要表
//	character_scene_object_tail.go:160  装备摘要每行 19 字节（+ 行内 u32 raw 长度前缀的 raw）
const (
	peerUserInfoMode0ObjectKeyOffset  = 0x4C
	peerUserInfoMode0NameLengthOffset = 0x4E
	// 名字之后的固定部分：job(1) + grow(1) + level(1) + sub_20038C0(1) + sub_2003970(1) + v112[1](1)
	peerUserInfoMode0PostNameFixedBytes = 6
	// 装备摘要每行固定字节数（character_scene_object_tail.go:160 同一个常量）。
	peerUserInfoMode0EquipRowBytes = currentSceneObjectEquipSummaryRowBytes
)

// peerUserInfoIdentityCarrierMode0GuardMode 解析档位。默认 = off（逐字节等于现状）。
func peerUserInfoIdentityCarrierMode0GuardMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(peerUserInfoIdentityCarrierMode0GuardEnv))) {
	case peerUserInfoIdentityCarrierMode0GuardOn, "1", "true", "yes", "enable", "enabled":
		return peerUserInfoIdentityCarrierMode0GuardOn
	case peerUserInfoIdentityCarrierMode0GuardStrict, "all", "any":
		return peerUserInfoIdentityCarrierMode0GuardStrict
	default:
		return peerUserInfoIdentityCarrierMode0GuardOff
	}
}

func peerUserInfoIdentityCarrierMode0GuardEnabled() bool {
	return peerUserInfoIdentityCarrierMode0GuardMode() != peerUserInfoIdentityCarrierMode0GuardOff
}

// peerUserInfoIdentityCarrierMode0Walk 是"客户端把这条 subtype0 载体当 mode0 体读"的
// 预演结果。字段只为判读事件服务，不参与任何下行字节构造。
type peerUserInfoIdentityCarrierMode0Walk struct {
	// Safe 为真表示这次预演**没有**发现"读越界"的一步。
	Safe bool
	// Skip 为真表示调用方必须扣下这条载体（Safe=false 且档位要求扣）。
	Skip bool
	// Reason 是第一步越界的位置名（判读用；Safe=true 时为空）。
	Reason string
	// 下面四个值用于把"为什么扣"写进事件。
	ObjectKey uint16
	NameLen   uint32
	RowCount  int
	RawLen    uint32
	// Offset 是第一步越界发生在这条体里的字节偏移。
	Offset int
}

// peerUserInfoIdentityCarrierMode0WalkFor 预演一次 mode0 解析。
//
// 只读字节，不查库、不改任何状态。规则与理由见文件头第 4 条：
//
//  1. 体短于 0x4E+4：客户端连 objectKey 都不读（client-patch/90CN.cpp:1678 的
//     `bodyLength >= 0x4E` 门槛）⇒ 不构成 mode0 解析 ⇒ Safe。
//  2. 名字长度 > 体内剩余：客户端 dstr 读端停在越界长度上（现场 char 6 的 A/B）
//     ⇒ on 档判 Safe（照发），strict 档判不安全（扣下）。
//  3. 名字长度读得下：继续走 19 字节/行的装备摘要表；任何一步（行头 / u32 raw 长度 /
//     raw 本体 / 行尾 10 字节）落到体外 ⇒ 客户端会按垃圾长度 memcpy ⇒ **不安全**。
func peerUserInfoIdentityCarrierMode0WalkFor(body []byte) peerUserInfoIdentityCarrierMode0Walk {
	out := peerUserInfoIdentityCarrierMode0Walk{Safe: true}
	if len(body) < peerUserInfoMode0NameLengthOffset+4 {
		return out
	}
	out.ObjectKey = binary.LittleEndian.Uint16(body[peerUserInfoMode0ObjectKeyOffset : peerUserInfoMode0ObjectKeyOffset+2])
	out.NameLen = binary.LittleEndian.Uint32(body[peerUserInfoMode0NameLengthOffset : peerUserInfoMode0NameLengthOffset+4])

	nameStart := peerUserInfoMode0NameLengthOffset + 4
	remaining := len(body) - nameStart
	if int64(out.NameLen) > int64(remaining) {
		out.Safe = false
		out.Reason = "mode0_name_length_out_of_body"
		out.Offset = peerUserInfoMode0NameLengthOffset
		return out
	}

	pos := nameStart + int(out.NameLen) + peerUserInfoMode0PostNameFixedBytes
	if pos >= len(body) {
		out.Safe = false
		out.Reason = "mode0_equip_summary_count_out_of_body"
		out.Offset = pos
		return out
	}
	out.RowCount = int(body[pos])
	pos++
	for index := 0; index < out.RowCount; index++ {
		if pos+5 > len(body) {
			out.Safe = false
			out.Reason = "mode0_equip_row_header_out_of_body"
			out.Offset = pos
			return out
		}
		pos += 5
		if pos+4 > len(body) {
			out.Safe = false
			out.Reason = "mode0_equip_row_raw_length_out_of_body"
			out.Offset = pos
			return out
		}
		out.RawLen = binary.LittleEndian.Uint32(body[pos : pos+4])
		pos += 4
		if uint64(out.RawLen) > uint64(len(body)-pos) {
			out.Safe = false
			out.Reason = "mode0_equip_row_raw_body_out_of_body"
			out.Offset = pos
			return out
		}
		pos += int(out.RawLen)
		if pos+10 > len(body) {
			out.Safe = false
			out.Reason = "mode0_equip_row_tail_out_of_body"
			out.Offset = pos
			return out
		}
		pos += 10
	}
	return out
}

// peerUserInfoIdentityCarrierMode0GuardSkip 回答"这条载体要不要扣下"。
// 默认档（off）永远返回 skip=false ⇒ 下发逐字节等于改动前。
func peerUserInfoIdentityCarrierMode0GuardSkip(body []byte) peerUserInfoIdentityCarrierMode0Walk {
	walk := peerUserInfoIdentityCarrierMode0WalkFor(body)
	switch peerUserInfoIdentityCarrierMode0GuardMode() {
	case peerUserInfoIdentityCarrierMode0GuardOn:
		// 只扣"名字长度读得下、但装备摘要表会读出体"的那一族（现场 char 4）。
		walk.Skip = !walk.Safe && walk.Reason != "mode0_name_length_out_of_body"
	case peerUserInfoIdentityCarrierMode0GuardStrict:
		walk.Skip = !walk.Safe
	default:
		walk.Skip = false
	}
	return walk
}

// peerUserInfoIdentityCarrierMode0GuardFields 生成扣包事件的固定字段集。
func peerUserInfoIdentityCarrierMode0GuardFields(walk peerUserInfoIdentityCarrierMode0Walk, bodyLen int) []any {
	return []any{
		"env", peerUserInfoIdentityCarrierMode0GuardEnv,
		"switch", peerUserInfoIdentityCarrierMode0GuardMode(),
		"reason", walk.Reason,
		"offset", walk.Offset,
		"object_key", walk.ObjectKey,
		"name_len", walk.NameLen,
		"row_count", walk.RowCount,
		"raw_len", walk.RawLen,
		"body_len", bodyLen,
		"mode0_equip_row_bytes", peerUserInfoMode0EquipRowBytes,
		"source", "peer_userinfo_identity_carrier_mode0_walk_preview",
	}
}
