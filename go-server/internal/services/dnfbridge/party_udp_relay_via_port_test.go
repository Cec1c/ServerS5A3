package dnfbridge

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// 本文件钉住"共享端口档"（party_udp_relay_via_port.go）：
//   - off（默认，viaPort=0）→ `Endpoint()` 照旧返回**各自腿口**，转发函数入口即 return；
//   - on  → 两个方向都被通告**同一个共享端口**，且到达该端口的 `0x02` 载荷
//     会被**从该端口**转发给"除发送者以外的每一个已观测端点"，绝不回投给发送者；
//   - 非 `0x02` 载荷（1 字节原生探测）一律不转发（仍只登记）。

// newViaPortRelay 建一个 viaPort 档的中继：探针监听口 = 共享端口本身。
// 探针口是"先探测空闲再绑定"，可能被抢走，所以最多重试 5 轮。
func newViaPortRelay(t *testing.T, recorder *relayLogRecorder) (*currentPartyUDPRelay, int) {
	t.Helper()
	portStart := testCurrentPartyUDPRelayPortStart(t)
	const portCount = 12
	for attempt := 1; attempt <= 5; attempt++ {
		via := testProbeListenerPortOutsideLegRange(t, portStart, portCount)
		relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
			enabled:                true,
			advertiseIP:            "127.0.0.1",
			portStart:              portStart,
			portCount:              portCount,
			probeReplay:            true,
			probeListen:            true,
			probePorts:             []int{via},
			seedForward:            true,
			datagramTrace:          true,
			observedEndpointFanout: true,
			viaPort:                via,
			logWarn:                recorder.log,
		})
		if err != nil {
			t.Fatalf("new via-port relay: %v", err)
		}
		bound := relay.probeListenerPorts()
		if len(bound) == 1 && bound[0] == via {
			t.Cleanup(func() { _ = relay.Close() })
			return relay, via
		}
		_ = relay.Close()
	}
	t.Fatal("could not bind the shared via port after 5 attempts")
	return nil, 0
}

// TestViaPortEndpointOverrideAdvertisesTheSharedPortForBothDirections 钉住 op11 通告值的来源：
// off 档必须是**各自腿口**（现行为），on 档必须是**同一个共享端口**。
func TestViaPortEndpointOverrideAdvertisesTheSharedPortForBothDirections(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, via := newViaPortRelay(t, recorder)
	// 先在 off 档建队并取到**真实腿口**（此时 Endpoint 返回腿口，才是基线）。
	relay.mu.Lock()
	relay.viaPort = 0
	relay.mu.Unlock()
	portAB, portBA := syncTwoMemberRoom(t, relay)

	if _, port, ok := relay.Endpoint(77, 101, 202); !ok || port != portAB {
		t.Fatalf("off mode must advertise the directed leg port %d, got %d ok=%t", portAB, port, ok)
	}
	if _, port, ok := relay.Endpoint(77, 202, 101); !ok || port != portBA {
		t.Fatalf("off mode must advertise the directed leg port %d, got %d ok=%t", portBA, port, ok)
	}

	// on 档：两个方向都必须是同一个共享端口。
	relay.mu.Lock()
	relay.viaPort = via
	relay.mu.Unlock()
	if _, port, ok := relay.Endpoint(77, 101, 202); !ok || port != uint16(via) {
		t.Fatalf("via mode must advertise the shared port %d to member 101, got %d ok=%t", via, port, ok)
	}
	if _, port, ok := relay.Endpoint(77, 202, 101); !ok || port != uint16(via) {
		t.Fatalf("via mode must advertise the shared port %d to member 202, got %d ok=%t", via, port, ok)
	}
}

