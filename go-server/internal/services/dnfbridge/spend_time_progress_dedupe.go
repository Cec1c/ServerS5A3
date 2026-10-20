package dnfbridge

import (
	"sync"
	"time"
)

// op1206（`currentSpendTimeProgressMsgID`，活动消耗时间进度）内容级去重 + 节奏闸。
//
// 现场证据（2026-09-27，全自动探针 A/B，操作者零参与）：
//   - 我们登录后连发 8+ 个 37 B 的 op1206；厂商同一相位**只连发 3 个**，且**都在
//     op108 与 op1303 之间**，之后每次更新间隔 30~60 秒。
//   - 我们实测在 op1303 之后**几秒内又连发 5 个** ⇒ 客户端弹出「熟练度 Lv」面板
//     （截图模板 MAD 0.00；屏蔽 op1206 后升到 78.65 = 面板消失）。
//   - `online_event_spend_time.go` 自己的注释写明：客户端在消费 op1206 时会
//     **绑定 2347 HUD/hover 控件**，在这条链路里插入额外包会让进度/控件状态错位。
//
// 修法是内容级对齐节奏，而不是屏蔽包：进度没变不发（同档位同阶段），并且两次下发
// 之间至少间隔 spendTimeProgressMinInterval（默认 30 秒）。这样既不丢信息，
// 也不会让客户端死等，同时彻底消灭"几秒内连发"这一可复现的面板触发形态。
const spendTimeProgressSecondsBucket = 60

// spendTimeProgressMinInterval 是最小重发间隔。用变量而非常量，便于单测把它置 0
// 或放大来分别验证"档位/阶段"逻辑与"节奏闸"逻辑。
var spendTimeProgressMinInterval = 30 * time.Second

type spendTimeProgressKey struct {
	connID      string
	characterID uint16
}

type spendTimeProgressMark struct {
	bucket uint64
	stages uint32
	at     time.Time
}

var (
	spendTimeProgressMu    sync.Mutex
	spendTimeProgressMarks = map[spendTimeProgressKey]spendTimeProgressMark{}
)

// spendTimeProgressShouldSend 报告本次进度是否需要真正下发，并记录最新进度。
func spendTimeProgressShouldSend(connID string, characterID uint16, onlineSeconds uint64, completedStages uint32) bool {
	bucket := onlineSeconds / spendTimeProgressSecondsBucket
	key := spendTimeProgressKey{connID: connID, characterID: characterID}
	spendTimeProgressMu.Lock()
	defer spendTimeProgressMu.Unlock()
	if previous, ok := spendTimeProgressMarks[key]; ok {
		if previous.bucket == bucket && previous.stages == completedStages {
			return false
		}
		if time.Since(previous.at) < spendTimeProgressMinInterval {
			return false
		}
	}
	spendTimeProgressMarks[key] = spendTimeProgressMark{
		bucket: bucket,
		stages: completedStages,
		at:     time.Now(),
	}
	return true
}

// forgetSpendTimeProgress 在会话结束时清理标记，避免长期运行下 map 无界增长。
func forgetSpendTimeProgress(connID string) {
	if connID == "" {
		return
	}
	spendTimeProgressMu.Lock()
	defer spendTimeProgressMu.Unlock()
	for key := range spendTimeProgressMarks {
		if key.connID == connID {
			delete(spendTimeProgressMarks, key)
		}
	}
}
