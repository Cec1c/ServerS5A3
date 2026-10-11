package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"net"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 本文件是 2026-09-25 本轮"按厂商 9.16 参考实现对齐 formation"的钉子，共四组：
//
//  1. `vendorO11Body…`：op11 body 用**厂商实测的两条 hex 做夹具**，逐字节相等
//     （`D:\DNF90-test\server\runtime\logs\packet_log.txt:3337` / `:3340` 去 16 字节头）。
//  2. `vendorFormation…`：`DNFBRIDGE_PARTY_FORMATION_ORDER` 三档的**发帧顺序**。
//  3. `includeSelf…`：`DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF` 两档的行集合
//     （off = 开发商形状"只有对端"；on = 逐字节回到我们改动前的形状）。
//  4. `switchMatrix…`：endian × include_self × order 的**组合矩阵**不 panic、不报错，
//     且每次组合的 op11 只发一条。

// 厂商 9.16 两条 op11 body（各 23B = `1 字节员数 + 22B/行`），逐字节照抄包日志。
const (
	vendorOp11BodyToChar2 = "010200c0a80065c0a8006575301c000000010002000064"
	vendorOp11BodyToChar1 = "010100c0a80065c0a8006575311b000000010002000064"
)

func mustDecodeVendorHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("vendor fixture hex %q: %v", value, err)
	}
	return decoded
}

// 22B 行布局（A21 `PartyIpInfoBuilder.cs:6-13`，与厂商实测逐字段相符）：
//
//	u16 uid(LE) | 4B inner | 4B outer | u16 port(BE) | u32 acc(LE) | u8 nat | u32 mtu(LE) | u8 char_attr
func vendorOp11Row(
	uid uint16,
	port uint16,
	accountID uint32,
) currentPartyPeerEndpointRow {
	return currentPartyPeerEndpointRow{
		userID:    uid,
		inner:     [4]byte{192, 168, 0, 101},
		outer:     [4]byte{192, 168, 0, 101},
		port:      port,
		accountID: accountID,
		// 厂商这两行的 nat/mtu/char_attr 不是我们的默认值，夹具必须逐字段给出，
		// 才能断言"同一个 22B 行布局"。它们的来源（客户端哪个字段）目前无证据，
		// 所以只用于夹具，不参与线上组装（见 party_peer_self_endpoint.go）。
		nat:      1,
		mtu:      512,
		charAttr: 0x64,
	}
}

// 第 1 组：厂商 hex 夹具**逐字节相等**。
func TestVendorOp11BodyByteForByteFromCapturedFixtures(t *testing.T) {
	cases := []struct {
		name     string
		hex      string
		uid      uint16
		port     uint16
		account  uint32
		relayLeg string
	}{
		{
			name:     "conn59_to_char2",
			hex:      vendorOp11BodyToChar2,
			uid:      2,
			port:     30000,
			account:  28,
			relayLeg: "recipient char_id=1 → peer char_id=2 @ relay port 30000",
		},
		{
			name:     "conn61_to_char1",
			hex:      vendorOp11BodyToChar1,
			uid:      1,
			port:     30001,
			account:  27,
			relayLeg: "recipient char_id=2 → peer char_id=1 @ relay port 30001",
		},
	}
	for _, tc := range cases {
		want := mustDecodeVendorHex(t, tc.hex)
		got := buildCurrentPartyPeerEndpointInfoRows([]currentPartyPeerEndpointRow{
			vendorOp11Row(tc.uid, tc.port, tc.account),
		})
		if !bytes.Equal(got, want) {
			t.Fatalf("%s (%s):\n got=% X\nwant=% X", tc.name, tc.relayLeg, got, want)
		}
		// 形状必须同时是"1 字节员数 + 22B/行"，且员数 = 1（只含对端）。
		if len(got) != 1+currentPartyPeerEndpointSize || got[0] != 1 {
			t.Fatalf("%s: shape len=%d count=%d, want len=%d count=1",
				tc.name, len(got), got[0], 1+currentPartyPeerEndpointSize)
		}
		// 逐字段回读，把"端口按网络序写"这件事钉死在夹具上。
		if uid := uint16(got[1]) | uint16(got[2])<<8; uid != tc.uid {
			t.Fatalf("%s: uid=%d want %d", tc.name, uid, tc.uid)
		}
		if port := uint16(got[11])<<8 | uint16(got[12]); port != tc.port {
			t.Fatalf("%s: port bytes=% X want %d big endian", tc.name, got[11:13], tc.port)
		}
		if account := uint32(got[13]) | uint32(got[14])<<8 | uint32(got[15])<<16 | uint32(got[16])<<24; account != tc.account {
			t.Fatalf("%s: account=%d want %d", tc.name, account, tc.account)
		}
		if got[17] != 1 || got[18] != 0 || got[19] != 2 || got[20] != 0 || got[21] != 0 || got[22] != 0x64 {
			t.Fatalf("%s: nat/mtu/char_attr = % X want nat=01 mtu=00020000 char_attr=64", tc.name, got[17:23])
		}
	}
}

