package dnfbridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 本文件是**厂商那套"境外 UDP 中继控制通道客户端"的 Go 移植**（严格"只按 B"：以我们
// 的 Go 源码为基座，把厂商 C# 的行为搬过来；厂商源码只读参考，不是我们的基座）。
//
// 厂商参考（只读）：
//   - `Server/DfoServer/Network/Core/RemotePartyUdpRelay.cs`（HELLO 认证 / 自动重连 /
//     SYNC 请求响应 / 10s 续租 / 30s 租约 / 2s 超时 / 重连后立即补齐）
//   - `Server/DfoServer/Network/Core/PartyRelayProtocol.cs`（wire 协议 v2）
//   - `PartyUdpRelay.cs` 头部注释：每个 (owner→peer) 有向腿独占一个 UDP 端口，
//     客户端按"源 ip:port"区分队友；**客户端视角恒为"队友 P 在中继公网IP:port(owner,peer)"**。
//
// 为什么必须有这个文件（港机现场事实）：`/etc/systemd/system/a21-relay.service` 的注释写明
// 「境外组队 UDP 中继（HK 192.0.2.12）… 国内游戏服只做游戏逻辑、**不绑定中继端口、不转发
// 队友流量**；本服务只监听控制 TCP 10250 + UDP 30000..30255」。也就是说厂商架构里中继是
// **独立进程**，游戏服只做两件事：① 在控制通道上把"房间成员集合"换成"端口矩阵"；
// ② 把矩阵写进 op11 的 22B 行里。我们此前的内嵌中继（party_udp_relay.go）自己 bind 31000+X
// 的腿——现场 6 次实测每次都只有 1 个 21 字节握手到达、`leg_forwarded_datagrams=0`，
// 而 a21-relay 正在为另一台有真实玩家的服务器正常转发（`first relay forward succeeded`）。
// 默认档因此切成 remote：**由 a21-relay 转发，我们不再绑任何中继端口**。
//
// 协议逐字节（与 PartyRelayProtocol.cs 同口径）：
//
//	帧:      u8 opcode | u16 BE length | payload[length]，单帧上限 8 KiB
//	HELLO:   0x01   payload = u8 version=2 + 32B HMAC-SHA256(secret, "A21RELAY-HELLO/v1")
//	         → 0x81 HELLO_OK（空 payload）
//	SYNC:    0x02   payload = u8 scope(0=party) + u32 BE roomId + u8 mode(0=成员键)
//	                            + u16 BE count + count×u16 BE memberKey
//	         → 0x82 SYNC_OK  = u32 BE roomId + u16 BE count + count×(u16 owner,u16 peer,u16 port)
//	         → 0x83 SYNC_FAIL= u32 BE roomId + u8 reason
//	CLOSE:   0x03   payload = u8 scope + u32 BE roomId → 0x84 CLOSE_OK
//
// 房间号命名空间（**与厂商/另一台服的房间号隔离**）：a21-relay 是**共享**中继，
// 我们抓到它同时在为另一台有真实玩家的服务器转发（room=18/19/20/22/61 这种小整数）。
// 我们的 partyID 也是 1/2/3 这类小整数，直接当 roomId 用就会和对方撞车（同一 scope=party
// ⇒ 中继会把两边玩家串到同一批腿上）。因此实际上线的 `roomId = ROOM_BASE + partyID`
// （`DNFBRIDGE_PARTY_UDP_RELAY_ROOM_BASE`，默认 100000），且 partyID=0 时**不注册**。
// 成员键（memberKey）保持厂商语义 = 角色 id（u16）：客户端看到的端口只由 (owner,peer) 决定，
// 成员键跨房间不冲突，不需要也不应该改。
//
// 与厂商的两处**有界偏离**（都在回退语义之内，且写成常量便于判读）：
//  1. 同一房间、同一成员集合且矩阵新鲜时**直接吃缓存**（默认 5s < 10s 续租周期），
//     不再每次广播都走一次 SYNC——否则组队广播路径每次都要等一个往返（厂商是同步等 2s）。
//  2. SYNC 超时后进入**降级窗口**（默认 2s）：窗口内直接返回"不可用"（调用方回退直连），
//     不重复阻塞。两者都不改变"拿不到矩阵 ⇒ 整份回退直连名单"的厂商语义。
//
// 安全：密钥只用于 HMAC，**绝不进日志**（日志里只有 configured/missing/invalid 三个词）。
// 本进程在 remote 档下不 bind 任何中继 UDP 端口、不转发任何队友流量。

// 开关与默认值。
const (
	// partyUDPRelayModeEnv 决定组队 P2P 走哪条路：
	//   - `remote`（线上默认，由 optionsFromConfig 给出）= 厂商架构：注册到 a21-relay；
	//   - `embedded` = 我们自建的内嵌中继（party_udp_relay.go），一键回退到改动前行为。
	partyUDPRelayModeEnv = "DNFBRIDGE_PARTY_UDP_RELAY_MODE"
	// partyUDPRelayRemoteEndpointEnv 是中继控制通道地址（host:port）。
	partyUDPRelayRemoteEndpointEnv = "DNFBRIDGE_PARTY_UDP_RELAY_REMOTE"
	// partyUDPRelayRemoteSecretEnv 是 64 个 hex 字符（32 字节）的共享密钥。
	partyUDPRelayRemoteSecretEnv = "DNFBRIDGE_PARTY_UDP_RELAY_SECRET"
	// partyUDPRelayRemoteRoomBaseEnv 是我们这一侧的房间号命名空间起点（见文件头）。
	partyUDPRelayRemoteRoomBaseEnv = "DNFBRIDGE_PARTY_UDP_RELAY_ROOM_BASE"

	partyUDPRelayModeValueRemote   = "remote"
	partyUDPRelayModeValueEmbedded = "embedded"

	// defaultPartyUDPRelayRemoteRoomBase 远离小整数房间号（另一台服在用的 18/19/20/22/61）。
	defaultPartyUDPRelayRemoteRoomBase = 100000
)

// 密钥状态（只用于日志；**不含密钥内容**）。
const (
	partyUDPRelayRemoteSecretConfigured = "configured"
	partyUDPRelayRemoteSecretMissing    = "missing"
	partyUDPRelayRemoteSecretInvalid    = "invalid"
)

// 具名事件（全小写连字符，判读用；与内嵌中继的 `party UDP relay ...` 日志并存不冲突）。
const (
	// partyUDPRelayRemoteEventConnected：控制通道认证成功（含 endpoint/scope/public_ip/room_base）。
	partyUDPRelayRemoteEventConnected = "party-udp-relay-remote-connected"
	// partyUDPRelayRemoteEventSyncOK：拿到端口矩阵（含 room_id/member_count/legs/端口区间）。
	partyUDPRelayRemoteEventSyncOK = "party-udp-relay-remote-sync-ok"
	// partyUDPRelayRemoteEventUnavailable：当前不可用 ⇒ 调用方**整份回退直连名单**
	// （reason 取下面的 partyUDPRelayRemoteReason* 常量）。
	partyUDPRelayRemoteEventUnavailable = "party-udp-relay-remote-unavailable"
	// partyUDPRelayRemoteEventChannelLost：已建立的控制通道断开（会按退避自动重连）。
	partyUDPRelayRemoteEventChannelLost = "party-udp-relay-remote-control-channel-lost"
	// partyUDPRelayRemoteEventRoomClosed：房间租约已交还（CLOSE）。
	partyUDPRelayRemoteEventRoomClosed = "party-udp-relay-remote-room-closed"
	// partyUDPRelayRemoteEventEmbeddedFallback：请求 remote 但起不来，**回退 embedded**
	// （绝不静默；reason 与 unavailable 同一套词）。
	partyUDPRelayRemoteEventEmbeddedFallback = "party-udp-relay-remote-embedded-fallback"
	// partyUDPRelayRemoteEventEmbeddedSelected：显式请求 embedded（A/B 档）。
	partyUDPRelayRemoteEventEmbeddedSelected = "party-udp-relay-remote-embedded-selected"
)

