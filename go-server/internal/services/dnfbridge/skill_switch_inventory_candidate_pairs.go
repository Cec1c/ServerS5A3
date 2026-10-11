package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// errSkillSwitchCandidatePairsCount 表示候选表的 pair 数落在 u8 count 之外
// （0 或 > 255）：构造不出来就一个字节都不发。
var errSkillSwitchCandidatePairsCount = errors.New("skill switch candidate pair count does not fit the client u8 count")

// 「Buff 强化」换装栏**下行候选对**实验（`DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS`）。
//
// # 为什么需要这个实验
//
// 客户端 1428 的入站解析器（DNF.exe RVA `0x565660`，逐条指令见
// `_dist/buff-skill-switch-diagnosis.md` §2.2）形状已经在活进程里复核：
//
//	body := u8 count + count × ( u32 key (LE), u32 value (LE) )   // 1 + 8*count 字节
//
// 但 `(key,value)` **填什么**没有拿到。只读实机测量
// （`_dist/buff-pairs-measurement.md` §6）给出两条候选，结论是：
//
//   - **候选 B「slot → 源槽位」= family-2 `(12,54)(14,62)…`：已证伪**，
//     1 387 MB 全内存扫描 `(12,54)` / `(14,62)` **0 命中**。**永远不要发这两组。**
//   - **候选 A「恒等 slot→slot」**：实机里真实存在**恰好 12 项**的
//     `std::map<u32,u32>`（key == value）：
//     `(12,12)(14,14)(15,15)(16,16)(17,17)(18,18)(19,19)(21,21)(22,22)(23,23)(25,25)(26,26)`。
//     项数与 `slot_count=12` 吻合、值域落在换装 slot 值域内、与客户端自己发的
//     family-1 上行同构 ⇒ **结构上最像**，但**没有证据**它就是 manager 329 那张 map
//     （只读环境走不完 `call 0x1c45ce0 → 0x1c3f590 → __RTDynamicCast` 那条链）。
//
// 所以本文件是**实验，不是修复**：默认关（`off`），只有显式
// `DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS=identity12` 才发一帧。
//
// # 安全性
//
// 帧**按构造**就是客户端解析器要求的形状（`u8 count + count×(u32,u32)`，
// count=12 ⇒ 1+8*12 = 97 字节），所以：
//
//   - 它**必然**通过既有的形状闸门 `skillSwitchInventoryFrameShapeValid`
//     （发送前还会再自校验一次，不通过就一个字节都不发）；
//   - 形状合法 ⇒ 即使"恒等对"是错的，客户端也只是写进一张没用的 map，
//     **不会**像改动前那批 6 字节回放帧那样把入站流读错位（读原语无边界检查）。
//
// 边界沿用既有投影点（`sendSkillSwitchInventoryProjection`，由
// `DNFBRIDGE_SKILL_SWITCH_INVENTORY` 决定是否投影），所以操作者只要**重新选角/进城**
// 就会命中；每个边界只发**一帧**。
const skillSwitchCandidatePairsEnv = "DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS"

const (
	// skillSwitchCandidatePairsOff 是**默认档**：不发任何实验帧，逐字节等于改动前。
	skillSwitchCandidatePairsOff = "off"
	// skillSwitchCandidatePairsIdentity12 是候选 A：12 组恒等对（key == value）。
	skillSwitchCandidatePairsIdentity12 = "identity12"
	// 候选 B/E：见下面 skillSwitchCandidatePairSets 里各自的依据注释。
	// ★ 不变量：任何候选都不得携带 family-2 那些**已被证伪**的值
	//   （54/62/61/57/59/63/58/64/60/56/55）—— 由
	//   TestSkillSwitchCandidatePairTableInvariants 强制（2026-09-30 它拦下过两次违规）。
	skillSwitchCandidatePairsSlotZero      = "slot_zero"
	skillSwitchCandidatePairsIdentityChar6 = "identity_char6"
	// skillSwitchCandidatePairsSlotFlagOne 是候选 D：只保留槽号、value 恒 1。
	skillSwitchCandidatePairsSlotFlagOne = "slot_flag_one"
)

// 实验帧的日志事件名（**具名 + 自带完整 hex**，线上直接 grep 判读）。
const (
	// skillSwitchCandidatePairsSendEvent：实验帧已下发。字段里有 `candidate`、
	// `pair_count`、`body_len`、`pairs`、`body_hex`、`source`（哪条边界）。
	skillSwitchCandidatePairsSendEvent = "game-skill-switch-inventory-candidate-pairs-send"
	// skillSwitchCandidatePairsBlockedEvent：构造/自校验失败 ⇒ 一个字节都没发。
	// 正常路径**不会**出现这条；出现即为 bug（或未来的候选表写错）。
	skillSwitchCandidatePairsBlockedEvent = "game-skill-switch-inventory-candidate-pairs-blocked"
)

// skillSwitchCandidatePair 是一组下行 pair（客户端读作两个 u32 LE）。
type skillSwitchCandidatePair struct {
	Key   uint32
	Value uint32
}

