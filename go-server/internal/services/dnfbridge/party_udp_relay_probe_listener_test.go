package dnfbridge

import (
	"bytes"
	"net"
	"strconv"
	"testing"
	"time"
)

// 本文件钉住 2026-09-25 第七次现场取证（20:46 party_id=1）得出的**单向死锁根因修法**：
// 中继手上必须有一个**我们自己观测到的**对端端点，否则"整场只有 1 个数据报、0 转发"
// 就是必然结果（该数据报只能被留存或丢弃，两端互相等待）。
//
// 现场依据（香港只读日志 `/opt/dnf90/logs/dnf90.out`）：
//
//	23514 20:46:10.434 peer endpoint not learned owner_id=3 peer_id=4 leg=31000 source=39.172.226.229:21574
//	23515 20:46:10.434 probe queued owner_id=4 peer_id=3 leg=31001 source=39.172.226.229:21574
//	23516 20:46:10.434 endpoint fallback used owner_id=4 peer_id=3 target=39.172.226.229:61630
//	23536 20:46:50.001 legs closed received_datagrams=1 leg_forwarded_datagrams=0
//
// 厂商对照（`D:\DNF90-test\server\runtime\logs\packet_log.txt`）：`game-party-peer-endpoint-bound`
// 发生在 **10:22:48/53**，组队 op11 在 **10:23:05** ⇒ 两端端点在组队前就已经被观测并 bind 好，
// 所以厂商中继从第一个数据报起就能双向投递（46,761 条 payload 持续流动）。
//
// 三条回归：
//
//	① 原生探针端点必须在组队**之前**就能被记下来，并成为腿的种子目标；
//	② 有种子目标时，**先发包那一端**的第一个数据报必须被立即投出去（而不是被留存/丢弃）；
//	③ 角色 id 对不上（同 IP 双开）绝不能互相串包；没有种子时行为与加本功能之前一致。

// newProbeListenerRelay 建一个带"常驻原生探针监听 + 种子端点投递"的中继（真开关档）。
// 探针端口用 0（由内核分配）⇒ 测试不需要猜端口、也不会与中继口或其它测试撞车；
// 实际端口从 relay.probeListenerPorts() 读回。现场部署里探针口是 30000、中继口是 31000+。
func newProbeListenerRelay(t *testing.T, recorder *relayLogRecorder) (*currentPartyUDPRelay, int) {
	t.Helper()
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:       true,
		advertiseIP:   "127.0.0.1",
		portStart:     portStart,
		portCount:     12,
		probeReplay:   true,
		probeListen:   true,
		probePorts:    []int{0},
		seedForward:   true,
		datagramTrace: true,
		logWarn:       recorder.log,
	})
	if err != nil {
		t.Fatalf("new UDP relay with probe listener: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	ports := relay.probeListenerPorts()
	if len(ports) != 1 {
		t.Fatalf("probe listener ports=%v want exactly one live bound port", ports)
	}
	return relay, ports[0]
}

// probePayload 造一条与厂商 21B 首包同形的载荷：第 1 字节 0x02、第 2 字节 = 发送者 uid-1。
func probePayload(characterID uint16) []byte {
	payload := make([]byte, 21)
	payload[0] = 0x02
	payload[1] = byte(characterID - 1)
	for index := 2; index < len(payload); index++ {
		payload[index] = byte(index)
	}
	return payload
}

// relayLegFor 取某条腿（按 (owner,peer) 定向键）。
func relayLegFor(t *testing.T, relay *currentPartyUDPRelay, ownerID, peerID uint16) *currentPartyUDPRelayLeg {
	t.Helper()
	relay.mu.Lock()
	leg := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: ownerID, peerID: peerID}]
	relay.mu.Unlock()
	if leg == nil {
		t.Fatalf("relay leg %d->%d missing", ownerID, peerID)
	}
	return leg
}

// syncTwoMemberRoom 建一个 (101,202) 的两人房并把端口返回来。
func syncTwoMemberRoom(t *testing.T, relay *currentPartyUDPRelay) (uint16, uint16) {
	t.Helper()
	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(77, bindings) {
		t.Fatal("relay did not create a complete two-member room")
	}
	_, portAB, okAB := relay.Endpoint(77, 101, 202)
	_, portBA, okBA := relay.Endpoint(77, 202, 101)
	if !okAB || !okBA {
		t.Fatal("directed relay endpoints missing")
	}
	return portAB, portBA
}

// ① 常驻探针监听：**组队之前**客户端自己发到中继端口的端点必须被记下来并能当种子。
func TestProbeListenerBindsEndpointBeforeAnyParty(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newProbeListenerRelay(t, recorder)
	client := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer client.Close()
	if _, err := client.WriteToUDP(probePayload(101), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: probePort}); err != nil {
		t.Fatalf("send native probe: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay native probe observed", 3*time.Second)
	waitRelayCondition(t, 3*time.Second, "native probe to be registered", func() bool {
		return relay.probeEndpointFor(101, [4]byte{127, 0, 0, 1}) != nil
	})
	endpoint := relay.probeEndpointFor(101, [4]byte{127, 0, 0, 1})
	local := client.LocalAddr().(*net.UDPAddr)
	if endpoint.Port != local.Port {
		t.Fatalf("probe endpoint port=%d want the observed source port %d", endpoint.Port, local.Port)
	}
	// 角色 id 解析不出来时绝不能登记（否则同 IP 双开会串包）。
	if got := relay.probeEndpointFor(102, [4]byte{127, 0, 0, 1}); got != nil {
		t.Fatalf("unparsed/other character must not inherit a probe endpoint: %v", got)
	}
	// 源 IPv4 与该角色游戏 TCP 会话观测 IP 不同时也不可用。
	if got := relay.probeEndpointFor(101, [4]byte{127, 0, 0, 2}); got != nil {
		t.Fatalf("probe endpoint must require the session-observed IPv4: %v", got)
	}
}

