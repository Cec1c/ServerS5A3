package dnfbridge

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	currentPartyUDPRelayMaxMembers  = 4
	currentPartyUDPRelayMaxDatagram = 2048
)

// currentPartyUDPRelayBinding is one authenticated half of a party transport.
// The TCP session's observed IPv4 is deliberately authoritative. A client may
// report an arbitrary inner/outer endpoint in CS op2, whereas the relay must
// never learn an endpoint from an unrelated Internet host.
type currentPartyUDPRelayBinding struct {
	characterID uint16
	generation  uint64
	address     [4]byte
	// advertisedPort 是该成员**自己**在 CS op2（`msg_id=2` / 31B）里报的 UDP 端口。
	// 它只用于"反向腿未学习时的回落猜测"（开关 DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK，
	// 默认 off，见 party_udp_relay_endpoint_fallback.go）：**绝不**参与源校验，也**绝不**
	// 用来决定目标 IP（目标 IP 永远取 address = 游戏 TCP 会话观察到的 IPv4）。
	advertisedPort uint16
}

type currentPartyUDPRelayLegKey struct {
	pvp     bool // 决斗房间与普通队伍使用不同命名空间。
	partyID uint16
	ownerID uint16
	peerID  uint16
}

type relayTraffic struct {
	received, forwarded, bytes, empty, rejected, missing, unlearned atomic.Uint64
	limited, failed                                                 atomic.Uint64
}

type relayInitialDatagram struct {
	state   atomic.Uint32 // 0 free, 1 writing, 2 ready, 3 consumed; never reused
	sender  *currentPartyUDPRelayLeg
	created time.Time
	size    int
	data    [currentPartyUDPRelayMaxDatagram]byte
}

// Monotonic origin shared by reader timestamps and cached socket deadlines.
var currentPartyRelayClockOrigin = time.Now()

type currentPartyUDPRelayLeg struct {
	writeDeadline atomic.Int64 // nanoseconds since currentPartyRelayClockOrigin
	deadlineMu    sync.Mutex   // serialize refreshes from concurrent initial datagram drains
	opposite      atomic.Pointer[currentPartyUDPRelayLeg]
	retired       atomic.Bool
	learnedPort   atomic.Uint32 // IP is the immutable authenticated binding address
	traffic       relayTraffic
	initial       [4]relayInitialDatagram

	key         currentPartyUDPRelayLegKey
	binding     currentPartyUDPRelayBinding
	peerBinding currentPartyUDPRelayBinding
	conn        *net.UDPConn
	port        uint16

	mu              sync.Mutex
	learnedEndpoint *net.UDPAddr
	// forwarded/bytesForwarded 是"这条腿真的搬运过的数据报"。副本内队友位移走的是客户端
	// P2P（服务端只在 op11 里公布中继端点），服务端**看不到任何位置包**；因此"队友不动"
	// 这类症状只能靠这些计数判定"P2P 通道到底有没有流量、卡在哪一步"。
	forwarded      uint64
	bytesForwarded uint64
	announced      bool
	// 下面的计数覆盖 readLoop 里**每一条** continue 路径：不匹配就静默丢弃的旧行为会把
	// "通道建了但一个包都没通"和"包来了但被源校验挡掉"混成同一个症状（都是零转发）。
	received              uint64
	lastReceivedAt        time.Time
	emptyPayload          uint64
	rejectedSource        uint64
	missingOppositeLeg    uint64
	unlearnedPeerEndpoint uint64
	// loggedXxx 保证每种丢弃原因只在**首次**记一条日志，避免刷屏。
	loggedEmpty     bool
	loggedSource    bool
	loggedMissing   bool
	loggedUnlearned bool
}

// relayLegDropReason 是腿的丢弃原因（只用于计数与日志，不参与转发判定）。
type relayLegDropReason int

const (
	relayLegDropEmptyPayload relayLegDropReason = iota
	relayLegDropSourceMismatch
	relayLegDropMissingOppositeLeg
	relayLegDropUnlearnedPeerEndpoint
)

// String 返回该原因在日志里的判读名。
func (reason relayLegDropReason) String() string {
	switch reason {
	case relayLegDropEmptyPayload:
		return "empty_payload"
	case relayLegDropSourceMismatch:
		return "source_mismatch"
	case relayLegDropMissingOppositeLeg:
		return "missing_opposite_leg"
	case relayLegDropUnlearnedPeerEndpoint:
		return "unlearned_peer_endpoint"
	default:
		return "unknown"
	}
}

