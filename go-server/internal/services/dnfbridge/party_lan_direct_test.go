package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件钉住 `DNFBRIDGE_PARTY_LAN_DIRECT`（同局域网直连档，见 party_lan_direct.go）：
//
//  1. `Default…`：默认档（含零值 options）**逐字节等于改动前**的中继路径，
//     并且显式 `relay` 与默认档完全一致（A/B 回退钉子）；
//  2. `SameLAN…`：两端自报 inner 相同 + 服务端观测到的 outer 相同 ⇒ 对端行写
//     **inner 地址 + 对端自报端口**（客户端因此不经中继直连）；
//  3. `RejectsDifferentObservedOuter…`（**最重要**）：自报 inner 相同、但服务端观测到的
//     两端 outer 不同 ⇒ **绝不**当同局域网（假阳性护栏），原样回退中继端点；
//  4. `RejectsUnusableInner…` / `RejectsUnregisteredPeer…`：inner 缺失/回环/组播、
//     或对端根本没上报过端点 ⇒ 回退中继；
//  5. 每个非直连场景都断言"拿到的就是中继端点那一对地址+端口"（回落没有被弱化）。

// 现场形态的自报值：两台客户端 inner 相同（同局域网/同机双开）。
var (
	lanFixtureInner     = [4]byte{192, 168, 0, 101}
	lanFixtureOtherLAN  = [4]byte{192, 168, 1, 2}
	lanFixtureReported  = [4]byte{38, 22, 90, 52} // 客户端自报的 outer（公网/加速器地址，与本档判定**无关**）
	lanFixtureObservedA = net.IPv4(203, 0, 113, 7)
	lanFixtureObservedB = net.IPv4(203, 0, 113, 9)
)

const (
	lanFixtureLeaderPort = 61694
	lanFixtureMemberPort = 61566
)

// 现场证据里的两个端口（`game-party-peer-endpoint-registered`）：61694 / 61566。

