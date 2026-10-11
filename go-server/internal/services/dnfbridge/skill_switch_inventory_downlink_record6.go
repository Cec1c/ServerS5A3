package dnfbridge

import (
	"encoding/hex"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 本档：把落库的换装记录**按客户端自己的字节形状**原样下发。
//
// 背景（2026-09-30 实机取证，全文见 _dist/buff-swap-two-fixes-handover.md 追加四/五）：
//
//	客户端上行每条记录的线上形状 = 6 字节：
//	    26 00 | 12 00 | 3f 00   ⇒  (u16 scope=0x0026, u8 slot_a, u8 mode, u16 slot_b)
//	其中 `record.Bytes()` 返回的是**包体**（4 字节，`RecordBodySize`），
//	那 2 字节 scope 前缀只在线上出现（`RecordWirePrefixSize`）。
//
//	此前所有下行帧用的是另一种形状 `u8 count + count×(u32,u32)`（`1+8*count`）；
//	`1+6*count`（补回前缀）也已实测下发过（count=15 / body_len=91 / 逐字节同形），
//	**面板左列仍未填** ⇒ 形状假设需要继续用"一次一个变量"的方式排除。
//
// 本档把"形状"做成一个可切换的族，全部默认关：
//
//	DNFBRIDGE_SKILL_SWITCH_DOWNLINK_SHAPE
//	    off（默认，空/未知/其它一律回落到此）  ：零字节、逐字节等于现状
//	    record6                                ：26 00 + 4 字节包体，`1 + 6*count`
//	    record6_p27                            ：27 00 + 4 字节包体，`1 + 6*count`
//	    record6_p00                            ：00 00 + 4 字节包体，`1 + 6*count`
//	    record4                                ：裸 4 字节包体（不带前缀），`1 + 4*count`
//
// 一行回退：删掉该 env 行 + `systemctl restart dnf90`。
const skillSwitchDownlinkShapeEnv = "DNFBRIDGE_SKILL_SWITCH_DOWNLINK_SHAPE"

const (
	// skillSwitchDownlinkShapeOff 是默认档：零字节、逐字节等于改动前。
	skillSwitchDownlinkShapeOff = "off"
	// skillSwitchDownlinkShapeRecord6 是当前档：补回客户端那 2 字节前缀（26 00）。
	skillSwitchDownlinkShapeRecord6 = "record6"
	// skillSwitchDownlinkShapeRecord6P27 / P00 是前缀变体（同一批记录、只换 scope 两字节）。
	skillSwitchDownlinkShapeRecord6P27 = "record6_p27"
	skillSwitchDownlinkShapeRecord6P00 = "record6_p00"
	// skillSwitchDownlinkShapeRecord4 是裸包体变体（不带前缀），`1 + 4*count`。
	skillSwitchDownlinkShapeRecord4 = "record4"
)

// skillSwitchDownlinkRecord6Event：本档的帧已下发（自带逐字节 hex，线上可 grep 判读）。
const skillSwitchDownlinkRecord6Event = "game-skill-switch-inventory-downlink-record6-send"

// skillSwitchDownlinkRecord6SkippedEvent：想发但发不了（没有可用记录 / 条数超 u8）。
const skillSwitchDownlinkRecord6SkippedEvent = "game-skill-switch-inventory-downlink-record6-skipped"

// skillSwitchDownlinkShapePrefix 是各档使用的 2 字节前缀（`record4` 档不用它）。
var skillSwitchDownlinkShapePrefix = map[string][2]byte{
	skillSwitchDownlinkShapeRecord6:    {0x26, 0x00},
	skillSwitchDownlinkShapeRecord6P27: {0x27, 0x00},
	skillSwitchDownlinkShapeRecord6P00: {0x00, 0x00},
}

// normalizeSkillSwitchDownlinkShape 归一化 env 取值；默认（空 / 未知）= off。
func normalizeSkillSwitchDownlinkShape(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case skillSwitchDownlinkShapeRecord6, "6", "record", "raw6":
		return skillSwitchDownlinkShapeRecord6
	case skillSwitchDownlinkShapeRecord6P27, "27":
		return skillSwitchDownlinkShapeRecord6P27
	case skillSwitchDownlinkShapeRecord6P00:
		return skillSwitchDownlinkShapeRecord6P00
	case skillSwitchDownlinkShapeRecord4, "4", "body4":
		return skillSwitchDownlinkShapeRecord4
	default:
		return skillSwitchDownlinkShapeOff
	}
}

