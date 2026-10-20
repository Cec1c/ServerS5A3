package dnfbridge

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// resetUpperSendFilterCache 让同一进程内的多个用例可以重新解析环境变量。
func resetUpperSendFilterCache() {
	upperSendFilterOnce = sync.Once{}
	upperSendFilterParsed = upperSendFilter{}
}

func TestParseUpperSendFilterVariants(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		class      byte
		msgID      uint16
		suppressed bool
	}{
		{name: "按 msg_id 屏蔽任意 classification", raw: "441,646", class: 7, msgID: 441, suppressed: true},
		{name: "未列出的包放行", raw: "441,646", class: 7, msgID: 442, suppressed: false},
		{name: "class 精确匹配命中", raw: "0:183", class: 0, msgID: 183, suppressed: true},
		{name: "class 精确匹配不误伤其他 class", raw: "0:183", class: 7, msgID: 183, suppressed: false},
		{name: "0x 前缀", raw: "0x1b7", class: 3, msgID: 439, suppressed: true},
		{name: "空白与分号分隔", raw: "  441 ;  646 \n 63 ", class: 0, msgID: 63, suppressed: true},
		{name: "混合写法", raw: "7:441, 0:183", class: 7, msgID: 441, suppressed: true},
		{name: "非法片段被忽略而不是误屏蔽", raw: "abc,,441x", class: 0, msgID: 441, suppressed: false},
		{name: "空开关不过滤", raw: "", class: 0, msgID: 441, suppressed: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			filter := parseUpperSendFilter(testCase.raw)
			if got := filter.Suppresses(testCase.class, testCase.msgID); got != testCase.suppressed {
				t.Fatalf("Suppresses(%d,%d)=%v, 期望 %v（开关=%q）",
					testCase.class, testCase.msgID, got, testCase.suppressed, testCase.raw)
			}
		})
	}
}

func TestParseUpperSendFilterMarksEmpty(t *testing.T) {
	if !parseUpperSendFilter("").Empty() {
		t.Fatal("空开关必须报告 Empty，否则生产路径会误判为启用了过滤")
	}
	if !parseUpperSendFilter("abc,,").Empty() {
		t.Fatal("全部非法片段等价于空开关")
	}
	if parseUpperSendFilter("183").Empty() {
		t.Fatal("合法片段不得报告 Empty")
	}
}

func TestCurrentUpperSendFilterReadsEnvAndCaches(t *testing.T) {
	resetUpperSendFilterCache()
	defer resetUpperSendFilterCache()
	t.Setenv(upperSendFilterEnv, "441, 0x1b7")
	filter := currentUpperSendFilter()
	if !filter.Suppresses(0, 441) || !filter.Suppresses(9, 439) {
		t.Fatalf("环境变量解析结果不正确: raw=%q", filter.raw)
	}
	// 解析结果必须缓存：热路径上每个包都读一次环境变量是不可接受的。
	t.Setenv(upperSendFilterEnv, "")
	if !currentUpperSendFilter().Suppresses(0, 441) {
		t.Fatal("第二次调用重新解析了环境变量，缓存失效")
	}
}

func TestCurrentUpperSendFilterEmptyByDefault(t *testing.T) {
	resetUpperSendFilterCache()
	defer resetUpperSendFilterCache()
	t.Setenv(upperSendFilterEnv, "")
	if !currentUpperSendFilter().Empty() {
		t.Fatal("默认（未设置开关）必须完全不过滤，否则会静默改变生产行为")
	}
}

func TestUpperPacketClassMsgID(t *testing.T) {
	// 12 字节与 16 字节 upper 头的 [0]=classification、[1:3]=msg_id 布局一致。
	packet16 := make([]byte, 16+2)
	packet16[0] = 7
	packet16[1] = 0xB7
	packet16[2] = 0x01 // 0x01B7 = 439
	class, msgID, ok := upperPacketClassMsgID(packet16)
	if !ok || class != 7 || msgID != 439 {
		t.Fatalf("16 字节头解析 = (%d,%d,%v)", class, msgID, ok)
	}
	packet12 := make([]byte, 12)
	packet12[0] = 0
	packet12[1] = 0xB7
	packet12[2] = 0x00 // 183
	class, msgID, ok = upperPacketClassMsgID(packet12)
	if !ok || class != 0 || msgID != 183 {
		t.Fatalf("12 字节头解析 = (%d,%d,%v)", class, msgID, ok)
	}
	if _, _, ok := upperPacketClassMsgID([]byte{0x00, 0xB7}); ok {
		t.Fatal("长度不足时必须返回 ok=false，让调用方放行而不是吞包")
	}
}

// TestUpperSendFilterWiring 锁死“诊断开关真的接在发包路径上”。上层包有两条出口：
// 固定 16 字节头发送器（多数登录相位包）与 writeGameUpperPacket 中心函数（兜底）。
// 少接任何一条，二分结论都会是假的。
func TestUpperSendFilterWiring(t *testing.T) {
	cases := []struct {
		file   string
		needle string
		owner  string
	}{
		{file: "game_wire.go", needle: "s.suppressUpperSend(session, classification, msgID", owner: "sendGameUpperFixed16Transport"},
		{file: "game_dproto.go", needle: "s.suppressUpperSend(session, classification, msgID, len(inner))", owner: "writeGameUpperPacketLocked"},
		{file: "upper_send_filter.go", needle: `upperSendFilterEnv = "DNFBRIDGE_SUPPRESS_UPPER_SENDS"`, owner: "开关名"},
	}
	for _, testCase := range cases {
		t.Run(testCase.owner, func(t *testing.T) {
			data, err := os.ReadFile(testCase.file)
			if err != nil {
				t.Fatalf("读取 %s 失败: %v", testCase.file, err)
			}
			if !strings.Contains(string(data), testCase.needle) {
				t.Fatalf("%s 里缺少 %q —— %s 的诊断闸门被移除了", testCase.file, testCase.needle, testCase.owner)
			}
		})
	}
}
