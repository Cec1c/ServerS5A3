package dnfbridge

// 本文件是**缺陷①（登录后武器按"没穿上/0 耐久"渲染）**的第五轮修法，也是第 4 轮
// （`login_authoritative_before_commit.go`）那条洞见的**推广**：第 4 轮只把**一帧**
// （name-tag mode0 重放）挪了位置，本文件把"提交前最后一件 mode1 载体必须是
// 装备承载那一帧"变成**每一条 mode1 都被记账、提交前被强制检查**的不变量。
//
// ── 一、为什么规则是这个（机制已在现场量到，不是猜）
//
// 正在跑的客户端自带 trace（`90CN_trace.log`，pid=3308，2026-09-29 00:09:08–00:15:42）
// 里，actor 的"已穿戴行数"唯一可观测计数器是
// `combat-power equipment snapshot generation=N mode=1 key=K rows=R`，它的取值**只**
// 跟着 `class0 op2 mode=1` 的派发走：
//
//	00:09:08.170 opcode=2 mode=0 → local-actor-create key=3（此时穿戴 0 行）
//	00:09:08.254 opcode=2 mode=1（163 字节 binding-only，**无装备 create 行**）
//	00:09:08.272 combat-power equipment snapshot generation=1 mode=1 key=3 rows=0  ← 被清零
//	00:09:09.083 opcode=2 mode=1（带 19 条装备 create 行）
//	00:09:09.110 combat-power equipment snapshot generation=2 mode=1 key=3 rows=19 ← 恢复
//	00:09:09.256 opcode=24 … 之后 5 次 op24，rows **一次都没变**
//
// ⇒ ① 客户端在 **op2 mode=0** 建 actor（穿戴为空）；② 在**它收到的最后一条 mode1**
//    上定型穿戴；③ **一条不带装备 create 行的 mode1 会把穿戴清零**；④ **op24 不重建**。
// 因此"提交那一刻客户端手里的最后一条 mode1"决定画面 —— 它必须带装备行。
//
// ── 二、本文件做什么（两条，都不新增包格式）
//
//  1. **记账**：登录路径上每一次 class0/op2 mode=1 的发送都由
//     `noteLoginMode1Send` 记一笔（`rows` = 从**已发出的包体**里读回的实际装备
//     create 行数，判据复用第 3 轮的 `loginActorGenerationMode1WireRowAudit`），
//     并打一条 `game-login-mode1-send` 事件。于是"哪一条 mode1 是最后一条"
//     在 `packet_log.txt` 里是**可 grep 的事实**，不依赖任何人的记忆。
//  2. **强制**：`enforceLoginMode1LastBeforeCommit` 在 typed op24 **发送之前**被调用。
//     若最后一笔记账**不带装备行**，就把**本会话已经发过的那一份装备承载 mode1**
//     （逐字节同一份 body，不重新构建、不拼接）再发一次，使它成为提交前最后一条
//     mode1；若最后一笔本来就带装备行 ⇒ **一个字节都不发**。
//
// 为什么不会回退：默认档在"顺序本来就对"的所有路由（冷登录、进副本后回城、
// 副本内重建）上**发送字节数与改动前完全相同**（只多日志）；只有出现
// "equipment-less mode1 落在最后"这一种形状时才会多一帧，而那一帧的字节等于
// 本会话已经发过的装备 mode1。
//
// ── 三、开关（消费点自读 env，不进 options.go；默认即修复，回退必须显式）
//
//	DNFBRIDGE_LOGIN_MODE1_LAST_BEFORE_COMMIT
//	  默认（含未设置/写错的值）= equipment_mode1_last_before_op24：上面的强制检查生效；
//	  record_only（别名 off/0/false/no/legacy/today/disable/disabled/none/skip）
//	    = 一键回退到今天：**不做任何强制、不补发任何一帧**（只保留记账事件）。

import (
	"os"
	"strconv"
	"strings"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
)

