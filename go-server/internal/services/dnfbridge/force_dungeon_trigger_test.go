package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// 钉住"模拟队长选定副本（整队进图）"测试触发器（force_dungeon_trigger.go）的边界：
// 默认 off（零副作用）、命令解析（nonce 被忽略）、命令文件只消费一次、
// 以及"只对在线且权威的角色生效"。
func TestForceDungeonTriggerDisabledByDefault(t *testing.T) {
	for _, value := range []string{"", "off", "0", "true", "yes", "1"} {
		t.Setenv(forceDungeonEnv, value)
		if forceDungeonEnabled() {
			t.Fatalf("value %q must not enable the test-only dungeon trigger", value)
		}
	}
	t.Setenv(forceDungeonEnv, "on")
	if !forceDungeonEnabled() {
		t.Fatal("an explicit on must enable the test-only dungeon trigger")
	}
}

func TestParseForceDungeonCommand(t *testing.T) {
	charID, dungeonID, difficulty, entryOption, err := parseForceDungeonCommand("1 3")
	if err != nil || charID != 1 || dungeonID != 3 || difficulty != 0 || entryOption != 0 {
		t.Fatalf("got char=%d dungeon=%d diff=%d opt=%d err=%v, want 1/3/0/0", charID, dungeonID, difficulty, entryOption, err)
	}
	charID, dungeonID, difficulty, entryOption, err = parseForceDungeonCommand("4 5 2 nonce-p9")
	if err != nil || charID != 4 || dungeonID != 5 || difficulty != 2 || entryOption != 0 {
		t.Fatalf("got char=%d dungeon=%d diff=%d opt=%d err=%v, want 4/5/2/0 (nonce ignored)",
			charID, dungeonID, difficulty, entryOption, err)
	}
	if _, _, _, _, err := parseForceDungeonCommand("1"); err == nil {
		t.Fatal("a single-field command must be rejected")
	}
	if _, _, _, _, err := parseForceDungeonCommand("x 3"); err == nil {
		t.Fatal("a non-numeric character id must be rejected")
	}
	if _, _, _, _, err := parseForceDungeonCommand("1 y"); err == nil {
		t.Fatal("a non-numeric dungeon id must be rejected")
	}
	if _, _, _, _, err := parseForceDungeonCommand("1 3 999"); err == nil {
		t.Fatal("a difficulty outside one byte must be rejected")
	}
	if _, _, _, _, err := parseForceDungeonCommand("1 3 0 70000"); err == nil {
		t.Fatal("an entry option above u16 must be rejected")
	}
	// 非数字的第 4 字段按现场 nonce 约定忽略（既有写法不能被破坏）。
	if _, _, _, opt, err := parseForceDungeonCommand("1 3 0 notanumber"); err != nil || opt != 0 {
		t.Fatalf("a non-numeric 4th field must be ignored as a nonce: opt=%d err=%v", opt, err)
	}
}

// 第 4 个字段（entry_option）是深渊派对那条链路的入口：op16 的 `body[5:7]` 是
// EntryOption(u16 LE)，客户端点「挑战深渊派对」时低字节写 1。不写该字段时必须仍为 0，
// 保证既有用法逐字节不变；写了就必须原样透传，否则**深渊资格判定永远不会被触发**
// （这正是 2026-09-28 我实测到的现象：force 出来的 op16 走完也没有任何
// `game-hell-*` 事件）。
func TestParseForceDungeonCommandEntryOptionCarriesHellFlag(t *testing.T) {
	charID, dungeonID, difficulty, entryOption, err := parseForceDungeonCommand("1 101 0 1")
	if err != nil {
		t.Fatalf("`1 101 0 1` must parse: %v", err)
	}
	if charID != 1 || dungeonID != 101 || difficulty != 0 || entryOption != 1 {
		t.Fatalf("got char=%d dungeon=%d diff=%d opt=%d, want 1/101/0/1", charID, dungeonID, difficulty, entryOption)
	}
	if entryOption == 0 {
		t.Fatal("entry option 1 (hell flag) must survive parsing")
	}
	// 显式 0 与大值（u16 上限）都要能解析；超 u16 必须被拒。
	if _, _, _, opt, err := parseForceDungeonCommand("1 101 0 0"); err != nil || opt != 0 {
		t.Fatalf("entry option 0: opt=%d err=%v", opt, err)
	}
	if _, _, _, opt, err := parseForceDungeonCommand("1 101 0 65535"); err != nil || opt != 65535 {
		t.Fatalf("entry option 65535: opt=%d err=%v", opt, err)
	}
	if _, _, _, _, err := parseForceDungeonCommand("1 101 0 65536"); err == nil {
		t.Fatal("an entry option above u16 must be rejected")
	}
}