// 第 1 组的"活路径"对照：默认档（include_self=off）下，我们真正发出去的 op11
// 与厂商夹具**同形**（23B、count=1、只含对端、端口网络序），差别只在 relay 端口/账号等取值。
func TestVendorOp11ShapeOnLiveSnapshotPath(t *testing.T) {
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	state := runtimePartyStateSnapshot(leader)
	writeFrames(t, leader)
	writeFrames(t, member)

	if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
		t.Fatal(err)
	}
	frames := writeFrames(t, leader)
	ids := frameMessageIDs(frames)
	if len(ids) < 3 || ids[0] != upperMsgPeerEndpointsID || ids[1] != upperMsgRealtimeInfoFrameID || ids[2] != upperMsgOp9RosterID {
		t.Fatalf("vendor order frames = %v, want [0x0b 0x99 0x09 ...]", ids)
	}
	endpoints := peerEndpointFrameBodies(frames)
	if len(endpoints) != 1 {
		t.Fatalf("op11 frames = %d want 1", len(endpoints))
	}
	body := endpoints[0]
	if len(body) != 1+currentPartyPeerEndpointSize || body[0] != 1 {
		t.Fatalf("leader op11 body = % X, want count=1 + one 22B peer row (vendor shape)", body)
	}
	if uid := uint16(body[1]) | uint16(body[2])<<8; uid != 1002 {
		t.Fatalf("leader op11 only row uid=%d want the peer 1002 (self 1001 must be absent)", uid)
	}
	if bytes.Contains(body[1:], []byte{0xe9, 0x03}) {
		t.Fatalf("leader op11 still carries its own char id 1001 (0x03e9) as a row: % X", body)
	}

	// 另一个收件人：同一场 formation，它收到的那一行是自己的对端（队长 1001）。
	if err := service.sendRuntimePartySnapshot(member, state); err != nil {
		t.Fatal(err)
	}
	memberFrames := writeFrames(t, member)
	memberEndpoints := peerEndpointFrameBodies(memberFrames)
	if len(memberEndpoints) != 1 || len(memberEndpoints[0]) != 1+currentPartyPeerEndpointSize {
		t.Fatalf("member op11 = % X, want one 23B body", memberEndpoints)
	}
	if uid := uint16(memberEndpoints[0][1]) | uint16(memberEndpoints[0][2])<<8; uid != 1001 {
		t.Fatalf("member op11 only row uid=%d want the peer 1001", uid)
	}
}

