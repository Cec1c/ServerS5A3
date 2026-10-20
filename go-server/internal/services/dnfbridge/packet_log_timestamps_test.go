package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件钉死 A2-1（包日志时间戳）的可证伪行为：
//   - 默认（未设置/空/未知）= on：**行尾**追加 ` t=<RFC3339Nano>`，行内既有字段
//     顺序一字不动（先断言"去掉时间戳后与改动前逐字节相同"，再断言时间戳存在）。
//   - off / legacy / 0 / false / no / disabled / none = 逐字节今天的行。
const packetLogTodaysLogLine = "RECV kind=game conn_id=game-000006 pkt_seq=7 type=34 len=3 hex=010203\n"

const packetLogTodaysEventLine = "EVENT kind=game-upper-finish-quest-blocked conn_id=game-000006 channel_id=121 quest_id=5146 reason=pvf_db_atomic_finish_rejected\n"

func writePacketLogFixture(t *testing.T) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "packet-log-timestamps.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logger.Log("RECV", "game", []byte{1, 2, 3},
		"conn_id", "game-000006", "pkt_seq", 7, "type", 34)
	logger.Event("game-upper-finish-quest-blocked",
		"conn_id", "game-000006", "channel_id", 121, "quest_id", 5146,
		"reason", "pvf_db_atomic_finish_rejected")
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged)
}

func packetLogFixtureLines(t *testing.T, text string) []string {
	t.Helper()
	lines := strings.Split(text, "\n")
	if len(lines) < 4 {
		t.Fatalf("packet log fixture lines=%d:\n%s", len(lines), text)
	}
	if !strings.HasPrefix(lines[0], "=== LongHeng Go dnfbridge packet log started ") || !strings.HasSuffix(lines[0], " ===") {
		t.Fatalf("packet log header changed shape: %q", lines[0])
	}
	return lines
}

// off 档必须与改动前逐字节相同（含行尾换行、字段顺序、无任何追加字段）。
func TestPacketLogTimestampsOffIsByteIdenticalToToday(t *testing.T) {
	for _, raw := range []string{"off", " legacy ", "0", "false", "no", "disabled", "none", "OFF"} {
		t.Run("switch="+raw, func(t *testing.T) {
			t.Setenv(packetLogTimestampsEnv, raw)
			lines := packetLogFixtureLines(t, writePacketLogFixture(t))
			if lines[1]+"\n" != packetLogTodaysLogLine {
				t.Fatalf("off tier log line=%q want=%q", lines[1]+"\n", packetLogTodaysLogLine)
			}
			if lines[2]+"\n" != packetLogTodaysEventLine {
				t.Fatalf("off tier event line=%q want=%q", lines[2]+"\n", packetLogTodaysEventLine)
			}
			if strings.Contains(lines[1], " t=") || strings.Contains(lines[2], " t=") {
				t.Fatalf("off tier still appended a timestamp:\n%s\n%s", lines[1], lines[2])
			}
		})
	}
}

// 默认档（未设置/空/未知）只追加行尾时间戳：去掉它必须逐字节回到今天的行。
func TestPacketLogTimestampsDefaultOnAppendsTrailingFieldOnly(t *testing.T) {
	for _, raw := range []string{"", "on", "banana"} {
		t.Run("switch="+raw, func(t *testing.T) {
			t.Setenv(packetLogTimestampsEnv, raw)
			before := time.Now().UTC().Add(-time.Minute)
			lines := packetLogFixtureLines(t, writePacketLogFixture(t))
			after := time.Now().UTC().Add(time.Minute)

			strippedLog, stamp := stripPacketLogTimestamp(t, lines[1])
			if strippedLog != strings.TrimSuffix(packetLogTodaysLogLine, "\n") {
				t.Fatalf("on tier changed the existing field order:\n got=%q\nwant=%q", strippedLog, strings.TrimSuffix(packetLogTodaysLogLine, "\n"))
			}
			strippedEvent, eventStamp := stripPacketLogTimestamp(t, lines[2])
			if strippedEvent != strings.TrimSuffix(packetLogTodaysEventLine, "\n") {
				t.Fatalf("on tier changed the existing event field order:\n got=%q\nwant=%q", strippedEvent, strings.TrimSuffix(packetLogTodaysEventLine, "\n"))
			}
			for name, value := range map[string]string{"log": stamp, "event": eventStamp} {
				parsed, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					t.Fatalf("%s timestamp %q is not RFC3339Nano: %v", name, value, err)
				}
				if parsed.Before(before) || parsed.After(after) {
					t.Fatalf("%s timestamp %s is outside the write window", name, parsed)
				}
			}
		})
	}
}

func TestPacketLogTimestampsSwitchPolicy(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", true}, {"on", true}, {"banana", true}, {"1", true},
		{"off", false}, {"OFF", false}, {"legacy", false}, {"0", false},
		{"false", false}, {"no", false}, {"disabled", false}, {"none", false},
	}
	for _, testCase := range cases {
		t.Setenv(packetLogTimestampsEnv, testCase.raw)
		if got := packetLogTimestampsEnabled(); got != testCase.want {
			t.Fatalf("switch=%q enabled=%t want=%t", testCase.raw, got, testCase.want)
		}
	}
}

// 行首字段顺序不变的硬钉：时间戳只能出现在行尾，`len=`/`hex=` 必须仍在旧位置。
func TestPacketLogTimestampsKeepsHexAndLenFieldsInPlace(t *testing.T) {
	t.Setenv(packetLogTimestampsEnv, "")
	lines := packetLogFixtureLines(t, writePacketLogFixture(t))
	line := lines[1]
	if !strings.HasPrefix(line, "RECV kind=game conn_id=game-000006 pkt_seq=7 type=34 len=3 hex=010203") {
		t.Fatalf("timestamped line lost the original field prefix: %q", line)
	}
	index := strings.Index(line, " t=")
	if index < 0 || strings.Count(line, " t=") != 1 {
		t.Fatalf("timestamp field is not exactly one field: %q", line)
	}
	if strings.ContainsAny(line[index+len(" t="):], " \t") {
		t.Fatalf("timestamp is not the last field on the line: %q", line)
	}
}

func stripPacketLogTimestamp(t *testing.T, line string) (string, string) {
	t.Helper()
	index := strings.Index(line, " t=")
	if index < 0 {
		t.Fatalf("line has no trailing t= timestamp: %q", line)
	}
	return line[:index], line[index+len(" t="):]
}

// 文件头分隔行必须**不带**时间戳：它自己就写着启动时间，且 `===` 收尾是既有锚点。
func TestPacketLogTimestampsLeaveTheHeaderDelimiterAlone(t *testing.T) {
	t.Setenv(packetLogTimestampsEnv, "")
	lines := packetLogFixtureLines(t, writePacketLogFixture(t))
	if strings.Contains(lines[0], " t=") {
		t.Fatalf("header delimiter was stamped: %q", lines[0])
	}
	if !strings.HasSuffix(lines[0], " ===") {
		t.Fatalf("header delimiter lost its === anchor: %q", lines[0])
	}
}
