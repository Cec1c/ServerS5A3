package dnfbridge

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 本文件是操作者工单①「副本门前撞在地图边上不进去、回头再进才弹选图界面」的判据测试。
//
// 机制（只读诊断 `_dist/dungeon-door-trigger-diagnosis.md` 已钉死，本文件不重新推导）：
//   - 布防：`town_move.go:334`，每一次"城镇 transport 型 op36 提交"都置
//     `session.townTransportEnterSelectPending = townTransportRequest`；
//   - 决策点：`game_enter_select_dungeon.go:29-42`，挂账为真 ⇒ 零发包 + `response=none`
//     （既没有 op15 成功体、也没有疲劳与 op27，当前客户端据此装不出可选副本页）；
//   - 挂账只由"被它吞掉的那条 op15"自己清掉、且**没有时限** ⇒ 玩家在 transport 之后
//     任意多久第一次撞门都会被吃掉，只有第二次（操作者的"回头再进"）才被正常应答。
//
// ★ 真伪判据：本文件**只写字面量开关名**（townTransportWindowEnvLiteral），不引用实现里
// 新增的任何符号 ⇒ 它在改动前的树上照样**编译**，并因为"挂账没有时限"而在
// TestTownTransportGateOp15IsAnsweredAfterTheSceneFinalizerWindow 的 (B) 段断言处 **FAIL**。
// 它的逐字取反就是既有用例 `town_enter_select_test.go:190`
// （TestTownTransportSceneFinalizerOp15DoesNotOpenDungeonSelector：同样的输入、相反的输出要求）。
const townTransportWindowEnvLiteral = "DNFBRIDGE_TOWN_TRANSPORT_ENTER_SELECT_WINDOW_MS"

// townTransportGateFixture 建夹具（夹具本身复用成型的 newTownMoveTest：角色 29 在 38/1，
// 真档 PVF 形状的 town 38/39/40 区域表）并把包日志接到临时文件上 —— 事件名与
// `response=` 字段是线上判读的第一判据，必须由测试钉住。
func townTransportGateFixture(t *testing.T) (*Service, *gameSession, *bufferConn, string) {
	t.Helper()
	service, session, _ := newTownMoveTest(t)
	logPath := filepath.Join(t.TempDir(), "town-transport-enter-select-window.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	connection, ok := session.conn.(*bufferConn)
	if !ok {
		t.Fatalf("town move fixture conn = %T", session.conn)
	}
	return service, session, connection, logPath
}

// townTransportGateArm 走**生产布防路径**（op36 提交 → `town_move.go:334`）把挂账布上。
// 目标固定用夹具里真实存在、无 `[need quest]`/`[need level]` 门槛的 (38,0)
// `new_Elvengard.map`：世界地图/传送站形状（opaque u16a=town、u16b=1、u32=0、tail=0）
// ⇒ `LooksLikeTownMapStationSelection` 为真；同城镇 ⇒ 不需要跨城镇拓扑证明。
func townTransportGateArm(t *testing.T, service *Service, session *gameSession, body []byte) {
	t.Helper()
	if err := service.handleTownSetUserArea(session, body); err != nil {
		t.Fatalf("arm town transport op36: %v", err)
	}
	if !session.townTransportEnterSelectPending {
		t.Fatalf("op36 body=%x 必须布防城镇 transport op15 挂账（town_move.go:334）", body)
	}
}

// townTransportGateOp15 驱动真实的 op15 入口所用的那个函数
// （`game_dispatch_upper.go:425-426` 就是 sendEnterSelectDungeonState(session,"upper_15",false,true)），
// 并返回本次真正写出的字节数。
func townTransportGateOp15(t *testing.T, service *Service, session *gameSession, connection *bufferConn, source string) []byte {
	t.Helper()
	connection.write.Reset()
	if err := service.sendEnterSelectDungeonState(session, source, false, true); err != nil {
		t.Fatalf("drive op15 source=%s: %v", source, err)
	}
	return append([]byte(nil), connection.write.Bytes()...)
}

// townTransportGateLogLine 返回包日志里含 event 的那一行（找不到即 FAIL）。
func townTransportGateLogLine(t *testing.T, logText string, event string) string {
	t.Helper()
	for _, line := range strings.Split(logText, "\n") {
		if strings.Contains(line, "kind="+event) {
			return line
		}
	}
	t.Fatalf("包日志缺少事件 %s（全文 %d 字节）：\n%s", event, len(logText), logText)
	return ""
}

// TestTownTransportGateOp15IsAnsweredAfterTheSceneFinalizerWindow 是本项的真伪判据。
//
// (A) 窗口内：逐字节等于改动前 —— 这条 op15 是客户端自己的场景收尾，零发包 + response=none；
// (B) ★ 证伪点：窗口已过 —— 这条 op15 是玩家自己的开门意图，**必须**照常应答
//
//	（op15 成功体 + 疲劳 + op27 选择页上下文）。
func TestTownTransportGateOp15IsAnsweredAfterTheSceneFinalizerWindow(t *testing.T) {
	service, session, connection, logPath := townTransportGateFixture(t)
	service.markCurrentTownSceneReady(session)

	// (A) 窗口内：用一个远大于测试耗时的窗口（60 s），避免夹具慢时把这一半测成"过期"。
	t.Setenv(townTransportWindowEnvLiteral, "60000")
	townTransportGateArm(t, service, session, buildTownMapStationRequest(38, 0, 1519, 489, 5))
	if written := townTransportGateOp15(t, service, session, connection, "test_op15_inside_window"); len(written) != 0 ||
		session.townTransportEnterSelectPending || session.enterSelectDungeonSent ||
		session.enterSelectDungeonAckSent || session.enterSelectDungeonContextSent {
		t.Fatalf("窗口内的场景收尾 op15 必须照旧被吞：wrote=%x pending=%t sent=%t ack=%t context=%t",
			written, session.townTransportEnterSelectPending,
			session.enterSelectDungeonSent, session.enterSelectDungeonAckSent, session.enterSelectDungeonContextSent)
	}

	// (B) ★ 窗口已过：挂账必须失效，这条 op15 必须被完整应答。
	t.Setenv(townTransportWindowEnvLiteral, "1")
	townTransportGateArm(t, service, session, buildTownMapStationRequest(38, 0, 1519, 489, 5))
	time.Sleep(20 * time.Millisecond) // 跨过 1 ms 窗口
	written := townTransportGateOp15(t, service, session, connection, "test_op15_after_window")
	if len(written) == 0 {
		t.Fatal("窗口外到达的 op15 必须被应答，但一个字节都没写：" +
			"改动前的无时限挂账会把它吞掉（response=none ⇒ 客户端装不出选图页，" +
			"操作者口径「副本门前撞在地图边上不进去、回头再进才弹选图界面」）")
	}
	if !session.enterSelectDungeonSent || !session.enterSelectDungeonAckSent || session.townTransportEnterSelectPending {
		t.Fatalf("窗口外的 op15 没有装上选择器：sent=%t ack=%t pending=%t wrote=%x",
			session.enterSelectDungeonSent, session.enterSelectDungeonAckSent,
			session.townTransportEnterSelectPending, written)
	}
	ack, _ := splitGameServerUpperPacket(t, written)
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketEnterSelectDungeon) ||
		!bytes.Equal(ack.Body, []byte{1, 1, 0}) {
		t.Fatalf("窗口外的 op15 应答形状不对：header=%+v body=%x written=%x", ack.Header, ack.Body, written)
	}

	// 两条路径的事件名与 response 字段（线上判读的第一判据，必须与兄弟窗口同形）。
	if err := service.packetLog.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	suppressed := townTransportGateLogLine(t, string(logged), "game-upper-enter-select-dungeon-after-town-transport-suppressed")
	if !strings.Contains(suppressed, "response=none") {
		t.Fatalf("窗口内事件必须仍报 response=none：%s", suppressed)
	}
	expired := townTransportGateLogLine(t, string(logged), "game-upper-enter-select-dungeon-after-town-transport-window-expired")
	if !strings.Contains(expired, "response=enter_select_state") ||
		!strings.Contains(expired, "reason=scene_finalizer_op15_never_arrived_answer_player_selector_intent") {
		t.Fatalf("窗口过期事件形状必须与兄弟窗口同形（response=enter_select_state）：%s", expired)
	}
}

