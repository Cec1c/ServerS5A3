package dnfbridge

import (
	"testing"
	"time"
)

// op1206 去重 + 节奏闸：
//   - 同档位同阶段 ⇒ 不重发（原来每 tick 都发，实测连发 8+ 个）
//   - 最小间隔内即使阶段变化也不得连发（现场实测在 op1303 之后几秒内连发 5 个 = 面板触发形态）
//   - 跨 60 秒档位、或超过最小间隔后阶段变化 ⇒ 必须发（不丢信息）
func TestSpendTimeProgressDedupeAndPacing(t *testing.T) {
	old := spendTimeProgressMinInterval
	defer func() { spendTimeProgressMinInterval = old }()

	spendTimeProgressMinInterval = 0
	conn := "game-test-spend-pacing"
	if !spendTimeProgressShouldSend(conn, 3, 0, 0) {
		t.Fatal("首次必须发")
	}
	if spendTimeProgressShouldSend(conn, 3, 10, 0) {
		t.Fatal("同档位同阶段不得重发")
	}
	if !spendTimeProgressShouldSend(conn, 3, 60, 0) {
		t.Fatal("跨 60 秒档位必须发")
	}
	if !spendTimeProgressShouldSend(conn, 3, 61, 1) {
		t.Fatal("阶段变化必须发")
	}

	spendTimeProgressMinInterval = time.Hour
	conn2 := "game-test-spend-pacing-2"
	if !spendTimeProgressShouldSend(conn2, 3, 0, 0) {
		t.Fatal("首包必须发（节奏闸不拦首包）")
	}
	if spendTimeProgressShouldSend(conn2, 3, 0, 1) {
		t.Fatal("最小间隔内即使阶段变化也不得连发（现场 5 连发的形态）")
	}

	forgetSpendTimeProgress(conn)
	forgetSpendTimeProgress(conn2)
	if !spendTimeProgressShouldSend(conn, 3, 61, 1) {
		t.Fatal("清理后应视为新会话，必须发")
	}
}
