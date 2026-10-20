package dnfbridge

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 现场样本：`90CN_protocol.log` 里 6 条 `msg=966 body_len=23` **逐字节完全相同**
// （07:05:28.832 / :41.000 / :53.045 / 07:15:09.080 / :21.186 / :33.283，
// 两段都是两成员队伍 + 队长在副本门口交互的窗口；同一窗口没有 msg=15）。
var currentP2pHolePunchingReportBody = []byte{
	0xFF, 0x00, 0x00, 0x00, 0x00, 0xE0, 0x2E, 0x00, 0x00, 0x01, 0x00, 0x00,
	0x00, 0x05, 0x00, 0x00, 0x00, 0x28, 0x00, 0x00, 0x00, 0x04, 0x00,
}

const currentP2pHolePunchingReportBodyHex = "ff00000000e02e00000100000005000000280000000400"

const currentP2pHolePunchingReportEvent = "game-p2p-hole-punching-report-observed"

// 分布点的钉子：走**真实 legacy 路由**（`handleLegacyGamePacket` →
// `handleGameCommand` → `handleAlignedGameCommand`），因为 966 不在
// `isKnownClientUpperMsg` 表里，class1/op966 帧在现场就是被 legacy 解析器切走的
// （与已实测的 `cmd=1 type=576` 同形）。三条上报必须留下**三行**（不合并、不丢样本），
// 且**一个字节都不回**。
func TestP2pHolePunchingReportIsObservedOnLegacyRouteWithoutReply(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	writeFrames(t, leader)

	// 夹具必须真的是"两成员队伍里的那个会话"，否则这条用例证明不了相关性字段。
	partyState := runtimePartyStateSnapshot(leader)
	if partyState.PartyID == 0 || len(runtimePartyMembers(partyState)) != 2 {
		t.Fatalf("fixture did not produce a 2-member runtime party: %+v", partyState)
	}

	// 现场是同一个 body 在一个窗口里发了 3 次。
	for burst := 0; burst < 3; burst++ {
		if err := service.handleLegacyGamePacket(leader, buildP2pHolePunchingReportLegacyPacket(t, uint16(burst))); err != nil {
			t.Fatalf("burst %d: %v", burst, err)
		}
	}

	// 零字节回包：全仓没有 op966 的已证 S2C 形状，观测者不许发明一个。
	if frames := writeFrames(t, leader); len(frames) != 0 {
		t.Fatalf("op966 must not write any reply, wrote %d frames: %+v", len(frames), frames)
	}

	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"kind=" + currentP2pHolePunchingReportEvent + " ",
		"conn_id=",
		"channel_id=10",
		"char_id=1001",
		"type=966",
		"runtime_cmd_name=ENUM_CMDPACKET_P2P_HOLE_PUNCHING_SUCCESS_RATE",
		"body_len=23",
		"body_hex=" + currentP2pHolePunchingReportBodyHex,
		"in_party=true",
		fmt.Sprintf("party_id=%d", partyState.PartyID),
		"party_member_count=2",
		// 布局未证实 ⇒ 只记 hex，不解码任何字段。
		"layout=unproven_hex_only_no_field_decoding",
		"reply=none",
		"behavior=read_only_observed_zero_bytes_sent",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("op966 observation is missing %q:\n%s", want, logged)
		}
	}
	// 每条上报一行：周期性上报的样本不许被合并掉。
	if got := strings.Count(logged, currentP2pHolePunchingReportEvent); got != 3 {
		t.Fatalf("op966 observation lines = %d want 3 (one per report):\n%s", got, logged)
	}
	if got := strings.Count(logged, "body_hex="+currentP2pHolePunchingReportBodyHex); got != 3 {
		t.Fatalf("op966 raw-body lines = %d want 3:\n%s", got, logged)
	}
}

// 队伍相关性（父子代理最关心的那个字段）：966 在实测里**只**出现在组队窗口，
// 单人副本那次一次都没有。回到今天的服务端：不在队伍里的会话拿到 in_party=false，
// 但仍然照记 body_hex（判据不能靠"没记"来表达）。
func TestP2pHolePunchingReportRecordsPartyCorrelation(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, _, _, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger

	solo := &gameSession{conn: &bufferConn{}, connID: "p2p-report-solo", selectedCharacterID: 1003}
	if err := service.handleLegacyGamePacket(solo, buildP2pHolePunchingReportLegacyPacket(t, 7)); err != nil {
		t.Fatal(err)
	}
	if frames := writeFrames(t, solo); len(frames) != 0 {
		t.Fatalf("solo op966 wrote %d frames, want read-only ignore", len(frames))
	}

	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"char_id=1003",
		"in_party=false",
		"party_id=0",
		"party_member_count=0",
		"body_hex=" + currentP2pHolePunchingReportBodyHex,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("solo op966 observation is missing %q:\n%s", want, logged)
		}
	}
}

