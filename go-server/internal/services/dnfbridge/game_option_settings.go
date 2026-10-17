package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// This file owns the account/character "game option" family that the current
// client pushes on every entry and after every option change:
//
//	op197 SAVE_GAME_OPTION_1            u32 len + blob            (account)
//	op198 SAVE_GAME_OPTION_2            u32 len + blob            (character + account-scoped slot prefix)
//	op368 SAVE_GAME_OPTION_QUICKCHATTING u8 bank + u32 len + blob  (account, 2 banks)
//	op447 SAVE_CHARACTER_OPTION         u32 len + 512B blob        (character)
//
// Live evidence (HK packet_log, 2026-09-24/25): all four arrive as
// `game-legacy` frames and every one of them fell through to
// `dnfbridge unhandled latest game command` (dnf90.out counts: 197 -> 281,
// 447 -> 138, 368 -> 4, 198 -> 2). Nothing was persisted and no account
// setting was ever sent back, which is exactly the operator report
// "游戏设置每次登陆游戏重置". `dnf_legacy_account_settings`
// (main_game_option/quickchat_bank0/quickchat_bank1/hotkey_key_type/
// hotkey_slots) is created by our schema but has never held a row.
//
// Reference implementation (A21/86JP `Network/Handlers/SettingsHandler.cs`
// + `Game/Settings/AccountSettingsRepository.cs` +
// `Network/Builders/AccountSettingsPacketBuilder.cs`): persist the blob, then
// replay it at entry as class0/0x00AD (game option) and class0/0x01C7 (hotkey
// option). The 90-level client's framing matches that reference byte for byte
// (`u32 len` prefix on 197/198/447, `u8 bank + u32 len` on 368).
//
// The replay is what makes persistence observable at all: the client has no
// local store for these values — the op197 body it sends at every entry is the
// same default template (bodies captured from two different characters differ
// in a single dword only), so a server that merely stores op197 without ever
// replaying 0x00AD leaves the player on client defaults forever.
const (
	currentSaveGameOption1Opcode        uint16 = uint16(dnfenum.CmdPacketSaveGameOption1)
	currentSaveGameOption2Opcode        uint16 = uint16(dnfenum.CmdPacketSaveGameOption2)
	currentSaveQuickchatOpcode          uint16 = uint16(dnfenum.CmdPacketSaveGameOptionQuickchatting)
	currentSaveCharacterOptionOpcode    uint16 = uint16(dnfenum.CmdPacketSaveCharacterOption)
	currentAccountGameOptionNoticeMsgID uint16 = 0x00AD
	currentAccountHotkeyNoticeMsgID     uint16 = 0x01C7
	// currentCharacterOptionNoticeMsgID 是"每角色选项"下发用的 class0 通知号。
	//
	// 依据（厂商同血统 9.16 参考实现的包号表 + 初始化构建器）：
	//   `Network/Core/PacketTypes.cs:398  CHARACTER_OPTION = 0x0187 // 391:
	//   ENUM_NOTIPACKET_CHARACTER_OPTION`
	//   与 `Network/Builders/Init/CharacterOptionBodyBuilder.cs`（NotiType 0x0187，
	//   把 `SaveCharacterOption` 存下来的**整个请求体**原样回放；没有存过就回 `u32 0`）。
	// 同一张表里 `0x0187` 在 C2S 命名空间是 `ENUM_CMDPACKET_CANCEL_CARGO_PAD`，
	// 所以这里按既有 0x00AD/0x01C7 的写法用裸十六进制常量，不借用 CmdPacket 枚举名。
	//
	// 现场形状（HK packet_log 2026-09-24，`:120`/`:136`/`:6977`）：
	// `cmd=1 type=447 runtime_cmd_name=ENUM_CMDPACKET_SAVE_CHARACTER_OPTION body_len=516`
	// = `u32 len(512=0x200) + 512B 角色选项 blob`，正好是参考实现的 `u32 len + 512B blob`。
	currentCharacterOptionNoticeMsgID   uint16 = 0x0187
	currentGameOptionBlobMaxLen                = 8 << 10
	currentQuickchatBlobMaxLen                 = 4 << 10
	currentAccountScopedHotkeySlotCount        = 1
)