// lanDirectFixture 造一个**真的中继房**（两个已绑定角色的会话 + 可注入的 TCP 观测地址 +
// 可注入的 CS op2 自报端点），让 `buildRuntimePartyPeerEndpointInfoForReceiver` 走生产形态的
// 中继路径。port=0 表示该会话**不上报**端点（对端/自己都当"未上报"处理）。
func lanDirectFixture(
	t *testing.T,
	leaderInner, memberInner, reportedOuter [4]byte,
	leaderPort, memberPort uint16,
	leaderObserved, memberObserved net.IP,
) (*Service, *gameSession, *gameSession) {
	t.Helper()
	service, leader, member, leaderGeneration, memberGeneration := changeHostFixture(t, 10, 10)
	leader.conn = &tcpAddrBufferConn{
		bufferConn: bufferConn{},
		addr:       &net.TCPAddr{IP: leaderObserved, Port: 41001},
	}
	member.conn = &tcpAddrBufferConn{
		bufferConn: bufferConn{},
		addr:       &net.TCPAddr{IP: memberObserved, Port: 41002},
	}
	if leaderPort != 0 {
		if !service.captureCurrentPartyPeerEndpointRegistration(
			leader, buildLANPeerEndpointRegistrationBody(t, leaderInner, reportedOuter, leaderPort)) {
			t.Fatalf("leader CS op2 port %d was not accepted", leaderPort)
		}
	}
	if memberPort != 0 {
		if !service.captureCurrentPartyPeerEndpointRegistration(
			member, buildLANPeerEndpointRegistrationBody(t, memberInner, reportedOuter, memberPort)) {
			t.Fatalf("member CS op2 port %d was not accepted", memberPort)
		}
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

// buildLANPeerEndpointRegistrationBody 造一份 31B CS op2，inner/outer/端口由调用方给出
// （形状与 `buildCapturedPeerEndpointRegistrationBody` 相同；本档需要**能把 inner 与
// 服务端观测到的 outer 分开控制**，所以另起一个）。
func buildLANPeerEndpointRegistrationBody(
	t *testing.T,
	inner, outer [4]byte,
	port uint16,
) []byte {
	t.Helper()
	body := make([]byte, currentPartyPeerRegistrationBodySize)
	body[0] = 5
	copy(body[1:5], inner[:])
	copy(body[5:9], outer[:])
	binary.BigEndian.PutUint16(body[9:11], port)
	binary.LittleEndian.PutUint32(body[11:15], 1472)
	binary.LittleEndian.PutUint32(body[15:19], 12)
	copy(body[19:], []byte("00ff211ef879"))
	return body
}

// lanPeerRow 把一条 22B 对端行逐字段读回来（行布局由厂商夹具钉死，本档一个字节都不改）。
type lanPeerRow struct {
	uid      uint16
	inner    [4]byte
	outer    [4]byte
	port     uint16
	account  uint32
	nat      byte
	mtu      uint32
	charAttr byte
}

func mustLanPeerRow(t *testing.T, body []byte, index int) lanPeerRow {
	t.Helper()
	if len(body) < 1+(index+1)*currentPartyPeerEndpointSize {
		t.Fatalf("op11 body too short for row %d: % X", index, body)
	}
	if count := int(body[0]); count <= index {
		t.Fatalf("op11 body count=%d, cannot read row %d: % X", count, index, body)
	}
	row := body[1+index*currentPartyPeerEndpointSize:]
	parsed := lanPeerRow{
		uid:      uint16(row[0]) | uint16(row[1])<<8,
		port:     uint16(row[10])<<8 | uint16(row[11]),
		account:  binary.LittleEndian.Uint32(row[12:16]),
		nat:      row[16],
		mtu:      binary.LittleEndian.Uint32(row[17:21]),
		charAttr: row[21],
	}
	copy(parsed.inner[:], row[2:6])
	copy(parsed.outer[:], row[6:10])
	return parsed
}

// relayEndpointBytes 是"改动前那一份 op11 body"的独立参考实现：直接把 `relay.Endpoint`
// 给出的地址+端口交给**未被本档改动的** `buildPartyPeerEndpointInfoBody`。
func relayEndpointBytes(t *testing.T, service *Service, partyID, owner, peer uint16) ([]byte, [4]byte, uint16) {
	t.Helper()
	address, port, ok := service.currentPartyUDPRelay().Endpoint(partyID, owner, peer)
	if !ok {
		t.Fatalf("relay leg %d->%d missing", owner, peer)
	}
	body := service.buildPartyPeerEndpointInfoBody([]party.PeerEndpoint{
		{UserID: peer, IPv4: address, Port: port, AccountID: uint32(peer)},
	})
	return body, address, port
}

// 1) 默认档 = `relay`：默认（含显式零值 options）与显式 `relay` 逐字节一致，
// 且与**改动前**的中继路径（relay.Endpoint → 未改动的 body builder）逐字节一致；
// legacy 四字段档（逐字节回退档）下再对一次，确保"本档关着就一个字节都不动"。
func TestPartyLANDirectDefaultModeIsByteIdenticalToRelayPath(t *testing.T) {
	// 夹具故意**满足**同局域网条件：即使如此，默认档也必须发中继端点。
	service, leader, member := lanDirectFixture(t, lanFixtureInner, lanFixtureInner, lanFixtureReported,
		lanFixtureLeaderPort, lanFixtureMemberPort, lanFixtureObservedA, lanFixtureObservedA)
	state := runtimePartyStateSnapshot(leader)

	want, relayAddress, relayPort := relayEndpointBytes(t, service, uint16(state.PartyID), 1001, 1002)

	// 默认档（零值 options 就是 relay）。
	if got := service.partyLANDirect(); got != partyLANDirectRelay {
		t.Fatalf("zero-value options lan_direct = %v want relay", got)
	}
	defaultBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if !bytes.Equal(defaultBody, want) {
		t.Fatalf("default-mode op11 body = % X\nwant pre-change relay body % X", defaultBody, want)
	}

	// 显式 relay 档（线上回退值）与默认档逐字节相同。
	service.options.partyLANDirect = partyLANDirectRelay
	explicit := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if !bytes.Equal(explicit, defaultBody) {
		t.Fatalf("explicit relay body = % X, want byte-identical to default % X", explicit, defaultBody)
	}

	row := mustLanPeerRow(t, defaultBody, 0)
	if row.uid != 1002 {
		t.Fatalf("row uid = %d want the peer 1002", row.uid)
	}
	if row.inner != relayAddress || row.outer != relayAddress {
		t.Fatalf("row inner/outer = % X/% X want the relay address % X", row.inner, row.outer, relayAddress)
	}
	if row.port != relayPort {
		t.Fatalf("row port = %d want the relay leg port %d", row.port, relayPort)
	}
	if row.port == lanFixtureMemberPort {
		t.Fatal("default mode advertised the client-registered port, it must advertise the relay leg port")
	}

	// 收包者是队员那一侧同样成立（方向无关）。
	memberWant, memberRelayAddress, memberRelayPort := relayEndpointBytes(t, service, uint16(state.PartyID), 1002, 1001)
	if got := service.buildRuntimePartyPeerEndpointInfoForReceiver(member, state); !bytes.Equal(got, memberWant) {
		t.Fatalf("member-side relay body = % X\nwant % X (relay % X:%d)",
			got, memberWant, memberRelayAddress, memberRelayPort)
	}

	// legacy 四字段档（本仓库的"逐字节回到改动前"档位）下，本档关闭必须仍然等于
	// 未被改动的 `party.BuildPeerEndpointInfo`。
	service.options.partyPeerRowVendorFields = partyPeerRowRowFieldsLegacy
	legacyWant := party.BuildPeerEndpointInfo([]party.PeerEndpoint{
		{UserID: 1002, IPv4: relayAddress, Port: relayPort, AccountID: 1002},
	})
	if got := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state); !bytes.Equal(got, legacyWant) {
		t.Fatalf("legacy-fields body = % X\nwant party.BuildPeerEndpointInfo % X", got, legacyWant)
	}
	// 同一 legacy 档下把本档打开：这个夹具满足同局域网条件 ⇒ 必须与上面不同（否则本档是死代码）。
	service.options.partyLANDirect = partyLANDirectLAN
	if got := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state); bytes.Equal(got, legacyWant) {
		t.Fatal("lan mode produced the relay bytes on a same-LAN fixture (the capability is not wired)")
	}
}

