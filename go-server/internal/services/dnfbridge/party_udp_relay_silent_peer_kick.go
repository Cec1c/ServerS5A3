package dnfbridge

import (
	"strconv"
	"strings"
	"time"
)

// 本文件实现"**让沉默的那一端开口**"+ "**沉默期间周期重投待投首包**"（现场取证后的第 3/4 条修法）。
//
// ## 为什么需要它
//
// 现场 21:07 / 21:15 / 23:31 三次实测合起来是同一个形状：**每次会话只有一侧在发包，
// 另一侧一个包都不发**（23:31 是 owner 3 发了唯一一个 21B 握手，owner 4 整场 0 包）。
// 中继要把包投给沉默那一端，必须知道它**真实的** NAT 端点；而加速器只在"客户端自己先发过包"
// 之后才建立映射 ⇒ 沉默端永远收不到 ⇒ 永远不回话 = 死锁。
//
// ## 厂商证据：op11（class0 / `msg_id=11` / 23B `PARTY_IP_INFO` 行）就是"开口"的扳机
//
//   - 厂商（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，只读）：op11 两行同在
//     `10:23:05.9845062` 发出；两位客户端的第一个 UDP 落在 `10:23:05.989`（owner 2）与
//     `10:23:06.011`（owner 1）⇒ **4 ms / 27 ms 后开始发包**；此后 60 秒内 **1,941 个 payload、
//     双向 945:996、32.4 包/秒**（8B ack 占 771 条）⇒ 握手成功后是**周期性 tick**。
//   - 我们这一侧（香港 20:46 那次）：腿建立/op11 在 `.362`，第一个数据报在 `.434` ⇒ 72 ms。
//
// 结论：客户端收到 op11 就会去发 P2P 探测包 —— 这是"让对端开口"唯一可用的服务端手段
// （UDP 源端口不可伪造，服务端无法代替对端发探测）。因此本文件在**已经死锁**的时刻，
// 给沉默的那个成员**重推一次名册**（复用既有运行时快照发送路径，其中包含 op11 行）。
//
// ## 23:31 那次的时序 bug（本版修的就是它）
//
// 唯一的数据报在腿建立 **65 ms** 后到达，而旧实现**只在收到数据报的那一刻内联评估一次**、
// 且要求腿龄 ≥ `silentPeerKickMinAge`(2s) ⇒ 条件不成立就 return，
// **之后再没有数据报 ⇒ 永远不会再评估** ⇒ `silent peer kick` 0 次。
//
// 现在改成 **per-leg 看门狗 goroutine**：
//
//	收到数据报且对向腿从未发包 ⇒ 起看门狗（每条腿最多一个，`kickRunning` 去重）
//	看门狗循环：先睡到腿龄 ≥ minAge（首次），之后按 interval：
//	  ① 对向腿已发包/已学端点、任一条腿不在队伍里、relay 已关闭、次数到上限、超出总窗口 ⇒ 退出
//	  ② 否则：催一次（phase=delayed 首次，之后 periodic）+ 把待投首包按全部候选**重投一次**
//
// ## 生命周期（不泄漏）
//
//   - 看门狗只监听 relay 级 `r.done`；`Close()` **先** close(r.done) **再** wg.Wait()，
//     所以 relay 关闭时看门狗一定被回收（另有 `silentPairStillLive` 处理"队伍解散但进程还在"）；
//   - 每条腿 `kickRunning` 保证只有一个看门狗；每个成员催的次数上限 `silentPeerKickMaxPerLeg`、
//     动作间隔 `silentPeerKickInterval`、总窗口 `DNFBRIDGE_PARTY_UDP_RELAY_RESEND_SECONDS`。
//
// ## 与 formation/进副本窗口的关系
//
// `silentPeerKickMinAge`（默认 2s）的原意被保留：formation 与"进副本"那几百毫秒内绝不打扰；
// 只有**已经死锁**（对向腿整场 0 包）时才动手，而此时重推名册正是唯一可能解开死锁的动作。
const (
	partyUDPRelaySilentPeerKickEnv        = "DNFBRIDGE_PARTY_UDP_RELAY_SILENT_PEER_KICK"
	partyUDPRelaySilentPeerKickDelayEnv   = "DNFBRIDGE_PARTY_UDP_RELAY_SILENT_PEER_KICK_SECONDS"
	partyUDPRelayProbeResendSecondsEnv    = "DNFBRIDGE_PARTY_UDP_RELAY_RESEND_SECONDS"
	partyUDPRelaySilentPeerKickPhaseDelay = "delayed"
	partyUDPRelaySilentPeerKickPhaseLoop  = "periodic"
)

