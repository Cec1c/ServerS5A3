package dnfbridge

import (
	"errors"
	"fmt"
	"net"
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
	partyID uint16
	ownerID uint16
	peerID  uint16
}

type currentPartyUDPRelayLeg struct {
	key     currentPartyUDPRelayLegKey
	binding currentPartyUDPRelayBinding
	conn    *net.UDPConn
	port    uint16

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
	emptyPayload          uint64
	rejectedSource        uint64
	missingOppositeLeg    uint64
	unlearnedPeerEndpoint uint64
	// loggedXxx 保证每种丢弃原因只在**首次**记一条日志，避免刷屏。
	loggedEmpty     bool
	loggedSource    bool
	loggedMissing   bool
	loggedUnlearned bool

	// 下面是"自报端点回落"这一个单变量实验的计数与一次性日志开关
	// （开关见 party_udp_relay_endpoint_fallback.go；关闭时这些计数恒为 0）。
	endpointFallbackWrites          uint64
	endpointFallbackDatagrams       uint64
	endpointFallbackBytes           uint64
	endpointFallbackFailed          uint64
	loggedEndpointFallbackWrite     bool
	loggedEndpointFallbackFailure   bool
	loggedEndpointFallbackExhausted bool
	loggedLearnedAfterFallback      bool

	// 下面是"首包留存重放"（party_udp_relay_probe_replay.go；关闭时计数恒为 0）。
	// endpointKnown 区分"第一次学到真实端点"（= 该补投待投队列）与之后的重复学习。
	endpointKnown          bool
	pending                pendingProbeDatagrams
	replayedDatagrams      uint64
	replayedBytes          uint64
	loggedReplay           bool
	memoBootstraps         uint64
	memoBootstrapDatagrams uint64
	memoBootstrapBytes     uint64
	memoBootstrapFailed    uint64
	loggedMemoBootstrap    bool

	// seedEndpoint 是"腿建立时就已经能写死的对端目标"（原生探针观测优先，其次同角色端点备忘；
	// 见 party_udp_relay_probe_listener.go 的 seedEndpointFor）。nil = 该对端从未被我们观测到，
	// 行为与加种子之前完全一致（首包留存/丢弃）。
	seedEndpoint         *net.UDPAddr
	seedForwardDatagrams uint64
	seedForwardBytes     uint64
	seedForwardFailed    uint64
	loggedSeedForward    bool
	loggedSeedFailure    bool
	loggedSeedExhausted  bool

	// 多候选扇出（party_udp_relay_probe_listener.go）：fanOutWritten/fanOutLogged 按候选 kind
	// 计数与"首次日志"，staleMemoDatagrams 单独计陈旧备忘候选，fanOutFailed 是候选写失败总数。
	// memoTTL 是本腿"新鲜备忘"的判定上限（DNFBRIDGE_PARTY_UDP_RELAY_MEMO_TTL_SECONDS）。
	fanOutWritten map[string]uint64
	fanOutLogged  map[string]bool
	// fanOutSourceLogged 是"候选 kind + 发送源口"的首日志去重（同一 kind 可能有多条入向路径，
	// 例如同一源 ip:port 打到 2311 与 2312 各一次 ⇒ 两条路径必须各记一条）。
	fanOutSourceLogged    map[string]bool
	fanOutFailed          uint64
	staleMemoDatagrams    uint64
	loggedFanOutExhausted bool
	memoTTL               time.Duration
	// 让沉默对端开口（party_udp_relay_silent_peer_kick.go）：kicks 是已催次数，
	// lastKickAt 是上一次动作时刻，kickRunning 保证每条腿只有一个看门狗，
	// probeResends 是"沉默期间重投待投首包"的轮次。
	kicks        uint32
	lastKickAt   time.Time
	kickRunning  bool
	probeResends uint64
	// createdAt 是腿绑定时刻（看门狗的最小年龄判定用）。
	createdAt time.Time
	// pendingWindow 是待投队列的"周期重投总窗口"（DNFBRIDGE_PARTY_UDP_RELAY_RESEND_SECONDS，默认 60s；
	// 零值 ⇒ 回落到 pendingTTL=20s，旧构造器/既有单测行为逐字节不变）。
	pendingWindow time.Duration
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
	ListenPort            uint16
	PartyID               uint16
	OwnerID               uint16
	PeerID                uint16
	ExpectedIPv4          string
	LearnedEndpoint       string
	AdvertisedPort        uint16
	Received              uint64
	Forwarded             uint64
	BytesForwarded        uint64
	EmptyPayload          uint64
	RejectedSource        uint64
	MissingOppositeLeg    uint64
	UnlearnedPeerEndpoint uint64
	// EndpointFallback* 只在 DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK=advertised
	// 时非零（默认关闭时恒为 0，判读时可据此确认开关状态）。
	EndpointFallbackDatagrams uint64
	EndpointFallbackBytes     uint64
	EndpointFallbackFailed    uint64
	// EndpointKnown/LearnedEndpoint 与 Pending*/Replayed*/MemoBootstrap* 属于
	// "首包留存重放"（party_udp_relay_probe_replay.go）。关闭时全部为 0/false。
	EndpointKnown          bool
	PendingDatagrams       int
	PendingBytes           int
	PendingDropped         uint64
	PendingExpired         uint64
	ReplayedDatagrams      uint64
	ReplayedBytes          uint64
	MemoBootstrapDatagrams uint64
	MemoBootstrapBytes     uint64
	MemoBootstrapFailed    uint64
	MemoBootstrapWrites    uint64
	// SeedEndpoint/SeedForward* 属于"种子端点投递"（party_udp_relay_probe_listener.go）。
	// 关闭时 SeedEndpoint 为空且计数恒为 0。
	SeedEndpoint         string
	SeedForwardDatagrams uint64
	SeedForwardBytes     uint64
	SeedForwardFailed    uint64
	// 多候选扇出（同上文件）：StaleMemoDatagrams 是"陈旧备忘"候选的写入数，
	// FanOutFailed 是候选写失败总数，FanOutKinds 是各候选 kind 的写入数。
	StaleMemoDatagrams uint64
	FanOutFailed       uint64
	FanOutKinds        map[string]uint64
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
		Received:              leg.received,
		Forwarded:             leg.forwarded,
		BytesForwarded:        leg.bytesForwarded,
		EmptyPayload:          leg.emptyPayload,
		RejectedSource:        leg.rejectedSource,
		MissingOppositeLeg:    leg.missingOppositeLeg,
		UnlearnedPeerEndpoint: leg.unlearnedPeerEndpoint,

		EndpointFallbackDatagrams: leg.endpointFallbackDatagrams,
		EndpointFallbackBytes:     leg.endpointFallbackBytes,
		EndpointFallbackFailed:    leg.endpointFallbackFailed,

		EndpointKnown:          leg.endpointKnown,
		PendingDatagrams:       len(leg.pending.payloads),
		PendingBytes:           leg.pending.bytes,
		PendingDropped:         leg.pending.dropped,
		PendingExpired:         leg.pending.expired,
		ReplayedDatagrams:      leg.replayedDatagrams,
		ReplayedBytes:          leg.replayedBytes,
		MemoBootstrapDatagrams: leg.memoBootstrapDatagrams,
		MemoBootstrapBytes:     leg.memoBootstrapBytes,
		MemoBootstrapFailed:    leg.memoBootstrapFailed,
		MemoBootstrapWrites:    leg.memoBootstraps,

		SeedForwardDatagrams: leg.seedForwardDatagrams,
		SeedForwardBytes:     leg.seedForwardBytes,
		SeedForwardFailed:    leg.seedForwardFailed,

		StaleMemoDatagrams: leg.staleMemoDatagrams,
		FanOutFailed:       leg.fanOutFailed,
	}
	if len(leg.fanOutWritten) > 0 {
		stats.FanOutKinds = make(map[string]uint64, len(leg.fanOutWritten))
		for kind, count := range leg.fanOutWritten {
			stats.FanOutKinds[kind] = count
		}
	}
	if leg.seedEndpoint != nil {
		stats.SeedEndpoint = leg.seedEndpoint.String()
	}
	if leg.learnedEndpoint != nil {
		stats.LearnedEndpoint = leg.learnedEndpoint.String()
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
	return leg.forwarded, leg.bytesForwarded
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
	legs      map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg
	closed    bool
	wg        sync.WaitGroup

	// forwardedTotal 是全部腿（含已关闭的）累计转发的数据报数，原子累加，不参与转发逻辑。
	forwardedTotal atomic.Uint64

	// endpointFallback 是 DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK 的归一化结果。
	// false（默认）= 反向腿未学习时照旧丢弃（现行为，逐字节不变）。
	endpointFallback bool

	// probeReplay 是 DNFBRIDGE_PARTY_UDP_RELAY_PROBE_REPLAY 的归一化结果（本文件之外，
	// 见 party_udp_relay_probe_replay.go）。false = 首包照旧丢弃（加开关前行为）。
	probeReplay bool
	// probeListen / seedForward / datagramTrace 是 party_udp_relay_probe_listener.go 的三个
	// 开关：常驻原生探针监听、种子端点投递（+ relay 级待投清扫）、每个入向数据报一行 trace。
	// 三者零值（false）= 加该文件之前的旧行为。
	probeListen   bool
	seedForward   bool
	datagramTrace bool
	// probes 是"按角色 id 解析出来的原生探针端点"（见 noteNativeProbe）。
	probes map[uint16]currentPartyUDPRelayProbeEndpoint
	// observedEndpointFanout 是 DNFBRIDGE_PARTY_UDP_RELAY_OBSERVED_ENDPOINT_FANOUT 的归一化结果
	// （**默认 on**，见 party_udp_relay_probe_listener.go「无归属观测端点表」一节）。
	// false = 不记表、不生成 observed_probe_endpoint 候选 = 加本功能之前逐字节一致。
	observedEndpointFanout bool
	// observedMu/observedEndpoints 是"无归属观测端点表"：探针监听收到的**每一个**数据报的
	// 源 ip:port（MRU 顺序，TTL=partyUDPRelayProbeTTL，上限=partyUDPRelayObservedEndpointLimit）。
	// 它不需要角色 id —— 现场客户端的原生探测载荷只有 1 字节。
	observedMu        sync.Mutex
	observedEndpoints []currentPartyUDPRelayObservedProbeEndpoint
	// memoTTL 是"新鲜备忘"的判定上限（DNFBRIDGE_PARTY_UDP_RELAY_MEMO_TTL_SECONDS，默认 30 秒）。
	memoTTL time.Duration
	// kickTiming 是"催沉默端 + 重投待投首包"的节拍（见 party_udp_relay_silent_peer_kick.go）。
	kickTiming partyUDPRelayKickTiming
	// onSilentPeer 是"让沉默对端开口"的回调（party_udp_relay_silent_peer_kick.go）。nil = 不做。
	onSilentPeer func(partyID, memberID uint16) bool
	// done 在 Close 时关闭：所有 relay 级后台 goroutine（清扫器、看门狗）都监听它。
	done chan struct{}
	// pendingWindow 是待投队列的"周期重投总窗口"（DNFBRIDGE_PARTY_UDP_RELAY_RESEND_SECONDS，
	// 默认 60s；零值 = 旧的一次性 pendingTTL，旧构造器路径行为不变）。
	pendingWindow time.Duration
	// probeListener 是常驻探针监听集合；腿要绑某个被它占用的端口时会先释放该端口。
	probeListener *currentPartyUDPRelayProbeListeners
	// sweeperStop 关闭 relay 级待投清扫器（见 sweepPendingProbes）。
	sweeperStop chan struct{}
	// endpointMemo 是"每个角色自己发过的最近真实端点"，只用于对向腿未学习时的引导，
	// 目标 IP 必须等于该角色游戏 TCP 会话观察到的 IPv4。
	memoMu       sync.Mutex
	endpointMemo map[uint16]currentPartyUDPRelayEndpointMemo

	logWarn func(string, ...any)

	// viaPort 是 DNFBRIDGE_PARTY_UDP_RELAY_VIA_PORT 的归一化结果（**0 = off = 现行为**）。
	//
	// >0 时中继换一种拓扑：op11 通告给两端的不再是**各自的腿口**，而是这个**共享端口**
	// （现场取 2312 = 客户端唯一持续保活的那个 UDP 端口），P2P 载荷也从这个端口收发。
	// 现场依据与边界见 party_udp_relay_via_port.go。
	viaPort int
}

