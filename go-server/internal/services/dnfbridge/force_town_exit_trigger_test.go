package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 钉住"模拟客户端走出赛丽亚房间（op1425）"测试触发器（force_town_exit_trigger.go）的边界：
// 默认 off（零副作用）、命令解析（多余字段=nonce 被忽略）、命令文件只消费一次、
// 以及"只对在线且权威的角色生效"。
func TestForceTownExitTriggerDisabledByDefault(t *testing.T) {
	for _, value := range []string{"", "off", "0", "true", "yes", "1"} {
		t.Setenv(forceTownExitEnv, value)
		if forceTownExitEnabled() {
			t.Fatalf("value %q must not enable the test-only town-exit trigger", value)
		}
	}
	t.Setenv(forceTownExitEnv, "on")
	if !forceTownExitEnabled() {
		t.Fatal("an explicit on must enable the test-only town-exit trigger")
	}
}

func TestParseForceTownExitCommand(t *testing.T) {
	if id, err := parseForceTownExitCommand("4"); err != nil || id != 4 {
		t.Fatalf("id=%d err=%v, want 4", id, err)
	}
	// 现场约定：每次触发要写不同内容（追加 nonce），多余字段必须被忽略。
	if id, err := parseForceTownExitCommand("1 nonce-p7"); err != nil || id != 1 {
		t.Fatalf("id=%d err=%v, want 1 (nonce ignored)", id, err)
	}
	if _, err := parseForceTownExitCommand("x"); err == nil {
		t.Fatal("a non-numeric character id must be rejected")
	}
	if _, err := parseForceTownExitCommand("  "); err == nil {
		t.Fatal("an empty command must be rejected")
	}
}

func TestForceTownExitRejectsOfflineCharacterWithoutSending(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	buffer := session.conn.(*bufferConn)
	buffer.write.Reset()

	ok, landing, reason := service.forceTownExitSeriaRoom(4242)
	if ok || reason != forceTownExitReasonCharacterNotOnline || landing.CharacterID != 0 {
		t.Fatalf("an offline character must be rejected: ok=%t reason=%s landing=%+v", ok, reason, landing)
	}
	if buffer.write.Len() != 0 {
		t.Fatal("a rejected trigger must not send a single byte")
	}
}

func TestForceTownExitPollerConsumesCommandFileOnce(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "force_town_exit.cmd")
	resultPath := filepath.Join(dir, "force_town_exit.result")
	if err := os.WriteFile(commandPath, []byte("4242 nonce-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.pollForceTownExitCommand(commandPath, resultPath)

	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), forceTownExitReasonCharacterNotOnline) {
		t.Fatalf("result = %q, want the offline rejection line", strings.TrimSpace(string(raw)))
	}
	if _, err := os.Stat(commandPath); !os.IsNotExist(err) {
		t.Fatal("the command file must be consumed (each trigger runs exactly once)")
	}
	before, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	service.pollForceTownExitCommand(commandPath, resultPath)
	after, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("a second poll must be a no-op, result changed %q -> %q", before, after)
	}
}

// 同一内容会被记忆跳过（防删除失败时重复执行）——现场因此必须每次追加 nonce。
func TestForceTownExitPollerSkipsRepeatedContent(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "force_town_exit.cmd")
	resultPath := filepath.Join(dir, "force_town_exit.result")
	if err := os.WriteFile(commandPath, []byte("4242 same"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.pollForceTownExitCommand(commandPath, resultPath)
	// 重写同一内容：必须被去重跳过（结果文件不被重写 ⇒ 时间戳不变）。
	infoBefore, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(commandPath, []byte("4242 same"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.pollForceTownExitCommand(commandPath, resultPath)
	infoAfter, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("identical command content must be skipped by the dedupe memory")
	}
}

func TestForceTownExitTriggerStartIsNoOpWhenOff(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "force_town_exit.cmd")
	if err := os.WriteFile(commandPath, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(forceTownExitEnv, "off")
	t.Setenv(forceTownExitCommandPathEnv, commandPath)
	service.startForceTownExitTrigger(t.Context())
	if _, err := os.Stat(commandPath); err != nil {
		t.Fatalf("the off switch must not touch the command file: %v", err)
	}
}
