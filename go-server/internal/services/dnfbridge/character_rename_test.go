package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// ---------------------------------------------------------------------------
// 阶段 1：op135（ENUM_CMDPACKET_CHANGE_CHARAC_NAME）具名判读。
//
// 现场帧来自 packet_log.txt:60954（两次请求体逐字节相同，见
// _dist/rename-delete-diagnosis.md §2.2）。本组用例钉死三件事：
//
//	1. 27 字节体能被**逐字段**解析出来（偏移不是猜的）；
//	2. 默认档打出具名事件，且**一个下行字节都不发**（S2C 形态未取证）；
//	3. `DNFBRIDGE_CHARACTER_RENAME=legacy` 精确回到今天那条路（同样零回包）。
// ---------------------------------------------------------------------------

// characterRenameLiveCapturePacket 是现场 40 字节 op135 原始帧（13 字节 legacy 头 +
// 27 字节体）。
const characterRenameLiveCapturePacket = "018700280000006b1f03f71400020b0000007465737437385f636f70790600000074657374383801"

func characterRenameLiveRaw(t *testing.T) []byte {
	t.Helper()
	raw, err := hex.DecodeString(characterRenameLiveCapturePacket)
	if err != nil {
		t.Fatalf("现场帧 hex 解不开: %v", err)
	}
	if len(raw) != 40 {
		t.Fatalf("现场帧长度 = %d, want 40", len(raw))
	}
	return raw
}

func TestDecodeCharacterRenameRequestMatchesLiveCapture(t *testing.T) {
	raw := characterRenameLiveRaw(t)
	body := raw[13:] // LegacyGameHeaderSize = 13
	if len(body) != 27 {
		t.Fatalf("体长度 = %d, want 27", len(body))
	}
	request, err := decodeCharacterRenameRequest(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if request.Flag != 0x02 {
		t.Fatalf("flag = %#x, want 0x02", request.Flag)
	}
	if string(request.OldNameRaw) != "test78_copy" {
		t.Fatalf("old name = %q, want test78_copy", request.OldNameRaw)
	}
	if string(request.NewNameRaw) != "test88" {
		t.Fatalf("new name = %q, want test88", request.NewNameRaw)
	}
	if request.Tail != 0x01 {
		t.Fatalf("tail = %#x, want 0x01", request.Tail)
	}
	if got := characterRenameDecodedName(request.OldNameRaw); got != "test78_copy" {
		t.Fatalf("decoded old name = %q", got)
	}
}

func TestDecodeCharacterRenameRequestRejectsMalformedBodies(t *testing.T) {
	live := characterRenameLiveRaw(t)[13:]
	// 每一条都是"偏移漂移/伪造"的一种形状：必须判失败，绝不把错位字节当名字。
	cases := []struct {
		name string
		body []byte
	}{
		{"空体", nil},
		{"只有 flag", live[:1]},
		{"旧名长度字段不全", live[:3]},
		{"旧名长度为零", append([]byte{0x02, 0x00, 0x00, 0x00, 0x00}, live[5:]...)},
		{"旧名长度超出体", append([]byte{0x02, 0xFF, 0x00, 0x00, 0x00}, live[5:]...)},
		{"缺 tail", live[:len(live)-1]},
		{"tail 后多一字节", append(append([]byte(nil), live...), 0x00)},
		{"新名段被截断", live[:20]},
	}
	for _, testCase := range cases {
		if _, err := decodeCharacterRenameRequest(testCase.body); err == nil {
			t.Fatalf("%s: decode 竟然成功（body=%x）", testCase.name, testCase.body)
		}
	}
}

// characterRenameTestService 造一个只带 packetLog 的 Service + 一条假会话
// （与 game_legacy_owner_routes_test.go 同一手法：op135 的新 handler 不碰仓储、
// 不碰网络，所以只需要日志与一个可断言的写缓冲）。
//
// 注意：packetLog 是**带缓冲**的，必须先 Close 再读文件（否则读到空文件），
// 所以这里把 logger 一起交给调用方，读取走 characterRenameReadLog。
func characterRenameTestService(t *testing.T) (*Service, *gameSession, string, *packetLogger) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatalf("open packet logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	service := &Service{packetLog: logger}
	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "legacy-change-charac-name",
		channel:             channelcatalog.Channel{ID: 252},
		selectedCharacterID: 15,
	}
	return service, session, logPath, logger
}

// characterRenameReadLog 先把缓冲刷进文件再读（顺序与原有用例一致）。
func characterRenameReadLog(t *testing.T, logger *packetLogger, path string) string {
	t.Helper()
	if err := logger.Close(); err != nil {
		t.Fatalf("close packet logger: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read packet log: %v", err)
	}
	return string(data)
}