// noteReceived 记一条"通过了源校验的数据报"。
func (leg *currentPartyUDPRelayLeg) noteReceived() uint64 {
	if leg == nil {
		return 0
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	leg.received++
	leg.lastReceivedAt = time.Now()
	return leg.received
}

// noteDrop 记一条被丢弃的数据报，返回 (该原因的累计条数, 是否是首次)。
// 首次为 true 时调用方才记日志（每条腿每种原因最多一条）。
func (leg *currentPartyUDPRelayLeg) noteDrop(reason relayLegDropReason) (uint64, bool) {
	if leg == nil {
		return 0, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	var counter *uint64
	var logged *bool
	switch reason {
	case relayLegDropEmptyPayload:
		counter, logged = &leg.emptyPayload, &leg.loggedEmpty
	case relayLegDropSourceMismatch:
		counter, logged = &leg.rejectedSource, &leg.loggedSource
	case relayLegDropMissingOppositeLeg:
		counter, logged = &leg.missingOppositeLeg, &leg.loggedMissing
	case relayLegDropUnlearnedPeerEndpoint:
		counter, logged = &leg.unlearnedPeerEndpoint, &leg.loggedUnlearned
	default:
		return 0, false
	}
	*counter++
	first := !*logged
	*logged = true
	return *counter, first
}

// relayLegStats 是一条腿的可观测快照（关闭汇总与单测使用）。
type relayLegStats struct {
	ListenPort               uint16
	PartyID                  uint16
	OwnerID                  uint16
	PeerID                   uint16
	ExpectedIPv4             string
	LearnedEndpoint          string
	AdvertisedPort           uint16
	Received                 uint64
	Forwarded                uint64
	BytesForwarded           uint64
	EmptyPayload             uint64
	RejectedSource           uint64
	MissingOppositeLeg       uint64
	UnlearnedPeerEndpoint    uint64
	EndpointKnown            bool
	RateLimited, WriteFailed uint64
}

// relayLegStatsSnapshot 取这条腿的计数快照。
func (leg *currentPartyUDPRelayLeg) relayLegStatsSnapshot() relayLegStats {
	if leg == nil {
		return relayLegStats{}
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	stats := relayLegStats{
		ListenPort:            leg.port,
		PartyID:               leg.key.partyID,
		OwnerID:               leg.key.ownerID,
		PeerID:                leg.key.peerID,
		ExpectedIPv4:          formatCurrentPartyUDPRelayIPv4(leg.binding.address),
		AdvertisedPort:        leg.binding.advertisedPort,
		Received:              leg.received + leg.traffic.received.Load(),
		Forwarded:             leg.forwarded + leg.traffic.forwarded.Load(),
		BytesForwarded:        leg.bytesForwarded + leg.traffic.bytes.Load(),
		EmptyPayload:          leg.emptyPayload + leg.traffic.empty.Load(),
		RejectedSource:        leg.rejectedSource + leg.traffic.rejected.Load(),
		MissingOppositeLeg:    leg.missingOppositeLeg + leg.traffic.missing.Load(),
		UnlearnedPeerEndpoint: leg.unlearnedPeerEndpoint + leg.traffic.unlearned.Load(),

		EndpointKnown: leg.learnedPort.Load() != 0,
		RateLimited:   leg.traffic.limited.Load(), WriteFailed: leg.traffic.failed.Load(),
	}
	if leg.learnedEndpoint != nil {
		stats.LearnedEndpoint = leg.learnedEndpoint.String()
	}
	if port := leg.learnedPort.Load(); port != 0 {
		stats.LearnedEndpoint = netip.AddrPortFrom(netip.AddrFrom4(leg.binding.address), uint16(port)).String()
	}
	return stats
}

// formatCurrentPartyUDPRelayIPv4 把 binding 里的 4 字节地址格式化成日志用的点分十进制。
func formatCurrentPartyUDPRelayIPv4(address [4]byte) string {
	return fmt.Sprintf("%d.%d.%d.%d", address[0], address[1], address[2], address[3])
}

// noteForwarded 记一条已转发的数据报，并返回 (累计条数, 累计字节, 是否是这条腿的第一条)。
// "第一条"只用于记一次活口日志，避免每个数据报刷屏。
func (leg *currentPartyUDPRelayLeg) noteForwarded(size int) (uint64, uint64, bool) {
	if leg == nil {
		return 0, 0, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	leg.forwarded++
	leg.bytesForwarded += uint64(size)
	first := !leg.announced
	leg.announced = true
	return leg.forwarded, leg.bytesForwarded, first
}

// ForwardedDatagrams 返回这条腿累计转发的数据报数与字节数。
func (leg *currentPartyUDPRelayLeg) ForwardedDatagrams() (uint64, uint64) {
	if leg == nil {
		return 0, 0
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	return leg.forwarded + leg.traffic.forwarded.Load(), leg.bytesForwarded + leg.traffic.bytes.Load()
}

// currentPartyUDPRelay is a per-directed-pair UDP relay. For party members A
// and B it owns two ports: A sends to A->B; received payload is emitted from
// B->A to B's learned endpoint. This keeps the existing client P2P handshake
// shape while exposing only the server address to every participant.
//
// It is intentionally IPv4-only because PARTY_IP_INFO carries exactly four
// octets. The listener always binds a concrete configured address; wildcard
// UDP listeners would violate the local profile's network boundary.
type currentPartyUDPRelay struct {
	mu sync.Mutex

	enabled   bool
	bindIP    net.IP
	advertise [4]byte
	portStart int
	portCount int
	usedPorts map[int]struct{}
	// Cooldown belongs to the port, across party/PVP namespaces. Bounded by portCount.
	portAvailableAt map[int]time.Time
	nextPort        int
	legs            map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg
	closed          bool
	wg              sync.WaitGroup

	// forwardedTotal 是全部腿（含已关闭的）累计转发的数据报数，原子累加，不参与转发逻辑。
	forwardedTotal atomic.Uint64

	probeListen   bool
	done          chan struct{}
	probeListener *currentPartyUDPRelayProbeListeners

	logWarn func(string, ...any)

	socketBufferBytes       int
	lastEffectiveRcvbuf     atomic.Int64
	lastEffectiveSndbuf     atomic.Int64
	loggedSocketBufferClamp atomic.Bool
	loggedSocketBufferIssue atomic.Bool
}

// Settings for authenticated per-leg forwarding. Recovery experiments were retired.
type currentPartyUDPRelaySettings struct {
	enabled               bool
	advertiseIP, bindHost string
	portStart, portCount  int
	probeListen           bool
	probePorts            []int
	socketBufferBytes     int
	logWarn               func(string, ...any)
}

func newCurrentPartyUDPRelay(enabled bool, advertiseIP string, portStart, portCount int, logWarn func(string, ...any)) (*currentPartyUDPRelay, error) {
	return newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{enabled: enabled, advertiseIP: advertiseIP, portStart: portStart, portCount: portCount, logWarn: logWarn})
}

func newCurrentPartyUDPRelayWithSettings(settings currentPartyUDPRelaySettings) (*currentPartyUDPRelay, error) {
	enabled := settings.enabled
	advertiseIP := settings.advertiseIP
	portStart := settings.portStart
	portCount := settings.portCount
	relay := &currentPartyUDPRelay{enabled: enabled, probeListen: settings.probeListen, socketBufferBytes: settings.socketBufferBytes, logWarn: settings.logWarn, done: make(chan struct{})}
	if !enabled {
		return relay, nil
	}
	ip := net.ParseIP(advertiseIP)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() {
		return nil, fmt.Errorf("party UDP relay requires a concrete IPv4 advertise address, got %q", advertiseIP)
	}
	if portStart < 1 || portStart > 65535 || portCount < 12 || portCount > 1024 || portStart+portCount-1 > 65535 {
		return nil, fmt.Errorf("party UDP relay port range %d+%d is invalid", portStart, portCount)
	}
	ipv4 := ip.To4()
	bindIP := ipv4
	if settings.bindHost != "" {
		bindIP = net.ParseIP(settings.bindHost).To4()
		if bindIP == nil || bindIP.IsUnspecified() {
			return nil, fmt.Errorf("party UDP relay requires a concrete IPv4 bind address, got %q", settings.bindHost)
		}
	}
	relay.bindIP = append(net.IP(nil), bindIP...)
	copy(relay.advertise[:], ipv4)
	relay.portStart = portStart
	relay.portCount = portCount
	relay.usedPorts = make(map[int]struct{})
	relay.portAvailableAt = make(map[int]time.Time)
	relay.legs = make(map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg)
	// 常驻原生探针监听（默认档；绑不上只记日志，绝不影响 dnfbridge 启动）。
	relay.startProbeListeners(settings.probePorts)
	relay.wg.Add(1)
	go relay.reportTraffic(relay.done)
	return relay, nil
}

func (r *currentPartyUDPRelay) Enabled() bool {
	return r != nil && r.enabled
}

// alive 报告这个 relay 还活着（Close 之后为 false）。
//
// 它与 Enabled() 的区别是刻意的：Enabled() 表达的是"**配置上**启用了内嵌中继"（这个语义
// 被启动汇总行与既有测试依赖，不能改），而后端选择（Service.partyRelayBackend）需要的是
// "现在还能不能拿来查端点"——已经 Close 的 relay 不该再被选中，否则 Stop() 之后
// 会拿着一个已关闭的 relay 去 Sync/Endpoint。remote 后端（party_udp_relay_remote.go）
// 的 Enabled() 已经自带这条语义，这里补齐内嵌侧的对称性。
func (r *currentPartyUDPRelay) alive() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed
}

// Sync installs a complete, transactional directed-pair set for one party.
// It returns false without publishing a partial relay topology when a member
// is stale/offline or the configured UDP range cannot satisfy the room.
func (r *currentPartyUDPRelay) Sync(partyID uint16, bindings []currentPartyUDPRelayBinding) bool {
	return r.syncGroup(false, partyID, bindings)
}
func (r *currentPartyUDPRelay) syncGroup(pvp bool, partyID uint16, bindings []currentPartyUDPRelayBinding) bool {
	if r == nil || !r.enabled {
		return false
	}
	bindings = currentPartyUDPRelayBindings(bindings)
	limit := currentPartyUDPRelayMaxMembers
	if pvp {
		limit = 8
	}
	if partyID == 0 || len(bindings) < 2 || len(bindings) > limit {
		return false
	}

	wanted := make(map[currentPartyUDPRelayLegKey]currentPartyUDPRelayBinding, len(bindings)*(len(bindings)-1))
	byID := make(map[uint16]currentPartyUDPRelayBinding, len(bindings))
	for _, owner := range bindings {
		byID[owner.characterID] = owner
		for _, peer := range bindings {
			if owner.characterID == peer.characterID {
				continue
			}
			wanted[currentPartyUDPRelayLegKey{pvp: pvp, partyID: partyID, ownerID: owner.characterID, peerID: peer.characterID}] = owner
		}
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return false
	}
	before := make(map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg)
	for key, leg := range r.legs {
		if key.partyID == partyID && key.pvp == pvp {
			before[key] = leg
		}
	}
	newLegs := make(map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg)
	// ★ 端口分配必须稳定：newLegLocked 在端口池里从 portStart 顺序扫描，
	// **谁先被遍历谁拿小端口**。下面原先直接 `range wanted`（map），而 Go 的
	// map 遍历顺序每次随机，于是同一个两人队重开一次，owner→端口 的映射就可能
	// 整个翻转。2026-11-08 00:07 / 00:08 线上实测：
	//   第一次 owner2→30002、owner5→30001；
	//   第二次 owner2→30001、owner5→30002。
	// 客户端会缓存第一次拿到的端口，翻转后一条腿全程收不到包
	// （`party UDP relay legs closed … idle_legs=1`），表现就是客户端弹
	// "防火墙"提示、一个玩家被踢回基地。
	// 按 (ownerID, peerID) 升序固定遍历顺序，端口映射就与建队先后无关。
	orderedLegKeys := make([]currentPartyUDPRelayLegKey, 0, len(wanted))
	for key := range wanted {
		orderedLegKeys = append(orderedLegKeys, key)
	}
	sort.Slice(orderedLegKeys, func(i, j int) bool {
		if orderedLegKeys[i].ownerID != orderedLegKeys[j].ownerID {
			return orderedLegKeys[i].ownerID < orderedLegKeys[j].ownerID
		}
		return orderedLegKeys[i].peerID < orderedLegKeys[j].peerID
	})
	for _, key := range orderedLegKeys {
		binding := wanted[key]
		if existing := r.legs[key]; existing != nil && existing.binding == binding && existing.peerBinding == byID[key.peerID] {
			continue
		}
		leg, err := r.newLegLocked(key, binding)
		if err != nil {
			for _, created := range newLegs {
				r.releaseLegLocked(created)
			}
			r.mu.Unlock()
			for _, created := range newLegs {
				_ = created.conn.Close()
			}
			r.log("party UDP relay synchronization deferred", "party_id", partyID, "error", err)
			return false
		}
		leg.peerBinding = byID[key.peerID]
		newLegs[key] = leg
	}

	// Socket allocation ran outside the topology lock. A concurrent change
	// to this group invalidates the plan; other parties can proceed freely.
	current := !r.closed
	count := 0
	for key, leg := range r.legs {
		if key.partyID == partyID && key.pvp == pvp {
			count++
			current = current && before[key] == leg
		}
	}
	if !current || count != len(before) {
		for _, leg := range newLegs {
			r.releaseLegLocked(leg)
		}
		r.mu.Unlock()
		for _, leg := range newLegs {
			_ = leg.conn.Close()
		}
		return false
	}
	toClose := make([]*currentPartyUDPRelayLeg, 0)
	for key, existing := range r.legs {
		if key.partyID != partyID || key.pvp != pvp {
			continue
		}
		if binding, wanted := wanted[key]; !wanted || existing.binding != binding || existing.peerBinding != byID[key.peerID] {
			existing.retired.Store(true)
			toClose = append(toClose, existing)
			delete(r.legs, key)
			delete(r.usedPorts, int(existing.port))
			r.notePortReleasedLocked(existing)
		}
	}
	for key, leg := range newLegs {
		r.legs[key] = leg
	}
	for key, leg := range r.legs {
		if key.partyID == partyID && key.pvp == pvp {
			leg.opposite.Store(r.legs[currentPartyUDPRelayLegKey{pvp: key.pvp, partyID: key.partyID, ownerID: key.peerID, peerID: key.ownerID}])
		}
	}
	r.wg.Add(len(newLegs))
	r.mu.Unlock()

	for _, leg := range toClose {
		_ = leg.conn.Close()
	}
	r.logLegClosure(toClose, "party_membership_changed")
	r.logLegCreation(sortedCurrentPartyUDPRelayLegs(newLegs))
	for _, leg := range newLegs {
		go r.readLoop(leg)
	}
	return true
}

// CloseParty closes sockets immediately; their ports remain quarantined.
func (r *currentPartyUDPRelay) CloseParty(partyID uint16) {
	if r == nil || partyID == 0 {
		return
	}
	r.mu.Lock()
	legs := r.removePartyLocked(partyID)
	r.mu.Unlock()
	for _, leg := range legs {
		_ = leg.conn.Close()
	}
	r.logLegClosure(legs, "close_party")
}

func (r *currentPartyUDPRelay) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if r.done != nil {
		close(r.done)
		r.done = nil
	}
	listeners := r.probeListener
	r.probeListener = nil
	legs := make([]*currentPartyUDPRelayLeg, 0, len(r.legs))
	for _, leg := range r.legs {
		leg.retired.Store(true)
		legs = append(legs, leg)
	}
	r.legs = make(map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg)
	r.usedPorts = make(map[int]struct{})
	r.mu.Unlock()
	listeners.close()
	var closeErr error
	for _, leg := range legs {
		closeErr = errors.Join(closeErr, leg.conn.Close())
	}
	r.logLegClosure(legs, "relay_close")
	r.wg.Wait()
	return closeErr
}

func (r *currentPartyUDPRelay) Endpoint(partyID, ownerID, peerID uint16) ([4]byte, uint16, bool) {
	if r == nil || !r.enabled || partyID == 0 || ownerID == 0 || peerID == 0 || ownerID == peerID {
		return [4]byte{}, 0, false
	}
	r.mu.Lock()
	leg := r.legs[currentPartyUDPRelayLegKey{partyID: partyID, ownerID: ownerID, peerID: peerID}]
	address := r.advertise
	r.mu.Unlock()
	if leg == nil {
		return [4]byte{}, 0, false
	}
	return address, leg.port, true
}

// Rotate through the pool without overriding cooldown on exhaustion.
func (r *currentPartyUDPRelay) newLegLocked(key currentPartyUDPRelayLegKey, binding currentPartyUDPRelayBinding) (*currentPartyUDPRelayLeg, error) {
	now := time.Now()
	for offset := 0; offset < r.portCount; offset++ {
		index := (r.nextPort + offset) % r.portCount
		port := r.portStart + index
		// Native keepalives keep targeting probe ports after party formation.
		// Reusing one as a directed leg lets a same-NAT peer overwrite its owner.
		if r.probeListener != nil {
			if _, reserved := r.probeListener.reservedPorts[port]; reserved {
				continue
			}
		}
		if _, used := r.usedPorts[port]; used {
			continue
		}
		if now.Before(r.portAvailableAt[port]) {
			continue
		}
		if r.closed {
			return nil, net.ErrClosed
		}
		r.usedPorts[port] = struct{}{}
		address := &net.UDPAddr{IP: append(net.IP(nil), r.bindIP...), Port: port}
		r.mu.Unlock()
		conn, err := net.ListenUDP("udp4", address)
		if err == nil {
			r.applySocketBuffer(conn, port, "relay_leg")
		}
		r.mu.Lock()
		if err != nil {
			delete(r.usedPorts, port)
			continue
		}
		r.nextPort = (index + 1) % r.portCount
		delete(r.portAvailableAt, port)
		return &currentPartyUDPRelayLeg{
			key:     key,
			binding: binding,
			conn:    conn,
			port:    uint16(port),
		}, nil
	}
	return nil, fmt.Errorf("no free ports in configured range %d-%d", r.portStart, r.portStart+r.portCount-1)
}

func (r *currentPartyUDPRelay) releaseLegLocked(leg *currentPartyUDPRelayLeg) {
	if leg == nil {
		return
	}
	delete(r.usedPorts, int(leg.port))
	// Never published. The caller closes its socket after releasing r.mu.
}

// Both namespaces share the same pool; switching modes cannot bypass cooldown.
func (r *currentPartyUDPRelay) notePortReleasedLocked(leg *currentPartyUDPRelayLeg) {
	if r.portAvailableAt == nil {
		r.portAvailableAt = make(map[int]time.Time)
	}
	delay := 60 * time.Second
	if leg.key.pvp {
		delay = pvpRelayPortQuarantine
	}
	r.portAvailableAt[int(leg.port)] = time.Now().Add(delay)
}

func (r *currentPartyUDPRelay) removePartyLocked(partyID uint16) []*currentPartyUDPRelayLeg {
	legs := make([]*currentPartyUDPRelayLeg, 0)
	for key, leg := range r.legs {
		if key.partyID != partyID || key.pvp {
			continue
		}
		delete(r.legs, key)
		delete(r.usedPorts, int(leg.port))
		r.notePortReleasedLocked(leg)
		leg.retired.Store(true)
		legs = append(legs, leg)
	}
	return legs
}

// The socket reader owns its rate window. Routes, endpoint ports and counters
// are atomic; no topology/leg lock, formatting or allocation occurs per packet.
func (r *currentPartyUDPRelay) readLoop(leg *currentPartyUDPRelayLeg) {
	defer r.wg.Done()
	var buffer [currentPartyUDPRelayMaxDatagram]byte
	window := time.Now()
	packets, empty := 0, 0
	for {
		n, source, err := leg.conn.ReadFromUDPAddrPort(buffer[:])
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
			}
			continue
		}
		if packets > 3000 {
			leg.traffic.limited.Add(1)
			continue
		}
		if !source.Addr().Is4() || source.Addr().As4() != leg.binding.address {
			leg.traffic.rejected.Add(1)
			continue
		}
		leg.traffic.received.Add(1)
		leg.learnedPort.Store(uint32(source.Port()))
		r.flushInitial(leg, now)
		opposite := leg.opposite.Load()
		if opposite == nil || opposite.retired.Load() {
			leg.traffic.missing.Add(1)
			continue
		}
		if opposite.learnedPort.Load() == 0 {
			leg.traffic.unlearned.Add(1)
			for i := range opposite.initial {
				slot := &opposite.initial[i]
				if !slot.state.CompareAndSwap(0, 1) {
					continue
				}
				slot.sender, slot.created, slot.size = leg, now, n
				copy(slot.data[:], buffer[:n])
				slot.state.Store(2)
				// Learning may have raced publication; either reader can drain.
				r.flushInitial(opposite, now)
				break
			}
			continue
		}
		r.forwardDatagram(leg, opposite, buffer[:n], now)
	}
}

