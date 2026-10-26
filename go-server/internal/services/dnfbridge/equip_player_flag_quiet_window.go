package dnfbridge

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

// 本文件实现 op19「玩家请求标志位」的**静默窗（quiet window）**：把"标志位误伤自动突发"的
// 最坏后果从「一次掉 5 件」压到「最多 1 件」。
//
// ## 判据与依据（实测）
//
// 客户端自动突发是**等间隔连发**（今天 0.25/0.32/0.33/0.91 s；历史批 0.20–0.50 s），
// 而玩家手动一次只发一条（现场两次相隔 12 s）。因此：
//
//	tail==1 的包，只有"它之后 quiet_ms 内**没有第二条 op19**"时才按厂商口径执行；
//	否则（第二条在窗内到达）⇒ 挂起那条按普通门禁路径处理（吞），新包照常处理。
//
// ## 为什么用"临时读截止"而不是定时器 goroutine（操作者拍板）
//
// 本仓库的 op19 处理**同步跑在读 goroutine 上**，回包也在同一条 goroutine 里发完。
// 若用 `time.AfterFunc` 在另一条 goroutine 回包，就会引入这个仓库从未有过的并发模式
// （会话写序列化 / 关闭取消 / 挂起态并发访问全在时序层）。改用**临时读截止**后：
//
//	挂起 ⇒ `conn.SetReadDeadline(now + quiet_ms)`；读到超时 ⇒ 读循环自己把挂起那条结掉
//	（"没有第二条" ⇒ 玩家请求 ⇒ 按厂商口径执行）；期间来了新包 ⇒ 读循环先结挂起、再处理新包。
//
// 四条边界（操作者点名要求，逐条对应实现）：
//
//  1. **超时 ≠ 连接错误**：只有 `session` 里确实有挂起、且错误是 `net.Error.Timeout()` 时，
//     才把它当成"我们的 400 ms 到期"（`isOp19QuietReadDeadline`）。真正的断线/EOF/ECONNRESET
//     仍走 `isQuietGameReadError` 原路 ⇒ **不会误断玩家**。
//  2. **恢复读截止**：游戏连接**今天根本没有设过读截止**（keepalive 是应用层的 30 s 报文，
//     见 `sendGameConnectionBootstrap`/msg_id=1206），所以"恢复"= `SetReadDeadline(time.Time{})`
//     （清掉），不存在与既有截止互相踩的问题。
//  3. **挂起期间来新包**：读循环在**处理每个包之前**先结挂起（按门禁路径），再处理新包
//     ⇒ 顺序不变、一个包都不丢（同一批 `SplitLatestGameStream` 拆出的多包同样逐包适用）。
//  4. **关闭/重启不泄漏**：挂起态只是 `session` 上的一个字节切片 + 一个 bool，**没有 goroutine、
//     没有定时器**；会话结束 = 结构体被丢弃 ⇒ 无泄漏、无 panic（结不掉就不回包，客户端本来也走了）。
//
// ## 开关
//
//	DNFBRIDGE_OP19_PLAYER_FLAG_QUIET_MS
//	  未设 / 空 / 非法 ⇒ **400 ms（修复档，默认）**
//	  0 / 负数        ⇒ **回退档**：不做静默窗判定 ⇒ 与 §18 那一版逐字节相同（tail==1 立即执行）
//	  1..5000         ⇒ 自定义窗口（上限 5000，避免把"手动脱下"拖得太久）
//
// 与 `DNFBRIDGE_OP19_PLAYER_FLAG` 的关系：后者决定"读不读这个字节"，本开关决定
// "读了之后要不要等一个静默窗"。两者都开（默认）才是完整的修复档。
const op19PlayerFlagQuietEnv = "DNFBRIDGE_OP19_PLAYER_FLAG_QUIET_MS"

const (
	op19PlayerFlagQuietDefaultMS = 400
	op19PlayerFlagQuietMaxMS     = 5000
)

// A move between positive slots of the main bag cannot reassert a worn slot.
// Keep its normal owner validation and ACK synchronous, including legacy flag 1.
// Slot zero (money) and every cross-container move retain their existing path.
func op19MainBagMove(body []byte) bool {
	request, err := dnfinventory.DecodeMoveItemspaceRequest(body)
	return err == nil && request.SourceListType == 0 && request.DestinationListType == 0 &&
		request.SourceSlotIndex > 0 && request.DestinationSlotIndex > 0
}