// 2) 同局域网：两端自报 inner 相同 + 服务端观测到的 outer 相同 ⇒ 对端行写 inner + 对端
// 自报端口，两个方向都如此（各拿各的对端端口），且**不是**中继端点。
func TestPartyLANDirectAdvertisesInnerAddressForSameLANParty(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, member := lanDirectFixture(t, lanFixtureInner, lanFixtureInner, lanFixtureReported,
		lanFixtureLeaderPort, lanFixtureMemberPort, lanFixtureObservedA, lanFixtureObservedA)
	service.packetLog = logger
	service.options.partyLANDirect = partyLANDirectLAN
	state := runtimePartyStateSnapshot(leader)

	leaderBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	leaderRow := mustLanPeerRow(t, leaderBody, 0)
	if leaderRow.uid != 1002 {
		t.Fatalf("leader row uid = %d want 1002", leaderRow.uid)
	}
	if leaderRow.inner != lanFixtureInner || leaderRow.outer != lanFixtureInner {
		t.Fatalf("leader-seen peer inner/outer = % X/% X want the LAN address % X in both fields",
			leaderRow.inner, leaderRow.outer, lanFixtureInner)
	}
	if leaderRow.port != lanFixtureMemberPort {
		t.Fatalf("leader-seen peer port = %d want the peer's registered port %d",
			leaderRow.port, lanFixtureMemberPort)
	}

	memberBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(member, state)
	memberRow := mustLanPeerRow(t, memberBody, 0)
	if memberRow.uid != 1001 {
		t.Fatalf("member row uid = %d want 1001", memberRow.uid)
	}
	if memberRow.inner != lanFixtureInner || memberRow.outer != lanFixtureInner {
		t.Fatalf("member-seen peer inner/outer = % X/% X want % X twice",
			memberRow.inner, memberRow.outer, lanFixtureInner)
	}
	if memberRow.port != lanFixtureLeaderPort {
		t.Fatalf("member-seen peer port = %d want the peer's registered port %d",
			memberRow.port, lanFixtureLeaderPort)
	}

	// 端口仍按**网络序**写在 11/12 字节（行布局与字节序一个字节都没改）。
	if leaderBody[11] != byte(lanFixtureMemberPort>>8) || leaderBody[12] != byte(lanFixtureMemberPort&0xff) {
		t.Fatalf("port bytes = % X want big endian %02X %02X",
			leaderBody[11:13], byte(lanFixtureMemberPort>>8), byte(lanFixtureMemberPort&0xff))
	}
	// 行形状/厂商四字段仍是既有的那一份（nat=1 / mtu=512 / char_attr=0x64）。
	if leaderRow.nat != vendorPartyPeerRowNat ||
		leaderRow.mtu != vendorPartyPeerRowMTU ||
		leaderRow.charAttr != vendorPartyPeerRowCharAttr {
		t.Fatalf("row vendor fields = nat=%d mtu=%d char_attr=%#x want %d/%d/%#x",
			leaderRow.nat, leaderRow.mtu, leaderRow.charAttr,
			vendorPartyPeerRowNat, vendorPartyPeerRowMTU, vendorPartyPeerRowCharAttr)
	}

	// 另一条出口（include_self=on + self_endpoint=reported 的"混合组装"档）也必须拿到
	// 同一份直连字节：第 0 行是自己、第 1 行仍然是对端的 LAN 地址+自报端口。
	service.options.partyPeerEndpointIncludeSelf = partyPeerEndpointIncludeSelfOn
	service.options.partyPeerSelfEndpoint = partyPeerSelfEndpointReported
	mixedBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if len(mixedBody) != 1+2*currentPartyPeerEndpointSize || mixedBody[0] != 2 {
		t.Fatalf("include_self=on body = % X, want the 45B/two-row form", mixedBody)
	}
	if self := mustLanPeerRow(t, mixedBody, 0); self.uid != 1001 {
		t.Fatalf("mixed self row uid = %d want the receiver 1001", self.uid)
	}
	mixedRow := mustLanPeerRow(t, mixedBody, 1)
	if mixedRow.uid != 1002 || mixedRow.inner != lanFixtureInner || mixedRow.port != lanFixtureMemberPort {
		t.Fatalf("mixed peer row = uid=%d % X:%d want 1002 % X:%d",
			mixedRow.uid, mixedRow.inner, mixedRow.port, lanFixtureInner, lanFixtureMemberPort)
	}

	// 每一条被改写的行留一条可判读事件（含 inner 与观测到的 outer）。
	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"game-party-peer-endpoint-lan-direct",
		"inner_ipv4=192.168.0.101",
		"observed_outer_ipv4=203.0.113.7",
		"udp_port=61566",
		"port_endian=be",
		"env=DNFBRIDGE_PARTY_LAN_DIRECT",
		"mode=lan",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("lan-direct event is missing %q:\n%s", want, logged)
		}
	}
}

