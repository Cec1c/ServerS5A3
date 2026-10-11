package dnfbridge

import (
	"context"
	"strconv"
	"strings"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// op205 does not carry EXP for all four professions. Refresh the actor through
// the CURRENT EXE mode0 parser, with the native disguise pair (zero/zero) and
// the separate profession u8/u32 pair consumed by 1C03F20. The old C# subtype0
// layout reset the wrong fields and could reopen qualification UI every visit.
// EXP remains the canonical persisted stat; this refresh never grants EXP.
const (
	// currentExpertJobExpNotifyEnv 是"副职业经验变更后是否补发原生 mode0"的开关名。
	currentExpertJobExpNotifyEnv = "DNFBRIDGE_EXPERT_JOB_EXP_NOTIFY"

	currentExpertJobExpNotifyOn     = "on"
	currentExpertJobExpNotifyLegacy = "legacy"

	// currentExpertJobExpNotifySource 是这条体的来源串（进判读事件）。
	currentExpertJobExpNotifySource = "current_exe_native_scene_mode0_profession_pair_after_exp_gain"
)

// currentExpertJobExpNotifyMode 读开关并归一化：只有**显式**写 legacy 及其别名才回退，
// 其余（未设置、空、拼错、on）都是修复档 —— 与本包既有开关同口径
// （`currentDisjointBatchAckShape` / `peerUserInfoIdentityCarrierEnabled`）。
func currentExpertJobExpNotifyMode() string {
	switch strings.ToLower(strings.TrimSpace(envString(currentExpertJobExpNotifyEnv, ""))) {
	case currentExpertJobExpNotifyLegacy, "off", "0", "false", "no", "disable", "disabled", "none":
		return currentExpertJobExpNotifyLegacy
	default:
		return currentExpertJobExpNotifyOn
	}
}

// currentExpertJobExpNotifyEnabled 回答"要不要补发原生 mode0"。
func currentExpertJobExpNotifyEnabled() bool {
	return currentExpertJobExpNotifyMode() != currentExpertJobExpNotifyLegacy
}

// notifyCurrentExpertJobExperience refreshes only the current actor through the
// same native mode0 builder used by town entry. It is skipped in a dungeon and
// failures are logged without interrupting the committed operation's reply.
func (s *Service) notifyCurrentExpertJobExperience(session *gameSession, character dnfrepo.CharacterRecord, jobType byte, experience int64) {
	if !currentExpertJobExpNotifyEnabled() {
		return
	}
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	if sessionHasActiveDungeonRuntime(session) {
		s.logPacketEvent("game-current-expert-job-exp-userinfo-skipped",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"job_type", jobType,
			"experience", experience,
			"switch", currentExpertJobExpNotifyMode(),
			"reason", "vendor_forbids_subtype0_inside_dungeon")
		return
	}
	charID, err := strconv.ParseUint(strings.TrimSpace(character.CharacterID), 10, 16)
	if err != nil || charID == 0 {
		s.logPacketEvent("game-current-expert-job-exp-userinfo-skipped",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"job_type", jobType,
			"experience", experience,
			"switch", currentExpertJobExpNotifyMode(),
			"reason", "character_id_is_not_a_u16")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	// The legacy subtype0 is not the current EXE's native scene-object row.
	// Sending it into 1C09160 shifts the remaining fields (including modals).
	body := s.buildCurrentSceneObjectListBodyForSessionInContext(ctx, session,
		uint16(charID), character.Name, character, true, currentConnectionTownActorOwnerContext(session))
	if len(body) == 0 {
		s.logPacketEvent("game-current-expert-job-exp-userinfo-skipped",
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"job_type", jobType,
			"experience", experience,
			"switch", currentExpertJobExpNotifyMode(),
			"reason", "subtype0_body_unavailable")
		return
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0); err != nil {
		s.logGameEvent(session, "game-current-expert-job-exp-userinfo-send-failed",
			"char_id", session.selectedCharacterID,
			"job_type", jobType,
			"experience", experience,
			"body_len", len(body),
			"error", err)
		return
	}
	s.logPacketEvent("game-current-expert-job-exp-userinfo-send",
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"job_type", jobType,
		"experience", experience,
		"msg_id", uint16(dnfenum.CmdPacketSetUDPIPPort),
		"classification", 0,
		"body_len", len(body),
		"body_source", currentExpertJobExpNotifySource,
		"switch", currentExpertJobExpNotifyMode())
}