// 第 3 组：include_self 两档的行集合差异（on = 逐字节回到我们改动前的形状）。
func TestIncludeSelfOnRestoresLegacyRowsByteForByte(t *testing.T) {
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	state := runtimePartyStateSnapshot(leader)

	// 默认档（off）：只含对端 = 1 行。
	off := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if len(off) != 1+currentPartyPeerEndpointSize || off[0] != 1 {
		t.Fatalf("include_self=off body = % X, want count=1 (peer only)", off)
	}

	// 回退档（on）：自己那一行 + 对端那一行 = 2 行、45B，且**前 22B 就是自己**。
	service.options.partyPeerEndpointIncludeSelf = partyPeerEndpointIncludeSelfOn
	on := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if len(on) != 1+2*currentPartyPeerEndpointSize || on[0] != 2 {
		t.Fatalf("include_self=on body = % X, want count=2 (45B legacy shape)", on)
	}
	if uid := uint16(on[1]) | uint16(on[2])<<8; uid != 1001 {
		t.Fatalf("include_self=on first row uid=%d want the receiver 1001", uid)
	}
	// on 档 = 自己那一行 + off 档那一行（逐字节）：这是"只多一条自己"的钉子。
	if !bytes.Equal(on[1+currentPartyPeerEndpointSize:], off[1:]) {
		t.Fatalf("include_self=on peer row = % X, want the off-mode row % X",
			on[1+currentPartyPeerEndpointSize:], off[1:])
	}

	// 未知收包者（receiver 为 nil / 未选角）时两档都必须退回"全员行"，不能把队伍发空。
	if got := service.buildRuntimePartyPeerEndpointInfoForReceiver(nil, state); len(got) != 1+2*currentPartyPeerEndpointSize {
		t.Fatalf("nil receiver body = % X, want the full 2-row form", got)
	}
}

// 第 3 组补充：队伍里只有收包者一个人时，默认档给出 count=0 的 body 并留一条可判读事件
// （厂商日志里没有单人样本，这是唯一现场判据；回退开关 = include_self=on）。
func TestIncludeSelfOffSoloPartyEmitsEmptyRosterWithEvent(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	solo := alignedcmd.PartyState{
		PartyID:    91,
		UserID:     1001,
		IsLeader:   true,
		MaxMembers: 4,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	body := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, solo)
	if !bytes.Equal(body, []byte{0}) {
		t.Fatalf("solo party op11 body = % X, want 00 (zero peer rows)", body)
	}
	if _, err := service.sendRuntimePartyPeerEndpointInfo(leader, solo, service.partyFormationOrder()); err != nil {
		t.Fatal(err)
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logged, "game-runtime-party-peer-endpoints-self-only") {
		t.Fatalf("solo party did not leave the判读 event:\n%s", logged)
	}
	if !strings.Contains(logged, "peer_count=0") || !strings.Contains(logged, "include_self=off") {
		t.Fatalf("solo party event is missing its judgement fields:\n%s", logged)
	}
}

// 第 2 组：三档 formation 顺序。
//
//	vendor      （默认）= op11 → op153 → op9
//	vendor_wire         = op11 → op9 → op153（厂商日志实际字节序）
//	legacy              = op153 → op11 → op9（逐字节回退）
func TestFormationOrderModesEmitTheirExactFrameOrder(t *testing.T) {
	cases := []struct {
		mode  partyFormationOrderMode
		name  string
		ids   []uint16
		order string
	}{
		{
			mode:  partyFormationOrderVendor,
			name:  "vendor",
			ids:   []uint16{upperMsgPeerEndpointsID, upperMsgRealtimeInfoFrameID, upperMsgOp9RosterID},
			order: "ordering=all_members_op11_barrier_before_any_op9_then_op153",
		},
		{
			mode:  partyFormationOrderVendorWire,
			name:  "vendor_wire",
			ids:   []uint16{upperMsgPeerEndpointsID, upperMsgOp9RosterID, upperMsgRealtimeInfoFrameID},
			order: "ordering=all_members_op11_barrier_before_any_op9_then_op153",
		},
		{
			mode:  partyFormationOrderLegacy,
			name:  "legacy",
			ids:   []uint16{upperMsgRealtimeInfoFrameID, upperMsgPeerEndpointsID, upperMsgOp9RosterID},
			order: "ordering=op153_then_op11_before_op9",
		},
	}
	for _, tc := range cases {
		logger, logPath := newPeerEndpointTestLogger(t)
		service, leader, _, _, _ := changeHostFixture(t, 10, 10)
		service.packetLog = logger
		service.options.partyFormationOrder = tc.mode
		state := runtimePartyStateSnapshot(leader)
		writeFrames(t, leader)

		if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		frames := writeFrames(t, leader)
		ids := frameMessageIDs(frames)
		// 只比较前 3 帧：之后可能还有"同区域队友那条 op9"等扇出帧。
		if len(ids) < 3 {
			t.Fatalf("%s: frames=%v want at least 3", tc.name, ids)
		}
		if ids[0] != tc.ids[0] || ids[1] != tc.ids[1] || ids[2] != tc.ids[2] {
			t.Fatalf("%s: frame order = %v want %v", tc.name, ids[:3], tc.ids)
		}
		// op11 恰好一条（M1 默认 off）。
		if got := len(peerEndpointFrameBodies(frames)); got != 1 {
			t.Fatalf("%s: op11 frames = %d want 1", tc.name, got)
		}
		logged := readPeerEndpointTestLog(t, logger, logPath)
		if !strings.Contains(logged, tc.order) {
			t.Fatalf("%s: log is missing %q:\n%s", tc.name, tc.order, logged)
		}
		if !strings.Contains(logged, "game-runtime-party-realtime-info-send") {
			t.Fatalf("%s: op153 send event missing (needed to judge the op153/op9 order):\n%s", tc.name, logged)
		}
		if tc.mode.usesVendorPhaseLabels() {
			for _, want := range []string{
				"source=managed_runtime_party_snapshot_phase1",
				"source=managed_runtime_party_snapshot_phase2",
				"peer_count=1",
				"include_self=off",
			} {
				if !strings.Contains(logged, want) {
					t.Fatalf("%s: vendor phase log is missing %q:\n%s", tc.name, want, logged)
				}
			}
		} else if strings.Contains(logged, "managed_runtime_party_snapshot_phase1") {
			t.Fatalf("legacy mode must not use the vendor phase labels:\n%s", logged)
		}
	}
}