// 3) **最重要的假阳性护栏**：两端自报 inner 完全相同（两家不同 NAT 后面都常报
// 192.168.1.2），但服务端**实际观测到的** outer 不同 ⇒ 绝不当同局域网，原样回退中继。
func TestPartyLANDirectRejectsSameInnerWithDifferentObservedOuter(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, member := lanDirectFixture(t, lanFixtureOtherLAN, lanFixtureOtherLAN, lanFixtureReported,
		lanFixtureLeaderPort, lanFixtureMemberPort, lanFixtureObservedA, lanFixtureObservedB)
	service.packetLog = logger
	service.options.partyLANDirect = partyLANDirectLAN
	state := runtimePartyStateSnapshot(leader)

	// 前置：两端自报 inner 确实相同（否则这个用例测的不是护栏那一条）。
	leaderRegistration := currentPartyPeerEndpointSnapshot(leader)
	memberRegistration := currentPartyPeerEndpointSnapshot(member)
	if leaderRegistration.InnerIPv4 != memberRegistration.InnerIPv4 {
		t.Fatalf("fixture inner addresses differ: %v vs %v", leaderRegistration.InnerIPv4, memberRegistration.InnerIPv4)
	}
	if leaderRegistration.InnerIPv4 != lanFixtureOtherLAN {
		t.Fatalf("fixture inner = %v want %v", leaderRegistration.InnerIPv4, lanFixtureOtherLAN)
	}
	// 客户端自报的 outer 两份**完全相同**：护栏只能来自服务端观测到的地址。
	if leaderRegistration.OuterIPv4 != memberRegistration.OuterIPv4 {
		t.Fatal("fixture self-reported outer addresses must be equal, so the guard is the observed one")
	}

	leaderWant, relayAddress, relayPort := relayEndpointBytes(t, service, uint16(state.PartyID), 1001, 1002)
	leaderBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if !bytes.Equal(leaderBody, leaderWant) {
		t.Fatalf("leader op11 body = % X\nwant the relay fallback % X", leaderBody, leaderWant)
	}
	leaderRow := mustLanPeerRow(t, leaderBody, 0)
	if leaderRow.inner == lanFixtureOtherLAN || leaderRow.port == lanFixtureMemberPort {
		t.Fatalf("different observed outer addresses were treated as same-LAN: inner=% X port=%d (relay % X:%d)",
			leaderRow.inner, leaderRow.port, relayAddress, relayPort)
	}
	if leaderRow.inner != relayAddress || leaderRow.port != relayPort {
		t.Fatalf("row = % X:%d want the relay endpoint % X:%d", leaderRow.inner, leaderRow.port, relayAddress, relayPort)
	}

	memberWant, memberRelayAddress, memberRelayPort := relayEndpointBytes(t, service, uint16(state.PartyID), 1002, 1001)
	memberBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(member, state)
	if !bytes.Equal(memberBody, memberWant) {
		t.Fatalf("member op11 body = % X\nwant the relay fallback % X", memberBody, memberWant)
	}
	memberRow := mustLanPeerRow(t, memberBody, 0)
	if memberRow.inner != memberRelayAddress || memberRow.port != memberRelayPort {
		t.Fatalf("member row = % X:%d want the relay endpoint % X:%d",
			memberRow.inner, memberRow.port, memberRelayAddress, memberRelayPort)
	}

	logged := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logged, "game-party-peer-endpoint-lan-direct") {
		t.Fatalf("the guard let a cross-NAT pair through and logged a lan-direct row:\n%s", logged)
	}
}

