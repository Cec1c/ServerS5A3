package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/platform/config"
)

// 现场（2026-09-26）：客户端每次登录弹出 `pvp02` 竞技场匹配房，参与者徽标
// 渲染服务端唯一的合成角色名 `dnf_46688`。合成名的产出点唯一
// （game_select_resolve.go:119-124），但**发包点**读代码定不下来，因此这里
// 用开关把"下一次登录"变成可判读实验。本文件只钉三件事：
//
//  1. 默认（零值 / 未设 DNFBRIDGE_FALLBACK_NAME_AUDIT）下：**下行字节不变、
//     日志不变**——审计是纯附加，不能成为新的变量；
//  2. 打开后：命中 `dnf_<角色ID>` 的内层明文包各记一条
//     `game-fallback-name-in-outbound-packet`，带 outbound_msg_id/偏移/名字；
//  3. 该事件不受 suppressPacketLogEvent 的 msg_id 过滤影响（1276/1516/1518
//     这三个号会被整条丢掉，字段名因此不叫 msg_id）。

func auditTestUpperPacket(msgID uint16, body []byte) []byte {
	packet := make([]byte, dnfproto.GameServerUpperHeaderSize16+len(body))
	packet[0] = 0
	binary.LittleEndian.PutUint16(packet[1:3], msgID)
	binary.LittleEndian.PutUint32(packet[3:7], uint32(len(packet)))
	copy(packet[dnfproto.GameServerUpperHeaderSize16:], body)
	return packet
}

func newFallbackNameAuditFixture(t *testing.T, enabled bool, logName string) (*Service, *gameSession, *packetLogger, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), logName+".log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{options: options{fallbackNameAudit: enabled}}
	service.packetLog = logger
	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "game-000001",
		channel:             channelcatalog.Channel{ID: 253},
		selectedCharacterID: 1,
	}
	return service, session, logger, logPath
}

func readFallbackNameAuditLog(t *testing.T, logger *packetLogger, logPath string) string {
	t.Helper()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged)
}

// ① 默认档：一个字节、一行日志都不多。
func TestFallbackNameAuditDefaultOffKeepsWireAndLogSilent(t *testing.T) {
	service, session, logger, logPath := newFallbackNameAuditFixture(t, false, "audit-off")
	packet := auditTestUpperPacket(411, []byte("row\x00dnf_46688\x00"))
	if err := service.writeGameUpperPacketLocked(session, packet); err != nil {
		t.Fatal(err)
	}
	written := append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
	if !bytes.Equal(written, packet) {
		t.Fatalf("default write changed bytes: %x", written)
	}
	text := readFallbackNameAuditLog(t, logger, logPath)
	if strings.Contains(text, "game-fallback-name-in-outbound-packet") {
		t.Fatalf("default (off) log recorded an audit event: %q", text)
	}
}

// ② 打开后：命中即记，且消息号/偏移/名字可判读。
func TestFallbackNameAuditLogsMsgIDOffsetAndName(t *testing.T) {
	service, session, logger, logPath := newFallbackNameAuditFixture(t, true, "audit-on")
	const msgID = uint16(1021)
	body := append([]byte{0x02, 0x00, 0x01, 0x00}, []byte("dnf_46688")...)
	body = append(body, 0x00, 0x7f)
	packet := auditTestUpperPacket(msgID, body)
	if err := service.writeGameUpperPacketLocked(session, packet); err != nil {
		t.Fatal(err)
	}
	written := append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
	if !bytes.Equal(written, packet) {
		t.Fatalf("audit changed the wire bytes: %x want %x", written, packet)
	}
	text := readFallbackNameAuditLog(t, logger, logPath)
	for _, want := range []string{
		"EVENT kind=game-fallback-name-in-outbound-packet",
		"layer=game_upper_inner_pre_dproto",
		"outbound_msg_id=1021",
		"outbound_msg_id_source=bytes_1_3_little_endian",
		"name=dnf_46688",
		"name_char_id=46688",
		"selected_char_id=1",
		"conn_id=game-000001",
		"target_channel_id=253",
		"packet_len=",
		"switch=DNFBRIDGE_FALLBACK_NAME_AUDIT",
		"reason=synthetic_character_name_would_be_sent_to_client",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("audit log missing %q: %q", want, text)
		}
	}
	// 上下文窗口必须落在包体里，且覆盖名字本身。
	offsetText := fallbackNameAuditFieldValue(text, "marker_offset")
	if offsetText != "20" {
		t.Fatalf("marker_offset=%s want 20 (16-byte upper header + 4 body bytes): %q", offsetText, text)
	}
}