const (
	// silentPeerKickMinAge 是"腿建立后多久才允许第一次催"：formation 与进副本窗口内不打扰。
	silentPeerKickMinAge = 2 * time.Second
	// silentPeerKickInterval 是同一对成员两次动作的最小间隔（催 + 重投共用这个节拍）。
	silentPeerKickInterval = 3 * time.Second
	// silentPeerKickMaxPerLeg 是每条腿最多催几次（足够覆盖一次实测，又不会变成刷包器）。
	silentPeerKickMaxPerLeg = 3
	// defaultProbeResendWindow 是"对端仍沉默期间持续重投待投首包"的总时长。
	defaultProbeResendWindow = 60 * time.Second
)

// partyUDPRelayKickTiming 是看门狗的节拍参数（env 归一化结果）。
type partyUDPRelayKickTiming struct {
	minAge   time.Duration
	interval time.Duration
	window   time.Duration
	maxKicks uint32
}

// readPartyUDPRelayKickTiming 解析两个时长 env：
//   - DNFBRIDGE_PARTY_UDP_RELAY_SILENT_PEER_KICK_SECONDS（默认 2 = 首次催的门槛）
//   - DNFBRIDGE_PARTY_UDP_RELAY_RESEND_SECONDS（默认 60 = 沉默期间重投总窗口）
//
// 非法/<=0 一律回默认；窗口上限 = memoTTL（30 分钟）。
func readPartyUDPRelayKickTiming(minAgeValue, windowValue string) partyUDPRelayKickTiming {
	timing := partyUDPRelayKickTiming{
		minAge:   silentPeerKickMinAge,
		interval: silentPeerKickInterval,
		window:   defaultProbeResendWindow,
		maxKicks: silentPeerKickMaxPerLeg,
	}
	timing.minAge = readPartyUDPRelaySeconds(minAgeValue, timing.minAge, 10*time.Minute)
	timing.window = readPartyUDPRelaySeconds(windowValue, timing.window, memoTTL)
	if timing.window < timing.interval {
		timing.window = timing.interval
	}
	return timing
}

// readPartyUDPRelaySeconds 解析"秒"型 env（空/非法/<=0 = 默认；上限钳制）。
func readPartyUDPRelaySeconds(value string, fallback, max time.Duration) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return fallback
	}
	parsed := time.Duration(seconds) * time.Second
	if parsed > max {
		return max
	}
	return parsed
}

// readPartyUDPRelaySilentPeerKickMode 解析开关：**默认 on**（死锁时重推名册是修法的一部分），
// 显式写 `off` 一键回退。
func readPartyUDPRelaySilentPeerKickMode(value string) bool {
	return !strings.EqualFold(strings.TrimSpace(value), partyUDPRelaySwitchOff)
}

// Service 侧访问器（零值 options = on，与其它"修法"开关一致：默认档即修法生效）。
func (s *Service) partyUDPRelaySilentPeerKickMode() bool {
	if s == nil {
		return false
	}
	return s.options.partyUDPRelaySilentPeerKick
}

// partyUDPRelayKickTiming 是 Service 侧归一化节拍（零值 options = 默认 2s/3s/60s/3 次）。
func (s *Service) partyUDPRelayKickTiming() partyUDPRelayKickTiming {
	timing := partyUDPRelayKickTiming{
		minAge:   silentPeerKickMinAge,
		interval: silentPeerKickInterval,
		window:   defaultProbeResendWindow,
		maxKicks: silentPeerKickMaxPerLeg,
	}
	if s == nil {
		return timing
	}
	if s.options.partyUDPRelayKickMinAge > 0 {
		timing.minAge = s.options.partyUDPRelayKickMinAge
	}
	if s.options.partyUDPRelayResendWindow > 0 {
		timing.window = s.options.partyUDPRelayResendWindow
	}
	if timing.window < timing.interval {
		timing.window = timing.interval
	}
	return timing
}