// currentPartyUDPRelaySettings 是 currentPartyUDPRelay 的全部构造参数。
// 旧构造器（newCurrentPartyUDPRelay / ...WithEndpointFallback / ...WithProbeReplay）保留原来的
// 签名并把新字段留零值，因此既有单测/探针路径行为逐字节不变。
type currentPartyUDPRelaySettings struct {
	enabled          bool
	advertiseIP      string
	bindHost         string // Empty preserves the original advertise-address binding.
	portStart        int
	portCount        int
	endpointFallback bool
	probeReplay      bool
	probeListen      bool
	probePorts       []int
	seedForward      bool
	datagramTrace    bool
	// observedEndpointFanout 打开"无归属观测端点进候选集"（同上文件；默认档由
	// optionsFromConfig 打开，零值 false = 旧行为）。
	observedEndpointFanout bool
	memoTTL                time.Duration
	kickTiming             partyUDPRelayKickTiming
	pendingWindow          time.Duration
	onSilentPeer           func(partyID, memberID uint16) bool
	logWarn                func(string, ...any)
	// viaPort >0 = 用共享端口替代定向腿口（见 currentPartyUDPRelay.viaPort）。
	viaPort int
}

func newCurrentPartyUDPRelay(enabled bool, advertiseIP string, portStart, portCount int, logWarn func(string, ...any)) (*currentPartyUDPRelay, error) {
	return newCurrentPartyUDPRelayWithEndpointFallback(enabled, advertiseIP, portStart, portCount, false, logWarn)
}