// ② 核心回归：A 一直没往自己的中继口发过任何包（现场 char4 就是这样），
// 但只要 A 在组队前被观测到过，**B 的第一个数据报就必须立刻投到 A 手上**。
func TestSeedForwardDeliversFirstDatagramToThePreviouslyObservedPeer(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newProbeListenerRelay(t, recorder)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()

	// 组队前：A 发过一次原生探针（= 厂商 `game-party-peer-endpoint-bound` 那一步）。
	if _, err := clientA.WriteToUDP(probePayload(101), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: probePort}); err != nil {
		t.Fatalf("send A native probe: %v", err)
	}
	waitRelayCondition(t, 3*time.Second, "A probe endpoint to be registered", func() bool {
		return relay.probeEndpointFor(101, [4]byte{127, 0, 0, 1}) != nil
	})

	portAB, portBA := syncTwoMemberRoom(t, relay)
	// 腿 (owner=101) 的种子 = A 自己被观测到的端点；腿 (owner=202) 没有种子（B 从未被观测到）。
	if stats := relayLegStatsFor(t, relay, 101, 202); stats.SeedEndpoint == "" {
		t.Fatalf("leg for the previously observed member must carry a seed endpoint: %+v", stats)
	}
	if stats := relayLegStatsFor(t, relay, 202, 101); stats.SeedEndpoint != "" {
		t.Fatalf("leg for the never-observed member must not invent a seed endpoint: %+v", stats)
	}
	_ = portAB

	// B（没被观测过的那一端）先发第一个数据报。
	if _, err := clientB.WriteToUDP([]byte("B-first-payload"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay fanout candidate used", 3*time.Second)
	if !recorder.contains("kind seed_probe") {
		t.Fatalf("the native probe endpoint must be used as a candidate: %s", recorder.joined())
	}
	// A 必须收到 B 的首包 —— 旧实现在这里永远是超时（A 从未向中继口发过包）。
	if payload := readClientDatagram(t, clientA, 2*time.Second); string(payload) != "B-first-payload" {
		t.Fatalf("A did not receive the seeded first datagram: %q", payload)
	}
	if stats := relayLegStatsFor(t, relay, 101, 202); stats.SeedForwardDatagrams != 1 {
		t.Fatalf("seed forward datagrams=%d want 1: %+v", stats.SeedForwardDatagrams, stats)
	}
	// 每包 trace 必须把判定写出来（复测判读用）。
	if !recorder.contains("party UDP relay datagram") {
		t.Fatal("datagram trace line missing while trace mode is on")
	}
	if !recorder.contains("decision fanout_seed_probe") {
		t.Fatalf("trace line must carry the decision: %s", recorder.joined())
	}
}

// ③ 关掉新开关必须逐字节回到旧行为：首包被留存，另一端在它自己发包前什么也收不到。
func TestSeedForwardOffKeepsLegacyHoldBehaviour(t *testing.T) {
	recorder := &relayLogRecorder{}
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:     true,
		advertiseIP: "127.0.0.1",
		portStart:   portStart,
		portCount:   12,
		probeReplay: true,
		probeListen: false,
		seedForward: false,
		logWarn:     recorder.log,
	})
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	_, portBA := syncTwoMemberRoom(t, relay)
	if stats := relayLegStatsFor(t, relay, 101, 202); stats.SeedEndpoint != "" {
		t.Fatalf("seed must stay empty when the switch is off: %+v", stats)
	}
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	if _, err := clientB.WriteToUDP([]byte("B-first"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe queued", 3*time.Second)
	if payload := readClientDatagram(t, clientA, 300*time.Millisecond); payload != nil {
		t.Fatalf("with seed forwarding off A must stay silent, got %q", payload)
	}
	if stats := relayLegStatsFor(t, relay, 101, 202); stats.SeedForwardDatagrams != 0 {
		t.Fatalf("seed forward must not fire when the switch is off: %+v", stats)
	}
}

// ④ relay 级清扫器：待投队列 TTL 到期必须能被记出来（旧实现只在同一条腿再次收到数据报时
// 才检查 TTL ⇒ "对端整场不发"这种最该被记出来的情形反而永远静默）。
func TestPendingProbeSweeperLogsExpiryWithoutFurtherTraffic(t *testing.T) {
	recorder := &relayLogRecorder{}
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:     true,
		advertiseIP: "127.0.0.1",
		portStart:   portStart,
		portCount:   12,
		probeReplay: true,
		seedForward: true,
		logWarn:     recorder.log,
	})
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	syncTwoMemberRoom(t, relay)
	leg := relayLegFor(t, relay, 202, 101)
	leg.queuePendingProbe([]byte("held-without-peer"))
	leg.mu.Lock()
	leg.pending.since = time.Now().Add(-2 * pendingTTL)
	leg.mu.Unlock()
	relay.sweepPendingProbes()
	if !recorder.contains("party UDP relay probe replay expired") {
		t.Fatalf("sweeper must report the TTL expiry with no further traffic: %s", recorder.joined())
	}
	if stats := relayLegStatsFor(t, relay, 202, 101); stats.PendingExpired == 0 {
		t.Fatalf("expired counter stayed 0: %+v", stats)
	}
}

// ⑤ 清扫器**只**记过期（活跃投递由 fanOutUnlearnedDatagram 在每一个入向数据报上完成，
// 重复补投只会让对端收到同一份载荷的多个副本）。
func TestPendingProbeSweeperOnlyLogsExpiry(t *testing.T) {
	recorder := &relayLogRecorder{}
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:     true,
		advertiseIP: "127.0.0.1",
		portStart:   portStart,
		portCount:   12,
		probeReplay: true,
		seedForward: true,
		logWarn:     recorder.log,
	})
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	syncTwoMemberRoom(t, relay)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	local := clientA.LocalAddr().(*net.UDPAddr)
	leg := relayLegFor(t, relay, 101, 202)
	leg.mu.Lock()
	leg.seedEndpoint = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: local.Port}
	leg.mu.Unlock()
	leg.queuePendingProbe([]byte("swept-payload"))
	relay.sweepPendingProbes()
	if payload := readClientDatagram(t, clientA, 400*time.Millisecond); payload != nil {
		t.Fatalf("the sweeper must not re-deliver held payloads (fan-out owns active delivery), got %q", payload)
	}
	if stats := relayLegStatsFor(t, relay, 101, 202); stats.SeedForwardDatagrams != 0 {
		t.Fatalf("sweeper must not count seed forwards: %+v", stats)
	}
}

// ⑥ 开关解析：三个 env 的默认档必须与文件头声明一致（on/on/off），并支持一键回退。
func TestProbeListenerSwitchDefaultsAndRollback(t *testing.T) {
	if got := readPartyUDPRelayProbeListenMode(""); got != partyUDPRelayProbeListenEnabled {
		t.Fatalf("probe listen default=%v want on", got)
	}
	if got := readPartyUDPRelayProbeListenMode("off"); got != partyUDPRelayProbeListenDisabled {
		t.Fatalf("probe listen off=%v want disabled", got)
	}
	if got := readPartyUDPRelaySeedForwardMode(""); got != partyUDPRelaySeedForwardEnabled {
		t.Fatalf("seed forward default=%v want on", got)
	}
	if got := readPartyUDPRelaySeedForwardMode("off"); got != partyUDPRelaySeedForwardDisabled {
		t.Fatalf("seed forward off=%v want disabled", got)
	}
	if readPartyUDPRelayDatagramTraceMode("") || !readPartyUDPRelayDatagramTraceMode("on") {
		t.Fatal("datagram trace must default off and accept on")
	}
	if got := readPartyUDPRelayProbeListenMode("").String(); got != partyUDPRelaySwitchOn {
		t.Fatalf("probe listen label=%q", got)
	}
	// 零值 options / 旧构造器 = 加本功能之前的行为（既有单测路径不受影响）。
	if (*Service)(nil).partyUDPRelayProbeListenMode().enabled() {
		t.Fatal("zero-value service must keep the probe listener off")
	}
	if (*Service)(nil).partyUDPRelaySeedForwardMode().enabled() {
		t.Fatal("zero-value service must keep seed forwarding off")
	}
	legacy, err := newCurrentPartyUDPRelayWithProbeReplay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, false, true, nil)
	if err != nil {
		t.Fatalf("legacy constructor: %v", err)
	}
	defer legacy.Close()
	if legacy.seedForward || legacy.probeListen || legacy.datagramTrace || legacy.sweeperStop != nil {
		t.Fatal("legacy constructor must not enable the new switches")
	}
}

