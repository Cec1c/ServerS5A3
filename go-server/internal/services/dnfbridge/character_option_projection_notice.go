package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// 本文件把 character_option_projection.go 的纯函数接到登录回放路径上：
// 解析"厂商投影需要的上下文"（只读仓储），失败一律回落原样回放。
//
// 与厂商的输入对账（三个入参，AccountSettings.cs:124）：
//
//	saved       <- record.Values["character_option_body_hex"] / ["character_option_hex"]
//	               （本仓已有；characterOptionNoticeFrame 取）
//	visibleBits <- **本仓有**：角色镜像列 `user_state_bits`
//	               （mysql_char.go:90，fallback 3；写入见
//	                character_created_defaults.go:97/:128，读取口径见
//	                current_actor_user_state.go:55-63）。
//	               线上只读 SELECT 实测 `character:3`（test2）= **3**，
//	               所以投影在这一档下**真的会生效**（改 index 126）。
//	               别和 `dnf_characters.user_state`（char 3 = 0）混起来，那是另一列。
//	packedMain  <- **本仓无从获得**：厂商是 `InitializationSnapshot.MainGameOptionBlob`
//	               （账号主选项包，来自 op197）。本仓 op197 只在
//	               `gameOptionSaveWindowOpen` 为真时才写
//	               `account:<id>:game_option.main_game_option_hex`，而现场该 scope
//	               从未有过行（game_option_settings.go:30-32）。因此这里**不去猜**：
//	               不传 PackedMain，走厂商 `packedMain == null` 分支，
//	               index 127 保持 0xFFFF，并在日志里写 `packed_main=absent`。
const characterOptionProjectionPackedMainAbsent = "absent"

// characterOptionProjectionVisibleBits 从角色记录里取厂商口径的 visibleBits。
//
// 已确证缺失：本仓今天**没有**把 op447 的输入与 `user_state_bits` 关联起来，
// 所以这一项只能"取到就用、取不到就明确失败"（失败 ⇒ 原样回放），
// 绝不拿厂商的 `?? (byte)3` 兜底值冒充现场值——那会把"缺输入"伪装成"有输入"。
func characterOptionProjectionVisibleBits(characterStats map[string]int64) (byte, bool) {
	if characterStats == nil {
		return 0, false
	}
	value, exists := characterStats["user_state_bits"]
	if !exists {
		return 0, false
	}
	if value < 0 || value > 0xFF {
		return 0, false
	}
	return byte(value), true
}

// loadCharacterOptionProjectionContext 只读地组装投影上下文。
//
// 角色不存在、账号不匹配、stat 缺失/越界都返回 error；调用方据此回落原样回放。
// 这里刻意**不**读账号包（见文件头注释），所以 PackedMain 恒为 nil。
func (s *Service) loadCharacterOptionProjectionContext(session *gameSession, characterID string) (characterOptionProjectionContext, error) {
	if s == nil || session == nil {
		return characterOptionProjectionContext{}, fmt.Errorf("character option projection requires a session")
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return characterOptionProjectionContext{}, fmt.Errorf("character option projection character repository is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		return characterOptionProjectionContext{}, fmt.Errorf("load character %s for option projection: %w", characterID, err)
	}
	if !found {
		return characterOptionProjectionContext{}, fmt.Errorf("character %s for option projection was not found", characterID)
	}
	if accountID := strings.TrimSpace(s.accountIDForSession(session)); accountID != "" &&
		accountID != strings.TrimSpace(character.AccountID) {
		return characterOptionProjectionContext{}, fmt.Errorf("character %s option projection owner mismatch", characterID)
	}
	bits, known := characterOptionProjectionVisibleBits(character.Stats)
	if !known {
		return characterOptionProjectionContext{}, fmt.Errorf("character %s has no usable user_state_bits stat", characterID)
	}
	return characterOptionProjectionContext{VisibleBits: bits, VisibleBitsKnown: true}, nil
}

// noticeFrameForCharacterOptionProjection 是 sendCharacterOptionNotice 用的外壳：
// 调纯函数做投影，并把结果/失败都记成可 grep 的具名日志事件。
//
// 失败语义（fail-safe）：返回原帧 + `character_option_projection=verbatim`
// + `projection_error=<原因>`，**绝不发一条半截帧**。成功时
// `character_option_projection=project` 带 `changed_indexes` 与 `changed_words`。
func (s *Service) noticeFrameForCharacterOptionProjection(
	session *gameSession,
	characterID string,
	frame []byte,
	frameSource string,
	mode string,
) []byte {
	if mode != characterOptionProjectionProject {
		// 默认档：一个字节都不动，也不多做一次仓储读（逐字节 + 逐行为今天）。
		return frame
	}
	projectionContext, err := s.loadCharacterOptionProjectionContext(session, characterID)
	if err != nil {
		s.logGameEvent(session, "game-character-option-projection-fallback",
			"char_id", characterID,
			"frame_source", frameSource,
			"character_option_projection", characterOptionProjectionVerbatim,
			"env", currentCharacterOptionProjectionEnv,
			"projection_error", err)
		return frame
	}
	body, projection, changedIndexes, changedHex, failure := characterOptionNoticeFrameForProjection(
		frame, frameSource, projectionContext, mode)
	fields := []any{
		"char_id", characterID,
		"frame_source", frameSource,
		"character_option_projection", projection,
		"env", currentCharacterOptionProjectionEnv,
		"visible_bits_known", projectionContext.VisibleBitsKnown,
		"visible_bits", projectionContext.VisibleBits,
		"packed_main", characterOptionProjectionPackedMainAbsent,
		"changed_indexes", changedIndexes,
		"changed_words", changedHex,
		"body_len_in", len(frame),
		"body_len_out", len(body),
	}
	// 成功时不写 projection_error：日志里出现 `<nil>` 会让 `grep projection_error=`
	// 把成功行也捞出来，等于把"投影成功"和"投影失败回落"混成一条 grep。
	if failure != nil {
		fields = append(fields, "projection_error", failure)
	}
	s.logGameEvent(session, "game-character-option-projection-applied", fields...)
	return body
}

// characterOptionProjectionEnvRaw 是未归一化的原始取值，随投影日志一起落盘：
// 让"设了但拼错"（例如 `Project`）在现场一眼可见，而不是静默走默认档。
func characterOptionProjectionEnvRaw() string {
	return strings.TrimSpace(os.Getenv(currentCharacterOptionProjectionEnv))
}