// newCurrentPartyUDPRelayWithEndpointFallback 与 newCurrentPartyUDPRelay 相同，只是额外
// 打开"反向腿未学习时回落到对端**自报**端口"这一个单变量实验。默认构造路径
// （endpointFallback=false）与加开关之前逐字节一致。
func newCurrentPartyUDPRelayWithEndpointFallback(
	enabled bool,
	advertiseIP string,
	portStart, portCount int,
	endpointFallback bool,
	logWarn func(string, ...any),
) (*currentPartyUDPRelay, error) {
	return newCurrentPartyUDPRelayWithProbeReplay(enabled, advertiseIP, portStart, portCount, endpointFallback, false, logWarn)
}

// newCurrentPartyUDPRelayWithProbeReplay 是全参构造：额外带上"首包留存重放"
// （party_udp_relay_probe_replay.go）。probeReplay=false 的路径与加该开关之前逐字节一致，
// 所以既有单测/探针（用前两个构造器或零值结构体）行为不受影响。
func newCurrentPartyUDPRelayWithProbeReplay(
	enabled bool,
	advertiseIP string,
	portStart, portCount int,
	endpointFallback bool,
	probeReplay bool,
	logWarn func(string, ...any),
) (*currentPartyUDPRelay, error) {
	return newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:          enabled,
		advertiseIP:      advertiseIP,
		portStart:        portStart,
		portCount:        portCount,
		endpointFallback: endpointFallback,
		probeReplay:      probeReplay,
		logWarn:          logWarn,
	})
}

