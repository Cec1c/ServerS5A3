package dnfbridge

import (
	"net"
	"strconv"
	"strings"
	"time"
)

// 本文件实现 2026-09-25 线上取证得出的**单向死锁最小修法**：
// "先到的那一个首包，不再被丢掉，而是在对端端点被学到的那一刻原样补投过去"。
//
// ## 现场因果链（香港 38.22.90.52 `/opt/dnf90/logs/dnf90.out`，只读引用）
//
// 六次组队实测（05:39 / 16:20 / 16:36 / 18:15 / 19:08 / 19:29）的腿汇总**全部**是：
//
//	party UDP relay legs closed reason=relay_close legs=2 idle_legs=2 \
//	  received_datagrams=1 leg_forwarded_datagrams=0 total_forwarded_datagrams=0 \
//	  rejected_source_datagrams=0 dropped_unlearned_peer_endpoint=1
//
// 即：**整场只有 1 个数据报到达中继**（19:08 那次在腿 31000，来源 `39.172.226.229:21281`；
// 19:29 那次在腿 31001，来源同一个 `21281`），它被 `unlearned_peer_endpoint` 丢掉，
// 此后**再没有任何数据报**。两次现场里"发过首包的那一端"还互换了（19:08 = char3，
// 19:29 = char4），说明**两端都具备发首包的能力**，卡住的不是客户端而是中继的丢弃语义。
//
// ## 为什么"只发一个包"就死了
//
// 中继是"每有序对一个 socket"，转发目标只能从**该腿自己的首包源**学（`party_udp_relay.go`
// 的 `leg.learnedEndpoint = source`）。于是 2 人队必然出现这个窗口：
//
//	A 的首包到达腿(A->B) -> 对向腿(B->A) 还没学过 => 目标未知 => **A 的首包被丢**
//	B 的首包到达腿(B->A) -> 对向腿(A->B) 已学过  => 正常转发
//
// 只要两端首包**都**到达，系统自愈（第二包起双向都通）。参考实现（厂商 9.16 本机
// `D:\DNF90-test\...\packet_log.txt`）在同机/局域网下两端首包相隔 **22 ms**
// （`10:23:05.989` owner2 / `10:23:06.011` owner1，各 21 B），所以从不暴露这个窗口；
// 公网部署下两端首包可能相隔数秒甚至某一端整场只发一个包，窗口就变成永久死锁。
//
// ## 修法（本文件）
//
//  1. **首包留存重放（probe replay，核心）**：腿(A->B) 收到 A 的数据报而对向腿
//     `learnedEndpoint == nil` 时，把该数据报**按原样**放进**对向腿**(B->A) 的待投队列
//     （对向腿才是"写给 B"的那个 socket）；一旦对向腿学到 B 的真实端点，
//     队列立刻用**对向腿自己的 socket** 投给 B。=> A 的首包不再浪费：
//     B 只要在任何时刻发了第一个包，就立刻收到 A 的首包，两端同时进入"收到过对端"状态。
//  2. **同角色端点备忘（endpoint memo）**：中继记住**每个角色自己发过**的最近真实端点
//     （IP 必须等于该角色游戏 TCP 会话观察到的 IPv4，见 `binding.address`），
//     下一次组队时若对向腿尚未学过，就用这个**真实观测过的**端点当引导目标。
//     依据：现场 NAT 端口在同一台机器上跨会话稳定（19:08 与 19:29 两次的源端口都是
//     `21281`），所以这条备忘比"客户端自报端口"（`..._ENDPOINT_FALLBACK=advertised`）
//     更接近真值；两者互不替代，各自独立开关。
//
// ## 安全边界（为什么不是"无条件放开"）
//
//   - 重放**只**发生在"该腿自己已经收到过、且通过了来源 IP 校验"的数据报上；
//   - 重放**只**写给"对向腿自己学到的真实端点"或"对向角色自己发过的备忘端点"，
//     目标 IP 永远等于该角色游戏 TCP 会话观察到的 IPv4（`binding.address`），
//     中继不可能被指向第三方；
//   - 队列有硬上限（`maxPendingDatagrams` 条 / `maxPendingBytes` 字节 / `pendingTTL` 存活），
//     备忘引导也有每腿写入上限（`maxMemoBootstraps` 条）与存活上限（`memoTTL`）；
//   - `off` 档与加本开关之前**逐字节相同**（丢弃 + 计数 + 首次日志），线上可一键回退。
//
// ## 判读串（部署后 grep）
//
//	party UDP relay probe queued           首包被留存（出现即证明"对向腿未学习"这个窗口被触发过）
//	party UDP relay probe replayed         留存的首包已补投（出现即证明死锁被打破）
//	party UDP relay endpoint memo bootstrap used  备忘端点被用作引导目标
//	party UDP relay probe replay expired   留存超过 TTL 仍未补投（= 对端整场一个包都没发）
const partyUDPRelayProbeReplayEnv = "DNFBRIDGE_PARTY_UDP_RELAY_PROBE_REPLAY"

