package partyudprelay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxDatagramBytes        = 2048
	defaultLeaseTTL         = 30 * time.Second
	defaultPartyCooldown    = 60 * time.Second
	defaultPVPCooldown      = 180 * time.Second
	defaultWriteTimeout     = 250 * time.Millisecond
	defaultControlWriteWait = 2 * time.Second
	defaultHelloReadWait    = 5 * time.Second
	defaultMaxPPS           = 3000
	initialBufferWindow     = 5 * time.Second
)

var relayClockOrigin = time.Now()

// Config configures a standalone v2 party UDP relay server.
type Config struct {
	ControlListen     string
	UDPIp             string
	PortMin           int
	PortMax           int
	Secret            []byte
	LeaseTTL          time.Duration
	PartyCooldown     time.Duration
	PVPCooldown       time.Duration
	WriteTimeout      time.Duration
	MaxPPS            int
	SocketBufferBytes int
	Now               func() time.Time
	Logger            *slog.Logger
}

type roomKey struct {
	scope  byte
	roomID uint32
}

type legPair struct {
	owner uint16
	peer  uint16
}

type initialDatagram struct {
	state   atomic.Uint32 // 0 free, 1 writing, 2 ready, 3 consumed
	sender  *relayLeg
	created time.Time
	size    int
	data    [maxDatagramBytes]byte
}

type legCounters struct {
	received, forwarded, bytes, empty, rejected, missing, unlearned atomic.Uint64
	limited, failed                                                 atomic.Uint64
}

type relayLeg struct {
	writeDeadline atomic.Int64 // ns since relayClockOrigin
	deadlineMu    sync.Mutex
	opposite      atomic.Pointer[relayLeg]
	retired       atomic.Bool
	// learned packs (uint64(ipv4BE) << 16) | uint64(port). Zero until learned.
	// Once IPv4 is learned from the first valid datagram, same-IP port rebinds
	// (Symmetric NAT) update the port while foreign IPv4 packets are rejected.
	learned atomic.Uint64
	traffic legCounters
	initial [4]initialDatagram

	room roomKey
	pair legPair
	conn *net.UDPConn
	port uint16
}

type roomState struct {
	key       roomKey
	legs      map[legPair]*relayLeg
	expiresAt time.Time
}

// Stats exposes atomic relay counters for observability and tests.
type Stats struct {
	ActiveRooms     int
	ActiveLegs      int
	Forwarded       uint64
	RejectedSource  uint64
	RateLimited     uint64
	WriteFailed     uint64
	ExpiredRooms    uint64
	HelloAccepted   uint64
	HelloRejected   uint64
	SyncSucceeded   uint64
	SyncFailed      uint64
	RoomsClosed     uint64
}

// Server is a minimal standalone Go UDP relay speaking the v2 control protocol.
type Server struct {
	bindIP            net.IP
	portMin           int
	portCount         int
	secret            []byte
	leaseTTL          time.Duration
	partyCooldown     time.Duration
	pvpCooldown       time.Duration
	writeTimeout      time.Duration
	maxPPS            int
	socketBufferBytes int
	nowFn             func() time.Time
	logger            *slog.Logger

	listener net.Listener

	// Lock order: syncMu -> mu. Data-plane readLoop never acquires either lock.
	syncMu          sync.Mutex
	mu              sync.Mutex
	closed          bool
	usedPorts       map[int]struct{}
	portAvailableAt map[int]time.Time
	nextPort        int
	rooms           map[roomKey]*roomState
	controlConns    map[net.Conn]struct{}

	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup

	forwardedTotal atomic.Uint64
	rejectedTotal  atomic.Uint64
	limitedTotal   atomic.Uint64
	failedTotal    atomic.Uint64
	expiredRooms   atomic.Uint64
	helloAccepted  atomic.Uint64
	helloRejected  atomic.Uint64
	syncSucceeded  atomic.Uint64
	syncFailed     atomic.Uint64
	roomsClosed    atomic.Uint64
}