// currentGameOptionNoticeEnv disables the 0x00AD/0x01C7 replay without a
// rebuild. Persistence is unconditional; only the replay is switchable, so a
// shape problem on a future client can be isolated from data loss.
const currentGameOptionNoticeEnv = "DNFBRIDGE_GAME_OPTION_NOTI"

// currentCharacterOptionNoticeEnv 关闭（或换档）"每角色选项"（class0/0x0187）的回放。
// 与 currentGameOptionNoticeEnv 同口径：落库永远照做，只有回放可开关/换档，
// 将来客户端形状有变时能把"数据丢失"和"回放形状"分开定位。
//
//	未设置 / on / 1 / true / yes   —— 有存档才回放（默认；没存过的角色逐字节不变）
//	empty                          —— 参考实现口径：没存档时发 `u32 0` 空帧
//	off / 0 / false / no           —— 从不回放（一键回退）
const currentCharacterOptionNoticeEnv = "DNFBRIDGE_CHARACTER_OPTION_NOTI"

// currentCharacterOptionNoticeEmptyValue 是"没存档也发空帧"的档位值。
const currentCharacterOptionNoticeEmptyValue = "empty"

type characterOptionNoticeMode byte

const (
	// characterOptionNoticeSavedOnly 只回放已保存的选项（默认）。
	characterOptionNoticeSavedOnly characterOptionNoticeMode = iota
	// characterOptionNoticeWithEmpty 没有存档时也发参考实现的 `u32 0` 空帧。
	characterOptionNoticeWithEmpty
	// characterOptionNoticeOff 完全不发。
	characterOptionNoticeOff
)

func currentCharacterOptionNoticeMode() characterOptionNoticeMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentCharacterOptionNoticeEnv))) {
	case currentCharacterOptionNoticeEmptyValue:
		return characterOptionNoticeWithEmpty
	case "0", "false", "no", "off":
		return characterOptionNoticeOff
	default:
		return characterOptionNoticeSavedOnly
	}
}

func accountGameOptionScope(accountID string) string {
	return "account:" + strings.TrimSpace(accountID) + ":game_option"
}

func characterHotkeyConfigScope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + ":hotkey_config"
}

func characterOptionScope(characterID string) string {
	return "character:" + strings.TrimSpace(characterID) + ":character_option"
}

// currentGameOptionEntryGuardEnv disables the entry-window guard below.
const currentGameOptionEntryGuardEnv = "DNFBRIDGE_GAME_OPTION_ENTRY_GUARD"

// gameOptionSaveWindowOpen reports whether an op197 may be treated as a
// player-initiated save.
//
// The current client pushes the same factory template at every entry: two
// captures from different characters differ in a single dword, and neither
// reflects anything the player ever changed. A last-write-wins store would
// therefore erase the player's saved settings on the next login, which is the
// opposite of the fix. A genuine save requires the player to open the option
// window, and that is only possible after the client itself acknowledges the
// town/dungeon scene — so anything arriving during the entry bootstrap is
// answered (0x00AD is already sent before it) but not persisted, and the skip
// is logged so live logs can prove the rule.
//
// Both readiness markers belong to a character. A previous character's marker
// must not admit the current character's entry template after selection changes.
func gameOptionSaveWindowOpen(session *gameSession) bool {
	if session == nil {
		return false
	}
	if !envBool(currentGameOptionEntryGuardEnv, true) {
		return true
	}
	if selected := session.selectedCharacterID; selected != 0 {
		if session.townSceneReadyCharacterID == selected {
			return true
		}
		if session.initialTownRouteStage >= currentInitialTownRoutePlayerStateSent &&
			session.initialTownRouteCharacterID == selected {
			return true
		}
	}
	return currentDungeonSceneActive(session)
}

func currentGameOptionNoticeEnabled() bool {
	return envBool(currentGameOptionNoticeEnv, true)
}