// watchSilentPeer 在 readLoop 的"对向腿端点未知"分支上被调用（非阻塞）：
// 能立刻动手就动手，条件不满足（例如 23:31 那次腿龄只有 65ms）就**起看门狗等到能动手为止**。
func (r *currentPartyUDPRelay) watchSilentPeer(leg, opposite *currentPartyUDPRelayLeg) {
	if r == nil || leg == nil || opposite == nil {
		return
	}
	if r.onSilentPeer == nil && !r.seedForward {
		return
	}
	opposite.mu.Lock()
	if opposite.kickRunning {
		opposite.mu.Unlock()
		return
	}
	r.mu.Lock()
	stop := r.done
	r.mu.Unlock()
	if stop == nil {
		opposite.mu.Unlock()
		return
	}
	opposite.kickRunning = true
	opposite.mu.Unlock()
	r.wg.Add(1)
	go r.silentPeerWatchdog(stop, leg, opposite)
}

// silentPeerWatchdog 是"催沉默端 + 重投待投首包"的看门狗。
func (r *currentPartyUDPRelay) silentPeerWatchdog(stop <-chan struct{}, leg, opposite *currentPartyUDPRelayLeg) {
	defer r.wg.Done()
	timing := r.kickTiming
	if timing.interval <= 0 {
		timing = readPartyUDPRelayKickTiming("", "")
	}
	deadline := time.Now().Add(timing.window)
	phase := partyUDPRelaySilentPeerKickPhaseDelay
	for {
		// ① 睡到"允许动手"的时刻：首次等腿龄到 minAge（23:31 那次就是死在这一点），之后按 interval。
		wait := timing.interval
		if phase == partyUDPRelaySilentPeerKickPhaseDelay {
			leg.mu.Lock()
			wait = timing.minAge - time.Since(leg.createdAt)
			leg.mu.Unlock()
			if wait < 0 {
				wait = 0
			}
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		select {
		case <-stop:
			return
		default:
		}
		if !r.silentPairStillLive(leg, opposite) {
			return
		}
		opposite.mu.Lock()
		kicks := opposite.kicks
		opposite.mu.Unlock()
		if kicks >= timing.maxKicks || time.Now().After(deadline) {
			return
		}
		// ② 催一次（在仍有预算时）。
		if r.onSilentPeer != nil {
			r.kickSilentPeer(leg, opposite, phase)
		}
		// ③ 同一节拍里把待投首包按**全部候选**重投一次：沉默端的 NAT 映射可能晚一点才建立，
		//    早投的那一份必丢（23:31 那次唯一一份就投在当时唯一的候选 advertised_port 上）。
		r.resendPendingProbes(leg, opposite)
		phase = partyUDPRelaySilentPeerKickPhaseLoop
	}
}

// silentPairStillLive 判断这两条腿是否还在同一个 relay 的腿表里，且对向腿仍然沉默。
func (r *currentPartyUDPRelay) silentPairStillLive(leg, opposite *currentPartyUDPRelayLeg) bool {
	if r == nil || leg == nil || opposite == nil {
		return false
	}
	r.mu.Lock()
	liveLeg := r.legs[leg.key] == leg
	liveOpposite := r.legs[opposite.key] == opposite
	closed := r.closed
	r.mu.Unlock()
	if closed || !liveLeg || !liveOpposite {
		return false
	}
	opposite.mu.Lock()
	defer opposite.mu.Unlock()
	return opposite.received == 0 && opposite.learnedEndpoint == nil
}

// kickSilentPeer 执行一次"催"：严格限量 + 判定性日志（phase/attempt/delivered/opposite_received）。
func (r *currentPartyUDPRelay) kickSilentPeer(leg, opposite *currentPartyUDPRelayLeg, phase string) {
	if r == nil || r.onSilentPeer == nil || leg == nil || opposite == nil {
		return
	}
	now := time.Now()
	opposite.mu.Lock()
	if opposite.received != 0 || opposite.learnedEndpoint != nil {
		opposite.mu.Unlock()
		return
	}
	kicks := opposite.kicks
	lastKick := opposite.lastKickAt
	if kicks >= silentPeerKickMaxPerLeg || (!lastKick.IsZero() && now.Sub(lastKick) < silentPeerKickInterval) {
		opposite.mu.Unlock()
		return
	}
	opposite.kicks++
	opposite.lastKickAt = now
	opposite.mu.Unlock()
	ok := r.onSilentPeer(leg.key.partyID, opposite.key.ownerID)
	r.log("party UDP relay silent peer kick",
		"party_id", opposite.key.partyID,
		"owner_id", opposite.key.ownerID,
		"peer_id", opposite.key.peerID,
		"leg_listen_port", opposite.port,
		"phase", phase,
		"attempt", kicks+1,
		"delivered", ok,
		"opposite_received", opposite.receivedDatagramCount(),
		"meaning", "the_member_that_never_sent_anything_got_another_party_roster_push_so_it_starts_its_own_p2p_probe")
}

// receivedDatagramCount 是这条腿收到过的数据报数（判读用，带锁读）。
func (leg *currentPartyUDPRelayLeg) receivedDatagramCount() uint64 {
	if leg == nil {
		return 0
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	return leg.received
}

// resendPendingProbes 把这条腿的待投首包**按全部候选**重投一遍（不消耗队列：窗口结束时才清空）。
// 返回 (重投的数据报数, 命中的候选串, 队列长度)。
func (r *currentPartyUDPRelay) resendPendingProbes(leg, opposite *currentPartyUDPRelayLeg) (int, string, int) {
	if r == nil || !r.seedForward || !r.probeReplay || leg == nil || opposite == nil {
		return 0, "", 0
	}
	payloads := opposite.peekPendingProbes()
	if len(payloads) == 0 {
		return 0, "", 0
	}
	resent := 0
	kindSet := make(map[string]struct{}, 4)
	for _, payload := range payloads {
		// 第三个返回值是"本次投递用的源端口"（观测端点档 = 探针口）：重投路径同样受它影响，
		// 因为 fanOutUnlearnedDatagram 内部按候选自带 via 选择发送源；这里只关心候选串。
		delivered, kinds, _ := r.fanOutUnlearnedDatagram(leg, opposite, payload, false)
		if !delivered {
			continue
		}
		resent++
		for _, kind := range strings.Split(kinds, "+") {
			if kind != "" {
				kindSet[kind] = struct{}{}
			}
		}
	}
	if resent == 0 {
		return 0, "", len(payloads)
	}
	kinds := make([]string, 0, len(kindSet))
	for kind := range kindSet {
		kinds = append(kinds, kind)
	}
	sortStrings(kinds)
	joined := strings.Join(kinds, "+")
	index := opposite.noteProbeResend(resent)
	if index == 1 {
		r.log("party UDP relay probe resend",
			"party_id", opposite.key.partyID,
			"owner_id", opposite.key.ownerID,
			"peer_id", opposite.key.peerID,
			"leg_listen_port", opposite.port,
			"phase", "resend",
			"resend_index", index,
			"pending_datagrams", len(payloads),
			"resent_datagrams", resent,
			"candidates", joined,
			"opposite_received", opposite.receivedDatagramCount(),
			"meaning", "the_peer_still_has_not_sent_anything_so_the_held_first_datagram_was_pushed_to_every_known_candidate_again")
	}
	return resent, joined, len(payloads)
}

// sortStrings 是插入排序（候选串很短，避免为几十个字符串再引 sort）。
func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

// kickSilentPartyMember 给**某一个成员单独**重推一次运行时队伍名册（其中就含 op11 `PARTY_IP_INFO`
// 行，即"开口"的扳机）。它复用 M2 重播所用的同一条发送原语，不新造协议字节。
//
// 前置条件（缺一即返回 false，行为与不开这个功能时一致）：队伍快照存在、该成员在线。
func (s *Service) kickSilentPartyMember(partyID, memberID uint16) bool {
	if s == nil || partyID == 0 || memberID == 0 {
		return false
	}
	manager := s.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByID(partyID)
	if !found {
		return false
	}
	identity, online := s.onlineGameSessionCharacterSnapshot(memberID)
	if !online || identity.session == nil {
		return false
	}
	if err := s.sendRuntimePartySnapshotWithoutPeerEndpointRepeat(identity.session, snapshot.StateFor(memberID)); err != nil {
		s.logGameEvent(identity.session, "party-udp-relay-silent-peer-kick-failed",
			"party_id", partyID,
			"member_id", memberID,
			"error", err)
		return false
	}
	s.logGameEvent(identity.session, "party-udp-relay-silent-peer-kick",
		"party_id", partyID,
		"member_id", memberID,
		"ordering", "op153_then_op11_then_op9_single_endpoint_frame",
		"source", "udp_relay_reverse_leg_has_never_sent_a_datagram")
	return true
}