// 不可用/回退原因（同时进 unavailable 与 embedded-fallback 两条事件）。
const (
	partyUDPRelayRemoteReasonModeEmbedded     = "mode_embedded"
	partyUDPRelayRemoteReasonRelayDisabled    = "relay_disabled"
	partyUDPRelayRemoteReasonEndpointInvalid  = "endpoint_invalid"
	partyUDPRelayRemoteReasonSecretMissing    = "secret_missing"
	partyUDPRelayRemoteReasonSecretInvalid    = "secret_invalid"
	partyUDPRelayRemoteReasonPublicIPInvalid  = "public_ip_invalid"
	partyUDPRelayRemoteReasonConnectFailed    = "connect_failed"
	partyUDPRelayRemoteReasonHelloRejected    = "hello_rejected"
	partyUDPRelayRemoteReasonNotConnected     = "control_channel_not_connected"
	partyUDPRelayRemoteReasonSendFailed       = "send_failed"
	partyUDPRelayRemoteReasonSyncTimeout      = "sync_timeout"
	partyUDPRelayRemoteReasonSyncRejected     = "sync_rejected"
	partyUDPRelayRemoteReasonChannelLost      = "control_channel_lost"
	partyUDPRelayRemoteReasonRoomIDOutOfRange = "room_id_out_of_range"
)

// wire 协议常量（逐字节对照 PartyRelayProtocol.cs）。
const (
	partyRelayOpHello    = 0x01
	partyRelayOpSync     = 0x02
	partyRelayOpClose    = 0x03
	partyRelayOpHelloOK  = 0x81
	partyRelayOpSyncOK   = 0x82
	partyRelayOpSyncFail = 0x83
	partyRelayOpCloseOK  = 0x84

	partyRelayVersion          = 2
	partyRelayScopeParty       = 0
	partyRelaySyncModeMembers  = 0
	partyRelayMaxFrameBytes    = 8 * 1024
	partyRelayMaxMembers       = 8
	partyRelaySyncHeaderBytes  = 1 + 4 + 1 + 2
	partyRelaySecretHexLength  = 64
	partyRelaySecretByteLength = 32
	partyRelayFrameHeaderBytes = 3
	// partyRelayHelloMACContext 是厂商写死的 HMAC 上下文串（只升 version 字节，上下文串不变）。
	partyRelayHelloMACContext = "A21RELAY-HELLO/v1"
)

// 节拍（厂商常量）：租约 30s、续租 10s、SYNC 超时 2s、重连退避 250ms→5s、稳定 30s 后重置。
const (
	partyRelaySyncTimeout          = 2 * time.Second
	partyRelayConnectTimeout       = 2 * time.Second
	partyRelayWriteTimeout         = 2 * time.Second
	partyRelayRefreshEvery         = 10 * time.Second
	partyRelayLeaseWindow          = 30 * time.Second
	partyRelayMinReconnectDelay    = 250 * time.Millisecond
	partyRelayMaxReconnectDelay    = 5 * time.Second
	partyRelayStableConnectionTime = 30 * time.Second
	// partyRelayRemoteFreshWindow 是本文件头"偏离 ①"的窗口：同一房间、同一成员集合时，
	// 矩阵在这个窗口内直接复用，不再走 SYNC（默认 5s，小于 10s 续租周期 ⇒ 租约仍会被刷新）。
	partyRelayRemoteFreshWindow = 5 * time.Second
	// partyRelayRemoteReasonThrottle 是同一 reason 的事件限流窗口（避免刷屏）。
	partyRelayRemoteReasonThrottle = 10 * time.Second
)

// partyUDPRelayMode 是 DNFBRIDGE_PARTY_UDP_RELAY_MODE 的归一化取值。
//
// **零值必须是 embedded（= 改动前行为）**：本仓所有不经过 optionsFromConfig 的构造路径
// （测试与探针里的 Service 字面量）直接塞 `s.partyUDPRelay` 并调用组队广播路径，零值必须
// 继续走内嵌中继；线上默认 remote 只在 optionsFromConfig 里给出（与 joustEventDisabled /
// storyDigestDisabled 的"零值即现状"约定一致）。
type partyUDPRelayMode int

const (
	// partyUDPRelayModeEmbedded 是零值：我们自建的内嵌中继（每对方向一条腿，自己 bind 端口）。
	partyUDPRelayModeEmbedded partyUDPRelayMode = iota
	// partyUDPRelayModeRemote 是线上默认：厂商架构——注册到独立的 a21-relay，本进程不绑中继口。
	partyUDPRelayModeRemote
)

func (mode partyUDPRelayMode) String() string {
	if mode == partyUDPRelayModeRemote {
		return partyUDPRelayModeValueRemote
	}
	return partyUDPRelayModeValueEmbedded
}

// readPartyUDPRelayMode 解析 DNFBRIDGE_PARTY_UDP_RELAY_MODE：只有显式 `embedded` 回退，
// 其余（未设置/空/非法）一律 remote —— 与线上默认一致。
func readPartyUDPRelayMode(value string) partyUDPRelayMode {
	if strings.EqualFold(strings.TrimSpace(value), partyUDPRelayModeValueEmbedded) {
		return partyUDPRelayModeEmbedded
	}
	return partyUDPRelayModeRemote
}

// partyUDPRelayRemoteSecretState 归一化密钥状态（只回三个词，**不回显内容**）。
func partyUDPRelayRemoteSecretState(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return partyUDPRelayRemoteSecretMissing
	}
	if len(raw) != partyRelaySecretHexLength {
		return partyUDPRelayRemoteSecretInvalid
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != partyRelaySecretByteLength {
		return partyUDPRelayRemoteSecretInvalid
	}
	return partyUDPRelayRemoteSecretConfigured
}

// parsePartyUDPRelayRemoteSecret 解析 64 hex ⇒ 32 字节。失败返回 nil（调用方回退 embedded）。
// 与厂商 PartyRelayProtocol.TryParseSecret 同口径：长度必须恰好 64 个 hex 字符。
func parsePartyUDPRelayRemoteSecret(value string) []byte {
	if partyUDPRelayRemoteSecretState(value) != partyUDPRelayRemoteSecretConfigured {
		return nil
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != partyRelaySecretByteLength {
		return nil
	}
	return decoded
}

// readPartyUDPRelayRemoteSecret 一次给出"解析后的密钥 + 状态词"。
// 状态词只用于启动汇总行与具名事件，**永远不含密钥内容**。
func readPartyUDPRelayRemoteSecret(value string) ([]byte, string) {
	state := partyUDPRelayRemoteSecretState(value)
	if state != partyUDPRelayRemoteSecretConfigured {
		return nil, state
	}
	return parsePartyUDPRelayRemoteSecret(value), state
}

// partyUDPRelayRemoteRoomBase 解析 DNFBRIDGE_PARTY_UDP_RELAY_ROOM_BASE：必须 >0 且
// BASE+65535 不越过 u32（partyID 是 u16）。非法/未设置回默认。
func partyUDPRelayRemoteRoomBase(value string) int {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return defaultPartyUDPRelayRemoteRoomBase
	}
	parsed := 0
	for index := 0; index < len(raw); index++ {
		if raw[index] < '0' || raw[index] > '9' {
			return defaultPartyUDPRelayRemoteRoomBase
		}
		parsed = parsed*10 + int(raw[index]-'0')
		if parsed > 1<<31 {
			return defaultPartyUDPRelayRemoteRoomBase
		}
	}
	if parsed <= 0 || parsed+int(^uint16(0)) < parsed {
		return defaultPartyUDPRelayRemoteRoomBase
	}
	if uint64(parsed)+uint64(^uint16(0)) > uint64(^uint32(0)) {
		return defaultPartyUDPRelayRemoteRoomBase
	}
	return parsed
}

// partyRelayHelloMAC 是厂商 ComputeHelloMac 的等价实现。
func partyRelayHelloMAC(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(partyRelayHelloMACContext))
	return mac.Sum(nil)
}

// buildPartyRelayHelloPayload 组 HELLO：u8 version=2 + 32B HMAC。
func buildPartyRelayHelloPayload(secret []byte) []byte {
	sum := partyRelayHelloMAC(secret)
	payload := make([]byte, 1+len(sum))
	payload[0] = partyRelayVersion
	copy(payload[1:], sum)
	return payload
}

