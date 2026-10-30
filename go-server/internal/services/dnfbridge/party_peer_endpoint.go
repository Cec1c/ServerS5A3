package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

const currentPartyPeerRegistrationBodySize = 31

const (
	currentPartyPeerMinimumMTU = 576
	currentPartyPeerMaximumMTU = 9000
)

// CS op2 `body[9:11]` 端口字节序 —— **大端（`be`）是默认档**。
//
// 结论变更（2026-09-25 本轮，推翻上一轮的 M0 小端结论）：**端口是大端**，
// 而同一个结构里的 MTU（`body[11:15]`）仍是小端 —— 这是一个**混合字节序**的定长结构。
//
// 决定性证据（厂商 9.16 一键端 `D:\DNF90-test\server\runtime\logs\packet_log.txt`，
// 同一场组队、同一秒内的原始 CS op2 与厂商**自己的中继源端口记录**配对）：
//
//  1. conn `game-000059`（char_id=1）：`:2524` CS op2 原始 body 里端口字节 `13 C7`
//     （完整 hex `…c0a80065c0a8006513c7c00500000c00000030306666…`），
//     同一会话的中继记录 `:3368` `kind=party-udp-relay-payload … source=192.168.0.101:5063`
//     ⇒ `BigEndian(13 C7)` = **5063** ✓；`LittleEndian` = 50963 ✗。
//  2. conn `game-000061`（char_id=2）：`:2908` 端口字节 `F4 65`，
//     中继记录 `:3357` `… source=192.168.0.101:62565`
//     ⇒ `BigEndian(F4 65)` = **62565** ✓；`LittleEndian` = 26100 ✗。
//  3. 同一份日志 `:2524`/`:2908` 的 MTU 字节仍是 `C0 05 00 00`（`LittleEndian` = 1472，
//     `BigEndian` = 3221684224 非法）⇒ 端口与 MTU **在同一个结构里确实混用字节序**，
//     上一轮"同结构不会混用字节序"的推断（E2）因此被推翻。
//
// 上一轮的三条推断为什么有误差（保留以便判读历史结论）：
//   - E1（本机成对样本 `F8 CB` ↔ 参考实现解析出的 52216）只证明"那份本机参考实现按小端读"，
//     不证明"客户端的字节就是小端"；
//   - E2（结构内 MTU 只能是 LE）对 MTU 成立，但不能外推到端口（本轮证据 3）；
//   - E3（A21/86JP 的 `SetUdpEndpointRequest` 读小端）是**别家实现的选择**，
//     本轮是同会话同秒的**实测配对**，优先级更高。
//
// 一键回退：`DNFBRIDGE_PARTY_PEER_PORT_ENDIAN=le` 恢复上一轮的小端读数
// （读法变了会连带改 op11 里"自己那一行"的端口字节与开关 A 的注入端口）。
const partyPeerPortEndianEnv = "DNFBRIDGE_PARTY_PEER_PORT_ENDIAN"

const (
	partyPeerPortEndianValueLittle = "le"
	partyPeerPortEndianValueBig    = "be"
)

// partyPeerPortEndianMode 是 partyPeerPortEndianEnv 的归一化取值。
type partyPeerPortEndianMode int

const (
	// partyPeerPortEndianBig 是默认档（零值）：与厂商 9.16 实测配对一致（见文件头）。
	partyPeerPortEndianBig partyPeerPortEndianMode = iota
	// partyPeerPortEndianLittle 是 2026-09-25 上一轮 M0 的读数（"与 A21/86JP 的
	// `SetUdpEndpointRequest` 一致"），保留它只为逐字节回退与 A/B 判读。
	partyPeerPortEndianLittle
)

func (mode partyPeerPortEndianMode) String() string {
	if mode == partyPeerPortEndianLittle {
		return partyPeerPortEndianValueLittle
	}
	return partyPeerPortEndianValueBig
}

func (mode partyPeerPortEndianMode) bigEndian() bool {
	return mode != partyPeerPortEndianLittle
}

// decode 按档位解 `body[9:11]` 的两个原始字节。
func (mode partyPeerPortEndianMode) decode(raw [2]byte) uint16 {
	if mode.bigEndian() {
		return binary.BigEndian.Uint16(raw[:])
	}
	return binary.LittleEndian.Uint16(raw[:])
}

