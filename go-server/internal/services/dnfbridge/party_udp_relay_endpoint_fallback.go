package dnfbridge

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// 本文件实现**单变量实验**：反向腿还没学到 endpoint 时，改用该成员**自己**在
// CS op2（`msg_id=2` / 31B SET_UDP_IP_PORT）里报过的 UDP 端口作为转发目标。
//
// 为什么需要它（现场因果链，2026-09-25 05:39 线上实测）：
//   - 中继是"每有序对一个 socket"的设计：owner 往 socket(owner,peer) 发，
//     负载从 socket(peer,owner) 发给 **peer 的已学真实端点**；
//   - "已学真实端点"只有一个来源：**该成员自己先往这条腿发过数据报**
//     （`readLoop` 里 `leg.learnedEndpoint = source`）；
//   - 实测：char4（队长 slot0）在腿建立 61ms 后就发了首包（`peer endpoint not learned
//     source=39.172.226.229:21418` 就是它的包），char3（队员 slot1）整场**一个数据报都没发**
//     （`received_datagrams=0`、所有 drop 计数为 0）⇒ 反向腿永远学不到 ⇒ 单向死锁。
//
// 因此中继手上关于"沉默成员"的唯一信息就是它**自报**的 endpoint：
//   - IP：**不取**客户端上报的 inner/outer（CS op2 里是内网 `192.168.0.101`，从公网不可达，
//     而且可被伪造 ⇒ 会让中继变成打向任意地址的反射器）。只取 `leg.binding.address`
//     —— 该成员**游戏 TCP 会话真实观察到**的 IPv4（`party_online.go` 的 sync 写入点）。
//   - 端口：只有这一个字段来自客户端自报（CS op2 的 `body[9:11]`，**小端** —— 2026-09-25
//     M0 取证后默认档，见 party_peer_endpoint.go 文件头；`DNFBRIDGE_PARTY_PEER_PORT_ENDIAN=be`
//     可一键回到历史的大端读数）。注意本文件里出现的"客户端自报 20484"是**旧的大端读数**，
//     小端读数下同一份现场字节是 1104 —— 本开关的注入目标端口随之改变，判读时要看
//     `game-party-peer-endpoint-registered` 的 `udp_port=`/`port_endian=` 两个字段。
//     家宽 NAT 不保端口（实测：客户端自报端口与它实际出公网的源端口 21418 不一致），
//     所以这只是**猜测**；它的价值在于"一次写入换来一次可判读的结果"。
//
// 默认行为**逐字节不变**：开关默认 `off`，关闭时 readLoop 仍然只做计数 + 首次日志 + 丢弃。
const partyUDPRelayEndpointFallbackEnv = "DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK"

const (
	partyUDPRelayEndpointFallbackValueOff        = "off"
	partyUDPRelayEndpointFallbackValueAdvertised = "advertised"
)

// currentPartyUDPRelayEndpointFallbackMaxDatagrams 是**每条腿**最多写出的回落数据报数。
// 上限存在的理由：这是"猜出来的目标端口"，猜错时不应该变成长期注入器；64 条足够覆盖
// 一次实测里客户端的前几秒流量，之后停手并记一条 `exhausted`，判读时一眼可见。
const currentPartyUDPRelayEndpointFallbackMaxDatagrams = 64

// partyUDPRelayEndpointFallbackMode 是 partyUDPRelayEndpointFallbackEnv 的归一化取值。
type partyUDPRelayEndpointFallbackMode int

const (
	// partyUDPRelayEndpointFallbackDisabled 是默认档：反向腿未学习时照旧丢弃（现行为）。
	partyUDPRelayEndpointFallbackDisabled partyUDPRelayEndpointFallbackMode = iota
	// partyUDPRelayEndpointFallbackAdvertised 打开回落：用对端自报端口 + 已认证 IPv4 再试一次。
	partyUDPRelayEndpointFallbackAdvertised
)

func (mode partyUDPRelayEndpointFallbackMode) String() string {
	if mode == partyUDPRelayEndpointFallbackAdvertised {
		return partyUDPRelayEndpointFallbackValueAdvertised
	}
	return partyUDPRelayEndpointFallbackValueOff
}

func (mode partyUDPRelayEndpointFallbackMode) enabled() bool {
	return mode == partyUDPRelayEndpointFallbackAdvertised
}

