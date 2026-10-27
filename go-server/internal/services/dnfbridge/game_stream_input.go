package dnfbridge

import (
	"context"
	"time"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 与登录初始化分离，以真实连接验证拆包、事务执行与连续结果写回。
// 生产调用仍在同一连接、同一会话事件循环内。
func (s *Service) handleGameStreamInput(ctx context.Context, session *gameSession) {
	conn := session.conn
	channel := session.channel
	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	var streamWaitTrace gameStreamWaitTrace
	var readAhead *nativeResultReadAhead
	var deferredRead *nativeResultReadChunk
	if s.options.currentClientVersion == "official-24.9" && nativeResultDeliveryEnabled() {
		readAhead = newNativeResultReadAhead(conn)
		defer readAhead.stop()
		defer func() { _ = s.finishNativeResultDelivery(session, "连接处理结束") }()
	}
	for {
		var n int
		var err error
		if readAhead == nil {
			n, err = conn.Read(chunk)
		} else {
			var input nativeResultReadChunk
			ready := deferredRead != nil
			if ready {
				input, deferredRead = *deferredRead, nil
			} else {
				input, ready = readAhead.tryNext()
			}
			if !ready {
				// 没有已到达的字节就立即发送，绝不等下一次用户操作或静默计时。
				if err := s.finishNativeResultDelivery(session, "已到达请求处理完毕"); err != nil {
					return
				}
				input = readAhead.next(ctx)
			}
			chunk, n, err = input.data, len(input.data), input.err
			if readAhead.obsoleteTimeout(input) {
				continue
			}
		}
		if err != nil {
			// 「静默窗」读截止到期：**这不是连接错误**（真正的断线/EOF/ECONNRESET 走下面的原路）。
			// 只有"本会话确实有一条挂起的 tail==1 op19" + 错误是超时，才判成我们自己的截止。
			// 到期 = 挂起那条之后 quiet_ms 内没有第二条 op19 ⇒ 玩家亲手发起 ⇒ 按厂商口径执行。
			if isOp19QuietReadDeadline(session, err) {
				s.resolveOp19QuietPending(session, true)
				if readAhead != nil {
					readAhead.changeDeadline(func() { clearOp19QuietReadDeadline(conn) })
				} else {
					clearOp19QuietReadDeadline(conn)
				}
				s.logPacketEvent("game-read-quiet-window-deadline",
					"conn_id", session.connID,
					"decision", "resolve-as-player-request",
					"reason", "op19-quiet-window-elapsed",
					"quiet_ms", op19PlayerFlagQuietWindow().Milliseconds())
				continue
			}
			quiet := isQuietGameReadError(err)
			s.logPacketEvent("game-read-close",
				"conn_id", session.connID,
				"remote", conn.RemoteAddr().String(),
				"local", conn.LocalAddr().String(),
				"channel_id", channel.ID,
				"pending_bytes", len(buffer),
				"quiet", quiet,
				"error", err)
			if !quiet && ctx.Err() == nil {
				s.logWarn("dnfbridge read game packet failed", "remote", conn.RemoteAddr().String(), "error", err)
			}
			return
		}
		if n > 0 {
			s.logGamePacket(session, "RECV", "game-raw", chunk[:n])
		}
		buffer = append(buffer, chunk[:n]...)
		packets, remaining, skipped, err := dnfproto.SplitLatestGameStream(buffer, s.options.maxPacketBytes)
		if err != nil {
			s.logPacketEvent("game-split-error",
				"conn_id", session.connID,
				"remote", conn.RemoteAddr().String(),
				"local", conn.LocalAddr().String(),
				"channel_id", channel.ID,
				"buffer_bytes", len(buffer),
				"error", err)
			s.logWarn("dnfbridge split game stream failed", "remote", conn.RemoteAddr().String(), "error", err)
			return
		}
		if skipped > 0 {
			s.logWarn("dnfbridge game frame resynced", "remote", conn.RemoteAddr().String(), "skipped", skipped)
		}
		if s.options.gameStreamWaitTrace {
			if metadata, report := streamWaitTrace.observe(time.Now(), remaining, len(remaining) < len(buffer), s.options.maxPacketBytes); report {
				fields := []any{"conn_id", session.connID, "channel_id", channel.ID, "pending_bytes", metadata.pendingBytes, "header_shape", metadata.headerShape}
				if metadata.hasChannelHeader {
					fields = append(fields, "head_class", metadata.classification, "head_opcode", metadata.opcode, "head_declared_length", metadata.declaredLength)
				}
				s.logPacketEvent("game-stream-wait", fields...)
			}
		}
		buffer = remaining
		if readAhead != nil {
			packets, buffer, deferredRead = s.collectNativeNPCReadyPackets(session, readAhead, packets, buffer)
			if deferredRead == nil {
				packets, buffer, deferredRead = s.collectNativeExtractionReadyPackets(session, readAhead, packets, buffer)
			}
		}
		for index := 0; index < len(packets); {
			packet := packets[index]
			count := nativeNPCDisjointPrefix(packets[index:])
			extractionCount := nativeExtractionPrefix(packets[index:])
			processed := 1
			// 「静默窗」挂起期间来了新包 ⇒ **先结挂起（按门禁路径）再处理本包**：
			// 顺序不变、一个包都不丢（同一批 SplitLatestGameStream 拆出的多包逐包适用）。
			if session.hasOp19QuietPending() {
				s.resolveOp19QuietPending(session, false)
				if readAhead != nil {
					readAhead.changeDeadline(func() { clearOp19QuietReadDeadline(conn) })
				} else {
					clearOp19QuietReadDeadline(conn)
				}
			}
			if err := s.callGameSession(ctx, session, "game-stream-packet", func() error {
				if readAhead != nil && extractionCount >= 2 {
					handled, err := s.handleNativeExtractionBatch(session, packets[index:index+extractionCount])
					if handled {
						processed = extractionCount
						return err
					}
				}
				if readAhead != nil && count >= 2 {
					handled, err := s.handleNativeNPCDisjointBatch(session, packets[index:index+count])
					if handled {
						processed = count
						return err
					}
				}
				if readAhead != nil {
					if err := s.prepareNativeResultDelivery(session, packet); err != nil {
						return err
					}
				}
				return s.handleGameStreamPacket(session, packet)
			}); err != nil {
				s.logWarn("dnfbridge handle game packet failed", "remote", conn.RemoteAddr().String(), "error", err)
				return
			}
			index += processed
			// 本包刚挂起了一条 ⇒ 给它挂上静默窗读截止（会话本来没有读截止，结算后清掉即可）。
			if session.hasOp19QuietPending() {
				if readAhead != nil {
					readAhead.changeDeadline(func() { armOp19QuietReadDeadline(conn, op19PlayerFlagQuietWindow()) })
				} else {
					armOp19QuietReadDeadline(conn, op19PlayerFlagQuietWindow())
				}
			}
		}
	}
}