// newCurrentPartyUDPRelayWithSettings 是全参构造：额外带上"常驻原生探针监听 / 种子端点投递 /
// 每个入向数据报 trace"（party_udp_relay_probe_listener.go）。三个新字段为 false 时
// 与加该文件之前逐字节一致（首包只留存或丢弃）。
func newCurrentPartyUDPRelayWithSettings(settings currentPartyUDPRelaySettings) (*currentPartyUDPRelay, error) {
	enabled := settings.enabled
	advertiseIP := settings.advertiseIP
	portStart := settings.portStart
	portCount := settings.portCount
	endpointFallback := settings.endpointFallback
	probeReplay := settings.probeReplay
	logWarn := settings.logWarn
	relay := &currentPartyUDPRelay{
		enabled:          enabled,
		endpointFallback: endpointFallback,
		probeReplay:      probeReplay,
		probeListen:      settings.probeListen,
		seedForward:      settings.seedForward,
		datagramTrace:    settings.datagramTrace,
		// 零值 false = 旧行为（不记表、不生成 observed_probe_endpoint 候选）。
		observedEndpointFanout: settings.observedEndpointFanout,
		memoTTL:                settings.memoTTL,
		kickTiming:             settings.kickTiming,
		onSilentPeer:           settings.onSilentPeer,
		viaPort:                settings.viaPort,
		logWarn:                logWarn,
		done:                   make(chan struct{}),
	}
	if relay.memoTTL <= 0 || relay.memoTTL > memoTTL {
		relay.memoTTL = defaultPartyUDPRelayMemoTTLSeconds * time.Second
	}
	if relay.kickTiming.interval <= 0 {
		relay.kickTiming = readPartyUDPRelayKickTiming("", "")
	}
	relay.pendingWindow = settings.pendingWindow
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
	relay.legs = make(map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg)
	relay.probes = make(map[uint16]currentPartyUDPRelayProbeEndpoint)
	// 常驻原生探针监听（默认档；绑不上只记日志，绝不影响 dnfbridge 启动）。
	relay.startProbeListeners(settings.probePorts)
	// relay 级待投清扫器：待投队列 TTL 到期必须能被**记出来**（旧实现只在同一条腿再次收到
	// 数据报时才会去检查 TTL ⇒ "对端整场不发"这种最该被记出来的情形反而永远静默）。
	if probeReplay && settings.seedForward {
		relay.sweeperStop = make(chan struct{})
		relay.wg.Add(1)
		go relay.pendingProbeSweeper(relay.sweeperStop)
	}
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
	if r == nil || !r.enabled {
		return false
	}
	bindings = currentPartyUDPRelayBindings(bindings)
	if partyID == 0 || len(bindings) < 2 || len(bindings) > currentPartyUDPRelayMaxMembers {
		return false
	}

	wanted := make(map[currentPartyUDPRelayLegKey]currentPartyUDPRelayBinding, len(bindings)*(len(bindings)-1))
	for _, owner := range bindings {
		for _, peer := range bindings {
			if owner.characterID == peer.characterID {
				continue
			}
			wanted[currentPartyUDPRelayLegKey{partyID: partyID, ownerID: owner.characterID, peerID: peer.characterID}] = owner
		}
	}

	// 种子端点在**取 r.mu 之前**算好：seedEndpointFor 内部要读 r.mu 保护的探针表，
	// 而在下面持锁的循环里取锁会自锁（2026-09-25 单测当场抓到过）。
	seeds := make(map[uint16]*net.UDPAddr, len(bindings))
	if r.seedForward || r.probeListen {
		for _, binding := range bindings {
			seeds[binding.characterID] = r.seedEndpointFor(binding.characterID, binding.address)
		}
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return false
	}
	newLegs := make(map[currentPartyUDPRelayLegKey]*currentPartyUDPRelayLeg)
	for key, binding := range wanted {
		if existing := r.legs[key]; existing != nil && existing.binding == binding {
			continue
		}
		leg, err := r.newLegLocked(key, binding)
		if err != nil {
			for _, created := range newLegs {
				r.releaseLegLocked(created)
			}
			r.mu.Unlock()
			r.log("party UDP relay synchronization deferred", "party_id", partyID, "error", err)
			return false
		}
		// 腿建立时就把"这条腿的 owner 已被我们观测到的端点"写死（原生探针 → 端点备忘）。
		// 方向核对：腿 (owner=O, peer=P) 的 socket 是**广告给 O** 的那个口，它的出向投递
		// （learnedEndpoint / pending 队列 / 投递目标）全部指向 **O 自己**；因此种子必须取
		// owner（= O）的观测端点，IP 校验用它自己 binding 里那份 TCP 观测 IPv4。
		// 有了它，对面那个人发来的第一个数据报会被**立即投给 O**，而不是被留存/丢弃后两端互等。
		leg.seedEndpoint = seeds[key.ownerID]
		newLegs[key] = leg
	}

	toClose := make([]*currentPartyUDPRelayLeg, 0)
	for key, existing := range r.legs {
		if key.partyID != partyID {
			continue
		}
		if binding, wanted := wanted[key]; !wanted || existing.binding != binding {
			toClose = append(toClose, existing)
			delete(r.legs, key)
			delete(r.usedPorts, int(existing.port))
		}
	}
	for key, leg := range newLegs {
		r.legs[key] = leg
	}
	r.mu.Unlock()

	for _, leg := range toClose {
		_ = leg.conn.Close()
	}
	r.logLegClosure(toClose, "party_membership_changed")
	r.logLegCreation(sortedCurrentPartyUDPRelayLegs(newLegs))
	for _, leg := range newLegs {
		r.wg.Add(1)
		go r.readLoop(leg)
	}
	return true
}