// normalizePartyRelayRemoteMembers 归一化成员键：去掉 0 与重复项并升序（厂商 NormalizeMembers）。
// 中继侧与游戏服侧用同一套规则，保证"同一成员集合"两端的幂等判定一致。
func normalizePartyRelayRemoteMembers(keys []uint16) []uint16 {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[uint16]struct{}, len(keys))
	result := make([]uint16, 0, len(keys))
	for _, key := range keys {
		if key == 0 {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

// buildPartyRelaySyncPayload 组 SYNC(mode 0)：u8 scope + u32 BE roomId + u8 mode
// + u16 BE count + count×u16 BE memberKey。**全部大端**。
func buildPartyRelaySyncPayload(roomID uint32, members []uint16) []byte {
	payload := make([]byte, partyRelaySyncHeaderBytes+len(members)*2)
	payload[0] = partyRelayScopeParty
	binary.BigEndian.PutUint32(payload[1:5], roomID)
	payload[5] = partyRelaySyncModeMembers
	binary.BigEndian.PutUint16(payload[6:8], uint16(len(members)))
	offset := partyRelaySyncHeaderBytes
	for _, member := range members {
		binary.BigEndian.PutUint16(payload[offset:offset+2], member)
		offset += 2
	}
	return payload
}

// buildPartyRelayClosePayload 组 CLOSE：u8 scope + u32 BE roomId。
func buildPartyRelayClosePayload(roomID uint32) []byte {
	payload := make([]byte, 5)
	payload[0] = partyRelayScopeParty
	binary.BigEndian.PutUint32(payload[1:5], roomID)
	return payload
}

// encodePartyRelayFrame 组一帧：u8 opcode + u16 BE len + payload。
func encodePartyRelayFrame(opcode byte, payload []byte) []byte {
	frame := make([]byte, partyRelayFrameHeaderBytes+len(payload))
	frame[0] = opcode
	binary.BigEndian.PutUint16(frame[1:3], uint16(len(payload)))
	copy(frame[partyRelayFrameHeaderBytes:], payload)
	return frame
}

// readPartyRelayFrame 从流上读一帧。帧长越界属协议违规，返回错误让调用方断链重连。
func readPartyRelayFrame(reader io.Reader) (byte, []byte, error) {
	header := make([]byte, partyRelayFrameHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := int(binary.BigEndian.Uint16(header[1:3]))
	if length > partyRelayMaxFrameBytes {
		return header[0], nil, fmt.Errorf("relay frame length %d exceeds %d", length, partyRelayMaxFrameBytes)
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			return header[0], nil, err
		}
	}
	return header[0], payload, nil
}

// partyUDPRelayRemoteLegKey 是中继端口矩阵的键：**有向腿 (owner→peer)**。
// 客户端行里的端口就是"收包者 → 该队友"这条腿的口（厂商 PartyUdpRelay.GetPort(roomId, owner, peer)）。
type partyUDPRelayRemoteLegKey struct {
	owner uint16
	peer  uint16
}

// decodePartyRelaySyncOK 解析 SYNC_OK：u32 BE roomId + u16 BE count + count×(u16 owner,u16 peer,u16 port)。
// 长度与 count 必须严格自洽（厂商 TryReadSyncOkPayload 同口径），否则整帧丢弃。
func decodePartyRelaySyncOK(payload []byte) (uint32, map[partyUDPRelayRemoteLegKey]uint16, bool) {
	if len(payload) < 6 {
		return 0, nil, false
	}
	roomID := binary.BigEndian.Uint32(payload[0:4])
	count := int(binary.BigEndian.Uint16(payload[4:6]))
	if len(payload) != 6+count*6 {
		return 0, nil, false
	}
	ports := make(map[partyUDPRelayRemoteLegKey]uint16, count)
	for index := 0; index < count; index++ {
		offset := 6 + index*6
		owner := binary.BigEndian.Uint16(payload[offset : offset+2])
		peer := binary.BigEndian.Uint16(payload[offset+2 : offset+4])
		port := binary.BigEndian.Uint16(payload[offset+4 : offset+6])
		if owner == 0 || peer == 0 || owner == peer || port == 0 {
			return 0, nil, false
		}
		ports[partyUDPRelayRemoteLegKey{owner: owner, peer: peer}] = port
	}
	return roomID, ports, true
}

// decodePartyRelaySyncFail 解析 SYNC_FAIL：u32 BE roomId + u8 reason。
func decodePartyRelaySyncFail(payload []byte) (uint32, byte, bool) {
	if len(payload) != 5 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(payload[0:4]), payload[4], true
}

// decodePartyRelayCloseOK 解析 CLOSE_OK：u32 BE roomId。
func decodePartyRelayCloseOK(payload []byte) (uint32, bool) {
	if len(payload) != 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(payload[0:4]), true
}

// partyUDPRelayRemoteMatrix 是一个房间当前可用的端口矩阵（只读快照）。
type partyUDPRelayRemoteMatrix struct {
	generation uint64
	ports      map[partyUDPRelayRemoteLegKey]uint16
	stampedAt  time.Time
}

// partyUDPRelayRemotePending 是一次前台 SYNC 的等待位（与厂商 PendingSync 同构）。
type partyUDPRelayRemotePending struct {
	members []uint16
	done    chan partyUDPRelayRemoteSyncResult
	once    sync.Once
}

type partyUDPRelayRemoteSyncResult struct {
	ok     bool
	reason string
}

func (pending *partyUDPRelayRemotePending) deliver(result partyUDPRelayRemoteSyncResult) {
	if pending == nil {
		return
	}
	pending.once.Do(func() {
		select {
		case pending.done <- result:
		default:
		}
	})
}

// partyUDPRelayRemoteSettings 是 remote 客户端的全部构造参数。
// 节拍全部可注入：线上用厂商常量（本文件顶部的 partyRelay* 常量），单测用毫秒级。
type partyUDPRelayRemoteSettings struct {
	endpoint string
	host     string
	port     int
	publicIP [4]byte
	secret   []byte
	roomBase int
	// onEvent 记具名事件（线上是 Service.logWarn，单测是收集器）。**绝不传密钥**。
	onEvent func(name string, args ...any)

	connectTimeout time.Duration
	syncTimeout    time.Duration
	writeTimeout   time.Duration
	refreshEvery   time.Duration
	freshWindow    time.Duration
	minBackoff     time.Duration
	maxBackoff     time.Duration
	stableAfter    time.Duration
}

// currentPartyUDPRelayRemote 是控制通道客户端。
//
// 并发模型：一条连接由 runChannel 独占（readLoop 读、renewLoop 续租），写帧统一走 sendMu；
// 组队广播路径只调 Sync/Endpoint（前者最多等 syncTimeout，后者纯内存查表）。
// 所有 socket 异常都被转成"当前不可用"，**不向调用方抛异常**。
type currentPartyUDPRelayRemote struct {
	host     string
	port     int
	endpoint string
	publicIP [4]byte
	secret   []byte
	roomBase int
	onEvent  func(name string, args ...any)

	connectTimeout time.Duration
	syncTimeout    time.Duration
	writeTimeout   time.Duration
	refreshEvery   time.Duration
	freshWindow    time.Duration
	minBackoff     time.Duration
	maxBackoff     time.Duration
	stableAfter    time.Duration

	sendMu sync.Mutex

	mu            sync.Mutex
	conn          net.Conn
	connected     bool
	closed        bool
	sessionSeq    uint64
	sessionID     uint64
	matrices      map[uint32]*partyUDPRelayRemoteMatrix
	roomMembers   map[uint32][]uint16
	roomEpoch     map[uint32]uint64
	closingRooms  map[uint32]struct{}
	pending       map[uint32]*partyUDPRelayRemotePending
	renewals      map[uint32][]uint16
	resyncPending map[uint32]struct{}
	degradedUntil time.Time
	// unavailableReason/unavailableAt 是 unavailable 事件的限流状态（同一 reason 10s 一条）。
	unavailableReason string
	unavailableAt     time.Time
	// loggedUnknownOpcodes 保证每种未知帧只记一次。
	loggedUnknownOpcodes map[byte]bool

	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	// 计数（判读与单测用）。
	connectCount  atomic.Uint64
	syncOKCount   atomic.Uint64
	syncFailCount atomic.Uint64
	syncTimeoutCt atomic.Uint64
	closeSentCt   atomic.Uint64
}

// newCurrentPartyUDPRelayRemote 建客户端并立刻开始连控制通道。
// 参数非法**不 panic**：返回 error，由调用方回退 embedded 并记一条具名事件。
func newCurrentPartyUDPRelayRemote(settings partyUDPRelayRemoteSettings) (*currentPartyUDPRelayRemote, error) {
	if settings.host == "" || settings.port <= 0 || settings.port > 65535 {
		return nil, fmt.Errorf("party udp relay remote endpoint is not host:port")
	}
	if len(settings.secret) != partyRelaySecretByteLength {
		// 只说长度，不回显内容。
		return nil, fmt.Errorf("party udp relay remote secret must be %d bytes", partyRelaySecretByteLength)
	}
	roomBase := settings.roomBase
	if roomBase <= 0 {
		roomBase = defaultPartyUDPRelayRemoteRoomBase
	}
	if uint64(roomBase)+uint64(^uint16(0)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("party udp relay remote room base %d overflows u32 with party ids", roomBase)
	}
	relay := &currentPartyUDPRelayRemote{
		host:                 settings.host,
		port:                 settings.port,
		endpoint:             settings.endpoint,
		publicIP:             settings.publicIP,
		secret:               append([]byte(nil), settings.secret...),
		roomBase:             roomBase,
		onEvent:              settings.onEvent,
		connectTimeout:       settings.connectTimeout,
		syncTimeout:          settings.syncTimeout,
		writeTimeout:         settings.writeTimeout,
		refreshEvery:         settings.refreshEvery,
		freshWindow:          settings.freshWindow,
		minBackoff:           settings.minBackoff,
		maxBackoff:           settings.maxBackoff,
		stableAfter:          settings.stableAfter,
		matrices:             make(map[uint32]*partyUDPRelayRemoteMatrix),
		roomMembers:          make(map[uint32][]uint16),
		pending:              make(map[uint32]*partyUDPRelayRemotePending),
		renewals:             make(map[uint32][]uint16),
		resyncPending:        make(map[uint32]struct{}),
		loggedUnknownOpcodes: make(map[byte]bool),
		done:                 make(chan struct{}),
	}
	if relay.endpoint == "" {
		relay.endpoint = net.JoinHostPort(settings.host, fmt.Sprintf("%d", settings.port))
	}
	if relay.connectTimeout <= 0 {
		relay.connectTimeout = partyRelayConnectTimeout
	}
	if relay.syncTimeout <= 0 {
		relay.syncTimeout = partyRelaySyncTimeout
	}
	if relay.writeTimeout <= 0 {
		relay.writeTimeout = partyRelayWriteTimeout
	}
	if relay.refreshEvery <= 0 {
		relay.refreshEvery = partyRelayRefreshEvery
	}
	if relay.freshWindow <= 0 {
		relay.freshWindow = partyRelayRemoteFreshWindow
	}
	if relay.minBackoff <= 0 {
		relay.minBackoff = partyRelayMinReconnectDelay
	}
	if relay.maxBackoff < relay.minBackoff {
		relay.maxBackoff = partyRelayMaxReconnectDelay
	}
	if relay.stableAfter <= 0 {
		relay.stableAfter = partyRelayStableConnectionTime
	}
	relay.wg.Add(1)
	go relay.runChannel()
	return relay, nil
}

// roomID 把我们的 partyID 映射进**我们自己的房间号命名空间**（见文件头）。
func (r *currentPartyUDPRelayRemote) roomID(partyID uint16) uint32 {
	if r == nil {
		return 0
	}
	return uint32(r.roomBase) + uint32(partyID)
}

// baseRoomID / publicIPLabel / connectedState / cachedRoomCount 供启动判读与单测使用。
func (r *currentPartyUDPRelayRemote) baseRoomID() int {
	if r == nil {
		return 0
	}
	return r.roomBase
}

func (r *currentPartyUDPRelayRemote) publicIPLabel() string {
	if r == nil {
		return ""
	}
	return formatCurrentPartyUDPRelayIPv4(r.publicIP)
}

func (r *currentPartyUDPRelayRemote) connectedState() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected
}

func (r *currentPartyUDPRelayRemote) cachedRoomCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.matrices)
}

