package dnfbridge

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// TestHardcoreCharacAckModeDefaultsToFailure 锁死"默认=修复态"：
// 我们的角色没有决斗场(하드코어)记录，默认必须回失败体，否则客户端又会长出
// 决斗场任务书占位面板（2026-09-27 现场事故）。
func TestHardcoreCharacAckModeDefaultsToFailure(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{env: "", want: hardcoreCharacAckFailure},
		{env: "  ", want: hardcoreCharacAckFailure},
		{env: "failure", want: hardcoreCharacAckFailure},
		{env: "FAILURE", want: hardcoreCharacAckFailure},
		{env: " failure ", want: hardcoreCharacAckFailure},
		{env: "success", want: hardcoreCharacAckSuccess},
		{env: "off", want: hardcoreCharacAckOff},
		{env: "bogus", want: hardcoreCharacAckFailure},
	}
	for _, testCase := range cases {
		t.Setenv(hardcoreCharacAckEnvKey, testCase.env)
		if got := currentHardcoreCharacAckMode(); got != testCase.want {
			t.Fatalf("env=%q → %q, 期望 %q", testCase.env, got, testCase.want)
		}
	}
}

// readUpperFrame 从管道读一包并按当前头长切出 msg_id 与 body。
func readUpperFrame(t *testing.T, client net.Conn, headerSize int) (uint16, []byte) {
	t.Helper()
	buffer := make([]byte, 4096)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	size, err := client.Read(buffer)
	if err != nil {
		t.Fatalf("读包失败: %v", err)
	}
	packet := buffer[:size]
	if len(packet) < headerSize {
		t.Fatalf("包长 %d 小于头长 %d", len(packet), headerSize)
	}
	msgID := uint16(packet[1]) | uint16(packet[2])<<8
	return msgID, append([]byte(nil), packet[headerSize:]...)
}

// TestHiddenCharacHardcoreAckBodies 逐档验证真正写到连接上的字节：
//
//	failure（默认）→ class1/op646 body 00 00（"没有决斗场数据"）
//	success（回退）→ op646 body 01（厂商数据态，会引发占位面板）
//	off            → 一个字节都不写
func TestHiddenCharacHardcoreAckBodies(t *testing.T) {
	wantMsgID := uint16(dnfenum.UpperMsgRebirthHardcoreCharac)
	cases := []struct {
		name      string
		env       string
		wantSent  bool
		wantBody  []byte
		assertion string
	}{
		{name: "默认回失败体", env: "", wantSent: true, wantBody: []byte{0x00, 0x00}},
		{name: "回退到厂商成功体", env: "success", wantSent: true, wantBody: []byte{0x01}},
		{name: "静默档不回包", env: "off", wantSent: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(hardcoreCharacAckEnvKey, testCase.env)
			service := &Service{}
			headerSize := service.gameUpperHeaderSize()
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			session := &gameSession{conn: server, connID: "game-test-hardcore-ack"}

			type result struct {
				sent bool
				err  error
			}
			done := make(chan result, 1)
			go func() {
				sent, err := service.sendHiddenCharacHardcoreAck(session)
				done <- result{sent: sent, err: err}
			}()

			if !testCase.wantSent {
				select {
				case got := <-done:
					if got.err != nil {
						t.Fatalf("off 档报错: %v", got.err)
					}
					if got.sent {
						t.Fatal("off 档不应报告已发送")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("off 档超时")
				}
				// 连接上不应有任何字节：设一个短读超时确认。
				_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
				buffer := make([]byte, 64)
				if size, err := client.Read(buffer); err == nil && size > 0 {
					t.Fatalf("off 档不应写包，实际写了 %d 字节", size)
				}
				return
			}

			msgID, body := readUpperFrame(t, client, headerSize)
			if msgID != wantMsgID {
				t.Fatalf("msg_id = %d, 期望 %d", msgID, wantMsgID)
			}
			if len(body) != len(testCase.wantBody) {
				t.Fatalf("body = % x, 期望 % x", body, testCase.wantBody)
			}
			for index := range testCase.wantBody {
				if body[index] != testCase.wantBody[index] {
					t.Fatalf("body = % x, 期望 % x", body, testCase.wantBody)
				}
			}
			select {
			case got := <-done:
				if got.err != nil {
					t.Fatalf("发送报错: %v", got.err)
				}
				if !got.sent {
					t.Fatal("应当报告已发送")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("发送超时")
			}
		})
	}
}

// TestHardcoreAckWiring 锁死接线：op645 的处理必须走新档位函数，且默认档位由开关决定。
func TestHardcoreAckWiring(t *testing.T) {
	source, err := os.ReadFile("game_dispatch_upper.go")
	if err != nil {
		t.Fatalf("读取 game_dispatch_upper.go 失败: %v", err)
	}
	text := string(source)
	if !strings.Contains(text, "s.sendHiddenCharacHardcoreAck(session)") {
		t.Fatal("op645 处理没有接上 sendHiddenCharacHardcoreAck")
	}
	if strings.Contains(text, "snapshot_112000_646_success_body") {
		t.Fatal("旧的 op646 恒定成功体分支仍然存在，弹窗会复发")
	}
}
