package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件实现**第二个单变量实验**：op11（class0 / `msg_id=11` PARTY_IP_INFO）里
// **收包者自己那一行**到底写什么 endpoint。
//
// ⚠️ 2026-09-25 归属变更：厂商 9.16 的 op11 **根本不发自己那一行**
// （body = `1 字节员数 + 22B/对端行`，见 `party_peer_endpoint_include_self.go` 文件头），
// 而 `DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF` 的**默认档已经改成 off = 厂商形状**。
// 因此本文件的两个档位在默认部署下**都不会被走到**（没有"自己那一行"可换 IP）：
// 只有在把 include_self 显式写成 `on`（一键回退到我们旧的"含自己那一行"形状）时，
// `observed` / `reported` 才重新决定那一行写什么。保留本开关是为了不丢失上一轮的
// A/B 能力与它的取证记录。
//
// 现状（`observed`，本开关的默认）：自己那一行 = 游戏 TCP 会话观察到的**公网** IPv4 + 客户端自报端口。
// 这与本仓库自己那句注释不符（`buildRuntimePartyPeerEndpointInfoForReceiver`：
// "The receiver's own row remains its client-registered endpoint, as the client uses it to
// retain its local socket identity"）—— 端口用的是客户端自报值，IP 却被换成了公网地址。
// 同族参考实现（86JP `PartyIpInfoBuilder.BuildForRelay`）在"自己那一行"写的是**成员自己上报的
// IPv4**（LAN 直连语义），并且注释明确"逐字节照 86JP 客户端 sub_D31160 读序，存进客户端队伍
// 成员对象 m+0x3F0(inner)/+0x3F4(outer)/+0x3F8(port)/+0x3FA(nat)"。
//
// 为什么值得单变量试一次：现场两个客户端都在同一台机器上（`192.168.0.101`），
// op11 自己那一行拿到的是 `39.172.226.229`（本机并不持有的地址）。若客户端**确实**用
// 自己那一行来建立/绑定本地 P2P socket（我们自己的注释就是这么写的），那么一个非本机地址
// 会让这一步失败，而失败者永远是"需要先建监听/回话那一侧"——与实测的
// "队长一发就发、队员一个数据报都不发"完全吻合。**这只是候选解释之一，不是结论。**
//
// 默认行为**逐字节不变**：默认 `observed` 走原来的 `party.BuildPeerEndpointInfo`，
// 一个字节都不改；只有显式写 `reported` 才改这一行。
const partyPeerSelfEndpointEnv = "DNFBRIDGE_PARTY_PEER_SELF_ENDPOINT"

const (
	partyPeerSelfEndpointValueObserved = "observed"
	partyPeerSelfEndpointValueReported = "reported"
)

// op11 行宽与 MTU：与 `internal/modules/dnf/party` 的 `partyPeerEndpointSize`(22) /
// `partyPeerMTU`(1500) 保持一致（那两个常量在 module 包内未导出，本包按已证实布局自持一份）。
const (
	currentPartyPeerEndpointSize = 22
	currentPartyPeerEndpointMTU  = 1500
)

// partyPeerSelfEndpointMode 是 partyPeerSelfEndpointEnv 的归一化取值。
type partyPeerSelfEndpointMode int

const (
	// partyPeerSelfEndpointObserved 是默认档：自己那一行照旧用 TCP 观察到的 IPv4。
	partyPeerSelfEndpointObserved partyPeerSelfEndpointMode = iota
	// partyPeerSelfEndpointReported 用客户端在 CS op2 里自报的 inner/outer/port。
	partyPeerSelfEndpointReported
)

func (mode partyPeerSelfEndpointMode) String() string {
	if mode == partyPeerSelfEndpointReported {
		return partyPeerSelfEndpointValueReported
	}
	return partyPeerSelfEndpointValueObserved
}

func (mode partyPeerSelfEndpointMode) reported() bool {
	return mode == partyPeerSelfEndpointReported
}

// readPartyPeerSelfEndpointMode 解析 DNFBRIDGE_PARTY_PEER_SELF_ENDPOINT。
// **默认 observed**（= 现行为）；只有显式写 `reported` 才改 op11 里自己那一行，
// 线上可一键回退。
func readPartyPeerSelfEndpointMode(value string) partyPeerSelfEndpointMode {
	if strings.EqualFold(strings.TrimSpace(value), partyPeerSelfEndpointValueReported) {
		return partyPeerSelfEndpointReported
	}
	return partyPeerSelfEndpointObserved
}

// partyPeerSelfEndpoint 返回当前 Service 的归一化档位（零值 options 的测试/探针路径
// = observed = 现行为）。
func (s *Service) partyPeerSelfEndpoint() partyPeerSelfEndpointMode {
	if s == nil {
		return partyPeerSelfEndpointObserved
	}
	return s.options.partyPeerSelfEndpoint
}