// ⑦ 探针端口列表：默认**只有**中继起始口（港机 30000–30255 属于 a21-relay，不能默认占用）；
// 显式配置覆盖默认，非法项被丢弃。
func TestProbePortDefaultsAndOverrides(t *testing.T) {
	ports := partyUDPRelayProbePorts("", 31000)
	if len(ports) != 1 || ports[0] != 31000 {
		t.Fatalf("default probe ports=%v want [31000]", ports)
	}
	if ports := partyUDPRelayProbePorts("45650", 31000); len(ports) != 1 || ports[0] != 45650 {
		t.Fatalf("explicit probe ports=%v", ports)
	}
	if ports := partyUDPRelayProbePorts("45650, 45650 ,x,70000,-1", 31000); len(ports) != 1 || ports[0] != 45650 {
		t.Fatalf("probe port parsing must drop invalid/duplicate entries: %v", ports)
	}
}

// ⑧ 角色 id 解析必须严格（第 1 字节 0x02）——解析错就会把 A 的包投给 B（同 IP 双开）。
func TestProbeCharacterIDParsing(t *testing.T) {
	if id, ok := partyUDPRelayProbeCharacterID([]byte{0x02, 0x00}); !ok || id != 1 {
		t.Fatalf("payload 02 00 => id=%d ok=%v want 1/true", id, ok)
	}
	if id, ok := partyUDPRelayProbeCharacterID([]byte{0x02, 0x01, 0x0c}); !ok || id != 2 {
		t.Fatalf("payload 02 01 => id=%d ok=%v want 2/true (vendor fixture)", id, ok)
	}
	if _, ok := partyUDPRelayProbeCharacterID([]byte{0x00, 0x01}); ok {
		t.Fatal("payload without the 0x02 first byte must not be parsed as a character id")
	}
	if _, ok := partyUDPRelayProbeCharacterID([]byte{0x02}); ok {
		t.Fatal("short payload must not be parsed")
	}
}

// ⑨ 探针口与中继范围重叠时保持专用，定向腿使用范围内其余端口。
func TestProbePortReservationKeepsRelayBindable(t *testing.T) {
	recorder := &relayLogRecorder{}
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:     true,
		advertiseIP: "127.0.0.1",
		portStart:   portStart,
		portCount:   12,
		probeReplay: true,
		probeListen: true,
		probePorts:  []int{portStart},
		seedForward: true,
		logWarn:     recorder.log,
	})
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	portAB, portBA := syncTwoMemberRoom(t, relay)
	if portAB == portBA {
		t.Fatalf("directed legs must bind distinct ports: %d/%d", portAB, portBA)
	}
	// 保留探针监听，不能把持续保活导向某个角色的定向腿。
	if ports := relay.probeListenerPorts(); len(ports) != 1 || ports[0] != portStart {
		t.Fatalf("dedicated probe listener lost: %v", ports)
	}
	if int(portAB) == portStart || int(portBA) == portStart {
		t.Fatal("probe port allocated to directed leg")
	}
	for _, probePort := range relay.probeListenerPorts() {
		if probePort == int(portAB) || probePort == int(portBA) {
			t.Fatalf("probe listener still holds a port a party leg is using: %d", probePort)
		}
	}
}