// 4) 每个非直连场景都必须**原样**回退中继端点（含"没有可观测 IPv4"这条）。
func TestPartyLANDirectFallsBackToRelayInEveryNonSameLANCase(t *testing.T) {
	cases := []struct {
		name           string
		leaderInner    [4]byte
		memberInner    [4]byte
		leaderPort     uint16
		memberPort     uint16
		leaderObserved net.IP
		memberObserved net.IP
		// injectPeerRegistration 覆盖"对端自报过 inner 但端口为 0"这种只能白盒构造的形状。
		injectPeerRegistration bool
	}{
		{
			name: "one_side_missing_inner", leaderInner: [4]byte{}, memberInner: lanFixtureInner,
			leaderPort: lanFixtureLeaderPort, memberPort: lanFixtureMemberPort,
			leaderObserved: lanFixtureObservedA, memberObserved: lanFixtureObservedA,
		},
		{
			name: "peer_missing_inner", leaderInner: lanFixtureInner, memberInner: [4]byte{},
			leaderPort: lanFixtureLeaderPort, memberPort: lanFixtureMemberPort,
			leaderObserved: lanFixtureObservedA, memberObserved: lanFixtureObservedA,
		},
		{
			name: "both_inner_loopback", leaderInner: [4]byte{127, 0, 0, 1}, memberInner: [4]byte{127, 0, 0, 1},
			leaderPort: lanFixtureLeaderPort, memberPort: lanFixtureMemberPort,
			leaderObserved: lanFixtureObservedA, memberObserved: lanFixtureObservedA,
		},
		{
			name: "both_inner_multicast", leaderInner: [4]byte{224, 0, 0, 5}, memberInner: [4]byte{224, 0, 0, 5},
			leaderPort: lanFixtureLeaderPort, memberPort: lanFixtureMemberPort,
			leaderObserved: lanFixtureObservedA, memberObserved: lanFixtureObservedA,
		},
		{
			name: "different_inner_same_observed_outer", leaderInner: [4]byte{192, 168, 1, 2}, memberInner: [4]byte{192, 168, 1, 3},
			leaderPort: lanFixtureLeaderPort, memberPort: lanFixtureMemberPort,
			leaderObserved: lanFixtureObservedA, memberObserved: lanFixtureObservedA,
		},
		{
			name: "peer_never_reported_an_endpoint", leaderInner: lanFixtureInner, memberInner: lanFixtureInner,
			leaderPort: lanFixtureLeaderPort, memberPort: 0,
			leaderObserved: lanFixtureObservedA, memberObserved: lanFixtureObservedA,
		},
		{
			name: "peer_reported_inner_without_udp_port", leaderInner: lanFixtureInner, memberInner: lanFixtureInner,
			leaderPort: lanFixtureLeaderPort, memberPort: lanFixtureMemberPort,
			leaderObserved: lanFixtureObservedA, memberObserved: lanFixtureObservedA,
			injectPeerRegistration: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, leader, member := lanDirectFixture(t, tc.leaderInner, tc.memberInner, lanFixtureReported,
				tc.leaderPort, tc.memberPort, tc.leaderObserved, tc.memberObserved)
			if tc.injectPeerRegistration {
				// 白盒：inner 已上报但端口为 0（`capture…` 不会存这种形状）⇒ 绝不通告端点。
				member.partyPeerMu.Lock()
				member.partyPeer = currentPartyPeerEndpointRegistration{
					InnerIPv4: tc.memberInner,
					OuterIPv4: lanFixtureReported,
					Port:      0,
				}
				member.partyPeerMu.Unlock()
			}
			service.options.partyLANDirect = partyLANDirectLAN
			state := runtimePartyStateSnapshot(leader)

			want, relayAddress, relayPort := relayEndpointBytes(t, service, uint16(state.PartyID), 1001, 1002)
			body := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
			if !bytes.Equal(body, want) {
				t.Fatalf("op11 body = % X\nwant the untouched relay fallback % X", body, want)
			}
			row := mustLanPeerRow(t, body, 0)
			if row.inner != relayAddress || row.outer != relayAddress || row.port != relayPort {
				t.Fatalf("row = % X/% X:%d want the relay endpoint % X:%d",
					row.inner, row.outer, row.port, relayAddress, relayPort)
			}
			if row.port == tc.memberPort && tc.memberPort != 0 {
				t.Fatalf("row advertised the client port %d instead of the relay leg port %d", row.port, relayPort)
			}
		})
	}
}

