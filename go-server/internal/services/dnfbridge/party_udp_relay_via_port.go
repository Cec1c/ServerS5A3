package dnfbridge

import (
	"net"
)

// 本文件实现 `DNFBRIDGE_PARTY_UDP_RELAY_VIA_PORT` 档：**把"定向腿口"换成"客户端唯一持续保活的那个共享端口"**。
//
// ## 现场依据（2026-09-26 凌晨，逐条可复现）
//
//  1. **客户端只认"被通告的那个端点"作源**：队长把 21B 握手发到 op11 通告的腿口（31000/31001），
//     而中继把队员的包投给队长时**源端口必须同为该腿口**，否则客户端丢弃。
//  2. **那个 NAT 只放行"客户端自己持续发包的目的端口"的入向**：本机 90 秒 `pktmon` 抓包显示
//     两个客户端**只在**持续向 `HK:2312` 发 1 字节保活（各 54 条），**没有任何**发往 31000/31001 的包；
//     同一窗口内从 2312 投出的包**全部到达**客户端 socket（32+32 条），而从 2311/腿口投出的
//     **一条都没到**。⇒ 腿口入向在队员那端是**关着**的，把包投到腿口等于丢。
//  3. **队员客户端从不主动发包**（本轮已用"客户端自己的邀请+接受"排除掉服务端因素后复测：
//     四条投递路径全试、`opposite_received=0`）。⇒ 不能指望"等队员先开口把 NAT 打开"。
//
// 结论：可行的拓扑只剩**"两端都被通告同一个端口，而且这个端口就是客户端一直在保活的那个"**。
// 现场那个端口是 `DNF_INITIAL_UDP_PORT2 = 2312`。
//
// ## 拓扑（viaPort 档）
//
//	op11 通告给 A 和 B 的"对端地址"都是 `advertise:viaPort`
//	A 把 P2P 载荷发到 viaPort ──┐
//	                            ├─→ 中继收到后**转发给除发送者以外的每一个已观测端点**（从 viaPort 发出）
//	B 把 P2P 载荷发到 viaPort ──┘
//
// **为什么不需要"归属"**：现场是 2 人队，且 `observed_probe_endpoint` 表里恰好只有这两个客户端的
// 端点（都因为保活探测进表）。"投给除发送者以外的所有已知端点"对 2 人队就是"投给对端"，
// 不需要判断"这个包是谁发的"。表里混进扫描器端点时也只是多发几份（受既有每腿上限与 TTL 约束）。
//
// ## 安全边界
//
//   - 开关**默认 0 = off**：off 档 `Endpoint()` 照旧返回腿口、本文件的转发函数在入口直接 return，
//     不新增任何出向数据报、逐字节保持现行为；
//   - 只转发**首字节为 `0x02`（厂商 P2P 首包族）**的载荷，1 字节原生探测仍只登记、不回包；
//   - 载荷**原样转发**（中继从不放大、从不新造字节）；绝不回投给发送者自己；
//   - 目标集合仍来自"我们内核亲自观测过的源端点"表（TTL 10 分钟 / 上限 8 条）。
const partyUDPRelayViaPortEnv = "DNFBRIDGE_PARTY_UDP_RELAY_VIA_PORT"

// partyUDPRelayViaPort 返回共享端口档的配置值（0 = off；零值 options 的测试/探针路径同样 = 0）。
func (s *Service) partyUDPRelayViaPort() int {
	if s == nil {
		return 0
	}
	return s.options.partyUDPRelayViaPort
}

// relayProbePortP2P 把"到达共享端口的 P2P 载荷"转发给**除发送者以外**的全部已观测端点，
// 并且**从这个共享端口发出**（源端口 = 被通告的对端端口）。
//
// 返回是否至少成功投出一份。
func (r *currentPartyUDPRelay) relayProbePortP2P(hitPort int, source *net.UDPAddr, payload []byte) bool {
	if r == nil || source == nil || len(payload) == 0 {
		return false
	}
	// off 档：一个字节都不发（本文件整体等于不存在）。
	if r.viaPort <= 0 || hitPort != r.viaPort || payload[0] != 0x02 {
		return false
	}
	entries := r.observedProbeEndpointEntries(source)
	if len(entries) == 0 {
		r.log("party UDP relay via port p2p undeliverable",
			"via_port", r.viaPort,
			"source", source.String(),
			"bytes", len(payload),
			"meaning", "no_other_observed_endpoint_yet_the_peer_has_never_probed_us_from_this_session")
		return false
	}
	socket, socketPort, fallback := r.viaPortDeliverySocket()
	if socket == nil {
		r.log("party UDP relay via port p2p undeliverable",
			"via_port", r.viaPort,
			"source", source.String(),
			"bytes", len(payload),
			"fallback_reason", fallback,
			"meaning", "the_shared_port_probe_socket_is_not_bound_so_the_source_port_cannot_match_the_advertised_peer_endpoint")
		return false
	}
	delivered := 0
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		target := currentPartyUDPRelayObservedTarget(entry)
		if target == nil {
			continue
		}
		// 同一个源 ip:port 可能因为"打到多个探针口"在表里有多条（每条带不同 hitPort）。
		// 共享端口档一律**从共享端口发出**，所以同一个目标只该投一份——否则对端会收到重复包。
		key := target.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		written, err := socket.WriteToUDP(payload, target)
		if err != nil || written != len(payload) {
			r.log("party UDP relay via port p2p forward failed",
				"via_port", r.viaPort,
				"target", target.String(),
				"bytes", len(payload),
				"error", errText(err, written, len(payload)))
			continue
		}
		delivered++
		r.log("party UDP relay via port p2p forwarded",
			"via_port", socketPort,
			"source", source.String(),
			"target", target.String(),
			"bytes", len(payload),
			"meaning", "the_shared_advertised_port_is_the_only_inbound_path_this_nat_keeps_open")
	}
	return delivered > 0
}

// viaPortDeliverySocket 返回"从共享端口发出"用的 socket：优先该端口的探针监听 socket；
// 探针监听不在位（被 releasePort 抢走/未启动）时退化为 nil 并由调用方记具名原因。
func (r *currentPartyUDPRelay) viaPortDeliverySocket() (*net.UDPConn, int, string) {
	if r == nil || r.viaPort <= 0 {
		return nil, 0, observedDeliveryFallbackListenerUnavailable
	}
	if conn := r.probeListenerConn(r.viaPort); conn != nil {
		return conn, r.viaPort, ""
	}
	return nil, 0, observedDeliveryFallbackListenerUnavailable
}