// Enabled 与内嵌中继同形：remote 档恒 true（"可用与否"由 Sync 的返回值表达——
// 控制通道不在位时 Sync 返回 false，调用方据此整份回退直连名单，与厂商语义一致）。
func (r *currentPartyUDPRelayRemote) Enabled() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed
}

// Sync 与内嵌中继同形：把"房间成员集合"换成端口矩阵并缓存；拿不到就返回 false。
// partyID=0、成员不足 2 人时**不注册**（厂商 TrySyncRoom 同口径）。
func (r *currentPartyUDPRelayRemote) Sync(partyID uint16, bindings []currentPartyUDPRelayBinding) bool {
	if r == nil || !r.Enabled() || partyID == 0 {
		return false
	}
	members := make([]uint16, 0, len(bindings))
	for _, binding := range bindings {
		members = append(members, binding.characterID)
	}
	members = normalizePartyRelayRemoteMembers(members)
	if len(members) < 2 || len(members) > partyRelayMaxMembers {
		// 单人不成房：把可能存在的旧房间交还掉，避免留一个没人用的租约。
		r.closeRoom(r.roomID(partyID), "below_two_members")
		return false
	}
	return r.syncRoom(r.roomID(partyID), members)
}

// Endpoint 与内嵌中继同形：返回 (中继公网IP, 该有向腿的中继端口)。
// 端口来自 SYNC_OK 矩阵，键是 (收包者 → 队友) —— 与厂商 GetPort(roomId, owner, peer) 一致。
func (r *currentPartyUDPRelayRemote) Endpoint(partyID, ownerID, peerID uint16) ([4]byte, uint16, bool) {
	if r == nil || partyID == 0 || ownerID == 0 || peerID == 0 || ownerID == peerID {
		return [4]byte{}, 0, false
	}
	roomID := r.roomID(partyID)
	r.mu.Lock()
	matrix := r.matrices[roomID]
	var port uint16
	var ok bool
	if matrix != nil {
		port, ok = matrix.ports[partyUDPRelayRemoteLegKey{owner: ownerID, peer: peerID}]
	}
	closed := r.closed
	r.mu.Unlock()
	if closed || !ok {
		return [4]byte{}, 0, false
	}
	return r.publicIP, port, true
}

// CloseParty 交还一个房间的租约（队伍解散/换队长时由 closeRuntimePartyUDPRelay 调）。
func (r *currentPartyUDPRelayRemote) CloseParty(partyID uint16) {
	if r == nil || partyID == 0 {
		return
	}
	r.closeRoom(r.roomID(partyID), "close_party")
}

// Close 停掉控制通道，并尽力为所有活跃房间发 CLOSE（不等 30s 租约自然到期）。
// 与内嵌中继同形，可重复调用。
func (r *currentPartyUDPRelayRemote) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		rooms := make([]uint32, 0, len(r.roomMembers))
		for roomID := range r.roomMembers {
			rooms = append(rooms, roomID)
		}
		r.closed = true
		r.mu.Unlock()

		for _, roomID := range rooms {
			_ = r.sendFrame(partyRelayOpClose, buildPartyRelayClosePayload(roomID))
		}

		close(r.done)
		r.mu.Lock()
		conn := r.conn
		r.conn = nil
		r.connected = false
		r.matrices = make(map[uint32]*partyUDPRelayRemoteMatrix)
		r.roomMembers = make(map[uint32][]uint16)
		for roomID, pending := range r.pending {
			delete(r.pending, roomID)
			pending.deliver(partyUDPRelayRemoteSyncResult{reason: partyUDPRelayRemoteReasonChannelLost})
		}
		r.renewals = make(map[uint32][]uint16)
		r.resyncPending = make(map[uint32]struct{})
		r.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		r.wg.Wait()
	})
	return nil
}

// ---------------------------------------------------------------------------
// 连接与收发
// ---------------------------------------------------------------------------

func (r *currentPartyUDPRelayRemote) isClosed() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// runChannel 是控制通道的常驻循环：认证成功 ⇒ 服务；断开 ⇒ 按退避重连（稳定 ≥30s 后重置）。
func (r *currentPartyUDPRelayRemote) runChannel() {
	defer r.wg.Done()
	delay := r.minBackoff
	for {
		if r.isClosed() {
			return
		}
		stable := r.serveChannel()
		if r.isClosed() {
			return
		}
		if stable {
			delay = r.minBackoff
		} else {
			delay *= 2
			if delay > r.maxBackoff {
				delay = r.maxBackoff
			}
		}
		select {
		case <-r.done:
			return
		case <-time.After(delay):
		}
	}
}

