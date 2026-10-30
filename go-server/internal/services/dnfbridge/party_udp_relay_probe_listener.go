package dnfbridge

import (
	"encoding/hex"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件实现 2026-09-25 第七次线上取证（20:46 party_id=1）得出的**单向死锁根因修法**：
// "对端从未在中继端口上发过数据报时，中继手上必须有一个**它自己观测到的**端点，
//
//	否则第一个数据报只能被丢掉/留存，两端互相等待 ⇒ 永久死锁"。
//
// ## 现场证据（香港 38.22.90.52 `/opt/dnf90/logs/dnf90.out`，只读引用）
//
//	23512 20:46:10.362 leg listening owner_id=3 peer_id=4 leg_listen_port=31000 advertised_udp_port=61566
//	23513 20:46:10.362 leg listening owner_id=4 peer_id=3 leg_listen_port=31001 advertised_udp_port=61630
//	23514 20:46:10.434 peer endpoint not learned owner_id=3 peer_id=4 leg=31000 source=39.172.226.229:21574
//	23515 20:46:10.434 probe queued       owner_id=4 peer_id=3 leg=31001 source=39.172.226.229:21574 held_from_owner_id=3
//	23516 20:46:10.434 endpoint fallback used owner_id=4 peer_id=3 leg=31001 target=39.172.226.229:61630
//	23536 20:46:50.001 legs closed received_datagrams=1 leg_forwarded_datagrams=0
//
// 逐行读法（**判读关键，上一轮被误读**）：`peer endpoint not learned` 的 owner/peer/leg 是
// **收到数据报的那条腿**（`logDrop` 用 `leg` 的字段）⇒ 那一个数据报是 **char3(owner 3)** 发到
// **31000** 的，源端口 21574。而 `probe queued` / `endpoint fallback used` 用的是**对向腿**
// 的字段（`handleUnlearnedProbe` / `forwardToAdvertisedEndpoint` 都写 `opposite.*`）⇒ 它们显示
// 31001/owner 4 只代表"要被投递的那一端是 char4"，不代表包来自 char4。
//
// ## 为什么必须"先观测、后投递"
//
//   - 中继的转发目标只有两个可能来源：① 该腿自己收到过的源端点（`learnedEndpoint`）；
//     ② 客户端在 CS op2 里**自报**的端口（`DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK=advertised`）。
//   - ②在加速器部署下**必然不可达**：现场同一次会话里 char3 自报 61566 而它真实的公网源端口是
//     21574（18:15 17488↔21230、19:08 61694↔21281、19:29 61566↔21281 同形）⇒ 自报端口是
//     客户端**本机**端口，加速器做的是端口改写，不是端口保持。
//   - 因此唯一可信的目标是**我们自己观测到的源端点**；而对一个整场不发包的成员，
//     我们只可能在**它组队之前**（或本场任何一次自发流量里）观测到它。
//
// ## 厂商对照（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，只读引用）
//
//	827  10:19:22.838 game-party-peer-endpoint-bound    conn=game-000026 client_pid=42912 udp_port=5063  source=op2_port_cross_validated_by_native_probe
//	1270 10:19:48.016 game-party-peer-endpoint-bound    conn=game-000029 client_pid=46152 udp_port=62565 source=op2_port_cross_validated_by_native_probe
//	2527 10:22:48.808 game-party-peer-endpoint-bound    conn=game-000059 client_pid=42912 udp_port=5063
//	2911 10:22:53.431 game-party-peer-endpoint-bound    conn=game-000061 client_pid=46152 udp_port=62565
//	3357 10:23:05.989 party-udp-relay-payload owner_id=2 source=192.168.0.101:62565   ← 首包
//	3368 10:23:06.011 party-udp-relay-payload owner_id=1 source=192.168.0.101:5063    ← +22ms
//
// 两条硬结论：
//  1. **端点是在组队之前就绑定好的**（10:22:48/53 绑，10:23:05 才组队），而且全文件第一条
//     `party-udp-relay-payload` 出现在 10:23:05.989 ⇒ 10:19~10:22 那些 bind 事件发生时
//     **根本没有任何 party 中继流量** ⇒ 客户端做过一次**未受邀请的 UDP 探针**（native probe），
//     厂商就是用它的**观测源端点**（并与 CS op2 端口交叉校验）填的 binding。
//  2. 正因如此，厂商中继从**第一个数据报**起就能向两端投递，"对向腿未学习"这个窗口在厂商
//     现场从未出现（46,761 条 payload 双向持续流动）。
//
// ## 本文件做什么
//
//   - `DNFBRIDGE_PARTY_UDP_RELAY_PROBE_LISTEN`（**默认 on**）：服务启动即在配置的探针端口上
//     常驻监听（默认 `30000` = 厂商中继起始口，加 `DNFBRIDGE_PARTY_UDP_RELAY_PORT_START`）。
//     收到的每一个数据报都记一条 `party UDP relay native probe observed`（源端点 + 命中端口 +
//     字节数 + 载荷 hex 预览），并按载荷第 2 字节 +1 解析出**发送者角色 id**
//     （依据：厂商 21B 首包 `02 01 …` 来自 uid=2、`02 00 …` 来自 uid=1 ⇒ 第 2 字节 = 发送者 uid-1）。
//     解析成功的端点进 `probes[charID]`，供腿建立时当"种子目标"。
//   - `DNFBRIDGE_PARTY_UDP_RELAY_PROBE_PORTS`（默认 `30000,<PORT_START>`）：探针监听端口列表。
//     探针端口保持专用；定向腿跳过这些端口，容量不足时沿用完整拓扑失败回落。
//   - `DNFBRIDGE_PARTY_UDP_RELAY_SEED_FORWARD`（**默认 on**）：腿建立时把"对端角色已被自己观测到的
//     端点"（原生探针优先，其次同进程端点备忘）写进 `leg.seedEndpoint`；第一个数据报到达而对向腿
//     未学习时，**直接投给种子端点**（而不是丢掉/只留存），并同时启动 relay 级定时清扫器
//     （`party UDP relay pending probe swept` / `probe replay expired`，见 party_udp_relay_probe_replay.go）。
//   - `DNFBRIDGE_PARTY_UDP_RELAY_DATAGRAM_TRACE`（**默认 off**）：每个**入向**数据报一行
//     `party UDP relay datagram`，带 source/bytes/decision/hex 预览。复测时打开它，
//     "谁发了、发到哪个端口、有没有被转发"一眼可见。
//   - `DNFBRIDGE_PARTY_UDP_RELAY_OBSERVED_ENDPOINT_FANOUT`（**默认 on**）：把"探针监听看到过的
//     **每一个**源端点"（无归属、不需要角色 id）接进反向腿的首包投递候选集，候选 kind =
//     `observed_probe_endpoint`。现场依据与安全边界见本文件「无归属观测端点表」一节。
//
// ## 安全边界
//
//   - 探针监听只**读**，不对探针回包（不引入任何新协议行为；厂商侧也只是登记端点）。
//   - 探针端点只有在"载荷解析出的角色 id 与腿的对端 id 相同"且"源 IPv4 == 该角色游戏 TCP
//     会话观测 IP"时才被当作种子 ⇒ 同机双开（两个客户端同一个公网 IP）也不可能把 A 的包
//     投给 B。
//   - 种子/备忘投递每条腿有硬上限（`maxSeedForwards`），不会变成长时间注入器。
//   - 三个开关全默认档 = 本次修法生效；把 `PROBE_LISTEN`/`SEED_FORWARD` 写成 `off` 即逐字节
//     回到加本文件之前的行为（首包被留存或丢弃），可一键回退。
const (
	partyUDPRelayProbeListenEnv   = "DNFBRIDGE_PARTY_UDP_RELAY_PROBE_LISTEN"
	partyUDPRelayProbePortsEnv    = "DNFBRIDGE_PARTY_UDP_RELAY_PROBE_PORTS"
	partyUDPRelaySeedForwardEnv   = "DNFBRIDGE_PARTY_UDP_RELAY_SEED_FORWARD"
	partyUDPRelayDatagramTraceEnv = "DNFBRIDGE_PARTY_UDP_RELAY_DATAGRAM_TRACE"
	// partyUDPRelayObservedEndpointFanoutEnv 是"把无归属观测端点接进首包投递候选集"的开关
	// （**默认 on**，显式 `off` 一键回退）。证据、语义与安全边界见本文件后半
	// 「无归属观测端点表」一节（2026-09-26 02:13 现场取证）。
	partyUDPRelayObservedEndpointFanoutEnv = "DNFBRIDGE_PARTY_UDP_RELAY_OBSERVED_ENDPOINT_FANOUT"
)

const (
	partyUDPRelaySwitchOn  = "on"
	partyUDPRelaySwitchOff = "off"
)

const (
	// defaultPartyUDPRelayNativeProbePort 是**厂商**中继起始口（厂商 op11 行里的 `7530`=30000）。
	//
	// ⚠️ 它**不再是我们的默认监听口**：港机同机跑着 a21-relay（`DfoServer` PID 515，
	// `ss -lunp` 实测占着 UDP 30041-30049+ ⇒ 30000–30255 这一段属于它）。
	// 默认探针端口改成 `DNFBRIDGE_PARTY_UDP_RELAY_PORT_START`(31000) 本身；
	// 只有在别的机器上复刻"客户端硬编码 30000"的厂商行为时才显式配 PROBE_PORTS。
	defaultPartyUDPRelayNativeProbePort = 30000
	// partyUDPRelayNativeProbeReservedLow/High 是 a21-relay 在港机上实测占用的 UDP 段，
	// 配 PROBE_PORTS 时必须避开（否则要么我们绑不上、要么把别人的包收进来）。
	partyUDPRelayNativeProbeReservedLow  = 30000
	partyUDPRelayNativeProbeReservedHigh = 30255
	// partyUDPRelayProbeTTL 是"原生探针端点"的存活时间。加速器/家宽 UDP 映射实测可跨会话复用
	// （19:08 与 19:29 两次同源 :21281），10 分钟足够覆盖一次实测，又不会把过期端口留太久。
	partyUDPRelayProbeTTL = 10 * time.Minute
	// partyUDPRelayProbeLogPerSource 是**同一个源端点**最多记几条探针日志（防误扫/刷屏）。
	partyUDPRelayProbeLogPerSource = 8
	// partyUDPRelayProbeHexPreview 是探针日志里载荷 hex 预览的最大字节数。
	partyUDPRelayProbeHexPreview = 48
	// maxSeedForwards 是每条腿用"候选端点"投递的最大数据报数（防猜测目标变注入器）。
	maxSeedForwards = 64
	// partyUDPRelayObservedEndpointLimit 是"无归属观测端点表"的条数上限。
	// 一次实测里同一台机器最多出现两三个 NAT 端点（不同目的地各一个映射），8 条足够覆盖
	// "同机双开 + 短暂重启"，同时把这张表钉死在常数大小（超出淘汰最旧的一条）。
	partyUDPRelayObservedEndpointLimit = 8
	// partyUDPRelayPendingSweepInterval 是 relay 级定时清扫器的周期。
	partyUDPRelayPendingSweepInterval = 2 * time.Second
)

// partyUDPRelayProbeListenMode / partyUDPRelaySeedForwardMode 是 env 的归一化取值。
// **零值 = Disabled/off = 加本文件之前的旧行为**（所有不经过 optionsFromConfig 的构造路径，
// 例如既有单测与探针里的字面量，行为逐字节不变）。
type partyUDPRelayProbeListenMode int

const (
	partyUDPRelayProbeListenDisabled partyUDPRelayProbeListenMode = iota
	partyUDPRelayProbeListenEnabled
)

func (mode partyUDPRelayProbeListenMode) String() string {
	if mode == partyUDPRelayProbeListenEnabled {
		return partyUDPRelaySwitchOn
	}
	return partyUDPRelaySwitchOff
}

func (mode partyUDPRelayProbeListenMode) enabled() bool {
	return mode == partyUDPRelayProbeListenEnabled
}

type partyUDPRelaySeedForwardMode int

const (
	partyUDPRelaySeedForwardDisabled partyUDPRelaySeedForwardMode = iota
	partyUDPRelaySeedForwardEnabled
)

func (mode partyUDPRelaySeedForwardMode) String() string {
	if mode == partyUDPRelaySeedForwardEnabled {
		return partyUDPRelaySwitchOn
	}
	return partyUDPRelaySwitchOff
}

func (mode partyUDPRelaySeedForwardMode) enabled() bool {
	return mode == partyUDPRelaySeedForwardEnabled
}

// readPartyUDPRelayProbeListenMode 解析 DNFBRIDGE_PARTY_UDP_RELAY_PROBE_LISTEN。
// **默认开启**（空/未设置/`on` = 探针监听生效），只有显式写 `off` 才关闭。
func readPartyUDPRelayProbeListenMode(value string) partyUDPRelayProbeListenMode {
	if strings.EqualFold(strings.TrimSpace(value), partyUDPRelaySwitchOff) {
		return partyUDPRelayProbeListenDisabled
	}
	return partyUDPRelayProbeListenEnabled
}

// readPartyUDPRelaySeedForwardMode 解析 DNFBRIDGE_PARTY_UDP_RELAY_SEED_FORWARD（同上：默认 on）。
func readPartyUDPRelaySeedForwardMode(value string) partyUDPRelaySeedForwardMode {
	if strings.EqualFold(strings.TrimSpace(value), partyUDPRelaySwitchOff) {
		return partyUDPRelaySeedForwardDisabled
	}
	return partyUDPRelaySeedForwardEnabled
}

// readPartyUDPRelayDatagramTraceMode 解析 DNFBRIDGE_PARTY_UDP_RELAY_DATAGRAM_TRACE（默认 off）。
func readPartyUDPRelayDatagramTraceMode(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), partyUDPRelaySwitchOn)
}

