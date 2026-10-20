package dnfbridge

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// relayLogRecorder 收集 relay 的 logWarn 输出，用于断言"每条丢弃路径都真的记了日志"。
type relayLogRecorder struct {
	mu       sync.Mutex
	messages []string
}

func (recorder *relayLogRecorder) log(message string, args ...any) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	line := message
	for index := 0; index < len(args); index++ {
		line += " " + formatRelayLogValue(args[index])
	}
	recorder.messages = append(recorder.messages, line)
}

func (recorder *relayLogRecorder) contains(substring string) bool {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, message := range recorder.messages {
		if bytes.Contains([]byte(message), []byte(substring)) {
			return true
		}
	}
	return false
}

func (recorder *relayLogRecorder) joined() string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var buffer bytes.Buffer
	for _, message := range recorder.messages {
		buffer.WriteString(message)
		buffer.WriteString("\n")
	}
	return buffer.String()
}

func formatRelayLogValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case uint16:
		return itoaRelayLog(int(typed))
	case uint64:
		return itoaRelayLog(int(typed))
	case int:
		return itoaRelayLog(typed)
	default:
		return "?"
	}
}

func itoaRelayLog(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// relayDiagnosticsFixture 造一对 127.0.0.1 上的腿，并把日志收进 recorder。
func relayDiagnosticsFixture(t *testing.T, recorder *relayLogRecorder) (*currentPartyUDPRelay, uint16, uint16) {
	t.Helper()
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", portStart, 12, recorder.log)
	if err != nil {
		t.Fatalf("new UDP relay: %v", err)
	}
	t.Cleanup(func() { _ = relay.Close() })
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
	return relay, portAB, portBA
}

// 找到某一条腿的统计快照（按 (owner,peer) 定向键，不受端口影响）。
func relayLegStatsFor(t *testing.T, relay *currentPartyUDPRelay, ownerID, peerID uint16) relayLegStats {
	t.Helper()
	relay.mu.Lock()
	leg := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: ownerID, peerID: peerID}]
	relay.mu.Unlock()
	if leg == nil {
		t.Fatalf("relay leg %d->%d missing", ownerID, peerID)
	}
	return leg.relayLegStatsSnapshot()
}