// serveChannel 建一条连接并服务到断开。返回 true 表示本次连接曾稳定 ≥ stableAfter
// （厂商的退避重置条件）。
func (r *currentPartyUDPRelayRemote) serveChannel() bool {
	dialer := &net.Dialer{Timeout: r.connectTimeout}
	conn, err := dialer.Dial("tcp", net.JoinHostPort(r.host, fmt.Sprintf("%d", r.port)))
	if err != nil {
		r.noteUnavailable(partyUDPRelayRemoteReasonConnectFailed, "error", err.Error())
		return false
	}
	startedAt := time.Now()
	sessionID := uint64(0)
	defer func() {
		_ = conn.Close()
		r.handleChannelLoss(sessionID)
	}()

	// HELLO：认证帧与回包都按 connectTimeout 设截止，避免半开连接把重连循环卡住。
	if err := conn.SetDeadline(time.Now().Add(r.connectTimeout)); err != nil {
		return false
	}
	if err := writePartyRelayFrame(conn, partyRelayOpHello, buildPartyRelayHelloPayload(r.secret), r.writeTimeout); err != nil {
		r.noteUnavailable(partyUDPRelayRemoteReasonConnectFailed, "error", err.Error())
		return false
	}
	opcode, _, err := readPartyRelayFrame(conn)
	if err != nil || opcode != partyRelayOpHelloOK {
		r.noteUnavailable(partyUDPRelayRemoteReasonHelloRejected)
		return false
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return false
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return false
	}
	r.conn = conn
	r.connected = true
	r.sessionSeq++
	sessionID = r.sessionSeq
	r.sessionID = sessionID
	r.closingRooms = nil
	r.degradedUntil = time.Time{}
	r.unavailableReason = ""
	r.mu.Unlock()
	r.connectCount.Add(1)
	r.event(partyUDPRelayRemoteEventConnected,
		"endpoint", r.endpoint,
		"scope", "party",
		"public_ip", r.publicIPLabel(),
		"room_base", r.roomBase,
		"secret", partyUDPRelayRemoteSecretConfigured)

	// 重连成功后**立即**补齐所有活跃房间，不等下一个续租周期。
	r.resyncActiveRooms()

	stop := make(chan struct{})
	finished := make(chan struct{}, 2)
	go func() { r.readLoop(conn); finished <- struct{}{} }()
	go func() { r.renewLoop(stop); finished <- struct{}{} }()
	<-finished
	close(stop)
	_ = conn.Close()
	<-finished
	return time.Since(startedAt) >= r.stableAfter
}

// readLoop 读帧并派发。单帧处理异常只丢该帧（控制通道的存活优先于单条消息）。
func (r *currentPartyUDPRelayRemote) readLoop(conn net.Conn) {
	for {
		opcode, payload, err := readPartyRelayFrame(conn)
		if err != nil {
			return
		}
		r.dispatch(opcode, payload)
	}
}

// renewLoop 每 refreshEvery 续租所有活跃房间（只发 SYNC 刷新租约，不建前台等待）。
func (r *currentPartyUDPRelayRemote) renewLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(r.refreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-r.done:
			return
		case <-ticker.C:
		}
		if !r.connectedState() {
			return
		}
		for _, room := range r.trackedRooms() {
			r.sendTrackedRoom(room, false)
		}
	}
}

type partyUDPRelayRemoteTrackedRoom struct {
	roomID  uint32
	members []uint16
	epoch   uint64
}

func (r *currentPartyUDPRelayRemote) trackedRooms() []partyUDPRelayRemoteTrackedRoom {
	r.mu.Lock()
	defer r.mu.Unlock()
	rooms := make([]partyUDPRelayRemoteTrackedRoom, 0, len(r.roomMembers))
	for roomID, members := range r.roomMembers {
		rooms = append(rooms, partyUDPRelayRemoteTrackedRoom{
			roomID:  roomID,
			members: append([]uint16(nil), members...),
			epoch:   r.roomEpoch[roomID],
		})
	}
	sort.Slice(rooms, func(left, right int) bool { return rooms[left].roomID < rooms[right].roomID })
	return rooms
}

// resyncActiveRooms 重连后立即补齐（并把它们标成"重连补齐中"，让回包走续租那条发布路径）。
func (r *currentPartyUDPRelayRemote) resyncActiveRooms() {
	rooms := r.trackedRooms()
	if len(rooms) == 0 {
		return
	}
	for _, room := range rooms {
		r.sendTrackedRoom(room, true)
	}
}

func (r *currentPartyUDPRelayRemote) dispatch(opcode byte, payload []byte) {
	switch opcode {
	case partyRelayOpSyncOK:
		roomID, ports, ok := decodePartyRelaySyncOK(payload)
		if !ok {
			r.noteUnknownFrame(opcode, "malformed_sync_ok")
			return
		}
		r.completeSync(roomID, ports, partyUDPRelayRemoteSyncResult{ok: true})
	case partyRelayOpSyncFail:
		roomID, _, ok := decodePartyRelaySyncFail(payload)
		if !ok {
			r.noteUnknownFrame(opcode, "malformed_sync_fail")
			return
		}
		r.syncFailCount.Add(1)
		r.completeSync(roomID, nil, partyUDPRelayRemoteSyncResult{reason: partyUDPRelayRemoteReasonSyncRejected})
	case partyRelayOpCloseOK:
		if roomID, ok := decodePartyRelayCloseOK(payload); ok {
			r.mu.Lock()
			delete(r.closingRooms, roomID)
			r.mu.Unlock()
		}
	default:
		// 未知帧忽略，**不因此断链**（厂商同口径）。
		r.noteUnknownFrame(opcode, "unknown_opcode")
	}
}

// noteUnknownFrame 每种异常帧只记一条（不刷屏、不断链）。
func (r *currentPartyUDPRelayRemote) noteUnknownFrame(opcode byte, reason string) {
	r.mu.Lock()
	if r.loggedUnknownOpcodes[opcode] {
		r.mu.Unlock()
		return
	}
	r.loggedUnknownOpcodes[opcode] = true
	r.mu.Unlock()
	r.event("party-udp-relay-remote-frame-ignored", "opcode", int(opcode), "reason", reason)
}