// skillSwitchCandidatePairSets 是**表驱动**的候选集合：加一条新候选 = 这里加一行
// （key 就是 `DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS` 的取值）。
//
// ⚠️ 只允许放**结构上有实机依据**的候选。family-2「slot → 源槽位」
// `(12,54)(14,62)(15,61)…` 已被 1.39 GB 全内存扫描**证伪**（0 命中），
// **禁止**作为新条目加进来。
var skillSwitchCandidatePairSets = map[string][]skillSwitchCandidatePair{
	skillSwitchCandidatePairsIdentity12: {
		{Key: 12, Value: 12},
		{Key: 14, Value: 14},
		{Key: 15, Value: 15},
		{Key: 16, Value: 16},
		{Key: 17, Value: 17},
		{Key: 18, Value: 18},
		{Key: 19, Value: 19},
		{Key: 21, Value: 21},
		{Key: 22, Value: 22},
		{Key: 23, Value: 23},
		{Key: 25, Value: 25},
		{Key: 26, Value: 26},
	},
	// ── 以下是 2026-09-30 新增的三族（都是"结构上"有依据的假设，尚未证实；
	//    每次只开一族，用 buff 强化面板当判据，一族一族排除）────────────────────
	//
	// 共同背景（线上实证，见 _dist/buff-swap-two-fixes-handover.md 追加三）：
	//   resend 那条路**一直在发**合法帧（shape_guard=pass、count=9、body_len=73），
	//   而且就在"选角重进、场景就绪"那一刻发；但 pairs 一直是【恒等对】(12,12)(13,13)…
	//   ⇒ 已证实"帧能到、形状对"，唯一的未知是 (key,value) 的**语义**。
	//
	// 候选 B「slot_zero」= (槽号, 0)：只声明"这个槽已登记"，负载为 0。
	//   依据：恒等对（value = 槽号）已发过、面板不动；若这张表只是登记位图，
	//   value 应当是常量（0 或 1）。**不携带 family-2 的任何值** ⇒ 符合不变量测试。
	"slot_zero": {
		{Key: 12, Value: 0},
		{Key: 14, Value: 0},
		{Key: 15, Value: 0},
		{Key: 16, Value: 0},
		{Key: 17, Value: 0},
		{Key: 18, Value: 0},
		{Key: 19, Value: 0},
		{Key: 20, Value: 0},
		{Key: 21, Value: 0},
		{Key: 22, Value: 0},
		{Key: 23, Value: 0},
	},
	// 候选 E「identity_char6」= 恒等对，但取 char 6【真实登记的那 11 个槽】
	//   （identity12 用的是 12,14,15,16,17,18,19,21,22,23,25,26 —— 与 char 6 的
	//   11 个真实槽不完全一致，多带 25/26、少带 20）。依据：把"集合本身选错"
	//   这个变量消掉。**同样不携带 family-2 的值。**
	"identity_char6": {
		{Key: 12, Value: 12},
		{Key: 14, Value: 14},
		{Key: 15, Value: 15},
		{Key: 16, Value: 16},
		{Key: 17, Value: 17},
		{Key: 18, Value: 18},
		{Key: 19, Value: 19},
		{Key: 20, Value: 20},
		{Key: 21, Value: 21},
		{Key: 22, Value: 22},
		{Key: 23, Value: 23},
	},
	// 候选 F「slot_itemid_char6」= key 换装槽、value =【物品 ID】。
	//   依据：现场实测（2026-09-30 探针）确认 map 里 key=换装槽、value=背包格号，
	//   但客户端据此既填不出左列、也算不出 (+10)。面板要"显示这件装备、并算出它的
	//   buff 强化等级"，更可能需要【物品身份】而不是"背包第几格"。
	//   取值来源：服务端 pair-resolution 现场解析出的 char 6 那 11 件：
	//     0:54=28309[weapon]  0:62=120229[coat]   0:61=240229[shoulder]
	//     0:57=180229[pants]  0:59=360229[shoes]  0:63=300229[waist]
	//     0:58=401127[amulet] 0:64=411127[wrist]  0:60=421127[ring]
	//     0:56=100341845[support]  0:55=100350891[magic stone]
	//   ★ 不携带 family-2 被证伪的那些值（54/62/61/57/59/63/58/64/60/56/55）。
	"slot_itemid_char6": {
		{Key: 12, Value: 28309},
		{Key: 14, Value: 120229},
		{Key: 15, Value: 240229},
		{Key: 16, Value: 180229},
		{Key: 17, Value: 360229},
		{Key: 18, Value: 300229},
		{Key: 19, Value: 401127},
		{Key: 20, Value: 411127},
		{Key: 21, Value: 421127},
		{Key: 22, Value: 100341845},
		{Key: 23, Value: 100350891},
	},
}

