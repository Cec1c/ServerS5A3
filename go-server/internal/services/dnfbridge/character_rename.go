// character_rename.go 负责选角界面「修改角色名」请求（class1/op135
// ENUM_CMDPACKET_CHANGE_CHARAC_NAME）的**具名判读**。
//
// # 为什么只判读、不回包、不写库
//
// 现场根因（2026-09-28，_dist/rename-delete-diagnosis.md §2.5）：op135 在本服务端
// **完全没有 handler** —— 请求落进 game_dispatch_legacy 的 default →
// handleAlignedGameCommand 返回"未登记" → 打一条匿名 "unhandled latest game command"
// → return nil，**一个下行字节都不发**。客户端在等一个永远不会到的改名结果，
// 所以界面上「改名不生效」且不报错、不断线。
//
// 但**厂商参考实现也没有这条 handler**（§2.4：GameProtocolHandler.cs 的注册表里
// 没有 d[0x0087]，全仓 `Handle_ENUM_CMDPACKET_CHANGE_CHARAC_NAME` 零命中；
// NOTI 侧只有宠物改名 RENAME_CREATURE）。因此**不存在可以照抄的 S2C 回包形态**，
// 按项目规则（AGENTS.md「Current EXE behavior is authoritative / 不为不确定的包
// 布局凭空造包」）不许发明一个回包。阶段 2 的回包形态必须先按 §2.7 抓包/反汇编取证。
//
// 本文件只做**阶段 1**：把"静默丢弃"换成"具名可判读"，即
//
//	解析 C2S op135 → 打一条带逐字段解码的具名事件 → **零回包**（与今天逐字节相同）。
//
// 客户端 op135 那条链路的落库与回包**不在本文件范围内**；操作者今天要改名请用
// GM 面板的 `POST /api/characters/{id}/rename`（gmtool/api_character_rename.go，
// 移植上游 A21 GM 工具，零协议发明）。
//
// # 请求体形态：来自现场抓包，不是推测
//
// packet_log.txt:60954 / :60966 两次 op135（体长均为 27，两次体逐字节相同）：
//
//	018700280000006b1f03f7140002 0b000000 7465737437385f636f7079 06000000 746573743838 01
//	└──── 17 字节外层头 ────┘ │  └ u32=11 ┘ └ "test78_copy"(11B) ┘ └ u32=6 ┘ └ "test88"(6B)┘ │
//	                          偏移 0：0x02                            偏移 26：0x01
//
// 即：u8 flag | u32 LE 旧名长度 | 旧名字节 | u32 LE 新名长度 | 新名字节 | u8 tail。
// 名字是**原始字节**（客户端本地码页 GBK/GB18030），不是 UTF-16、不是 UTF-8 字符串。
//
// ⚠️ 偏移 0 的 `0x02` 与偏移 26 的 `0x01` **语义未定名**（§2.2 表末两行、§6 第 2 条：
// 疑似 forced-rename reason / flag）。本文件只如实回报它们的原始数值，并在事件里标
// `*_semantics="unpinned"`，绝不按猜测解释。
//
// # 开关
//
//	`DNFBRIDGE_CHARACTER_RENAME`
//	  未设置 / 空 / 任何未知值 = **具名判读档**（本文件生效：解析 + 具名事件 + 零回包）
//	  字面量 `legacy`         = **逐字节回退到今天的行为**：不解析，交回
//	                            handleGameLegacyFallbackCommand（aligned fallback +
//	                            匿名桶日志），同样零回包。
//
// 回退字面量与同类开关一致（gmtool/api_character_delete.go:330 的
// chardelHardModeFromEnv、character_delete.go:66 的 characterDeleteAckLegacyFromEnv：
// 白名单只放"退回旧行为"的字面量）。两档都**不发任何字节**，区别只在日志：
// 这一条是"改行为之前先把行为看清楚"的最小步，A/B 时可用 legacy 档核对。
package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

const (
	// characterRenameLegacyEnv 是 op135 判读开关名。
	characterRenameLegacyEnv = "DNFBRIDGE_CHARACTER_RENAME"

	// characterRenameLegacyValue 是唯一的回退字面量。
	characterRenameLegacyValue = "legacy"

	// characterRenameMinNameBytes / MaxNameBytes 只用于**拒绝明显不合法的长度字段**
	// （不是业务校验：客户端口径的 2..12 字节属于阶段 2 的落库规则）。
	// 上限取 64：dnf_characters.name 是 varchar(64)，比它更长的长度字段一定是
	// 解析漂移或伪造，直接判失败比"照单全收"更安全。
	characterRenameMinNameBytes = 1
	characterRenameMaxNameBytes = 64
)

// characterRenameRequest 是 op135 请求体的逐字节解析结果（形态见文件头）。
type characterRenameRequest struct {
	// Flag 是偏移 0 的字节，语义未定名（疑似被强制改名的 reason）。
	Flag byte
	// OldNameRaw / NewNameRaw 是原始名字节（客户端本地码页）。
	OldNameRaw []byte
	NewNameRaw []byte
	// Tail 是最后一个字节，语义未定名。
	Tail byte
}