// completeSync 处理一次 SYNC 回包（前台请求 / 续租 / 重连补齐三种来源）。
// 语义与厂商 RemotePartyUdpRelay.CompleteSync 对齐：没有前台请求时，只有"重连补齐中"或
// "续租内容与已跟踪房间一致"才会重新发布矩阵；被拒时把房间从活跃集合里摘掉。
func (r *currentPartyUDPRelayRemote) completeSync(roomID uint32, ports map[partyUDPRelayRemoteLegKey]uint16, result partyUDPRelayRemoteSyncResult) {
	r.mu.Lock()
	if !r.connected {
		// 旧连接迟到的回包：丢弃，不能让上一次会话的端口矩阵复活。
		r.mu.Unlock()
		return
	}
	if _, closing := r.closingRooms[roomID]; closing {
		r.mu.Unlock()
		return
	}
	pending := r.pending[roomID]
	if pending != nil {
		delete(r.pending, roomID)
	}
	if pending == nil && result.ok {
		tracked, trackedOK := r.roomMembers[roomID]
		_, resyncing := r.resyncPending[roomID]
		renewal, renewing := r.renewals[roomID]
		if trackedOK && (resyncing || (renewing && equalPartyRelayRemoteMembers(tracked, renewal))) {
			delete(r.renewals, roomID)
			delete(r.resyncPending, roomID)
			r.publishMatrixLocked(roomID, tracked, ports)
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		return
	}
	delete(r.renewals, roomID)
	if result.ok && pending != nil {
		r.publishMatrixLocked(roomID, pending.members, ports)
	} else if !result.ok {
		delete(r.matrices, roomID)
		delete(r.roomMembers, roomID)
		r.resyncPendingDelete(roomID)
	}
	r.mu.Unlock()
	if pending != nil {
		pending.deliver(result)
	}
}

// resyncPendingDelete 在持锁状态下清掉"重连补齐中"标记。
func (r *currentPartyUDPRelayRemote) resyncPendingDelete(roomID uint32) {
	delete(r.resyncPending, roomID)
}

// publishMatrixLocked 发布一个房间的端口矩阵（调用方必须持 r.mu）。
func (r *currentPartyUDPRelayRemote) publishMatrixLocked(roomID uint32, members []uint16, ports map[partyUDPRelayRemoteLegKey]uint16) {
	previous := r.matrices[roomID]
	// 顺序要紧：先与**发布前**的成员集合/端口矩阵比较，再覆盖（否则永远"相等"）。
	sameMembers := previous != nil && equalPartyRelayRemoteMembers(r.roomMembers[roomID], members)
	samePorts := previous != nil && equalPartyRelayRemotePorts(previous.ports, ports)
	matrix := &partyUDPRelayRemoteMatrix{
		generation: r.syncOKCount.Add(1),
		ports:      ports,
		stampedAt:  time.Now(),
	}
	r.matrices[roomID] = matrix
	r.roomMembers[roomID] = append([]uint16(nil), members...)
	delete(r.resyncPending, roomID)

	// sync-ok 事件只在"矩阵真的变了"（首次 / 成员变了 / 端口变了）时记一条，
	// 否则每 10s 的续租回包会把日志刷满。
	if sameMembers && samePorts {
		return
	}
	portMin, portMax := partyRelayRemotePortRange(ports)
	r.event(partyUDPRelayRemoteEventSyncOK,
		"room_id", roomID,
		"member_count", len(members),
		"legs", len(ports),
		"port_min", portMin,
		"port_max", portMax,
		"room_base", r.roomBase,
		"public_ip", r.publicIPLabel())
}

func equalPartyRelayRemotePorts(left, right map[partyUDPRelayRemoteLegKey]uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for key, port := range left {
		if other, ok := right[key]; !ok || other != port {
			return false
		}
	}
	return true
}

func partyRelayRemotePortRange(ports map[partyUDPRelayRemoteLegKey]uint16) (int, int) {
	if len(ports) == 0 {
		return 0, 0
	}
	minimum, maximum := 0, 0
	first := true
	for _, port := range ports {
		value := int(port)
		if first {
			minimum, maximum, first = value, value, false
			continue
		}
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}
	return minimum, maximum
}

func equalPartyRelayRemoteMembers(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// syncRoom 是前台 SYNC：命缓存 ⇒ 立即成功；否则发 SYNC 并最多等 syncTimeout。
// 任何失败路径都会记一条 `party-udp-relay-remote-unavailable`（带 fallback=direct_peer_list）。
func (r *currentPartyUDPRelayRemote) syncRoom(roomID uint32, members []uint16) bool {
	if r == nil || r.isClosed() || roomID == 0 {
		return false
	}
	if len(members) < 2 {
		r.closeRoom(roomID, "below_two_members")
		return false
	}
	now := time.Now()
	r.sendMu.Lock()
	r.mu.Lock()
	if matrix := r.matrices[roomID]; matrix != nil &&
		equalPartyRelayRemoteMembers(r.roomMembers[roomID], members) &&
		now.Sub(matrix.stampedAt) < r.freshWindow {
		r.mu.Unlock()
		r.sendMu.Unlock()
		return true
	}
	if now.Before(r.degradedUntil) {
		// 刚超时过：窗口内直接回"不可用"，不重复阻塞组队广播路径。
		r.mu.Unlock()
		r.sendMu.Unlock()
		return false
	}
	if !r.connected || r.conn == nil {
		r.mu.Unlock()
		r.sendMu.Unlock()
		r.noteUnavailable(partyUDPRelayRemoteReasonNotConnected, "room_id", roomID)
		return false
	}
	_, closing := r.closingRooms[roomID]
	_, renewing := r.renewals[roomID]
	_, resyncing := r.resyncPending[roomID]
	if closing || r.pending[roomID] != nil || renewing || resyncing {
		// This protocol has no request ID. Never let an old reply consume a
		// replacement waiter; CLOSE_OK is the ordered retirement boundary.
		r.mu.Unlock()
		r.sendMu.Unlock()
		return false
	}
	pending := &partyUDPRelayRemotePending{
		members: append([]uint16(nil), members...),
		done:    make(chan partyUDPRelayRemoteSyncResult, 1),
	}
	r.pending[roomID] = pending
	r.mu.Unlock()

	sendErr := r.sendFrameLocked(partyRelayOpSync, buildPartyRelaySyncPayload(roomID, members))
	r.sendMu.Unlock()
	if err := sendErr; err != nil {
		r.removePending(roomID, pending)
		r.dropActiveRoomIfUnpublished(roomID, pending)
		r.noteUnavailable(partyUDPRelayRemoteReasonSendFailed, "room_id", roomID, "error", err.Error())
		return false
	}

	select {
	case result := <-pending.done:
		if result.ok {
			return true
		}
		r.dropActiveRoomIfUnpublished(roomID, pending)
		reason := result.reason
		if reason == "" {
			reason = partyUDPRelayRemoteReasonSyncRejected
		}
		r.noteUnavailable(reason, "room_id", roomID, "member_count", len(members))
		return false
	case <-time.After(r.syncTimeout):
		r.closeRoomExpected(roomID, "sync_timeout", pending)
		r.dropActiveRoomIfUnpublished(roomID, pending)
		r.mu.Lock()
		r.degradedUntil = time.Now().Add(r.syncTimeout)
		r.mu.Unlock()
		r.syncTimeoutCt.Add(1)
		r.noteUnavailable(partyUDPRelayRemoteReasonSyncTimeout,
			"room_id", roomID, "member_count", len(members), "timeout_ms", r.syncTimeout.Milliseconds())
		return false
	case <-r.done:
		r.removePending(roomID, pending)
		return false
	}
}

func (r *currentPartyUDPRelayRemote) removePending(roomID uint32, pending *partyUDPRelayRemotePending) {
	r.mu.Lock()
	if current := r.pending[roomID]; current == pending {
		delete(r.pending, roomID)
	}
	r.mu.Unlock()
}

// dropActiveRoomIfUnpublished 撤销一次失败请求建立的"期望活跃"状态：
// 只有当这个房间确实没有可用矩阵时才移除，避免把上一次成功的结果误删。
func (r *currentPartyUDPRelayRemote) dropActiveRoomIfUnpublished(roomID uint32, pending *partyUDPRelayRemotePending) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.pending[roomID]; current != nil && current != pending {
		return // 已有更新的请求在等，让它决定最终状态
	}
	if r.matrices[roomID] == nil {
		delete(r.roomMembers, roomID)
	}
}

// closeRoom 交还一个房间：清缓存 + 发 CLOSE（尽力而为）。
func (r *currentPartyUDPRelayRemote) closeRoom(roomID uint32, reason string) {
	r.closeRoomExpected(roomID, reason, nil)
}

func (r *currentPartyUDPRelayRemote) closeRoomExpected(roomID uint32, reason string, expected *partyUDPRelayRemotePending) {
	if r == nil || roomID == 0 {
		return
	}
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	r.mu.Lock()
	if expected != nil && r.pending[roomID] != expected {
		r.mu.Unlock()
		return
	}
	if _, closing := r.closingRooms[roomID]; closing {
		r.mu.Unlock()
		return
	}
	_, existed := r.roomMembers[roomID]
	if r.roomEpoch == nil {
		r.roomEpoch = make(map[uint32]uint64)
	}
	r.roomEpoch[roomID]++
	delete(r.matrices, roomID)
	delete(r.roomMembers, roomID)
	delete(r.renewals, roomID)
	delete(r.resyncPending, roomID)
	pending := r.pending[roomID]
	delete(r.pending, roomID)
	connected := r.connected
	closed := r.closed
	if connected && !closed && (existed || pending != nil) {
		if r.closingRooms == nil {
			r.closingRooms = make(map[uint32]struct{})
		}
		r.closingRooms[roomID] = struct{}{}
	}
	r.mu.Unlock()
	pending.deliver(partyUDPRelayRemoteSyncResult{reason: partyUDPRelayRemoteReasonChannelLost})
	if (!existed && pending == nil) || closed {
		return
	}
	if connected {
		if err := r.sendFrameLocked(partyRelayOpClose, buildPartyRelayClosePayload(roomID)); err == nil {
			r.closeSentCt.Add(1)
		}
	}
	r.event(partyUDPRelayRemoteEventRoomClosed, "room_id", roomID, "reason", reason, "room_base", r.roomBase)
}

// sendFrame 串行写一帧（写失败只代表"当前不可用"，异常不回传调用方）。
func (r *currentPartyUDPRelayRemote) sendFrame(opcode byte, payload []byte) error {
	if r == nil {
		return errors.New("relay remote is nil")
	}
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	return r.sendFrameLocked(opcode, payload)
}

// Caller holds sendMu from state validation through the complete frame write.
func (r *currentPartyUDPRelayRemote) sendFrameLocked(opcode byte, payload []byte) error {
	r.mu.Lock()
	conn := r.conn
	connected := r.connected
	r.mu.Unlock()
	if conn == nil || !connected {
		return errors.New("control channel is not connected")
	}
	err := writePartyRelayFrame(conn, opcode, payload, r.writeTimeout)
	if err != nil {
		// A partially written request has no reliable response boundary.
		// Closing this channel lets the reader retire all pending state.
		_ = conn.Close()
	}
	return err
}

// writePartyRelayFrame 一把写完一帧（持 sendMu ⇒ 多 goroutine 写不会交错）。
func writePartyRelayFrame(conn net.Conn, opcode byte, payload []byte, timeout time.Duration) error {
	if conn == nil {
		return errors.New("relay control channel is not connected")
	}
	frame := encodePartyRelayFrame(opcode, payload)
	if timeout > 0 {
		if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
	}
	for written := 0; written < len(frame); {
		count, err := conn.Write(frame[written:])
		if err != nil {
			return err
		}
		if count <= 0 {
			return io.ErrShortWrite
		}
		written += count
	}
	return nil
}

// handleChannelLoss 按会话号收尾：只失效"连接相关"的缓存（矩阵必须重新推导，
// 中继侧房间可能已被 30s 租约回收），保留"期望活跃房间"集合供重连后立即补齐。
func (r *currentPartyUDPRelayRemote) handleChannelLoss(sessionID uint64) {
	r.mu.Lock()
	if sessionID != 0 && sessionID != r.sessionID {
		r.mu.Unlock()
		return
	}
	activeRooms := len(r.roomMembers)
	r.connected = false
	r.conn = nil
	r.matrices = make(map[uint32]*partyUDPRelayRemoteMatrix)
	r.renewals = make(map[uint32][]uint16)
	r.resyncPending = make(map[uint32]struct{})
	r.closingRooms = nil
	for roomID, pending := range r.pending {
		delete(r.pending, roomID)
		pending.deliver(partyUDPRelayRemoteSyncResult{reason: partyUDPRelayRemoteReasonChannelLost})
	}
	closed := r.closed
	r.mu.Unlock()
	if sessionID == 0 || closed {
		return
	}
	r.event(partyUDPRelayRemoteEventChannelLost,
		"endpoint", r.endpoint,
		"scope", "party",
		"session", sessionID,
		"desired_rooms", activeRooms,
		"fallback", "direct_peer_list")
	r.noteUnavailable(partyUDPRelayRemoteReasonChannelLost)
}

// noteUnavailable 记一条限流的"当前不可用 ⇒ 回退直连名单"事件（同一 reason 10s 一条）。
func (r *currentPartyUDPRelayRemote) noteUnavailable(reason string, args ...any) {
	if r == nil || r.onEvent == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if r.unavailableReason == reason && time.Since(r.unavailableAt) < partyRelayRemoteReasonThrottle {
		r.mu.Unlock()
		return
	}
	r.unavailableReason = reason
	r.unavailableAt = time.Now()
	r.mu.Unlock()
	fields := []any{
		"reason", reason,
		"fallback", "direct_peer_list",
		"endpoint", r.endpoint,
		"scope", "party",
		"room_base", r.roomBase,
	}
	fields = append(fields, args...)
	r.event(partyUDPRelayRemoteEventUnavailable, fields...)
}

// event 记一条具名事件（**绝不带密钥**）。
func (r *currentPartyUDPRelayRemote) event(name string, args ...any) {
	if r == nil || r.onEvent == nil {
		return
	}
	r.onEvent(name, args...)
}

// ---------------------------------------------------------------------------
// 启动期：档位决议与后端构造（Service 侧）
// ---------------------------------------------------------------------------

// partyUDPRelayRemoteReasonConfigInvalid 是"配置在本进程内就没通过校验"的兜底原因。
const partyUDPRelayRemoteReasonConfigInvalid = "config_invalid"

// partyUDPRelayRemotePlan 是"请求档位 → 生效档位"的归一化结果（启动汇总行与判读共用）。
type partyUDPRelayRemotePlan struct {
	requested partyUDPRelayMode
	effective partyUDPRelayMode
	// reason 只有在 requested != effective 时才有值（mode_embedded / secret_missing / …）。
	reason   string
	settings partyUDPRelayRemoteSettings
}

// parsePartyUDPRelayRemoteEndpoint 解析 host:port（只做字面解析，不做 DNS）。
func parsePartyUDPRelayRemoteEndpoint(endpoint string) (string, int, error) {
	text := strings.TrimSpace(endpoint)
	if text == "" {
		return "", 0, errors.New("endpoint is empty")
	}
	host, portText, err := net.SplitHostPort(text)
	if err != nil {
		return "", 0, fmt.Errorf("endpoint %q is not host:port", text)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", 0, errors.New("endpoint host is empty")
	}
	port := 0
	for index := 0; index < len(portText); index++ {
		if portText[index] < '0' || portText[index] > '9' {
			return "", 0, fmt.Errorf("endpoint port %q is not numeric", portText)
		}
		port = port*10 + int(portText[index]-'0')
	}
	if port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("endpoint port %d is out of range", port)
	}
	return host, port, nil
}