// Start validates cfg, opens the TCP control listener, and starts background loops.
func Start(cfg Config) (*Server, error) {
	if len(cfg.Secret) != SecretByteLength {
		return nil, errors.New("party udp relay requires a 32-byte shared secret")
	}
	ip := net.ParseIP(cfg.UDPIp)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() {
		return nil, fmt.Errorf("party udp relay requires a concrete IPv4 udp bind address, got %q", cfg.UDPIp)
	}
	portCount := cfg.PortMax - cfg.PortMin + 1
	if cfg.PortMin < 1 || cfg.PortMax > 65535 || portCount < 12 {
		return nil, fmt.Errorf("party udp relay port range %d-%d is invalid (need at least 12 ports)", cfg.PortMin, cfg.PortMax)
	}
	if cfg.ControlListen == "" {
		return nil, errors.New("party udp relay requires a non-empty control listen address")
	}

	leaseTTL := cfg.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultLeaseTTL
	}
	partyCooldown := cfg.PartyCooldown
	if partyCooldown <= 0 {
		partyCooldown = defaultPartyCooldown
	}
	pvpCooldown := cfg.PVPCooldown
	if pvpCooldown <= 0 {
		pvpCooldown = defaultPVPCooldown
	}
	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}
	maxPPS := cfg.MaxPPS
	if maxPPS <= 0 {
		maxPPS = defaultMaxPPS
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	listener, err := net.Listen("tcp", cfg.ControlListen)
	if err != nil {
		return nil, fmt.Errorf("listen control tcp %q: %w", cfg.ControlListen, err)
	}

	s := &Server{
		bindIP:            append(net.IP(nil), ip.To4()...),
		portMin:           cfg.PortMin,
		portCount:         portCount,
		secret:            append([]byte(nil), cfg.Secret...),
		leaseTTL:          leaseTTL,
		partyCooldown:     partyCooldown,
		pvpCooldown:       pvpCooldown,
		writeTimeout:      writeTimeout,
		maxPPS:            maxPPS,
		socketBufferBytes: cfg.SocketBufferBytes,
		nowFn:             nowFn,
		logger:            logger,
		listener:          listener,
		usedPorts:         make(map[int]struct{}),
		portAvailableAt:   make(map[int]time.Time),
		rooms:             make(map[roomKey]*roomState),
		controlConns:      make(map[net.Conn]struct{}),
		done:              make(chan struct{}),
	}

	s.wg.Add(2)
	go s.acceptLoop()
	go s.reapLoop()
	return s, nil
}

// ControlAddr returns the concrete TCP address the control listener is bound to.
func (s *Server) ControlAddr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Stats returns a snapshot of active rooms, legs, and cumulative packet counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	activeRooms := len(s.rooms)
	activeLegs := 0
	for _, room := range s.rooms {
		activeLegs += len(room.legs)
	}
	s.mu.Unlock()
	return Stats{
		ActiveRooms:    activeRooms,
		ActiveLegs:     activeLegs,
		Forwarded:      s.forwardedTotal.Load(),
		RejectedSource: s.rejectedTotal.Load(),
		RateLimited:    s.limitedTotal.Load(),
		WriteFailed:    s.failedTotal.Load(),
		ExpiredRooms:   s.expiredRooms.Load(),
		HelloAccepted:  s.helloAccepted.Load(),
		HelloRejected:  s.helloRejected.Load(),
		SyncSucceeded:  s.syncSucceeded.Load(),
		SyncFailed:     s.syncFailed.Load(),
		RoomsClosed:    s.roomsClosed.Load(),
	}
}

// Close shuts down the control listener, all control connections, all UDP legs,
// and waits for every background goroutine to exit.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.done)
		conns := make([]net.Conn, 0, len(s.controlConns))
		for c := range s.controlConns {
			conns = append(conns, c)
		}
		s.controlConns = make(map[net.Conn]struct{})
		s.mu.Unlock()

		var closeErr error
		if s.listener != nil {
			closeErr = errors.Join(closeErr, s.listener.Close())
		}
		for _, c := range conns {
			_ = c.Close()
		}

		// Drain any legs left after any in-flight SyncRoom/CloseRoom finishes.
		s.syncMu.Lock()
		s.mu.Lock()
		var legs []*relayLeg
		for _, room := range s.rooms {
			for _, leg := range room.legs {
				leg.retired.Store(true)
				legs = append(legs, leg)
			}
		}
		s.rooms = make(map[roomKey]*roomState)
		s.usedPorts = make(map[int]struct{})
		s.mu.Unlock()
		s.syncMu.Unlock()

		for _, leg := range legs {
			closeErr = errors.Join(closeErr, leg.conn.Close())
		}
		s.wg.Wait()
		s.closeErr = closeErr
	})
	return s.closeErr
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.controlConns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serveControlConn(conn)
	}
}

