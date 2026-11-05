package dnfbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/platform/metrics"
)

func dungeonDeathTimingDiagnosticEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == dungeonDeathTimingDiagnosticMsg {
			events = append(events, event)
		}
	}
	return events
}

func TestDungeonDeathTimingDiagnosticKeepsOnlyFixedTimingsWithoutPacketLog(t *testing.T) {
	var logs bytes.Buffer
	service := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), performance: newBridgePerformanceMetrics(metrics.New())}
	if service.packetLog != nil {
		t.Fatal("test requires raw packet logging to be disabled")
	}

	timingFields := []struct {
		key   string
		value int64
	}{
		{"总毫秒", 127},
		{"副本锁等待毫秒", 3},
		{"前置校验毫秒", 5},
		{"掉落规划毫秒", 7},
		{"死亡通知毫秒", 11},
		{"经验处理毫秒", 13},
		{"任务处理毫秒", 17},
		{"收尾毫秒", 19},
		{"任务资格查询毫秒", 23},
		{"杀敌任务毫秒", 29},
		{"任务掉落毫秒", 31},
		{"杀怪任务毫秒", 37},
		{"房间缓存毫秒", 41},
		{"清房任务毫秒", 43},
		{"宠物清房毫秒", 47},
	}
	fields := []any{"对象", uint32(7001)}
	for _, field := range timingFields {
		fields = append(fields, field.key, field.value)
	}
	fields = append(fields,
		"conn_id", "PRIVATE_CONN",
		"channel_id", 121,
		"account_id", "PRIVATE_ACCOUNT",
		"body", "PRIVATE_BODY",
		"error", errors.New("PRIVATE_ERROR"),
	)

	service.logGameEvent(&gameSession{connID: "PRIVATE_SESSION_CONN", accountID: "PRIVATE_ACCOUNT"}, dungeonDeathTimingEventKind, fields...)
	events := dungeonDeathTimingDiagnosticEvents(t, &logs)
	if len(events) != 1 {
		t.Fatalf("diagnostic events=%v, want exactly one", events)
	}
	event := events[0]
	if event["kind"] != dungeonDeathTimingEventKind || event["对象"] != float64(7001) {
		t.Fatalf("diagnostic lost its fixed identity: %v", event)
	}
	for _, field := range timingFields {
		if event[field.key] != float64(field.value) {
			t.Fatalf("%s=%v, want %d", field.key, event[field.key], field.value)
		}
	}
	for _, key := range []string{"conn_id", "channel_id", "account_id", "body", "error"} {
		if _, found := event[key]; found {
			t.Fatalf("private or session field %q escaped: %v", key, event)
		}
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("private value reached the ordinary log")
	}
	service.logGameEvent(nil, dungeonDeathTimingEventKind, fields...)
	if got := len(dungeonDeathTimingDiagnosticEvents(t, &logs)); got != 1 {
		t.Fatalf("rapid repeated slow death produced %d diagnostics", got)
	}
}

func TestDungeonDeathTimingDiagnosticRejectsOtherEventsWithoutPacketLog(t *testing.T) {
	var logs bytes.Buffer
	service := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), performance: newBridgePerformanceMetrics(metrics.New())}
	if service.packetLog != nil {
		t.Fatal("test requires raw packet logging to be disabled")
	}

	service.logGameEvent(nil, "怪物死亡处理相邻事件",
		"对象", uint32(7002),
		"总毫秒", int64(127),
		"account_id", "PRIVATE_ACCOUNT",
	)
	if events := dungeonDeathTimingDiagnosticEvents(t, &logs); len(events) != 0 {
		t.Fatalf("unrelated event produced death timing diagnostics: %v", events)
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("private value reached the ordinary log")
	}
	service.logGameEvent(nil, dungeonDeathTimingEventKind, "总毫秒", int64(150))
	if got := len(dungeonDeathTimingDiagnosticEvents(t, &logs)); got != 1 {
		t.Fatal("unrelated event consumed the slow-sample budget")
	}
}

func TestDungeonDeathTimingDiagnosticUsesOriginalSlowThreshold(t *testing.T) {
	var logs bytes.Buffer
	service := &Service{logger: slog.New(slog.NewJSONHandler(&logs, nil)), performance: newBridgePerformanceMetrics(metrics.New())}
	fast := newCurrentDungeonDeathTiming()
	fast.finish(service, nil, 1)
	if len(dungeonDeathTimingDiagnosticEvents(t, &logs)) != 0 {
		t.Fatal("fast death emitted a timing diagnostic")
	}
	started := time.Now().Add(-200*time.Millisecond)
	slow := currentDungeonDeathTiming{started: started, last: started}
	slow.finish(service, nil, 2)
	events := dungeonDeathTimingDiagnosticEvents(t, &logs)
	if len(events) != 1 || events[0]["总毫秒"].(float64) < 200 || events[0]["对象"] != float64(2) {
		t.Fatalf("original timing finish lost its slow sample: %v", events)
	}
}