// 默认档（开关未设）：具名事件 + 零回包。
func TestLegacyChangeCharacNameIsParsedAndLoggedWithoutReply(t *testing.T) {
	service, session, logPath, logger := characterRenameTestService(t)
	raw := characterRenameLiveRaw(t)

	if err := service.handleLegacyGamePacket(session, raw); err != nil {
		t.Fatalf("handleLegacyGamePacket: %v", err)
	}

	// 零回包：客户端等不到应答这件事没被改变（阶段 1 只判读）。
	if got := session.conn.(*bufferConn).write.Bytes(); len(got) != 0 {
		t.Fatalf("op135 判读档居然发了下行字节: %x", got)
	}
	text := characterRenameReadLog(t, logger, logPath)
	for _, marker := range []string{
		"kind=game-change-charac-name-request",
		"type=135",
		"runtime_cmd_name=ENUM_CMDPACKET_CHANGE_CHARAC_NAME",
		"flag=2",
		"old_name=test78_copy",
		"new_name=test88",
		"tail=1",
		"reply=none_s2c_shape_unverified",
		"boundary=phase1_parse_and_log_only_no_write_no_reply",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("op135 具名事件缺 %q\nlog=%s", marker, text)
		}
	}
	if strings.Contains(text, "kind=game-change-charac-name-parse-failed") {
		t.Fatalf("现场帧不该解析失败: %s", text)
	}
}

// legacy 档：精确回到今天那条路（不解析、交回 aligned fallback），同样零回包。
func TestLegacyChangeCharacNameSwitchRestoresSilentFallback(t *testing.T) {
	t.Setenv(characterRenameLegacyEnv, "legacy")
	service, session, logPath, logger := characterRenameTestService(t)

	if err := service.handleLegacyGamePacket(session, characterRenameLiveRaw(t)); err != nil {
		t.Fatalf("handleLegacyGamePacket: %v", err)
	}
	if got := session.conn.(*bufferConn).write.Bytes(); len(got) != 0 {
		t.Fatalf("legacy 档居然发了下行字节: %x", got)
	}
	text := characterRenameReadLog(t, logger, logPath)
	if !strings.Contains(text, "kind=game-change-charac-name-legacy-fallback") {
		t.Fatalf("legacy 档没有具名回退事件: %s", text)
	}
	if strings.Contains(text, "kind=game-change-charac-name-request") {
		t.Fatalf("legacy 档不该解析 op135: %s", text)
	}
}

// 解析失败也必须零回包（并且要说清楚是"没读懂"，不是"没这条 opcode"）。
func TestLegacyChangeCharacNameMalformedBodyLogsParseFailureWithoutReply(t *testing.T) {
	service, session, logPath, logger := characterRenameTestService(t)
	raw := characterRenameLiveRaw(t)
	// 把体截断成 9 字节（模拟偏移漂移），外层长度字段同步改小以通过帧解析。
	body := raw[13:22]
	frame := make([]byte, 13+len(body))
	copy(frame, raw[:13])
	copy(frame[13:], body)
	binary.LittleEndian.PutUint32(frame[3:7], uint32(len(frame)))

	if err := service.handleLegacyGamePacket(session, frame); err != nil {
		t.Fatalf("handleLegacyGamePacket: %v", err)
	}
	if got := session.conn.(*bufferConn).write.Bytes(); len(got) != 0 {
		t.Fatalf("解析失败时也不许发下行字节: %x", got)
	}
	text := characterRenameReadLog(t, logger, logPath)
	if !strings.Contains(text, "kind=game-change-charac-name-parse-failed") {
		t.Fatalf("坏体没有具名解析失败事件: %s", text)
	}
	if !strings.Contains(text, "body_hex=") {
		t.Fatalf("坏体事件应带 body_hex 便于继续取证: %s", text)
	}
}

// 开关解析：只有字面量 legacy 回退，其余（含空、未知值）都是判读档。
func TestCharacterRenameLegacyFromEnvOnlyLiteralLegacy(t *testing.T) {
	for _, value := range []string{"legacy", "LEGACY", " legacy ", "\tLegacy\n"} {
		if !characterRenameLegacyFromEnv(value) {
			t.Fatalf("characterRenameLegacyFromEnv(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", " ", "off", "0", "false", "no", "legacyy", "phase1", "on"} {
		if characterRenameLegacyFromEnv(value) {
			t.Fatalf("characterRenameLegacyFromEnv(%q) = true, want false（只有字面量 legacy 回退）", value)
		}
	}
	// 现场帧的 type 必须真的是这个枚举（防止把别的 opcode 接进来）。
	if uint16(dnfenum.CmdPacketChangeCharacName) != 135 {
		t.Fatalf("CmdPacketChangeCharacName = %d, want 135", uint16(dnfenum.CmdPacketChangeCharacName))
	}
}
