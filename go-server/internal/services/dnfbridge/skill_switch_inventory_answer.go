package dnfbridge

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「登记当场回帧」（`DNFBRIDGE_SKILL_SWITCH_ANSWER_REGISTRATION`）——**第三条下发路径**。
//
// # 为什么要有它（第八轮：65 字节的帧确实发出去了，真机面板仍然空）
//
// 到第七轮为止，服务端只有**场景边界**这一条下行路径：`initial_town_…`（选角/进城）、
// `legacy_type1345_scene_ready_ack_…`（客户端场景就绪）、`request37_…final_actor_skills`。
// 线上实测（`packet_log.txt`，2026-09-28）显示：
//
//	17:12:45.073..47.763Z  char 6 推 9 条记录 → 服务端**当场什么都没回**
//	18:38:57.839..39:00.526Z char 1 推 9 条记录 → 服务端**当场什么都没回**
//	18:39:04.534Z          收尾哨兵（客户端说"这一段登记完了"）→ 仍然什么都没回
//	18:39:08.536Z          直到"回到选择角色再进入"那两条边界才回帧（65 字节）
//
// ⇒ 玩家在**面板里**操作的那几秒，服务端一个字节都没答；答帧要等下一次重投影边界
// （最快 5 秒后，最慢十几分钟）。如果面板是在**打开/登记那一刻**从那张 map 建行的，
// 那么"等边界再答"**结构性地永远晚一步**。
//
// 本档做的就是补上这一步：客户端用 `ModeClear` 哨兵给这一段登记收尾时（`[21 2c ff ff]`
// 或现场那几种变体，见 `record.go`），**当场**把落库后的换装帧回一帧。
//
// # 契约
//
//   - 只由**哨兵**触发（`result.Sentinel`）：它才是客户端"这一段登记完了"的分隔符；
//     普通记录不触发（否则一段 13 条会回 13 帧）；
//   - 发送内容与边界路径**逐字节同源**：同一台发送器（`sendGameUpperRawClass`，
//     `msg_id=1428 classification=0`）、同一个构造器（`buildSkillSwitchLoadoutBody`）、
//     同一份落库状态（`skillswitch.Load` 之后再过 `skillSwitchLoadoutPairs`）；
//   - 空换装（没有 `slot_*` 行）⇒ **零字节**，只记 `…-answer-skipped`；
//   - 写线失败不影响落库：与 `applySkillSwitchInventory` 一样 fire-and-forget
//     （只记 `…-answer-failed`，绝不把上行处理打断）；
//   - 回退字面量 `off`（别名 `today` / `live` / `legacy` / `rollback` / `0` / `disable`）
//     = **与线上当前这一版逐字节相同**：这一档一行代码都不执行、不发、不记。
//
// # 仍未确证（不要在别处引用成结论）
//
//  1. 客户端面板是不是"打开那一刻"读那张 map —— 本档**不假设**这一点，它只是把
//     "玩家操作的那几秒服务端不应保持沉默"这条补上；若面板读的其实是别的载体，
//     本档不会让它显示任何东西（判据见 `_dist/buff-swap-round8.md` §3）；
//  2. 哨兵的 `Slot` 字节含义仍未证实（现场实测 ∈ {3,4,5,9,33}）；本档只看 `Mode==0x2c`，
//     与 `Record.Sentinel()` 同一判据，不新增任何语义假设。
const skillSwitchAnswerRegistrationEnv = "DNFBRIDGE_SKILL_SWITCH_ANSWER_REGISTRATION"

const (
	// skillSwitchAnswerRegistrationImmediate 是**默认档（修复档）**：哨兵到达时当场回一帧。
	skillSwitchAnswerRegistrationImmediate = "immediate"
	// skillSwitchAnswerRegistrationOff 是**一键回退字面量**：与线上当前这一版逐字节相同。
	skillSwitchAnswerRegistrationOff = "off"
)

const (
	// skillSwitchAnswerRegistrationSentEvent：当场回了帧。
	skillSwitchAnswerRegistrationSentEvent = "game-skill-switch-inventory-registration-answer-send"
	// skillSwitchAnswerRegistrationSkippedEvent：没回（空换装）。
	skillSwitchAnswerRegistrationSkippedEvent = "game-skill-switch-inventory-registration-answer-skipped"
	// skillSwitchAnswerRegistrationFailedEvent：构造/写线失败（零字节）。
	skillSwitchAnswerRegistrationFailedEvent = "game-skill-switch-inventory-registration-answer-failed"
)

// normalizeSkillSwitchAnswerRegistration 归一化本档（只有回退字面量才离开修复档）。
func normalizeSkillSwitchAnswerRegistration(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case skillSwitchAnswerRegistrationOff, "today", "live", "legacy", "rollback", "0", "disable":
		return skillSwitchAnswerRegistrationOff
	default:
		return skillSwitchAnswerRegistrationImmediate
	}
}