func (r *currentPartyUDPRelay) flushInitial(target *currentPartyUDPRelayLeg, now time.Time) {
	if target.learnedPort.Load() == 0 {
		return
	}
	for i := range target.initial {
		slot := &target.initial[i]
		if slot.state.CompareAndSwap(2, 3) && now.Sub(slot.created) <= 5*time.Second {
			r.forwardDatagram(slot.sender, target, slot.data[:slot.size], now)
		}
	}
}

func (r *currentPartyUDPRelay) forwardDatagram(from, to *currentPartyUDPRelayLeg, payload []byte, now time.Time) {
	if from.retired.Load() || to.retired.Load() || from.opposite.Load() != to {
		return
	}
	port := to.learnedPort.Load()
	if port == 0 {
		return
	}
	// A stale writer owns only its old socket. Close fails it naturally, and
	// port quarantine prevents that socket's address from changing owners.
	if err := to.ensureWriteDeadline(now); err != nil {
		if !from.retired.Load() && !to.retired.Load() && from.opposite.Load() == to {
			to.traffic.failed.Add(1)
		}
		return
	}
	if _, err := to.conn.WriteToUDPAddrPort(payload, netip.AddrPortFrom(netip.AddrFrom4(to.binding.address), uint16(port))); err != nil {
		to.writeDeadline.Store(0)
		if !from.retired.Load() && !to.retired.Load() && from.opposite.Load() == to {
			from.traffic.failed.Add(1)
		}
		return
	}
	from.traffic.forwarded.Add(1)
	from.traffic.bytes.Add(uint64(len(payload)))
	r.forwardedTotal.Add(1)
}