// readPartyUDPRelayObservedEndpointFanoutMode 解析
// DNFBRIDGE_PARTY_UDP_RELAY_OBSERVED_ENDPOINT_FANOUT。
//
// **默认 on**（空/未设置/`on` = "无归属观测端点"进候选集），只有显式写 `off` 才关闭；
// `off` 档下探针数据报**连表都不记**，所有既有事件的字段与顺序逐字节不变。
// options 的零值仍是 false，所以不经过 optionsFromConfig 的构造路径
// （既有单测与探针里的字面量）与加本功能之前逐字节一致。
func readPartyUDPRelayObservedEndpointFanoutMode(value string) bool {
	return !strings.EqualFold(strings.TrimSpace(value), partyUDPRelaySwitchOff)
}

// Service 侧访问器（零值 options = 关闭 = 旧行为；默认档由 optionsFromConfig 打开）。
func (s *Service) partyUDPRelayObservedEndpointFanoutMode() bool {
	if s == nil {
		return false
	}
	return s.options.partyUDPRelayObservedEndpointFanout
}

// partyUDPRelayProbePorts 解析探针监听端口列表。
//
// 默认 = **只有** `portStart`（我们的中继起始口 31000）。**默认不再包含厂商口 30000**：
// 港机上 a21-relay（DfoServer，PID 515）实测占着 UDP 30000–30255，抢它要么绑不上、
// 要么把别人的包收进来。要在"客户端硬编码 30000"的环境里复刻厂商 native probe，
// 必须显式写 `DNFBRIDGE_PARTY_UDP_RELAY_PROBE_PORTS=30000`（并确认该口空闲）。
//
// 非法项被丢弃；空表 = 不监听（等价于 PROBE_LISTEN=off 的效果）。
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

// partyUDPRelayProbePortReserved 判断某个端口是否落在 a21-relay 实测占用段里（判读/日志用）。
func partyUDPRelayProbePortReserved(port int) bool {
	return port >= partyUDPRelayNativeProbeReservedLow && port <= partyUDPRelayNativeProbeReservedHigh
}