// CloseParty releases all directed ports for a party immediately. It is used
// when the current client creates a new PartyID during leadership transfer.
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
	if r.sweeperStop != nil {
		close(r.sweeperStop)
		r.sweeperStop = nil
	}
	if r.done != nil {
		close(r.done)
		r.done = nil
	}
	listeners := r.probeListener
	r.probeListener = nil
	legs := make([]*currentPartyUDPRelayLeg, 0, len(r.legs))
	for _, leg := range r.legs {
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
	viaPort := r.viaPort
	r.mu.Unlock()
	if leg == nil {
		return [4]byte{}, 0, false
	}
	// viaPort 档（DNFBRIDGE_PARTY_UDP_RELAY_VIA_PORT>0）：两端都被通告**同一个共享端口**，
	// 因为现场那个 NAT 只放行"客户端自己持续发包的那一个目的端口"的入向
	// （见 party_udp_relay_via_port.go 的现场依据）。默认（viaPort=0）逐字节不变。
	if viaPort > 0 && viaPort <= 65535 {
		return address, uint16(viaPort), true
	}
	return address, leg.port, true
}

func (r *currentPartyUDPRelay) newLegLocked(key currentPartyUDPRelayLegKey, binding currentPartyUDPRelayBinding) (*currentPartyUDPRelayLeg, error) {
	for offset := 0; offset < r.portCount; offset++ {
		port := r.portStart + offset
		if _, used := r.usedPorts[port]; used {
			continue
		}
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: append(net.IP(nil), r.bindIP...), Port: port})
		if err != nil {
			// 常驻原生探针监听可能正占着这个端口（PROBE_PORTS 配到了中继范围时）：
			// 先释放它再重试一次。探针监听**永不阻塞中继**。
			if r.probeListener != nil && r.probeListener.releasePort(port) {
				conn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: append(net.IP(nil), r.bindIP...), Port: port})
			}
			if err != nil {
				continue
			}
		}
		r.usedPorts[port] = struct{}{}
		return &currentPartyUDPRelayLeg{
			key:           key,
			binding:       binding,
			conn:          conn,
			port:          uint16(port),
			memoTTL:       r.memoTTL,
			createdAt:     time.Now(),
			pendingWindow: r.pendingWindow,
		}, nil
	}
	return nil, fmt.Errorf("no free ports in configured range %d-%d", r.portStart, r.portStart+r.portCount-1)
}