// readPartyUDPRelayEndpointFallbackMode 解析 DNFBRIDGE_PARTY_UDP_RELAY_ENDPOINT_FALLBACK。
// **默认关闭**：空/未设置/`off` 都是现行为（丢弃 + 计数 + 日志），只有显式写
// `advertised` 才打开这一次注入实验，线上可一键回退。
func readPartyUDPRelayEndpointFallbackMode(value string) partyUDPRelayEndpointFallbackMode {
	if strings.EqualFold(strings.TrimSpace(value), partyUDPRelayEndpointFallbackValueAdvertised) {
		return partyUDPRelayEndpointFallbackAdvertised
	}
	return partyUDPRelayEndpointFallbackDisabled
}

// partyUDPRelayEndpointFallbackMode 返回当前 Service 的归一化档位（零值 options 的
// 测试/探针路径 = off = 现行为）。
func (s *Service) partyUDPRelayEndpointFallbackMode() partyUDPRelayEndpointFallbackMode {
	if s == nil {
		return partyUDPRelayEndpointFallbackDisabled
	}
	return s.options.partyUDPRelayEndpointFallback
}

// endpointFallbackTarget 返回这条腿的"自报端点"猜测目标。
//
// 前置条件（三个都必须成立，任何一个不成立就返回 ok=false，行为与关闭开关时一致）：
//  1. `leg.binding.advertisedPort != 0` —— 该成员在 CS op2 里报过一个非零 UDP 端口；
//     （`captureCurrentPartyPeerEndpointRegistration` 已经保证 0 端口不覆盖既有值。）
//  2. `leg.learnedEndpoint == nil` —— 这条腿**至今**没收到过它的数据报。一旦收到，
//     真实端点即为权威，回落必须立刻让位（本函数不再被调用）。
//  3. 该腿的回落写入次数还没到上限。
//
// 目标地址 = (`leg.binding.address`, `leg.binding.advertisedPort`)。
func (leg *currentPartyUDPRelayLeg) endpointFallbackTarget() (*net.UDPAddr, bool, bool) {
	if leg == nil {
		return nil, false, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if leg.binding.advertisedPort == 0 || leg.learnedEndpoint != nil || leg.conn == nil {
		return nil, false, false
	}
	if leg.endpointFallbackWrites >= currentPartyUDPRelayEndpointFallbackMaxDatagrams {
		exhausted := !leg.loggedEndpointFallbackExhausted
		leg.loggedEndpointFallbackExhausted = true
		return nil, false, exhausted
	}
	leg.endpointFallbackWrites++
	return &net.UDPAddr{
		IP:   net.IPv4(leg.binding.address[0], leg.binding.address[1], leg.binding.address[2], leg.binding.address[3]),
		Port: int(leg.binding.advertisedPort),
	}, true, false
}

// noteEndpointFallbackWrite 记一次回落写入的结果（成功/失败各一个计数）。
func (leg *currentPartyUDPRelayLeg) noteEndpointFallbackWrite(size int, failed bool) (uint64, uint64, bool) {
	if leg == nil {
		return 0, 0, false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if failed {
		leg.endpointFallbackFailed++
		return leg.endpointFallbackDatagrams, leg.endpointFallbackFailed, false
	}
	leg.endpointFallbackDatagrams++
	leg.endpointFallbackBytes += uint64(size)
	first := !leg.loggedEndpointFallbackWrite
	leg.loggedEndpointFallbackWrite = true
	return leg.endpointFallbackDatagrams, leg.endpointFallbackFailed, first
}

// noteEndpointFallbackFailureLogged 保证"回落写入失败"每种腿只记一条日志。
func (leg *currentPartyUDPRelayLeg) noteEndpointFallbackFailureLogged() bool {
	if leg == nil {
		return false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if leg.loggedEndpointFallbackFailure {
		return false
	}
	leg.loggedEndpointFallbackFailure = true
	return true
}

// noteLearnedEndpointAfterFallback 在"回落写过之后，这条腿终于收到对端自己的数据报"时
// 返回 true（只返回一次）。这正是本次实验要拿的**判定性证据**：沉默的一端真的被唤醒并
// 回了包 ⇒ 客户端是"收到对端的包才回话"的响应式模型，中继只需要先递一包进去。
func (leg *currentPartyUDPRelayLeg) noteLearnedEndpointAfterFallback() bool {
	if leg == nil {
		return false
	}
	leg.mu.Lock()
	defer leg.mu.Unlock()
	if leg.endpointFallbackDatagrams == 0 || leg.loggedLearnedAfterFallback {
		return false
	}
	leg.loggedLearnedAfterFallback = true
	return true
}

// forwardToAdvertisedEndpoint 是 readLoop 在"反向腿未学习"分支上唯一的实验性动作。
//
// 语义边界（必须逐条成立，否则函数立刻返回、行为与开关关闭时完全一致）：
//   - `r.endpointFallback` 打开（默认关闭）；
//   - 目标腿（= 对端**自己**发过才会用到的那条腿）存在、其 socket 还在、
//     其 `learnedEndpoint` 仍为 nil、且它自报过非零端口（见 endpointFallbackTarget）。
//
// 与既有语义的兼容性：
//   - 源校验、`Sync` 事务性、端口分配、`Endpoint()` 公布的端口**全部不动**；
//   - 真实的 learnedEndpoint 一旦出现，本路径自动失效（`endpointFallbackTarget`
//     的第二个前置条件），不会与权威目标争抢；
//   - 上限 64 条/腿（`currentPartyUDPRelayEndpointFallbackMaxDatagrams`）。
//
// 日志三串（判读用）：
//   - `party UDP relay endpoint fallback used`：第一次成功写出，带 target/条数；
//   - `party UDP relay endpoint fallback failed`：第一次 WriteToUDP 出错；
//   - `party UDP relay endpoint fallback exhausted`：该腿达到上限、不再写。
//
// 注意：`WriteToUDP` 成功**只证明本机把包交给了内核**，不证明对端收到（家宽 NAT 不保
// 端口，这个端口只是猜测）。唯一能证明送到的证据是随后出现
// `party UDP relay endpoint fallback learned real endpoint`（对端自己回包了）。
func (r *currentPartyUDPRelay) forwardToAdvertisedEndpoint(
	source *currentPartyUDPRelayLeg,
	opposite *currentPartyUDPRelayLeg,
	payload []byte,
) {
	if r == nil || !r.endpointFallback || source == nil || opposite == nil || len(payload) == 0 {
		return
	}
	target, ok, exhausted := opposite.endpointFallbackTarget()
	if exhausted {
		r.log("party UDP relay endpoint fallback exhausted",
			"party_id", opposite.key.partyID,
			"owner_id", opposite.key.ownerID,
			"peer_id", opposite.key.peerID,
			"leg_listen_port", opposite.port,
			"target", formatCurrentPartyUDPRelayIPv4(opposite.binding.address)+":"+strconv.Itoa(int(opposite.binding.advertisedPort)),
			"datagrams", currentPartyUDPRelayEndpointFallbackMaxDatagrams,
			"meaning", "the_advertised_endpoint_guess_is_not_reachable_stop_injecting")
		return
	}
	if !ok || target == nil {
		return
	}
	written, err := opposite.conn.WriteToUDP(payload, target)
	if err != nil || written != len(payload) {
		datagrams, failed, _ := opposite.noteEndpointFallbackWrite(0, true)
		if opposite.noteEndpointFallbackFailureLogged() {
			r.log("party UDP relay endpoint fallback failed",
				"party_id", opposite.key.partyID,
				"owner_id", opposite.key.ownerID,
				"peer_id", opposite.key.peerID,
				"leg_listen_port", opposite.port,
				"target", target.String(),
				"datagrams", datagrams,
				"failed_datagrams", failed,
				"error", errText(err, written, len(payload)))
		}
		return
	}
	datagrams, _, first := opposite.noteEndpointFallbackWrite(len(payload), false)
	if first {
		r.log("party UDP relay endpoint fallback used",
			"party_id", opposite.key.partyID,
			"owner_id", opposite.key.ownerID,
			"peer_id", opposite.key.peerID,
			"leg_listen_port", opposite.port,
			"target", target.String(),
			"datagrams", datagrams,
			"bytes", len(payload),
			"meaning", "reverse_leg_endpoint_unknown_used_the_members_own_cs_op2_reported_udp_port")
	}
}

// errText 给"WriteToUDP 返回 nil 但写的字节数不对"补一条可判读的错误串。
func errText(err error, written, want int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("short write: %d of %d", written, want)
}
