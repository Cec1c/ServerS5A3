package dnfbridge

import (
	"context"
	"errors"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 本文件实现 A1「op34 永不静默丢弃」的开关与判定。
//
// 线上直证（`_dist/quest-finish-latency-diagnosis.md` §2.2 / §6 A1，2026-09-27）：
// 23:28–23:29 操作者连点 4 次 op34（任务 5146，conn game-000006），4 次都是结算事务
// 撞满 `createWriteTimeout = 3s`（repository_timeouts.go:6，同时被当作资源加载与结算
// 两处 ctx 的上界），日志为
//
//	game-upper-finish-quest-blocked … reason=pvf_db_atomic_finish_rejected
//	error=context deadline exceeded
//	sql: transaction has already been committed or rolled back
//
// 但 `context.DeadlineExceeded` **不属于** `currentFinishQuestBusinessFailure()`，
// 旧代码在 quest_finish.go:196-199 直接 `return nil` ⇒ 服务端**一个字节都不回**，
// 客户端每次点击空等 ≥3 秒。这就是操作者报的「点 NPC 完成任务卡 3 秒以上」。
//
// 开关语义（**fail-safe：只有字面量 legacy 才回到旧行为**）：
//   - 未设置 / 空 / 未知值（含 on/true/1/yes）= **修复档**：结算 ctx 超时或取消时
//     也发**与业务失败逐字节相同**的失败包（sendGameUpperFailure(34, 22)，体 `00 16`）。
//   - `legacy` = 逐字节回到今天（静默 `return nil`）。这是唯一的回退值；因此把开关
//     误设成 `off`/`0`/`false` **不会**静默——宁可多回一个失败包，也绝不静默丢包。
const currentQuestFinishFailureReplyEnv = "DNFBRIDGE_QUEST_FINISH_FAILURE_REPLY"

// currentQuestFinishFailureReplyLegacy 是唯一的回退值（= 2026-09-27 的静默行为）。
const currentQuestFinishFailureReplyLegacy = "legacy"

// currentQuestFinishFailureReplyEnabled 在运行时读取开关，默认返回 true。
func currentQuestFinishFailureReplyEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(currentQuestFinishFailureReplyEnv)), currentQuestFinishFailureReplyLegacy)
}

// currentQuestFinishFailureReplyMode 返回启动回显用的有效档位。
func currentQuestFinishFailureReplyMode() string {
	if currentQuestFinishFailureReplyEnabled() {
		return "on"
	}
	return currentQuestFinishFailureReplyLegacy
}

// currentFinishQuestContextFailureReason 判定结算/加载错误是否属于"上下文超时/取消"。
//
// 返回非空字符串时，调用方必须回一个失败包（当开关处于修复档）：`context deadline
// exceeded` 正是这条路径上唯一被实测到的 ≥3 秒成本，它绝不能再变成"点了没反应"。
// 错误链与线上一致：WithinCharacterSettlement 是 errors.Join(applyErr, tx.Rollback())，
// 所以这里必须用 errors.Is 穿透 Join/fmt.Errorf 包装。
//
// 注意两个 reason 的措辞保持 `settlement_` 前缀不变（线上 grep 口径已由 2026-09-28
// 的实现固定）；调用方用独立的 `stage=` 字段区分到底是哪一段超时。
func currentFinishQuestContextFailureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "settlement_context_deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "settlement_context_canceled"
	default:
		return ""
	}
}

// replyCurrentFinishQuestContextFailure 发送 op34 的**失败回执**，并写一条可 grep 的
// 证据事件。它复用的就是 `handleCurrentFinishQuestTimed` 里业务失败分支**已经在线上跑**
// 的那条失败包（`sendGameUpperFailure(34, 22)`，体 `00 16`，15 字节帧
// `0122000f00000056b961c200000016`）——**不存在第二种回包形状**。
//
// 只在 `currentFinishQuestContextFailureReason(err) != "" &&
// currentQuestFinishFailureReplyEnabled()` 成立时调用；`legacy` 档或非上下文失败必须
// 保持今天的 `return nil`（调用点自己判条件，因此"哪里会回包"在源码里一眼可见）。
//
// stage 取值（与调用点一一对应，用于把"3 秒在哪一段"和"哪条路径回了包"钉在一起）：
//   - settlement                    结算事务（ApplyFinishSettlement）
//   - quest_catalog_load            任务目录加载
//   - progression_or_item_pvf_load  progression 表 + 掉落目录加载
//   - profession_pvf_load           专家职业（profiles + skill catalog）加载
func (s *Service) replyCurrentFinishQuestContextFailure(
	session *gameSession,
	questID uint16,
	stage string,
	timing *currentQuestFinishTiming,
	err error,
) error {
	s.logGameEvent(session, "game-upper-finish-quest-failure-reply",
		"quest_id", questID,
		"stage", stage,
		"msg_id", uint16(dnfenum.CmdPacketFinishQuest),
		"classification", dnfproto.DefaultChannelClassification,
		"failure_code", 22,
		"reason", currentFinishQuestContextFailureReason(err),
		"error", err)
	timing.markAckStart()
	sendErr := s.sendGameUpperFailure(session, uint16(dnfenum.CmdPacketFinishQuest), 22)
	timing.markAckDone()
	return sendErr
}