// 第 2 组补充：vendor 档 + M1 repeat 时，第二次 op11 仍在 op9 之后，且日志串是
// vendor 顺序串加 `_then_op11_after_op9`。
func TestFormationOrderVendorKeepsAfterRosterRepeatAfterOp9(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	service.options.partyPeerEndpointsAfterRoster = partyPeerEndpointsAfterRosterRepeat
	state := runtimePartyStateSnapshot(leader)
	writeFrames(t, leader)

	if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
		t.Fatal(err)
	}
	ids := frameMessageIDs(writeFrames(t, leader))
	if len(ids) < 4 ||
		ids[0] != upperMsgPeerEndpointsID ||
		ids[1] != upperMsgRealtimeInfoFrameID ||
		ids[2] != upperMsgOp9RosterID ||
		ids[3] != upperMsgPeerEndpointsID {
		t.Fatalf("vendor+repeat frame order = %v, want [0x0b 0x99 0x09 0x0b ...]", ids)
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"ordering=all_members_op11_barrier_before_any_op9_then_op153_then_op11_after_op9",
		"body_matches_bootstrap=true",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("vendor+repeat log is missing %q:\n%s", want, logged)
		}
	}
}

// 第 2 组补充：legacy 档（配合 include_self=on）必须**逐字节**保持改动前的 op11 body
// （含自己那一行、45B）。三个开关各自独立：完整回退 = order=legacy + include_self=on
// + port_endian=le。
func TestFormationOrderLegacyKeepsLegacyOp11Bytes(t *testing.T) {
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.options.partyFormationOrder = partyFormationOrderLegacy
	service.options.partyPeerEndpointIncludeSelf = partyPeerEndpointIncludeSelfOn
	state := runtimePartyStateSnapshot(leader)
	writeFrames(t, leader)

	if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
		t.Fatal(err)
	}
	frames := writeFrames(t, leader)
	endpoints := peerEndpointFrameBodies(frames)
	if len(endpoints) != 1 {
		t.Fatalf("legacy op11 frames = %d want 1", len(endpoints))
	}
	body := endpoints[0]
	if len(body) != 1+2*currentPartyPeerEndpointSize || body[0] != 2 {
		t.Fatalf("legacy op11 body = % X, want the pre-change 45B/count=2 shape", body)
	}
	// 改动前的顺序是 `op153 → op11 → op9`。
	ids := frameMessageIDs(frames)
	if ids[0] != upperMsgRealtimeInfoFrameID || ids[1] != upperMsgPeerEndpointsID || ids[2] != upperMsgOp9RosterID {
		t.Fatalf("legacy frame order = %v, want [0x99 0x0b 0x09 ...]", ids)
	}
}