// currentSkillSwitchAnswerRegistration 直接自读 env（本档刻意不进 options.go）。
func currentSkillSwitchAnswerRegistration() string {
	return normalizeSkillSwitchAnswerRegistration(os.Getenv(skillSwitchAnswerRegistrationEnv))
}

// skillSwitchAnswerRegistrationSource 是这条路径写进日志的 source（与边界路径可区分）。
const skillSwitchAnswerRegistrationSource = "current_exe_op1428_registration_sentinel"

// answerSkillSwitchRegistration 在**哨兵**（登记分隔符）到达时当场回一帧换装。
//
// 只在默认档下执行；任何失败都只记事件、返回 nil（与落库同一条 fire-and-forget 契约）。
func (s *Service) answerSkillSwitchRegistration(session *gameSession, sentinel bool) {
	if s.buffSwapReapplyEnabled() {
		return
	}
	if s == nil || session == nil || !sentinel {
		return
	}
	if session.selectedCharacterID == 0 {
		return
	}
	if currentSkillSwitchAnswerRegistration() != skillSwitchAnswerRegistrationImmediate {
		return
	}
	if !s.skillSwitchLoadoutResendActive() {
		// 与边界路径同一组只读档口：回退档 / 非默认 tier / nil service ⇒ 不发。
		return
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		s.logPacketEvent(skillSwitchAnswerRegistrationSkippedEvent,
			"conn_id", session.connID,
			"char_id", session.selectedCharacterID,
			"source", skillSwitchAnswerRegistrationSource,
			"reason", "settings_repository_unavailable")
		return
	}
	characterID := strconv.FormatUint(uint64(session.selectedCharacterID), 10)
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	layout := s.skillSwitchInventoryRecordBodyLayout()
	state, found, err := skillswitch.Load(ctx, repos, characterID, layout)
	if err != nil || !found || len(state.Records) == 0 {
		s.logPacketEvent(skillSwitchAnswerRegistrationSkippedEvent,
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"scope", skillswitch.Scope(characterID),
			"source", skillSwitchAnswerRegistrationSource,
			"slot_count", len(state.Records),
			"load_error", err,
			"reason", "no_persisted_skill_switch_inventory")
		return
	}
	pairsMode := s.skillSwitchLoadoutPairsMode()
	body, err := buildSkillSwitchLoadoutBody(state.Records, pairsMode)
	if err != nil {
		s.logPacketEvent(skillSwitchAnswerRegistrationFailedEvent,
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"scope", skillswitch.Scope(characterID),
			"source", skillSwitchAnswerRegistrationSource,
			"loadout_rows", len(state.Records),
			"reason", "loadout_frame_build_failed",
			"build_error", err)
		return
	}
	if err := s.sendGameUpperRawClass(session, currentSkillSwitchInventoryOpcode, body, 0); err != nil {
		s.logPacketEvent(skillSwitchAnswerRegistrationFailedEvent,
			"conn_id", session.connID,
			"channel_id", session.channel.ID,
			"char_id", session.selectedCharacterID,
			"scope", skillswitch.Scope(characterID),
			"source", skillSwitchAnswerRegistrationSource,
			"loadout_rows", len(state.Records),
			"reason", "send_failed",
			"error", err)
		return
	}
	s.logPacketEvent(skillSwitchAnswerRegistrationSentEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"scope", skillswitch.Scope(characterID),
		"source", skillSwitchAnswerRegistrationSource,
		"msg_id", currentSkillSwitchInventoryOpcode,
		"classification", 0,
		"loadout_rows", len(state.Records),
		"count", int(body[0]),
		"body_len", len(body),
		"shape_guard", "pass",
		"downlink_shape_gate", "len_eq_1_plus_8_times_count",
		"pairs", skillSwitchLoadoutPairsText(state.Records, pairsMode),
		"body_hex", skillSwitchBytesHex(body),
		"trigger", "op1428_sentinel_registration_delimiter",
		// 这一档**不假设**面板读什么；它只保证"玩家操作的那几秒服务端不再沉默"。
		"answer_switch", currentSkillSwitchAnswerRegistration(),
		"answer_switch_default", skillSwitchAnswerRegistrationImmediate,
		"answer_rollback_literal", skillSwitchAnswerRegistrationOff,
		"answered_at", time.Now().UTC().Format(time.RFC3339Nano),
		"proof_state", "unproven_panel_data_source")
}

// skillSwitchBytesHex 是给日志用的十六进制渲染（与 `hex.EncodeToString` 同形，
// 刻意不引新 import）。
func skillSwitchBytesHex(body []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(body)*2)
	for _, b := range body {
		out = append(out, digits[b>>4], digits[b&0x0f])
	}
	return string(out)
}
