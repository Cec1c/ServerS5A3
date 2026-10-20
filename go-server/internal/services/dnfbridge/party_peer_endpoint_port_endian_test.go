package dnfbridge

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件钉住 CS op2 `body[9:11]` 端口字节序的**当前结论**：端口是**大端**，
// 且默认档就是大端（`DNFBRIDGE_PARTY_PEER_PORT_ENDIAN` 未设置 / 写 `be`）；
// 写 `le` 回到 2026-09-25 上一轮 M0 的小端读数（一键回退）。
//
// 决定性样本（厂商 9.16 一键端 `D:\DNF90-test\server\runtime\logs\packet_log.txt`，
// **同一场组队、同一秒**的"原始 CS op2 字节 ↔ 厂商自己的中继源端口记录"配对）：
//   - `:2524` body 里 `13 C7`（conn game-000059 / char_id=1）
//     ↔ `:3368` `kind=party-udp-relay-payload … source=192.168.0.101:5063`
//     ⇒ 大端 5063 ✓ / 小端 50963 ✗；
//   - `:2908` body 里 `F4 65`（conn game-000061 / char_id=2）
//     ↔ `:3357` `… source=192.168.0.101:62565`
//     ⇒ 大端 62565 ✓ / 小端 26100 ✗。
//
// 同一结构里的 MTU（`C0 05 00 00`）仍是小端 1472 ⇒ **混合字节序**（上一轮"同结构不混用
// 字节序"的推断已经被这一条推翻）。
//
// 其余样本来自本工作区里真实的包日志（用于把两种读数逐个钉死，不是结论依据）：
//   - `f8 cb`：`local-login-test\Server\_bak-20260924-autorepair\packet_log.txt:246`；
//   - `50 84` / `50 04` / `58 04`：`Temp\crash-mail-20260925\packet_log.*.txt`；
//   - `80 1f`：`YKCN最爱的版本\DNFLauncher_V1.3.9\ServerS4A21-本地部署-DNFLauncher直连\packet_log.txt:52`。
func TestPartyPeerPortEndianReadingsForRealCapturedSamples(t *testing.T) {
	cases := []struct {
		name   string
		raw    [2]byte
		little uint16
		big    uint16
	}{
		{"vendor_paired_13c7", [2]byte{0x13, 0xC7}, 50963, 5063},
		{"vendor_paired_f465", [2]byte{0xF4, 0x65}, 26100, 62565},
		{"m0_paired_capture_f8cb", [2]byte{0xF8, 0xCB}, 52216, 63691},
		{"live_capture_5084", [2]byte{0x50, 0x84}, 33872, 20612},
		{"live_capture_5004", [2]byte{0x50, 0x04}, 1104, 20484},
		{"live_capture_5804", [2]byte{0x58, 0x04}, 1112, 22532},
		{"live_capture_50c4", [2]byte{0x50, 0xC4}, 50256, 20676},
		{"a21_local_capture_801f", [2]byte{0x80, 0x1F}, 8064, 32799},
	}
	for _, tc := range cases {
		if got := partyPeerPortEndianLittle.decode(tc.raw); got != tc.little {
			t.Fatalf("%s: little endian reading = %d want %d", tc.name, got, tc.little)
		}
		if got := partyPeerPortEndianBig.decode(tc.raw); got != tc.big {
			t.Fatalf("%s: big endian reading = %d want %d", tc.name, got, tc.big)
		}
	}
}