// 5) 会话没有可观测 IPv4（测试假 conn 的形状 / 非 TCP 远端）时，本档**绝不**配对；
// 同时"中继不可用 ⇒ 整份回退直连名单"这条既有路径也一个字节都不变。
func TestPartyLANDirectRequiresAnObservedRemoteAddress(t *testing.T) {
	service, leader, member := lanDirectFixture(t, lanFixtureInner, lanFixtureInner, lanFixtureReported,
		lanFixtureLeaderPort, lanFixtureMemberPort, lanFixtureObservedA, lanFixtureObservedA)
	state := runtimePartyStateSnapshot(leader)

	// 对照：正常观测地址下本档确实会命中（证明下面 false 不是别的原因）。
	service.options.partyLANDirect = partyLANDirectLAN
	if _, _, _, ok := service.partyLANDirectPeerEndpoint(leader, 1002); !ok {
		t.Fatal("same-LAN fixture was not paired while both observed addresses were available")
	}

	// 把对端会话换成没有可观测 IPv4 的假 conn ⇒ 判据必须立刻 false（防御性护栏）。
	member.conn = &bufferConn{}
	if _, _, _, ok := service.partyLANDirectPeerEndpoint(leader, 1002); ok {
		t.Fatal("a peer without an observed IPv4 address was paired as same-LAN")
	}

	// 整份快照：中继同步此时对整队返回 false ⇒ 走既有的"整份回退直连名单"分支，
	// 本档在该分支不生效（开关打开与关闭逐字节相同）。
	lanBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	service.options.partyLANDirect = partyLANDirectRelay
	relayBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if !bytes.Equal(lanBody, relayBody) {
		t.Fatalf("relay-unavailable fallback changed with the switch on:\nlan=% X\nrelay=% X", lanBody, relayBody)
	}
	if want := service.buildRuntimePartyPeerEndpointInfoForMembers(state,
		mustPairRowsWithoutReceiver(t, service, state, 1001)); !bytes.Equal(lanBody, want) {
		t.Fatalf("relay-unavailable body = % X\nwant the untouched direct fallback % X", lanBody, want)
	}
}