// Service 侧访问器（零值 options = off = 旧行为，与既有开关约定一致）。
func (s *Service) partyUDPRelayProbeListenMode() partyUDPRelayProbeListenMode {
	if s == nil {
		return partyUDPRelayProbeListenDisabled
	}
	return s.options.partyUDPRelayProbeListen
}

func (s *Service) partyUDPRelaySeedForwardMode() partyUDPRelaySeedForwardMode {
	if s == nil {
		return partyUDPRelaySeedForwardDisabled
	}
	return s.options.partyUDPRelaySeedForward
}

func (s *Service) partyUDPRelayDatagramTraceMode() bool {
	if s == nil {
		return false
	}
	return s.options.partyUDPRelayDatagramTrace
}

func (r *currentPartyUDPRelay) probeListenModeLabel() string {
	if r != nil && r.probeListen {
		return partyUDPRelaySwitchOn
	}
	return partyUDPRelaySwitchOff
}

func (r *currentPartyUDPRelay) seedForwardModeLabel() string {
	if r != nil && r.seedForward {
		return partyUDPRelaySwitchOn
	}
	return partyUDPRelaySwitchOff
}

// currentPartyUDPRelayProbeEndpoint 是"某个角色自己发到中继端口上的、被我们观测到的端点"。
type currentPartyUDPRelayProbeEndpoint struct {
	characterID uint16
	address     [4]byte
	port        uint16
	hitPort     int
	seenAt      time.Time
	payload     []byte
}

// partyUDPRelayProbeCharacterID 从探针载荷里解析发送者角色 id。
// 依据（厂商 9.16 现场，同机双开）：owner_id=2 的首包 `02010000000c00…`、owner_id=1 的首包
// `02000000000c00…` ⇒ 第 2 字节 = 发送者 uid - 1，第 1 字节 = 0x02（P2P 首包族）。
// 解析不可靠时返回 ok=false，调用方**只在 ok 时才把它当种子**（避免同 IP 双开互相串包）。
func partyUDPRelayProbeCharacterID(payload []byte) (uint16, bool) {
	if len(payload) < 2 || payload[0] != 0x02 {
		return 0, false
	}
	return uint16(payload[1]) + 1, true
}

// partyUDPRelayAddrIPv4 把观测到的源地址转成 4 字节（IPv6/未知返回 false）。
func partyUDPRelayAddrIPv4(source *net.UDPAddr) ([4]byte, bool) {
	var address [4]byte
	if source == nil || source.IP == nil || source.Port <= 0 || source.Port > 65535 {
		return address, false
	}
	ipv4 := source.IP.To4()
	if len(ipv4) != net.IPv4len {
		return address, false
	}
	copy(address[:], ipv4)
	return address, true
}

// ============================ 无归属观测端点表 ============================
//
// 2026-09-26 02:13 现场取证（香港 `/opt/dnf90/logs/dnf90.out`，只读引用；
// 探针口配成 `2311,2312,31000` = 登录通告里给客户端的 `DNF_INITIAL_UDP_PORT1/2` + 中继起始口）：
//
//	02:13:09.685 native probe observed listener_port=2311 source=39.172.226.229:21854 bytes=1 payload_hex=01
//	02:13:12.199 native probe observed listener_port=2312 source=39.172.226.229:21863 bytes=1 payload_hex=00
//	02:13:13.253 native probe observed listener_port=2312 source=39.172.226.229:21854 bytes=1 payload_hex=00
//	…30 秒后…
//	02:13:40.806 peer endpoint not learned owner_id=3 peer_id=4 leg=31000 source=39.172.226.229:21863
//	02:13:40.806 fanout candidate used        owner_id=4 peer_id=3 leg=31001 kind=advertised_port target=39.172.226.229:61502
//
// 两条硬结论：
//
//  1. **客户端原生探测的载荷只有 1 字节**（打第一个通告口是 `01`、打第二个是 `00`），
//     **不含 uid** ⇒ `partyUDPRelayProbeCharacterID` 必然解析失败 ⇒ 端点进不了 `probes[charID]`，
//     而 `:21854` / `:21863` 恰恰就是**两台客户端真实的加速器映射端点**
//     （自报的 61502/61630 是内网口，不可达）。
//  2. 我们**明明在 30 秒前就观测到了对端真实端点 `:21854`**，首包却仍然投给不可达的
//     `advertised_port :61502` ⇒ 队员那端永远收不到队长的握手 ⇒ 它的 P2P 永不启动、
//     永不回包 ⇒ 对向腿永远学不到端点（死锁闭合）。
//
// 因此本表把"**每一个**探针数据报的源 `ip:port`"记下来（不需要角色 id），并在反向腿端点未知时
// 作为候选 kind `observed_probe_endpoint` 参与多候选扇出。它与 `probes[charID]`（种子）
// **两条路并存、互不替代**：能解析出角色 id 的走种子（额外要求源 IP == 该角色 TCP 会话观测 IP），
// 解析不出来的走本表。
//
// ## 第二步：候选命中了还不够 —— 必须**从对端探测命中的那个监听口发出**（02:28 现场）
//
// 只把候选接进扇出之后，02:28 实测拿到的是"投出去了、但对面还是 0 包"：
//
//	02:28:17.804 fanout candidate used leg=31001 kind=observed_probe_endpoint target=39.172.226.229:21863
//	02:28:17.804 fanout candidate used leg=31001 kind=advertised_port     target=39.172.226.229:61502
//	02:28:19.772 silent peer kick phase=delayed attempt=1 delivered=true opposite_received=0
//
// 即 `delivered=true` 只证明**本机把包交给了内核**。真正的原因是入向方向被加速器/NAT 过滤：
// 队员的 P2P socket（`:21863`）**只向我们的 2312 发过包**，所以它只接受**来自 2312 口**的入向包；
// 而从腿口 31001 发出去的那一份被丢掉。厂商现场也是同一口径：探测源口与后来的收发口**是同一个
// socket**（`192.168.0.101:5063` 既探测又收发）。
//
// 所以本表按 `(ip:port, hitPort)` 逐条保存（同一客户端打 2311 与 2312 算两条入向路径），
// 投递时用该条 `hitPort` 对应的**探针 socket 作为发送源**；socket 已被 `releasePort` 显式关闭、
// 或探针口未知/监听不在位时，退化回腿口并在 `fanout candidate used` 行记
// `fallback_reason=probe_socket_released|probe_listener_port_unknown|probe_listener_unavailable|
// probe_socket_write_failed`（绝不静默）。判读字段：`via_listener_port=` 与 `delivery_source=`，
// 以及 datagram 行的 `delivery_source_port=`。
//
// ## 安全边界（为什么不是"无条件放开"）
//
//   - 表里的每一条都是**我们自己的 socket 真实收到过**的源端点（内核给出的源地址，
//     不是客户端自报字段），且只记 `ip:port`，不记载荷、不关联角色；
//   - **绝不回投给发送者自己**：候选生成时排除"本条数据报的源端点"（`exclude`）；
//   - 只在"对向腿 `learnedEndpoint == nil`"这个死锁窗口里被使用；对端一旦自己开口，
//     权威目标立刻接管（扇出路径根本不会被走到）；
//   - 仍然受既有的每腿写入上限 `maxSeedForwards`(64) 约束（本 kind 计入 `seedForwardDatagrams`），
//     不会变成长时间注入器；
//   - **用探针 socket 发**只改变"源端口"，不改目标、不改载荷、不新增协议字节；它本来就是
//     我们自己绑的 socket（只多了一个出向用途），且只在上面那个死锁窗口内发生；
//   - TTL = `partyUDPRelayProbeTTL`(10 分钟)，条数上限 = `partyUDPRelayObservedEndpointLimit`(8)，
//     超出淘汰最旧；
//   - 探针口是公网可达的，因此表里**可能**混进扫描器/其它主机的源端点（它们会挤占 MRU 槽位）。
//     代价被上面三条钉死：只有在"对向腿从未开口"这个窗口里才会被用到，每腿总写入上限 64，
//     转发的载荷就是**入向的那一份**（中继从不放大、从不新造字节），且 10 分钟内必然过期。
//     需要零容忍时可以一键 `off`（或收紧 PROBE_PORTS）；
//   - `off` 档下**连表都不记**（`noteObservedProbeEndpoint` 直接返回）⇒ 与加本功能之前逐字节一致。
type currentPartyUDPRelayObservedProbeEndpoint struct {
	address [4]byte
	port    uint16
	hitPort int
	seenAt  time.Time
}