// partyUDPRelayRemotePublicIP 选出写进 op11 的"中继公网 IP"。
//
// 口径照厂商 RemotePartyUdpRelay：**显式配置的 publicIp 优先，控制端点的主机只作回落**
// （厂商 `publicIp = IsNullOrWhiteSpace(publicIp) ? host : publicIp`）。我们这里的
// publicIp 就是 `DNFBRIDGE_SERVER_IP`。两个理由：
//   - 控制通道端点可以是内网/回环地址（本地联调、同机部署），而 op11 里给客户端的必须是
//     **公网可达**地址；
//   - 港机上 a21-relay 与游戏服同机（192.0.2.12），两者本来就相同，这条口径不会引入差异。
//
// 两者都拿不到具体 IPv4（含 0.0.0.0）时返回 false ⇒ 回退 embedded 并记具名事件。
func partyUDPRelayRemotePublicIP(host, advertiseIP string) ([4]byte, bool) {
	for _, candidate := range []string{advertiseIP, host} {
		parsed := net.ParseIP(strings.TrimSpace(candidate))
		if parsed == nil {
			continue
		}
		ipv4 := parsed.To4()
		if ipv4 == nil || ipv4.IsUnspecified() {
			continue
		}
		var result [4]byte
		copy(result[:], ipv4)
		return result, true
	}
	return [4]byte{}, false
}

// logPartyUDPRelayRemoteEvent 是 Service 侧的具名事件出口。
// **message 就是事件名**（全小写连字符，便于 grep），字段一律结构化 kv。
// 不可用/回退类走 WARN，其余走 INFO；无论级别，grep 事件名都能捞到。
func (s *Service) logPartyUDPRelayRemoteEvent(name string, args ...any) {
	if s == nil {
		return
	}
	switch name {
	case partyUDPRelayRemoteEventUnavailable,
		partyUDPRelayRemoteEventChannelLost,
		partyUDPRelayRemoteEventEmbeddedFallback:
		s.logWarn(name, args...)
	default:
		s.logInfo(name, args...)
	}
}

// resolvePartyUDPRelayRemotePlan 决定本次启动实际生效的中继后端：
//   - 总开关关（DNFBRIDGE_PARTY_UDP_RELAY_ENABLED=false）⇒ 两个后端都不启用（与改动前一致）；
//   - 请求 embedded ⇒ embedded（A/B 档，记一条 `party-udp-relay-remote-embedded-selected`）；
//   - 请求 remote ⇒ 逐项校验端点/密钥/公网 IP/房间号命名空间，任一不成立就**回退 embedded**，
//     并记 `party-udp-relay-remote-unavailable` + `party-udp-relay-remote-embedded-fallback`
//     （**绝不静默**），且**绝不让启动失败**。
func (s *Service) resolvePartyUDPRelayRemotePlan() partyUDPRelayRemotePlan {
	requested := partyUDPRelayModeEmbedded
	if s != nil {
		requested = s.options.partyUDPRelayMode
	}
	plan := partyUDPRelayRemotePlan{requested: requested, effective: partyUDPRelayModeEmbedded}
	if s == nil {
		return plan
	}
	if !s.options.partyUDPRelayEnabled {
		// 总开关关：两个后端都不生效（与改动前一致：内嵌 relay 会建但 Enabled()=false、
		// 不绑任何端口）。这里给一个**准确**的原因，而不是伪装成"配置不全"。
		plan.reason = partyUDPRelayRemoteReasonRelayDisabled
		return plan
	}
	if requested != partyUDPRelayModeRemote {
		plan.reason = partyUDPRelayRemoteReasonModeEmbedded
		return plan
	}
	host, port, err := parsePartyUDPRelayRemoteEndpoint(s.options.partyUDPRelayRemoteEndpoint)
	if err != nil {
		plan.reason = partyUDPRelayRemoteReasonEndpointInvalid
		return plan
	}
	if s.options.partyUDPRelayRemoteSecret == nil {
		if s.options.partyUDPRelayRemoteSecretState == partyUDPRelayRemoteSecretInvalid {
			plan.reason = partyUDPRelayRemoteReasonSecretInvalid
		} else {
			plan.reason = partyUDPRelayRemoteReasonSecretMissing
		}
		return plan
	}
	publicIP, ok := partyUDPRelayRemotePublicIP(host, s.options.serverIP)
	if !ok {
		plan.reason = partyUDPRelayRemoteReasonPublicIPInvalid
		return plan
	}
	roomBase := s.options.partyUDPRelayRemoteRoomBase
	if roomBase <= 0 || uint64(roomBase)+uint64(^uint16(0)) > uint64(^uint32(0)) {
		plan.reason = partyUDPRelayRemoteReasonRoomIDOutOfRange
		return plan
	}
	plan.effective = partyUDPRelayModeRemote
	plan.settings = partyUDPRelayRemoteSettings{
		endpoint: net.JoinHostPort(host, fmt.Sprintf("%d", port)),
		host:     host,
		port:     port,
		publicIP: publicIP,
		secret:   s.options.partyUDPRelayRemoteSecret,
		roomBase: roomBase,
		onEvent:  func(name string, args ...any) { s.logPartyUDPRelayRemoteEvent(name, args...) },
	}
	return plan
}