func (s *Server) serveControlConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.controlConns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	_ = conn.SetReadDeadline(time.Now().Add(defaultHelloReadWait))
	opcode, payload, err := readFrame(conn)
	if err != nil || opcode != OpHello || !verifyHelloPayload(s.secret, payload) {
		s.helloRejected.Add(1)
		return
	}
	if err := writeFrame(conn, OpHelloOK, nil, defaultControlWriteWait); err != nil {
		return
	}
	s.helloAccepted.Add(1)
	_ = conn.SetDeadline(time.Time{})

	for {
		opcode, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch opcode {
		case OpSync:
			req, ok := decodeSyncRequest(payload)
			if !ok {
				return
			}
			if req.mode != SyncModeMembers {
				s.syncFailed.Add(1)
				if err := writeFrame(conn, OpSyncFail, encodeSyncFail(req.roomID, SyncFailInvalidMode), defaultControlWriteWait); err != nil {
					return
				}
				continue
			}
			legs, failReason := s.syncRoom(req.scope, req.roomID, req.members)
			if failReason != 0 {
				s.syncFailed.Add(1)
				if err := writeFrame(conn, OpSyncFail, encodeSyncFail(req.roomID, failReason), defaultControlWriteWait); err != nil {
					return
				}
				continue
			}
			s.syncSucceeded.Add(1)
			if err := writeFrame(conn, OpSyncOK, encodeSyncOK(req.roomID, legs), defaultControlWriteWait); err != nil {
				return
			}
		case OpClose:
			scope, roomID, ok := decodeCloseRequest(payload)
			if !ok {
				return
			}
			s.closeRoom(scope, roomID)
			if err := writeFrame(conn, OpCloseOK, encodeCloseOK(roomID), defaultControlWriteWait); err != nil {
				return
			}
		default:
			// Ignore unknown opcodes to match v2 forward compatibility.
		}
	}
}

func (s *Server) syncRoom(scope byte, roomID uint32, rawMembers []uint16) ([]legAllocation, byte) {
	if roomID == 0 {
		return nil, SyncFailInvalidRoom
	}
	members := normalizeMembers(rawMembers)
	if len(members) < 2 || len(members) > MaxRoomMembers {
		return nil, SyncFailInvalidMembers
	}

	wanted := make([]legPair, 0, len(members)*(len(members)-1))
	wantedSet := make(map[legPair]struct{}, len(members)*(len(members)-1))
	for _, owner := range members {
		for _, peer := range members {
			if owner == peer {
				continue
			}
			pair := legPair{owner: owner, peer: peer}
			wanted = append(wanted, pair)
			wantedSet[pair] = struct{}{}
		}
	}

	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	key := roomKey{scope: scope, roomID: roomID}
	now := s.nowFn()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, SyncFailServerClosed
	}
	existingRoom := s.rooms[key]
	existingLegs := map[legPair]*relayLeg(nil)
	if existingRoom != nil {
		existingLegs = existingRoom.legs
	}

	newLegs := make(map[legPair]*relayLeg)
	rollback := func() {
		for _, leg := range newLegs {
			delete(s.usedPorts, int(leg.port))
		}
		s.mu.Unlock()
		for _, leg := range newLegs {
			_ = leg.conn.Close()
		}
	}

	for _, pair := range wanted {
		if existingLegs != nil && existingLegs[pair] != nil {
			continue
		}
		leg, err := s.allocateLegLocked(key, pair, now)
		if err != nil {
			reason := SyncFailPortExhausted
			if errors.Is(err, net.ErrClosed) {
				reason = SyncFailServerClosed
			}
			rollback()
			return nil, reason
		}
		newLegs[pair] = leg
	}

	if s.closed {
		rollback()
		return nil, SyncFailServerClosed
	}

	room := existingRoom
	if room == nil {
		room = &roomState{key: key, legs: make(map[legPair]*relayLeg, len(wanted))}
		s.rooms[key] = room
	}

	var toClose []*relayLeg
	for pair, leg := range room.legs {
		if _, keep := wantedSet[pair]; !keep {
			delete(room.legs, pair)
			delete(s.usedPorts, int(leg.port))
			s.quarantinePortLocked(leg.port, scope, now)
			leg.retired.Store(true)
			toClose = append(toClose, leg)
		}
	}
	for pair, leg := range newLegs {
		room.legs[pair] = leg
	}
	for pair, leg := range room.legs {
		leg.opposite.Store(room.legs[legPair{owner: pair.peer, peer: pair.owner}])
	}
	room.expiresAt = now.Add(s.leaseTTL)

	allocations := make([]legAllocation, 0, len(wanted))
	for _, pair := range wanted {
		leg := room.legs[pair]
		allocations = append(allocations, legAllocation{
			owner: pair.owner,
			peer:  pair.peer,
			port:  leg.port,
		})
	}
	s.wg.Add(len(newLegs))
	s.mu.Unlock()

	for _, leg := range toClose {
		_ = leg.conn.Close()
	}
	for _, leg := range newLegs {
		go s.readLoop(leg)
	}
	return allocations, 0
}