// 透传钉子：带 entry_option=1 时，合成的 op16 body 的 body[5:7] 必须是 1（LE），
// 且能被服务端解码器读回 EntryOption=1。没有这颗钉子就无法证明"深渊标志真的写进去了"。
func TestForceDungeonBodyCarriesEntryOptionIntoHellFlag(t *testing.T) {
	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{
		DungeonID:       101,
		Difficulty:      0,
		EntryOption:     1,
		SelectionMode:   0,
		RuntimeState:    0,
		RuntimeToken:    forceDungeonRuntimeToken,
		Reserved:        0,
		PartyState:      0,
		LeaderObjectKey: 0,
		SpecialMode:     0,
	})
	if len(body) < 7 {
		t.Fatalf("body too short: %d", len(body))
	}
	if body[5] != 1 || body[6] != 0 {
		t.Fatalf("body[5:7]=%v want [1 0] (the hell flag the client sends)", body[5:7])
	}
	decoded, err := dungeoncmd.DecodeSelectDungeonRequest(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.EntryOption != 1 {
		t.Fatalf("decoded EntryOption=%d want 1", decoded.EntryOption)
	}
	if decoded.DungeonID != 101 {
		t.Fatalf("decoded DungeonID=%d want 101", decoded.DungeonID)
	}
}

// 给编码器钉一颗钉子：触发器合成的 op16 body 必须能被服务端解码器逐字段读回，
// 形状照抄现场真实客户端日志（`runtime_token=65535`、其余未用字段为 0），不新增任何字节。
func TestForceDungeonBodyRoundTripsThroughDecoder(t *testing.T) {
	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{
		DungeonID:       3,
		Difficulty:      0,
		RuntimeToken:    forceDungeonRuntimeToken,
		LeaderObjectKey: 1,
	})
	if len(body) != dungeoncmd.SelectDungeonRequestSize {
		t.Fatalf("op16 body_len=%d, want %d", len(body), dungeoncmd.SelectDungeonRequestSize)
	}
	decoded, err := dungeoncmd.DecodeSelectDungeonRequest(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.DungeonID != 3 || decoded.Difficulty != 0 || decoded.RuntimeToken != forceDungeonRuntimeToken ||
		decoded.LeaderObjectKey != 1 || decoded.EntryOption != 0 || decoded.SelectionMode != 0 ||
		decoded.RuntimeState != 0 || decoded.Reserved != 0 || decoded.PartyState != 0 || decoded.SpecialMode != 0 {
		t.Fatalf("decoded op16 = %+v, want dungeon 3 / token 0xFFFF / leader key 1 and every other field zero", decoded)
	}
}

func TestForceDungeonRejectsOfflineCharacterWithoutSending(t *testing.T) {
	service, session, _ := newTownMoveTest(t)
	buffer := session.conn.(*bufferConn)
	buffer.write.Reset()

	ok, reason := service.forceDungeonSelect(4242, 3, 0, 0)
	if ok || reason != forceDungeonReasonCharacterNotOnline {
		t.Fatalf("an offline character must be rejected: ok=%t reason=%s", ok, reason)
	}
	if buffer.write.Len() != 0 {
		t.Fatal("a rejected trigger must not send a single byte")
	}
	ok, reason = service.forceDungeonSelect(0, 3, 0, 0)
	if ok || reason != forceDungeonReasonZeroCharacterID {
		t.Fatalf("character 0 must be rejected: ok=%t reason=%s", ok, reason)
	}
	ok, reason = service.forceDungeonSelect(4242, 0, 0, 0)
	if ok || reason != forceDungeonReasonZeroDungeonID {
		t.Fatalf("dungeon 0 must be rejected: ok=%t reason=%s", ok, reason)
	}
	if buffer.write.Len() != 0 {
		t.Fatal("rejected triggers must never write packets")
	}
}

func TestForceDungeonPollerConsumesCommandFileOnce(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "force_dungeon.cmd")
	resultPath := filepath.Join(dir, "force_dungeon.result")
	if err := os.WriteFile(commandPath, []byte("4242 3 nonce-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.pollForceDungeonCommand(commandPath, resultPath)

	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), forceDungeonReasonCharacterNotOnline) {
		t.Fatalf("result = %q, want the offline rejection line", strings.TrimSpace(string(raw)))
	}
	if _, err := os.Stat(commandPath); !os.IsNotExist(err) {
		t.Fatal("the command file must be consumed (each trigger runs exactly once)")
	}
}

func TestForceDungeonTriggerStartIsNoOpWhenOff(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "force_dungeon.cmd")
	if err := os.WriteFile(commandPath, []byte("1 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(forceDungeonEnv, "off")
	t.Setenv(forceDungeonCommandPathEnv, commandPath)
	service.startForceDungeonTrigger(t.Context())
	if _, err := os.Stat(commandPath); err != nil {
		t.Fatalf("the off switch must not touch the command file: %v", err)
	}
}
