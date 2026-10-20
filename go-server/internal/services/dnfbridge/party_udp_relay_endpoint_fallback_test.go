package dnfbridge

import (
	"bytes"
	"net"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/party"
)

// 这一组用例钉死两个**默认关闭**的单变量实验：
//  1. DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK=advertised
//     —— 反向腿未学习时，用该成员**自报**的 UDP 端口 + 它已被游戏 TCP 会话认证的 IPv4 再写一次。
//  2. DNFBRIDGE_PARTY_PEER_SELF_ENDPOINT=reported
//     —— op11 里"自己那一行"改用客户端 CS op2 自报的 inner/outer/port。
//
// 关键不变量：**默认档（off / observed）必须与加开关之前逐字节一致**。

func testRelayWithEndpointFallback(t *testing.T, recorder *relayLogRecorder, advertised map[uint16]uint16) (*currentPartyUDPRelay, uint16, uint16) {
	t.Helper()
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelayWithEndpointFallback(true, "127.0.0.1", portStart, 12, true, recorder.log)
	if err != nil {
		t.Fatalf("new UDP relay with endpoint fallback: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	address := [4]byte{127, 0, 0, 1}
	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: address, advertisedPort: advertised[101]},
		{characterID: 202, generation: 1, address: address, advertisedPort: advertised[202]},
	}
	if !relay.Sync(77, bindings) {
		t.Fatal("relay did not create a complete two-member room")
	}
	_, portAB, okAB := relay.Endpoint(77, 101, 202)
	_, portBA, okBA := relay.Endpoint(77, 202, 101)
	if !okAB || !okBA {
		t.Fatal("directed relay endpoints missing")
	}
	return relay, portAB, portBA
}

// 默认档解析：空、未设置、off 都是 off；只有 advertised 打开。
func TestPartyUDPRelayEndpointFallbackModeDefaultsOff(t *testing.T) {
	for _, value := range []string{"", "  ", "off", "OFF", "none", "1", "true"} {
		if got := readPartyUDPRelayEndpointFallbackMode(value); got.enabled() {
			t.Fatalf("value %q enabled the endpoint fallback; only advertised may", value)
		}
	}
	if got := readPartyUDPRelayEndpointFallbackMode(" advertised "); !got.enabled() {
		t.Fatalf("advertised value did not enable the fallback: %v", got)
	}
}