func waitRelayCondition(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitRelayLog 等一条日志真的落盘。计数是在 readLoop 里**先加、后记日志**，所以要断言
// "日志里有"就必须等日志本身，不能只等计数（否则测试自己制造竞态）。
func waitRelayLog(t *testing.T, recorder *relayLogRecorder, substring string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if recorder.contains(substring) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for log %q:\n%s", substring, recorder.joined())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 腿建立时必须把 expected（= 该会话游戏 TCP 连接看到的对端 IPv4）与 bind/advertise
// （= relay 实际 ListenUDP 的地址 / 写进 op11 的地址，都取自 DNFBRIDGE_SERVER_IP）
// 写进日志，这样线上一次判读就能把"CS op2 上报的 endpoint"与"中继真正校验/绑定的地址"对齐。
func TestCurrentPartyUDPRelayLogsLegExpectedAddressOnCreation(t *testing.T) {
	recorder := &relayLogRecorder{}
	_, _, _ = relayDiagnosticsFixture(t, recorder)
	if !recorder.contains("party UDP relay leg listening") {
		t.Fatalf("leg creation log missing:\n%s", recorder.joined())
	}
	if !recorder.contains("expected 127.0.0.1") {
		t.Fatalf("leg creation log must carry expected=:\n%s", recorder.joined())
	}
	if !recorder.contains("bind 127.0.0.1") || !recorder.contains("advertise 127.0.0.1") {
		t.Fatalf("leg creation log must carry bind=/advertise=:\n%s", recorder.joined())
	}
}

// 源 IP 不匹配的数据报：必须计数 + 记一条 `party UDP relay source rejected`，
// 并且**一个字节都不转发**（安全阀语义不变）。
func TestCurrentPartyUDPRelayCountsRejectedSourceDatagrams(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, portAB, _ := relayDiagnosticsFixture(t, recorder)
	forged := testCurrentPartyUDPClient(t, "127.0.0.2")
	defer forged.Close()
	if _, err := forged.WriteToUDP([]byte("forged"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send forged datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay source rejected", 3*time.Second)
	stats := relayLegStatsFor(t, relay, 101, 202)
	if stats.RejectedSource == 0 {
		t.Fatalf("rejected source counter stayed 0: %+v", stats)
	}
	if stats.Forwarded != 0 || stats.Received != 0 {
		t.Fatalf("forged datagram must not be counted as received/forwarded: %+v", stats)
	}
	if stats.ExpectedIPv4 != "127.0.0.1" {
		t.Fatalf("leg expected=%q want 127.0.0.1", stats.ExpectedIPv4)
	}
	if !recorder.contains("expected 127.0.0.1") {
		t.Fatalf("source rejection log must carry expected=:\n%s", recorder.joined())
	}
	if relay.ForwardedDatagramTotal() != 0 {
		t.Fatalf("relay forwarded %d datagrams from a forged source", relay.ForwardedDatagramTotal())
	}
}

// 对面那条腿还没发过任何数据报 ⇒ 没学到 endpoint ⇒ 本方向的包必须被计数并记日志，
// 这条静默黑洞正是"通道建了但队友不动"最可能的形态。
func TestCurrentPartyUDPRelayCountsUnlearnedPeerEndpoint(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, portAB, portBA := relayDiagnosticsFixture(t, recorder)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	endpointAB := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}
	endpointBA := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portBA)}

	// B 还没发过 ⇒ A 这一包落在"对面 endpoint 未学到"。
	if _, err := clientA.WriteToUDP([]byte("A-early"), endpointAB); err != nil {
		t.Fatalf("send A early payload: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay peer endpoint not learned", 3*time.Second)
	stats := relayLegStatsFor(t, relay, 101, 202)
	if stats.UnlearnedPeerEndpoint == 0 {
		t.Fatalf("unlearned peer endpoint counter stayed 0: %+v", stats)
	}
	if stats.Forwarded != 0 {
		t.Fatalf("datagram with an unlearned peer endpoint must not be forwarded: %+v", stats)
	}
	if stats.Received != 1 {
		t.Fatalf("received=%d want 1 (source matched, dropped later)", stats.Received)
	}

	// B 先发一条把自己的 endpoint 教给中继，A 的后续数据报就应该真的到达 B。
	if _, err := clientB.WriteToUDP([]byte("b-ready"), endpointBA); err != nil {
		t.Fatalf("send B handshake: %v", err)
	}
	buffer := make([]byte, 64)
	waitRelayCondition(t, 5*time.Second, "relayed payload after the peer endpoint is learned", func() bool {
		if _, err := clientA.WriteToUDP([]byte("A-late"), endpointAB); err != nil {
			t.Fatalf("send A late payload: %v", err)
		}
		if err := clientB.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
			t.Fatalf("set client B deadline: %v", err)
		}
		n, _, err := clientB.ReadFromUDP(buffer)
		return err == nil && n > 0
	})
	if !bytes.Equal(buffer[:6], []byte("A-late")) {
		t.Fatalf("relayed payload = %q, want A-late", buffer[:6])
	}
	// 计数由转发腿的 goroutine 在 WriteToUDP 之后累加，而 B 可能在累加完成前就
	// 读到包；这里等一下这个累加，判据不变（全包回归时偶发过一次假红）。
	waitRelayCondition(t, 2*time.Second, "forwarded datagram total is counted", func() bool {
		return relay.ForwardedDatagramTotal() > 0
	})
}

// 空载荷数据报也必须计数 + 记日志（旧实现把它和源不匹配合成一条静默 continue）。
func TestCurrentPartyUDPRelayCountsEmptyPayloadDatagrams(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, portAB, _ := relayDiagnosticsFixture(t, recorder)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	if _, err := clientA.WriteToUDP(nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send zero-length datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay empty payload dropped", 3*time.Second)
	if stats := relayLegStatsFor(t, relay, 101, 202); stats.EmptyPayload == 0 {
		t.Fatalf("empty payload counter stayed 0: %+v", stats)
	}
}

// opposite leg 缺失（队伍成员变更/关队的瞬态）：必须计数 + 记日志，绝不静默丢。
func TestCurrentPartyUDPRelayCountsMissingOppositeLeg(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, portAB, _ := relayDiagnosticsFixture(t, recorder)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	// 摘掉反向腿，模拟"广播过程中队伍成员变更/关队"的瞬态。socket 也要关，
	// 否则它的 readLoop 会一直阻塞在 ReadFromUDP 上，relay.Close() 的 wg.Wait() 永不返回。
	relay.mu.Lock()
	orphan := relay.legs[currentPartyUDPRelayLegKey{partyID: 77, ownerID: 202, peerID: 101}]
	delete(relay.legs, currentPartyUDPRelayLegKey{partyID: 77, ownerID: 202, peerID: 101})
	relay.mu.Unlock()
	if orphan == nil {
		t.Fatal("reverse relay leg missing before the test removed it")
	}
	_ = orphan.conn.Close()
	if _, err := clientA.WriteToUDP([]byte("A-orphan"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send orphan payload: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay opposite leg missing", 3*time.Second)
	stats := relayLegStatsFor(t, relay, 101, 202)
	if stats.MissingOppositeLeg == 0 {
		t.Fatalf("missing opposite leg counter stayed 0: %+v", stats)
	}
	if stats.Forwarded != 0 {
		t.Fatalf("datagram without an opposite leg must not be forwarded: %+v", stats)
	}
}

// 关闭汇总必须带上每条丢弃路径的条数（判读串 party UDP relay legs closed）。
func TestCurrentPartyUDPRelayCloseSummaryCarriesDropCounters(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, portAB, _ := relayDiagnosticsFixture(t, recorder)
	forged := testCurrentPartyUDPClient(t, "127.0.0.2")
	defer forged.Close()
	if _, err := forged.WriteToUDP([]byte("forged"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(portAB)}); err != nil {
		t.Fatalf("send forged datagram: %v", err)
	}
	waitRelayLog(t, recorder, "party UDP relay source rejected", 3*time.Second)
	relay.CloseParty(77)
	if !recorder.contains("party UDP relay legs closed") {
		t.Fatalf("close summary missing:\n%s", recorder.joined())
	}
	for _, field := range []string{
		"rejected_source_datagrams",
		"dropped_empty_datagrams",
		"dropped_missing_opposite_leg",
		"dropped_unlearned_peer_endpoint",
		"received_datagrams",
	} {
		if !recorder.contains(field) {
			t.Fatalf("close summary missing %s:\n%s", field, recorder.joined())
		}
	}
}