// op19PlayerFlagQuietWindow 归一化静默窗（每次调用都读 env，不缓存）。
//
//	未设 / 空 / 非法 / 超过上限 ⇒ 默认 400 ms（上限收敛，避免把手动脱下拖太久）
//	0 / 负数                    ⇒ 0 = 回退档（不做静默窗判定，与 §18 那一版逐字节相同）
func op19PlayerFlagQuietWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv(op19PlayerFlagQuietEnv))
	if raw == "" {
		return op19PlayerFlagQuietDefaultMS * time.Millisecond
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return op19PlayerFlagQuietDefaultMS * time.Millisecond
	}
	if value <= 0 {
		return 0
	}
	if value > op19PlayerFlagQuietMaxMS {
		value = op19PlayerFlagQuietMaxMS
	}
	return time.Duration(value) * time.Millisecond
}

// hasOp19QuietPending 回答"本会话是否有一条挂起的 tail==1 包"。
// 只在读 goroutine 上读写（挂起、结算、读循环都在同一条 goroutine）⇒ 不需要锁。
func (s *gameSession) hasOp19QuietPending() bool {
	return s != nil && len(s.op19QuietPending) > 0
}

// pendOp19QuietBody 记下这条包（复制一份，读缓冲会被复用）。
func (s *gameSession) pendOp19QuietBody(body []byte) {
	s.op19QuietPending = append([]byte(nil), body...)
}

// dropOp19QuietPending 丢弃挂起态（会话结束/结算完成后）。
func (s *gameSession) dropOp19QuietPending() {
	s.op19QuietPending = nil
}

// isOp19QuietReadDeadline 判定"这个读错误是不是我们自己挂的静默窗截止到期"。
//
// **必须同时**满足：会话里确实有挂起 + 错误是超时。这样真正的断线（EOF/ECONNRESET/
// net.ErrClosed/其它）永远不会被误判成"静默窗到期"，反之亦然。
func isOp19QuietReadDeadline(session *gameSession, err error) bool {
	if session == nil || !session.hasOp19QuietPending() || err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// armOp19QuietReadDeadline 给连接挂上静默窗读截止（会话本来没有读截止）。
func armOp19QuietReadDeadline(conn net.Conn, window time.Duration) {
	if conn == nil || window <= 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(window))
}

// clearOp19QuietReadDeadline 结算后把读截止清掉（回到"没有截止"的原始状态）。
func clearOp19QuietReadDeadline(conn net.Conn) {
	if conn == nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
}

// resolveOp19QuietPending 结掉挂起的那条 tail==1 包。
//
//	asPlayerRequest = true （静默窗内没有第二条 ⇒ 玩家亲手发起）⇒ 保持尾字节 1，按厂商口径执行；
//	asPlayerRequest = false（静默窗内来了第二条 ⇒ 它属于一串突发）⇒ 把尾字节清 0，
//	                         于是它就**完全等价于一条普通的复述包**，走既有窗口/occurrence 门禁。
//
// 两条路都通过**同一个真实入口** `handleAlignedGameCommand` 重放（不是第二条捷径），
// 顺序与线上一致；重放期间用 `op19QuietResolving` 防止再次挂起。
func (s *Service) resolveOp19QuietPending(session *gameSession, asPlayerRequest bool) {
	if session == nil || !session.hasOp19QuietPending() {
		return
	}
	body := append([]byte(nil), session.op19QuietPending...)
	session.dropOp19QuietPending()
	flagged := len(body) >= 27 && body[26] == 1
	if !asPlayerRequest && flagged {
		body[26] = 0
	}
	session.op19QuietResolving = true
	defer func() { session.op19QuietResolving = false }()
	if _, err := s.handleAlignedGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketMoveItemspace),
		body,
	); err != nil {
		s.logWarn("dnfbridge op19 quiet window resolve failed",
			"conn_id", session.connID,
			"error", err)
	}
	decision := "swallow"
	reason := "quiet-window"
	if asPlayerRequest {
		decision = "apply-vendor"
	}
	s.logPacketEvent("equip-move/player-request-flag",
		"conn_id", session.connID,
		"decision", decision,
		"reason", reason,
		"resolved_by", map[bool]string{true: "quiet-window-timeout", false: "second-op19-in-window"}[asPlayerRequest],
		"quiet_ms", op19PlayerFlagQuietWindow().Milliseconds())
}