// Initial datagrams may be drained by either reader. Publish a new deadline
// only after the socket accepted it. Regular sends only load the timestamp.
func (leg *currentPartyUDPRelayLeg) ensureWriteDeadline(now time.Time) error {
	elapsed := now.Sub(currentPartyRelayClockOrigin)
	refreshBelow := int64(elapsed + currentPartyRelayWriteTimeout/2)
	if leg.writeDeadline.Load() >= refreshBelow {
		return nil
	}
	leg.deadlineMu.Lock()
	defer leg.deadlineMu.Unlock()
	if leg.writeDeadline.Load() >= refreshBelow {
		return nil
	}
	if err := leg.conn.SetWriteDeadline(now.Add(currentPartyRelayWriteTimeout)); err != nil {
		leg.writeDeadline.Store(0)
		return err
	}
	leg.writeDeadline.Store(int64(elapsed + currentPartyRelayWriteTimeout))
	return nil
}

// Diagnostics run off the packet path, at most once per ten seconds per leg.
func (r *currentPartyUDPRelay) reportTraffic(done <-chan struct{}) {
	defer r.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			r.mu.Lock()
			legs := sortedCurrentPartyUDPRelayLegs(r.legs)
			r.mu.Unlock()
			for _, leg := range legs {
				t := &leg.traffic
				if t.empty.Load()+t.rejected.Load()+t.missing.Load()+t.unlearned.Load()+t.limited.Load()+t.failed.Load() == 0 {
					continue
				}
				r.log("party UDP relay traffic summary", "party_id", leg.key.partyID, "owner_id", leg.key.ownerID, "peer_id", leg.key.peerID,
					"empty_payload", t.empty.Load(), "source_mismatch", t.rejected.Load(), "unlearned", t.unlearned.Load(), "rate_limited", t.limited.Load(), "write_failed", t.failed.Load())
			}
		}
	}
}