// ⑩ 多候选扇出（21:15 现场的直接修法）：反向腿端点未知时，同一个数据报要**同时**投给
// 所有已知候选 —— 陈旧备忘（旧 NAT 端口）与 CS op2 自报端口各一份，每个候选单独计数与首次日志。
func TestFanOutUnlearnedDatagramHitsEveryKnownCandidate(t *testing.T) {
	recorder := &relayLogRecorder{}
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:          true,
		advertiseIP:      "127.0.0.1",
		portStart:        portStart,
		portCount:        12,
		endpointFallback: true,
		probeReplay:      true,
		seedForward:      true,
		memoTTL:          30 * time.Second,
		logWarn:          recorder.log,
	})
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	staleEndpoint := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer staleEndpoint.Close()
	advertisedEndpoint := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer advertisedEndpoint.Close()

	// 角色 101 上次自己发过的端点（已陈旧 ≈ 90 秒）+ 它 CS op2 自报的端口。
	relay.rememberEndpoint(&currentPartyUDPRelayLeg{
		key:     currentPartyUDPRelayLegKey{partyID: 77, ownerID: 101},
		binding: currentPartyUDPRelayBinding{characterID: 101, address: [4]byte{127, 0, 0, 1}},
	}, staleEndpoint.LocalAddr().(*net.UDPAddr))
	relay.memoMu.Lock()
	memo := relay.endpointMemo[101]
	memo.seenAt = time.Now().Add(-90 * time.Second)
	relay.endpointMemo[101] = memo
	relay.memoMu.Unlock()

	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: uint16(advertisedEndpoint.LocalAddr().(*net.UDPAddr).Port)},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(77, bindings) {
		t.Fatal("relay did not create a complete two-member room")
	}
	_, portBA, _ := relay.Endpoint(77, 202, 101)
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	if _, err := clientB.WriteToUDP([]byte("fan-out-payload"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay fanout candidate used", 3*time.Second)
	stats := relayLegStatsFor(t, relay, 101, 202)
	if stats.StaleMemoDatagrams != 1 {
		t.Fatalf("stale memo candidate must be written exactly once per datagram: %+v", stats)
	}
	if stats.EndpointFallbackDatagrams != 1 {
		t.Fatalf("advertised port candidate must be written exactly once per datagram: %+v", stats)
	}
	if !recorder.contains("kind memo_stale") || !recorder.contains("kind advertised_port") {
		t.Fatalf("both candidate kinds must be logged with their kind= label: %s", recorder.joined())
	}
	if payload := readClientDatagram(t, staleEndpoint, 2*time.Second); string(payload) != "fan-out-payload" {
		t.Fatalf("stale memo candidate did not receive a copy: %q", payload)
	}
	if payload := readClientDatagram(t, advertisedEndpoint, 2*time.Second); string(payload) != "fan-out-payload" {
		t.Fatalf("advertised port candidate did not receive a copy: %q", payload)
	}
}

// ⑪ 陈旧记忆不得被当成"新鲜"：超过 MEMO_TTL_SECONDS 的备忘只能走 memo_stale 候选。
func TestMemoTTLSeparatesFreshFromStale(t *testing.T) {
	if got := readPartyUDPRelayMemoTTL(""); got != defaultPartyUDPRelayMemoTTLSeconds*time.Second {
		t.Fatalf("default memo ttl=%v want %ds", got, defaultPartyUDPRelayMemoTTLSeconds)
	}
	if got := readPartyUDPRelayMemoTTL("5"); got != 5*time.Second {
		t.Fatalf("memo ttl 5 => %v", got)
	}
	if got := readPartyUDPRelayMemoTTL("nonsense"); got != defaultPartyUDPRelayMemoTTLSeconds*time.Second {
		t.Fatalf("invalid memo ttl must fall back to the default: %v", got)
	}
	if got := readPartyUDPRelayMemoTTL("99999999"); got != memoTTL {
		t.Fatalf("memo ttl must be capped at %v, got %v", memoTTL, got)
	}
	address := [4]byte{127, 0, 0, 1}
	fresh := currentPartyUDPRelayEndpointMemo{valid: true, address: address, port: 4567, seenAt: time.Now()}
	if _, ok := fresh.portForTTL(address, 30*time.Second); !ok {
		t.Fatal("a fresh memo must be usable inside its ttl")
	}
	fresh.seenAt = time.Now().Add(-90 * time.Second)
	if _, ok := fresh.portForTTL(address, 30*time.Second); ok {
		t.Fatal("a stale memo must not pass the fresh ttl")
	}
	if port, ok := fresh.portFor(address); !ok || port != 4567 {
		t.Fatal("the stale memo must still be available as the outer (memoTTL) bound")
	}
}

// ⑫ 23:31 那次的时序 bug 回归：唯一的数据报在腿龄 **远小于 minAge** 时到达，
// 看门狗必须**睡到 minAge 再催**（旧实现一次性内联判断 ⇒ 永远不触发），
// 且对端一开口/被催满次数就必须停手。
func TestSilentPeerWatchdogKicksAfterMinAgeAndStopsWhenPeerSpeaks(t *testing.T) {
	if !readPartyUDPRelaySilentPeerKickMode("") || readPartyUDPRelaySilentPeerKickMode("off") {
		t.Fatal("silent peer kick must default on and accept off")
	}
	recorder := &relayLogRecorder{}
	portStart := testCurrentPartyUDPRelayPortStart(t)
	kicked := make(chan string, 16)
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:     true,
		advertiseIP: "127.0.0.1",
		portStart:   portStart,
		portCount:   12,
		probeReplay: true,
		seedForward: true,
		kickTiming: partyUDPRelayKickTiming{
			minAge:   120 * time.Millisecond,
			interval: 80 * time.Millisecond,
			window:   3 * time.Second,
			maxKicks: silentPeerKickMaxPerLeg,
		},
		onSilentPeer: func(partyID, memberID uint16) bool {
			kicked <- time.Now().Format("150405.000")
			return true
		},
		logWarn: recorder.log,
	})
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	syncTwoMemberRoom(t, relay)
	portAB, portBA := 0, 0
	_, baPort, okBA := relay.Endpoint(77, 202, 101)
	_, abPort, okAB := relay.Endpoint(77, 101, 202)
	if !okAB || !okBA {
		t.Fatal("directed relay endpoints missing")
	}
	portAB, portBA = int(abPort), int(baPort)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	// 现场形态：B 在腿建立后几十毫秒内发唯一一个包，A（=owner 101）整场沉默。
	if _, err := clientB.WriteToUDP([]byte("B-handshake"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: portBA}); err != nil {
		t.Fatalf("send B datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay peer endpoint not learned", 3*time.Second)
	// ① 门槛内不许催（这正是 23:31 那次的时间点）。
	select {
	case <-kicked:
		t.Fatal("kick must not fire before the minimum age")
	case <-time.After(60 * time.Millisecond):
	}
	// ② 睡过 minAge 之后必须自己催起来（旧实现永远不会走到这里）。
	select {
	case <-kicked:
	case <-time.After(2 * time.Second):
		t.Fatal("delayed watchdog never kicked after the minimum age")
	}
	waitRelayLog(t, recorder, "phase delayed", 2*time.Second)
	// ③ 对端（A=owner 101）开口后必须立刻停手。
	if _, err := clientA.WriteToUDP([]byte("A-speaks"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: portAB}); err != nil {
		t.Fatalf("send A datagram: %v", err)
	}
	waitRelayCondition(t, 2*time.Second, "watchdog to observe the peer speaking", func() bool {
		return relayLegStatsFor(t, relay, 101, 202).Received > 0
	})
	time.Sleep(250 * time.Millisecond)
	drained := len(kicked)
	for len(kicked) > 0 {
		<-kicked
	}
	time.Sleep(400 * time.Millisecond)
	if len(kicked) != 0 {
		t.Fatalf("watchdog kept kicking after the peer spoke (drained=%d, new=%d)", drained, len(kicked))
	}
	if drained > silentPeerKickMaxPerLeg {
		t.Fatalf("kicks=%d exceed the per-leg cap %d", drained, silentPeerKickMaxPerLeg)
	}
}

