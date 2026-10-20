package dnfbridge

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「登记当场回帧」（`DNFBRIDGE_SKILL_SWITCH_ANSWER_REGISTRATION`）的钉子。
//
// 现场依据（`packet_log.txt`，2026-09-28）：玩家在面板里登记的那几秒，服务端**一个字节都没回** ——
//
//	18:38:57.839..18:39:00.526Z char 1 推 9 条记录 ⇒ 无任何下行
//	18:39:04.534Z              收尾哨兵        ⇒ 无任何下行
//	18:39:08.536Z              直到"回到选择角色再进入"才回那 65 字节的帧
//
// 本档让服务端在**哨兵**（客户端给这一段登记收尾）到达时当场回一帧。

// TestSkillSwitchAnswerRegistrationSendsOnSentinel 是主用例：记录阶段**不发**，
// 哨兵一到**立刻**发一帧，内容与边界路径逐字节同源。
func TestSkillSwitchAnswerRegistrationSendsOnSentinel(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)
	seedSkillSwitchDeletePreState(t, fixture.repos)

	for _, body := range skillSwitchDeleteBurstBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	// 记录阶段：一帧都不许发（否则一段 9 条会回 9 帧）。
	if packets := drainUpperPackets(t, fixture.connection); len(packets) != 0 {
		t.Fatalf("records alone wrote %d packets, want 0 (only the sentinel answers)", len(packets))
	}

	applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)

	packets := drainUpperPackets(t, fixture.connection)
	if len(packets) != 1 {
		t.Fatalf("sentinel wrote %d packets, want exactly the one in-band answer", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchDeleteKeepFrameHex {
		t.Fatalf("in-band answer body = %s\nwant (same as the boundary frame) %s",
			got, skillSwitchDeleteKeepFrameHex)
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchAnswerRegistrationSentEvent,
		"char_id=1",
		"source=" + skillSwitchAnswerRegistrationSource,
		"msg_id=" + strconv.Itoa(int(currentSkillSwitchInventoryOpcode)),
		"loadout_rows=20",
		"count=20",
		"body_len=161",
		"shape_guard=pass",
		"downlink_shape_gate=len_eq_1_plus_8_times_count",
		"pairs=" + skillSwitchDeleteKeepPairsText,
		"body_hex=" + skillSwitchDeleteKeepFrameHex,
		"trigger=op1428_sentinel_registration_delimiter",
		"answer_switch=" + skillSwitchAnswerRegistrationImmediate,
		"answer_switch_default=" + skillSwitchAnswerRegistrationImmediate,
		"answer_rollback_literal=" + skillSwitchAnswerRegistrationOff,
		"proof_state=unproven_panel_data_source",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("answer log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchAnswerRegistrationRollbackLiteralIsByteForByteToday 钉回退字面量：
// `off` 档下哨兵**不发任何字节**（= 线上当前这一版）。
func TestSkillSwitchAnswerRegistrationRollbackLiteralIsByteForByteToday(t *testing.T) {
	t.Setenv(skillSwitchAnswerRegistrationEnv, skillSwitchAnswerRegistrationOff)
	fixture := newSkillSwitchLoadoutFixture(t)
	seedSkillSwitchDeletePreState(t, fixture.repos)

	registerSkillSwitchDeleteBurst(t, fixture)

	if packets := drainUpperPackets(t, fixture.connection); len(packets) != 0 {
		t.Fatalf("the rollback literal wrote %d packets, want 0", len(packets))
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	if strings.Contains(log, skillSwitchAnswerRegistrationSentEvent) {
		t.Fatalf("the rollback literal must not write the answer event; log=\n%s", log)
	}
	// 落库不受影响：20 行一行不少（第六轮的删除语义仍然生效）。
	if got := len(readStoredSlotValues(t, fixture.repos, "1")); got != 20 {
		t.Fatalf("rows = %d, want the untouched 20", got)
	}
}

// TestSkillSwitchAnswerRegistrationSkipsEmptyLoadout 钉失败契约：
// 空换装 ⇒ 只记 `…-answer-skipped`，零字节。
func TestSkillSwitchAnswerRegistrationSkipsEmptyLoadout(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)

	applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)

	if packets := drainUpperPackets(t, fixture.connection); len(packets) != 0 {
		t.Fatalf("an empty loadout wrote %d packets, want 0", len(packets))
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchAnswerRegistrationSkippedEvent,
		"source=" + skillSwitchAnswerRegistrationSource,
		"reason=no_persisted_skill_switch_inventory",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("skip log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchAnswerRegistrationIgnoresPlainRecords 钉触发点只有哨兵。
func TestSkillSwitchAnswerRegistrationIgnoresPlainRecords(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)
	seedSkillSwitchDeletePreState(t, fixture.repos)

	for _, body := range skillSwitchDeleteBurstBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	if packets := drainUpperPackets(t, fixture.connection); len(packets) != 0 {
		t.Fatalf("plain records wrote %d packets, want 0", len(packets))
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	if strings.Contains(log, skillSwitchAnswerRegistrationSentEvent) {
		t.Fatalf("only a sentinel may answer; log=\n%s", log)
	}
}

// TestNormalizeSkillSwitchAnswerRegistrationTable 钉开关口径。
func TestNormalizeSkillSwitchAnswerRegistrationTable(t *testing.T) {
	for _, value := range []string{"", "immediate", "IMMEDIATE", "on", "unknown"} {
		if got := normalizeSkillSwitchAnswerRegistration(value); got != skillSwitchAnswerRegistrationImmediate {
			t.Fatalf("normalize(%q) = %q, want %q", value, got, skillSwitchAnswerRegistrationImmediate)
		}
	}
	for _, value := range []string{"off", "OFF", "today", "live", "legacy", "rollback", "0", "disable"} {
		if got := normalizeSkillSwitchAnswerRegistration(value); got != skillSwitchAnswerRegistrationOff {
			t.Fatalf("normalize(%q) = %q, want %q", value, got, skillSwitchAnswerRegistrationOff)
		}
	}
	t.Setenv(skillSwitchAnswerRegistrationEnv, "")
	if got := currentSkillSwitchAnswerRegistration(); got != skillSwitchAnswerRegistrationImmediate {
		t.Fatalf("current() = %q, want %q", got, skillSwitchAnswerRegistrationImmediate)
	}
	t.Setenv(skillSwitchAnswerRegistrationEnv, "off")
	if got := currentSkillSwitchAnswerRegistration(); got != skillSwitchAnswerRegistrationOff {
		t.Fatalf("current() = %q, want %q", got, skillSwitchAnswerRegistrationOff)
	}
	_ = skillswitch.Scope("1")
}