// 第 2 组补充：中继路径（生产形态）下的 vendor 形状：
// op11 只有对端一行，端口是该腿的中继端口、按**网络序**写；本机 IP 是 relay IP。
func TestVendorOp11RelayLegShapeCarriesRelayEndpointWithoutSelfRow(t *testing.T) {
	service, leader, _ := vendorRelayFixture(t)
	state := runtimePartyStateSnapshot(leader)
	relay := service.currentPartyUDPRelay()
	address, port, ok := relay.Endpoint(uint16(state.PartyID), 1001, 1002)
	if !ok {
		t.Fatal("relay leg 1001->1002 missing")
	}
	body := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if len(body) != 1+currentPartyPeerEndpointSize || body[0] != 1 {
		t.Fatalf("relay op11 body = % X, want count=1 (peer only)", body)
	}
	if uid := uint16(body[1]) | uint16(body[2])<<8; uid != 1002 {
		t.Fatalf("relay op11 row uid=%d want the peer 1002", uid)
	}
	if !bytes.Equal(body[3:7], address[:]) || !bytes.Equal(body[7:11], address[:]) {
		t.Fatalf("relay op11 inner/outer = % X/% X want relay ip % X", body[3:7], body[7:11], address)
	}
	if got := uint16(body[11])<<8 | uint16(body[12]); got != port {
		t.Fatalf("relay op11 port = %d want %d (network order)", got, port)
	}
	if body[11] == byte(port) && body[12] == byte(port>>8) && port > 0xff {
		t.Fatalf("relay op11 port bytes = % X look little endian, want network order", body[11:13])
	}
}

// 第 4 组：开关组合矩阵（endian × include_self × order）不 panic、不报错，
// 且无论哪个组合，每次 formation 的 op11 都**恰好一条**、body 长度与 include_self 档位一致。
func TestPartyFormationSwitchMatrixDoesNotPanic(t *testing.T) {
	endians := []partyPeerPortEndianMode{partyPeerPortEndianBig, partyPeerPortEndianLittle}
	includeSelves := []partyPeerEndpointIncludeSelfMode{partyPeerEndpointIncludeSelfOff, partyPeerEndpointIncludeSelfOn}
	orders := []partyFormationOrderMode{
		partyFormationOrderVendor,
		partyFormationOrderVendorWire,
		partyFormationOrderLegacy,
	}
	cases := 0
	for _, endian := range endians {
		for _, includeSelf := range includeSelves {
			for _, order := range orders {
				cases++
				name := endian.String() + "/" + includeSelf.String() + "/" + order.String()
				t.Run(name, func(t *testing.T) {
					service, leader, member, _, _ := changeHostFixture(t, 10, 10)
					service.options.partyPeerPortEndian = endian
					service.options.partyPeerEndpointIncludeSelf = includeSelf
					service.options.partyFormationOrder = order
					state := runtimePartyStateSnapshot(leader)

					// CS op2 上报在两个字节序档位下都必须被接受（读法与形状无关）。
					sendCapturedPeerEndpointRegistration(t, service, leader, 20484)
					sendCapturedPeerEndpointRegistration(t, service, member, 21000)
					writeFrames(t, leader)
					writeFrames(t, member)

					if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
						t.Fatalf("snapshot: %v", err)
					}
					endpoints := peerEndpointFrameBodies(writeFrames(t, leader))
					if len(endpoints) != 1 {
						t.Fatalf("op11 frames = %d want exactly one", len(endpoints))
					}
					wantRows := 1
					if includeSelf.includesSelf() {
						wantRows = 2
					}
					if len(endpoints[0]) != 1+wantRows*currentPartyPeerEndpointSize || int(endpoints[0][0]) != wantRows {
						t.Fatalf("op11 body = % X, want count=%d (%d rows)",
							endpoints[0], wantRows, wantRows)
					}
				})
			}
		}
	}
	if cases != 12 {
		t.Fatalf("matrix cases = %d want 12", cases)
	}
}

