// character_delete.go 负责选角界面「删除角色」请求（class1/op6
// ENUM_CMDPACKET_DELETE_CHARACTER）的协议适配与落库。
//
// 现场根因（2026-09-28，_dist/rename-delete-diagnosis.md §3）：op6 没有具名路由，
// 请求落进 game_dispatch_legacy 的 default → aligned fallback；请求体解析偏移错 1 字节
// （把 [slot:u16] 当 [slot:u8]，nameLen 读成 2816），解析失败后走 ResponseAllowed=false
// 分支，**一个字节都不回**。客户端在「立即删除」之后死等应答 ⇒ 界面假死。
//
// 回包形态不是发明的，逐条来自厂商 CharacterSelectHandler.cs:927-1013：
//   - 成功 = [0x01][flag=0x00][slot:u16 LE]（:990-1003；同处明写旧 [00][charId]
//     会让客户端判 Error 并把 charId 低字节当 ErrCode 闪退，故绝不使用该形态）；
//   - 失败 = 单字节 {0x02} 坏请求/坏槽位（:931/:942/:954）、
//     {0x15} 名字与槽位不符（:962）、{0x28} 阻塞/异常兜底（:970-973/:1011）；
//   - 删除后**不补发**角色列表（:1005-1006，客户端 Ok 分支自己擦除并前移压缩）。
//
// 落库口径与 GM 面板软删一致（gmtool/api_character_delete.go:227-316）：
// dnf_characters.delete_flag=1 + 槽位挪到负墓碑槽（chardelTombstoneSlot，:776-781），
// 一次 SaveCharacterFields(CharacterFieldBase|CharacterFieldStats) 写回；背包/任务/邮件
// 等以 character_id 为键的关联行**保留**（厂商同样只动 characters 一张表）。
//
// 信道：与已现场验证可用的 op692 回包同一条 game-upper 明文路（16 字节头、
// classification=1）。服务端全部游戏下行只有 game-upper 这一条（诊断 §3.6 信道普查），
// 没有第二条可选。
package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/character"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// errGameDeleteCharacterIDUnavailable：角色号不是正十进制数，无法按 GM 口径腾出墓碑槽。
var errGameDeleteCharacterIDUnavailable = errors.New("game delete character: non-positive character id")

const (
	// characterDeleteAckEnv 是删除角色回包开关名。
	//
	//	未设置 / 空 / 任何未知值 = 修复档（正确解析 + 厂商 ACK + 软删）
	//	字面量 `legacy`           = 逐字节回退到今天的行为（走原 aligned fallback，不回包）
	characterDeleteAckEnv = "DNFBRIDGE_CHARACTER_DELETE_ACK"

	// characterDeleteAckLegacyValue 是唯一的回退字面量。
	characterDeleteAckLegacyValue = "legacy"

	// 厂商 CharacterSelectHandler.cs:931-1011 的结果码。
	characterDeleteAckOK           = 0x01
	characterDeleteAckBadRequest   = 0x02
	characterDeleteAckNameMismatch = 0x15
	characterDeleteAckBlocked      = 0x28

	// characterDeleteDeletedFlag 与 GM 软删同值（gmtool/api_character_delete.go:115）。
	characterDeleteDeletedFlag = 1
)

// characterDeleteAckLegacyFromEnv 解析开关：只有字面量 `legacy`（忽略大小写与首尾空白）
// 回到旧行为，其余一切（含空、未知值）都是修复档。取舍与同类开关一致
// （gmtool/api_character_delete.go:330 chardelHardModeFromEnv：白名单只放回退字面量）。
func characterDeleteAckLegacyFromEnv(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), characterDeleteAckLegacyValue)
}

func characterDeleteAckLegacy() bool {
	return characterDeleteAckLegacyFromEnv(os.Getenv(characterDeleteAckEnv))
}