// logDrop 记一条"某个 readLoop 丢弃原因首次出现"的日志。字段固定带
// (party_id, owner_id, peer_id, leg_listen_port, expected, source)，线上一次判读即可定死
// 卡在哪一步。source 为 nil 时（空载荷那条没有可用的源地址）不写 source。
func (r *currentPartyUDPRelay) logDrop(
	leg *currentPartyUDPRelayLeg,
	reason relayLegDropReason,
	count uint64,
	source *net.UDPAddr,
	extra ...any,
) {
	if leg == nil {
		return
	}
	fields := []any{
		"party_id", leg.key.partyID,
		"owner_id", leg.key.ownerID,
		"peer_id", leg.key.peerID,
		"leg_listen_port", leg.port,
		"reason", reason.String(),
		"dropped_datagrams", count,
	}
	if source != nil {
		fields = append(fields, "source", source.String())
	}
	fields = append(fields, extra...)
	switch reason {
	case relayLegDropSourceMismatch:
		r.log("party UDP relay source rejected", fields...)
	case relayLegDropEmptyPayload:
		r.log("party UDP relay empty payload dropped", fields...)
	case relayLegDropMissingOppositeLeg:
		r.log("party UDP relay opposite leg missing", fields...)
	case relayLegDropUnlearnedPeerEndpoint:
		r.log("party UDP relay peer endpoint not learned", fields...)
	default:
		r.log("party UDP relay datagram dropped", fields...)
	}
}