// handleGameOptionCommand owns the four option saves. It returns handled=false
// for every other opcode so the ordinary dispatch chain keeps running.
func (s *Service) handleGameOptionCommand(session *gameSession, typ uint16, body []byte) (bool, error) {
	switch typ {
	case currentSaveGameOption1Opcode:
		return true, s.handleSaveGameOption1(session, body)
	case currentSaveGameOption2Opcode:
		return true, s.handleSaveGameOption2(session, body)
	case currentSaveQuickchatOpcode:
		return true, s.handleSaveQuickchat(session, body)
	case currentSaveCharacterOptionOpcode:
		return true, s.handleSaveCharacterOption(session, body)
	default:
		return false, nil
	}
}

// lengthPrefixedOptionBlob validates the `u32 len + blob` framing shared by
// op197/op198/op447 and returns a copy of the payload.
func lengthPrefixedOptionBlob(body []byte, maxLen int) ([]byte, bool) {
	if len(body) < 4 {
		return nil, false
	}
	declared := int(binary.LittleEndian.Uint32(body[0:4]))
	if declared <= 0 || declared > maxLen || len(body) < 4+declared {
		return nil, false
	}
	return append([]byte(nil), body[4:4+declared]...), true
}

func (s *Service) handleSaveGameOption1(session *gameSession, body []byte) error {
	blob, ok := lengthPrefixedOptionBlob(body, currentGameOptionBlobMaxLen)
	if !ok {
		s.logGameEvent(session, "game-save-game-option-1-blocked",
			"body_len", len(body),
			"reason", "invalid_length_prefixed_blob")
		return nil
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		s.logGameEvent(session, "game-save-game-option-1-blocked",
			"body_len", len(body),
			"reason", "account_unresolved")
		return nil
	}
	if !gameOptionSaveWindowOpen(session) {
		s.logGameEvent(session, "game-account-game-option-save-deferred",
			"account_id", accountID,
			"blob_len", len(blob),
			"reason", "entry_bootstrap_template",
			"guard_env", currentGameOptionEntryGuardEnv)
		return nil
	}
	err := s.saveAccountGameOptionValues(session, accountID, map[string]string{
		"main_game_option_hex": hex.EncodeToString(blob),
		"main_game_option_len": strconv.Itoa(len(blob)),
		"source":               "current_exe_op197",
	})
	s.logGameEvent(session, "game-account-game-option-saved",
		"account_id", accountID,
		"blob_len", len(blob),
		"save_error", err,
		"wire_source", "current_exe_sub_1CF5280_u32_len_prefixed_blob")
	return nil
}

func (s *Service) handleSaveGameOption2(session *gameSession, body []byte) error {
	blob, ok := lengthPrefixedOptionBlob(body, currentGameOptionBlobMaxLen)
	if !ok {
		s.logGameEvent(session, "game-save-game-option-2-blocked",
			"body_len", len(body),
			"reason", "invalid_length_prefixed_blob")
		return nil
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	if session.selectedCharacterID != 0 {
		err := s.saveCharacterSettingsValues(session, characterHotkeyConfigScope(characterID), map[string]string{
			"hotkey_config_hex": hex.EncodeToString(blob),
			"hotkey_config_len": strconv.Itoa(len(blob)),
			"source":            "current_exe_op198",
		})
		if err != nil {
			s.logWarn("dnfbridge hotkey config save failed",
				"conn_id", session.connID,
				"char_id", characterID,
				"error", err)
		}
	}
	// The account-scoped prefix is the only part of this blob that survives a
	// character switch (86JP AccountSettings.ExtractAccountScopedHotkeySlots).
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID != "" {
		accountScoped := blob
		if limit := currentAccountScopedHotkeySlotCount * 2; len(accountScoped) > limit {
			accountScoped = accountScoped[:limit]
		}
		if err := s.saveAccountGameOptionValues(session, accountID, map[string]string{
			"hotkey_slots_hex": hex.EncodeToString(accountScoped),
			"source":           "current_exe_op198_account_scoped_prefix",
		}); err != nil {
			s.logWarn("dnfbridge account hotkey slot save failed",
				"conn_id", session.connID,
				"account_id", accountID,
				"error", err)
		}
	}
	s.logGameEvent(session, "game-character-hotkey-config-saved",
		"char_id", characterID,
		"account_id", accountID,
		"blob_len", len(blob),
		"wire_source", "current_exe_sub_268D1C0_u32_len_prefixed_blob")
	return nil
}

func (s *Service) handleSaveQuickchat(session *gameSession, body []byte) error {
	if len(body) < 5 {
		s.logGameEvent(session, "game-save-quickchat-blocked",
			"body_len", len(body),
			"reason", "body_too_short")
		return nil
	}
	bank := body[0]
	if bank > 1 {
		s.logGameEvent(session, "game-save-quickchat-blocked",
			"body_len", len(body),
			"bank", bank,
			"reason", "bank_out_of_range")
		return nil
	}
	declared := int(binary.LittleEndian.Uint32(body[1:5]))
	if declared <= 0 || declared > currentQuickchatBlobMaxLen || len(body) < 5+declared {
		s.logGameEvent(session, "game-save-quickchat-blocked",
			"body_len", len(body),
			"bank", bank,
			"declared_len", declared,
			"reason", "invalid_length_prefixed_blob")
		return nil
	}
	blob := body[5 : 5+declared]
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		s.logGameEvent(session, "game-save-quickchat-blocked",
			"body_len", len(body),
			"reason", "account_unresolved")
		return nil
	}
	key := "quickchat_bank0_hex"
	if bank == 1 {
		key = "quickchat_bank1_hex"
	}
	err := s.saveAccountGameOptionValues(session, accountID, map[string]string{
		key:      hex.EncodeToString(blob),
		"source": "current_exe_op368",
	})
	s.logGameEvent(session, "game-account-quickchat-saved",
		"account_id", accountID,
		"bank", bank,
		"blob_len", len(blob),
		"save_error", err)
	return nil
}