// ⑬ 沉默期间必须**周期性重投**待投首包（23:31 那次唯一一份投在当时唯一候选上就没了；
// 沉默端的 NAT 映射可能晚一点才建立，所以要在总窗口内反复投给全部候选）。
func TestProbeResendRepeatsWhilePeerStaysSilent(t *testing.T) {
	recorder := &relayLogRecorder{}
	portStart := testCurrentPartyUDPRelayPortStart(t)
	advertisedEndpoint := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer advertisedEndpoint.Close()
	advertisedPort := uint16(advertisedEndpoint.LocalAddr().(*net.UDPAddr).Port)
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:          true,
		advertiseIP:      "127.0.0.1",
		portStart:        portStart,
		portCount:        12,
		endpointFallback: true,
		probeReplay:      true,
		seedForward:      true,
		kickTiming: partyUDPRelayKickTiming{
			minAge:   20 * time.Millisecond,
			interval: 60 * time.Millisecond,
			window:   2 * time.Second,
			maxKicks: silentPeerKickMaxPerLeg,
		},
		pendingWindow: 2 * time.Second,
		logWarn:       recorder.log,
	})
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}, advertisedPort: advertisedPort},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(77, bindings) {
		t.Fatal("relay did not create a complete two-member room")
	}
	_, portBA, _ := relay.Endpoint(77, 202, 101)
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	if _, err := clientB.WriteToUDP([]byte("held-first"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe resend", 3*time.Second)
	// 首投 1 份 + 至少 2 轮重投 ⇒ ≥3 份都落在同一个自报端口候选上。
	copies := 0
	for {
		payload := readClientDatagram(t, advertisedEndpoint, 200*time.Millisecond)
		if payload == nil {
			break
		}
		if string(payload) != "held-first" {
			t.Fatalf("unexpected resent payload %q", payload)
		}
		copies++
		if copies >= 8 {
			break
		}
	}
	if copies < 3 {
		t.Fatalf("resend rounds delivered %d copies, want >= 3 (1 initial + >=2 resends)", copies)
	}
	if !recorder.contains("phase resend") || !recorder.contains("candidates advertised_port") {
		t.Fatalf("resend log must carry phase/candidates: %s", recorder.joined())
	}
	// 待投队列在窗口内**不消耗**（否则第二轮就没得投了）。
	if stats := relayLegStatsFor(t, relay, 101, 202); stats.PendingDatagrams == 0 {
		t.Fatalf("held queue must survive the resend window: %+v", stats)
	}
}

// ⑭ 新 env 的解析：门槛 2s、重投窗口 60s、非法值回默认、探针口默认**不含**厂商口 30000
// （港机 30000–30255 属于 a21-relay/DfoServer）。
func TestKickTimingAndProbePortDefaults(t *testing.T) {
	timing := readPartyUDPRelayKickTiming("", "")
	if timing.minAge != silentPeerKickMinAge || timing.window != defaultProbeResendWindow || timing.interval != silentPeerKickInterval {
		t.Fatalf("default kick timing=%+v want minAge=%v window=%v interval=%v", timing, silentPeerKickMinAge, defaultProbeResendWindow, silentPeerKickInterval)
	}
	if timing.maxKicks != silentPeerKickMaxPerLeg {
		t.Fatalf("default max kicks=%d want %d", timing.maxKicks, silentPeerKickMaxPerLeg)
	}
	custom := readPartyUDPRelayKickTiming("5", "90")
	if custom.minAge != 5*time.Second || custom.window != 90*time.Second {
		t.Fatalf("custom kick timing=%+v", custom)
	}
	if got := readPartyUDPRelayKickTiming("nonsense", "-3"); got.minAge != silentPeerKickMinAge || got.window != defaultProbeResendWindow {
		t.Fatalf("invalid values must fall back to defaults: %+v", got)
	}
	if got := readPartyUDPRelayKickTiming("999999", "99999999"); got.minAge != 10*time.Minute || got.window != memoTTL {
		t.Fatalf("kick timing must be clamped: %+v", got)
	}
	ports := partyUDPRelayProbePorts("", 31000)
	if len(ports) != 1 || ports[0] != 31000 {
		t.Fatalf("probe ports default=%v want [31000] only (30000-30255 belongs to a21-relay)", ports)
	}
	if !partyUDPRelayProbePortReserved(30041) || partyUDPRelayProbePortReserved(31000) {
		t.Fatal("reserved-port helper must flag the a21-relay UDP range")
	}
}

// ==================== 无归属观测端点表（2026-09-26 02:13 现场） ====================
//
// 现场形状（香港只读日志 `/opt/dnf90/logs/dnf90.out`）：
//
//	02:13:09.685 native probe observed listener_port=2311 source=39.172.226.229:21854 bytes=1 payload_hex=01
//	02:13:12.199 native probe observed listener_port=2312 source=39.172.226.229:21863 bytes=1 payload_hex=00
//	02:13:40.806 peer endpoint not learned owner_id=3 peer_id=4 leg=31000 source=39.172.226.229:21863
//	02:13:40.806 fanout candidate used        owner_id=4 peer_id=3 leg=31001 kind=advertised_port target=39.172.226.229:61502
//
// 即：真实端点 `:21854` **30 秒前就被我们观测到过**，首包却仍然投给不可达的自报端口 `:61502`。
// 下面六条回归钉住修法：1 字节探针要进表、表要接进候选集、绝不回投发送者、TTL 过期、上限/MRU、
// `off` 档与旧行为逐字节一致。

// newObservedEndpointRelay 建一个"常驻探针监听 + 无归属观测端点扇出"的中继（真开关档），
// 探针口用 0（内核分配）。
// observedFanout=false 用来钉住 off 档。
func newObservedEndpointRelay(t *testing.T, recorder *relayLogRecorder, observedFanout bool) (*currentPartyUDPRelay, int) {
	t.Helper()
	relay, ports := newObservedEndpointRelayWithProbePorts(t, recorder, observedFanout, []int{0})
	if len(ports) != 1 {
		t.Fatalf("probe listener ports=%v want exactly one live bound port", ports)
	}
	return relay, ports[0]
}

// newObservedEndpointRelayWithProbePorts 同上，但探针口列表由调用方给（传多个 0 = 多个监听口，
// 用来复刻"客户端往 2311 与 2312 各打一次探针"的两条入向路径）。
//
// ⚠️ 此投递故障夹具把探针口放在腿范围外；范围重叠另由专用端口回归覆盖。
func newObservedEndpointRelayWithProbePorts(
	t *testing.T,
	recorder *relayLogRecorder,
	observedFanout bool,
	probePorts []int,
) (*currentPartyUDPRelay, []int) {
	t.Helper()
	portStart := testCurrentPartyUDPRelayPortStart(t)
	const portCount = 12
	// 探针口是"先探测一个空闲端口、关掉、稍后再绑"——两次调用之间该端口可能被别的用例或
	// 别的进程抢走。抢走时 relay 只会少绑一个口（不报错），旧实现直接 t.Fatalf，
	// 于是**全量并发跑**时会偶发失败（单跑必过），把真实回归淹掉。
	// 这里改成"重新取端口再建一次"，最多 5 轮；仍然绑不上才算失败。
	var lastBound []int
	for attempt := 1; attempt <= 5; attempt++ {
		ports := make([]int, 0, len(probePorts))
		for range probePorts {
			ports = append(ports, testProbeListenerPortOutsideLegRange(t, portStart, portCount))
		}
		relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
			enabled:                true,
			advertiseIP:            "127.0.0.1",
			portStart:              portStart,
			portCount:              portCount,
			probeReplay:            true,
			probeListen:            true,
			probePorts:             ports,
			seedForward:            true,
			datagramTrace:          true,
			observedEndpointFanout: observedFanout,
			logWarn:                recorder.log,
		})
		if err != nil {
			t.Fatalf("new UDP relay with observed endpoint fanout: %v", err)
		}
		bound := relay.probeListenerPorts()
		if len(bound) == len(ports) {
			t.Cleanup(func() { _ = relay.Close() })
			return relay, bound
		}
		lastBound = bound
		_ = relay.Close()
	}
	t.Fatalf("probe listener ports=%v want %d live bound ports after 5 attempts", lastBound, len(probePorts))
	return nil, nil
}

// testProbeListenerPortOutsideLegRange 分配一个**不在腿端口范围里**的探针口（见上面的 ⚠️）。
func testProbeListenerPortOutsideLegRange(t *testing.T, portStart, portCount int) int {
	t.Helper()
	for attempt := 0; attempt < 40; attempt++ {
		listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("allocate probe listener port: %v", err)
		}
		port := listener.LocalAddr().(*net.UDPAddr).Port
		_ = listener.Close()
		if port < portStart || port > portStart+portCount-1 {
			return port
		}
	}
	t.Fatal("could not allocate a probe listener port outside the leg port range")
	return 0
}

// sendOneByteNativeProbe 复刻现场客户端的原生探测：**1 字节**载荷（`01` = 第一个通告口、
// `00` = 第二个），不含 uid。
func sendOneByteNativeProbe(t *testing.T, client *net.UDPConn, probePort int, payload byte) {
	t.Helper()
	if _, err := client.WriteToUDP([]byte{payload}, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: probePort}); err != nil {
		t.Fatalf("send 1-byte native probe: %v", err)
	}
}