// mustPairRowsWithoutReceiver 用与生产路径同一个行集合判据（include_self 档）取对端行集合。
func mustPairRowsWithoutReceiver(
	t *testing.T,
	service *Service,
	state alignedcmd.PartyState,
	receiverID uint16,
) []alignedcmd.PartyMemberState {
	t.Helper()
	members, _ := partyPeerEndpointRowsExcludeReceiver(
		runtimePartyMembers(state), receiverID, service.partyPeerEndpointIncludeSelf())
	return members
}

// 6) 档位解析：**默认与非法值都是 relay**（一键回退 = 删掉 env 或写 relay）。
func TestPartyLANDirectModeParsingAndDefaults(t *testing.T) {
	var probe *Service
	if got := probe.partyLANDirect(); got != partyLANDirectRelay {
		t.Fatalf("nil Service lan_direct = %v want relay", got)
	}
	if got := (&Service{}).partyLANDirect(); got != partyLANDirectRelay {
		t.Fatalf("zero-value options lan_direct = %v want relay", got)
	}
	if partyLANDirectRelay.String() != "relay" || partyLANDirectLAN.String() != "lan" {
		t.Fatalf("mode strings = %q/%q want relay/lan",
			partyLANDirectRelay.String(), partyLANDirectLAN.String())
	}
	if partyLANDirectRelay.enabled() || !partyLANDirectLAN.enabled() {
		t.Fatal("enabled() must be true only for the lan mode")
	}
	for _, value := range []string{"", "  ", "relay", "RELAY", "off", "on", "1", "true", "nonsense"} {
		if got := readPartyLANDirectMode(value); got != partyLANDirectRelay {
			t.Fatalf("value %q enabled the LAN direct path; only an explicit lan may", value)
		}
	}
	for _, value := range []string{"lan", "LAN", " lan "} {
		if got := readPartyLANDirectMode(value); got != partyLANDirectLAN {
			t.Fatalf("value %q = %v want lan", value, got)
		}
	}
	if partyLANDirectEnv != "DNFBRIDGE_PARTY_LAN_DIRECT" {
		t.Fatalf("env name = %q want DNFBRIDGE_PARTY_LAN_DIRECT", partyLANDirectEnv)
	}
}

// 6) 纯函数级的安全条件：可用地址判据 + "服务端观测到的地址"来源。
func TestPartyLANUsableIPv4RejectsUnusableAddresses(t *testing.T) {
	usable := [][4]byte{
		{192, 168, 0, 101}, {10, 0, 0, 7}, {172, 16, 3, 4}, {169, 254, 1, 1}, {203, 0, 113, 7},
	}
	for _, address := range usable {
		if !partyLANUsableIPv4(address) {
			t.Fatalf("%v must be usable as a LAN direct address", address)
		}
	}
	unusable := [][4]byte{
		{0, 0, 0, 0}, {127, 0, 0, 1}, {127, 1, 2, 3},
		{224, 0, 0, 1}, {239, 255, 255, 255}, {240, 0, 0, 1}, {255, 255, 255, 255},
	}
	for _, address := range unusable {
		if partyLANUsableIPv4(address) {
			t.Fatalf("%v must not be usable as a LAN direct address", address)
		}
	}
	// 观测地址来源 = 会话的 TCP 远端地址（与中继同步同源）；假 conn 没有可观测 IPv4。
	session := &gameSession{conn: &tcpAddrBufferConn{
		bufferConn: bufferConn{},
		addr:       &net.TCPAddr{IP: lanFixtureObservedA, Port: 41001},
	}}
	address, ok := partySessionObservedIPv4(session)
	if !ok || address != [4]byte{203, 0, 113, 7} {
		t.Fatalf("observed ipv4 = % X ok=%v want 203.0.113.7", address, ok)
	}
	if _, ok := partySessionObservedIPv4(&gameSession{conn: &bufferConn{}}); ok {
		t.Fatal("a fake conn must not yield an observed IPv4 address")
	}
	if _, ok := partySessionObservedIPv4(nil); ok {
		t.Fatal("a nil session must not yield an observed IPv4 address")
	}
}