// noteObservedProbeEndpoint 把**每一个**探针数据报的源端点记进"无归属观测端点表"。
//
// 语义：同一条 `(ip:port, hitPort)` 只保留一条（刷新 seenAt 并挪到最前 = MRU 顺序），
// 超出 `partyUDPRelayObservedEndpointLimit` 时淘汰**最旧**的一条。
// **为什么要带上 hitPort 去重**：客户端会往多个通告口各打一次探针（现场 2311 收 `01`、
// 2312 收 `00`），它们是**同一个源 ip:port 的两条不同入向路径**；投递时必须"从对端探测命中的
// 那个监听口发出"才可能穿过加速器/NAT 的入向过滤，所以两条都要留在表里。
// 只读、只记：不产生任何日志、不回包、不改任何协议字节。
func (r *currentPartyUDPRelay) noteObservedProbeEndpoint(source *net.UDPAddr, hitPort int) {
	if r == nil || !r.observedEndpointFanout || source == nil {
		return
	}
	address, ok := partyUDPRelayAddrIPv4(source)
	if !ok {
		return
	}
	entry := currentPartyUDPRelayObservedProbeEndpoint{
		address: address,
		port:    uint16(source.Port),
		hitPort: hitPort,
		seenAt:  time.Now(),
	}
	r.observedMu.Lock()
	defer r.observedMu.Unlock()
	kept := make([]currentPartyUDPRelayObservedProbeEndpoint, 0, len(r.observedEndpoints)+1)
	kept = append(kept, entry)
	for _, existing := range r.observedEndpoints {
		if existing.address == entry.address && existing.port == entry.port && existing.hitPort == entry.hitPort {
			continue
		}
		if len(kept) >= partyUDPRelayObservedEndpointLimit {
			break
		}
		kept = append(kept, existing)
	}
	r.observedEndpoints = kept
}