// readClientDatagramWithSource 与 readClientDatagram 相同，但把**包的来源地址**也返回 ——
// "这个包是从探针口发出来的还是从腿口发出来的"必须靠它判定。
func readClientDatagramWithSource(t *testing.T, client *net.UDPConn, timeout time.Duration) ([]byte, *net.UDPAddr) {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(timeout))
	buffer := make([]byte, 2048)
	read, source, err := client.ReadFromUDP(buffer)
	if err != nil {
		return nil, nil
	}
	return append([]byte(nil), buffer[:read]...), source
}

// ageObservedProbeEndpoints 把表里每一条的观测时刻往前推（模拟 TTL 过期）。
func ageObservedProbeEndpoints(relay *currentPartyUDPRelay, age time.Duration) {
	relay.observedMu.Lock()
	for index := range relay.observedEndpoints {
		relay.observedEndpoints[index].seenAt = time.Now().Add(-age)
	}
	relay.observedMu.Unlock()
}

// ⑮ 1 字节探针解析不出角色 id，但源端点**必须**仍然进"无归属观测端点表"，
// 且**不得**因此混进 probes[charID]（两条路并存，不是替代）。
func TestObservedProbeEndpointRecordsOneByteProbeWithoutCharacterID(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newObservedEndpointRelay(t, recorder, true)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	sendOneByteNativeProbe(t, clientA, probePort, 0x01)
	waitRelayLog(t, recorder, "party UDP relay native probe observed", 3*time.Second)
	waitRelayCondition(t, 3*time.Second, "1-byte probe endpoint to enter the observed table", func() bool {
		return relay.observedProbeEndpointCount() == 1
	})
	targets := relay.observedProbeEndpointTargets(nil)
	if len(targets) != 1 {
		t.Fatalf("observed endpoints=%v want exactly one", targets)
	}
	local := clientA.LocalAddr().(*net.UDPAddr)
	if targets[0].Port != local.Port || !targets[0].IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("observed endpoint=%v want the kernel-observed source %s", targets[0], local)
	}
	relay.mu.Lock()
	attributed := len(relay.probes)
	relay.mu.Unlock()
	if attributed != 0 {
		t.Fatalf("a 1-byte payload must not be attributed to any character: %d entries", attributed)
	}
	if endpoint := relay.probeEndpointFor(101, [4]byte{127, 0, 0, 1}); endpoint != nil {
		t.Fatalf("an unparsed probe must not become a per-character seed: %v", endpoint)
	}
	if !recorder.contains("payload_hex 01") {
		t.Fatalf("the 1-byte payload must still be logged: %s", recorder.joined())
	}
}

// ⑯ 决定性回归（现场形状）：两端都只发过 1 字节原生探测 ⇒ 腿没有任何种子，
// 队长随后的 21B 握手必须投给"另一个被观测到的端点"，且**绝不回投给发送者自己**。
func TestObservedProbeEndpointFanOutDeliversFirstHandshakeToThePeer(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newObservedEndpointRelay(t, recorder, true)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	sendOneByteNativeProbe(t, clientA, probePort, 0x01)
	sendOneByteNativeProbe(t, clientB, probePort, 0x00)
	waitRelayCondition(t, 3*time.Second, "both 1-byte probe endpoints to be observed", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})

	portAB, _ := syncTwoMemberRoom(t, relay)
	// 1 字节载荷解析不出角色 id ⇒ 两条腿都**没有**种子：旧实现下这个场景必然死锁。
	if stats := relayLegStatsFor(t, relay, 202, 101); stats.SeedEndpoint != "" {
		t.Fatalf("unparsed probes must not produce a per-character seed: %+v", stats)
	}
	handshake := probePayload(101)
	if _, err := clientA.WriteToUDP(handshake, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A handshake: %v", err)
	}
	// B 必须收到（投给 B 自己被观测到的那个端点）——旧实现在这里永远超时。
	if payload := readClientDatagram(t, clientB, 3*time.Second); !bytes.Equal(payload, handshake) {
		t.Fatalf("B did not receive the handshake through the observed endpoint: %q", payload)
	}
	// A 绝不能收到自己那份（发送者端点必须被排除）。
	if payload := readClientDatagram(t, clientA, 500*time.Millisecond); payload != nil {
		t.Fatalf("the relay must never send a datagram back to its sender, got %q", payload)
	}
	if !recorder.contains("kind observed_probe_endpoint") {
		t.Fatalf("the observed endpoint must be used as a candidate: %s", recorder.joined())
	}
	if !recorder.contains("decision fanout_observed_probe_endpoint") {
		t.Fatalf("the datagram trace must carry the new decision: %s", recorder.joined())
	}
	if stats := relayLegStatsFor(t, relay, 202, 101); stats.FanOutKinds["observed_probe_endpoint"] == 0 {
		t.Fatalf("observed_probe_endpoint candidate count stayed 0: %+v", stats)
	} else if len(stats.FanOutKinds) != 1 {
		// 这一场里**只有**新候选可用（没有种子、没有备忘、自报端口回落默认关）⇒
		// 命中 kind 必须唯一，投递不可能是别的路径顺手完成的（钉住因果关系）。
		t.Fatalf("the delivery must come from the new candidate alone: %+v", stats.FanOutKinds)
	}
}

// ⑯b 同一条观测端点必须**两条入向路径都投**：探针口一份 + 腿口一份。
//
// 现场依据（2026-09-26 04:30:52，party_id=1 owner_id=1 peer_id=4）：队长把 21B 握手
// 发到腿口 31000 后，扇出只从探针口 2311/2312 投给了队员当时唯一在场的观测端点，
// 结果 `opposite_received=0`，60 秒后 `probe replay expired total_expired_datagrams=1`
// ——对端一个字节都没回来。原因是**源端口不匹配**：op11 通告给对端的"对端地址"是**腿口**，
// 当前客户端只认被通告的那个端点作源，探针口那份被丢；而腿口那份此前根本没发过。
// 本用例把"两条路径都必须出现"钉死：既要有探针口（穿透 NAT），也要有腿口（协议上被认）。
func TestObservedProbeEndpointFanOutAlsoDeliversFromTheLegSocket(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newObservedEndpointRelay(t, recorder, true)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	sendOneByteNativeProbe(t, clientA, probePort, 0x01)
	sendOneByteNativeProbe(t, clientB, probePort, 0x00)
	waitRelayCondition(t, 3*time.Second, "both 1-byte probe endpoints to be observed", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})

	portAB, portBA := syncTwoMemberRoom(t, relay)
	handshake := probePayload(101)
	if _, err := clientA.WriteToUDP(handshake, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A handshake: %v", err)
	}
	// B 应当收到**两份**（探针口一份、腿口一份），每一份都与入向载荷逐字节相同。
	sources := make(map[int]bool, 2)
	for attempt := 0; attempt < 4; attempt++ {
		payload, source := readClientDatagramWithSource(t, clientB, 2*time.Second)
		if payload == nil || source == nil {
			break
		}
		if !bytes.Equal(payload, handshake) {
			t.Fatalf("payload %q is not the inbound datagram verbatim", payload)
		}
		sources[source.Port] = true
	}
	if !sources[int(probePort)] {
		t.Fatalf("the probe listener path must still be used; sources=%v", sources)
	}
	if !sources[int(portBA)] {
		t.Fatalf("the leg socket path must also be used (op11 advertises the leg port); sources=%v", sources)
	}
	// 判读字段必须能区分两条路径：`via_listener_port` = 腿口那一份。
	if !recorder.contains("delivery_source leg_socket") {
		t.Fatalf("the leg-socket delivery must be logged as such: %s", recorder.joined())
	}
	if !recorder.contains("delivery_source probe_listener_socket") {
		t.Fatalf("the probe-listener delivery must be logged as such: %s", recorder.joined())
	}
}