// ForwardedDatagramTotal 是**所有腿累计**转发的数据报数（跨已关闭的腿也保留）。
// 判读：一次实测结束后若仍为 0 ⇒ 副本内队友位移所依赖的客户端 P2P 通道一个数据报都没走，
// "对方站着不动/放技能看不到"就不是服务端位置转发缺失，而是 P2P 通道本身没建起来。
func (r *currentPartyUDPRelay) ForwardedDatagramTotal() uint64 {
	if r == nil {
		return 0
	}
	return r.forwardedTotal.Load()
}

// logLegClosure 在腿被回收时记一条汇总（条数/空闲腿/累计流量/每条丢弃路径的条数），
// 判读串 `party UDP relay legs closed`。只在实际有关闭动作时记，不随心跳刷屏。
func (r *currentPartyUDPRelay) logLegClosure(legs []*currentPartyUDPRelayLeg, reason string) {
	if r == nil || len(legs) == 0 {
		return
	}
	var received, forwarded, bytes, empty, rejected, unlearned, limited, failed, missing uint64
	idle := 0
	for _, leg := range legs {
		st := leg.relayLegStatsSnapshot()
		received += st.Received
		forwarded += st.Forwarded
		bytes += st.BytesForwarded
		empty += st.EmptyPayload
		rejected += st.RejectedSource
		unlearned += st.UnlearnedPeerEndpoint
		missing += st.MissingOppositeLeg
		limited += st.RateLimited
		failed += st.WriteFailed
		if st.Received == 0 {
			idle++
		}
	}
	r.log("party UDP relay legs closed", "party_id", legs[0].key.partyID, "reason", reason, "closed_legs", len(legs), "idle_legs", idle,
		"received_datagrams", received, "leg_forwarded_datagrams", forwarded, "forwarded_bytes", bytes, "empty_payload", empty, "source_mismatch", rejected,
		"unlearned_peer_endpoint", unlearned, "rate_limited", limited, "write_failed", failed,
		"rejected_source_datagrams", rejected, "dropped_empty_datagrams", empty,
		"dropped_missing_opposite_leg", missing, "dropped_unlearned_peer_endpoint", unlearned)
}