const (
	// loginMode1LastBeforeCommitEnv 是"提交前最后一条 mode1 必须带装备行"的开关名。
	loginMode1LastBeforeCommitEnv = "DNFBRIDGE_LOGIN_MODE1_LAST_BEFORE_COMMIT"

	loginMode1LastBeforeCommitEnforced    = "equipment_mode1_last_before_op24"
	loginMode1LastBeforeCommitRecordOnly  = "record_only"
	loginMode1LastBeforeCommitPosition    = "immediately_before_the_typed_op24_final_transition_commit"
	loginMode1LastBeforeCommitSendEvent   = "game-login-mode1-send"
	loginMode1LastBeforeCommitAuditEvent  = "game-login-mode1-last-before-commit"
	loginMode1LastBeforeCommitMechanism   = "the_last_class0_op2_mode1_dispatch_before_the_commit_fixes_the_actor_worn_set_and_a_mode1_without_equipment_create_rows_zeroes_it"
	loginMode1LastBeforeCommitClientProof = "90CN_trace_log_pid_3308_20260929_000908_mode1_163_bytes_rows_0_then_mode1_with_19_rows_rows_19_and_five_op24_commits_left_rows_unchanged"
)

// loginMode1SendRecord 是登录路径上**已经发出**的一帧 class0/op2 mode=1 的记账。
type loginMode1SendRecord struct {
	// Source 是发送点（调用方给的字符串），用于日志归属。
	Source string
	// Body 是**逐字节**那一帧已经发出去的包体（重发时用它，绝不重新构建）。
	Body []byte
	// RowCount 是从 Body 里读回的装备 create 行数（0 = 这一帧不带装备行）。
	RowCount int
	// EquipmentBearing 为真表示这一帧带装备 create 行（= 可以作为"最后一件"）。
	EquipmentBearing bool
	// Reasserted 为真表示这一帧是**强制检查补发的**（不是路由原本就有的那一帧）。
	Reasserted bool
}

// loginMode1LastBeforeCommitEnforcedTier 回答"要不要在提交前强制最后一条 mode1 带装备行"。
// 只有**显式**写回退字面量才关闭；其余取值（含未设置、写错的串）都是修复档。
func loginMode1LastBeforeCommitEnforcedTier() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(loginMode1LastBeforeCommitEnv))) {
	case loginMode1LastBeforeCommitRecordOnly, "skip", "off", "0", "false", "no",
		"legacy", "today", "disable", "disabled", "none":
		return false
	default:
		return true
	}
}

// loginMode1LastBeforeCommitSwitch 只用于事件/诊断。
func loginMode1LastBeforeCommitSwitch() string {
	if loginMode1LastBeforeCommitEnforcedTier() {
		return loginMode1LastBeforeCommitEnforced
	}
	return loginMode1LastBeforeCommitRecordOnly
}

// noteLoginMode1Send 记一笔"登录路径上发出了一帧 class0/op2 mode=1"。
//
// 行数**从已发出的包体里读回**（`loginActorGenerationMode1WireRowAudit`，与第 3/4 轮
// 同一个读取器），因此事件里的 `rows` 是"这一包真正带出去的装备 create 行数"，
// 不是任何中间标志位。
func (s *Service) noteLoginMode1Send(session *gameSession, body []byte, source string) {
	if s == nil || session == nil || len(body) == 0 {
		return
	}
	rows, _, _, _, _, auditOK := loginActorGenerationMode1WireRowAudit(body)
	record := loginMode1SendRecord{
		Source:           source,
		Body:             append([]byte(nil), body...),
		RowCount:         rows,
		EquipmentBearing: auditOK && rows > 0,
	}
	session.loginMode1Trace = append(session.loginMode1Trace, record)
	s.logPacketEvent(loginMode1LastBeforeCommitSendEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"mode1_index", len(session.loginMode1Trace),
		"msg_id", uint16(dnfenum.CmdPacketSetUDPIPPort),
		"classification", 0,
		"body_len", len(body),
		"rows", rows,
		"equipment_bearing", boolDigit(record.EquipmentBearing),
		"row_audit_ok", boolDigit(auditOK),
		"switch", loginMode1LastBeforeCommitSwitch(),
		"switch_env", loginMode1LastBeforeCommitEnv)
}