// currentSkillSwitchDownlinkShape 直接自读 env（本档刻意不进 options.go，与同族档位一致）。
func currentSkillSwitchDownlinkShape() string {
	return normalizeSkillSwitchDownlinkShape(os.Getenv(skillSwitchDownlinkShapeEnv))
}

// buildSkillSwitchDownlinkRecord6Body 按形状构造包体：
//
//	record6 / record6_p27 / record6_p00 ： `u8 count + count × (prefix2 + body4)` ⇒ `1 + 6*count`
//	record4                            ： `u8 count + count × body4`            ⇒ `1 + 4*count`
//
// 包体逐字节取 `record.Bytes()`，**不重编码、不改字段**（客户端自己说的字节就是真源）。
// 长度既不是 `RecordBodySize` 也不是 `RecordWireSize` 的记录直接跳过并报数
// （宁可少发，也不发一条畸形的）。
func buildSkillSwitchDownlinkRecord6Body(records []skillswitch.Record, shape string) ([]byte, int, int) {
	bare := shape == skillSwitchDownlinkShapeRecord4
	prefix, hasPrefix := skillSwitchDownlinkShapePrefix[shape]
	entries := make([][]byte, 0, len(records))
	skipped := 0
	for _, record := range records {
		raw := record.Bytes()
		var wire []byte
		switch len(raw) {
		case skillswitch.RecordWireSize:
			// 已经是线上形状（legacy wire 布局）：原样用。
			wire = raw
		case skillswitch.RecordBodySize:
			switch {
			case bare:
				wire = raw
			case hasPrefix:
				wire = make([]byte, 0, skillswitch.RecordWireSize)
				wire = append(wire, prefix[:]...)
				wire = append(wire, raw...)
			default:
				skipped++
				continue
			}
		default:
			skipped++
			continue
		}
		entries = append(entries, wire)
	}
	if len(entries) == 0 || len(entries) > 0xFF {
		return nil, len(entries), skipped
	}
	body := make([]byte, 0, 1+len(entries)*skillswitch.RecordWireSize)
	body = append(body, byte(len(entries)))
	for _, wire := range entries {
		body = append(body, wire...)
	}
	return body, len(entries), skipped
}

// sendSkillSwitchDownlinkRecord6 在登记边界上按选定形状下发一条。
//
// 默认档（env 未设 / off）**立即返回 nil**：零字节、零日志、逐字节等于改动前。
// 任何构造失败只记 skipped 事件并返回 nil —— 与同族档位"读失败不能打断登录"的契约一致。
func (s *Service) sendSkillSwitchDownlinkRecord6(
	session *gameSession,
	source string,
	characterID string,
	records []skillswitch.Record,
) error {
	if s == nil || session == nil {
		return nil
	}
	shape := currentSkillSwitchDownlinkShape()
	if shape == skillSwitchDownlinkShapeOff {
		return nil
	}
	body, count, skipped := buildSkillSwitchDownlinkRecord6Body(records, shape)
	if body == nil {
		s.logPacketEvent(skillSwitchDownlinkRecord6SkippedEvent,
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"scope", skillswitch.Scope(characterID),
			"record_count", len(records),
			"usable_count", count,
			"skipped_count", skipped,
			"record_wire_size", skillswitch.RecordWireSize,
			"shape", shape,
			"reason", "no_usable_records_or_count_out_of_u8")
		return nil
	}
	if err := s.sendGameUpperRawClass(session, currentSkillSwitchInventoryOpcode, body, 0); err != nil {
		return err
	}
	perEntry := skillswitch.RecordWireSize
	if shape == skillSwitchDownlinkShapeRecord4 {
		perEntry = skillswitch.RecordBodySize
	}
	s.logPacketEvent(skillSwitchDownlinkRecord6Event,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"scope", skillswitch.Scope(characterID),
		"msg_id", currentSkillSwitchInventoryOpcode,
		"classification", 0,
		"count", count,
		"body_len", len(body),
		"bytes_per_entry", perEntry,
		"record_wire_size", skillswitch.RecordWireSize,
		"shape", shape,
		"body_source", "verbatim_client_uplink_records",
		"body_hex", hex.EncodeToString(body),
		"shape_env", skillSwitchDownlinkShapeEnv,
		"downlink_count_semantics", "unverified")
	return nil
}