// logLegCreation 在腿刚绑定时记一条，把 `expected`（= 该会话游戏 TCP 连接看到的对端 IPv4）
// 与监听端口一起写进日志。线上一次判读即可把"客户端 CS op2 上报的 endpoint"与
// "中继真正用来校验的地址"对齐，不必再翻代码。
//
// 同时写 `bind=`/`advertise=`：`bind` 是 relay **实际 ListenUDP 的地址**（永远是具体 IPv4；
// 配置成 0.0.0.0/未指定时 `newCurrentPartyUDPRelay` 直接返回错误），`advertise` 是写进 op11 的
// 地址。通告取自 `DNFBRIDGE_SERVER_IP`，监听优先取 `DNFBRIDGE_GAME_LISTEN_HOST`，
// 未指定时保留原通告地址绑定。线上"看不到 31000/31001 入向流量"先看这条：
// **没有 party 就没有腿、没有 UDP socket**，此时零入向是必然；只有出现 `leg listening`
// 才代表那对端口真的在监听。
func (r *currentPartyUDPRelay) logLegCreation(legs []*currentPartyUDPRelayLeg) {
	if r == nil || len(legs) == 0 {
		return
	}
	bind := r.bindAddressString()
	advertise := formatCurrentPartyUDPRelayIPv4(r.advertise)
	for _, leg := range legs {
		stats := leg.relayLegStatsSnapshot()
		r.log("party UDP relay leg listening",
			"party_id", stats.PartyID,
			"owner_id", stats.OwnerID,
			"peer_id", stats.PeerID,
			"leg_listen_port", stats.ListenPort,
			"bind", bind,
			"advertise", advertise,
			"expected", stats.ExpectedIPv4,
			"advertised_udp_port", stats.AdvertisedPort,
			"probe_replay", "per_leg_four_packets_five_seconds",
			"native_probe_listen", r.probeListen,
			"source", "binding_is_the_owner_sessions_observed_game_tcp_ipv4_not_the_cs_op2_report")
	}
}