// handleGameDeleteCharacter 处理 class1/op6 删除角色请求。
//
// 调用方（game_dispatch_legacy.go 的具名 case）已经处理过 legacy 档：那条路不进这里。
func (s *Service) handleGameDeleteCharacter(session *gameSession, body []byte) error {
	// 解析复用 character 模块的同一份解码器（aligned fallback 也走它），
	// 避免桥接层再维护第二份偏移实现导致两处再次漂移。
	req, err := character.DecodeDeleteCharacterRequest(body)
	if err != nil {
		s.logGameEvent(session, "game-delete-character-parse-failed",
			"body_len", len(body),
			"error", err)
		return s.sendGameDeleteCharacterError(session, characterDeleteAckBadRequest)
	}
	s.logGameEvent(session, "game-delete-character-request",
		"body_len", len(body),
		"slot", req.Slot,
		"name", req.Name)

	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		s.logGameEvent(session, "game-delete-character-repository-missing", "slot", req.Slot)
		return s.sendGameDeleteCharacterError(session, characterDeleteAckBlocked)
	}
	accountID := s.accountIDForSession(session)
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()

	records, err := s.listCharacters(ctx, repos, accountID)
	if err != nil {
		s.logGameEvent(session, "game-delete-character-list-failed",
			"account_id", accountID, "slot", req.Slot, "error", err)
		return s.sendGameDeleteCharacterError(session, characterDeleteAckBlocked)
	}
	// 厂商 :951-958 用客户端 0-based 列表索引寻址（list[slotIndex]），越界回 {0x02}。
	slotIndex := int(req.Slot)
	if slotIndex >= len(records) {
		s.logGameEvent(session, "game-delete-character-slot-out-of-range",
			"account_id", accountID, "slot", req.Slot, "roster", len(records))
		return s.sendGameDeleteCharacterError(session, characterDeleteAckBadRequest)
	}
	target := records[slotIndex]
	if !characterDeleteNameMatches(target, []byte(req.Name)) {
		// 名字与槽位不符 ⇒ {0x15}（厂商 :959-963）。这也是"列表下标漂移"的兜底：
		// 下标即使偏了，也只会报名字不符，绝不会删掉另一个角色。
		s.logGameEvent(session, "game-delete-character-name-mismatch",
			"account_id", accountID,
			"slot", req.Slot,
			"request_name", req.Name,
			"character_id", target.CharacterID,
			"record_name", target.Name)
		return s.sendGameDeleteCharacterError(session, characterDeleteAckNameMismatch)
	}

	previousSlot, tombstoneSlot, err := s.softDeleteGameCharacter(ctx, repos, target)
	if err != nil {
		s.logGameEvent(session, "game-delete-character-write-failed",
			"account_id", accountID,
			"character_id", target.CharacterID,
			"slot", slotIndex,
			"error", err)
		return s.sendGameDeleteCharacterError(session, characterDeleteAckBlocked)
	}
	if visible, verifyErr := s.gameCharacterStillVisible(ctx, repos, accountID, target.CharacterID); verifyErr != nil || visible {
		// 与 GM 软删同一口径（api_character_delete.go:271-282）：读不回来不算成功。
		// 写成功但读面仍可见 ⇒ 不能回 Ok，否则玩家重登角色又回来了。
		s.logGameEvent(session, "game-delete-character-verify-failed",
			"account_id", accountID,
			"character_id", target.CharacterID,
			"slot", slotIndex,
			"visible", visible,
			"error", verifyErr)
		return s.sendGameDeleteCharacterError(session, characterDeleteAckBlocked)
	}
	s.logGameEvent(session, "game-delete-character-success",
		"account_id", accountID,
		"character_id", target.CharacterID,
		"name", target.Name,
		"slot", slotIndex,
		"previous_slot", previousSlot,
		"tombstone_slot", tombstoneSlot,
		"delete_flag", characterDeleteDeletedFlag,
		"roster_after", len(records)-1,
		"roster_replay", "none_vendor_1005_1006")
	s.removeCurrentSelectorRosterCharacter(session, target.CharacterID)
	// 成功 ACK 回显请求里的客户端 0-based slot（厂商 :990-1003，勿用 charId）。
	return s.sendGameDeleteCharacterSuccess(session, req.Slot)
}