// TestViaPortForwardsP2PFromTheSharedPortToTheOtherEndpoint 是本文件的决定性用例：
// A 把 21B 握手发到共享端口 ⇒ B 必须**从共享端口**收到逐字节相同的一份，且 A 不得收到自己那份。
func TestViaPortForwardsP2PFromTheSharedPortToTheOtherEndpoint(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, via := newViaPortRelay(t, recorder)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()

	// 两端都向共享端口发过 1 字节保活 ⇒ 两个真实端点都进观测表（现场形态）。
	sendOneByteNativeProbe(t, clientA, via, 0x01)
	sendOneByteNativeProbe(t, clientB, via, 0x00)
	waitRelayCondition(t, 3*time.Second, "both endpoints observed on the shared port", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})

	handshake := probePayload(101)
	if _, err := clientA.WriteToUDP(handshake, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: via}); err != nil {
		t.Fatalf("send handshake to the shared port: %v", err)
	}
	payload, source := readClientDatagramWithSource(t, clientB, 3*time.Second)
	if !bytes.Equal(payload, handshake) {
		t.Fatalf("B must receive the handshake verbatim through the shared port, got %q", payload)
	}
	if source == nil || source.Port != via {
		t.Fatalf("the forward must come **from** the shared port %d (that is the advertised peer endpoint), got %v", via, source)
	}
	if payload := readClientDatagram(t, clientA, 300*time.Millisecond); payload != nil {
		t.Fatalf("the shared port must never echo a datagram back to its sender, got %q", payload)
	}
	if !recorder.contains("party UDP relay via port p2p forwarded") {
		t.Fatalf("the shared-port forward must be logged: %s", recorder.joined())
	}
}

// TestViaPortIgnoresNonP2PPayloads 钉住安全边界：1 字节原生探测仍只登记、绝不转发。
func TestViaPortIgnoresNonP2PPayloads(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, via := newViaPortRelay(t, recorder)
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	sendOneByteNativeProbe(t, clientA, via, 0x01)
	sendOneByteNativeProbe(t, clientB, via, 0x00)
	waitRelayCondition(t, 3*time.Second, "both endpoints observed", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})

	// 只有 1 字节探测：B 不应收到任何东西。
	if payload := readClientDatagram(t, clientB, 400*time.Millisecond); payload != nil {
		t.Fatalf("1-byte native probes must never be forwarded, got %q", payload)
	}
	// 首字节不是 0x02 的载荷同样不转发。
	if _, err := clientA.WriteToUDP([]byte("NOTP2P"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: via}); err != nil {
		t.Fatalf("send non-p2p payload: %v", err)
	}
	if payload := readClientDatagram(t, clientB, 400*time.Millisecond); payload != nil {
		t.Fatalf("a payload that is not in the 0x02 family must not be forwarded, got %q", payload)
	}
	if recorder.contains("party UDP relay via port p2p forwarded") {
		t.Fatalf("nothing should have been forwarded: %s", recorder.joined())
	}
}

// TestViaPortOffByDefaultDoesNotForward 钉住默认档（零值 = off）零副作用。
func TestViaPortOffByDefaultDoesNotForward(t *testing.T) {
	recorder := &relayLogRecorder{}
	relay, viaPorts := newObservedEndpointRelayWithProbePorts(t, recorder, true, []int{0})
	if len(viaPorts) != 1 {
		t.Fatalf("probe listener ports=%v want one", viaPorts)
	}
	via := viaPorts[0]
	// 该脚手架没有设置 viaPort（零值 = off）。用它的探针口当"共享端口"发一份 0x02 载荷。
	clientA := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientA.Close()
	clientB := testCurrentPartyUDPClient(t, "127.0.0.1")
	defer clientB.Close()
	sendOneByteNativeProbe(t, clientA, via, 0x01)
	sendOneByteNativeProbe(t, clientB, via, 0x00)
	waitRelayCondition(t, 3*time.Second, "both endpoints observed", func() bool {
		return relay.observedProbeEndpointCount() == 2
	})
	if _, err := clientA.WriteToUDP(probePayload(101), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: via}); err != nil {
		t.Fatalf("send handshake: %v", err)
	}
	if payload := readClientDatagram(t, clientB, 400*time.Millisecond); payload != nil {
		t.Fatalf("off mode must not forward anything, got %q", payload)
	}
	if recorder.contains("via port p2p forwarded") {
		t.Fatalf("off mode must not log a via-port forward: %s", recorder.joined())
	}
	if got := (&Service{}).partyUDPRelayViaPort(); got != 0 {
		t.Fatalf("zero-value service via port = %d, want 0", got)
	}
}