// 第 4 组补充：三个开关的**默认档**必须就是厂商形状（这是本轮的验收口径）。
func TestPartyFormationVendorDefaults(t *testing.T) {
	var probe *Service
	if mode := probe.partyPeerPortEndian(); mode != partyPeerPortEndianBig {
		t.Fatalf("default port endian = %v want be", mode)
	}
	if mode := probe.partyPeerEndpointIncludeSelf(); mode != partyPeerEndpointIncludeSelfOff {
		t.Fatalf("default include_self = %v want off", mode)
	}
	if mode := probe.partyFormationOrder(); mode != partyFormationOrderVendor {
		t.Fatalf("default formation order = %v want vendor", mode)
	}
	if got := readPartyPeerPortEndianMode(""); got != partyPeerPortEndianBig {
		t.Fatalf("empty env port endian = %v want be", got)
	}
	if got := readPartyPeerEndpointIncludeSelfMode(""); got != partyPeerEndpointIncludeSelfOff {
		t.Fatalf("empty env include_self = %v want off", got)
	}
	if got := readPartyFormationOrderMode(""); got != partyFormationOrderVendor {
		t.Fatalf("empty env formation order = %v want vendor", got)
	}
	// 非法值也回到默认档（不允许"写错就换档"）。
	if got := readPartyFormationOrderMode(" VENDOR_WIRE "); got != partyFormationOrderVendorWire {
		t.Fatalf("trimmed/case-folded vendor_wire = %v want vendor_wire", got)
	}
	if got := readPartyFormationOrderMode("nonsense"); got != partyFormationOrderVendor {
		t.Fatalf("invalid formation order = %v want vendor", got)
	}
	if got := readPartyPeerEndpointIncludeSelfMode("yes"); got != partyPeerEndpointIncludeSelfOff {
		t.Fatalf("invalid include_self = %v want off（只有显式 on 才回到旧形状）", got)
	}
	if got := readPartyPeerPortEndianMode("LE"); got != partyPeerPortEndianLittle {
		t.Fatalf("LE = %v want le", got)
	}
}

// vendorRelayFixture 造一个**真的中继房**（两个 127.0.0.1 腿）+ 两个带 TCP 远端地址的会话，
// 让 `syncRuntimePartyUDPRelay` 通过，从而走到生产形态的中继 op11 组装。
func vendorRelayFixture(t *testing.T) (*Service, *gameSession, *gameSession) {
	t.Helper()
	service, leader, member, leaderGeneration, memberGeneration := changeHostFixture(t, 10, 10)
	leader.conn = &tcpAddrBufferConn{
		bufferConn: bufferConn{},
		addr:       &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 41001},
	}
	member.conn = &tcpAddrBufferConn{
		bufferConn: bufferConn{},
		addr:       &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 41002},
	}
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	state := runtimePartyStateSnapshot(leader)
	if state.PartyID <= 0 {
		t.Fatalf("fixture party state = %+v", state)
	}
	if !relay.Sync(uint16(state.PartyID), []currentPartyUDPRelayBinding{
		{characterID: 1001, generation: leaderGeneration, address: [4]byte{127, 0, 0, 1}},
		{characterID: 1002, generation: memberGeneration, address: [4]byte{127, 0, 0, 1}},
	}) {
		t.Fatal("relay refused the fixture room")
	}
	service.mu.Lock()
	service.partyUDPRelay = relay
	service.mu.Unlock()
	if !service.syncRuntimePartyUDPRelay(state) {
		t.Fatal("service did not adopt the fixture relay room")
	}
	return service, leader, member
}

// tcpAddrBufferConn 让会话的 TCP 远端地址是真实的 `*net.TCPAddr`
// （`syncRuntimePartyUDPRelay` 要求每个成员都有可观察的 IPv4 会话地址）。
type tcpAddrBufferConn struct {
	bufferConn
	addr *net.TCPAddr
}

func (c *tcpAddrBufferConn) RemoteAddr() net.Addr { return c.addr }
