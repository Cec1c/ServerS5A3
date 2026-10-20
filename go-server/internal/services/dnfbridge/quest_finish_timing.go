package dnfbridge

import (
	"os"
	"strings"
	"time"
)

// 本文件实现 A2-3：op34 的三段计时埋点（load / 结算事务 / ACK）。
//
// 开关 `DNFBRIDGE_QUEST_FINISH_TIMING`：**默认 off**。它每个 op34 请求写一行包日志，
// 是排查期开关，不是常开埋点（旧报告 §2.1 的日志洪水教训）。
// 打开后每个 op34 请求写且只写一行：
//
//	EVENT kind=game-upper-finish-quest-timing quest_id=… load_ms=… tx_ms=… ack_ms=… total_ms=…
//
// 三段定义：
//   - load_ms：进入 handleCurrentFinishQuest 到资源就绪（任务目录 / progression+道具 PVF /
//     专家职业资源）。这三样里的 progression.Load 目前每次完成都重做（无缓存）。
//   - tx_ms：ApplyFinishSettlement（结算事务，ctx 上界 = createWriteTimeout = 3s）。
//     线上那次「3 秒撞满」就是这一段；失败时 tx_ms ≈ 3000。
//   - ack_ms：组 ACK + 发包（buildCurrentFinishQuestSuccessBody → sendGameUpperSuccess）。
const currentQuestFinishTimingEnv = "DNFBRIDGE_QUEST_FINISH_TIMING"

// currentQuestFinishTimingEnabled 只认显式打开值；默认（未设置/空/未知）off。
func currentQuestFinishTimingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(currentQuestFinishTimingEnv))) {
	case "on", "1", "true", "yes", "enabled", "enable":
		return true
	default:
		return false
	}
}

// currentQuestFinishTiming 记录一次 op34 的三段耗时。nil 即开关关闭（默认），
// 所有打点方法都对 nil 接收者安全，因此调用点不需要任何 if。
type currentQuestFinishTiming struct {
	questID   uint16
	loadStart time.Time
	loadEnd   time.Time
	txStart   time.Time
	txEnd     time.Time
	ackStart  time.Time
	ackEnd    time.Time
}

// startCurrentQuestFinishTiming 只在开关打开时返回记录器；默认返回 nil（零成本）。
func startCurrentQuestFinishTiming() *currentQuestFinishTiming {
	if !currentQuestFinishTimingEnabled() {
		return nil
	}
	return &currentQuestFinishTiming{loadStart: time.Now()}
}

func (t *currentQuestFinishTiming) markQuest(questID uint16) {
	if t == nil {
		return
	}
	t.questID = questID
}

func (t *currentQuestFinishTiming) markLoadDone() {
	if t == nil {
		return
	}
	t.loadEnd = time.Now()
}

func (t *currentQuestFinishTiming) markTxStart() {
	if t == nil {
		return
	}
	t.txStart = time.Now()
}

func (t *currentQuestFinishTiming) markTxDone() {
	if t == nil {
		return
	}
	t.txEnd = time.Now()
}

func (t *currentQuestFinishTiming) markAckStart() {
	if t == nil {
		return
	}
	t.ackStart = time.Now()
}

func (t *currentQuestFinishTiming) markAckDone() {
	if t == nil {
		return
	}
	t.ackEnd = time.Now()
}

// log 写唯一那一行 game-upper-finish-quest-timing。由 handleCurrentFinishQuest 的
// defer 调用 ⇒ 成功、业务失败、上下文失败、以及加载阶段失败都会留一行。
func (t *currentQuestFinishTiming) log(s *Service, session *gameSession) {
	if t == nil || s == nil {
		return
	}
	loadEnd := t.loadEnd
	if loadEnd.IsZero() {
		// 加载阶段就没走完：load_ms = 从进入到现在的全部耗时。
		loadEnd = time.Now()
	}
	s.logGameEvent(session, "game-upper-finish-quest-timing",
		"quest_id", t.questID,
		"load_ms", currentQuestFinishStageMillis(t.loadStart, loadEnd),
		"tx_ms", currentQuestFinishStageMillis(t.txStart, t.txEnd),
		"ack_ms", currentQuestFinishStageMillis(t.ackStart, t.ackEnd),
		"total_ms", currentQuestFinishStageMillis(t.loadStart, time.Now()))
}

// currentQuestFinishStageMillis 把一段耗时转成毫秒；未打点的段（零值）恒为 0。
func currentQuestFinishStageMillis(start time.Time, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