// enforceLoginMode1LastBeforeCommit 在 typed op24 **发送之前**跑：保证提交前最后一条
// mode1 是装备承载那一帧。
//
// **调用契约**：调用方必须已经持有 `session.townMu`（本函数**不加锁**，否则会自死锁；
// 调用点 `sendCurrentTownTransitionLocked` 正是持锁调用的），并把"这条路由是否属于
// 本次选角进城"由调用方算好传进来（`owned`）。
//
// 语义（全部可 grep）：
//   - 回退档（`record_only`）：不做任何强制、不补发任何一帧，只写事件
//     `action=rollback_record_only_no_enforcement`（= 今天逐字节）；
//   - 本路由没有记账（例如站内回城用副本链路，不在本文件的记账点）：`action=no_mode1_recorded`；
//   - 最后一笔带装备行：`action=kept_last_mode1_is_equipment_bearing`，**零字节**；
//   - 最后一笔不带装备行、且本会话发过装备承载 mode1：把那一份 body **原样**再发一次
//     （`msg_id=2 classification=0`，与它第一次发出时同一台发送器
//     `sendGameUpperRawClass`），并使它成为最后一笔，`action=reasserted_equipment_mode1_last`；
//   - 最后一笔不带装备行、但本会话**从未**发过装备承载 mode1：`action=no_equipment_mode1_available`
//     —— fail-open，**绝不编造一帧**（与 list 3 容器那条 fail-closed 口径一致）。
func (s *Service) enforceLoginMode1LastBeforeCommit(session *gameSession, source string, owned bool) error {
	if s == nil || session == nil {
		return nil
	}
	if !owned {
		return nil
	}
	if !loginMode1LastBeforeCommitEnforcedTier() {
		s.logMode1LastBeforeCommit(session, source, "rollback_record_only_no_enforcement", -1, loginMode1SendRecord{})
		return nil
	}
	if len(session.loginMode1Trace) == 0 {
		s.logMode1LastBeforeCommit(session, source, "no_mode1_recorded", -1, loginMode1SendRecord{})
		return nil
	}
	lastIndex := len(session.loginMode1Trace) - 1
	last := session.loginMode1Trace[lastIndex]
	if last.EquipmentBearing {
		s.logMode1LastBeforeCommit(session, source, "kept_last_mode1_is_equipment_bearing", lastIndex, last)
		return nil
	}
	equipmentIndex := -1
	for index := lastIndex - 1; index >= 0; index-- {
		if session.loginMode1Trace[index].EquipmentBearing {
			equipmentIndex = index
			break
		}
	}
	if equipmentIndex < 0 {
		s.logMode1LastBeforeCommit(session, source, "no_equipment_mode1_available", lastIndex, last)
		return nil
	}
	reassert := session.loginMode1Trace[equipmentIndex]
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), reassert.Body, 0); err != nil {
		return err
	}
	record := loginMode1SendRecord{
		Source:           source,
		Body:             reassert.Body,
		RowCount:         reassert.RowCount,
		EquipmentBearing: true,
		Reasserted:       true,
	}
	session.loginMode1Trace = append(session.loginMode1Trace, record)
	s.logMode1LastBeforeCommit(session, source, "reasserted_equipment_mode1_last", len(session.loginMode1Trace)-1, record)
	return nil
}

// logMode1LastBeforeCommit 打出判读事件：这条登录路由在 typed op24 之前一共发了几帧
// mode1、最后一帧带不带装备行、以及本次的动作（零字节 / 补发 / fail-open）。
func (s *Service) logMode1LastBeforeCommit(
	session *gameSession,
	source string,
	action string,
	lastIndex int,
	last loginMode1SendRecord,
) {
	rows := make([]string, 0, len(session.loginMode1Trace))
	for index, record := range session.loginMode1Trace {
		rows = append(rows, mode1TraceEntry(index, record))
	}
	s.logPacketEvent(loginMode1LastBeforeCommitAuditEvent,
		"conn_id", session.connID,
		"channel_id", session.channel.ID,
		"char_id", session.selectedCharacterID,
		"source", source,
		"msg_id", uint16(dnfenum.CmdPacketSetUDPIPPort),
		"position", loginMode1LastBeforeCommitPosition,
		"action", action,
		"mode1_sends_before_commit", len(session.loginMode1Trace),
		"last_mode1_index", lastIndex,
		"last_mode1_source", last.Source,
		"last_mode1_rows", last.RowCount,
		"last_mode1_equipment_bearing", boolDigit(last.EquipmentBearing),
		"last_mode1_reasserted", boolDigit(last.Reasserted),
		"mode1_send_order", strings.Join(rows, ","),
		"mechanism", loginMode1LastBeforeCommitMechanism,
		"client_proof", loginMode1LastBeforeCommitClientProof,
		"switch", loginMode1LastBeforeCommitSwitch(),
		"switch_env", loginMode1LastBeforeCommitEnv)
}

// mode1TraceEntry 把一笔记账渲染成 `index:source:rows:equipment`，供事件里原样回显。
func mode1TraceEntry(index int, record loginMode1SendRecord) string {
	equipment := "0"
	if record.EquipmentBearing {
		equipment = "1"
	}
	return strconv.Itoa(index) + ":" + record.Source + ":" + strconv.Itoa(record.RowCount) + ":" + equipment
}