const (
	partyUDPRelayProbeReplayValueOn  = "on"
	partyUDPRelayProbeReplayValueOff = "off"
)

const (
	// maxPendingDatagrams 是**每条腿**最多留存的数据报数。现场首包只有 1 条，
	// 8 条足够覆盖"对端迟到几秒"的窗口，同时把最坏情况下的内存钉死在 8x2048 B。
	maxPendingDatagrams = 8
	// maxPendingBytes 是**每条腿**待投队列的总字节上限（超出丢最旧的）。
	maxPendingBytes = 4096
	// pendingTTL 是待投队列的存活时间。超过即清空并记 `probe replay expired`：
	// "对端整场一个包都没发"这条判读就靠它。
	pendingTTL = 20 * time.Second
	// maxMemoBootstraps 是**每条腿**用备忘端点引导的最大写入条数（防注入器）。
	maxMemoBootstraps = 32
	// memoTTL 是角色端点备忘的存活时间。
	memoTTL = 30 * time.Minute
)

// partyUDPRelayProbeReplayMode 是 partyUDPRelayProbeReplayEnv 的归一化取值。
type partyUDPRelayProbeReplayMode int

const (
	// partyUDPRelayProbeReplayDisabled 是 options 零值：丢弃 + 计数 + 首次日志（加开关前行为）。
	partyUDPRelayProbeReplayDisabled partyUDPRelayProbeReplayMode = iota
	// partyUDPRelayProbeReplayEnabled 是**线上默认档**（env 空/未设置也是它）。
	partyUDPRelayProbeReplayEnabled
)

func (mode partyUDPRelayProbeReplayMode) String() string {
	if mode == partyUDPRelayProbeReplayEnabled {
		return partyUDPRelayProbeReplayValueOn
	}
	return partyUDPRelayProbeReplayValueOff
}

func (mode partyUDPRelayProbeReplayMode) enabled() bool {
	return mode == partyUDPRelayProbeReplayEnabled
}

// readPartyUDPRelayProbeReplayMode 解析 DNFBRIDGE_PARTY_UDP_RELAY_PROBE_REPLAY。
// **默认开启**（空/未设置/`on` = 修法生效），只有显式写 `off` 才回到旧行为。
// 注意：options 的零值仍是 Disabled，这样所有不经过 optionsFromConfig 的构造路径
// （既有单测与探针里的 Service/relay 字面量）与加本开关之前逐字节一致。
func readPartyUDPRelayProbeReplayMode(value string) partyUDPRelayProbeReplayMode {
	if strings.EqualFold(strings.TrimSpace(value), partyUDPRelayProbeReplayValueOff) {
		return partyUDPRelayProbeReplayDisabled
	}
	return partyUDPRelayProbeReplayEnabled
}

