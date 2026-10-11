package dnfbridge

import (
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func resetVendorAbsentBlockCache() {
	vendorAbsentBlockOnce = sync.Once{}
	vendorAbsentBlockParsed = upperSendFilter{}
}

// TestVendorAbsentSendBlockDefault 锁死默认名单：455/102 是现场证据锁定的
// "厂商全库零收发"包，默认必须挡；否则进城后决斗场任务书占位面板会复发。
func TestVendorAbsentSendBlockDefault(t *testing.T) {
	resetVendorAbsentBlockCache()
	defer resetVendorAbsentBlockCache()
	t.Setenv(vendorAbsentSendBlockEnvKey, "")
	block := currentVendorAbsentSendBlock()
	if !block.Suppresses(0, 455) {
		t.Fatal("默认必须阻断 op455：厂商全库零收发，且 r28/r29 自动化 A/B 证明它就是弹窗真凶")
	}
	// 宠物/生物状态包 102 是真实功能，绝不能被默认挡掉（3 条既有用例依赖它）。
	if block.Suppresses(0, 102) {
		t.Fatal("默认名单不能包含 op102（宠物场景状态）")
	}
	// 厂商确实会发的包绝不能被默认挡掉。
	for _, msgID := range []uint16{102, 441, 63, 143, 14, 646, 411, 108, 2} {
		if block.Suppresses(0, msgID) || block.Suppresses(1, msgID) {
			t.Fatalf("默认名单不应包含厂商会发的 op%d", msgID)
		}
	}
}

func TestVendorAbsentSendBlockEnvOverride(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		msgID      uint16
		suppressed bool
	}{
		{name: "追加名单", env: "455,102,999", msgID: 999, suppressed: true},
		{name: "替换名单后旧项放行", env: "999", msgID: 455, suppressed: false},
		{name: "none 完全关闭", env: "none", msgID: 455, suppressed: false},
		{name: "off 完全关闭", env: "off", msgID: 102, suppressed: false},
		{name: "短横线完全关闭", env: "-", msgID: 455, suppressed: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			block := parseVendorAbsentSendBlock(testCase.env)
			if got := block.Suppresses(0, testCase.msgID); got != testCase.suppressed {
				t.Fatalf("env=%q op=%d → %v, 期望 %v", testCase.env, testCase.msgID, got, testCase.suppressed)
			}
		})
	}
	// 空白环境变量的"用默认"是调用方（currentVendorAbsentSendBlock）的语义。
	t.Run("空白用默认", func(t *testing.T) {
		resetVendorAbsentBlockCache()
		defer resetVendorAbsentBlockCache()
		t.Setenv(vendorAbsentSendBlockEnvKey, "   ")
		if !currentVendorAbsentSendBlock().Suppresses(0, 455) {
			t.Fatal("空白环境变量应落到默认名单（含 op455）")
		}
	})
}

// TestVendorAbsentSendBlockStopsPacketOnWire 在真实发包出口上验证：
// 名单内的包一个字节都不写，名单外的包照常写。
func TestVendorAbsentSendBlockStopsPacketOnWire(t *testing.T) {
	resetVendorAbsentBlockCache()
	resetUpperSendFilterCache()
	defer resetVendorAbsentBlockCache()
	defer resetUpperSendFilterCache()
	t.Setenv(vendorAbsentSendBlockEnvKey, "455,102")
	t.Setenv(upperSendFilterEnv, "")

	service := &Service{}
	headerSize := service.gameUpperHeaderSize()

	blocked := anchorUpperPacket(455, 0, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	allowed := anchorUpperPacket(600, 0, []byte{0xAB})

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	session := &gameSession{conn: server, connID: "game-test-vendor-absent"}

	done := make(chan error, 1)
	go func() {
		if err := service.writeGameUpperPacketLocked(session, blocked); err != nil {
			done <- err
			return
		}
		done <- service.writeGameUpperPacketLocked(session, allowed)
	}()

	// 第一个读到的包必须是 allowed（600）：若 455 没被挡，先到的会是它。
	msgID, _ := readUpperFrame(t, client, headerSize)
	if msgID != 600 {
		t.Fatalf("被阻断的 op455 之后应只剩 600，实际先读到 msg_id=%d", msgID)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("写包失败: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("写包超时")
	}
}

func TestVendorAbsentSendBlockWiring(t *testing.T) {
	source, err := os.ReadFile("game_dproto.go")
	if err != nil {
		t.Fatalf("读取 game_dproto.go 失败: %v", err)
	}
	if !strings.Contains(string(source), "s.blockVendorAbsentSend(session, classification, msgID, len(inner))") {
		t.Fatal("发包出口没有接上 blockVendorAbsentSend —— 阻断名单会失效")
	}
}
