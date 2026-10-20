package dnfbridge

import (
	"context"
	"encoding/binary"
	"math"
	"strconv"
	"strings"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// The generated enum's name for 0x0562 belongs to an older protocol table.
// Current NoPack statically registers this opcode to sub_11DBE40.
const currentStoryDigestLastLevelMsgID uint16 = 0x0562

// Latest official 24.9 registers class0/1377 -> 1455210 (one u32),
// and class1/1443 -> 1FC0020 at 43F9DF0. The older generated enum is
// offset by one/two here; 1378 now reads a different 20-byte structure.
const official249StoryDigestLastLevelMsgID uint16 = 1377

func (s *Service) storyDigestLastLevelMsgID() uint16 {
	if s.options.currentClientVersion == "official-24.9" {
		return official249StoryDigestLastLevelMsgID
	}
	return currentStoryDigestLastLevelMsgID
}

// storyDigestEnabled 报告运行期"剧情回顾(story digest)"子系统是否开启，来源是
// 启动环境变量 DNFBRIDGE_STORY_DIGEST_ENABLED（默认开启，见
// options.storyDigestDisabled）。置 false 是"该子系统的下发/推进是否把部分角色
// 推进受限状态"这一怀疑的单变量实验档：
//   - 选角 ack 之后不再下发 class0/op1378（story digest last-level 通知）；
//   - 客户端上报"开始播放"(op1445) 时不再推进、也不再持久化
//     story_digest_last_level，只记一条抑制事件（既有实现本来就没有可证明的
//     ACK，所以抑制路径同样零下发，唯一差别是 stat 保持不变）。
//
// 选项按反向存放，因此零值 options（测试与探针里的 Service 字面量）依旧保持
// 子系统开启：未设置开关时本函数恒为 true，所有下发与推进路径与加开关前逐字节
// 一致。读取点的完整清单见本文件两个抑制点，以及 service.go 的启动日志。
func (service *Service) storyDigestEnabled() bool {
	return service != nil && !service.options.storyDigestDisabled
}

// logCurrentStoryDigestDisabled 为每一条被开关抑制的路径记一条
// game-story-digest-disabled：复测时只看日志就能确认开关生效，不必逐包对比。
func (service *Service) logCurrentStoryDigestDisabled(session *gameSession, source string) {
	if service == nil {
		return
	}
	service.logGameEvent(session, "game-story-digest-disabled",
		"source", source,
		"reason", "story_digest_disabled",
		"switch", "DNFBRIDGE_STORY_DIGEST_ENABLED")
}

func buildCurrentStoryDigestLastLevelBody(character dnfrepo.CharacterRecord, hasCharacter bool, accountViewedLevel uint32) []byte {
	level := resolveCurrentStoryDigestSendLevel(character, hasCharacter, accountViewedLevel)
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, level)
	return body
}

// resolveCurrentStoryDigestSendLevel 按账号级记录决定 class0/op1378 实际下发的
// 等级：
//   - 基线是 max(角色自己的 story_digest_last_level, 账号级 viewed_level)；
//   - 若账号级 viewed_level 已经 >= 该角色当前等级，说明这个账号"全都看过"，
//     直接下发角色当前等级（客户端按等级选中同一段剧情回顾 ⇒ 不再强制播放）。
//
// 账号级记录缺失（老账号/内存后端/读失败）时 viewed_level=0，函数退化成
// max(自己的记录, 0)，与加账号记录之前逐字节一致。
func resolveCurrentStoryDigestSendLevel(character dnfrepo.CharacterRecord, hasCharacter bool, accountViewedLevel uint32) uint32 {
	sent := currentStoryDigestLastLevel(character, hasCharacter)
	if accountViewedLevel > sent {
		sent = accountViewedLevel
	}
	charLevel := currentStoryDigestCharacterLevel(character, hasCharacter)
	if accountViewedLevel > 0 && charLevel > 0 && accountViewedLevel >= charLevel {
		sent = charLevel
	}
	return sent
}

// currentStoryDigestCharacterLevel 返回角色当前等级（客户端用来选剧情回顾段）。
func currentStoryDigestCharacterLevel(character dnfrepo.CharacterRecord, hasCharacter bool) uint32 {
	if !hasCharacter || character.Level <= 0 {
		return 0
	}
	if uint64(character.Level) > uint64(math.MaxUint32) {
		return math.MaxUint32
	}
	return uint32(character.Level)
}

