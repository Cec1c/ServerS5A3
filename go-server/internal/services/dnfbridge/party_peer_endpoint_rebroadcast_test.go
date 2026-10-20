package dnfbridge

import (
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 本文件钉住 M2（`DNFBRIDGE_PARTY_PEER_ENDPOINT_REBROADCAST`，对齐 A21/86JP 的 D7）：
//   - off（默认）：端口变了也**一帧都不多发**（现行为）；
//   - on：**端口真的变了**（含 0 → 非 0 的首次上报）才重播整份队伍信息，
//     每个成员各收一份自己的名册（`op153 × 1 + op11 × 1 + op9`）；
//   - 端口没变（同一端口再报一次 / 全零重连探针）：**零重播**；
//   - 重播一律**单次 op11**，即使 M1 的 repeat 档同时打开（两个开关各自单独判读）。
//
// 现场对照：A21 `Network\Handlers\PartyHandler.cs:337-378`（`:360-371` 重播点）、
// 86JP `Network\Handlers\PartyHandler.cs:71-110`。

// sendCapturedPeerEndpointRegistration 把一份真实形状的 CS op2（31B）交给会话的 upper 分发，
// 与 `channel_reconnect_test.go` 用的是同一条路径（class1 / `UpperMsgCharacterRoster` / 31B）。
func sendCapturedPeerEndpointRegistration(t *testing.T, service *Service, session *gameSession, port uint16) {
	t.Helper()
	frame, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.UpperMsgCharacterRoster),
		buildCapturedPeerEndpointRegistrationBody(t, port),
		1,
		dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(session, frame); err != nil {
		t.Fatal(err)
	}
}

func countRosterFrames(frames []upperFrame) (realtime int, endpoints int, op9 int) {
	for _, frame := range frames {
		switch frame.msgID {
		case upperMsgRealtimeInfoFrameID:
			realtime++
		case upperMsgPeerEndpointsID:
			endpoints++
		case upperMsgOp9RosterID:
			op9++
		}
	}
	return realtime, endpoints, op9
}

func TestPeerEndpointRebroadcastOffKeepsProvenBehaviour(t *testing.T) {
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	writeFrames(t, leader)
	writeFrames(t, member)

	sendCapturedPeerEndpointRegistration(t, service, leader, 20484)

	if got := currentPartyPeerPort(leader); got != 20484 {
		t.Fatalf("registered port = %d want 20484 (host order; CS op2 writes it big endian)", got)
	}
	for name, session := range map[string]*gameSession{"leader": leader, "member": member} {
		if frames := writeFrames(t, session); len(frames) != 0 {
			realtime, endpoints, op9 := countRosterFrames(frames)
			t.Fatalf("off mode sent frames to %s: total=%d (op153=%d op11=%d op9=%d)",
				name, len(frames), realtime, endpoints, op9)
		}
	}
}

func TestPeerEndpointRebroadcastOnlyFiresOnRealPortChanges(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	service.options.partyPeerEndpointRebroadcast = partyPeerEndpointRebroadcastOn
	writeFrames(t, leader)
	writeFrames(t, member)

	// 1) 首次上报（0 → 20484）= 端口真的变了 ⇒ 两个成员各一份完整名册。
	sendCapturedPeerEndpointRegistration(t, service, leader, 20484)
	for name, session := range map[string]*gameSession{"leader": leader, "member": member} {
		realtime, endpoints, op9 := countRosterFrames(writeFrames(t, session))
		if realtime != 1 || endpoints != 1 || op9 < 1 {
			t.Fatalf("first port change to %s: op153=%d op11=%d op9=%d, want one full roster per member",
				name, realtime, endpoints, op9)
		}
	}

	// 2) 同一端口再报一次 ⇒ **零重播**。
	sendCapturedPeerEndpointRegistration(t, service, leader, 20484)
	for name, session := range map[string]*gameSession{"leader": leader, "member": member} {
		if frames := writeFrames(t, session); len(frames) != 0 {
			realtime, endpoints, op9 := countRosterFrames(frames)
			t.Fatalf("unchanged port re-report sent frames to %s: total=%d (op153=%d op11=%d op9=%d)",
				name, len(frames), realtime, endpoints, op9)
		}
	}

	// 3) 全零的 31 字节重连探针 ⇒ 端口没变（也不覆盖既有值）⇒ 零重播。
	sendCapturedPeerEndpointRegistration(t, service, leader, 0)
	if got := currentPartyPeerPort(leader); got != 20484 {
		t.Fatalf("zero-port probe overwrote the stored port: %d want 20484", got)
	}
	for name, session := range map[string]*gameSession{"leader": leader, "member": member} {
		if frames := writeFrames(t, session); len(frames) != 0 {
			realtime, endpoints, op9 := countRosterFrames(frames)
			t.Fatalf("zero-port probe sent frames to %s: total=%d (op153=%d op11=%d op9=%d)",
				name, len(frames), realtime, endpoints, op9)
		}
	}

	// 4) 端口换成一个新值 ⇒ 再一次重播；仍**只发一个 op11**（M1 不在这条路上叠加）。
	service.options.partyPeerEndpointsAfterRoster = partyPeerEndpointsAfterRosterRepeat
	sendCapturedPeerEndpointRegistration(t, service, leader, 52216)
	if got := currentPartyPeerPort(leader); got != 52216 {
		t.Fatalf("registered port = %d want 52216", got)
	}
	for name, session := range map[string]*gameSession{"leader": leader, "member": member} {
		realtime, endpoints, op9 := countRosterFrames(writeFrames(t, session))
		if realtime != 1 || endpoints != 1 || op9 < 1 {
			t.Fatalf("second port change to %s: op153=%d op11=%d op9=%d, want a single-endpoint-frame roster",
				name, realtime, endpoints, op9)
		}
	}

	// 判读串钉子：整场里"重播"事件恰好两次（两次真实端口变化），
	// 端口没变的那两次上报一次都不许出现。`registered` 事件是 3 条而不是 4 条：
	// 全零的重连探针在 `captureCurrentPartyPeerEndpointRegistration` 里就提前返回了
	// （端口 0 不覆盖既有值，也不记注册日志 —— 这是改动前就有的行为）。
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if got := strings.Count(logged, "game-party-peer-endpoint-rebroadcast "); got != 2 {
		t.Fatalf("rebroadcast events = %d want 2:\n%s", got, logged)
	}
	if got := strings.Count(logged, "game-party-peer-endpoint-registered "); got != 3 {
		t.Fatalf("registered events = %d want 3 (每次非零上报都记录，只有变化才重播):\n%s", got, logged)
	}
}