func (s *Server) closeRoom(scope byte, roomID uint32) {
	if roomID == 0 {
		return
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	key := roomKey{scope: scope, roomID: roomID}
	now := s.nowFn()

	s.mu.Lock()
	room := s.rooms[key]
	if room == nil {
		s.mu.Unlock()
		return
	}
	delete(s.rooms, key)
	toClose := make([]*relayLeg, 0, len(room.legs))
	for _, leg := range room.legs {
		delete(s.usedPorts, int(leg.port))
		s.quarantinePortLocked(leg.port, scope, now)
		leg.retired.Store(true)
		toClose = append(toClose, leg)
	}
	s.mu.Unlock()

	s.roomsClosed.Add(1)
	for _, leg := range toClose {
		_ = leg.conn.Close()
	}
}

func (s *Server) reapLoop() {
	defer s.wg.Done()
	interval := s.leaseTTL / 3
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	if interval < 25*time.Millisecond {
		interval = 25 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.reapExpired()
		}
	}
}

func (s *Server) reapExpired() {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	now := s.nowFn()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	var expiredKeys []roomKey
	for key, room := range s.rooms {
		if !now.Before(room.expiresAt) {
			expiredKeys = append(expiredKeys, key)
		}
	}
	var toClose []*relayLeg
	for _, key := range expiredKeys {
		room := s.rooms[key]
		delete(s.rooms, key)
		for _, leg := range room.legs {
			delete(s.usedPorts, int(leg.port))
			s.quarantinePortLocked(leg.port, key.scope, now)
			leg.retired.Store(true)
			toClose = append(toClose, leg)
		}
	}
	s.mu.Unlock()

	if len(expiredKeys) > 0 {
		s.expiredRooms.Add(uint64(len(expiredKeys)))
	}
	for _, leg := range toClose {
		_ = leg.conn.Close()
	}
}

func (s *Server) allocateLegLocked(room roomKey, pair legPair, now time.Time) (*relayLeg, error) {
	for offset := 0; offset < s.portCount; offset++ {
		index := (s.nextPort + offset) % s.portCount
		port := s.portMin + index
		if _, used := s.usedPorts[port]; used {
			continue
		}
		if avail, ok := s.portAvailableAt[port]; ok && now.Before(avail) {
			continue
		}
		if s.closed {
			return nil, net.ErrClosed
		}
		s.usedPorts[port] = struct{}{}
		addr := &net.UDPAddr{IP: append(net.IP(nil), s.bindIP...), Port: port}
		s.mu.Unlock()

		conn, err := net.ListenUDP("udp4", addr)
		if err == nil && s.socketBufferBytes > 0 {
			_ = conn.SetReadBuffer(s.socketBufferBytes)
			_ = conn.SetWriteBuffer(s.socketBufferBytes)
		}

		s.mu.Lock()
		if err != nil {
			delete(s.usedPorts, port)
			continue
		}
		if s.closed {
			delete(s.usedPorts, port)
			s.mu.Unlock()
			_ = conn.Close()
			s.mu.Lock()
			return nil, net.ErrClosed
		}
		s.nextPort = (index + 1) % s.portCount
		delete(s.portAvailableAt, port)
		return &relayLeg{
			room: room,
			pair: pair,
			conn: conn,
			port: uint16(port),
		}, nil
	}
	return nil, fmt.Errorf("no free UDP ports in %d-%d", s.portMin, s.portMin+s.portCount-1)
}

func (s *Server) quarantinePortLocked(port uint16, scope byte, now time.Time) {
	delay := s.partyCooldown
	if scope != ScopeParty {
		delay = s.pvpCooldown
	}
	if delay <= 0 {
		return
	}
	s.portAvailableAt[int(port)] = now.Add(delay)
}

func packAddrPort(ap netip.AddrPort) (uint32, uint64, bool) {
	if !ap.IsValid() || ap.Port() == 0 {
		return 0, 0, false
	}
	addr := ap.Addr().Unmap()
	if !addr.Is4() {
		return 0, 0, false
	}
	ip4 := addr.As4()
	ipU32 := binary.BigEndian.Uint32(ip4[:])
	return ipU32, (uint64(ipU32) << 16) | uint64(ap.Port()), true
}

func unpackAddrPort(packed uint64) (netip.AddrPort, bool) {
	if packed == 0 {
		return netip.AddrPort{}, false
	}
	var ip4 [4]byte
	binary.BigEndian.PutUint32(ip4[:], uint32(packed>>16))
	port := uint16(packed & 0xffff)
	if port == 0 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(netip.AddrFrom4(ip4), port), true
}

