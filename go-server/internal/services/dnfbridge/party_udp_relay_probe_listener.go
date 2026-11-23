package dnfbridge

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const partyUDPRelayProbePortsEnv = "DNFBRIDGE_PARTY_UDP_RELAY_PROBE_PORTS"

// Native probe ports stay reserved for the relay lifetime. Their packets
// cannot authenticate a directed leg and never become forwarding targets.
type currentPartyUDPRelayProbeListeners struct {
	relay         *currentPartyUDPRelay
	mu            sync.Mutex
	conns         map[int]*net.UDPConn
	wg            sync.WaitGroup
	bound         []int
	reservedPorts map[int]struct{}
}

func (l *currentPartyUDPRelayProbeListeners) readLoop(_ int, conn *net.UDPConn) {
	defer l.wg.Done()
	var buffer [currentPartyUDPRelayMaxDatagram]byte
	for {
		if _, _, err := conn.ReadFromUDPAddrPort(buffer[:]); err != nil {
			return
		}
	}
}

func partyUDPRelayProbePorts(value string, portStart int) []int {
	ports := make([]int, 0, 2)
	seen := make(map[int]struct{}, 2)
	add := func(port int) {
		if port < 1 || port > 65535 {
			return
		}
		if _, exists := seen[port]; exists {
			return
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		add(portStart)
		return ports
	}
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		port, err := strconv.Atoi(token)
		if err != nil {
			continue
		}
		add(port)
	}
	return ports
}

func (r *currentPartyUDPRelay) startProbeListeners(ports []int) {
	if r == nil || !r.probeListen || len(ports) == 0 {
		return
	}
	listeners := &currentPartyUDPRelayProbeListeners{relay: r, conns: make(map[int]*net.UDPConn, len(ports)), reservedPorts: make(map[int]struct{}, len(ports))}
	bound := make([]int, 0, len(ports))
	failed := make([]int, 0, len(ports))
	for _, port := range ports {
		if port < 0 || port > 65535 {
			continue
		}
		if port != 0 {
			listeners.reservedPorts[port] = struct{}{}
		}
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: append(net.IP(nil), r.bindIP...), Port: port})
		if err != nil {
			failed = append(failed, port)
			continue
		}
		actual := conn.LocalAddr().(*net.UDPAddr).Port
		r.applySocketBuffer(conn, actual, "probe_listener")
		listeners.reservedPorts[actual] = struct{}{}
		listeners.conns[actual] = conn
		listeners.bound = append(listeners.bound, actual)
		bound = append(bound, actual)
	}
	r.mu.Lock()
	r.probeListener = listeners
	r.mu.Unlock()
	if len(bound) > 0 {
		r.log("party UDP relay native probe listener bound",
			"ports", formatPartyUDPRelayPorts(bound),
			"bind", r.bindAddressString(),
			"requested_socket_buffer_bytes", r.socketBufferBytes,
			"effective_rcvbuf_bytes", r.lastEffectiveRcvbuf.Load(),
			"effective_sndbuf_bytes", r.lastEffectiveSndbuf.Load(),
			"meaning", "reserved_native_probe_ports_never_used_for_directed_legs")
	}
	if len(failed) > 0 {
		r.log("party UDP relay native probe listener unavailable", "ports", formatPartyUDPRelayPorts(failed))
	}
	for port, conn := range listeners.conns {
		listeners.wg.Add(1)
		go listeners.readLoop(port, conn)
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		listeners.wg.Wait()
	}()
}

func (r *currentPartyUDPRelay) probeListenerPorts() []int {
	listeners := r.probeListeners()
	if listeners == nil {
		return nil
	}
	listeners.mu.Lock()
	defer listeners.mu.Unlock()
	ports := make([]int, 0, len(listeners.conns))
	for port := range listeners.conns {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

func formatPartyUDPRelayPorts(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return strings.Join(parts, ",")
}

func (l *currentPartyUDPRelayProbeListeners) releasePort(port int) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	conn, ok := l.conns[port]
	if ok {
		delete(l.conns, port)
	}
	l.mu.Unlock()
	if !ok || conn == nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (l *currentPartyUDPRelayProbeListeners) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	conns := make([]*net.UDPConn, 0, len(l.conns))
	for _, conn := range l.conns {
		conns = append(conns, conn)
	}
	l.conns = make(map[int]*net.UDPConn)
	l.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (r *currentPartyUDPRelay) probeListeners() *currentPartyUDPRelayProbeListeners {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.probeListener
}