// 回城/进城选图收尾窗口里不重播（只记一条 skipped 日志），这是 M2 与
// `DNFBRIDGE_PARTY_DUNGEON_*` / op11 场景刷新禁令之间唯一的时序守卫。
func TestPeerEndpointRebroadcastSkippedDuringTownReturnWindow(t *testing.T) {
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	service.options.partyPeerEndpointRebroadcast = partyPeerEndpointRebroadcastOn
	leader.backToVillageEnterSelectPending = true
	writeFrames(t, leader)
	writeFrames(t, member)

	sendCapturedPeerEndpointRegistration(t, service, leader, 20484)

	// 端口仍然要记下来（记录与重播是两件事）。
	if got := currentPartyPeerPort(leader); got != 20484 {
		t.Fatalf("registered port = %d want 20484", got)
	}
	for name, session := range map[string]*gameSession{"leader": leader, "member": member} {
		if frames := writeFrames(t, session); len(frames) != 0 {
			realtime, endpoints, op9 := countRosterFrames(frames)
			t.Fatalf("town-return window sent frames to %s: total=%d (op153=%d op11=%d op9=%d)",
				name, len(frames), realtime, endpoints, op9)
		}
	}
}

// 未成形的队伍（只有自己/已解散）没有"整份队伍信息"可重播。
func TestPeerEndpointRebroadcastRequiresMultiMemberParty(t *testing.T) {
	service := &Service{options: options{partyPeerEndpointRebroadcast: partyPeerEndpointRebroadcastOn}}
	session := &gameSession{conn: &bufferConn{}, connID: "no-party", selectedCharacterID: 42}
	service.bindGameSessionCharacter(session, 42)

	sendCapturedPeerEndpointRegistration(t, service, session, 20484)

	if frames := writeFrames(t, session); len(frames) != 0 {
		t.Fatalf("party-less session received %d frames", len(frames))
	}
}

// 单变量纪律：M2 的重播路径永远只发一次 op11（即使 M1 = repeat）。
func TestPeerEndpointRebroadcastUsesSingleEndpointFrameBuilder(t *testing.T) {
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.options.partyPeerEndpointRebroadcast = partyPeerEndpointRebroadcastOn
	service.options.partyPeerEndpointsAfterRoster = partyPeerEndpointsAfterRosterRepeat
	state := runtimePartyStateSnapshot(leader)
	writeFrames(t, leader)

	if err := service.sendRuntimePartySnapshotWithoutPeerEndpointRepeat(leader, state); err != nil {
		t.Fatal(err)
	}
	frames := writeFrames(t, leader)
	realtime, endpoints, op9 := countRosterFrames(frames)
	if realtime != 1 || endpoints != 1 || op9 < 1 {
		t.Fatalf("rebroadcast roster: op153=%d op11=%d op9=%d, want a single op11", realtime, endpoints, op9)
	}
}

// 只读观测：op97（PEER_CONNECT_RESULT）不得改变行为（不新增/不抑制任何回包），
// 但要留下一条可判读的日志（参考实现是"收下即忽略"）。
func TestPeerConnectResultStaysReadOnly(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	writeFrames(t, leader)
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body[0:4], 1)
	frame, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketPeerConnectResult),
		body,
		1,
		dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(leader, frame); err != nil {
		t.Fatal(err)
	}
	if frames := writeFrames(t, leader); len(frames) != 0 {
		t.Fatalf("op97 produced %d outbound frames, want read-only ignore", len(frames))
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logged, "game-party-peer-connect-result-observed") {
		t.Fatalf("op97 was not observed in the packet log:\n%s", logged)
	}
	if !strings.Contains(logged, "body_hex=0100000000000000") {
		t.Fatalf("op97 observation did not record the raw body:\n%s", logged)
	}
	if !strings.Contains(logged, "behavior=read_only_ignored_reference_implementations_accept_and_drop") {
		t.Fatalf("op97 observation is missing the read-only marker:\n%s", logged)
	}
}