// readPartyPeerPortEndianMode 解析 DNFBRIDGE_PARTY_PEER_PORT_ENDIAN。
// **默认 `be`（= 厂商实测配对）**：空/未设置/非法值都回到 `be`；只有显式写 `le`
// 才回到上一轮的小端读数（一键回退）。
func readPartyPeerPortEndianMode(value string) partyPeerPortEndianMode {
	if strings.EqualFold(strings.TrimSpace(value), partyPeerPortEndianValueLittle) {
		return partyPeerPortEndianLittle
	}
	return partyPeerPortEndianBig
}

// partyPeerPortEndian 返回当前 Service 的归一化档位（零值 options 的测试/探针路径
// = be = 默认档）。
func (s *Service) partyPeerPortEndian() partyPeerPortEndianMode {
	if s == nil {
		return partyPeerPortEndianBig
	}
	return s.options.partyPeerPortEndian
}

// currentPartyPeerEndpointRegistration is the current client's CS op2
// SET_UDP_IP_PORT report. The first byte is the client character attribute,
// followed by inner/outer IPv4, a UDP port, little-endian MTU, and a
// length-prefixed machine token. Only the endpoint fields are retained;
// the token is deliberately ignored.
//
// Port 按 `partyPeerPortEndianEnv` 解（**默认大端**，见文件头实测配对）；
// PortRaw 保留原始两字节，只用于日志里同时打出两种读数，方便下一次实测判读。
type currentPartyPeerEndpointRegistration struct {
	CharacterAttribute byte
	InnerIPv4          [4]byte
	OuterIPv4          [4]byte
	Port               uint16
	PortRaw            [2]byte
	MTU                uint32
	// PeerRowAcc / PeerRowAccStamped 是 op11 对端行 `acc` 字段的**每会话盖章**
	// （见 party_peer_row_vendor_fields.go）。两个字段一起才有意义：
	// `PeerRowAccStamped` 区分"acc = 0"与"还没盖过章"。盖章时机 = 本会话第一次被
	// party 代码看见（正常就是这条 CS op2 上报），之后终身不变。
	PeerRowAcc        uint32
	PeerRowAccStamped bool
}

func parseCurrentPartyPeerEndpointRegistration(
	body []byte,
	endian partyPeerPortEndianMode,
) (currentPartyPeerEndpointRegistration, error) {
	if len(body) != currentPartyPeerRegistrationBodySize {
		return currentPartyPeerEndpointRegistration{}, fmt.Errorf(
			"current party peer registration body length %d, want %d",
			len(body),
			currentPartyPeerRegistrationBodySize,
		)
	}
	registration := currentPartyPeerEndpointRegistration{
		CharacterAttribute: body[0],
		MTU:                binary.LittleEndian.Uint32(body[11:15]),
	}
	copy(registration.PortRaw[:], body[9:11])
	registration.Port = endian.decode(registration.PortRaw)
	copy(registration.InnerIPv4[:], body[1:5])
	copy(registration.OuterIPv4[:], body[5:9])
	tokenLength := int(binary.LittleEndian.Uint32(body[15:19]))
	if tokenLength < 0 || tokenLength > len(body)-19 {
		return currentPartyPeerEndpointRegistration{}, fmt.Errorf(
			"current party peer registration token length %d exceeds %d",
			tokenLength,
			len(body)-19,
		)
	}
	return registration, nil
}

// partyPeerEndpointChanged 只比较**端口**（M2 的触发条件："端口真正变化"）。
// 0 → 非 0 也算变化（首次上报）；非 0 → 0 不会走到这里（0 端口不覆盖既有值）。
func partyPeerEndpointChanged(
	previous currentPartyPeerEndpointRegistration,
	next currentPartyPeerEndpointRegistration,
) bool {
	return previous.Port != next.Port
}

// currentPartyPeerPortReadings 同时给出小端/大端两种读数，只喂日志。
func currentPartyPeerPortReadings(raw [2]byte) (little uint16, big uint16) {
	return partyPeerPortEndianLittle.decode(raw), partyPeerPortEndianBig.decode(raw)
}