// ⑰ TTL 过期后不得再作为候选（否则旧会话留下的 NAT 端口会变成长期注入目标）。
func TestObservedProbeEndpointExpiresWithTTL(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newObservedEndpointRelay(t, recorder, true)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	sendOneByteNativeProbe(t, clientA, probePort, 0x01)
	sendOneByteNativeProbe(t, clientB, probePort, 0x00)
	waitRelayCondition(t, 3*time.Second, "both probe endpoints to be observed", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})
	ageObservedProbeEndpoints(relay, 2*partyUDPRelayProbeTTL)
	if got := relay.observedProbeEndpointCount(); got != 0 {
		t.Fatalf("expired observed endpoints must not stay candidates: %d", got)
	}
	portAB, _ := syncTwoMemberRoom(t, relay)
	if _, err := clientA.WriteToUDP([]byte("A-first-after-ttl"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe queued", 3*time.Second)
	if payload := readClientDatagram(t, clientB, 500*time.Millisecond); payload != nil {
		t.Fatalf("expired endpoints must not receive traffic, got %q", payload)
	}
	if recorder.contains("kind observed_probe_endpoint") {
		t.Fatalf("expired endpoints must not be logged as candidates: %s", recorder.joined())
	}
}

// ⑱ 表必须有上限与"同端点刷新时间"语义（超出淘汰最旧、刷新挪到最前），否则它会无界增长。
func TestObservedProbeEndpointTableHonoursLimitAndRefresh(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, _ := newObservedEndpointRelay(t, recorder, true)
	const firstPort = 40001
	for index := 0; index < partyUDPRelayObservedEndpointLimit+3; index++ {
		relay.noteObservedProbeEndpoint(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: firstPort + index}, 0)
	}
	if got := relay.observedProbeEndpointCount(); got != partyUDPRelayObservedEndpointLimit {
		t.Fatalf("observed endpoint table size=%d want the cap %d", got, partyUDPRelayObservedEndpointLimit)
	}
	targets := relay.observedProbeEndpointTargets(nil)
	if targets[0].Port != firstPort+partyUDPRelayObservedEndpointLimit+2 {
		t.Fatalf("MRU order broken: newest first is %v", targets[0])
	}
	for _, target := range targets {
		if target.Port == firstPort {
			t.Fatalf("the oldest observed endpoint must be evicted: %v", targets)
		}
	}
	relay.noteObservedProbeEndpoint(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: firstPort + 4}, 0)
	if got := relay.observedProbeEndpointCount(); got != partyUDPRelayObservedEndpointLimit {
		t.Fatalf("refreshing an existing endpoint must not grow the table: %d", got)
	}
	if front := relay.observedProbeEndpointTargets(nil)[0].Port; front != firstPort+4 {
		t.Fatalf("a refreshed endpoint must move to the front, got %d", front)
	}
}

