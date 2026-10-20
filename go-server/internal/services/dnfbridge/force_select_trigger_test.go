package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 钉住"模拟客户端打开副本选择器"测试触发器（force_select_trigger.go）的边界：
// 默认 off（零副作用）、命令解析、命令文件只消费一次、以及"只对在线且权威的角色生效"。
func TestForceSelectTriggerDisabledByDefault(t *testing.T) {
	for _, value := range []string{"", "off", "0", "true", "yes", "1"} {
		t.Setenv(forceSelectEnv, value)
		if forceSelectEnabled() {
			t.Fatalf("value %q must not enable the test-only selector trigger", value)
		}
	}
	t.Setenv(forceSelectEnv, "on")
	if !forceSelectEnabled() {
		t.Fatal("an explicit on must enable the test-only selector trigger")
	}
}

func TestParseForceSelectCommand(t *testing.T) {
	if id, err := parseForceSelectCommand("17"); err != nil || id != 17 {
		t.Fatalf("id=%d err=%v, want 17", id, err)
	}
	if id, err := parseForceSelectCommand("  29  extra"); err != nil || id != 29 {
		t.Fatalf("id=%d err=%v, want 29 (extra fields ignored)", id, err)
	}
	if _, err := parseForceSelectCommand("abc"); err == nil {
		t.Fatal("a non-numeric character id must be rejected")
	}
	if _, err := parseForceSelectCommand("   "); err == nil {
		t.Fatal("an empty command must be rejected")
	}
}

func TestForceSelectRejectsOfflineCharacterWithoutSending(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	buffer := session.conn.(*bufferConn)
	buffer.write.Reset()

	ok, partyID, leaderID, isLeader, reason := service.forceEnterSelectDungeon(4242)
	if ok || reason != forceSelectReasonCharacterNotOnline || partyID != 0 || leaderID != 0 || isLeader {
		t.Fatalf("an offline character must be rejected: ok=%t reason=%s party=%d leader=%d is_leader=%t",
			ok, reason, partyID, leaderID, isLeader)
	}
	if buffer.write.Len() != 0 {
		t.Fatal("a rejected trigger must not send a single byte")
	}
}

func TestForceSelectPollerConsumesCommandFileOnce(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "force_select.cmd")
	resultPath := filepath.Join(dir, "force_select.result")
	if err := os.WriteFile(commandPath, []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.pollForceSelectCommand(commandPath, resultPath)

	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), forceSelectReasonCharacterNotOnline) {
		t.Fatalf("result = %q, want the offline rejection line", strings.TrimSpace(string(raw)))
	}
	if _, err := os.Stat(commandPath); !os.IsNotExist(err) {
		t.Fatal("the command file must be consumed (each trigger runs exactly once)")
	}
	// 再 poll 一次：文件已删且内容被记忆 ⇒ 结果文件不被重写。
	before, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	service.pollForceSelectCommand(commandPath, resultPath)
	after, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("a second poll must be a no-op, result changed %q -> %q", before, after)
	}
}

// 开关默认 off 时启动函数必须零副作用（不起 goroutine、不读文件）。
func TestForceSelectTriggerStartIsNoOpWhenOff(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "force_select.cmd")
	if err := os.WriteFile(commandPath, []byte("17\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(forceSelectEnv, "off")
	t.Setenv(forceSelectCommandPathEnv, commandPath)
	service.startForceSelectTrigger(t.Context())
	if _, err := os.Stat(commandPath); err != nil {
		t.Fatalf("the off switch must not touch the command file: %v", err)
	}
}
