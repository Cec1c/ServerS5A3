package dnfbridge

import (
	"net"
	"testing"
	"time"
)

// 本文件钉住 2026-09-25 "首包留存重放"（party_udp_relay_probe_replay.go）的语义。
//
// 现场依据（香港只读日志 `/opt/dnf90/logs/dnf90.out`，六次组队实测）：每次只有 **1 个**
// 数据报到达中继并被 `unlearned_peer_endpoint` 丢掉，此后整场零流量
// （`legs closed … idle_legs=2 received_datagrams=1 leg_forwarded_datagrams=0`）。
// 因此这里的两条主回归是：
//
//	off 档 = 那唯一一个首包仍然被丢（逐字节旧行为）；
//	on  档 = 那唯一一个首包在对端发包的瞬间被原样补投过去（死锁被打破）。

// probeReplayFixture 未使用（保留构造器路径的单点入口，便于以后扩展多腿用例）。
func newProbeReplayRelay(t *testing.T, recorder *relayLogRecorder, enabled bool) *currentPartyUDPRelay {
	t.Helper()
	portStart := testCurrentPartyUDPRelayPortStart(t)
	t.Logf("UDP probe replay fixture: bindIP=127.0.0.1 portStart=%d portCount=12 range=%d-%d partyID=77 bindings=2 probeReplay=%t", portStart, portStart, portStart+11, enabled)
	// Preserve the real relay error: the shared recorder otherwise renders error as "?".
	logRelay := func(message string, args ...any) {
		values := append([]any(nil), args...)
		for index, value := range values {
			if err, ok := value.(error); ok {
				values[index] = err.Error()
			}
		}
		recorder.log(message, values...)
	}
	relay, err := newCurrentPartyUDPRelayWithProbeReplay(true, "127.0.0.1", portStart, 12, false, enabled, logRelay)
	if err != nil {
		t.Fatalf("new UDP relay: %v; bindIP=127.0.0.1 portStart=%d portCount=12 range=%d-%d probeReplay=%t logs=%s", err, portStart, portStart, portStart+11, enabled, recorder.joined())
	}
	t.Cleanup(func() { _ = relay.Close() })
	bindings := []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 1, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 1, address: [4]byte{127, 0, 0, 1}},
	}
	if !relay.Sync(77, bindings) {
		t.Fatalf("relay did not create a complete two-member room: bindIP=127.0.0.1 portStart=%d portCount=12 range=%d-%d partyID=77 bindings=2 probeReplay=%t logs=%s", portStart, portStart, portStart+11, enabled, recorder.joined())
	}
	return relay
}

// 客户端在某条腿上收一个数据报（带超时，收到返回 payload）。
func readClientDatagram(t *testing.T, client *net.UDPConn, timeout time.Duration) []byte {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(timeout))
	buffer := make([]byte, 2048)
	read, _, err := client.ReadFromUDP(buffer)
	if err != nil {
		return nil
	}
	return append([]byte(nil), buffer[:read]...)
}