// TestTownTransportGateOp15InsideTheWindowStaysSuppressed 钉住"窗口内逐字节等于改动前"这一半：
// 零发包、挂账被这条 op15 消费、不置任何选择器标志，事件仍报 response=none。
func TestTownTransportGateOp15InsideTheWindowStaysSuppressed(t *testing.T) {
	t.Setenv(townTransportWindowEnvLiteral, "60000")
	service, session, connection, logPath := townTransportGateFixture(t)
	service.markCurrentTownSceneReady(session)
	townTransportGateArm(t, service, session, buildTownMapStationRequest(38, 0, 1519, 489, 5))

	if written := townTransportGateOp15(t, service, session, connection, "test_op15_inside_window_only"); len(written) != 0 {
		t.Fatalf("窗口内的场景收尾 op15 必须零发包：wrote=%x", written)
	}
	if session.townTransportEnterSelectPending || session.enterSelectDungeonSent ||
		session.enterSelectDungeonAckSent || session.enterSelectDungeonContextSent {
		t.Fatalf("窗口内不得置任何选择器标志：pending=%t sent=%t ack=%t context=%t",
			session.townTransportEnterSelectPending, session.enterSelectDungeonSent,
			session.enterSelectDungeonAckSent, session.enterSelectDungeonContextSent)
	}
	if err := service.packetLog.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	line := townTransportGateLogLine(t, string(logged), "game-upper-enter-select-dungeon-after-town-transport-suppressed")
	if !strings.Contains(line, "response=none") {
		t.Fatalf("窗口内事件必须报 response=none：%s", line)
	}
}

// TestTownTransportGateLegacyLiteralKeepsTheUnboundedSwallow 是**回退档**的逐字判据：
// legacy / off / false / no = 逐字节回到改动前（无时限恒吞）。这里刻意睡 20 ms 再过窗口，
// 用来区分"有时限"与"无时限"：回退档必须照样吞掉（否则回退档就不是回退档了）。
func TestTownTransportGateLegacyLiteralKeepsTheUnboundedSwallow(t *testing.T) {
	for _, literal := range []string{"legacy", "off", "false", "no"} {
		t.Run(literal, func(t *testing.T) {
			t.Setenv(townTransportWindowEnvLiteral, literal)
			service, session, connection, _ := townTransportGateFixture(t)
			service.markCurrentTownSceneReady(session)
			townTransportGateArm(t, service, session, buildTownMapStationRequest(38, 0, 1519, 489, 5))
			time.Sleep(20 * time.Millisecond)
			if written := townTransportGateOp15(t, service, session, connection, "test_op15_legacy_rollback"); len(written) != 0 {
				t.Fatalf("回退档 %q 必须逐字节回到改动前（无时限恒吞）：wrote=%x", literal, written)
			}
			if session.enterSelectDungeonSent || session.enterSelectDungeonAckSent {
				t.Fatalf("回退档 %q 不得装上选择器：sent=%t ack=%t",
					literal, session.enterSelectDungeonSent, session.enterSelectDungeonAckSent)
			}
		})
	}
}