// captureCurrentPartyPeerEndpointRegistration 记下这次 CS op2 自报的 endpoint。
//
// 返回值 = **端口是否真的变了**（含 0 → 非 0 的首次上报）。它只服务
// DNFBRIDGE_PARTY_PEER_ENDPOINT_REBROADCAST（默认 off，见
// party_peer_endpoint_rebroadcast.go）：参考实现在收到 CS op2 后会重播整份队伍信息
// （A21 `PartyHandler.cs:360-371` / 86JP `:94-105`），本函数把"该不该重播"的判据
// 收敛成这一个布尔值，调用方不再自己比较。
func (s *Service) captureCurrentPartyPeerEndpointRegistration(session *gameSession, body []byte) bool {
	if session == nil {
		return false
	}
	endian := s.partyPeerPortEndian()
	registration, err := parseCurrentPartyPeerEndpointRegistration(body, endian)
	if err != nil {
		s.logGameEvent(session, "game-party-peer-endpoint-registration-rejected",
			"body_len", len(body),
			"reason", "current_exe_op2_31_byte_layout_mismatch",
			"error", err)
		return false
	}
	// A zero-filled 31-byte reconnect probe is used by tests and by older
	// clients before their UDP socket is ready. Preserve the existing endpoint
	// until a real non-zero port arrives.
	if registration.Port == 0 {
		s.logPartyPeerEndpointRegistrationDiagnostic(session, registration, false)
		return false
	}
	// vendor 档：CS op2 就是"这个会话开始了"的权威信号（与厂商的 acc 取样时刻同一个
	// 事件），在这里给会话盖 op11 对端行的 acc 章；已经盖过就复用，绝不重发新号。
	// 取号必须在取会话锁**之前**（锁序见 partyPeerRowSessionAcc）。
	if s.partyPeerRowVendorFields().vendor() {
		if acc, ok := s.partyPeerRowSessionAcc(session); ok {
			registration.PeerRowAcc = acc
			registration.PeerRowAccStamped = true
		}
	}
	session.partyPeerMu.Lock()
	changed := partyPeerEndpointChanged(session.partyPeer, registration)
	session.partyPeer = registration
	session.partyPeerMu.Unlock()
	s.logPartyPeerEndpointRegistrationDiagnostic(session, registration, changed)
	little, big := currentPartyPeerPortReadings(registration.PortRaw)
	s.logGameEvent(session, "game-party-peer-endpoint-registered",
		"char_id", session.selectedCharacterID,
		"inner_ipv4", fmt.Sprintf("%d.%d.%d.%d",
			registration.InnerIPv4[0], registration.InnerIPv4[1], registration.InnerIPv4[2], registration.InnerIPv4[3]),
		"outer_ipv4", fmt.Sprintf("%d.%d.%d.%d",
			registration.OuterIPv4[0], registration.OuterIPv4[1], registration.OuterIPv4[2], registration.OuterIPv4[3]),
		"udp_port", registration.Port,
		"udp_port_raw", hex.EncodeToString(registration.PortRaw[:]),
		"udp_port_le", little,
		"udp_port_be", big,
		"port_endian", endian.String(),
		"port_changed", changed,
		"mtu", registration.MTU,
		"advertised_mtu", currentPartyPeerAdvertisedMTU(registration),
		"character_attribute", registration.CharacterAttribute,
		// op11 对端行四字段档（默认 vendor）与本次会话的 acc 盖章，判读串见
		// party_peer_row_vendor_fields.go 文件头。
		"peer_row_vendor_fields", s.partyPeerRowVendorFields().String(),
		"peer_row_acc", registration.PeerRowAcc,
		"peer_row_acc_stamped", registration.PeerRowAccStamped,
		"source", "current_exe_cs_op2_dynamic_udp_endpoint")
	return changed
}

func currentPartyPeerPort(session *gameSession) uint16 {
	return currentPartyPeerEndpointSnapshot(session).Port
}

func currentPartyPeerEndpointSnapshot(session *gameSession) currentPartyPeerEndpointRegistration {
	if session == nil {
		return currentPartyPeerEndpointRegistration{}
	}
	session.partyPeerMu.Lock()
	defer session.partyPeerMu.Unlock()
	return session.partyPeer
}

func currentPartyPeerAdvertisedMTU(registration currentPartyPeerEndpointRegistration) uint32 {
	if registration.MTU < currentPartyPeerMinimumMTU ||
		registration.MTU > currentPartyPeerMaximumMTU {
		return 0
	}
	return registration.MTU
}