func (s *Server) readLoop(leg *relayLeg) {
	defer s.wg.Done()
	var buf [maxDatagramBytes]byte
	window := time.Now()
	packets, empty := 0, 0
	for {
		n, src, err := leg.conn.ReadFromUDPAddrPort(buf[:])
		if err != nil || leg.retired.Load() {
			return
		}
		now := time.Now()
		if now.Sub(window) >= time.Second {
			window, packets, empty = now, 0, 0
		}
		packets++
		if n == 0 {
			leg.traffic.empty.Add(1)
			empty++
			if empty > 100 {
				leg.traffic.limited.Add(1)
				s.limitedTotal.Add(1)
			}
			continue
		}
		if packets > s.maxPPS {
			leg.traffic.limited.Add(1)
			s.limitedTotal.Add(1)
			continue
		}
		srcIP, packed, ok := packAddrPort(src)
		if !ok {
			leg.traffic.rejected.Add(1)
			s.rejectedTotal.Add(1)
			continue
		}
		prev := leg.learned.Load()
		if prevIP := uint32(prev >> 16); prevIP != 0 && srcIP != prevIP {
			leg.traffic.rejected.Add(1)
			s.rejectedTotal.Add(1)
			continue
		}
		if prev != packed {
			leg.learned.Store(packed)
		}
		leg.traffic.received.Add(1)
		s.flushInitial(leg, now)

		opposite := leg.opposite.Load()
		if opposite == nil || opposite.retired.Load() {
			leg.traffic.missing.Add(1)
			continue
		}
		if opposite.learned.Load() == 0 {
			leg.traffic.unlearned.Add(1)
			for i := range opposite.initial {
				slot := &opposite.initial[i]
				if !slot.state.CompareAndSwap(0, 1) {
					continue
				}
				slot.sender, slot.created, slot.size = leg, now, n
				copy(slot.data[:], buf[:n])
				slot.state.Store(2)
				s.flushInitial(opposite, now)
				break
			}
			continue
		}
		s.forwardDatagram(leg, opposite, buf[:n], now)
	}
}

func (s *Server) flushInitial(target *relayLeg, now time.Time) {
	if target.learned.Load() == 0 {
		return
	}
	for i := range target.initial {
		slot := &target.initial[i]
		if slot.state.CompareAndSwap(2, 3) && now.Sub(slot.created) <= initialBufferWindow {
			s.forwardDatagram(slot.sender, target, slot.data[:slot.size], now)
		}
	}
}

func (s *Server) forwardDatagram(from, to *relayLeg, payload []byte, now time.Time) {
	if from.retired.Load() || to.retired.Load() || from.opposite.Load() != to {
		return
	}
	dst, ok := unpackAddrPort(to.learned.Load())
	if !ok {
		return
	}
	if err := to.ensureWriteDeadline(now, s.writeTimeout); err != nil {
		if !from.retired.Load() && !to.retired.Load() && from.opposite.Load() == to {
			to.traffic.failed.Add(1)
			s.failedTotal.Add(1)
		}
		return
	}
	if _, err := to.conn.WriteToUDPAddrPort(payload, dst); err != nil {
		to.writeDeadline.Store(0)
		if !from.retired.Load() && !to.retired.Load() && from.opposite.Load() == to {
			from.traffic.failed.Add(1)
			s.failedTotal.Add(1)
		}
		return
	}
	from.traffic.forwarded.Add(1)
	from.traffic.bytes.Add(uint64(len(payload)))
	s.forwardedTotal.Add(1)
}

func (leg *relayLeg) ensureWriteDeadline(now time.Time, timeout time.Duration) error {
	elapsed := now.Sub(relayClockOrigin)
	refreshBelow := int64(elapsed + timeout/2)
	if leg.writeDeadline.Load() >= refreshBelow {
		return nil
	}
	leg.deadlineMu.Lock()
	defer leg.deadlineMu.Unlock()
	if leg.writeDeadline.Load() >= refreshBelow {
		return nil
	}
	if err := leg.conn.SetWriteDeadline(now.Add(timeout)); err != nil {
		leg.writeDeadline.Store(0)
		return err
	}
	leg.writeDeadline.Store(int64(elapsed + timeout))
	return nil
}

// ActivePortsForRoom returns a sorted copy of currently allocated ports for
// (scope, roomID); intended for observability and tests.
func (s *Server) ActivePortsForRoom(scope byte, roomID uint32) []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.rooms[roomKey{scope: scope, roomID: roomID}]
	if room == nil {
		return nil
	}
	ports := make([]uint16, 0, len(room.legs))
	for _, leg := range room.legs {
		ports = append(ports, leg.port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports
}