// characterDeleteNameMatches 判断请求名是否就是这个角色。
//
// 厂商 :959 用 **原始字节** 比较（NameBytesEqual）。请求体里的名字是客户端本地码页
// （GB18030）字节，而我们的 dnf_characters.name 是 UTF-8 Go 字符串，因此两条都认：
// 先比"记录名重新编码回 GB18030 的字节"（= 厂商口径），再比"请求字节按 GB18030 解码后的字符串"。
// 两条都不中才是真的不符。
func characterDeleteNameMatches(record dnfrepo.CharacterRecord, raw []byte) bool {
	candidates := [][]byte{raw}
	if trimmed := bytes.TrimRight(raw, "\x00"); len(trimmed) != len(raw) {
		candidates = append(candidates, trimmed)
	}
	for _, candidate := range candidates {
		if len(candidate) == 0 {
			continue
		}
		if bytes.Equal(candidate, rosterNameBytes(record.Name)) {
			return true
		}
		if decoded, err := decodeCharacterName(candidate); err == nil && decoded == record.Name {
			return true
		}
	}
	return false
}

// softDeleteGameCharacter 按 GM 面板软删的同一口径落库，返回原槽位与墓碑槽位。
func (s *Service) softDeleteGameCharacter(
	ctx context.Context,
	repos dnfrepo.Group,
	record dnfrepo.CharacterRecord,
) (int, int, error) {
	characterID := numericCharacterID(record)
	if characterID <= 0 {
		// 负墓碑槽需要正的十进制角色号（gmtool chardelTombstoneSlot:776-781 同样拒绝）。
		return 0, 0, errGameDeleteCharacterIDUnavailable
	}
	updated := dnfrepo.CloneCharacter(record)
	if updated.Stats == nil {
		updated.Stats = make(map[string]int64, 1)
	}
	previousSlot := updated.Slot
	updated.Stats["delete_flag"] = characterDeleteDeletedFlag
	// 墓碑槽 = -角色号：选人槽位区间是 [0, defaultCharacterSlots)，墓碑行不会被任何
	// 列表/选角读面看到；角色号是主键，-角色号 天然唯一（同槽位删两次不会撞唯一键）。
	tombstoneSlot := -characterID
	updated.Slot = tombstoneSlot
	updated.UpdatedAt = time.Now().UTC()
	if err := dnfrepo.SaveCharacterFields(ctx, repos.Character, updated,
		dnfrepo.CharacterFieldBase, dnfrepo.CharacterFieldStats); err != nil {
		return previousSlot, tombstoneSlot, err
	}
	return previousSlot, tombstoneSlot, nil
}

// gameCharacterStillVisible 复核角色是否还在可见角色表里（读面 = ListByAccount，
// 与选角/角色列表用的同一个方法）。这里不走 listCharacters：那条路会顺带装配
// 装备摘要，复核只需要"在不在"。
func (s *Service) gameCharacterStillVisible(
	ctx context.Context,
	repos dnfrepo.Group,
	accountID string,
	characterID string,
) (bool, error) {
	records, err := repos.Character.ListByAccount(ctx, accountID, defaultCharacterSlots)
	if err != nil {
		return false, err
	}
	wanted := strings.TrimSpace(characterID)
	for _, record := range records {
		if strings.TrimSpace(record.CharacterID) == wanted {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) sendGameDeleteCharacterSuccess(session *gameSession, slot uint16) error {
	return s.sendGameDeleteCharacterAck(session, characterDeleteSuccessAckBody(slot))
}

func (s *Service) sendGameDeleteCharacterError(session *gameSession, code byte) error {
	// 厂商失败包是**单字节**结果码（不是 op692 族的 [0x00][code]）：{0x02}/{0x15}/{0x28}。
	return s.sendGameDeleteCharacterAck(session, []byte{code})
}

func (s *Service) sendGameDeleteCharacterAck(session *gameSession, body []byte) error {
	// 与 sendGameCheckName（character_create_transport.go:136-145）同一手法：
	// game-upper 明文、同族 classification=1、msg_id = 6。
	return s.sendGameUpperRawClassNoCodec(
		session,
		uint16(dnfenum.CmdPacketDeleteCharacter),
		body,
		dnfproto.DefaultChannelClassification,
	)
}

// characterDeleteSuccessAckBody 生成厂商成功 ACK：[0x01][flag=0x00][slot:u16 LE]。
//
// body[0] 必须非 0（0x00 会让客户端判 Error 并把 body[1] 当 ErrCode 崩溃），
// body[1]=0 表示不设删除残留态（保守档），body[2..3] 是客户端本地 0-based slot。
func characterDeleteSuccessAckBody(slot uint16) []byte {
	return []byte{
		characterDeleteAckOK,
		0x00,
		byte(slot & 0xFF),
		byte((slot >> 8) & 0xFF),
	}
}