// partyUDPRelayProbeReplayMode 返回当前 Service 的归一化档位（零值 options = off = 旧行为）。
func (s *Service) partyUDPRelayProbeReplayMode() partyUDPRelayProbeReplayMode {
	if s == nil {
		return partyUDPRelayProbeReplayDisabled
	}
	return s.options.partyUDPRelayProbeReplay
}

// probeReplayModeLabel 是腿建立/腿汇总日志里的档位标签。
func (r *currentPartyUDPRelay) probeReplayModeLabel() string {
	if r != nil && r.probeReplay {
		return partyUDPRelayProbeReplayValueOn
	}
	return partyUDPRelayProbeReplayValueOff
}

// pendingProbeDatagrams 是这条腿**待投**给它自己 owner 所对的那个 peer 的数据报
// （来源是对向腿的 owner）。
type pendingProbeDatagrams struct {
	payloads [][]byte
	bytes    int
	since    time.Time
	dropped  uint64
	expired  uint64
	logged   bool
}

// queuePendingProbe 把一条数据报放进待投队列（超出上限时丢最旧的）。
// 调用方必须已经通过来源 IP 校验，并且已经确认本腿的目标端点未知。
func (leg *currentPartyUDPRelayLeg) queuePendingProbe(payload []byte) (int, int, bool) {
	if leg == nil || len(payload) == 0 {
		return 0, 0, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	now := time.Now()
	window := leg.pendingWindow
	if window <= 0 {
		window = pendingTTL
	}
	if len(leg.pending.payloads) > 0 && !leg.pending.since.IsZero() && now.Sub(leg.pending.since) > window {
		leg.pending.expired += uint64(len(leg.pending.payloads))
		leg.pending.payloads = nil
		leg.pending.bytes = 0
	}
	if len(leg.pending.payloads) == 0 {
		leg.pending.since = now
	}
	for len(leg.pending.payloads) >= maxPendingDatagrams ||
		(len(leg.pending.payloads) > 0 && leg.pending.bytes+len(payload) > maxPendingBytes) {
		leg.pending.bytes -= len(leg.pending.payloads[0])
		leg.pending.payloads = leg.pending.payloads[1:]
		leg.pending.dropped++
	}
	leg.pending.payloads = append(leg.pending.payloads, append([]byte(nil), payload...))
	leg.pending.bytes += len(payload)
	first := !leg.pending.logged
	leg.pending.logged = true
	return len(leg.pending.payloads), leg.pending.bytes, first
}

// takePendingProbes 取走待投队列（清空）。
func (leg *currentPartyUDPRelayLeg) takePendingProbes() [][]byte {
	if leg == nil {
		return nil
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if len(leg.pending.payloads) == 0 {
		return nil
	}
	out := leg.pending.payloads
	leg.pending.payloads = nil
	leg.pending.bytes = 0
	leg.pending.since = time.Time{}
	return out
}

// peekPendingProbes 是 takePendingProbes 的**只读**版本：看门狗在重投窗口内每一轮都要重投
// 同一批待投包，所以不能把它们取走。
func (leg *currentPartyUDPRelayLeg) peekPendingProbes() [][]byte {
	if leg == nil {
		return nil
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if len(leg.pending.payloads) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(leg.pending.payloads))
	for _, payload := range leg.pending.payloads {
		out = append(out, append([]byte(nil), payload...))
	}
	return out
}

// noteProbeResend 记一轮"沉默期间重投"（返回轮次，1 = 首次）。
func (leg *currentPartyUDPRelayLeg) noteProbeResend(resent int) uint64 {
	if leg == nil {
		return 0
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	leg.probeResends++
	return leg.probeResends
}

// probeResendRounds 是已执行的重投轮次（判读用）。
func (leg *currentPartyUDPRelayLeg) probeResendRounds() uint64 {
	if leg == nil {
		return 0
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	return leg.probeResends
}

// pendingWindowOrDefault 是这条腿的"周期重投总窗口"（零值 = 旧的一次性 pendingTTL）。
func (leg *currentPartyUDPRelayLeg) pendingWindowOrDefault() time.Duration {
	if leg == nil {
		return pendingTTL
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if leg.pendingWindow <= 0 {
		return pendingTTL
	}
	return leg.pendingWindow
}

// peekPendingProbeExpiry 返回"队列已超时"的条数（只在超时那一刻返回非零，随后清零）。
func (leg *currentPartyUDPRelayLeg) peekPendingProbeExpiry() (uint64, int, bool) {
	if leg == nil {
		return 0, 0, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if len(leg.pending.payloads) == 0 || leg.pending.since.IsZero() {
		return 0, 0, false
	}
	window := leg.pendingWindow
	if window <= 0 {
		window = pendingTTL
	}
	if time.Since(leg.pending.since) <= window {
		return 0, 0, false
	}
	count := len(leg.pending.payloads)
	leg.pending.expired += uint64(count)
	leg.pending.payloads = nil
	leg.pending.bytes = 0
	leg.pending.since = time.Time{}
	return leg.pending.expired, count, true
}

// notePendingProbeReplayed 记一次补投（返回累计条数/字节数与"是否首次"）。
func (leg *currentPartyUDPRelayLeg) notePendingProbeReplayed(size int) (uint64, uint64, bool) {
	if leg == nil {
		return 0, 0, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	leg.replayedDatagrams++
	leg.replayedBytes += uint64(size)
	first := !leg.loggedReplay
	leg.loggedReplay = true
	return leg.replayedDatagrams, leg.replayedBytes, first
}

// memoBootstrapTarget 返回该腿 owner 角色的"自己发过的最近真实端点"备忘目标。
// 三个前置条件缺一不可：备忘存在且未过期、备忘 IP 等于该角色游戏 TCP 会话观察到的
// IPv4（`binding.address`）、本条腿还没收到过自己的数据报（`learnedEndpoint == nil`）。
func (leg *currentPartyUDPRelayLeg) memoBootstrapTarget(memo *currentPartyUDPRelayEndpointMemo) (*net.UDPAddr, bool) {
	if leg == nil || memo == nil || !memo.valid {
		return nil, false
	}
	leg.mu.Lock()
	if leg.learnedEndpoint != nil || leg.conn == nil {
		leg.mu.Unlock()
		return nil, false
	}
	if leg.memoBootstraps >= maxMemoBootstraps {
		leg.mu.Unlock()
		return nil, false
	}
	if port, ok := memo.portForTTL(leg.binding.address, leg.memoTTL); ok {
		leg.memoBootstraps++
		leg.mu.Unlock()
		return &net.UDPAddr{
			IP:   net.IPv4(leg.binding.address[0], leg.binding.address[1], leg.binding.address[2], leg.binding.address[3]),
			Port: int(port),
		}, true
	}
	leg.mu.Unlock()
	return nil, false
}

// noteMemoBootstrap 记一次备忘引导写入（返回累计条数与"是否首次"）。
func (leg *currentPartyUDPRelayLeg) noteMemoBootstrap(size int, failed bool) (uint64, bool) {
	if leg == nil {
		return 0, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if failed {
		leg.memoBootstrapFailed++
		return leg.memoBootstrapFailed, false
	}
	leg.memoBootstrapDatagrams++
	leg.memoBootstrapBytes += uint64(size)
	first := !leg.loggedMemoBootstrap
	leg.loggedMemoBootstrap = true
	return leg.memoBootstrapDatagrams, first
}

// currentPartyUDPRelayEndpointMemo 是"某个角色自己发过的最近真实端点"。
// 只保留一个 (address, port)，address 必须与后来复用的 `binding.address` 相等才生效。
type currentPartyUDPRelayEndpointMemo struct {
	valid   bool
	address [4]byte
	port    uint16
	seenAt  time.Time
}

// portFor 在备忘未过期且 IP 与给定地址一致时返回备忘端口。
func (memo *currentPartyUDPRelayEndpointMemo) portFor(address [4]byte) (uint16, bool) {
	return memo.portForTTL(address, memoTTL)
}

// portForTTL 与 portFor 相同，但用调用方给的新鲜度上限（DNFBRIDGE_PARTY_UDP_RELAY_MEMO_TTL_SECONDS，
// 默认 30 秒）。现场依据：21:15 那次转发目标 21590 是 21:07 留下的旧 NAT 端口，加速器早已回收
// ⇒ 陈旧备忘**只能**作为多候选扇出里的一份，不能单独使用（见 party_udp_relay_probe_listener.go）。
func (memo *currentPartyUDPRelayEndpointMemo) portForTTL(address [4]byte, ttl time.Duration) (uint16, bool) {
	if memo == nil || !memo.valid || memo.port == 0 {
		return 0, false
	}
	if ttl <= 0 || ttl > memoTTL {
		ttl = memoTTL
	}
	if time.Since(memo.seenAt) > ttl {
		return 0, false
	}
	if memo.address != address {
		return 0, false
	}
	return memo.port, true
}

// rememberEndpoint 在角色自己发来数据报时更新备忘（只记**已通过来源校验**的端点）。
func (r *currentPartyUDPRelay) rememberEndpoint(leg *currentPartyUDPRelayLeg, source *net.UDPAddr) {
	if r == nil || !r.probeReplay || leg == nil || source == nil || source.Port <= 0 || source.Port > 65535 {
		return
	}
	memo := currentPartyUDPRelayEndpointMemo{
		valid:   true,
		address: leg.binding.address,
		port:    uint16(source.Port),
		seenAt:  time.Now(),
	}
	r.memoMu.Lock()
	if r.endpointMemo == nil {
		r.endpointMemo = make(map[uint16]currentPartyUDPRelayEndpointMemo)
	}
	r.endpointMemo[leg.key.ownerID] = memo
	r.memoMu.Unlock()
}

// lookupEndpointMemo 取某角色上一次自己发过的真实端点备忘。
func (r *currentPartyUDPRelay) lookupEndpointMemo(characterID uint16) *currentPartyUDPRelayEndpointMemo {
	if r == nil || characterID == 0 {
		return nil
	}
	r.memoMu.Lock()
	defer r.memoMu.Unlock()
	memo, ok := r.endpointMemo[characterID]
	if !ok {
		return nil
	}
	return &memo
}

// handleUnlearnedProbe 是 readLoop 在"对向腿端点未知"分支上的新动作（`off` 档下不被调用）。
//
//	leg      = 收到数据报的那条腿（owner = 发送者）
//	opposite = 对向腿（owner = 接收者）—— **补投必须用它自己的 socket 写**，
//	           这样接收端看到的来源地址才等于它在 op11 里被告知的中继端点。
//
// 返回 true 表示数据报已经通过"备忘引导"写出去了（调用方就不必再走自报端口回落）。
func (r *currentPartyUDPRelay) handleUnlearnedProbe(
	leg *currentPartyUDPRelayLeg,
	opposite *currentPartyUDPRelayLeg,
	source *net.UDPAddr,
	payload []byte,
) bool {
	if r == nil || !r.probeReplay || leg == nil || opposite == nil || len(payload) == 0 {
		return false
	}
	// (1) 先留一份：对端一旦发包就立刻补投。
	r.mu.Lock()
	if !r.pairCurrentLocked(leg, opposite) {
		r.mu.Unlock()
		return false
	}
	queued, bytes, first := opposite.queuePendingProbe(payload)
	r.mu.Unlock()
	if first {
		sourceText := ""
		if source != nil {
			sourceText = source.String()
		}
		r.log("party UDP relay probe queued",
			"party_id", opposite.key.partyID,
			"owner_id", opposite.key.ownerID,
			"peer_id", opposite.key.peerID,
			"leg_listen_port", opposite.port,
			"queued_datagrams", queued,
			"queued_bytes", bytes,
			"source", sourceText,
			"held_from_owner_id", leg.key.ownerID,
			"held_payload_bytes", len(payload),
			"meaning", "the_opposite_leg_has_not_sent_yet_first_datagram_is_held_for_replay_instead_of_dropped",
		)
	}
	// (2) 再试"同角色上次自己发过的真实端点"（比自报端口更接近真值）。
	memo := r.lookupEndpointMemo(opposite.key.ownerID)
	target, ok := opposite.memoBootstrapTarget(memo)
	if !ok || target == nil {
		return false
	}
	written, err := r.writeCurrentPairDatagram(leg, opposite, opposite.conn, payload, target)
	failed := err != nil || written != len(payload)
	datagrams, firstLog := opposite.noteMemoBootstrap(len(payload), failed)
	if failed {
		if firstLog {
			r.log("party UDP relay endpoint memo bootstrap failed",
				"party_id", opposite.key.partyID,
				"owner_id", opposite.key.ownerID,
				"peer_id", opposite.key.peerID,
				"leg_listen_port", opposite.port,
				"target", target.String(),
				"failed_datagrams", datagrams,
				"error", errText(err, written, len(payload)))
		}
		return false
	}
	if firstLog {
		r.log("party UDP relay endpoint memo bootstrap used",
			"party_id", opposite.key.partyID,
			"owner_id", opposite.key.ownerID,
			"peer_id", opposite.key.peerID,
			"leg_listen_port", opposite.port,
			"target", target.String(),
			"datagrams", datagrams,
			"bytes", len(payload),
			"meaning", "used_the_endpoint_this_member_itself_was_last_seen_sending_from_never_a_client_reported_ip",
		)
	}
	return true
}

// flushPendingProbes 在这条腿**第一次**学到自己的真实端点后，把待投队列原样补投给对端。
// 这条正是"打破单向死锁"的那一步：A 的首包在 B 发第一个包的瞬间被送到 B。
func (r *currentPartyUDPRelay) flushPendingProbes(leg *currentPartyUDPRelayLeg) {
	r.flushPendingProbesTo(leg, nil, "learned_endpoint")
}

// flushPendingProbesTo 是 flushPendingProbes 的通用形式：target 非空时投给该目标（= 种子端点，
// 由 relay 级定时清扫器驱动，见 sweepPendingProbes），否则投给这条腿自己学到的真实端点。
//
// 为什么需要 target 版本（第七次现场取证，2026-08-25 20:46 party_id=1）：旧实现的补投**只**发生在
// "这条腿自己收到了对端的数据报"那一刻，而对端整场一个包都没发时它永远不会发生 ⇒ 待投队列里的
// 首包既送不出去、也永远不会被记成过期（TTL 只在同一条腿再次收到数据报时才被检查）。
func (r *currentPartyUDPRelay) flushPendingProbesTo(leg *currentPartyUDPRelayLeg, target *net.UDPAddr, trigger string) {
	if r == nil || !r.probeReplay || leg == nil {
		return
	}
	leg.mu.Lock()
	destination := cloneCurrentPartyUDPRelayAddr(target)
	if destination == nil {
		destination = cloneCurrentPartyUDPRelayAddr(leg.learnedEndpoint)
	}
	seedWrite := target != nil
	exhausted := seedWrite && leg.seedForwardDatagrams >= maxSeedForwards
	closedLeg := leg.conn == nil
	leg.mu.Unlock()
	if destination == nil || exhausted || closedLeg {
		return
	}
	pending := leg.takePendingProbes()
	if len(pending) == 0 {
		return
	}
	var replayed, failed int
	var bytes uint64
	for _, payload := range pending {
		written, err := r.writeCurrentLegDatagram(leg, payload, destination)
		if err != nil || written != len(payload) {
			failed++
			continue
		}
		replayed++
		bytes += uint64(len(payload))
		datagrams, totalBytes, first := leg.notePendingProbeReplayed(len(payload))
		if seedWrite {
			leg.noteSeedForward(len(payload), false)
		}
		if first {
			r.log("party UDP relay probe replayed",
				"party_id", leg.key.partyID,
				"owner_id", leg.key.ownerID,
				"peer_id", leg.key.peerID,
				"leg_listen_port", leg.port,
				"target", destination.String(),
				"trigger", trigger,
				"replayed_datagrams", datagrams,
				"replayed_bytes", totalBytes,
				"pending_datagrams", len(pending),
				"meaning", "the_held_first_datagram_was_delivered_the_moment_the_peer_endpoint_became_known",
			)
		}
	}
	if failed > 0 {
		r.log("party UDP relay probe replay failed",
			"party_id", leg.key.partyID,
			"owner_id", leg.key.ownerID,
			"peer_id", leg.key.peerID,
			"leg_listen_port", leg.port,
			"target", destination.String(),
			"trigger", trigger,
			"replayed_datagrams", replayed,
			"replayed_bytes", bytes,
			"failed_datagrams", failed,
		)
	}
}

// pendingProbeSweeper 是 relay 级定时清扫器（`DNFBRIDGE_PARTY_UDP_RELAY_SEED_FORWARD=on` 时启动）。
// 它做两件旧实现做不到的事：
//  1. **把 TTL 到期记出来**：`party UDP relay probe replay expired` 现在是"对端整场一个数据报都
//     没发"的机器证据，而不是一个永远不会被求值的条件；
//  2. **用种子端点重试补投**：对端在组队前被观测到（原生探针/端点备忘）时，留存的第一个数据报会
//     被反复投给那个目标，直到对端回话（learnedEndpoint 出现后自动让位）或达到每腿写入上限。
func (r *currentPartyUDPRelay) pendingProbeSweeper(stop <-chan struct{}) {
	defer r.wg.Done()
	ticker := time.NewTicker(partyUDPRelayPendingSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.sweepPendingProbes()
		}
	}
}

// sweepPendingProbes 是清扫器的一轮：把"待投队列已过期"记出来。
//
// 注意这里**不再**做补投重试：活跃投递由 `fanOutUnlearnedDatagram`
// （party_udp_relay_probe_listener.go）在**每一个**入向数据报上完成，重复补投只会让对端
// 收到同一份载荷的多个副本。待投队列仍然保留其原义：对端**真实端点**被学到的那一刻
// （`flushPendingProbes`）立即补投。
func (r *currentPartyUDPRelay) sweepPendingProbes() {
	if r == nil || !r.probeReplay {
		return
	}
	r.mu.Lock()
	legs := make([]*currentPartyUDPRelayLeg, 0, len(r.legs))
	for _, leg := range r.legs {
		legs = append(legs, leg)
	}
	r.mu.Unlock()
	for _, leg := range legs {
		r.logPendingProbeExpiry(leg)
	}
}

// logPendingProbeExpiry 在待投队列的**周期重投窗口**走完时记一条：
// **出现它就等于"这一端整场一个数据报都没发"**，是"客户端根本不发"这条判读的机器证据。
// 窗口长度 = `DNFBRIDGE_PARTY_UDP_RELAY_RESEND_SECONDS`（默认 60s，窗口内每 3s 重投一轮）。
func (r *currentPartyUDPRelay) logPendingProbeExpiry(leg *currentPartyUDPRelayLeg) {
	if r == nil || !r.probeReplay || leg == nil {
		return
	}
	expired, count, ok := leg.peekPendingProbeExpiry()
	if !ok {
		return
	}
	r.log("party UDP relay probe replay expired",
		"party_id", leg.key.partyID,
		"owner_id", leg.key.ownerID,
		"peer_id", leg.key.peerID,
		"leg_listen_port", leg.port,
		"expired_datagrams", count,
		"total_expired_datagrams", expired,
		"ttl_seconds", strconv.Itoa(int(leg.pendingWindowOrDefault()/time.Second)),
		"resend_rounds", leg.probeResendRounds(),
		"meaning", "the_peer_of_this_leg_never_sent_a_single_datagram_within_the_resend_window",
	)
}
