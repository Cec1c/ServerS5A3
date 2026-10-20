package dnfbridge

import (
	"strconv"
	"strings"
	"sync"

	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件把 op11（class0 / `msg_id=11` PARTY_IP_INFO）**对端行**里最后四个字段
// （`acc` / `nat` / `mtu` / `char_attr`）对齐到厂商 9.16 的实测值，全部 env 可注入。
//
// # 一、取证（只读引用厂商参考环境 `D:\DNF90-test\server\runtime\logs\packet_log.txt`）
//
// 该日志里 `msg_id=11 classification=0`（= op11）共 **12 条实例**，覆盖两次干净进程、
// 三个队伍、两个方向（去 16 字节 class0 头后 body 逐字节）：
//
//	行号   conn      收包者       body（23B = 1B 员数 + 22B/行）
//	:3337  game-59   char_id=1   01 | 0200 c0a80065 c0a80065 7530 1c000000 01 00020000 64
//	:3340  game-61   char_id=2   01 | 0100 c0a80065 c0a80065 7531 1b000000 01 00020000 64
//	:5361  game-59   char_id=1   01 | 0200 … 同上（副本进场 remotes，body 与 formation 逐字节相同）
//	:5380  game-61   char_id=2   01 | 0100 … 同上
//	:16629 game-55   char_id=1   01 | 0200 c0a80065 c0a80065 7530 30000000 01 00020000 64
//	:16632 game-52   char_id=2   01 | 0100 c0a80065 c0a80065 7531 31000000 01 00020000 64
//	:18631 game-55   char_id=1   同 :16629
//	:18650 game-52   char_id=2   同 :16632
//	:23543 game-55   char_id=1   同 :16629（party_id 换成 3 之后的值**没有变**）
//	:23546 game-52   char_id=2   同 :16632
//	:25677 game-55   char_id=1   同 :16629（过图那一次）
//	:25696 game-52   char_id=2   同 :16632
//
// 逐字段回读（22B 行 = `u16 uid(LE) | 4B inner | 4B outer | u16 port(BE) | u32 acc(LE) |
// u8 nat | u32 mtu(LE) | u8 char_attr`，见 `party_peer_self_endpoint.go` 的行结构）：
//
//	字段        12/12 实例取值          结论
//	nat         恒 `01`                 **恒定 1**（客户端 CS op2 的 `body[0]` 也是 1，见下）
//	mtu         恒 `00 02 00 00`(LE512) **恒定 512**（不是原样回显 CS op2 的 1472）
//	char_attr   恒 `64`                **恒定 0x64**
//	acc         `1c000000`/`1b000000`（进程 A）/ `30000000`/`31000000`（进程 B）
//	                                  **不是常量**：见下面"acc 规律"
//	uid/port    uid 恒 = 对端 char_id；port 恒 = relay 端口 30000/30001（= portStart + 腿号）
//
// `nat`/`mtu` 与客户端自报值的关系（同会话配对，CS op2 = `msg_id=2
// ENUM_CMDPACKET_SET_UDP_IP_PORT`，body 31B）：
//
//	:2524 conn=game-000059 CS op2 body = `01 c0a80065 c0a80065 13c7 c0050000 0c000000 30306666…`
//	      ⇒ nat?=01、inner/outer=192.168.0.101、port(BE)=5063、**mtu(LE)=1472**
//	:2908 conn=game-000061 同上，port(BE)=0xf465=62565、mtu=1472
//
// ⇒ **厂商把客户端自报的 mtu=1472 改写成 512**（`nat` 恰好与客户端 `body[0]=1` 同值，
// 无法区分"回显"与"服务端恒定值"，因此本开关按**恒定值**落地）。这正是"这两个值是
// 服务端在中继路径上故意设定的"这一假设的直接证据；它们是"客户端要不要往中继发包"
// 的候选开关，本轮把它做成默认档（单变量 A/B：`legacy` 一键回退）。
//
// # 二、acc 规律（本轮新推出，4/4 精确命中）
//
// **厂商的 acc = 该成员会话建立时，本服务进程已经接受过的 `game` 连接数（0 基），
// 建成后不再改变。**
//
// 证据（同一份日志；计数命令 = `dnfbridge game connection accepted` 在
// `game-connect` 之前出现的条数）：
//
//	成员              会话建立时刻            acc   本进程此前的 game 连接数
//	char1 (conn59)   10:22:48.8003255Z 进程A   27    27 ✓
//	char2 (conn61)   10:22:53.4241620Z 进程A   28    28 ✓
//	char2 (conn52)   10:37:47.6215128Z 进程B   48    48 ✓
//	char1 (conn55)   10:38:17.0104698Z 进程B   49    49 ✓
//
// 四条旁证（把"巧合"排除掉）：
//  1. 同一进程内 **channel** 连接数（32/33/4/6）与之不匹配；两者**之和**恰好等于
//     厂商自己的 `conn_id`（32+27=59=`game-000059`、33+28=61=`game-000061`）⇒
//     `conn_id` 数的是"全部连接"、`acc` 数的是"game 子集"，是两个独立计数器。
//  2. 同一进程内 **两个成员的 acc 不同**（27/28、48/49）⇒ acc 在**会话建立时**取样，
//     不是 party formation 时才生成。
//  3. 同进程内 party_id 从 2 换成 3（`:23543`/`:23546`）acc **一点没变** ⇒
//     它是会话级盖章，不是队伍级/腿级编号。
//  4. 两次进程里两个成员的先后顺序**翻转**（进程A char1 先登录 ⇒ 27<28；进程B char2
//     先登录 ⇒ 48<49），与"按登录顺序递增"完全一致。
//
// 我们的复刻：**同一形状**（进程内单调、小整数、按会话建立顺序递增、每会话终身不变、
// 非零），计数点取**该会话在本进程里第一次被 party 代码盖章的时刻**（正常路径 =
// 收到它的 CS op2 `SET_UDP_IP_PORT`，即厂商同一个"会话开始"信号；CS op2 缺失时在
// 第一次组装 op11 行时补盖）。与厂商的**唯一差别是计数对象**：厂商数 `game` 连接，
// 我们数"被盖章的会话"。真正的 game 连接计数器在 transport 层（`game.go`
// `handleGameConn`），不属于本轮允许改动的组队文件，因此这里不去动它；绝对数值因此
// 与厂商不同（我们 1,2,…；厂商 27/28/48/49），但客户端只把它当**不透明的小整数**存下
// （86JP `PartyIpInfoBuilder.cs:12` 注明该值存进客户端成员对象），形状一致即可。
// 需要绝对数值时用 `DNFBRIDGE_PARTY_PEER_ROW_ACC=<u32>` 钉死（夹具回归就是这么做的）。
//
// # 三、开关（两个，独立于其它 `DNFBRIDGE_PARTY_*` 组）
//
//	DNFBRIDGE_PARTY_PEER_ROW_VENDOR_FIELDS   vendor（默认，空/非法也是它）| legacy
//	DNFBRIDGE_PARTY_PEER_ROW_ACC             auto（默认）| <u32>（十进制或 0x 前缀）
//
// `legacy` = 逐字节回到改动前（`acc` = 成员 char_id、`nat=0`、`mtu=1500`、
// `char_attr=0`，即 `party.BuildPeerEndpointInfo` 的既有输出）；
// `ROW_ACC` 只在 `vendor` 档生效（`legacy` 档忽略它，保证回退是**无条件**逐字节的）。
const partyPeerRowVendorFieldsEnv = "DNFBRIDGE_PARTY_PEER_ROW_VENDOR_FIELDS"

// partyPeerRowAccEnv 见文件头"三、开关"。
const partyPeerRowAccEnv = "DNFBRIDGE_PARTY_PEER_ROW_ACC"

const (
	partyPeerRowVendorFieldsValueVendor = "vendor"
	partyPeerRowVendorFieldsValueLegacy = "legacy"
)

// 厂商 9.16 op11 对端行里三个**恒定**字段（12/12 实例，见文件头表）。
const (
	vendorPartyPeerRowNat      byte   = 1
	vendorPartyPeerRowMTU      uint32 = 512
	vendorPartyPeerRowCharAttr byte   = 0x64
)

// partyPeerRowVendorFieldsMode 是 partyPeerRowVendorFieldsEnv 的归一化取值。
type partyPeerRowVendorFieldsMode int

const (
	// partyPeerRowRowFieldsVendor 是默认档（零值）= 厂商 9.16 实测值。
	partyPeerRowRowFieldsVendor partyPeerRowVendorFieldsMode = iota
	// partyPeerRowRowFieldsLegacy = 改动前（`char_id/0/1500/0`）逐字节回退。
	partyPeerRowRowFieldsLegacy
)

func (mode partyPeerRowVendorFieldsMode) String() string {
	if mode == partyPeerRowRowFieldsLegacy {
		return partyPeerRowVendorFieldsValueLegacy
	}
	return partyPeerRowVendorFieldsValueVendor
}

// vendor 表示 op11 对端行是否要盖厂商字段。
func (mode partyPeerRowVendorFieldsMode) vendor() bool {
	return mode != partyPeerRowRowFieldsLegacy
}

// readPartyPeerRowVendorFieldsMode 解析 DNFBRIDGE_PARTY_PEER_ROW_VENDOR_FIELDS。
// **默认 vendor**：空/未设置/非法值都是 vendor；只有显式写 `legacy` 才逐字节回退。
func readPartyPeerRowVendorFieldsMode(value string) partyPeerRowVendorFieldsMode {
	if strings.EqualFold(strings.TrimSpace(value), partyPeerRowVendorFieldsValueLegacy) {
		return partyPeerRowRowFieldsLegacy
	}
	return partyPeerRowRowFieldsVendor
}

// partyPeerRowVendorFields 返回当前 Service 的归一化档位（零值 options / nil Service
// 的测试与探针路径 = vendor = 厂商值）。
func (s *Service) partyPeerRowVendorFields() partyPeerRowVendorFieldsMode {
	if s == nil {
		return partyPeerRowRowFieldsVendor
	}
	return s.options.partyPeerRowVendorFields
}

// partyPeerRowAccSetting 是 DNFBRIDGE_PARTY_PEER_ROW_ACC 的归一化取值。
//
// 用 `pinned` 而不是 `auto` 当第一字段：**零值 = auto**（本仓库惯例：零值 options 必须
// 就是默认档，否则零值 Service / 探针路径会写出 acc=0 的行）。
type partyPeerRowAccSetting struct {
	// pinned = true 表示 env 显式钉死了一个值。
	pinned bool
	// value 只在 pinned = true 时有意义（`ROW_ACC=0` 是合法的显式钉死值）。
	value uint32
	// raw / bad 只用于把"非法取值已回退到 auto"记进启动日志。
	raw string
	bad bool
}

// auto 表示按规律算（见文件头"二、acc 规律"）。
func (setting partyPeerRowAccSetting) auto() bool {
	return !setting.pinned
}

func (setting partyPeerRowAccSetting) String() string {
	if setting.auto() {
		return partyPeerRowAccAutoValue
	}
	return strconv.FormatUint(uint64(setting.value), 10)
}

const partyPeerRowAccAutoValue = "auto"

// readPartyPeerRowAccSetting 解析 DNFBRIDGE_PARTY_PEER_ROW_ACC。
// **默认 auto**（空/未设置/非法值都回落 auto）；显式十进制或 `0x` 十六进制 = 钉死。
func readPartyPeerRowAccSetting(value string) partyPeerRowAccSetting {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.EqualFold(trimmed, partyPeerRowAccAutoValue) {
		return partyPeerRowAccSetting{}
	}
	parsed, err := strconv.ParseUint(trimmed, 0, 32)
	if err != nil {
		return partyPeerRowAccSetting{raw: trimmed, bad: true}
	}
	return partyPeerRowAccSetting{pinned: true, value: uint32(parsed), raw: trimmed}
}

// partyPeerRowAcc 返回当前 Service 的归一化取值（零值 options / nil Service = auto）。
func (s *Service) partyPeerRowAcc() partyPeerRowAccSetting {
	if s == nil {
		return partyPeerRowAccSetting{}
	}
	return s.options.partyPeerRowAcc
}

// partyPeerRowAccSeq 是"每会话一个进程内序号"的发号器。
//
// **进程级**（不是 Service 级）：厂商的计数器同样是进程级（服务重启即从头开始，
// 证据 = 进程 A 27/28 与进程 B 48/49 各自从自己进程的连接计数出发）。放在包级还
// 有一个好处：新增状态全部留在本文件里，不动 `Service` 结构体。
var (
	partyPeerRowAccSeqMu sync.Mutex
	partyPeerRowAccSeq   uint32
)

// nextPartyPeerRowAcc 取下一个小整数序号。
//
// **1 基**（不从 0 开始）：厂商实测值全是 ≥27 的非零小整数，而 0 在同一个 4 字节字段里
// 天然是"未设置"的形状，1 基可以避免客户端把 0 当成空值（无客户端证据，纯保守选择）。
func nextPartyPeerRowAcc() uint32 {
	partyPeerRowAccSeqMu.Lock()
	defer partyPeerRowAccSeqMu.Unlock()
	partyPeerRowAccSeq++
	if partyPeerRowAccSeq == 0 { // 溢出兜底：回到 1，绝不发 0
		partyPeerRowAccSeq = 1
	}
	return partyPeerRowAccSeq
}

// resetPartyPeerRowAccSeqForTest 把发号器复位（只给回归用；线上一个进程只跑一个
// Service，不存在复位需求）。
func resetPartyPeerRowAccSeqForTest(next uint32) {
	partyPeerRowAccSeqMu.Lock()
	partyPeerRowAccSeq = next
	partyPeerRowAccSeqMu.Unlock()
}

// partyPeerRowSessionAcc 返回该会话**稳定**的 acc 盖章：已有就复用，没有就取一个新号
// 并写回会话（写回保证同一次队伍里 op11 被发三次——formation / 名册后补发 / 副本进场
// remotes——时对端看到的 acc 不变，与厂商"会话级盖章"一致）。
//
// 锁序：本函数先释放会话锁再向发号器取号（发号器只吃自己的包级锁），最后再取会话锁
// 写回 —— 全程不出现"持会话锁取 Service/包级锁"的嵌套，与仓库既有锁序一致。
func (s *Service) partyPeerRowSessionAcc(session *gameSession) (uint32, bool) {
	if session == nil {
		return 0, false
	}
	session.partyPeerMu.Lock()
	stamped, acc := session.partyPeer.PeerRowAccStamped, session.partyPeer.PeerRowAcc
	session.partyPeerMu.Unlock()
	if stamped {
		return acc, true
	}
	fresh := nextPartyPeerRowAcc()
	session.partyPeerMu.Lock()
	if session.partyPeer.PeerRowAccStamped {
		fresh = session.partyPeer.PeerRowAcc
	} else {
		session.partyPeer.PeerRowAcc = fresh
		session.partyPeer.PeerRowAccStamped = true
	}
	session.partyPeerMu.Unlock()
	s.logGameEvent(session, "game-party-peer-row-acc-stamped",
		"char_id", session.selectedCharacterID,
		"acc", fresh,
		"source", "vendor_party_peer_row_acc_per_process_session_ordinal")
	return fresh, true
}

// partyPeerRowAccountID 决定一行 op11 的 acc 字段。
//
//	vendor + auto    → 该成员会话的进程内会话序号（见文件头"二"）
//	vendor + <u32>   → 钉死值
//	legacy           → 原样保留调用方给的值（= 成员 char_id，逐字节回退）
func (s *Service) partyPeerRowAccountID(userID uint16, legacyAccountID uint32) uint32 {
	if !s.partyPeerRowVendorFields().vendor() {
		return legacyAccountID
	}
	if override := s.partyPeerRowAcc(); override.pinned {
		return override.value
	}
	session, ok := s.onlineGameSession(userID)
	if !ok || session == nil {
		// 会话已不在线（退役中的快照）：给出与旧行为同值的确定性回落，绝不每发一次
		// 就换一个号（那会让对端看到 acc 漂移，厂商从不如此）。
		return legacyAccountID
	}
	acc, ok := s.partyPeerRowSessionAcc(session)
	if !ok {
		return legacyAccountID
	}
	return acc
}

// applyPartyPeerRowVendorFields 把厂商档的四个字段盖到一组已构造好的行上。
// legacy 档**原样返回**（调用方随后逐字节走 `party.BuildPeerEndpointInfo` 那条老路）。
func (s *Service) applyPartyPeerRowVendorFields(
	rows []currentPartyPeerEndpointRow,
) []currentPartyPeerEndpointRow {
	if !s.partyPeerRowVendorFields().vendor() {
		return rows
	}
	patched := make([]currentPartyPeerEndpointRow, 0, len(rows))
	for _, row := range rows {
		row.nat = vendorPartyPeerRowNat
		row.mtu = vendorPartyPeerRowMTU
		row.charAttr = vendorPartyPeerRowCharAttr
		row.accountID = s.partyPeerRowAccountID(row.userID, row.accountID)
		patched = append(patched, row)
	}
	return patched
}

// buildPartyPeerEndpointInfoBody 是直连形态（无中继 / 中继不可用 / 收包者未知）的
// op11 body 出口，也是本文件的唯一接线点之一：
//
//	legacy 档 → **一个字节都不碰**：仍走已证实的 `party.BuildPeerEndpointInfo`；
//	vendor 档 → 转成同一个 22B 行结构、盖上厂商四字段，再复用
//	            `buildCurrentPartyPeerEndpointInfoRows`（行布局不变，只换四个值）。
func (s *Service) buildPartyPeerEndpointInfoBody(endpoints []party.PeerEndpoint) []byte {
	if !s.partyPeerRowVendorFields().vendor() {
		return party.BuildPeerEndpointInfo(endpoints)
	}
	rows := make([]currentPartyPeerEndpointRow, 0, len(endpoints))
	for _, endpoint := range endpoints {
		rows = append(rows, peerEndpointRowFromEndpoint(endpoint))
	}
	return buildCurrentPartyPeerEndpointInfoRows(s.applyPartyPeerRowVendorFields(rows))
}