// characterRenameLegacyFromEnv 解析 DNFBRIDGE_CHARACTER_RENAME：
// 只有字面量 `legacy`（忽略大小写与首尾空白）回到旧行为，其余一切（含空、未知值）
// 都走具名判读档。
func characterRenameLegacyFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), characterRenameLegacyValue)
}

func characterRenameLegacy() bool {
	return characterRenameLegacyFromEnv(os.Getenv(characterRenameLegacyEnv))
}

// decodeCharacterRenameRequest 解析 op135 请求体。
//
// 严格版：长度字段必须落在 1..64，两段名字都必须完整落在体内，末尾**恰好**剩 1 字节
// tail。任何不符都返回 error（调用方记 game-change-charac-name-parse-failed 并仍然
// 零回包）—— 宁可说"没读懂"，也不要把漂移的偏移当成名字记进日志。
func decodeCharacterRenameRequest(body []byte) (characterRenameRequest, error) {
	if len(body) < 1 {
		return characterRenameRequest{}, fmt.Errorf("body too short: %d", len(body))
	}
	oldName, rest, err := characterRenameReadName(body[1:], "old")
	if err != nil {
		return characterRenameRequest{}, err
	}
	newName, rest, err := characterRenameReadName(rest, "new")
	if err != nil {
		return characterRenameRequest{}, err
	}
	if len(rest) != 1 {
		return characterRenameRequest{}, fmt.Errorf("tail: want exactly 1 byte, got %d", len(rest))
	}
	return characterRenameRequest{
		Flag:       body[0],
		OldNameRaw: oldName,
		NewNameRaw: newName,
		Tail:       rest[0],
	}, nil
}

// characterRenameReadName 读一个 [u32 LE 长度][字节] 名字段，返回名字与剩余字节。
func characterRenameReadName(raw []byte, label string) ([]byte, []byte, error) {
	if len(raw) < 4 {
		return nil, nil, fmt.Errorf("%s name length field missing: %d", label, len(raw))
	}
	length := int(binary.LittleEndian.Uint32(raw[:4]))
	if length < characterRenameMinNameBytes || length > characterRenameMaxNameBytes {
		return nil, nil, fmt.Errorf("%s name length out of range: %d", label, length)
	}
	if 4+length > len(raw) {
		return nil, nil, fmt.Errorf("%s name exceeds body: need=%d have=%d", label, 4+length, len(raw))
	}
	name := append([]byte(nil), raw[4:4+length]...)
	return name, raw[4+length:], nil
}

// characterRenameDecodedName 把原始名字节按客户端码页解成文本（只为日志可读）。
// 解不出来时返回 ""（原始字节仍然会以 *_hex 字段如实记录）。
func characterRenameDecodedName(raw []byte) string {
	name, err := decodeCharacterName(raw)
	if err != nil {
		return ""
	}
	return name
}

// handleGameChangeCharacName 处理 class1/op135 改名请求：解析 + 具名事件 + **零回包**。
//
// 调用方（game_dispatch_legacy.go 的具名 case）已经处理过 legacy 档：那条路不进这里。
// 本函数**没有任何 send* 调用**，也没有任何仓储访问 —— 这是"阶段 1 = 只判读"的
// 代码化；阶段 2（落库 + 回包）必须先有 §2.7 的回包证据才能加。
func (s *Service) handleGameChangeCharacName(session *gameSession, cmd byte, typ uint16, body []byte) error {
	request, err := decodeCharacterRenameRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-change-charac-name-parse-failed",
			"type", typ,
			"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
			"body_len", len(body),
			"body_hex", hex.EncodeToString(body),
			"error", err,
			"reply", "none_s2c_shape_unverified")
		return nil
	}
	oldName := characterRenameDecodedName(request.OldNameRaw)
	newName := characterRenameDecodedName(request.NewNameRaw)
	s.logGameEvent(session, "game-change-charac-name-request",
		"type", typ,
		"runtime_cmd_name", runtimeCmdPacketName(cmd, typ),
		"body_len", len(body),
		"flag", request.Flag,
		"flag_semantics", "unpinned_forced_rename_reason_candidate",
		"old_name", oldName,
		"old_name_hex", hex.EncodeToString(request.OldNameRaw),
		"old_name_bytes", len(request.OldNameRaw),
		"new_name", newName,
		"new_name_hex", hex.EncodeToString(request.NewNameRaw),
		"new_name_bytes", len(request.NewNameRaw),
		"tail", request.Tail,
		"tail_semantics", "unpinned",
		"reply", "none_s2c_shape_unverified",
		"boundary", "phase1_parse_and_log_only_no_write_no_reply",
		"evidence", "_dist/rename-delete-diagnosis.md section 2.2")
	return nil
}