// newPartyUDPRelayBackends 按计划建后端。remote 档**只建控制通道客户端**（本进程不 bind
// 任何中继 UDP 端口）；embedded 档只建内嵌中继（逐字节回到改动前）。两个返回值至多一个非 nil。
// 内嵌中继构造失败仍按原样让启动失败（配置错误必须可见）；remote 档失败则回退 embedded。
func (s *Service) newPartyUDPRelayBackends() (*currentPartyUDPRelayRemote, *currentPartyUDPRelay, error) {
	plan := s.resolvePartyUDPRelayRemotePlan()
	switch {
	case plan.effective == partyUDPRelayModeRemote:
		remote, err := newCurrentPartyUDPRelayRemote(plan.settings)
		if err == nil {
			return remote, nil, nil
		}
		// 计划阶段已经逐项校验过，理论上到不了这里；真到了也必须可见、且不让启动失败。
		plan.reason = partyUDPRelayRemoteReasonConfigInvalid
		s.logPartyUDPRelayRemoteEvent(partyUDPRelayRemoteEventUnavailable,
			"reason", plan.reason,
			"fallback", "embedded_relay",
			"endpoint", s.options.partyUDPRelayRemoteEndpoint,
			"scope", "party",
			"secret", s.options.partyUDPRelayRemoteSecretState)
		s.logPartyUDPRelayRemoteEvent(partyUDPRelayRemoteEventEmbeddedFallback,
			"reason", plan.reason,
			"mode_requested", plan.requested.String(),
			"mode_effective", plan.effective.String(),
			"endpoint", s.options.partyUDPRelayRemoteEndpoint)
	case plan.requested == partyUDPRelayModeRemote && plan.reason == partyUDPRelayRemoteReasonRelayDisabled:
		// 总开关关：没有中继可用，端点行整份走直连名单。
		s.logPartyUDPRelayRemoteEvent(partyUDPRelayRemoteEventUnavailable,
			"reason", plan.reason,
			"fallback", "direct_peer_list",
			"endpoint", s.options.partyUDPRelayRemoteEndpoint,
			"scope", "party",
			"secret", s.options.partyUDPRelayRemoteSecretState)
	case plan.requested == partyUDPRelayModeRemote:
		if plan.reason == "" {
			plan.reason = partyUDPRelayRemoteReasonConfigInvalid
		}
		s.logPartyUDPRelayRemoteEvent(partyUDPRelayRemoteEventUnavailable,
			"reason", plan.reason,
			"fallback", "embedded_relay",
			"endpoint", s.options.partyUDPRelayRemoteEndpoint,
			"scope", "party",
			"secret", s.options.partyUDPRelayRemoteSecretState,
			"room_base", s.options.partyUDPRelayRemoteRoomBase)
		s.logPartyUDPRelayRemoteEvent(partyUDPRelayRemoteEventEmbeddedFallback,
			"reason", plan.reason,
			"mode_requested", plan.requested.String(),
			"mode_effective", plan.effective.String(),
			"endpoint", s.options.partyUDPRelayRemoteEndpoint)
	default:
		s.logPartyUDPRelayRemoteEvent(partyUDPRelayRemoteEventEmbeddedSelected,
			"mode_requested", plan.requested.String(),
			"mode_effective", plan.effective.String(),
			"port_start", s.options.partyUDPRelayPortStart,
			"port_count", s.options.partyUDPRelayPortCount)
	}
	relay, err := newCurrentPartyUDPRelayWithSettings(currentPartyUDPRelaySettings{
		enabled:          s.options.partyUDPRelayEnabled,
		advertiseIP:      s.options.serverIP,
		bindHost:         s.options.gameListenHost,
		portStart:        s.options.partyUDPRelayPortStart,
		portCount:        s.options.partyUDPRelayPortCount,
		endpointFallback: s.partyUDPRelayEndpointFallbackMode().enabled(),
		probeReplay:      s.partyUDPRelayProbeReplayMode().enabled(),
		probeListen:      s.partyUDPRelayProbeListenMode().enabled(),
		probePorts:       s.options.partyUDPRelayProbePorts,
		seedForward:      s.partyUDPRelaySeedForwardMode().enabled(),
		datagramTrace:    s.partyUDPRelayDatagramTraceMode(),
		// 无归属观测端点进候选集（**唯一一处把新开关接进 embedded 中继的管线**；
		// 本行只透传 options，不触碰本文件的 remote 后端逻辑）。
		observedEndpointFanout: s.partyUDPRelayObservedEndpointFanoutMode(),
		// 共享端口档（0 = off）：把"定向腿口"换成"客户端唯一持续保活的那个 UDP 端口"，
		// 见 party_udp_relay_via_port.go。零值 = 现行为。
		viaPort:       s.partyUDPRelayViaPort(),
		memoTTL:       s.partyUDPRelayMemoTTL(),
		kickTiming:    s.partyUDPRelayKickTiming(),
		pendingWindow: s.partyUDPRelayKickTiming().window,
		onSilentPeer: func(partyID, memberID uint16) bool {
			if !s.partyUDPRelaySilentPeerKickMode() {
				return false
			}
			return s.kickSilentPartyMember(partyID, memberID)
		},
		logWarn: func(message string, args ...any) { s.logWarn(message, args...) },
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, relay, nil
}

// partyRelayBackend 返回当前生效的中继后端：remote 优先，其次 embedded，都没有则 nil。
// 两个后端的 Enabled/Sync/Endpoint/CloseParty 语义同形，调用方不需要分支。
//
// 注意"请求 remote 但控制通道当前不在位"**不算**这里的分支条件：那时仍然返回 remote
// 后端，由它的 Sync 返回 false 让调用方整份回退直连名单（厂商语义）。
func (s *Service) partyRelayBackend() partyUDPRelayBackend {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	remote := s.partyUDPRelayRemote
	embedded := s.partyUDPRelay
	s.mu.Unlock()
	if remote != nil && remote.Enabled() {
		return remote
	}
	// 内嵌侧的 Enabled() 只表达"配置上启用"，这里还要 aliveness（Close 之后不再选中）。
	if embedded == nil || !embedded.Enabled() || !embedded.alive() {
		return nil
	}
	return embedded
}

// partyUDPRelayEffectiveMode 返回"实际在用的后端"档位（启动汇总行用）。
func (s *Service) partyUDPRelayEffectiveMode() partyUDPRelayMode {
	if s == nil {
		return partyUDPRelayModeEmbedded
	}
	s.mu.Lock()
	remote := s.partyUDPRelayRemote
	s.mu.Unlock()
	if remote != nil && remote.Enabled() {
		return partyUDPRelayModeRemote
	}
	return partyUDPRelayModeEmbedded
}

// closePartyUDPRelayBackends 关闭两个后端（幂等；remote 档下内嵌 relay 为 nil）。
func (s *Service) closePartyUDPRelayBackends() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	remote := s.partyUDPRelayRemote
	embedded := s.partyUDPRelay
	s.mu.Unlock()
	var closeErr error
	if embedded != nil {
		closeErr = errors.Join(closeErr, embedded.Close())
	}
	if remote != nil {
		closeErr = errors.Join(closeErr, remote.Close())
	}
	return closeErr
}

// ---------------------------------------------------------------------------
// 后端抽象：让 remote 与 embedded 在调用方看来完全同形
// ---------------------------------------------------------------------------

// partyUDPRelayBackend 是"组队 UDP 中继后端"的最小接口。
//
// 两个实现：
//   - `*currentPartyUDPRelay`（内嵌，party_udp_relay.go）：自己 bind 每对方向一条腿；
//   - `*currentPartyUDPRelayRemote`（本文件）：厂商架构，注册到独立进程 a21-relay，
//     只把 SYNC_OK 里的 (owner,peer)→port 矩阵写进 op11。
//
// 调用方（syncRuntimePartyUDPRelay / buildRuntimePartyPeerEndpointInfoForReceiver /
// closeRuntimePartyUDPRelay）只依赖这五个方法，因此**切档不改调用方形状**：
// `Sync` 返回 false 时调用方按厂商语义整份回退直连名单（不半份混合）。
type partyUDPRelayBackend interface {
	Enabled() bool
	Sync(partyID uint16, bindings []currentPartyUDPRelayBinding) bool
	Endpoint(partyID, ownerID, peerID uint16) ([4]byte, uint16, bool)
	CloseParty(partyID uint16)
	Close() error
}