// ③ 只有"dnf_ + 非零十进制角色 ID"才算合成名：玩家真名 / 空 ID 不记。
func TestFallbackNameAuditIgnoresNonSyntheticNames(t *testing.T) {
	service, session, logger, logPath := newFallbackNameAuditFixture(t, true, "audit-shapes")
	for _, body := range [][]byte{
		[]byte("dnf_alice\x00"),
		[]byte("dnf_\x00"),
		[]byte("dnf_0\x00"),
		[]byte("dnf_655360\x00"),
		[]byte("t3\x00"),
	} {
		if err := service.writeGameUpperPacketLocked(session, auditTestUpperPacket(441, body)); err != nil {
			t.Fatal(err)
		}
	}
	text := readFallbackNameAuditLog(t, logger, logPath)
	if strings.Contains(text, "game-fallback-name-in-outbound-packet") {
		t.Fatalf("non-synthetic name shapes were audited: %q", text)
	}
}

// ④ 心跳号（1276）在 suppressPacketLogEvent 里是被整条丢掉的；审计行必须在
// 任何消息号下都能落盘，否则"日志里搜不到"又会变成构造性的假证据。
func TestFallbackNameAuditSurvivesEventLogFilter(t *testing.T) {
	service, session, logger, logPath := newFallbackNameAuditFixture(t, true, "audit-heartbeat")
	packet := auditTestUpperPacket(1276, []byte("dnf_46688"))
	if err := service.writeGameUpperPacketLocked(session, packet); err != nil {
		t.Fatal(err)
	}
	text := readFallbackNameAuditLog(t, logger, logPath)
	if !strings.Contains(text, "game-fallback-name-in-outbound-packet") ||
		!strings.Contains(text, "outbound_msg_id=1276") {
		t.Fatalf("heartbeat audit event was filtered out: %q", text)
	}
	if strings.Contains(text, " msg_id=1276") {
		t.Fatalf("audit line carries a msg_id field and can be filtered again: %q", text)
	}
}

// ⑤ legacy/固定帧写入路径同样被审计（不同 layer 标签），并且字节不变。
func TestFallbackNameAuditCoversRawFrameWriter(t *testing.T) {
	service, session, logger, logPath := newFallbackNameAuditFixture(t, true, "audit-raw")
	packet := auditTestUpperPacket(1021, []byte("dnf_46688"))
	if err := service.writeGameRawPacketLocked(session, packet); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(session.conn.(*bufferConn).write.Bytes(), packet) {
		t.Fatal("raw write changed bytes")
	}
	text := readFallbackNameAuditLog(t, logger, logPath)
	if !strings.Contains(text, "layer=game_raw_frame") {
		t.Fatalf("raw frame layer not audited: %q", text)
	}
}

// ⑥ 开关本身：默认 off（零值 = 线上一字不变），on/0 可现场切换，不需要重新编译。
func TestFallbackNameAuditOptionDefaultsOffAndFollowsEnv(t *testing.T) {
	t.Setenv(fallbackNameAuditEnv, "")
	if got := optionsFromConfig(config.ServiceConfig{}).fallbackNameAudit; got {
		t.Fatalf("default %s empty enabled the audit", fallbackNameAuditEnv)
	}
	t.Setenv(fallbackNameAuditEnv, "on")
	if got := optionsFromConfig(config.ServiceConfig{}).fallbackNameAudit; !got {
		t.Fatalf("%s=on did not enable the audit", fallbackNameAuditEnv)
	}
	t.Setenv(fallbackNameAuditEnv, "0")
	if got := optionsFromConfig(config.ServiceConfig{}).fallbackNameAudit; got {
		t.Fatalf("%s=0 (rollback) did not disable the audit", fallbackNameAuditEnv)
	}
}

func fallbackNameAuditFieldValue(text string, field string) string {
	for _, token := range strings.Fields(text) {
		if strings.HasPrefix(token, field+"=") {
			return strings.TrimPrefix(token, field+"=")
		}
	}
	return ""
}