// 默认档（= 现行为）：反向腿未学习时**一个字节都不写**，只计数 + 记一条未学习日志。
// 这条用例就是"关闭时行为与加开关前逐字节一致"的钉子。
func TestPartyUDPRelayEndpointFallbackStaysOffByDefault(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, portAB, _ := relayDiagnosticsFixture(t, recorder)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	if _, err := clientA.WriteToUDP([]byte("A-early"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A payload: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay peer endpoint not learned", 3*time.Second)
	stats := relayLegStatsFor(t, relay, 202, 101)
	if stats.EndpointFallbackDatagrams != 0 || stats.EndpointFallbackFailed != 0 {
		t.Fatalf("endpoint fallback wrote datagrams with the switch off: %+v", stats)
	}
	if recorder.contains("party UDP relay endpoint fallback used") {
		t.Fatalf("fallback log appeared with the switch off:\n%s", recorder.joined())
	}
}

// 打开后：中继把 A 的负载写到"对端自报端口"上，并且**目标 IP 只可能取已认证的
// binding.address**（这里 127.0.0.1），不是任何客户端上报的 IP。
// 用例里让 B 真的**监听**在自报端口上，于是"包真的到了 B"也可断言。
func TestPartyUDPRelayEndpointFallbackTargetsAdvertisedPort(t *testing.T) {
	// B 先占一个端口当作它的"自报端口"。
	listenerB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer listenerB.Close()
	advertisedPort := uint16(listenerB.LocalAddr().(*net.UDPAddr).Port)

	recorder := &relayLogRecorder{}
	relay, portAB, _ := testRelayWithEndpointFallback(t, recorder, map[uint16]uint16{202: advertisedPort})
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()

	if _, err := clientA.WriteToUDP([]byte("A-to-silent-B"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A payload: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay endpoint fallback used", 3*time.Second)
	if !recorder.contains("endpoint_fallback advertised") &&
		!recorder.contains("endpoint_fallback") {
		t.Fatalf("leg creation log must carry the endpoint_fallback label:\n%s", recorder.joined())
	}

	buffer := make([]byte, 64)
	if err := listenerB.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set B deadline: %v", err)
	}
	n, source, err := listenerB.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("silent member did not receive the fallback datagram: %v", err)
	}
	if !bytes.Equal(buffer[:n], []byte("A-to-silent-B")) {
		t.Fatalf("fallback payload = %q want A-to-silent-B", buffer[:n])
	}
	// 发方必须是"对端那条腿"的 socket（= B 被告知要发往的那个端口），且源 IP 是 relay 绑定地址。
	if source.Port == int(portAB) {
		t.Fatalf("fallback must be emitted from the peer's own leg socket, got source port %d (= A leg)", source.Port)
	}
	// 回落计数记在**对端那条腿**（202→101，= 被喂包的那条）上；
	// "未学习/未转发"记在**发方那条腿**（101→202）上。
	fallbackStats := relayLegStatsFor(t, relay, 202, 101)
	if fallbackStats.EndpointFallbackDatagrams == 0 {
		t.Fatalf("fallback counter stayed 0: %+v", fallbackStats)
	}
	senderStats := relayLegStatsFor(t, relay, 101, 202)
	if senderStats.Forwarded != 0 {
		t.Fatalf("the abandoned forwarding path must not be counted as a forward: %+v", senderStats)
	}
	if senderStats.UnlearnedPeerEndpoint == 0 {
		t.Fatalf("unlearned peer endpoint counter must still be recorded: %+v", senderStats)
	}
}

// 一旦那条腿**自己**发来了数据报：回落到此为止（真实端点成为权威），
// 并记一条 `learned real endpoint`——这就是"沉默的一端被唤醒"的判定性证据。
func TestPartyUDPRelayEndpointFallbackYieldsToLearnedEndpoint(t *testing.T) {
	listenerB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer listenerB.Close()
	advertisedPort := uint16(listenerB.LocalAddr().(*net.UDPAddr).Port)

	recorder := &relayLogRecorder{}
	relay, portAB, portBA := testRelayWithEndpointFallback(t, recorder, map[uint16]uint16{202: advertisedPort})
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()

	if _, err := clientA.WriteToUDP([]byte("A-early"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A payload: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay endpoint fallback used", 3*time.Second)

	// B 从它**真正的** socket 发一条 ⇒ 该腿学到真实端点、回落让位。
	if _, err := clientB.WriteToUDP([]byte("b-ready"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B handshake: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay endpoint fallback learned real endpoint", 3*time.Second)
	stats := relayLegStatsFor(t, relay, 202, 101)
	if stats.LearnedEndpoint == "" {
		t.Fatalf("learned endpoint missing after B sent: %+v", stats)
	}

	// 之后 A 的包必须走**已验证**的真实端点（clientB 自己的 socket），而不是自报端口。
	buffer := make([]byte, 64)
	if err := clientB.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set B deadline: %v", err)
	}
	if _, err := clientA.WriteToUDP([]byte("A-late"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A late payload: %v", err)
	}
	n, _, err := clientB.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("B did not receive the relayed datagram after learning: %v", err)
	}
	if !bytes.Equal(buffer[:n], []byte("A-late")) {
		t.Fatalf("relayed payload = %q want A-late", buffer[:n])
	}
	if relay.ForwardedDatagramTotal() == 0 {
		t.Fatal("forwarded total stayed 0 after the peer endpoint was learned")
	}
}

// 没有自报端口（CS op2 没报过 / 报了 0）时：即使开关打开也不写任何东西 —— 回落
// 绝不能靠"猜一个端口"来工作。
func TestPartyUDPRelayEndpointFallbackRequiresAdvertisedPort(t *testing.T) {
	recorder := &relayLogRecorder{}
	_, portAB, _ := testRelayWithEndpointFallback(t, recorder, nil)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	if _, err := clientA.WriteToUDP([]byte("A-early"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A payload: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay peer endpoint not learned", 3*time.Second)
	if recorder.contains("party UDP relay endpoint fallback used") {
		t.Fatalf("fallback fired without an advertised port:\n%s", recorder.joined())
	}
}

// 端口上限：每条腿最多 currentPartyUDPRelayEndpointFallbackMaxDatagrams 条回落写入，
// 到顶后记一条 exhausted 并停手（避免把中继变成长期注入器）。
func TestPartyUDPRelayEndpointFallbackStopsAtCap(t *testing.T) {
	recorder := &relayLogRecorder{}
	leg := &currentPartyUDPRelayLeg{
		key:     currentPartyUDPRelayLegKey{partyID: 77, ownerID: 202, peerID: 101},
		binding: currentPartyUDPRelayBinding{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: 41234},
		conn:    &net.UDPConn{},
	}
	for attempt := 0; attempt < currentPartyUDPRelayEndpointFallbackMaxDatagrams; attempt++ {
		if _, ok, exhausted := leg.endpointFallbackTarget(); !ok || exhausted {
			t.Fatalf("attempt %d refused before the cap: ok=%t exhausted=%t", attempt, ok, exhausted)
		}
	}
	_, ok, exhausted := leg.endpointFallbackTarget()
	if ok || !exhausted {
		t.Fatalf("cap not enforced: ok=%t exhausted=%t", ok, exhausted)
	}
	if _, ok, exhausted := leg.endpointFallbackTarget(); ok || exhausted {
		t.Fatalf("exhausted log must be one-shot: ok=%t exhausted=%t", ok, exhausted)
	}
	_ = recorder
}

// 学到的真实端点比自报端口优先：`endpointFallbackTarget` 在 learnedEndpoint 非空时
// 必须拒绝回落（回落永远不能与权威目标争抢）。
func TestPartyUDPRelayEndpointFallbackRefusesAfterLearned(t *testing.T) {
	leg := &currentPartyUDPRelayLeg{
		binding:         currentPartyUDPRelayBinding{address: [4]byte{10, 0, 0, 7}, advertisedPort: 40000},
		learnedEndpoint: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 7), Port: 55555},
		conn:            &net.UDPConn{},
	}
	if _, ok, _ := leg.endpointFallbackTarget(); ok {
		t.Fatal("fallback target offered although a learned endpoint exists")
	}
}

// op11"自己那一行"的第二个单变量实验：默认档解析。
func TestPartyPeerSelfEndpointModeDefaultsObserved(t *testing.T) {
	for _, value := range []string{"", " observed ", "observed", "OFF", "1"} {
		if got := readPartyPeerSelfEndpointMode(value); got.reported() {
			t.Fatalf("value %q switched the self row to the reported endpoint", value)
		}
	}
	if got := readPartyPeerSelfEndpointMode("reported"); !got.reported() {
		t.Fatalf("reported value did not switch the self row: %v", got)
	}
}

// 新行构造器必须与已证实的 party.BuildPeerEndpointInfo **逐字节相同**（等价输入下），
// 否则 `reported` 档就不只是"换了自己那一行的 IP"，而会连带改掉整份 body 的形状。
func TestBuildCurrentPartyPeerEndpointInfoRowsMatchesProvenBuilder(t *testing.T) {
	endpoints := []party.PeerEndpoint{
		{UserID: 3, IPv4: [4]byte{38, 22, 90, 52}, Port: 31000, AccountID: 3},
		{UserID: 4, IPv4: [4]byte{38, 22, 90, 52}, Port: 31001, AccountID: 4},
	}
	rows := make([]currentPartyPeerEndpointRow, 0, len(endpoints))
	for _, endpoint := range endpoints {
		rows = append(rows, peerEndpointRowFromEndpoint(endpoint))
	}
	got := buildCurrentPartyPeerEndpointInfoRows(rows)
	want := party.BuildPeerEndpointInfo(endpoints)
	if !bytes.Equal(got, want) {
		t.Fatalf("row builder bytes = %x, proven builder = %x", got, want)
	}
}

// `reported` 档的自报行：inner/outer 分开写、端口网络序、长度恒 22B/成员。
func TestCurrentPartyPeerSelfEndpointRowUsesReportedInnerOuterAndPort(t *testing.T) {
	registration := currentPartyPeerEndpointRegistration{
		CharacterAttribute: 7,
		InnerIPv4:          [4]byte{192, 168, 0, 101},
		OuterIPv4:          [4]byte{192, 168, 0, 101},
		Port:               20612,
	}
	row, ok := currentPartyPeerSelfEndpointRow(registration, 3, 3)
	if !ok {
		t.Fatal("reported row rejected although the registration is complete")
	}
	body := buildCurrentPartyPeerEndpointInfoRows([]currentPartyPeerEndpointRow{row})
	if len(body) != 1+currentPartyPeerEndpointSize {
		t.Fatalf("body len = %d want %d", len(body), 1+currentPartyPeerEndpointSize)
	}
	if body[0] != 1 {
		t.Fatalf("member count = %d want 1", body[0])
	}
	if !bytes.Equal(body[3:7], []byte{192, 168, 0, 101}) || !bytes.Equal(body[7:11], []byte{192, 168, 0, 101}) {
		t.Fatalf("inner/outer = %x/%x want 192.168.0.101 twice", body[3:7], body[7:11])
	}
	if body[11] != 0x50 || body[12] != 0x84 {
		t.Fatalf("port bytes = %02x %02x want 50 84 (20612 big endian)", body[11], body[12])
	}
	if body[13] != 3 || body[17] != 0 {
		t.Fatalf("account/nat bytes = %d/%d want 3/0", body[13], body[17])
	}
	if body[22] != 0 {
		t.Fatalf("char attr = %d want 0 (只换 IP/端口，char_attr 保持已证实的 0)", body[22])
	}
	if ipv4 := net.IPv4(body[3], body[4], body[5], body[6]); ipv4.IsUnspecified() {
		t.Fatal("reported row lost its address")
	}
}

// 没有 CS op2 上报时（端口 0 / IP 全零）：`reported` 档必须拒绝并回落现行为。
func TestCurrentPartyPeerSelfEndpointRowRequiresRegistration(t *testing.T) {
	cases := []currentPartyPeerEndpointRegistration{
		{},
		{Port: 20612},
		{InnerIPv4: [4]byte{192, 168, 0, 101}, OuterIPv4: [4]byte{192, 168, 0, 101}},
	}
	for index, registration := range cases {
		if _, ok := currentPartyPeerSelfEndpointRow(registration, 3, 3); ok {
			t.Fatalf("case %d accepted an unusable registration: %+v", index, registration)
		}
	}
}