// accountStoryDigestViewedLevel 读取账号级"剧情回顾已看过"记录。
// 返回 (等级, 是否读到账号记录)：读失败或账号不存在时返回 (0,false)，调用方
// 退回"只看角色自己记录"的旧行为，并在日志里留下原因，不静默但也绝不因为
// 一条账号元数据缺失而阻断选角。
func (s *Service) accountStoryDigestViewedLevel(ctx context.Context, session *gameSession) (uint32, bool) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Account == nil {
		return 0, false
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return 0, false
	}
	account, found, err := repositories.Account.Load(ctx, accountID)
	if err != nil {
		s.logGameEvent(session, "game-current-story-digest-account-load-failed",
			"account_id", accountID,
			"error", err,
			"fallback", "character_story_digest_last_level_only")
		return 0, false
	}
	if !found {
		return 0, false
	}
	return dnfrepo.AccountStoryDigestViewedLevel(account), true
}

func currentStoryDigestLastLevel(character dnfrepo.CharacterRecord, hasCharacter bool) uint32 {
	if !hasCharacter || character.Stats == nil {
		return 0
	}
	value := character.Stats[dnfrepo.CharacterStoryDigestLastLevelStatKey]
	if value <= 0 {
		return 0
	}
	if value > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(value)
}

func (s *Service) sendCurrentStoryDigestLastLevel(session *gameSession, character dnfrepo.CharacterRecord, hasCharacter bool, source string) error {
	if !s.storyDigestEnabled() {
		// 关闭时连 body 都不构造：调用方（选角 ack 之后、进场之前）继续走
		// 原来的包序，只是少这一条 op1378。
		s.logCurrentStoryDigestDisabled(session, source)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	accountViewedLevel, accountFound := s.accountStoryDigestViewedLevel(ctx, session)
	characterStoryLevel := currentStoryDigestLastLevel(character, hasCharacter)
	body := buildCurrentStoryDigestLastLevelBody(character, hasCharacter, accountViewedLevel)
	sentLevel := binary.LittleEndian.Uint32(body)
	msgID := s.storyDigestLastLevelMsgID()
	s.logGameEvent(session, "game-current-story-digest-last-level-send",
		"source", source,
		"char_id", session.selectedCharacterID,
		"msg_id", msgID,
		"classification", 0,
		"last_level", sentLevel,
		"sent_level", sentLevel,
		"char_level", currentStoryDigestCharacterLevel(character, hasCharacter),
		"character_story_digest_last_level", characterStoryLevel,
		"account_viewed_level", accountViewedLevel,
		"account_record_found", accountFound,
		"migration_version", characterStoryDigestMigrationVersion(character, hasCharacter),
		"body_len", len(body),
		"body_source", "current_exe_sub_11DBE40_raw_u32_before_scene_finalizer")
	return s.sendGameUpperRawClass(session, msgID, body, 0)
}

func characterStoryDigestMigrationVersion(character dnfrepo.CharacterRecord, hasCharacter bool) uint32 {
	if !hasCharacter || character.Stats == nil {
		return 0
	}
	value := character.Stats[dnfrepo.CharacterStoryDigestMigrationVersionStatKey]
	if value <= 0 {
		return 0
	}
	if value > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(value)
}

func (s *Service) handleCurrentStoryDigestAccepted(session *gameSession, classification byte, body []byte) error {
	if !s.storyDigestEnabled() {
		// 关闭时只记日志：不改库、不回包。既有实现在这条上报上本来就没有
		// 任何 S2C 响应（advance 成功路径记的是
		// response=none_current_exe_has_no_proven_ack），所以抑制路径的回包
		// 形状与开关前一致（都是零下发），差别只有 story_digest_last_level
		// 不再被推进/持久化。
		s.logCurrentStoryDigestDisabled(session, "op1445_accepted_playback_start")
		return nil
	}
	if classification != dnfproto.DefaultChannelClassification {
		s.logGameEvent(session, "game-current-story-digest-advance-blocked",
			"classification", classification,
			"body_len", len(body),
			"reason", "current_exe_op1445_command_class_mismatch")
		return nil
	}
	if len(body) != 0 {
		s.logGameEvent(session, "game-current-story-digest-advance-blocked",
			"classification", classification,
			"body_len", len(body),
			"reason", "current_exe_op1445_is_bodyless")
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		s.logGameEvent(session, "game-current-story-digest-advance-blocked",
			"body_len", len(body),
			"reason", "selected_character_missing")
		return nil
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		s.logGameEvent(session, "game-current-story-digest-advance-failed",
			"char_id", session.selectedCharacterID,
			"reason", "character_repository_missing")
		return dnfrepo.ErrCharacterStoryDigestAdvanceUnavailable
	}

	characterID := strconv.Itoa(int(session.selectedCharacterID))
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	record, found, err := repositories.Character.Load(ctx, characterID)
	if err != nil {
		s.logGameEvent(session, "game-current-story-digest-character-load-failed",
			"char_id", session.selectedCharacterID,
			"error", err)
		return err
	}
	if !found || strings.TrimSpace(record.AccountID) != strings.TrimSpace(s.accountIDForSession(session)) {
		s.logGameEvent(session, "game-current-story-digest-advance-blocked",
			"char_id", session.selectedCharacterID,
			"found", found,
			"record_account_id", record.AccountID,
			"session_account_id", s.accountIDForSession(session),
			"reason", "selected_character_owner_mismatch")
		return nil
	}
	if record.Level <= 0 || uint64(record.Level) > uint64(math.MaxUint32) {
		s.logGameEvent(session, "game-current-story-digest-advance-blocked",
			"char_id", session.selectedCharacterID,
			"character_level", record.Level,
			"reason", "character_level_out_of_u32_range")
		return nil
	}
	previousLevel := currentStoryDigestLastLevel(record, true)
	previousVersion := characterStoryDigestMigrationVersion(record, true)
	previousAccountViewedLevel, accountFound := s.accountStoryDigestViewedLevel(ctx, session)
	if err := dnfrepo.AdvanceCharacterStoryDigest(ctx, repositories.Character, characterID, uint32(record.Level), dnfrepo.CurrentCharacterStoryDigestMigrationVersion); err != nil {
		s.logGameEvent(session, "game-current-story-digest-advance-failed",
			"char_id", session.selectedCharacterID,
			"character_level", record.Level,
			"previous_level", previousLevel,
			"previous_migration_version", previousVersion,
			"error", err)
		return err
	}
	accountAdvanceErr := s.advanceAccountStoryDigestViewedLevel(ctx, session, repositories, record.Level)
	accountViewedLevel := previousAccountViewedLevel
	if accountAdvanceErr == nil && uint32LevelExceeds(record.Level, accountViewedLevel) {
		accountViewedLevel = uint32(record.Level)
	}
	if accountAdvanceErr != nil {
		// 角色自己的记录已经推进（客户端不会再重复播这一段）；账号级记录是
		// 兜底，写失败只记事件，不影响本次回包形状（这条上报本来就没有 ACK）。
		s.logGameEvent(session, "game-current-story-digest-account-advance-failed",
			"char_id", session.selectedCharacterID,
			"account_id", s.accountIDForSession(session),
			"char_level", record.Level,
			"sent_level", record.Level,
			"account_viewed_level", accountViewedLevel,
			"previous_account_viewed_level", previousAccountViewedLevel,
			"account_record_found", accountFound,
			"error", businessLogDiagnostic(accountAdvanceErr))
	}
	s.logGameEvent(session, "game-current-story-digest-advanced",
		"char_id", session.selectedCharacterID,
		"previous_level", previousLevel,
		"current_level", record.Level,
		"char_level", record.Level,
		"sent_level", record.Level,
		"account_viewed_level", accountViewedLevel,
		"previous_account_viewed_level", previousAccountViewedLevel,
		"account_record_found", accountFound,
		"account_advanced", accountAdvanceErr == nil,
		"previous_migration_version", previousVersion,
		"migration_version", dnfrepo.CurrentCharacterStoryDigestMigrationVersion,
		"wire_body_len", 0,
		"response", "none_current_exe_has_no_proven_ack",
		"persistence_boundary", "current_exe_accepted_playback_start_notification")
	return nil
}

// advanceAccountStoryDigestViewedLevel 把账号级"剧情回顾已看过"记录推到该角色
// 当前等级（GREATEST 语义，幂等、可重入）。调用后的可见值由调用方按
// max(写前读到的旧值, 本角色等级) 记账，避免多查一次库。
func (s *Service) advanceAccountStoryDigestViewedLevel(
	ctx context.Context,
	session *gameSession,
	repositories dnfrepo.Group,
	characterLevel int,
) error {
	if !uint32LevelExceeds(characterLevel, 0) {
		return nil
	}
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	if accountID == "" {
		return dnfrepo.ErrAccountStoryDigestAccountMissing
	}
	if repositories.Account == nil {
		return dnfrepo.ErrAccountStoryDigestAdvanceUnavailable
	}
	return dnfrepo.AdvanceAccountStoryDigestViewedLevel(ctx, repositories.Account, accountID, uint32(characterLevel))
}

// uint32LevelExceeds 报告 level 是否是落在 uint32 范围内的正数且大于 current。
func uint32LevelExceeds(level int, current uint32) bool {
	return level > 0 && uint64(level) <= uint64(math.MaxUint32) && uint32(level) > current
}
