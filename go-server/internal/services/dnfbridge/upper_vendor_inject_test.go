package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func anchorUpperPacket(msgID uint16, class byte, body []byte) []byte {
	packet := make([]byte, 16+len(body))
	packet[0] = class
	binary.LittleEndian.PutUint16(packet[1:3], msgID)
	binary.LittleEndian.PutUint32(packet[3:7], uint32(len(packet)))
	copy(packet[16:], body)
	return packet
}

func resetUpperInjectCache() {
	upperInjectOnce = sync.Once{}
	upperInjectParsed = upperInjectTable{}
}

func TestParseUpperInjectTableVariants(t *testing.T) {
	packet183 := "00b700120000005c7c239402000000000001"
	table, err := parseUpperInjectTable(strings.Join([]string{
		"# 注释行",
		"",
		"4 0xb7 0 " + packet183,
		"1 183 0 " + packet183,
		"  2   183   0   " + packet183 + "  ",
	}, "\n"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(table.entries) != 3 {
		t.Fatalf("条目数 = %d, 期望 3", len(table.entries))
	}
	if table.entries[0].anchor != 4 || table.entries[0].msgID != 183 || table.entries[0].class != 0 {
		t.Fatalf("首条解析 = %+v", table.entries[0])
	}
	if !bytes.Equal(table.entries[0].packet, table.entries[2].packet) {
		t.Fatal("同 hex 不同行的字节不一致")
	}
	if table.entries[1].anchor != 1 || table.entries[2].anchor != 2 {
		t.Fatalf("锚点解析错误: %d / %d", table.entries[1].anchor, table.entries[2].anchor)
	}
}

func TestParseUpperInjectTableRejectsBadInput(t *testing.T) {
	packet183 := "00b700120000005c7c239402000000000001"
	cases := []struct {
		name string
		text string
	}{
		{name: "字段不足", text: "4 183 0"},
		{name: "锚点非数字", text: "abc 183 0 " + packet183},
		{name: "msg_id 非数字", text: "4 xyz 0 " + packet183},
		{name: "class 非数字", text: "4 183 zz " + packet183},
		{name: "hex 非法", text: "4 183 0 zz11"},
		{name: "hex 过短", text: "4 183 0 00b7"},
		{name: "头与声明不一致", text: "4 184 0 " + packet183},
		{name: "class 与包头不一致", text: "4 183 7 " + packet183},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := parseUpperInjectTable(testCase.text); err == nil {
				t.Fatalf("非法输入必须报错，否则会把坏包注入到客户端: %q", testCase.text)
			}
		})
	}
}

func TestCurrentUpperInjectTableEmptyByDefault(t *testing.T) {
	resetUpperInjectCache()
	defer resetUpperInjectCache()
	t.Setenv(upperInjectFileEnv, "")
	if !currentUpperInjectTable().Empty() {
		t.Fatal("默认（未设置开关）必须不注入任何包")
	}
}

// TestUpperVendorInjectEmitsBeforeAnchorOncePerSession 锁死注入的两个关键语义：
// ① 厂商包出现在锚点包**之前**（顺序即厂商顺序）；
// ② 每条注入每会话只发一次（锚点包在登录期会被多次发送）。
func TestUpperVendorInjectEmitsBeforeAnchorOncePerSession(t *testing.T) {
	resetUpperInjectCache()
	defer resetUpperInjectCache()

	vendorFirst := anchorUpperPacket(183, 0, []byte{0x00, 0x01})
	vendorSecond := anchorUpperPacket(183, 0, []byte{0x01, 0x01})
	path := t.TempDir() + "/inject.txt"
	body := "# 厂商登录最前段的 op183 x2，挂在锚点 op4 之前\n" +
		"4 183 0 " + hexOf(vendorFirst) + "\n" +
		"4 183 0 " + hexOf(vendorSecond) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写注入表失败: %v", err)
	}
	t.Setenv(upperInjectFileEnv, path)

	service := &Service{}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	session := &gameSession{conn: server, connID: "game-test-inject"}

	anchor := anchorUpperPacket(4, 0, []byte{0xAA})
	// net.Pipe 是无缓冲同步管道：写会阻塞到对端读取，所以写入必须异步发起，
	// 再按期望顺序逐包读取，最后回收写入结果。
	write := func(packet []byte) chan error {
		done := make(chan error, 1)
		go func() { done <- service.writeGameUpperPacketLocked(session, packet) }()
		return done
	}
	waitWrite := func(done chan error) {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("写包失败: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("写包未结束")
		}
	}
	read := func() []byte {
		buffer := make([]byte, 4096)
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		size, err := client.Read(buffer)
		if err != nil {
			t.Fatalf("读包失败: %v", err)
		}
		return append([]byte(nil), buffer[:size]...)
	}

	done := write(anchor)
	if got := read(); !bytes.Equal(got, vendorFirst) {
		t.Fatalf("第一个包应为厂商 op183(00 01)，实际 msg_id=%d len=%d", uint16(got[1])|uint16(got[2])<<8, len(got))
	}
	if got := read(); !bytes.Equal(got, vendorSecond) {
		t.Fatalf("第二个包应为厂商 op183(01 01)，实际 msg_id=%d len=%d", uint16(got[1])|uint16(got[2])<<8, len(got))
	}
	if got := read(); !bytes.Equal(got, anchor) {
		t.Fatalf("第三个包应为锚点 op4")
	}
	waitWrite(done)

	// 再次发送锚点：不得重复注入。
	done = write(anchor)
	if got := read(); !bytes.Equal(got, anchor) {
		t.Fatalf("锚点重发时不应再注入，实际 msg_id=%d len=%d", uint16(got[1])|uint16(got[2])<<8, len(got))
	}
	waitWrite(done)
}

// TestUpperInjectWiring 锁死"注入真的接在发包路径上、且注入包走 core 通道"。
func TestUpperInjectWiring(t *testing.T) {
	dproto, err := os.ReadFile("game_dproto.go")
	if err != nil {
		t.Fatalf("读取 game_dproto.go 失败: %v", err)
	}
	text := string(dproto)
	for _, needle := range []string{
		"s.emitUpperVendorInjectionsLocked(session, msgID)",
		"func (s *Service) writeUpperPacketLockedCore(session *gameSession, inner []byte) error",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("game_dproto.go 缺少 %q —— 注入/写出口的接线被移除了", needle)
		}
	}
	inject, err := os.ReadFile("upper_vendor_inject.go")
	if err != nil {
		t.Fatalf("读取 upper_vendor_inject.go 失败: %v", err)
	}
	if !strings.Contains(string(inject), "s.writeUpperPacketLockedCore(session, append([]byte(nil), entry.packet...))") {
		t.Fatal("注入包必须走 writeUpperPacketLockedCore（同一保护链路），否则会递归或绕过 DPROTO")
	}
}

func hexOf(packet []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(packet)*2)
	for _, value := range packet {
		out = append(out, digits[value>>4], digits[value&0x0F])
	}
	return string(out)
}