func (s *Service) handleSaveCharacterOption(session *gameSession, body []byte) error {
	blob, ok := lengthPrefixedOptionBlob(body, currentGameOptionBlobMaxLen)
	// Pinned v97 NOTI390 reader (2255DB0) accepts exactly 512 bytes.
	// Reject incompatible saves before they can replace a valid character snapshot.
	if !ok || (s.options.currentClientVersion == "official-24.9" && len(blob) != 512) {
		s.logGameEvent(session, "game-save-character-option-blocked",
			"body_len", len(body),
			"reason", "invalid_length_prefixed_blob")
		return nil
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	if session.selectedCharacterID == 0 {
		s.logGameEvent(session, "game-save-character-option-blocked",
			"body_len", len(body),
			"reason", "character_unresolved")
		return nil
	}
	// op447 also fires during the entry bootstrap (live: header_seq=29), so it
	// needs the same entry-window guard as op197.
	if !gameOptionSaveWindowOpen(session) {
		s.logGameEvent(session, "game-character-option-save-deferred",
			"char_id", characterID,
			"blob_len", len(blob),
			"reason", "entry_bootstrap_template",
			"guard_env", currentGameOptionEntryGuardEnv)
		return nil
	}
	// `character_option_body_hex` 存的是**带 u32 长度前缀的完整帧**（客户端原样发来的
	// 那 516 字节）。参考实现 `SettingsHandler.Handle_SAVE_CHARACTER_OPTION` 就是
	// `SaveCharacterOption(characterId, body)` 原样落库，再由
	// `CharacterOptionBodyBuilder` 原样回放；所以回放要走这一份，而不是只有 blob 的
	// `character_option_hex`。长度按声明的 blob 截断，避免尾部垃圾被无限存下来。
	frame := body[:4+len(blob)]
	previous := s.loadSavedCharacterOptionBlob(characterID)
	err := s.saveCharacterSettingsValues(session, characterOptionScope(characterID), map[string]string{
		"character_option_body_hex": hex.EncodeToString(frame),
		"character_option_hex":      hex.EncodeToString(blob),
		"character_option_len":      strconv.Itoa(len(blob)),
		"source":                    "current_exe_op447",
	})
	fields := []any{"char_id", characterID, "blob_len", len(blob), "frame_len", len(frame), "save_error", err}
	s.logGameEvent(session, "game-character-option-saved", append(fields, characterOptionSaveLogFields(previous, blob)...)...)
	if err == nil {
		s.logCharacterOptionSkillListsCleared(session, characterID, previous, blob)
	}
	return nil
}

// sendCharacterOptionNotice 回放**每角色**选项（class0/0x0187）。
//
// 为什么必须有这一步：客户端对 0x0187 这一族**没有本地存储**——它每次进城把
// `u32 len + 512B blob`（op447）推给服务端，权威副本只在服务端。只落库不回放，
// 玩家每次重登看到的都是客户端默认值，也就是操作者报的
// 「穿脱装备 选择角色 重新登录后 Buff强化换装栏被重置」/「游戏设置每次登陆游戏重置」。
// 这与 0x00AD（账号选项）是同一个契约，参考实现把两者放在同一个
// `InitPacketBuilderRegistry`（`HotkeyConfigBodyBuilder` → `CharacterOptionBodyBuilder`
// → `GameOptionBodyBuilder`）。
//
// 形状：把 op447 的**完整帧原样**发回去（`character_option_body_hex`）；
// 只有旧数据（本改动之前只存了 `character_option_hex`）才按 `u32 len + blob` 重建，
// 两者对客户端都是同一个"u32 长度前缀 + blob"帧。
//
// 没有任何已保存选项时**默认不发**（与 0x00AD 同口径）：参考实现的
// `CharacterOptionBodyBuilder` 在无存档时会发 `u32 0`，但发空帧等于主动要求客户端回到
// 默认值，对"从没改过选项"的角色是可见的行为变化；默认档保持"不发 → 客户端用自己的本地默认"，
// 只修"已保存过却被重置"的那一半。`DNFBRIDGE_CHARACTER_OPTION_NOTI=empty` 提供参考实现的
// 显式空帧档，供线上 A/B（客户端是否需要一次显式空帧才会清掉旧本地状态）。
func (s *Service) sendCharacterOptionNotice(session *gameSession, source string) error {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	mode := currentCharacterOptionNoticeMode()
	if mode == characterOptionNoticeOff {
		s.logGameEvent(session, "game-character-option-notice-disabled",
			"source", source,
			"env", currentCharacterOptionNoticeEnv)
		return nil
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	// 投影档只在 DNFBRIDGE_CHARACTER_OPTION_PROJECTION=project 时生效；默认档
	// 原样返回 frame，**一个字节都不动**（逐字节今天）。失败会回落原样回放并记
	// game-character-option-projection-fallback，见 character_option_projection_notice.go。
	projectionMode := currentCharacterOptionProjectMode()
	frame := []byte(nil)
	frameSource := ""
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	record, found, err := repos.Settings.Load(ctx, characterOptionScope(characterID))
	cancel()
	if err != nil {
		s.logWarn("dnfbridge character option load failed",
			"conn_id", session.connID,
			"char_id", characterID,
			"error", err)
		return nil
	}
	if found {
		frame, frameSource = characterOptionNoticeFrame(record.Values)
	}
	frame = s.noticeFrameForCharacterOptionProjection(session, characterID, frame, frameSource, projectionMode)
	if len(frame) == 0 {
		if mode != characterOptionNoticeWithEmpty {
			s.logGameEvent(session, "game-character-option-notice-skipped",
				"source", source,
				"char_id", characterID,
				"reason", "no_saved_character_option",
				"env", currentCharacterOptionNoticeEnv,
				"character_option_projection", projectionMode,
				"projection_env_raw", characterOptionProjectionEnvRaw())
			return nil
		}
		// 参考实现口径：没有存档时发 `u32 0` 空帧。
		frame = []byte{0, 0, 0, 0}
		frameSource = "reference_u32_zero_empty_frame"
	}
	if err := s.sendGameUpperRawClass(session, s.currentCharacterOptionNoticeID(), frame, 0); err != nil {
		return err
	}
	fields := []any{
		"source", source,
		"char_id", characterID,
		"msg_id", s.currentCharacterOptionNoticeID(),
		"classification", 0,
		"body_len", len(frame),
		"frame_source", frameSource,
		"character_option_projection", projectionMode,
		"projection_env_raw", characterOptionProjectionEnvRaw(),
		"env", currentCharacterOptionNoticeEnv,
		"body_source", "csharp_CharacterOptionBodyBuilder_u32_len_prefixed_verbatim_replay",
	}
	s.logGameEvent(session, "game-character-option-notice-send", append(fields, characterOptionNoticeLogFields(frame)...)...)
	return nil
}

// characterOptionNoticeFrame 取回放帧：优先"op447 原帧"，其次用旧口径的 blob 重建。
func characterOptionNoticeFrame(values map[string]string) ([]byte, string) {
	if values == nil {
		return nil, ""
	}
	if frame := decodeSettingsHex(values["character_option_body_hex"]); len(frame) >= 4 {
		return frame, "op447_verbatim_frame"
	}
	blob := decodeSettingsHex(values["character_option_hex"])
	if len(blob) == 0 {
		return nil, ""
	}
	frame := make([]byte, 4+len(blob))
	binary.LittleEndian.PutUint32(frame[0:4], uint32(len(blob)))
	copy(frame[4:], blob)
	return frame, "rebuilt_u32_len_prefixed_blob"
}

func (s *Service) saveAccountGameOptionValues(session *gameSession, accountID string, values map[string]string) error {
	return s.saveSettingsValues(session, accountGameOptionScope(accountID), values)
}

func (s *Service) saveCharacterSettingsValues(session *gameSession, scope string, values map[string]string) error {
	return s.saveSettingsValues(session, scope, values)
}

// saveSettingsValues merges values into one settings scope. It never fails the
// caller: a settings save is a fire-and-forget client push and must not turn
// into a session error (the client sends no request id to answer).
func (s *Service) saveSettingsValues(session *gameSession, scope string, values map[string]string) error {
	if s == nil || strings.TrimSpace(scope) == "" {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return errSettingsRepositoryUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	record, found, err := repos.Settings.Load(ctx, scope)
	if err != nil {
		return err
	}
	if !found {
		record = dnfrepo.SettingsRecord{Scope: scope, Values: map[string]string{}}
	}
	record = dnfrepo.CloneSettings(record)
	if record.Values == nil {
		record.Values = map[string]string{}
	}
	for key, value := range values {
		record.Values[key] = value
	}
	record.Values["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	record.UpdatedAt = time.Now().UTC()
	if err := repos.Settings.Save(ctx, record); err != nil {
		if session != nil {
			s.logWarn("dnfbridge settings save failed",
				"conn_id", session.connID,
				"scope", scope,
				"error", err)
		}
		return err
	}
	return nil
}

var errSettingsRepositoryUnavailable = errors.New("settings repository unavailable")

// sendAccountGameOptionNotices replays persisted account options. Current
// official hotkeys use 454 with `u8 key_type + u32 length + text`; legacy
// clients retain 455 and their slot layout. Missing main options stay local,
// while a fresh Creator receives its separately resolved PVF keyboard defaults.
func (s *Service) sendAccountGameOptionNotices(session *gameSession, source string) error {
	if s == nil || session == nil {
		return nil
	}
	if !currentGameOptionNoticeEnabled() {
		s.logGameEvent(session, "game-account-game-option-notice-disabled",
			"source", source,
			"env", currentGameOptionNoticeEnv)
		return nil
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	record, found, err := repos.Settings.Load(ctx, accountGameOptionScope(accountID))
	if err != nil {
		s.logWarn("dnfbridge game option load failed",
			"conn_id", session.connID,
			"account_id", accountID,
			"error", err)
		return nil
	}
	if !found {
		if s.options.currentClientVersion == "official-24.9" {
			return s.sendAccountHotkeyNotice(session, source, accountID, nil)
		}
		return nil
	}
	main := decodeSettingsHex(record.Values["main_game_option_hex"])
	if len(main) == 0 {
		s.logGameEvent(session, "game-account-game-option-notice-skipped",
			"source", source,
			"account_id", accountID,
			"reason", "no_saved_main_game_option")
		return s.sendAccountHotkeyNotice(session, source, accountID, record.Values)
	}
	bank0 := decodeSettingsHex(record.Values["quickchat_bank0_hex"])
	bank1 := decodeSettingsHex(record.Values["quickchat_bank1_hex"])
	body := buildAccountGameOptionNoticeBody(main, bank0, bank1)
	if err := s.sendGameUpperRawClass(session, currentAccountGameOptionNoticeMsgID, body, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-account-game-option-notice-send",
		"source", source,
		"account_id", accountID,
		"msg_id", currentAccountGameOptionNoticeMsgID,
		"classification", 0,
		"main_len", len(main),
		"quickchat_bank0_len", len(bank0),
		"quickchat_bank1_len", len(bank1),
		"body_len", len(body),
		"body_source", "csharp_AccountSettingsPacketBuilder_BuildGameOptionBody")
	return s.sendAccountHotkeyNotice(session, source, accountID, record.Values)
}

// sendAccountHotkeyNotice replays class0/0x01C7. The hotkey slots come from the
// account-scoped prefix of the last op198 when one exists; otherwise the value
// seeded for the character at creation (`character:<id>:hotkeys`, PVF/C#
// default layout) is projected so a character switch cannot inherit the
// previous character's key table.
func (s *Service) sendAccountHotkeyNotice(session *gameSession, source string, accountID string, values map[string]string) error {
	keyType := parseSettingsUint8(values["hotkey_key_type"])
	slots := decodeSettingsHex(values["hotkey_slots_hex"])
	slotSource := "account_op198_prefix"
	if s.options.currentClientVersion == "official-24.9" {
		// 2005E00 -> 2261680 consumes key type; 22616B0 -> 2260DC0
		// then reads the complete u32-length-prefixed configuration string
		// through 3752120/3752050, with a 0x578-byte capacity. The old
		// one-slot account prefix cannot restore a player's key mappings.
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		repos, ok := s.repositoryGroup()
		if !ok || repos.Settings == nil || session.selectedCharacterID == 0 {
			cancel()
			return nil
		}
		id := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
		record, found, err := repos.Settings.Load(ctx, characterHotkeyConfigScope(id))
		if err != nil {
			cancel()
			s.logGameEvent(session, "game-account-hotkey-notice-skipped", "reason", "saved_configuration_load_failed", "error", err)
			return nil
		}
		if repos.Character == nil {
			cancel()
			return nil
		}
		character, characterFound, characterErr := repos.Character.Load(ctx, id)
		if characterErr != nil || !characterFound ||
			strings.TrimSpace(character.AccountID) != strings.TrimSpace(accountID) ||
			strings.TrimSpace(s.accountIDForSession(session)) != strings.TrimSpace(accountID) ||
			strconv.FormatUint(uint64(session.selectedCharacterID), 10) != id {
			cancel()
			s.logGameEvent(session, "game-account-hotkey-notice-skipped", "reason", "saved_configuration_character_unavailable_or_owner_changed")
			return nil
		}
		job, jobErr := strconv.ParseUint(strings.TrimSpace(character.Job), 10, 8)
		creator := jobErr == nil && byte(job) == csharpCreatorMageJob
		if found {
			slots = decodeSettingsHex(record.Values["hotkey_config_hex"])
			slotSource = "character_op198_complete_configuration"
		} else if creator {
			slots, err = s.creatorDefaultConfig(ctx)
			slotSource = "creator_default_pvf_configuration"
			if err != nil {
				cancel()
				s.logGameEvent(session, "game-account-hotkey-notice-skipped", "reason", "creator_default_configuration_unavailable", "error", err)
				return nil
			}
		} else {
			cancel()
			return nil
		}
		cancel()
		if len(slots) == 0 || len(slots) >= 0x578 {
			s.logGameEvent(session, "game-account-hotkey-notice-skipped", "reason", "configuration_exceeds_native_capacity", "blob_len", len(slots))
			return nil
		}
		// Job 10 selects the native CreatorType bank. Existing saved text is
		// replayed verbatim; defaults are projected only when no record exists.
		if creator {
			keyType = 1
		}
	}
	if len(slots) == 0 && session.selectedCharacterID != 0 && currentHotkeySeededLayoutEnabled() {
		characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
		if record, found, err := s.loadCharacterHotkeySettings(characterID); err == nil && found {
			slots = decodeSettingsHex(record.Values["slots_hex"])
			keyType = parseSettingsUint8(record.Values["key_type"])
			slotSource = "character_default_layout"
		}
	}
	if len(slots) == 0 {
		s.logGameEvent(session, "game-account-hotkey-notice-skipped",
			"source", source,
			"account_id", accountID,
			"reason", "no_hotkey_slots")
		return nil
	}
	body := buildAccountHotkeyNoticeBody(keyType, slots)
	if err := s.sendGameUpperRawClass(session, s.currentAccountHotkeyNoticeID(), body, 0); err != nil {
		return err
	}
	s.logGameEvent(session, "game-account-hotkey-notice-send",
		"source", source,
		"account_id", accountID,
		"msg_id", s.currentAccountHotkeyNoticeID(),
		"classification", 0,
		"key_type", keyType,
		"slot_count", len(slots)/2,
		"slot_source", slotSource,
		"body_len", len(body),
		"body_source", "csharp_AccountSettingsPacketBuilder_BuildHotkeyOptionBody")
	return nil
}

func (s *Service) loadCharacterHotkeySettings(characterID string) (dnfrepo.SettingsRecord, bool, error) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return dnfrepo.SettingsRecord{}, false, errSettingsRepositoryUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	return repos.Settings.Load(ctx, newCharacterHotkeySettingsScope(characterID))
}

// buildAccountGameOptionNoticeBody matches 86JP
// AccountSettingsPacketBuilder.BuildGameOptionBody: three u32-length-prefixed
// blobs in account -> bank0 -> bank1 order.
func buildAccountGameOptionNoticeBody(main, bank0, bank1 []byte) []byte {
	body := make([]byte, 0, 12+len(main)+len(bank0)+len(bank1))
	for _, blob := range [][]byte{main, bank0, bank1} {
		var length [4]byte
		binary.LittleEndian.PutUint32(length[:], uint32(len(blob)))
		body = append(body, length[:]...)
		body = append(body, blob...)
	}
	return body
}

// buildAccountHotkeyNoticeBody matches
// AccountSettingsPacketBuilder.BuildHotkeyOptionBody.
func buildAccountHotkeyNoticeBody(keyType byte, slots []byte) []byte {
	body := make([]byte, 5+len(slots))
	body[0] = keyType
	binary.LittleEndian.PutUint32(body[1:5], uint32(len(slots)))
	copy(body[5:], slots)
	return body
}

func decodeSettingsHex(value string) []byte {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil
	}
	return decoded
}

func parseSettingsUint8(value string) byte {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 8)
	if err != nil {
		return 0
	}
	return byte(parsed)
}

// currentHotkeySeededLayoutEnabled 控制"账号没有热键存档时，是否回落到**参考实现**
// 的角色默认热键布局"。
//
// 2026-09-27：默认**关闭**。那份默认布局来自参考抓包，回放出去会让客户端弹出一块
// 公告/任务书面板并停住（操作者报的"弹窗 + 卡死"）；而厂商服务端在账号没有热键存档时
// 并不发这个包（其 packet_log 全库 SEND=0/RECV=0）。因此默认只发玩家真实保存过的热键，
// 需要复现旧行为时设 DNFBRIDGE_HOTKEY_SEEDED_LAYOUT=on。
const currentHotkeySeededLayoutEnv = "DNFBRIDGE_HOTKEY_SEEDED_LAYOUT"

func currentHotkeySeededLayoutEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentHotkeySeededLayoutEnv))) {
	case "on", "1", "true", "yes", "enable", "enabled":
		return true
	default:
		return false
	}
}

// Native registrations: NOTI390 -> 1FEA550 -> 2255DB0 (512-byte
// character options); NOTI454 -> 2005E00 -> 2260DC0 (key type plus full config).
func (s *Service) currentCharacterOptionNoticeID() uint16 {
	if s.options.currentClientVersion == "official-24.9" {
		return 390
	}
	return currentCharacterOptionNoticeMsgID
}
func (s *Service) currentAccountHotkeyNoticeID() uint16 {
	if s.options.currentClientVersion == "official-24.9" {
		return 454
	}
	return currentAccountHotkeyNoticeMsgID
}