// off 档必须与加开关之前逐字节一致：那唯一一个首包被丢弃、零转发、零补投、零留存。
func TestProbeReplayOffKeepsLegacySingleDatagramDrop(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay := newProbeReplayRelay(t, recorder, false)
	_, portAB, _ := relay.Endpoint(77, 101, 202)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	if _, err := clientA.WriteToUDP([]byte("A-only-first-datagram"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay peer endpoint not learned", 3*time.Second)
	stats := relayLegStatsFor(t, relay, 101, 202)
	if stats.UnlearnedPeerEndpoint != 1 {
		t.Fatalf("unlearned peer endpoint counter=%d want 1: %+v", stats.UnlearnedPeerEndpoint, stats)
	}
	if stats.Forwarded != 0 || stats.ReplayedDatagrams != 0 {
		t.Fatalf("off mode must not forward or replay: %+v", stats)
	}
	if stats.PendingDatagrams != 0 {
		t.Fatalf("off mode must not hold the datagram: %+v", stats)
	}
	if relay.ForwardedDatagramTotal() != 0 {
		t.Fatalf("off mode forwarded total=%d want 0", relay.ForwardedDatagramTotal())
	}
}

// on 档核心回归：A 的首包先到（对向腿未学习）时被**留存**而不是丢掉；
// B 一旦发出自己的第一个包，A 的首包立刻被原样补投给 B —— 这就是死锁被打破的那一刻。
func TestProbeReplayDeliversHeldFirstDatagramWhenPeerFinallySends(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay := newProbeReplayRelay(t, recorder, true)
	_, portAB, okAB := relay.Endpoint(77, 101, 202)
	_, portBA, okBA := relay.Endpoint(77, 202, 101)
	if !okAB || !okBA {
		t.Fatal("directed relay endpoints missing")
	}
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()

	// ① A 只发一个包（现场就是这样：整场只有 1 个数据报）。
	if _, err := clientA.WriteToUDP([]byte("A-held-first"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe queued", 3*time.Second)
	if stats := relayLegStatsFor(t, relay, 202, 101); stats.PendingDatagrams != 1 {
		t.Fatalf("held datagram missing on the writing leg: %+v", stats)
	}

	// B 还没发过任何东西 ⇒ 此时 A 收不到任何数据报（旧行为，必须保持）。
	if payload := readClientDatagram(t, clientA, 200*time.Millisecond); payload != nil {
		t.Fatalf("A must not receive anything before B speaks, got %q", payload)
	}

	// ② B 发出自己的第一个包。
	if _, err := clientB.WriteToUDP([]byte("B-first"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe replayed", 3*time.Second)

	// B 必须收到被留存的那一条（旧实现在这里永远是超时）。
	if payload := readClientDatagram(t, clientB, 2*time.Second); string(payload) != "A-held-first" {
		t.Fatalf("B did not receive the replayed held datagram: %q", payload)
	}
	// 同时 A 必须收到 B 的存活包（正常转发路径不受影响）。
	if payload := readClientDatagram(t, clientA, 2*time.Second); string(payload) != "B-first" {
		t.Fatalf("A received an unexpected datagram: %q", payload)
	}

	stats := relayLegStatsFor(t, relay, 202, 101)
	if stats.ReplayedDatagrams != 1 {
		t.Fatalf("replayed datagrams=%d want 1: %+v", stats.ReplayedDatagrams, stats)
	}
	if stats.PendingDatagrams != 0 {
		t.Fatalf("held queue must be drained after the replay: %+v", stats)
	}
}

// 留存必须有硬上限：对端整场不发时不能变成无界内存/注入器。
func TestProbeReplayQueueIsBounded(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay := newProbeReplayRelay(t, recorder, true)
	_, portAB, okAB := relay.Endpoint(77, 101, 202)
	_, portBA, okBA := relay.Endpoint(77, 202, 101)
	if !okAB || !okBA {
		t.Fatal("directed relay endpoints missing")
	}
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}
	for index := 0; index < maxPendingDatagrams*3; index++ {
		if _, err := clientA.WriteToUDP([]byte("held-payload"), target); err != nil {
			t.Fatalf("send A datagram %d: %v", index, err)
		}
	}
	waitRelayCondition(t, 3*time.Second, "held queue to reach the cap", func() bool {
		stats := relayLegStatsFor(t, relay, 202, 101)
		return stats.PendingDropped > 0
	})
	stats := relayLegStatsFor(t, relay, 202, 101)
	if stats.PendingDatagrams > maxPendingDatagrams {
		t.Fatalf("held queue=%d exceeds cap %d: %+v", stats.PendingDatagrams, maxPendingDatagrams, stats)
	}
	if stats.PendingBytes > maxPendingBytes {
		t.Fatalf("held bytes=%d exceeds cap %d: %+v", stats.PendingBytes, maxPendingBytes, stats)
	}
	if stats.PendingDropped == 0 {
		t.Fatalf("oldest held datagrams must be dropped once the cap is hit: %+v", stats)
	}

	// B 发包后最多补投 cap 条，绝不超过留存上限。
	if _, err := clientB.WriteToUDP([]byte("B-first"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B first datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe replayed", 3*time.Second)
	stats = relayLegStatsFor(t, relay, 202, 101)
	if stats.ReplayedDatagrams > maxPendingDatagrams {
		t.Fatalf("replayed=%d exceeds cap %d", stats.ReplayedDatagrams, maxPendingDatagrams)
	}
}

// 来源 IP 不匹配的数据报必须**在校验处就被挡掉**，绝不能进留存队列（安全阀语义不变）。
func TestProbeReplayNeverQueuesForgedSource(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay := newProbeReplayRelay(t, recorder, true)
	_, portAB, _ := relay.Endpoint(77, 101, 202)
	forged := testCurrentPartyUDPClient(t, "127.0.0.2")
	defer forged.Close()
	if _, err := forged.WriteToUDP([]byte("forged"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send forged datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay source rejected", 3*time.Second)
	if stats := relayLegStatsFor(t, relay, 101, 202); stats.RejectedSource == 0 {
		t.Fatalf("forged source counter stayed 0: %+v", stats)
	}
	if stats := relayLegStatsFor(t, relay, 202, 101); stats.PendingDatagrams != 0 {
		t.Fatalf("forged datagram reached the held queue: %+v", stats)
	}
}

// 备份端点（同角色上次自己发过的真实端点）只在该角色**自己发过**、且 IP 与其
// 游戏 TCP 会话观察到的 IPv4 完全一致时才可用：证明它不是"客户端自报 IP"。
func TestProbeReplayEndpointMemoRequiresIdenticalObservedIP(t *testing.T) {
	memo := currentPartyUDPRelayEndpointMemo{
		valid:   true,
		address: [4]byte{127, 0, 0, 1},
		port:    45678,
		seenAt:  time.Now(),
	}
	if port, ok := memo.portFor([4]byte{127, 0, 0, 1}); !ok || port != 45678 {
		t.Fatalf("memo must answer for the same observed IPv4, got port=%d ok=%v", port, ok)
	}
	if _, ok := memo.portFor([4]byte{10, 0, 0, 1}); ok {
		t.Fatal("memo must never answer for a different observed IPv4")
	}
	expired := memo
	expired.seenAt = time.Now().Add(-2 * memoTTL)
	if _, ok := expired.portFor([4]byte{127, 0, 0, 1}); ok {
		t.Fatal("expired memo must not be used")
	}
	zero := memo
	zero.port = 0
	if _, ok := zero.portFor([4]byte{127, 0, 0, 1}); ok {
		t.Fatal("memo with a zero port must not be used")
	}
}

// 备忘引导：角色自己在上一场发过端点，下一场它沉默时，中继能用那个**真实观测过的**
// 端点把队友的首包送进去（现场 NAT 端口跨会话稳定：19:08 与 19:29 都是 21281）。
func TestProbeReplayEndpointMemoBootstrapsNextParty(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay := newProbeReplayRelay(t, recorder, true)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()

	// 第一场：B 自己发一个包 ⇒ 中继记住 B 的真实端点。
	_, portBA, okBA := relay.Endpoint(77, 202, 101)
	if !okBA {
		t.Fatal("directed relay endpoint B->A missing")
	}
	if _, err := clientB.WriteToUDP([]byte("B-session-one"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B session one datagram: %v", err)
	}
	waitRelayCondition(t, 3*time.Second, "B endpoint memo", func() bool {
		relay.memoMu.Lock()
		defer relay.memoMu.Unlock()
		memo, ok := relay.endpointMemo[202]
		return ok && memo.valid && memo.port != 0
	})
	relay.CloseParty(77)
	// 清掉 A 的收包队列，避免把上一场的残留当成这一场的证据。
	_ = readClientDatagram(t, clientB, 100*time.Millisecond)

	// 第二场（新 party id ⇒ 新端口），B **整场不发**。
	if !relay.Sync(78, []currentPartyUDPRelayBinding{
		{characterID: 101, generation: 2, address: [4]byte{127, 0, 0, 1}},
		{characterID: 202, generation: 2, address: [4]byte{127, 0, 0, 1}},
	}) {
		t.Fatal("second party sync failed")
	}
	_, portAB2, _ := relay.Endpoint(78, 101, 202)
	if _, err := clientA.WriteToUDP([]byte("A-second-party-probe"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB2)}); err != nil {
		t.Fatalf("send A second party probe: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay endpoint memo bootstrap used", 3*time.Second)
	// B 必须真的收到 A 的那一条（否则"唤醒沉默端"只是日志上的假阳性）。
	got := readClientDatagram(t, clientB, 2*time.Second)
	if string(got) != "A-second-party-probe" {
		t.Fatalf("B was not woken by the memo bootstrap, got %q", got)
	}
}

// 备忘引导有每腿写入上限：猜错时不能变成长期注入器。
func TestProbeReplayEndpointMemoBootstrapIsBounded(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer conn.Close()
	leg := &currentPartyUDPRelayLeg{conn: conn, binding: currentPartyUDPRelayBinding{address: [4]byte{127, 0, 0, 1}}}
	memo := &currentPartyUDPRelayEndpointMemo{valid: true, address: [4]byte{127, 0, 0, 1}, port: 45678, seenAt: time.Now()}
	for index := 0; index < maxMemoBootstraps; index++ {
		if _, ok := leg.memoBootstrapTarget(memo); !ok {
			t.Fatalf("memo bootstrap %d must be allowed", index)
		}
	}
	if _, ok := leg.memoBootstrapTarget(memo); ok {
		t.Fatal("memo bootstrap must stop at the per-leg cap")
	}
	if leg.memoBootstraps != maxMemoBootstraps {
		t.Fatalf("memo bootstrap writes=%d want %d", leg.memoBootstraps, maxMemoBootstraps)
	}
	// 已经学过端点的腿绝不再用备忘引导（真实端点永远优先）。
	learned := &currentPartyUDPRelayLeg{conn: conn, binding: currentPartyUDPRelayBinding{address: [4]byte{127, 0, 0, 1}}}
	learned.learnedEndpoint = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
	if _, ok := learned.memoBootstrapTarget(memo); ok {
		t.Fatal("a leg that already learned its endpoint must never use the memo")
	}
	// 没有 socket 的腿（已关闭）不得引导。
	if _, ok := (&currentPartyUDPRelayLeg{binding: currentPartyUDPRelayBinding{address: [4]byte{127, 0, 0, 1}}}).memoBootstrapTarget(memo); ok {
		t.Fatal("a leg without a socket must never use the memo")
	}
}

// 队列 TTL 到期必须报出来：出现它就是"这一端整场一个数据报都没发"的机器证据。
func TestProbeReplayExpiryReportsSilentPeer(t *testing.T) {
	leg := &currentPartyUDPRelayLeg{key: currentPartyUDPRelayLegKey{partyID: 77, ownerID: 202, peerID: 101}}
	leg.queuePendingProbe([]byte("held"))
	if _, count, ok := leg.peekPendingProbeExpiry(); ok || count != 0 {
		t.Fatal("a fresh queue must not be reported as expired")
	}
	leg.mu.Lock()
	leg.pending.since = time.Now().Add(-2 * pendingTTL)
	leg.mu.Unlock()
	expired, count, ok := leg.peekPendingProbeExpiry()
	if !ok || count != 1 || expired != 1 {
		t.Fatalf("expiry=(%d,%d,%v) want (1,1,true)", expired, count, ok)
	}
	if _, count, ok := leg.peekPendingProbeExpiry(); ok || count != 0 {
		t.Fatal("expiry must only be reported once per drained queue")
	}
}

// 关闭汇总必须带上新计数与档位，且旧字段一个不少（判读串 party UDP relay legs closed）。
func TestProbeReplayCloseSummaryCarriesNewCounters(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay := newProbeReplayRelay(t, recorder, true)
	_, portAB, okAB := relay.Endpoint(77, 101, 202)
	_, portBA, okBA := relay.Endpoint(77, 202, 101)
	if !okAB || !okBA {
		t.Fatal("directed relay endpoints missing")
	}
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	if _, err := clientA.WriteToUDP([]byte("A-held"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send A datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe queued", 3*time.Second)
	if _, err := clientB.WriteToUDP([]byte("B-live"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}); err != nil {
		t.Fatalf("send B datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay probe replayed", 3*time.Second)
	relay.CloseParty(77)
	if !recorder.contains("party UDP relay legs closed") {
		t.Fatalf("close summary missing:\n%s", recorder.joined())
	}
	for _, field := range []string{
		// 旧字段（语义一字未动）
		"received_datagrams",
		"leg_forwarded_datagrams",
		"dropped_unlearned_peer_endpoint",
		"endpoint_fallback_mode",
		// 新字段
		"probe_replay_mode",
		"replayed_probe_datagrams",
		"dropped_pending_probe_datagrams",
		"expired_probe_datagrams",
		"endpoint_memo_bootstrap_datagrams",
		"endpoint_memo_bootstrap_failed",
	} {
		if !recorder.contains(field) {
			t.Fatalf("close summary missing %s:\n%s", field, recorder.joined())
		}
	}
	if !recorder.contains("probe_replay_mode on") {
		t.Fatalf("close summary must report probe_replay_mode on:\n%s", recorder.joined())
	}
}

// 腿建立日志必须带上 probe_replay 档位，线上一次判读即可确认开关是否真的生效。
func TestProbeReplayLegCreationLogCarriesMode(t *testing.T) {
	recorder := &relayLogRecorder{}
	_ = newProbeReplayRelay(t, recorder, true)
	if !recorder.contains("probe_replay on") {
		t.Fatalf("leg creation log must carry probe_replay on:\n%s", recorder.joined())
	}
}

// 开关解析：空/未设置 = on（修法生效）；只有显式 `off` 才回退；options 零值仍是旧行为。
func TestProbeReplaySwitchDefaultsAndReadback(t *testing.T) {
	for _, value := range []string{"", "  ", "on", "ON", "On", "enabled", "1", "true", "advertised", "disabled"} {
		if got := readPartyUDPRelayProbeReplayMode(value); got != partyUDPRelayProbeReplayEnabled {
			t.Fatalf("value %q must fall back to on, got %s", value, got)
		}
	}
	for _, value := range []string{"off", "OFF", " off ", "Off"} {
		if got := readPartyUDPRelayProbeReplayMode(value); got != partyUDPRelayProbeReplayDisabled {
			t.Fatalf("value %q must select off, got %s", value, got)
		}
	}
	if got := readPartyUDPRelayProbeReplayMode(""); got.String() != partyUDPRelayProbeReplayValueOn {
		t.Fatalf("default label=%q want %q", got.String(), partyUDPRelayProbeReplayValueOn)
	}
	// 零值 options（既有单测与探针的构造路径）= 旧行为，逐字节不变。
	if got := (*Service)(nil).partyUDPRelayProbeReplayMode(); got.enabled() {
		t.Fatal("nil Service must read as off")
	}
	var empty Service
	if got := empty.partyUDPRelayProbeReplayMode(); got.enabled() {
		t.Fatal("zero-value options must read as off so existing tests stay byte-identical")
	}
}