// normalizeSkillSwitchCandidatePairsMode 归一化 `DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS`。
//
// 默认（空 / `off` / 未知取值）= `off`：这是实验，默认必须关。
// 只有**候选表里存在的名字**才算开启 ⇒ 加一条候选不需要改这个函数。
func normalizeSkillSwitchCandidatePairsMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == skillSwitchCandidatePairsOff {
		return skillSwitchCandidatePairsOff
	}
	if _, ok := skillSwitchCandidatePairSets[value]; ok {
		return value
	}
	return skillSwitchCandidatePairsOff
}

// skillSwitchCandidatePairsMode 返回归一化后的档位；零值（测试里的 Service 字面量）
// 一律 `off` —— 默认必须逐字节等于今天。
func (s *Service) skillSwitchCandidatePairsMode() string {
	if s == nil || strings.TrimSpace(s.options.skillSwitchCandidatePairsMode) == "" {
		return skillSwitchCandidatePairsOff
	}
	return normalizeSkillSwitchCandidatePairsMode(s.options.skillSwitchCandidatePairsMode)
}

// buildSkillSwitchCandidatePairsBody 构造**客户端 1428 入站形状**的包体：
// `u8 count` + `count × (u32 key LE, u32 value LE)`。
//
// 形状来源是客户端自己的解析器（DNF.exe RVA 0x565660），不是推测：
// `count` 之后是 `count` 组 `ReadU32` 对，`map[key] = value`。
// pair 数超过 u8 ⇒ 返回错误（构造失败一个字节都不发，见调用方）。
func buildSkillSwitchCandidatePairsBody(pairs []skillSwitchCandidatePair) ([]byte, error) {
	if len(pairs) == 0 || len(pairs) > 0xFF {
		return nil, errSkillSwitchCandidatePairsCount
	}
	body := make([]byte, 1+8*len(pairs))
	body[0] = byte(len(pairs))
	offset := 1
	for _, pair := range pairs {
		binary.LittleEndian.PutUint32(body[offset:offset+4], pair.Key)
		binary.LittleEndian.PutUint32(body[offset+4:offset+8], pair.Value)
		offset += 8
	}
	return body, nil
}

// skillSwitchCandidatePairsText 是给日志用的确定性 pair 列表（便于与报告逐对比对）。
func skillSwitchCandidatePairsText(pairs []skillSwitchCandidatePair) string {
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, "("+strconv.FormatUint(uint64(pair.Key), 10)+
			","+strconv.FormatUint(uint64(pair.Value), 10)+")")
	}
	return strings.Join(parts, "")
}

// sendSkillSwitchCandidatePairs 在**同一条合法投影边界**上发一帧候选对实验帧。
//
// 默认档 `off`：直接返回、一个字节都不发（逐字节等于改动前）。
// 实验档：先按构造，再自校验形状，通过才发；任一环节不通过都只记
// `...-candidate-pairs-blocked` 且**零字节**。
func (s *Service) sendSkillSwitchCandidatePairs(session *gameSession, source string, characterID string) error {
	if s == nil || session == nil {
		return nil
	}
	mode := s.skillSwitchCandidatePairsMode()
	pairs, ok := skillSwitchCandidatePairSets[mode]
	if !ok || len(pairs) == 0 {
		return nil
	}
	body, err := buildSkillSwitchCandidatePairsBody(pairs)
	if err != nil {
		s.logPacketEvent(skillSwitchCandidatePairsBlockedEvent,
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"candidate", mode,
			"reason", "candidate_pair_build_failed",
			"build_error", err)
		return nil
	}
	// 自校验：实验帧必须**按构造**满足客户端 1428 入站形状（= 既有形状闸门的判定式）。
	// 不通过 ⇒ 一个字节都不发：实验失败可以接受，污染客户端入站流不可以。
	if !skillSwitchInventoryFrameShapeValid(body) {
		s.logPacketEvent(skillSwitchCandidatePairsBlockedEvent,
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"source", source,
			"candidate", mode,
			"body_len", len(body),
			"count", int(body[0]),
			"expected_body_len", 1+8*int(body[0]),
			"body_hex", hex.EncodeToString(body),
			"reason", "frame_shape_guard_rejected")
		return nil
	}
	if err := s.sendGameUpperRawClass(session, currentSkillSwitchInventoryOpcode, body, 0); err != nil {
		return err
	}
	s.logPacketEvent(skillSwitchCandidatePairsSendEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"scope", skillswitch.Scope(characterID),
		"source", source,
		"candidate", mode,
		"msg_id", currentSkillSwitchInventoryOpcode,
		"classification", 0,
		"count", int(body[0]),
		"pair_count", len(pairs),
		"body_len", len(body),
		"shape_guard", "pass",
		"pairs", skillSwitchCandidatePairsText(pairs),
		"body_hex", hex.EncodeToString(body),
		// 证据状态：**未证实**。恒等 12 对只是结构上最像 manager 329 那张 map 的候选。
		"proof_state", "unproven_candidate",
		"candidate_evidence", "live 12-entry std::map<u32,u32> key==value found; NOT proven to be manager 329's map",
		"falsified_candidate", "family2_slot_to_source_slot_zero_hits_never_send",
		"downlink_count_semantics", "unverified")
	return nil
}