// currentPartyPeerEndpointRow 是 op11 的一个成员行，保留 inner/outer **分开**写的能力
// （客户端自报时两者可能不同；`party.BuildPeerEndpointInfo` 只能写同一个 IPv4 两次）。
//
// nat / mtu / charAttr 三个字段：**零值 = 这个构造器不改那三个字节**
// （nat=0、mtu 取 `currentPartyPeerEndpointMTU`(1500)、charAttr=0）。
// 厂商 9.16 实测的 `nat=1`、`mtu=512`、`char_attr=0x64` 由
// `DNFBRIDGE_PARTY_PEER_ROW_VENDOR_FIELDS`（**默认 vendor**）在 op11 的 body 出口统一
// 盖上（对每一行生效，含 `reported` 档的"自己那一行"），见 party_peer_row_vendor_fields.go；
// `legacy` 档出口不盖，字节与改动前逐字节相同。
type currentPartyPeerEndpointRow struct {
	userID    uint16
	inner     [4]byte
	outer     [4]byte
	port      uint16
	accountID uint32
	nat       byte
	mtu       uint32
	charAttr  byte
}

// buildCurrentPartyPeerEndpointInfoRows 按已证实的 22 字节/成员布局逐字节重建 op11 body：
//
//	u8 count, repeat count { u16 uid(LE), 4B inner, 4B outer, u16 port(网络序=大端),
//	                         u32 accountID(LE), u8 nat, u32 mtu(LE), u8 charAttr }
//
// 逐字段与 A21 `PartyIpInfoBuilder.cs:6-13` 的注释布局相同；厂商 9.16 的两条实测 body
// （`01 0200 …` / `01 0100 …`）在本函数下**逐字节相等**（见
// `party_vendor_formation_alignment_test.go` 的 `vendor…` 夹具用例）。
//
// 这个函数**只**在 `reported` 档里被调用（默认档仍然走 `party.BuildPeerEndpointInfo`），
// 所以它不需要覆盖该 builder 的全部行为；但为了可回归，测试里对它做了
// "与 party.BuildPeerEndpointInfo 在等价输入下逐字节相同" 的断言。
func buildCurrentPartyPeerEndpointInfoRows(rows []currentPartyPeerEndpointRow) []byte {
	if len(rows) > currentPartyUDPRelayMaxMembers {
		rows = rows[:currentPartyUDPRelayMaxMembers]
	}
	body := make([]byte, 0, 1+len(rows)*currentPartyPeerEndpointSize)
	body = append(body, byte(len(rows)))
	for _, row := range rows {
		mtu := row.mtu
		if mtu == 0 {
			mtu = currentPartyPeerEndpointMTU
		}
		body = append(body, byte(row.userID), byte(row.userID>>8))
		body = append(body, row.inner[:]...)
		body = append(body, row.outer[:]...)
		body = append(body, byte(row.port>>8), byte(row.port))
		body = append(body, byte(row.accountID), byte(row.accountID>>8),
			byte(row.accountID>>16), byte(row.accountID>>24))
		body = append(body, row.nat)
		body = append(body, byte(mtu&0xff), byte((mtu>>8)&0xff),
			byte((mtu>>16)&0xff), byte((mtu>>24)&0xff))
		body = append(body, row.charAttr)
	}
	return body
}

// currentPartyPeerSelfEndpointRow 用客户端自报的 endpoint 造"自己那一行"。
// 返回 ok=false 表示该会话没有可用的 CS op2 上报（端口为 0 或 IP 全零），
// 此时调用方必须回落到现行为（TCP 观察到的 IPv4 + 默认端口）。
func currentPartyPeerSelfEndpointRow(
	registration currentPartyPeerEndpointRegistration,
	userID uint16,
	accountID uint32,
) (currentPartyPeerEndpointRow, bool) {
	if registration.Port == 0 {
		return currentPartyPeerEndpointRow{}, false
	}
	inner := registration.InnerIPv4
	outer := registration.OuterIPv4
	if ipv4IsZero(inner) && ipv4IsZero(outer) {
		return currentPartyPeerEndpointRow{}, false
	}
	if ipv4IsZero(outer) {
		outer = inner
	}
	if ipv4IsZero(inner) {
		inner = outer
	}
	return currentPartyPeerEndpointRow{
		userID:    userID,
		inner:     inner,
		outer:     outer,
		port:      registration.Port,
		accountID: accountID,
		// char_attr 在这个构造器里保持 0：本实验**只**换自己那一行的 IP/端口。
		// 注意 char_attr 的**最终出口值**由 `DNFBRIDGE_PARTY_PEER_ROW_VENDOR_FIELDS`
		// 决定（默认 vendor = 0x64，在 body 出口统一盖上；legacy = 保持这里的 0）。
		// CS op2 的 `body[0]` 与 op11 `char_attr` 的关系仍然没有证据，
		// registration.CharacterAttribute 只被解析并写进日志供判读。
		charAttr: 0,
	}, true
}

func ipv4IsZero(address [4]byte) bool {
	return address[0] == 0 && address[1] == 0 && address[2] == 0 && address[3] == 0
}

// peerEndpointRowFromEndpoint 把现行为（`party.PeerEndpoint`）转成同一个行结构，
// 用于"两条路径字节一致"的回归断言与混合组装。
func peerEndpointRowFromEndpoint(endpoint party.PeerEndpoint) currentPartyPeerEndpointRow {
	return currentPartyPeerEndpointRow{
		userID:    endpoint.UserID,
		inner:     endpoint.IPv4,
		outer:     endpoint.IPv4,
		port:      endpoint.Port,
		accountID: endpoint.AccountID,
	}
}