// observedProbeEndpointEntries 返回"除 `exclude` 之外"的全部仍在 TTL 内的表项（MRU 顺序）。
//
// `exclude` 是**本条数据报的源端点**：中继绝不能把包原样投回发送者自己
// （同机双开时两端同一个公网 IP，只有端口不同 ⇒ 必须按 `ip:port` 精确排除）。
func (r *currentPartyUDPRelay) observedProbeEndpointEntries(exclude *net.UDPAddr) []currentPartyUDPRelayObservedProbeEndpoint {
	if r == nil || !r.observedEndpointFanout {
		return nil
	}
	excluded, hasExcluded := partyUDPRelayAddrIPv4(exclude)
	excludedPort := 0
	if exclude != nil {
		excludedPort = exclude.Port
	}
	now := time.Now()
	r.observedMu.Lock()
	entries := append([]currentPartyUDPRelayObservedProbeEndpoint(nil), r.observedEndpoints...)
	r.observedMu.Unlock()
	kept := make([]currentPartyUDPRelayObservedProbeEndpoint, 0, len(entries))
	for _, entry := range entries {
		if entry.port == 0 || now.Sub(entry.seenAt) > partyUDPRelayProbeTTL {
			continue
		}
		if hasExcluded && entry.address == excluded && int(entry.port) == excludedPort {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// observedProbeEndpointTargets 是本表的"投递目标"视图（按 `ip:port` 去重、MRU 顺序）。
// 它只用于判读与测试；实际投递用 observedProbeEndpointEntries（还需要 hitPort 来选发送源）。
func (r *currentPartyUDPRelay) observedProbeEndpointTargets(exclude *net.UDPAddr) []*net.UDPAddr {
	entries := r.observedProbeEndpointEntries(exclude)
	targets := make([]*net.UDPAddr, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		target := currentPartyUDPRelayObservedTarget(entry)
		key := target.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return targets
}

// currentPartyUDPRelayObservedTarget 把一条表项转成投递目标。
func currentPartyUDPRelayObservedTarget(entry currentPartyUDPRelayObservedProbeEndpoint) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(entry.address[0], entry.address[1], entry.address[2], entry.address[3]),
		Port: int(entry.port),
	}
}

// observedProbeEndpointCount 是表里仍在 TTL 内的**表项**条数（判读与测试用；
// 同一源 ip:port 在多个探针口各命中一次算多条）。
func (r *currentPartyUDPRelay) observedProbeEndpointCount() int {
	return len(r.observedProbeEndpointEntries(nil))
}

// 退化原因（`fallback_reason=` 字段的取值）。
const (
	// observedDeliveryFallbackSocketReleased：想从探针口发，但那个 socket 已被显式关闭。
	observedDeliveryFallbackSocketReleased = "probe_socket_released"
	// observedDeliveryFallbackPortUnknown：表项没记到命中的监听口（测试直接登记等）。
	observedDeliveryFallbackPortUnknown = "probe_listener_port_unknown"
	// observedDeliveryFallbackListenerUnavailable：探针监听整体不在位（未启动/已关闭）。
	observedDeliveryFallbackListenerUnavailable = "probe_listener_unavailable"
	// observedDeliveryFallbackWriteFailed：探针 socket 在"取出之后、写出之前"被关掉，
	// 本次已改用腿口补发。
	observedDeliveryFallbackWriteFailed = "probe_socket_write_failed"
)

// probeListenerConn 返回某个探针监听口当前**活着**的 socket（判读/投递共用）。
// nil = 该口没在监听（未启动、或已被 `releasePort` 显式关闭）。
func (r *currentPartyUDPRelay) probeListenerConn(hitPort int) *net.UDPConn {
	if r == nil || hitPort <= 0 {
		return nil
	}
	listeners := r.probeListeners()
	if listeners == nil {
		return nil
	}
	listeners.mu.Lock()
	defer listeners.mu.Unlock()
	return listeners.conns[hitPort]
}

// observedProbeEndpointDeliverySocket 决定"投给这条观测端点时从哪个 socket 发出"：
//
//   - 该端点当初命中的探针口仍然活着 ⇒ **用它**。现场判据（2026-09-26 02:28）：
//     数据经加速器进入时，对端 P2P socket 只向**我们的 2311/2312** 发过包；我们把队长的
//     握手从腿口 31001 发出去时 `delivered=true` 却 `opposite_received=0`（被丢），
//     厂商现场也是"探测口 = 后来的收发口"（同一 socket）。
//   - 该 socket 已被 `releasePort` 显式关闭 / 探针口未知 / 探针监听不在位 ⇒ 退化回腿口，
//     并返回**具名原因**（`fallback_reason=`），绝不静默。
func (r *currentPartyUDPRelay) observedProbeEndpointDeliverySocket(
	entry currentPartyUDPRelayObservedProbeEndpoint,
) (*net.UDPConn, int, string) {
	if r == nil {
		return nil, 0, observedDeliveryFallbackListenerUnavailable
	}
	if entry.hitPort <= 0 {
		return nil, 0, observedDeliveryFallbackPortUnknown
	}
	if listeners := r.probeListeners(); listeners == nil {
		return nil, 0, observedDeliveryFallbackListenerUnavailable
	}
	conn := r.probeListenerConn(entry.hitPort)
	if conn == nil {
		return nil, 0, observedDeliveryFallbackSocketReleased
	}
	return conn, entry.hitPort, ""
}

// learnedEndpointSnapshot 取"这条腿自己学到的源端点"的快照 —— 也就是**最近一次入向数据报的
// 发送者**（readLoop 在扇出之前刚把它写成这条腿的 learnedEndpoint）。
// 扇出用它来排除"回投给发送者自己"。
func (leg *currentPartyUDPRelayLeg) learnedEndpointSnapshot() *net.UDPAddr {
	if leg == nil {
		return nil
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	return cloneCurrentPartyUDPRelayAddr(leg.learnedEndpoint)
}

// currentPartyUDPRelayProbeListeners 是常驻 UDP 探针监听集合；池内探针口同样保持专用。
// 它只读、只记日志、只登记端点；不参与转发，也不改任何协议字节。
type currentPartyUDPRelayProbeListeners struct {
	relay *currentPartyUDPRelay
	mu    sync.Mutex
	conns map[int]*net.UDPConn
	wg    sync.WaitGroup
	shown map[string]int
	// bound 是**实际**绑到的端口（配置 0 = 由内核分配，主要用于测试与判读）。
	bound []int
	// Immutable after publication to relay.probeListener. Reserve configured
	// ports even when bind fails, and actual ports for kernel-assigned port 0.
	// Closing a probe socket must not turn later keepalives into directed traffic.
	reservedPorts map[int]struct{}
}

// startProbeListeners 在配置的端口上常驻监听。端口 0 = 由内核分配（测试用）。
// 任何一个端口绑不上都只记一条日志并继续（绝不因为探针端口失败而让 dnfbridge 起不来）。
func (r *currentPartyUDPRelay) startProbeListeners(ports []int) {
	if r == nil || !r.probeListen || len(ports) == 0 {
		return
	}
	listeners := &currentPartyUDPRelayProbeListeners{relay: r, conns: make(map[int]*net.UDPConn, len(ports)), shown: make(map[string]int, 32), reservedPorts: make(map[int]struct{}, len(ports))}
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
			"meaning", "any_datagram_arriving_here_before_a_party_exists_is_a_client_side_native_probe_and_tells_us_the_real_nat_endpoint")
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

// probeListenerPorts 返回**当前仍活着**的常驻探针监听端口（显式关闭后会从这里消失；
// 判读与测试都用它）。
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

// formatPartyUDPRelayPorts 把端口列表格式化成日志用的 `a,b,c`。
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

func (l *currentPartyUDPRelayProbeListeners) readLoop(port int, conn *net.UDPConn) {
	defer l.wg.Done()
	buffer := make([]byte, currentPartyUDPRelayMaxDatagram)
	for {
		n, source, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if n <= 0 {
			continue
		}
		l.relay.noteNativeProbe(port, source, buffer[:n])
		// viaPort 档：本端口就是"被通告给两端的共享 P2P 端口"，首字节 0x02 的载荷
		// （厂商 P2P 首包族）必须**转发给对端**，而不是只登记。off 档该函数入口即 return。
		l.relay.relayProbePortP2P(port, source, buffer[:n])
	}
}

// releasePort 显式关闭一个探针 socket，但保留其专用端口；不用于定向腿分配。
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

// close 关掉全部探针 socket（中继 Close 时调用）。
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

// noteNativeProbe 记一条原生探针，并在能解析出角色 id 时登记端点。
func (r *currentPartyUDPRelay) noteNativeProbe(hitPort int, source *net.UDPAddr, payload []byte) {
	if r == nil || source == nil || len(payload) == 0 {
		return
	}
	address, ok := partyUDPRelayAddrIPv4(source)
	if !ok {
		return
	}
	characterID, parsed := partyUDPRelayProbeCharacterID(payload)
	if parsed {
		r.mu.Lock()
		if r.probes == nil {
			r.probes = make(map[uint16]currentPartyUDPRelayProbeEndpoint, 4)
		}
		r.probes[characterID] = currentPartyUDPRelayProbeEndpoint{
			characterID: characterID,
			address:     address,
			port:        uint16(source.Port),
			hitPort:     hitPort,
			seenAt:      time.Now(),
			payload:     append([]byte(nil), payload...),
		}
		r.mu.Unlock()
	}
	// **无论载荷能否解析出角色 id**，每一个数据报的源端点都进"无归属观测端点表"：
	// 现场实测的原生探测载荷只有 1 字节（打 2311 是 `01`、打 2312 是 `00`），
	// 解析必然失败，而它正是首包投递唯一缺的那一份真值（见本文件"无归属观测端点表"一节）。
	// 放在日志节流之前：节流只影响日志，绝不影响端点登记。
	r.noteObservedProbeEndpoint(source, hitPort)
	if !r.logNativeProbeOnce(hitPort, source) {
		return
	}
	fields := []any{
		"listener_port", hitPort,
		"source", source.String(),
		"bytes", len(payload),
		"payload_hex", partyUDPRelayPayloadPreview(payload),
	}
	if parsed {
		fields = append(fields, "char_id", characterID, "char_id_source", "payload_second_byte_plus_one")
	} else {
		fields = append(fields, "char_id", 0, "char_id_source", "unparsed_payload_first_byte_not_0x02")
	}
	fields = append(fields, "meaning", "a_client_sent_udp_to_our_relay_port_without_an_active_party_this_is_the_vendor_native_probe_path")
	r.log("party UDP relay native probe observed", fields...)
}

// logNativeProbeOnce 限制同一个源端点最多记 partyUDPRelayProbeLogPerSource 条日志。
func (r *currentPartyUDPRelay) logNativeProbeOnce(hitPort int, source *net.UDPAddr) bool {
	listeners := r.probeListeners()
	if listeners == nil || source == nil {
		return true
	}
	key := strconv.Itoa(hitPort) + "|" + source.String()
	listeners.mu.Lock()
	defer listeners.mu.Unlock()
	if listeners.shown[key] >= partyUDPRelayProbeLogPerSource {
		return false
	}
	listeners.shown[key]++
	return true
}

func (r *currentPartyUDPRelay) probeListeners() *currentPartyUDPRelayProbeListeners {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.probeListener
}

// probeEndpointFor 返回该角色**被我们观测到**的最近探针端点。
// 两个前置条件：解析出的角色 id 相同、源 IPv4 == 该角色游戏 TCP 会话观测 IP（同 IP 双开也不能串）。
func (r *currentPartyUDPRelay) probeEndpointFor(characterID uint16, address [4]byte) *net.UDPAddr {
	if r == nil || characterID == 0 {
		return nil
	}
	r.mu.Lock()
	probe, ok := r.probes[characterID]
	r.mu.Unlock()
	if !ok || probe.characterID != characterID || probe.address != address || probe.port == 0 {
		return nil
	}
	if time.Since(probe.seenAt) > partyUDPRelayProbeTTL {
		return nil
	}
	return &net.UDPAddr{
		IP:   net.IPv4(address[0], address[1], address[2], address[3]),
		Port: int(probe.port),
	}
}

// seedEndpointFor 是"腿建立时能给这条腿的 owner 写死的目标"的取值顺序：
//  1. 原生探针端点（本场/组队前该角色自己发到中继端口、且载荷解析出的角色 id 对得上）；
//  2. 同角色端点备忘（本进程内该角色**自己发过**的真实端点）。
//
// 方向：腿 (owner=O, peer=P) 的 socket 是广告给 O 的那个口，它的出向投递目标永远是 **O 自己**
// （learnedEndpoint 就是 O 的源端点、pending 队列里也是要投给 O 的包）。所以这里的 characterID
// 传的是**腿的 owner**，address 是该 owner 自己 binding 里的 TCP 观测 IPv4。
//
// 两者都必须满足 IP == 该角色游戏 TCP 会话观测 IP；都没有则返回 nil（= 旧行为）。
func (r *currentPartyUDPRelay) seedEndpointFor(characterID uint16, address [4]byte) *net.UDPAddr {
	if r == nil || characterID == 0 {
		return nil
	}
	if endpoint := r.probeEndpointFor(characterID, address); endpoint != nil {
		return endpoint
	}
	memo := r.lookupEndpointMemo(characterID)
	if memo == nil {
		return nil
	}
	port, ok := memo.portFor(address)
	if !ok {
		return nil
	}
	return &net.UDPAddr{
		IP:   net.IPv4(address[0], address[1], address[2], address[3]),
		Port: int(port),
	}
}

// partyUDPRelayPayloadPreview 生成载荷 hex 预览（超长截断并标注）。
func partyUDPRelayPayloadPreview(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if len(payload) <= partyUDPRelayProbeHexPreview {
		return hex.EncodeToString(payload)
	}
	return hex.EncodeToString(payload[:partyUDPRelayProbeHexPreview]) + "…"
}

// noteSeedForward 记一次"用种子端点投递"（成功/失败各一计数），并返回是否首次。
func (leg *currentPartyUDPRelayLeg) noteSeedForward(size int, failed bool) (uint64, bool) {
	if leg == nil {
		return 0, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if failed {
		leg.seedForwardFailed++
		first := !leg.loggedSeedFailure
		leg.loggedSeedFailure = true
		return leg.seedForwardFailed, first
	}
	leg.seedForwardDatagrams++
	leg.seedForwardBytes += uint64(size)
	first := !leg.loggedSeedForward
	leg.loggedSeedForward = true
	return leg.seedForwardDatagrams, first
}

// noteSeedExhausted 在达到 maxSeedForwards 时只记一次日志。
func (leg *currentPartyUDPRelayLeg) noteSeedExhausted() bool {
	if leg == nil {
		return false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if leg.loggedSeedExhausted {
		return false
	}
	leg.loggedSeedExhausted = true
	return true
}

// forwardToSeedEndpoint 是 readLoop 在"对向腿端点未知"分支上的**根因修法**：
// 用"这条腿的对端角色已被我们观测到的端点"（原生探针 → 端点备忘）立刻投一包过去。
// 厂商实现就是靠这个（组队前已 bind 的两端端点）从第一个数据报起双向可达。
//
// 返回 true 表示数据报已经从对向腿的 socket 写出（调用方不必再走自报端口回落猜测）。
func (r *currentPartyUDPRelay) forwardToSeedEndpoint(
	leg *currentPartyUDPRelayLeg,
	opposite *currentPartyUDPRelayLeg,
	payload []byte,
) bool {
	if r == nil || !r.seedForward || leg == nil || opposite == nil || len(payload) == 0 {
		return false
	}
	opposite.mu.Lock()
	if opposite.learnedEndpoint != nil || opposite.conn == nil {
		opposite.mu.Unlock()
		return false
	}
	target := cloneCurrentPartyUDPRelayAddr(opposite.seedEndpoint)
	exhausted := opposite.seedForwardDatagrams >= maxSeedForwards
	opposite.mu.Unlock()
	if target == nil {
		return false
	}
	if exhausted {
		if opposite.noteSeedExhausted() {
			r.log("party UDP relay seed endpoint exhausted",
				"party_id", opposite.key.partyID,
				"owner_id", opposite.key.ownerID,
				"peer_id", opposite.key.peerID,
				"leg_listen_port", opposite.port,
				"target", target.String(),
				"datagrams", uint64(maxSeedForwards),
				"meaning", "the_seed_endpoint_is_not_reachable_stop_injecting")
		}
		return false
	}
	written, err := r.writeCurrentPairDatagram(leg, opposite, opposite.conn, payload, target)
	if err != nil || written != len(payload) {
		failed, first := opposite.noteSeedForward(0, true)
		if first {
			r.log("party UDP relay seed endpoint failed",
				"party_id", opposite.key.partyID,
				"owner_id", opposite.key.ownerID,
				"peer_id", opposite.key.peerID,
				"leg_listen_port", opposite.port,
				"target", target.String(),
				"failed_datagrams", failed,
				"error", errText(err, written, len(payload)))
		}
		return false
	}
	datagrams, first := opposite.noteSeedForward(len(payload), false)
	if first {
		r.log("party UDP relay seed endpoint used",
			"party_id", opposite.key.partyID,
			"owner_id", opposite.key.ownerID,
			"peer_id", opposite.key.peerID,
			"leg_listen_port", opposite.port,
			"target", target.String(),
			"datagrams", datagrams,
			"bytes", len(payload),
			"meaning", "the_peer_endpoint_was_observed_before_the_party_started_so_the_first_datagram_was_delivered_instead_of_held")
	}
	return true
}

// traceRelayDatagram 是 DNFBRIDGE_PARTY_UDP_RELAY_DATAGRAM_TRACE=on 时"每个入向数据报一行"。
// 复测时打开它即可一眼看出：谁发了、发到哪个端口、中继做了什么决定。
// deliverySourcePort 缺省为 0（= 本条数据报没有投出去）。
func (r *currentPartyUDPRelay) traceRelayDatagram(
	leg *currentPartyUDPRelayLeg,
	source *net.UDPAddr,
	size int,
	payload []byte,
	decision string,
) {
	r.traceRelayDatagramDelivered(leg, source, size, payload, decision, 0)
}

// traceRelayDatagramDelivered 与 traceRelayDatagram 相同，另带 `delivery_source_port=`：
// 本次投递**实际使用的源端口** —— 观测端点档是它探测命中的探针口（2311/2312/…），
// 其余候选与退化路径是腿口。判读"是不是从探针口发出去的"就看这个字段。
// 0 = 没有投出去（此时不写该字段）。
func (r *currentPartyUDPRelay) traceRelayDatagramDelivered(
	leg *currentPartyUDPRelayLeg,
	source *net.UDPAddr,
	size int,
	payload []byte,
	decision string,
	deliverySourcePort int,
) {
	if r == nil || !r.datagramTrace || leg == nil {
		return
	}
	sourceText := ""
	if source != nil {
		sourceText = source.String()
	}
	fields := []any{
		"party_id", leg.key.partyID,
		"owner_id", leg.key.ownerID,
		"peer_id", leg.key.peerID,
		"leg_listen_port", leg.port,
		"source", sourceText,
		"bytes", size,
		"decision", decision,
	}
	if deliverySourcePort > 0 {
		fields = append(fields, "delivery_source_port", deliverySourcePort)
	}
	fields = append(fields, "payload_hex", partyUDPRelayPayloadPreview(payload))
	r.log("party UDP relay datagram", fields...)
}

// memoTTL 开关：DNFBRIDGE_PARTY_UDP_RELAY_MEMO_TTL_SECONDS（默认 30 秒）。
//
// 依据（2026-09-25 21:07 / 21:15 两次现场）：加速器 NAT 端口只存活几十秒到两分钟。
// 21:15 那次三份连续转发的目标 `39.172.226.229:21590` 是 **21:07 那次**留下的旧端口
// （相隔约 8 分钟）⇒ 全部被加速器丢掉、对端一个包都没收到。所以陈旧记忆**不能单独使用**，
// 只能作为多候选扇出里的一份（成本≈0，但不指望它）。
const partyUDPRelayMemoTTLSecondsEnv = "DNFBRIDGE_PARTY_UDP_RELAY_MEMO_TTL_SECONDS"

const defaultPartyUDPRelayMemoTTLSeconds = 30

// readPartyUDPRelayMemoTTL 解析 MEMO_TTL_SECONDS：空/非法/<=0 都回默认 30 秒；上限 = memoTTL。
func readPartyUDPRelayMemoTTL(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultPartyUDPRelayMemoTTLSeconds * time.Second
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return defaultPartyUDPRelayMemoTTLSeconds * time.Second
	}
	if time.Duration(seconds)*time.Second > memoTTL {
		return memoTTL
	}
	return time.Duration(seconds) * time.Second
}

// Service 侧访问器（零值/非法 = 默认 30 秒）。
func (s *Service) partyUDPRelayMemoTTL() time.Duration {
	if s == nil || s.options.partyUDPRelayMemoTTL <= 0 {
		return defaultPartyUDPRelayMemoTTLSeconds * time.Second
	}
	return s.options.partyUDPRelayMemoTTL
}

// partyUDPRelayCandidate 是"反向腿端点未知"时的一个投递候选（多候选扇出用）。
type partyUDPRelayCandidate struct {
	kind   string
	target *net.UDPAddr
	fresh  bool
	// via 非空 = **用这个 socket 作为发送源**（而不是腿的 socket）。目前只有
	// `observed_probe_endpoint` 会带它：现场判据是与对端 P2P socket 只向我们**探测命中的那个
	// 监听口**放行入向包（见 observedProbeEndpointDeliverySocket）。
	via *net.UDPConn
	// viaPort 是 via 绑定的端口（判读 `via_listener_port=` 用；via == nil 时由投递方填腿口）。
	viaPort int
	// fallbackReason 非空 = 本想从探针口发、但该 socket 不可用，**退化回腿口**的原因
	// （observedDeliveryFallback* 之一；会写进 `fanout candidate used` 的 `fallback_reason=`）。
	fallbackReason string
}

// unlearnedCandidateTargets 汇总所有可用候选（按可靠性排序、按 ip:port 去重）：
//
//	seed_probe               —— 该角色**在组队前/本场自己发到中继端口**被我们观测到的端点
//	                            （最可信：还要求载荷解析出的角色 id 对得上、源 IP == 该角色 TCP 观测 IP）
//	observed_probe_endpoint  —— 与目标成员会话IPv4一致、且不是本条数据报发送者的观测端点
//	                            （不需要角色 id；保留同IP不同NAT端口的1字节探针引导）
//	seed_memo_fresh          —— 该角色自己发过的端点，且新鲜（≤ MEMO_TTL_SECONDS）
//	memo_stale               —— 同上但已陈旧（仍发一份，成本≈0，但绝不单独依赖它）
//	advertised_port          —— 该角色 CS op2 自报端口 + 游戏 TCP 观测 IP（加速器下通常不可达）
//
// `sender` 是**本条数据报的源端点**：`observed_probe_endpoint` 候选必须排除它，
// 否则中继会把包原样投回发送者自己（同机双开时两端同一个公网 IP、只有端口不同）。
//
// 所有候选的 IP 都必须等于该角色游戏 TCP 会话观测到的 IPv4（binding.address）。
func (r *currentPartyUDPRelay) unlearnedCandidateTargets(
	skipMemo bool,
	opposite *currentPartyUDPRelayLeg,
	sender *net.UDPAddr,
) []partyUDPRelayCandidate {
	if r == nil || opposite == nil {
		return nil
	}
	address := opposite.binding.address
	candidates := make([]partyUDPRelayCandidate, 0, 6)
	seen := make(map[string]struct{}, 6)
	add := func(kind string, target *net.UDPAddr, fresh bool, via *net.UDPConn, viaPort int, fallbackReason string) {
		if target == nil || target.Port <= 0 {
			return
		}
		key := target.String()
		if via != nil {
			// 同一个目标可能有多条"入向路径"（同一源 ip:port 打到 2311 与 2312 各一次）：
			// 路径不同就都要投，所以带上发送源口一起去重。
			key += "|probe:" + strconv.Itoa(viaPort)
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, partyUDPRelayCandidate{
			kind:           kind,
			target:         target,
			fresh:          fresh,
			via:            via,
			viaPort:        viaPort,
			fallbackReason: fallbackReason,
		})
	}
	if probe := r.probeEndpointFor(opposite.key.ownerID, address); probe != nil {
		add("seed_probe", probe, true, nil, 0, "")
	}
	// 无归属观测端点（默认档；`off` 时 entries 返回 nil ⇒ 与旧行为一致）。
	// 每条表项都从"它当初探测命中的那个监听口"发出（socket 已释放时退化回腿口并记因）。
	for _, entry := range r.observedProbeEndpointEntries(sender) {
		// Observation alone does not associate an arbitrary host with this
		// member. Keep the same authenticated IPv4 boundary as normal reads.
		if entry.address != address {
			continue
		}
		via, viaPort, fallbackReason := r.observedProbeEndpointDeliverySocket(entry)
		target := currentPartyUDPRelayObservedTarget(entry)
		add("observed_probe_endpoint", target, true, via, viaPort, fallbackReason)
		// 同一条观测端点**再从腿口发一份**（`via == nil` ⇒ 源 = 腿 socket）。
		//
		// 现场依据（2026-09-26 04:30:52，party_id=1 owner_id=1 peer_id=4）：
		// 队长（角色 1）把 21B 握手发到了腿口 31000，我们的扇出把同一份载荷投给了
		// 队员（角色 4）当时**唯一**在表里的观测端点 `:21210`——但只从探针口
		// 2311/2312 发出（`delivery_source=probe_listener_socket`），对端 `opposite_received=0`
		// 且此后 60s 内一个字节都没有回来（`probe replay expired total_expired_datagrams=1`）。
		//
		// 两条入向路径**互不替代**，各有各的死角：
		//   - 探针口那份：对端 NAT 一定放行（它的 P2P socket 刚向 2311/2312 发过探针），
		//     但**源端口是我们自己的探针口**，而 op11 通告给对端的"对端地址"是**腿口**；
		//     当前客户端只认被通告的那个端点作源，源不匹配的那份会被丢。
		//   - 腿口这份：源端口正是对端期待的那个（31000/31001），协议上一定认；
		//     但若对端从未向腿口发过包，它的 NAT 可能挡掉（取决于 NAT 类型）。
		// 所以两条都投：成本是每腿每个候选 +1 个数据报（仍受每腿 `maxSeedForwards` 上限约束，
		// 载荷是**入向的那一份**，中继从不放大、从不新造字节），收益是"哪条真能到"在日志里
		// 一眼可判（`fanout candidate used` 的 `via_listener_port` 会显式区分腿口与探针口）。
		add("observed_probe_endpoint", target, true, nil, 0, "")
	}
	if memo := r.lookupEndpointMemo(opposite.key.ownerID); memo != nil && !skipMemo {
		if port, ok := memo.portForTTL(address, r.memoTTL); ok {
			add("seed_memo_fresh", currentPartyUDPRelayMemoTarget(address, port), true, nil, 0, "")
		} else if port, ok := memo.portFor(address); ok {
			add("memo_stale", currentPartyUDPRelayMemoTarget(address, port), false, nil, 0, "")
		}
	}
	if r.endpointFallback && opposite.binding.advertisedPort != 0 {
		add("advertised_port", currentPartyUDPRelayMemoTarget(address, opposite.binding.advertisedPort), false, nil, 0, "")
	}
	return candidates
}

// currentPartyUDPRelayMemoTarget 用观测 IP + 端口拼一个候选目标。
func currentPartyUDPRelayMemoTarget(address [4]byte, port uint16) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(address[0], address[1], address[2], address[3]),
		Port: int(port),
	}
}

// noteFanOut 记一次候选投递（成功/失败），返回是否是**该腿该候选的首次**日志。
func (leg *currentPartyUDPRelayLeg) noteFanOut(kind string, size int, failed bool) bool {
	if leg == nil {
		return false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if leg.fanOutWritten == nil {
		leg.fanOutWritten = make(map[string]uint64, 4)
	}
	if leg.fanOutLogged == nil {
		leg.fanOutLogged = make(map[string]bool, 4)
	}
	if failed {
		leg.fanOutFailed++
		key := kind + "|failed"
		if leg.fanOutLogged[key] {
			return false
		}
		leg.fanOutLogged[key] = true
		return true
	}
	leg.fanOutWritten[kind]++
	switch kind {
	case "seed_probe", "seed_memo_fresh", "observed_probe_endpoint":
		leg.seedForwardDatagrams++
		leg.seedForwardBytes += uint64(size)
	case "advertised_port":
		leg.endpointFallbackDatagrams++
		leg.endpointFallbackBytes += uint64(size)
	case "memo_stale":
		leg.staleMemoDatagrams++
	}
	if leg.fanOutLogged[kind] {
		return false
	}
	leg.fanOutLogged[kind] = true
	return true
}

// noteFanOutSource 返回"该候选 kind **从该源口**发出"是否首次（每个 (kind, 源口) 组合只记一条）。
//
// 为什么不能只看 kind（noteFanOut 的 fanOutLogged）：同一个 kind 可能有多条入向路径
// —— 同一个源 `ip:port` 打到 2311 与 2312 各一次，两条路径的**发送源口不同**，
// 判读"到底从哪个口发出去的"时两条都必须出现在日志里。
func (leg *currentPartyUDPRelayLeg) noteFanOutSource(kind string, viaPort int) bool {
	if leg == nil {
		return false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if leg.fanOutSourceLogged == nil {
		leg.fanOutSourceLogged = make(map[string]bool, 4)
	}
	key := kind + "|" + strconv.Itoa(viaPort)
	if leg.fanOutSourceLogged[key] {
		return false
	}
	leg.fanOutSourceLogged[key] = true
	return true
}

// noteFanOutExhausted 达到每腿候选写入上限时只记一次。
func (leg *currentPartyUDPRelayLeg) noteFanOutExhausted() bool {
	if leg == nil {
		return false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if leg.loggedFanOutExhausted {
		return false
	}
	leg.loggedFanOutExhausted = true
	return true
}

// fanOutUnlearnedDatagram 是 readLoop 在"对向腿端点未知"分支上的**多候选扇出**：
// 把同一个数据报**同时**投给所有已知候选（探针端点 / 无归属观测端点 / 新鲜备忘 / 陈旧备忘 / 自报端口）。
//
// 为什么扇出而不是"挑一个"：所有候选都是**猜测**，而每一次入向数据报都是一次唤醒对端的机会；
// 逐个候选各发一份的成本是几个字节，收益是"哪一条真的能到"在日志里一眼可判
// （`party UDP relay fanout candidate used` 带 `kind=`）。
//
// `observed_probe_endpoint` 这一档（2026-09-26 02:13/02:28 现场）是最后一块拼图，分两步：
//
//  1. 候选本身：队长已把首包发到中继、而队员的真实端点我们几十秒前刚从它自己的 1 字节原生
//     探测里看到过；旧实现只投不可达的自报端口（`advertised_port`），队员永远不开口。
//  2. **发送源口**：02:28 现场 `delivered=true` 而 `opposite_received=0` 说明"投出去"≠"送得到"——
//     对端 P2P socket 只向**我们探测命中的那个监听口**放过包（厂商现场同样是"探测口 = 收发口"，
//     同一个 socket），所以这一档**从该端点 `hitPort` 对应的探针 socket 发出**，
//     而不是从腿口。socket 已被 `releasePort` 释放时退化回腿口并记
//     `fallback_reason=probe_socket_released`（同族原因见 observedDeliveryFallback*）。
//
// 候选生成时必须排除本条数据报的发送者自己（见 unlearnedCandidateTargets 的 `sender`）。
//
// skipMemo=true 时跳过备忘候选（调用方已经用备忘投过一次，避免同一条包重复投）。
// 返回 (是否有任何候选写成功, 成功候选的 kind 串, 本次实际使用的**投递源端口**；
// 0 = 一条都没投出去)。
func (r *currentPartyUDPRelay) fanOutUnlearnedDatagram(
	leg *currentPartyUDPRelayLeg,
	opposite *currentPartyUDPRelayLeg,
	payload []byte,
	skipMemo bool,
) (bool, string, int) {
	if r == nil || !r.seedForward || leg == nil || opposite == nil || len(payload) == 0 {
		return false, "", 0
	}
	// 发送者 = 最近一次入向数据报的源端点（readLoop 在扇出之前刚写入这条腿）。
	// 缓存备忘重投路径（resendPendingProbes）里同样是"这条腿的 owner 发来的那份载荷"。
	sender := leg.learnedEndpointSnapshot()
	candidates := r.unlearnedCandidateTargets(skipMemo, opposite, sender)
	if len(candidates) == 0 {
		return false, "", 0
	}
	delivered := false
	deliverySourcePort := 0
	kinds := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		opposite.mu.Lock()
		conn := opposite.conn
		capReached := opposite.seedForwardDatagrams >= maxSeedForwards
		opposite.mu.Unlock()
		if conn == nil {
			continue
		}
		if capReached && candidate.kind != "advertised_port" {
			if opposite.noteFanOutExhausted() {
				r.log("party UDP relay fanout exhausted",
					"party_id", opposite.key.partyID,
					"owner_id", opposite.key.ownerID,
					"peer_id", opposite.key.peerID,
					"leg_listen_port", opposite.port,
					"datagrams", uint64(maxSeedForwards),
					"meaning", "every_candidate_was_tried_the_configured_number_of_times_stop_injecting")
			}
			continue
		}
		// 发送源：观测端点候选带 `via` 时用**探针口的 socket**（对端只认这个源口），
		// 其余候选（含退化后的观测端点）走腿口 = 旧行为。
		source := conn
		sourcePort := int(opposite.port)
		viaSource := "leg_socket"
		fallbackReason := candidate.fallbackReason
		if candidate.via != nil {
			source = candidate.via
			sourcePort = candidate.viaPort
			viaSource = "probe_listener_socket"
			fallbackReason = ""
		}
		written, err := r.writeCurrentPairDatagram(leg, opposite, source, payload, candidate.target, candidate.kind == "observed_probe_endpoint")
		if (err != nil || written != len(payload)) && candidate.via != nil {
			// 探针 socket 在"取出之后、写出之前"被 releasePort 关掉（显式关闭）：改用腿口补发。
			viaErr := err
			written, err = r.writeCurrentPairDatagram(leg, opposite, conn, payload, candidate.target, candidate.kind == "observed_probe_endpoint")
			if err == nil && written == len(payload) {
				sourcePort = int(opposite.port)
				viaSource = "leg_socket"
				fallbackReason = observedDeliveryFallbackWriteFailed
			} else {
				err = viaErr
			}
		}
		if err != nil || written != len(payload) {
			if opposite.noteFanOut(candidate.kind, 0, true) {
				fields := []any{
					"party_id", opposite.key.partyID,
					"owner_id", opposite.key.ownerID,
					"peer_id", opposite.key.peerID,
					"leg_listen_port", opposite.port,
					"kind", candidate.kind,
					"target", candidate.target.String(),
					"fresh", candidate.fresh,
					"via_listener_port", sourcePort,
					"delivery_source", viaSource,
					"error", errText(err, written, len(payload)),
				}
				if fallbackReason != "" {
					fields = append(fields, "fallback_reason", fallbackReason)
				}
				r.log("party UDP relay fanout candidate failed", fields...)
			}
			continue
		}
		kinds = append(kinds, candidate.kind)
		delivered = true
		// `delivery_source_port` 记**第一条成功投递的源口**（= 首选路径；观测端点档下
		// 就是它探测命中的那个探针口）。后续候选（同一条观测端点的腿口那份）不覆盖它，
		// 否则这个判读字段会随候选遍历顺序漂移，现场就不能从 datagram 行读出主路径了；
		// 每条路径各自的源口看 `fanout candidate used` 的 `via_listener_port`。
		if deliverySourcePort == 0 {
			deliverySourcePort = sourcePort
		}
		// 计数仍然按 kind 累计；"首次日志"按 (kind, 源口) 判定（见 noteFanOutSource）。
		opposite.noteFanOut(candidate.kind, len(payload), false)
		if opposite.noteFanOutSource(candidate.kind, sourcePort) {
			fields := []any{
				"party_id", opposite.key.partyID,
				"owner_id", opposite.key.ownerID,
				"peer_id", opposite.key.peerID,
				"leg_listen_port", opposite.port,
				"kind", candidate.kind,
				"target", candidate.target.String(),
				"fresh", candidate.fresh,
				// via_listener_port = 本次真正用的**发送源端口**：观测端点档默认是它探测命中的
				// 探针口（2311/2312/…），其余候选与退化路径是腿口。
				"via_listener_port", sourcePort,
				"delivery_source", viaSource,
				"bytes", len(payload),
				"meaning", "reverse_leg_endpoint_unknown_the_datagram_was_sent_to_every_known_candidate_for_this_member",
			}
			if fallbackReason != "" {
				fields = append(fields, "fallback_reason", fallbackReason)
			}
			r.log("party UDP relay fanout candidate used", fields...)
		}
	}
	return delivered, strings.Join(kinds, "+"), deliverySourcePort
}
