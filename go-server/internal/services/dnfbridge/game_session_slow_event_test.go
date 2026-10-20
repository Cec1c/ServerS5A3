package dnfbridge

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件钉死 A2-2 的两件事：
//  1. 慢事件告警必须点名**事件类型**（原行只有 priority+duration ⇒ 2026-09-27 23:30
//     那 7 条 ≥3 秒卡顿至今无法归属到任何一条路径）；
//  2. 阈值可配（DNFBRIDGE_EVENT_SLOW_THRESHOLD_MS），默认 3000 = createWriteTimeout
//     = 改动前的行为；只接受正整数毫秒，超上限截断。
func TestGameSessionSlowEventWarnNamesTheEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &lockedLogBuffer{}
	t.Setenv(gameSessionEventSlowThresholdEnv, "1") // 1ms ⇒ 5ms 的处理器必然触发告警。
	service := &Service{logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))}
	session := &gameSession{
		connID:              "slow-event-attribution",
		ctx:                 ctx,
		selectedCharacterID: 19,
		characterGeneration: 1,
	}
	if err := service.startGameSessionEvents(ctx, session); err != nil {
		t.Fatal(err)
	}
	defer stopTestGameSessionEvents(t, service, session)

	if err := service.postGameSessionCharacterEvent(session, "slow-warning-probe", 19, 1, func() error {
		time.Sleep(5 * time.Millisecond)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(logs.String(), "exceeded slow threshold") {
		time.Sleep(5 * time.Millisecond)
	}
	text := logs.String()
	line := ""
	for _, candidate := range strings.Split(text, "\n") {
		if strings.Contains(candidate, "exceeded slow threshold") {
			line = candidate
			break
		}
	}
	if line == "" {
		t.Fatalf("no slow-event WARN was written:\n%s", text)
	}
	for _, want := range []string{
		"msg=\"event processing exceeded slow threshold\"",
		"loop=dnf-game-session-slow-event-attribution",
		"priority=medium",
		"duration=",
		"event=slow-warning-probe",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("slow-event WARN missing %q:\n%s", want, line)
		}
	}
}

func TestGameSessionEventSlowThresholdPolicy(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 3000},
		{"   ", 3000},
		{"legacy", 3000},
		{"0", 3000},
		{"-5", 3000},
		{"abc", 3000},
		{"300", 300},
		{"1", 1},
		{"3000", 3000},
		{"600000", 600000},
		{"600001", 600000},
		{"999999999", 600000},
	}
	for _, testCase := range cases {
		t.Setenv(gameSessionEventSlowThresholdEnv, testCase.raw)
		if got := currentGameSessionEventSlowThresholdMillis(); got != testCase.want {
			t.Fatalf("switch=%q threshold=%dms want=%dms", testCase.raw, got, testCase.want)
		}
	}
	// 默认值必须仍然就是 createWriteTimeout（改动前 game_session_events.go 直接用
	// 它当阈值）：任何"默认值漂移"都会改变线上告警的触发点。
	t.Setenv(gameSessionEventSlowThresholdEnv, "")
	if got := currentGameSessionEventSlowThreshold(); got != createWriteTimeout {
		t.Fatalf("default threshold=%s want=%s", got, createWriteTimeout)
	}
}

// lockedLogBuffer 让事件循环 goroutine 与测试读取之间不再有数据竞争。
type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