func (r *currentPartyUDPRelay) releaseLegLocked(leg *currentPartyUDPRelayLeg) {
	if leg == nil {
		return
	}
	delete(r.usedPorts, int(leg.port))
	_ = leg.conn.Close()
}

func (r *currentPartyUDPRelay) removePartyLocked(partyID uint16) []*currentPartyUDPRelayLeg {
	legs := make([]*currentPartyUDPRelayLeg, 0)
	for key, leg := range r.legs {
		if key.partyID != partyID {
			continue
		}
		delete(r.legs, key)
		delete(r.usedPorts, int(leg.port))
		legs = append(legs, leg)
	}
	return legs
}

func (r *currentPartyUDPRelay) readLoop(leg *currentPartyUDPRelayLeg) {
	defer r.wg.Done()
	buffer := make([]byte, currentPartyUDPRelayMaxDatagram)
	for {
		n, source, err := leg.conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if n <= 0 {
			if count, first := leg.noteDrop(relayLegDropEmptyPayload); first {
				r.logDrop(leg, relayLegDropEmptyPayload, count, nil,
					"meaning", "client_sent_a_zero_length_udp_datagram")
			}
			r.traceRelayDatagram(leg, source, n, nil, "dropped_empty_payload")
			continue
		}
		if !currentPartyUDPRelaySourceMatches(source, leg.binding.address) {
			if count, first := leg.noteDrop(relayLegDropSourceMismatch); first {
				r.logDrop(leg, relayLegDropSourceMismatch, count, source,
					"expected", formatCurrentPartyUDPRelayIPv4(leg.binding.address),
					"meaning", "expected_is_the_game_tcp_sessions_observed_ipv4_the_client_cs_op2_report_is_not_used_here")
			}
			r.traceRelayDatagram(leg, source, n, buffer[:n], "dropped_source_mismatch")
			continue
		}
		leg.noteReceived()
		leg.mu.Lock()
		firstLearn := !leg.endpointKnown
		leg.endpointKnown = true
		leg.learnedEndpoint = cloneCurrentPartyUDPRelayAddr(source)
		leg.mu.Unlock()
		r.rememberEndpoint(leg, source)
		if firstLearn {
			// 首包留存重放：这条腿第一次学到"写给它的对端"的真实端点 ⇒ 把之前为它
			// 留存下来的（对向腿的）首包原样补投过去。off 档下本函数立即返回。
			r.flushPendingProbes(leg)
		}
		r.logPendingProbeExpiry(leg)
		if leg.noteLearnedEndpointAfterFallback() {
			// 判定性证据：这条腿先前只被"自报端点回落"喂过包，现在它**自己**发来了数据报
			// ⇒ 对端客户端被唤醒并回话了（响应式模型成立），且从这里开始 learnedEndpoint
			// 就是权威目标，回落路径自动让位。
			r.log("party UDP relay endpoint fallback learned real endpoint",
				"party_id", leg.key.partyID,
				"owner_id", leg.key.ownerID,
				"peer_id", leg.key.peerID,
				"leg_listen_port", leg.port,
				"learned_endpoint", source.String(),
				"meaning", "the_leg_that_was_only_fed_by_the_advertised_endpoint_guess_has_now_sent_its_own_datagram")
		}

		r.mu.Lock()
		opposite := r.legs[currentPartyUDPRelayLegKey{
			partyID: leg.key.partyID,
			ownerID: leg.key.peerID,
			peerID:  leg.key.ownerID,
		}]
		r.mu.Unlock()
		if opposite == nil {
			if count, first := leg.noteDrop(relayLegDropMissingOppositeLeg); first {
				r.logDrop(leg, relayLegDropMissingOppositeLeg, count, source,
					"meaning", "relay_topology_changed_mid_flight_party_close_or_member_leave")
			}
			r.traceRelayDatagram(leg, source, n, buffer[:n], "dropped_missing_opposite_leg")
			continue
		}
		opposite.mu.Lock()
		destination := cloneCurrentPartyUDPRelayAddr(opposite.learnedEndpoint)
		opposite.mu.Unlock()
		if destination == nil {
			// 这条是**最隐蔽的黑洞**：对面那条腿还没发过任何数据报 ⇒ 没学到 endpoint ⇒
			// 本方向的包全被丢弃。旧实现静默 continue，"通道建了但单向没通"看起来与
			// "客户端一个包都没发"完全一样。
			if count, first := leg.noteDrop(relayLegDropUnlearnedPeerEndpoint); first {
				r.logDrop(leg, relayLegDropUnlearnedPeerEndpoint, count, source,
					"meaning", "the_opposite_directed_leg_has_not_sent_yet_so_its_endpoint_is_unknown")
			}
			// 判定顺序（**先真值、后猜测、能发多少发多少**）：
			//   ① 留存首包（对端一开口就补投）+ 新鲜备忘引导（≤ MEMO_TTL_SECONDS；
			//      party_udp_relay_probe_replay.go）；
			//   ② 多候选扇出（party_udp_relay_probe_listener.go）：原生探针端点 / 陈旧备忘 /
			//      自报端口 —— 每个候选各投一份、各自计数与首次日志，绝不静默；
			//   ③ 让沉默的对端开口（party_udp_relay_silent_peer_kick.go，严格限量）。
			memoWrote := r.handleUnlearnedProbe(leg, opposite, source, buffer[:n])
			fanOut, kinds, deliverySourcePort := r.fanOutUnlearnedDatagram(leg, opposite, buffer[:n], memoWrote)
			if !fanOut {
				// 没有任何候选可投时仍然走自报端口回落（它受旧开关控制，默认 off = 旧行为）。
				r.forwardToAdvertisedEndpoint(leg, opposite, buffer[:n])
			}
			// 让沉默的对端开口（+ 沉默期间周期重投待投首包）：**看门狗**，不是一次性内联判断。
			// 23:31 那次的时序 bug 就是"唯一的数据报在腿龄 65ms 时到达、一次性判断因 minAge=2s
			// 直接 return，之后再没有数据报 ⇒ 永远不会再评估"。现在起一个 per-leg goroutine，
			// 睡到腿龄够 minAge 再动手，之后按 interval 周期重试，直到对端开口/腿被回收/relay 关闭。
			r.watchSilentPeer(leg, opposite)
			decision := "held_queued_for_replay"
			switch {
			case fanOut:
				decision = "fanout_" + kinds
			case memoWrote:
				decision = "memo_bootstrap"
			}
			// delivery_source_port：真相在这一格 —— 无归属观测端点档是从**探针口**发出去的
			// （现场判据：对端只认它自己探测过的那个源口），其余候选与退化路径是腿口。
			r.traceRelayDatagramDelivered(leg, source, n, buffer[:n], decision, deliverySourcePort)
			continue
		}
		if _, err := opposite.conn.WriteToUDP(buffer[:n], destination); err != nil {
			r.log("party UDP relay forward failed", "party_id", leg.key.partyID, "owner_id", leg.key.ownerID, "peer_id", leg.key.peerID, "error", err)
			r.traceRelayDatagram(leg, source, n, buffer[:n], "forward_failed")
			continue
		}
		datagrams, bytesForwarded, first := leg.noteForwarded(n)
		r.forwardedTotal.Add(1)
		r.traceRelayDatagram(leg, source, n, buffer[:n], "forwarded_learned_endpoint")
		if first {
			r.log("party UDP relay leg carries traffic",
				"party_id", leg.key.partyID,
				"owner_id", leg.key.ownerID,
				"peer_id", leg.key.peerID,
				"listen_port", leg.port,
				"datagrams", datagrams,
				"bytes", bytesForwarded,
				"meaning", "client_p2p_path_is_live_for_this_directed_pair")
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
	var forwarded, received, idle uint64
	var emptyPayload, rejectedSource, missingOpposite, unlearnedPeer uint64
	var fallbackDatagrams, fallbackFailed uint64
	var pendingDropped, pendingExpired, replayed, memoDatagrams, memoFailed uint64
	var seedDatagrams, seedFailed uint64
	for _, leg := range legs {
		stats := leg.relayLegStatsSnapshot()
		forwarded += stats.Forwarded
		received += stats.Received
		emptyPayload += stats.EmptyPayload
		rejectedSource += stats.RejectedSource
		missingOpposite += stats.MissingOppositeLeg
		unlearnedPeer += stats.UnlearnedPeerEndpoint
		fallbackDatagrams += stats.EndpointFallbackDatagrams
		fallbackFailed += stats.EndpointFallbackFailed
		pendingDropped += stats.PendingDropped
		pendingExpired += stats.PendingExpired
		replayed += stats.ReplayedDatagrams
		memoDatagrams += stats.MemoBootstrapDatagrams
		memoFailed += stats.MemoBootstrapFailed
		seedDatagrams += stats.SeedForwardDatagrams
		seedFailed += stats.SeedForwardFailed
		if stats.Forwarded == 0 && stats.ReplayedDatagrams == 0 && stats.SeedForwardDatagrams == 0 {
			idle++
		}
	}
	r.log("party UDP relay legs closed",
		"reason", reason,
		"legs", len(legs),
		"idle_legs", idle,
		"received_datagrams", received,
		"leg_forwarded_datagrams", forwarded,
		"total_forwarded_datagrams", r.ForwardedDatagramTotal(),
		"rejected_source_datagrams", rejectedSource,
		"dropped_empty_datagrams", emptyPayload,
		"dropped_missing_opposite_leg", missingOpposite,
		"dropped_unlearned_peer_endpoint", unlearnedPeer,
		"endpoint_fallback_datagrams", fallbackDatagrams,
		"endpoint_fallback_failed", fallbackFailed,
		"endpoint_fallback_mode", r.endpointFallbackModeLabel(),
		// 首包留存重放（party_udp_relay_probe_replay.go）：`replayed_probe_datagrams > 0`
		// 就是"单向死锁已被打破"的机器证据；`expired_probe_datagrams > 0` 则是
		// "这一端整场一个数据报都没发"的机器证据。
		"probe_replay_mode", r.probeReplayModeLabel(),
		"replayed_probe_datagrams", replayed,
		"dropped_pending_probe_datagrams", pendingDropped,
		"expired_probe_datagrams", pendingExpired,
		"endpoint_memo_bootstrap_datagrams", memoDatagrams,
		"endpoint_memo_bootstrap_failed", memoFailed,
		// 种子端点投递（party_udp_relay_probe_listener.go）：`seed_forward_datagrams > 0` 就是
		// "对端在组队前已被我们观测到，所以第一个数据报直接被投出去"的机器证据。
		"native_probe_listen_mode", r.probeListenModeLabel(),
		"seed_forward_mode", r.seedForwardModeLabel(),
		"seed_forward_datagrams", seedDatagrams,
		"seed_forward_failed", seedFailed)
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
			// seed_endpoint = 腿建立时就已经能写死的对端目标（原生探针观测 → 端点备忘）。
			// 非空即代表"这一端的第一个数据报会被立即投递"；空 = 只能等对端自己先发包。
			"seed_endpoint", stats.SeedEndpoint,
			"endpoint_fallback", r.endpointFallbackModeLabel(),
			"probe_replay", r.probeReplayModeLabel(),
			"native_probe_listen", r.probeListenModeLabel(),
			"seed_forward", r.seedForwardModeLabel(),
			"source", "binding_is_the_owner_sessions_observed_game_tcp_ipv4_not_the_cs_op2_report")
	}
}

// endpointFallbackModeLabel 是 DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK 当前档位的
// 日志标签（`off` = 默认现行为 / `advertised` = 打开自报端点回落）。leg 建立与腿汇总
// 都带这个字段，判读时不必去翻 env。
func (r *currentPartyUDPRelay) endpointFallbackModeLabel() string {
	if r != nil && r.endpointFallback {
		return partyUDPRelayEndpointFallbackValueAdvertised
	}
	return partyUDPRelayEndpointFallbackValueOff
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