// ── 重进边界补发「登记应答」 ────────────────────────────────────────────────
//
// 依据（2026-09-30 实机取证，见 _dist/buff-swap-two-fixes-handover.md 追加六）：
//
//	操作者当场登记 +10 的那一刻，服务端回的**唯一**一帧是
//	    kind=game-skill-switch-inventory-registration-answer-send
//	    source=current_exe_op1428_registration_sentinel
//	    trigger=op1428_sentinel_registration_delimiter
//	    count=15  body_len=121  shape_guard=pass  downlink_shape_gate=len_eq_1_plus_8_times_count
//	    pairs=(3,47)(12,54)(13,13)(14,62)(15,61)(16,57)(17,59)(18,63)(19,58)(20,64)
//	          (21,60)(22,56)(23,55)(25,25)(26,26)
//
//	而全部 60 条 answer 的 `source` **只有这一种** ⇒ 服务端**只在客户端发哨兵时**才回它。
//	选角重进（场景就绪）时客户端不发哨兵 ⇒ 服务端不发 answer ⇒ **面板左列空**。
//
//	本档就是补上这个触发点：在场景就绪边界把**同一帧**再发一次。
//
// 开关：DNFBRIDGE_SKILL_SWITCH_ANSWER_AT_SCENE_READY
//
//	off（默认，空/未知一律回落到此）：什么都不做，逐字节等于现状
//	on                              ：在登记投影边界（重进/场景就绪）补发一次登记应答
//
// 一行回退：删掉该 env 行 + `systemctl restart dnf90`。
const skillSwitchAnswerAtSceneReadyEnv = "DNFBRIDGE_SKILL_SWITCH_ANSWER_AT_SCENE_READY"

// skillSwitchAnswerAtSceneReadyEvent：本档真的补发了一次（自带来源，线上可 grep 判读）。
const skillSwitchAnswerAtSceneReadyEvent = "game-skill-switch-inventory-answer-at-scene-ready"

// skillSwitchAnswerAtSceneReadyEnabled 直接自读 env；默认关（本档刻意不进 options.go）。
func skillSwitchAnswerAtSceneReadyEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(skillSwitchAnswerAtSceneReadyEnv))) {
	case "on", "1", "true", "yes":
		return true
	}
	return false
}

// answerSkillSwitchRegistrationAtSceneReady 在登记投影边界补发一次登记应答。//
// 默认关 ⇒ 立即返回（零字节、一条日志都不写）。
// 开启时复用**同一条** answer 路径（`answerSkillSwitchRegistration`），
// 所以内容与登记时那一帧同源、形状一致（`1+8*count`），不新造编码。
func (s *Service) answerSkillSwitchRegistrationAtSceneReady(session *gameSession, source string) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	if !skillSwitchAnswerAtSceneReadyEnabled() {
		return
	}
	s.logPacketEvent(skillSwitchAnswerAtSceneReadyEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"trigger", "scene_ready_boundary_not_client_sentinel",
		"answer_env", skillSwitchAnswerAtSceneReadyEnv,
		"reused_path", "answerSkillSwitchRegistration")
	// 第三参数语义 = "把这次当作收到了哨兵分隔符" ⇒ 走与登记时完全相同的那条应答路径。
	s.answerSkillSwitchRegistration(session, true)
}

// ── "查看他人信息" 应答的 body hex 记录档 ────────────────────────────────────
//
// 背景（2026-09-30）：点 t3（char 4）的"查看信息"会【闪退】，而 t3 看别人正常。
// 服务端日志给出唯一显著差异：
//
//	t3(char4) 看 t78(char6)： peer-userinfo-response-send body_len=2098 grow_type=33
//	t78(char6) 看 t3(char4)： peer-userinfo-response-send body_len=1028 grow_type=0   ← 闪退侧
//
// 本档只做一件事：把那份 body 的 hex 也记下来，供逐段对比（找出 char 4 缺了哪一段）。
//
// 开关：DNFBRIDGE_PEER_USERINFO_BODY_HEX
//
//	off（默认，空/未知一律回落到此）：记空串，逐字节等于现状
//	on                              ：记 body 的 hex（上限 32 KiB，防止日志爆）
const peerUserInfoBodyHexEnv = "DNFBRIDGE_PEER_USERINFO_BODY_HEX"

// peerUserInfoBodyHexCap 是 hex 记录的字节上限（超过即截断，够看结构）。
const peerUserInfoBodyHexCap = 32768

// peerUserInfoBodyHexForLog 在开关打开时返回 body 的 hex；默认关返回空串。
func peerUserInfoBodyHexForLog(body []byte) string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(peerUserInfoBodyHexEnv))) {
	case "on", "1", "true", "yes":
	default:
		return ""
	}
	if len(body) > peerUserInfoBodyHexCap {
		body = body[:peerUserInfoBodyHexCap]
	}
	return hex.EncodeToString(body)
}