// bindAddressString 是 relay 实际 ListenUDP 用的 IPv4（可与通告地址不同）。
func (r *currentPartyUDPRelay) bindAddressString() string {
	if r == nil || r.bindIP == nil {
		return ""
	}
	if ipv4 := r.bindIP.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return r.bindIP.String()
}

func (r *currentPartyUDPRelay) log(message string, args ...any) {
	if r != nil && r.logWarn != nil {
		r.logWarn(message, args...)
	}
}

func currentPartyUDPRelayBindings(bindings []currentPartyUDPRelayBinding) []currentPartyUDPRelayBinding {
	unique := make(map[uint16]currentPartyUDPRelayBinding, len(bindings))
	for _, binding := range bindings {
		if binding.characterID == 0 || binding.generation == 0 {
			return nil
		}
		if _, exists := unique[binding.characterID]; exists {
			return nil
		}
		unique[binding.characterID] = binding
	}
	result := make([]currentPartyUDPRelayBinding, 0, len(unique))
	for _, binding := range unique {
		result = append(result, binding)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].characterID < result[right].characterID })
	return result
}

// sortedCurrentPartyUDPRelayLegs 按 (owner_id, peer_id) 排序，让"腿建立"日志逐次可复现。
func sortedCurrentPartyUDPRelayLegs(legs map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg) []*currentPartyUDPRelayLeg {
	if len(legs) == 0 {
		return nil
	}
	result := make([]*currentPartyUDPRelayLeg, 0, len(legs))
	for _, leg := range legs {
		result = append(result, leg)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].key.ownerID != result[right].key.ownerID {
			return result[left].key.ownerID < result[right].key.ownerID
		}
		return result[left].key.peerID < result[right].key.peerID
	})
	return result
}

func currentPartyUDPRelaySourceMatches(source *net.UDPAddr, expected [4]byte) bool {
	if source == nil || source.IP == nil {
		return false
	}
	ip := source.IP.To4()
	return len(ip) == net.IPv4len && ip[0] == expected[0] && ip[1] == expected[1] && ip[2] == expected[2] && ip[3] == expected[3]
}

func cloneCurrentPartyUDPRelayAddr(source *net.UDPAddr) *net.UDPAddr {
	if source == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), source.IP...), Port: source.Port, Zone: source.Zone}
}
