package dnfbridge

import (
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 本文件实现 op11（class0 / `msg_id=11` PARTY_IP_INFO）的**行集合**对齐：
// **收包者自己那一行到底发不发**。
//
// 厂商 9.16 实测（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，同一场组队同一秒）：
//
//	:3335/:3337 conn=game-000059 char_id=1 → body_len=23 msg_id=11 peer_count=1
//	  SEND hex=000b0027000000c13c09f0610000000001 0200 c0a80065 c0a80065 7530 1c000000 01 00020000 64
//	:3338/:3340 conn=game-000061 char_id=2 → body_len=23 msg_id=11 peer_count=1
//	  SEND hex=000b0027000000671291cc6000000000 01 0100 c0a80065 c0a80065 7531 1b000000 01 00020000 64
//
// 去掉 16 字节 class0 头之后，body = `1 字节员数 + 22 字节/行`：
//
//	conn59 → `01 | 0200 c0a80065 c0a80065 7530 1c000000 01 00020000 64`  （只含对端 char_id=2）
//	conn61 → `01 | 0100 c0a80065 c0a80065 7531 1b000000 01 00020000 64`  （只含对端 char_id=1）
//
// ⇒ **员数 = 对端数（不含自己）**，自己那一行**根本不存在**；两台客户端各收一条、内容互为对端。
// 对比我们改动前的形状：`00 02 <自己 22B> <对端 22B>`（45B、count=2、含自己那一行），
// 也就是**多发了一条"自己"的行**。
//
// 为什么值得对齐：队友"一个数据报都不发"这条症状，最可能的服务端侧解释就是客户端被自己那一行
// 带进了错误的本地端点绑定；同一份客户端（同一个 `90CN.dll`）在厂商 9.16 服务端上组队完全正常，
// 而厂商从不发那一行。
//
// 边界：`DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF=on` 是**一键回退**，回到我们改动前的
// 逐字节形状（含自己那一行，且 `DNFBRIDGE_PARTY_PEER_SELF_ENDPOINT` 重新生效）。
// 注：C# 系的 A21 `PartyIpInfoBuilder.BuildForRelay` 是**含**自己那一行的，所以 `on` 档
// 对齐的是 A21，不是厂商 9.16 —— 默认档才是厂商形状。
const partyPeerEndpointIncludeSelfEnv = "DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF"

const (
	partyPeerEndpointIncludeSelfValueOn  = "on"
	partyPeerEndpointIncludeSelfValueOff = "off"
)

// partyPeerEndpointIncludeSelfMode 是 partyPeerEndpointIncludeSelfEnv 的归一化取值。
type partyPeerEndpointIncludeSelfMode int

const (
	// partyPeerEndpointIncludeSelfOff 是默认档（零值）= 厂商形状：op11 只含对端行。
	partyPeerEndpointIncludeSelfOff partyPeerEndpointIncludeSelfMode = iota
	// partyPeerEndpointIncludeSelfOn 是回退档 = 我们改动前的形状（含自己那一行）。
	partyPeerEndpointIncludeSelfOn
)

func (mode partyPeerEndpointIncludeSelfMode) String() string {
	if mode == partyPeerEndpointIncludeSelfOn {
		return partyPeerEndpointIncludeSelfValueOn
	}
	return partyPeerEndpointIncludeSelfValueOff
}

// includesSelf 表示 op11 是否要写"收包者自己那一行"。
func (mode partyPeerEndpointIncludeSelfMode) includesSelf() bool {
	return mode == partyPeerEndpointIncludeSelfOn
}

// readPartyPeerEndpointIncludeSelfMode 解析 DNFBRIDGE_PARTY_PEER_ENDPOINT_INCLUDE_SELF。
// **默认 off（= 厂商形状）**：空/未设置/非法值都是 off；只有显式写 `on` 才回到旧形状。
func readPartyPeerEndpointIncludeSelfMode(value string) partyPeerEndpointIncludeSelfMode {
	if strings.EqualFold(strings.TrimSpace(value), partyPeerEndpointIncludeSelfValueOn) {
		return partyPeerEndpointIncludeSelfOn
	}
	return partyPeerEndpointIncludeSelfOff
}

// partyPeerEndpointIncludeSelf 返回当前 Service 的归一化档位（零值 options 的测试/探针路径
// = off = 厂商形状）。
func (s *Service) partyPeerEndpointIncludeSelf() partyPeerEndpointIncludeSelfMode {
	if s == nil {
		return partyPeerEndpointIncludeSelfOff
	}
	return s.options.partyPeerEndpointIncludeSelf
}

// partyPeerEndpointRowsExcludeReceiver 按档位把收包者自己从 op11 成员表里去掉。
//
// off 档（默认）返回去掉自己的成员表；on 档（或收包者未知）原样返回。
// 队伍里只有收包者一个人时 off 档会返回空表 ⇒ body = `00`（员数 0，无行）；这是厂商形状
// 的直接推论（"只发对端"），但厂商日志里没有单队伍样本，因此单独记一条
// `game-runtime-party-peer-endpoints-self-only` 便于实测判读（回退开关 = include_self=on）。
func partyPeerEndpointRowsExcludeReceiver(
	members []alignedcmd.PartyMemberState,
	receiverID uint16,
	mode partyPeerEndpointIncludeSelfMode,
) ([]alignedcmd.PartyMemberState, bool) {
	if mode.includesSelf() || receiverID == 0 {
		return members, false
	}
	peers := make([]alignedcmd.PartyMemberState, 0, len(members))
	for _, member := range members {
		if member.UserID == receiverID {
			continue
		}
		peers = append(peers, member)
	}
	return peers, len(peers) == 0 && len(members) > 0
}