// ⑲ 新候选必须沿用既有的每腿写入上限（不得变成无限注入器）。
func TestObservedProbeEndpointFanOutHonoursSeedForwardCap(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newObservedEndpointRelay(t, recorder, true)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	sendOneByteNativeProbe(t, clientA, probePort, 0x01)
	sendOneByteNativeProbe(t, clientB, probePort, 0x00)
	waitRelayCondition(t, 3*time.Second, "both probe endpoints to be observed", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})
	portAB, _ := syncTwoMemberRoom(t, relay)
	// 把"投给 B 的那条腿"的候选写入数顶到上限。
	opposite := relayLegFor(t, relay, 202, 101)
	opposite.mu.Lock()
	opposite.seedForwardDatagrams = maxSeedForwards
	opposite.mu.Unlock()
	if _, err := clientA.WriteToUDP([]byte("A-first-at-cap"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay fanout exhausted", 3*time.Second)
	if payload := readClientDatagram(t, clientB, 500*time.Millisecond); payload != nil {
		t.Fatalf("the per-leg cap must stop candidate injection, got %q", payload)
	}
}

// ⑳ off 档必须逐字节回到改动前：**探针连表都不记**，首包照旧只留存/丢弃。
func TestObservedProbeEndpointFanOutOffKeepsLegacyBehaviour(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newObservedEndpointRelay(t, recorder, false)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	sendOneByteNativeProbe(t, clientA, probePort, 0x01)
	sendOneByteNativeProbe(t, clientB, probePort, 0x00)
	// 探针日志照旧（既有行为不变），但表必须是空的。
	waitRelayLog(t, recorder, "party UDP relay native probe observed", 3*time.Second)
	time.Sleep(200 * time.Millisecond)
	if got := relay.observedProbeEndpointCount(); got != 0 {
		t.Fatalf("with the switch off no probe endpoint may be recorded: %d", got)
	}
	portAB, _ := syncTwoMemberRoom(t, relay)
	if _, err := clientA.WriteToUDP([]byte("A-first-off"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe queued", 3*time.Second)
	if payload := readClientDatagram(t, clientB, 500*time.Millisecond); payload != nil {
		t.Fatalf("with the switch off B must stay silent, got %q", payload)
	}
	if recorder.contains("observed_probe_endpoint") {
		t.Fatalf("the off mode must not use the new candidate: %s", recorder.joined())
	}
	if stats := relayLegStatsFor(t, relay, 202, 101); stats.Received != 0 || stats.Forwarded != 0 {
		t.Fatalf("off mode must not deliver anything to the silent peer: %+v", stats)
	}
}

// ㉑ 新开关的解析：**默认 on**，只有显式 `off` 才关闭。
func TestObservedEndpointFanoutSwitchDefaults(t *testing.T) {
	for _, value := range []string{"", "on", "ON", " true ", "nonsense"} {
		if !readPartyUDPRelayObservedEndpointFanoutMode(value) {
			t.Fatalf("value %q must fall back to the default on", value)
		}
	}
	if readPartyUDPRelayObservedEndpointFanoutMode("off") || readPartyUDPRelayObservedEndpointFanoutMode(" OFF ") {
		t.Fatal("only an explicit off may disable observed endpoint fanout")
	}
}

// ==================== 第二步：从对端探测命中的那个监听口发出（02:28 现场） ====================
//
// 02:28 现场是"候选命中了、包也发出去了（delivered=true），但对面整场 0 包"：
// 队员的 P2P socket 只向我们的 2312 发过包 ⇒ 它只接受**来自 2312 口**的入向包，
// 而从腿口 31001 发出的那一份被加速器/NAT 丢掉。下面三条回归钉住"发送源口"这一环。

// ㉒ 决定性回归：投给无归属观测端点的那一份必须**从该端点探测命中的探针口发出**。
func TestObservedProbeEndpointDeliveryUsesTheProbeListenerSocket(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newObservedEndpointRelay(t, recorder, true)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	// 组队前：两台都在**同一个探针口**上被观测到过（现场 2312）。
	sendOneByteNativeProbe(t, clientA, probePort, 0x01)
	sendOneByteNativeProbe(t, clientB, probePort, 0x00)
	waitRelayCondition(t, 3*time.Second, "both probe endpoints to be observed", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})

	portAB, portBA := syncTwoMemberRoom(t, relay)
	handshake := probePayload(101)
	if _, err := clientA.WriteToUDP(handshake, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A handshake: %v", err)
	}
	// B 收到的那一份**必须来自探针口**（而不是腿口 portBA）——这就是 02:28 缺的那一步。
	payload, source := readClientDatagramWithSource(t, clientB, 3*time.Second)
	if !bytes.Equal(payload, handshake) {
		t.Fatalf("B did not receive the handshake: %q", payload)
	}
	if source == nil {
		t.Fatal("B received the handshake without a source address")
	}
	if source.Port != probePort {
		t.Fatalf("delivery source port=%d want the probe listener port %d (leg port is %d)",
			source.Port, probePort, portBA)
	}
	if !source.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("delivery source ip=%v want 127.0.0.1", source.IP)
	}
	// A 依旧收不到自己那份（发送者排除不受"换源口"影响）。
	if payload := readClientDatagram(t, clientA, 500*time.Millisecond); payload != nil {
		t.Fatalf("the relay must never send a datagram back to its sender, got %q", payload)
	}
	// 投递在 readLoop 里、日志在其后 ⇒ 等最后一条（datagram trace）落盘再断言其余字段。
	waitRelayLog(t, recorder, "delivery_source_port "+strconv.Itoa(probePort), 3*time.Second)
	if !recorder.contains("via_listener_port " + strconv.Itoa(probePort)) {
		t.Fatalf("the candidate log must report the probe listener port: %s", recorder.joined())
	}
	if !recorder.contains("delivery_source probe_listener_socket") {
		t.Fatalf("the candidate log must report the probe listener socket as the source: %s", recorder.joined())
	}
	if recorder.contains("fallback_reason") {
		t.Fatalf("no fallback may happen while the probe socket is alive: %s", recorder.joined())
	}
}

// ㉓ 探针 socket 已被显式关闭时：退化回腿口补发，并**具名**记因（绝不静默、绝不丢包）。
func TestObservedProbeEndpointDeliveryFallsBackWhenProbeSocketReleased(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePort := newObservedEndpointRelay(t, recorder, true)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	sendOneByteNativeProbe(t, clientA, probePort, 0x01)
	sendOneByteNativeProbe(t, clientB, probePort, 0x00)
	waitRelayCondition(t, 3*time.Second, "both probe endpoints to be observed", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})
	// 故障注入：显式关闭探针socket，验证投递回落；正常腿分配不得再抢占该口。
	if listeners := relay.probeListeners(); listeners == nil || !listeners.releasePort(probePort) {
		t.Fatal("releasePort must report the released probe socket")
	}
	portAB, portBA := syncTwoMemberRoom(t, relay)
	handshake := probePayload(101)
	if _, err := clientA.WriteToUDP(handshake, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A handshake: %v", err)
	}
	payload, source := readClientDatagramWithSource(t, clientB, 3*time.Second)
	if !bytes.Equal(payload, handshake) {
		t.Fatalf("B must still receive the handshake after the probe socket was released: %q", payload)
	}
	if source == nil || source.Port != int(portBA) {
		t.Fatalf("the fallback delivery must come from the leg port %d, got %v", portBA, source)
	}
	// 投递发生在 readLoop 里、日志在其后 ⇒ 必须等日志落盘再断言（不能只等数据报）。
	waitRelayLog(t, recorder, "fallback_reason "+observedDeliveryFallbackSocketReleased, 3*time.Second)
	if !recorder.contains("delivery_source leg_socket") {
		t.Fatalf("the fallback path must report the leg socket as the source: %s", recorder.joined())
	}
}

// ㉔ 同一源端点打到**多个探针口**时是"多条入向路径"，每条都要投（各自从命中的那个口发出）：
// 现场两台客户端都会往 2311 与 2312 各打一次，我们无法预先知道加速器放行哪一个。
func TestObservedProbeEndpointDeliveryFansOutAcrossEveryProbedListenerPort(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, probePorts := newObservedEndpointRelayWithProbePorts(t, recorder, true, []int{0, 0})
	if len(probePorts) != 2 {
		t.Fatalf("probe listener ports=%v want two", probePorts)
	}
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	// B 往两个探针口各打一次（payload `01` / `00` = 第几个通告口，与现场一致）；
	// A 只打一次（它只是发送者，会被排除）。
	sendOneByteNativeProbe(t, clientB, probePorts[0], 0x01)
	sendOneByteNativeProbe(t, clientB, probePorts[1], 0x00)
	sendOneByteNativeProbe(t, clientA, probePorts[1], 0x00)
	waitRelayCondition(t, 3*time.Second, "every probed listener port to be recorded", func() bool {
		return relay.observedProbeEndpointCount() == 3
	})
	portAB, portBA := syncTwoMemberRoom(t, relay)
	handshake := probePayload(101)
	if _, err := clientA.WriteToUDP(handshake, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A handshake: %v", err)
	}
	// B 收到的是 3 份：两条探针路径各一份（各自从命中的那个探针口发出）
	// + 腿口一份（同一条端点再从腿口投，见 ⑯b：op11 通告给对端的就是腿口）。
	sources := make(map[int]int, 3)
	for attempt := 0; attempt < 3; attempt++ {
		payload, source := readClientDatagramWithSource(t, clientB, 3*time.Second)
		if !bytes.Equal(payload, handshake) {
			t.Fatalf("attempt %d: unexpected payload %q", attempt, payload)
		}
		if source == nil {
			t.Fatalf("attempt %d: missing source address", attempt)
		}
		sources[source.Port]++
	}
	if sources[probePorts[0]] != 1 || sources[probePorts[1]] != 1 {
		t.Fatalf("B must receive one copy from each probed listener port %v, got %v", probePorts, sources)
	}
	if sources[int(portBA)] != 1 {
		t.Fatalf("B must also receive one copy from the leg port %d, got %v", portBA, sources)
	}
	for _, port := range probePorts {
		// 投递在 readLoop 里、日志在其后 ⇒ 等日志落盘再断言。
		waitRelayLog(t, recorder, "via_listener_port "+strconv.Itoa(port), 3*time.Second)
	}
}