// 默认档 = 大端（厂商实测配对）；只有显式 `le` 才回到上一轮的小端读数（一键回退）。
func TestPartyPeerPortEndianDefaultsToBigEndian(t *testing.T) {
	if mode := readPartyPeerPortEndianMode(""); mode != partyPeerPortEndianBig {
		t.Fatalf("empty env mode = %v want %v", mode, partyPeerPortEndianBig)
	}
	for _, value := range []string{"be", "BE", " be ", "big", "network"} {
		if mode := readPartyPeerPortEndianMode(value); mode != partyPeerPortEndianBig {
			t.Fatalf("env %q mode = %v want %v", value, mode, partyPeerPortEndianBig)
		}
	}
	for _, value := range []string{"le", "LE", " le "} {
		if mode := readPartyPeerPortEndianMode(value); mode != partyPeerPortEndianLittle {
			t.Fatalf("env %q mode = %v want %v", value, mode, partyPeerPortEndianLittle)
		}
	}
	if mode := readPartyPeerPortEndianMode("le"); mode.String() != partyPeerPortEndianValueLittle {
		t.Fatalf("le mode string = %q", mode.String())
	}
	if mode := readPartyPeerPortEndianMode(""); mode.String() != partyPeerPortEndianValueBig {
		t.Fatalf("default mode string = %q", mode.String())
	}
	// 零值 options（测试/探针路径）必须等于默认档 = 大端。
	service := &Service{}
	if mode := service.partyPeerPortEndian(); mode != partyPeerPortEndianBig {
		t.Fatalf("zero-value service mode = %v want %v", mode, partyPeerPortEndianBig)
	}
	var probe *Service
	if mode := probe.partyPeerPortEndian(); mode != partyPeerPortEndianBig {
		t.Fatalf("nil service mode = %v want %v", mode, partyPeerPortEndianBig)
	}
}

// `le` 档必须**逐字节**还原上一轮 M0 的小端读数（回退钉子）；默认档按大端读。
// 夹具用厂商实测的那条字节（`13 C7`）。
func TestCaptureCurrentPartyPeerEndpointRegistrationHonoursEndianSwitch(t *testing.T) {
	body := buildCapturedPeerEndpointRegistrationBody(t, 0x13C7)
	big, err := parseCurrentPartyPeerEndpointRegistration(body, partyPeerPortEndianBig)
	if err != nil {
		t.Fatal(err)
	}
	if big.Port != 5063 {
		t.Fatalf("big endian port = %d want 5063 (vendor capture pairing)", big.Port)
	}
	if big.PortRaw != [2]byte{0x13, 0xC7} {
		t.Fatalf("raw port bytes = % X want 13 C7", big.PortRaw)
	}
	if big.MTU != 1472 {
		t.Fatalf("mtu = %d want 1472 (little endian in both modes)", big.MTU)
	}
	if big.CharacterAttribute != 5 {
		t.Fatalf("character attribute = %d want 5", big.CharacterAttribute)
	}
	if big.InnerIPv4 != [4]byte{192, 168, 0, 101} || big.OuterIPv4 != [4]byte{192, 168, 0, 101} {
		t.Fatalf("inner/outer = %v/%v want 192.168.0.101 twice", big.InnerIPv4, big.OuterIPv4)
	}
	little, err := parseCurrentPartyPeerEndpointRegistration(body, partyPeerPortEndianLittle)
	if err != nil {
		t.Fatal(err)
	}
	if little.Port != 50963 {
		t.Fatalf("little endian port = %d want 50963 (previous M0 reading)", little.Port)
	}
	if little.PortRaw != big.PortRaw || little.MTU != big.MTU {
		t.Fatalf("raw port/mtu must not depend on the mode: % X/%d vs % X/%d",
			little.PortRaw, little.MTU, big.PortRaw, big.MTU)
	}
}

// M2 的触发判据：**只有端口真的变了**才算变化（含 0 → 非 0 的首次上报），
// 同一端口重复上报、以及全零的 31 字节重连探针都不算。
func TestCaptureCurrentPartyPeerEndpointRegistrationReportsOnlyPortChanges(t *testing.T) {
	service := &Service{}
	session := &gameSession{connID: "peer-endpoint-change", selectedCharacterID: 7}

	if changed := service.captureCurrentPartyPeerEndpointRegistration(session, buildCapturedPeerEndpointRegistrationBody(t, 20484)); !changed {
		t.Fatal("first non-zero report must count as a change (0 -> port)")
	}
	if got := currentPartyPeerPort(session); got != 20484 {
		t.Fatalf("stored port = %d want 20484", got)
	}
	if changed := service.captureCurrentPartyPeerEndpointRegistration(session, buildCapturedPeerEndpointRegistrationBody(t, 20484)); changed {
		t.Fatal("repeating the same port must not count as a change")
	}
	if changed := service.captureCurrentPartyPeerEndpointRegistration(session, make([]byte, currentPartyPeerRegistrationBodySize)); changed {
		t.Fatal("a zero-filled 31-byte reconnect probe must not count as a change")
	}
	if got := currentPartyPeerPort(session); got != 20484 {
		t.Fatalf("zero-port probe overwrote the stored port: %d want 20484", got)
	}
	if changed := service.captureCurrentPartyPeerEndpointRegistration(session, buildCapturedPeerEndpointRegistrationBody(t, 52216)); !changed {
		t.Fatal("a different non-zero port must count as a change")
	}
	if got := currentPartyPeerPort(session); got != 52216 {
		t.Fatalf("stored port = %d want 52216", got)
	}
}

