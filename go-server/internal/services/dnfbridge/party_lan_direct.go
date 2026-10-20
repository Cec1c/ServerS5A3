package dnfbridge

import (
	"net"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 本文件实现 op11（class0 / `msg_id=11` PARTY_IP_INFO）**对端行**的第三种来源：
// **同局域网直连**（LAN direct）。
//
// # 一、要解决的问题（2026-09-25 现场证据）
//
// 现场两台客户端跑在**同一台机器/同一个局域网**里（`game-party-peer-endpoint-registered`，
// `source=current_exe_cs_op2_dynamic_udp_endpoint`）：
//
//	char_id=0 inner_ipv4=192.168.0.101 outer_ipv4=192.168.0.101 udp_port=61694
//	char_id=0 inner_ipv4=192.168.0.101 outer_ipv4=192.168.0.101 udp_port=61566
//	char_id=1 inner_ipv4=192.168.0.101 outer_ipv4=192.168.0.101 udp_port=61566
//
// 两台客户端自报的 inner 完全相同、端口不同，而服务端看到的 outer（游戏 TCP 会话远端地址）
// 也是同一个公网地址。此时 op11 通告的是**中继端点**（港机 `38.22.90.52:<port>`），
// 队长的 21 字节握手被中继转发到队员的**公网**端点 ⇒ 该数据报必须穿过 **NAT hairpinning**，
// 而大量家用路由器直接丢弃这种包 —— 与实测"队长一发就发、队员一个数据报都不回"完全吻合。
//
// 同局域网的两台客户端**根本不需要中继**：把对端的 **inner（私网）地址**直接通告给彼此，
// 两端就能在局域网内直连。
//
// # 二、开关
//
//	DNFBRIDGE_PARTY_LAN_DIRECT   relay（默认，空/未设置/非法值都是它）| lan
//
// `relay` = **逐字节回到改动前**（op11 对端行仍然写中继端点，即 `relay.Endpoint(...)`
// 的地址+端口）；`lan` = 满足下面全部安全条件时改写为对端自报的 inner 地址 + 自报端口。
// 线上回退：把 env 删掉或写成 `relay`。
//
// # 三、安全条件（必须**全部**成立才走直连；任一条不成立 = 原样回退中继）
//
//  1. `lan` 档已开启；
//  2. 收包者与对端的会话都是**当前**已绑定角色的会话（生成号非零、不能是同一个会话）——
//     不放松任何既有校验（频道隔离/生成号/队伍归属仍由上游与 `syncRuntimePartyUDPRelay` 把关）；
//  3. **对端**已经上报过 CS op2 且端口非 0（**绝不**为"没上报过的成员"编造端点）；
//  4. 两端的 `inner_ipv4` **相等且可用**（非全零、非 127/8 回环、非组播/保留段）；
//  5. 服务端**实际观测到的**两端 outer 地址（游戏 TCP 会话远端 IPv4）**相等**——
//     这是**关键的假阳性护栏**：两个互不相干的玩家（不同家庭）都常报 `192.168.1.2`，
//     但服务端看到他们的公网地址不同，绝不能配成一对。
//
// 关键点是第 5 条：只看"inner 相同"会把两家不同 NAT 后面的玩家错配成局域网直连，
// 那是比中继更坏的结果（双方永远连不上，且没有任何回退）。
//
// # 四、边界（本档**不**改的东西）
//
//   - 行布局 / 字节序**一个字节都不改**：仍是
//     `u16 uid(LE) | 4B inner | 4B outer | u16 port(BE) | u32 acc(LE) | u8 nat | u32 mtu(LE) | u8 charAttr`，
//     端口仍按网络序写、仍取"客户端自报端口"（与 `buildRuntimePartyPeerEndpointInfoForMembers`
//     的直连形态同一个语义），字节序仍由 `DNFBRIDGE_PARTY_PEER_PORT_ENDIAN` 决定（默认 be）；
//   - inner/outer **两个字段都写同一个 inner 地址**：客户端无法把"可达地址"选错
//     （客户端自报的 outer 在 NAT 下是公网地址，写进去会让它去穿 NAT）；
//   - 只有"**通告**给客户端的那一个地址/端口对"改变。中继腿**照旧建立**（本档不改
//     `syncRuntimePartyUDPRelay` / `relay.Sync` / 房间生命周期），所以同队里非同局域网的
//     成员仍然拿到中继端点；中继不可用时也照旧整份回退（见
//     `buildRuntimePartyPeerEndpointInfoForReceiver` 的早退分支），本档在该分支**不生效**；
//   - 中继房仍然为该队保留：客户端若因为任何原因改回用通告里的中继地址也仍然可用，
//     本档不做任何"拆腿"动作。
const partyLANDirectEnv = "DNFBRIDGE_PARTY_LAN_DIRECT"

const (
	partyLANDirectValueRelay = "relay"
	partyLANDirectValueLAN   = "lan"
)

// partyLANDirectMode 是 partyLANDirectEnv 的归一化取值。
type partyLANDirectMode int

const (
	// partyLANDirectRelay 是默认档（**零值**）：op11 对端行写中继端点 = 改动前行为。
	partyLANDirectRelay partyLANDirectMode = iota
	// partyLANDirectLAN 在安全条件全部成立时改写为同局域网直连端点。
	partyLANDirectLAN
)

func (mode partyLANDirectMode) String() string {
	if mode == partyLANDirectLAN {
		return partyLANDirectValueLAN
	}
	return partyLANDirectValueRelay
}

// enabled 表示是否允许把对端行改写成同局域网直连端点。
func (mode partyLANDirectMode) enabled() bool {
	return mode == partyLANDirectLAN
}

// readPartyLANDirectMode 解析 DNFBRIDGE_PARTY_LAN_DIRECT。
// **默认 `relay`（= 改动前行为）**：空/未设置/非法值都回到 `relay`；只有显式写 `lan`
// 才启用同局域网直连（一键回退 = 删掉 env 或写 `relay`）。
func readPartyLANDirectMode(value string) partyLANDirectMode {
	if strings.EqualFold(strings.TrimSpace(value), partyLANDirectValueLAN) {
		return partyLANDirectLAN
	}
	return partyLANDirectRelay
}

// partyLANDirect 返回当前 Service 的归一化档位（零值 options 的测试/探针路径
// = relay = 改动前行为）。
func (s *Service) partyLANDirect() partyLANDirectMode {
	if s == nil {
		return partyLANDirectRelay
	}
	return s.options.partyLANDirect
}

// partyLANUsableIPv4 判断一个客户端自报的 inner 地址是否"可用作局域网直连地址"：
// 非全零、非 127/8 回环、非组播(224/4)与保留/广播段(240/4)。
//
// 169.254/16（APIPA）**算可用**：它确实是链路本地单播地址，两侧同值时仍可直连；
// 真正的假阳性护栏是下面那条"服务端观测到的 outer 必须相等"。
func partyLANUsableIPv4(address [4]byte) bool {
	if ipv4IsZero(address) {
		return false
	}
	if address[0] == 127 {
		return false
	}
	return address[0] < 224
}

// partySessionObservedIPv4 取"服务端**实际观测到**的该会话 IPv4"，即游戏 TCP 会话的远端地址。
// 与 `syncRuntimePartyUDPRelay` 用的是同一个来源（所以本档与中继腿的地址判据同源）。
// 返回 ok=false 表示这个会话没有可观测的 IPv4（测试用的假 conn、非 TCP 地址等）。
func partySessionObservedIPv4(session *gameSession) ([4]byte, bool) {
	if session == nil || session.conn == nil {
		return [4]byte{}, false
	}
	tcpAddress, ok := session.conn.RemoteAddr().(*net.TCPAddr)
	if !ok || tcpAddress.IP == nil {
		return [4]byte{}, false
	}
	ipv4 := tcpAddress.IP.To4()
	if len(ipv4) != net.IPv4len {
		return [4]byte{}, false
	}
	var address [4]byte
	copy(address[:], ipv4)
	return address, true
}

// partyLANDirectPeerEndpoint 决定某一对"收包者 → 对端"是否走同局域网直连，并给出该用的
// **inner 地址 + 对端自报端口**。第二个返回值 ok=false = 必须原样回退中继端点。
//
// 全部安全条件见文件头"三"；这里逐条对应，任一条不成立立即 false，不做任何"半份"决定。
func (s *Service) partyLANDirectPeerEndpoint(
	receiver *gameSession,
	peerUserID uint16,
) (address [4]byte, port uint16, observed [4]byte, ok bool) {
	if s == nil || !s.partyLANDirect().enabled() || peerUserID == 0 {
		return [4]byte{}, 0, [4]byte{}, false
	}
	// 收包者必须是**当前**绑定角色的会话（生成号非零），与中继同步用的是同一份快照。
	receiverIdentity, bound := s.boundGameSessionCharacterSnapshot(receiver)
	if !bound || receiverIdentity.generation == 0 {
		return [4]byte{}, 0, [4]byte{}, false
	}
	peerIdentity, online := s.onlineGameSessionCharacterSnapshot(peerUserID)
	if !online || peerIdentity.generation == 0 || peerIdentity.session == receiver {
		return [4]byte{}, 0, [4]byte{}, false
	}
	// 对端必须**自己上报过**端点：端口 0 = 从没上报过（含全零重连探针）⇒ 绝不编造端点。
	peerRegistration := currentPartyPeerEndpointSnapshot(peerIdentity.session)
	if peerRegistration.Port == 0 {
		return [4]byte{}, 0, [4]byte{}, false
	}
	inner := peerRegistration.InnerIPv4
	if !partyLANUsableIPv4(inner) {
		return [4]byte{}, 0, [4]byte{}, false
	}
	// 两端的自报 inner 必须**相等**（"同局域网"的第一半证据）。
	if receiverRegistration := currentPartyPeerEndpointSnapshot(receiver); inner != receiverRegistration.InnerIPv4 {
		return [4]byte{}, 0, [4]byte{}, false
	}
	// 假阳性护栏：服务端**实际观测到**的两端 outer 地址也必须相等。
	receiverObserved, hasReceiver := partySessionObservedIPv4(receiver)
	if !hasReceiver {
		return [4]byte{}, 0, [4]byte{}, false
	}
	peerObserved, hasPeer := partySessionObservedIPv4(peerIdentity.session)
	if !hasPeer || peerObserved != receiverObserved {
		return [4]byte{}, 0, [4]byte{}, false
	}
	return inner, peerRegistration.Port, receiverObserved, true
}

// logPartyLANDirectEndpoint 为每一条被改写成同局域网直连的对端行留一条可判读事件
// （一次 op11 每个对端最多一条；不满足条件时不打，避免每次组队刷屏）。
func (s *Service) logPartyLANDirectEndpoint(
	receiver *gameSession,
	state alignedcmd.PartyState,
	peerUserID uint16,
	inner [4]byte,
	port uint16,
	observed [4]byte,
) {
	s.logGameEvent(receiver, "game-party-peer-endpoint-lan-direct",
		"char_id", receiver.selectedCharacterID,
		"peer_char_id", peerUserID,
		"party_id", state.PartyID,
		"inner_ipv4", formatCurrentPartyUDPRelayIPv4(inner),
		"inner_and_outer_written", formatCurrentPartyUDPRelayIPv4(inner),
		"observed_outer_ipv4", formatCurrentPartyUDPRelayIPv4(observed),
		"udp_port", port,
		"port_endian", s.partyPeerPortEndian().String(),
		"env", partyLANDirectEnv,
		"mode", s.partyLANDirect().String(),
		"source", "same_lan_inner_ipv4_and_equal_observed_outer_address")
}