// body 长度未证实：任何长度（含 0 与现场之外的形状）都必须**只记不崩**，
// 并且长度原样进日志，不做"补齐/截断到 23"这种发明。
func TestP2pHolePunchingReportToleratesUnexpectedBodyLengths(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	writeFrames(t, leader)

	for _, bodyLen := range []int{0, 1, 5, 22, 23, 24, 64} {
		body := make([]byte, bodyLen)
		for index := range body {
			body[index] = byte(index + 1)
		}
		raw := buildLegacyGamePacketForBridgeTest(
			byte(dnfenum.GameCmdCommand),
			uint16(dnfenum.CmdPacketP2pHolePunchingSuccessRate),
			uint16(bodyLen),
			body,
		)
		if err := service.handleLegacyGamePacket(leader, raw); err != nil {
			t.Fatalf("body_len=%d: %v", bodyLen, err)
		}
		if frames := writeFrames(t, leader); len(frames) != 0 {
			t.Fatalf("body_len=%d wrote %d frames, want read-only ignore", bodyLen, len(frames))
		}
	}

	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, bodyLen := range []int{0, 1, 5, 22, 23, 24, 64} {
		want := fmt.Sprintf("body_len=%d body_hex=%s", bodyLen, p2pHolePunchingReportBodyHex(bodyLen))
		if !strings.Contains(logged, want) {
			t.Fatalf("body_len=%d was not observed verbatim (want %q):\n%s", bodyLen, want, logged)
		}
	}
	if got := strings.Count(logged, currentP2pHolePunchingReportEvent); got != 7 {
		t.Fatalf("op966 observation lines = %d want 7:\n%s", got, logged)
	}
}

// 归属边界：只有 966 归这个观测者。别的 opcode（含同族的 576/op19/op97 与 966 的邻居）
// 必须返回 handled=false，绝不因为"顺手"被吞掉。
func TestP2pHolePunchingReportOnlyClaimsItsOwnOpcode(t *testing.T) {
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	for _, typ := range []uint16{
		uint16(dnfenum.CmdPacketMoveItemspace),
		uint16(dnfenum.CmdPacketPeerConnectResult),
		uint16(dnfenum.CmdPacketCreateUpgradeRoom),
		uint16(dnfenum.CmdPacketP2pHolePunchingSuccessRate) - 1,
		uint16(dnfenum.CmdPacketP2pHolePunchingSuccessRate) + 1,
	} {
		handled, err := service.observeCurrentP2pHolePunchingReport(leader, typ, currentP2pHolePunchingReportBody)
		if err != nil {
			t.Fatalf("type=%d: %v", typ, err)
		}
		if handled {
			t.Fatalf("type=%d must not be claimed by the op966 observer", typ)
		}
	}
	handled, err := service.observeCurrentP2pHolePunchingReport(
		leader,
		uint16(dnfenum.CmdPacketP2pHolePunchingSuccessRate),
		currentP2pHolePunchingReportBody,
	)
	if err != nil || !handled {
		t.Fatalf("op966 handled=%t err=%v, want the observer to own it", handled, err)
	}
	// nil 会话/服务不许 panic（分发层在测试与探针里会构造半成品 Service）。
	var nilService *Service
	if handled, err := nilService.observeCurrentP2pHolePunchingReport(leader, 966, nil); handled || err != nil {
		t.Fatalf("nil service handled=%t err=%v", handled, err)
	}
	if handled, err := service.observeCurrentP2pHolePunchingReport(nil, 966, nil); handled || err != nil {
		t.Fatalf("nil session handled=%t err=%v", handled, err)
	}
}

func buildP2pHolePunchingReportLegacyPacket(t *testing.T, seq uint16) []byte {
	t.Helper()
	return buildLegacyGamePacketForBridgeTest(
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketP2pHolePunchingSuccessRate),
		seq,
		append([]byte(nil), currentP2pHolePunchingReportBody...),
	)
}

// p2pHolePunchingReportBodyHex 生成 1..n 的递增字节串，与用例里造的 body 同形。
func p2pHolePunchingReportBodyHex(bodyLen int) string {
	body := make([]byte, bodyLen)
	for index := range body {
		body[index] = byte(index + 1)
	}
	return hex.EncodeToString(body)
}