// buildCapturedPeerEndpointRegistrationBody 造一份与真实抓包**逐字段同形**的 31B CS op2：
// char_attr=5、inner=outer=192.168.0.101、端口**大端**（厂商实测配对；见文件头）、
// MTU=1472（小端）、token 长度 12 + 机器令牌。
func buildCapturedPeerEndpointRegistrationBody(t *testing.T, port uint16) []byte {
	t.Helper()
	body := make([]byte, currentPartyPeerRegistrationBodySize)
	body[0] = 5
	copy(body[1:5], []byte{192, 168, 0, 101})
	copy(body[5:9], []byte{192, 168, 0, 101})
	binary.BigEndian.PutUint16(body[9:11], port)
	binary.LittleEndian.PutUint32(body[11:15], 1472)
	binary.LittleEndian.PutUint32(body[15:19], 12)
	copy(body[19:], []byte("00ff211ef879"))
	return body
}

// newPeerEndpointTestLogger 把包日志接进临时文件：下次实测判读串必须在测试里也有钉子，
// 否则"日志串写错了"只会在线上才发现。
func newPeerEndpointTestLogger(t *testing.T) (*packetLogger, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return logger, logPath
}

// readPeerEndpointTestLog 关闭日志器并读回全文。
func readPeerEndpointTestLog(t *testing.T, logger *packetLogger, logPath string) string {
	t.Helper()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// `game-party-peer-endpoint-registered` 必须把**两种读数**都打出来：
// 下一次实测只看这一行就能判断端口字节序假设对不对，不必回头翻 hex。
func TestPeerEndpointRegisteredLogCarriesBothEndianReadings(t *testing.T) {
	// 两段各用一个日志器：`readPeerEndpointTestLog` 会 Close（与既有测试的写法一致），
	// 关闭后的日志器不再接受写入，所以不能在同一份日志里比两个档位。
	// 夹具用厂商实测的那两个字节（`13 C7` ⇒ 大端 5063）。
	bigLog := func() string {
		logger, logPath := newPeerEndpointTestLogger(t)
		service := &Service{packetLog: logger}
		session := &gameSession{connID: "peer-endpoint-log", selectedCharacterID: 7}
		service.captureCurrentPartyPeerEndpointRegistration(session, buildCapturedPeerEndpointRegistrationBody(t, 0x13C7))
		return readPeerEndpointTestLog(t, logger, logPath)
	}
	logged := bigLog()
	for _, want := range []string{
		"game-party-peer-endpoint-registered",
		"udp_port=5063",
		"udp_port_le=50963",
		"udp_port_be=5063",
		"udp_port_raw=13c7",
		"port_endian=be",
		"port_changed=true",
		"mtu=1472",
		"advertised_mtu=1472",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("registered log is missing %q:\n%s", want, logged)
		}
	}

	// 回退档必须在日志里一眼可见：`le` 档下同一份字节读成 50963（上一轮 M0 的读数），
	// 两种读数仍然都打出来（下一次实测据此判读）。
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{packetLog: logger, options: options{partyPeerPortEndian: partyPeerPortEndianLittle}}
	session := &gameSession{connID: "peer-endpoint-log-le", selectedCharacterID: 7}
	service.captureCurrentPartyPeerEndpointRegistration(session, buildCapturedPeerEndpointRegistrationBody(t, 0x13C7))
	logged = readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logged, "port_endian=le") || !strings.Contains(logged, "udp_port=50963") {
		t.Fatalf("little-endian mode log is missing the rollback marker:\n%s", logged)
	}
	if got := currentPartyPeerPort(session); got != 50963 {
		t.Fatalf("little-endian stored port = %d want 50963 (previous M0 reading)", got)
	}
}
