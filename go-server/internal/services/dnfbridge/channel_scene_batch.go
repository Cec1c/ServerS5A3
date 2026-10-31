package dnfbridge

import (
	"fmt"
	"io"
	"time"
)

const currentChannelSceneBatchLimit = 8 << 20

// 选角ACK会先建立town的首个区域。房间op24保持在完整角色资料之后，
// 因此普通换线先备齐整组合法报文，再一次提交已编码的字节流，避免准备资料时
// 让客户端独自停在ACK建立的街道。包体、顺序、序号及DPROTO编码仍走原路径。
type currentChannelSceneBatch struct {
	wire    []byte
	upper   [][]byte
	frames  int
	started time.Time
	err     error
}

func (s *Service) beginCurrentOrdinaryChannelSceneBatch(p *gameSession) (bool, error) {
	if s == nil || p == nil || p.conn == nil || s.options.currentClientVersion != "official-24.9" || !p.channelReconnect || currentColdSpecialChannel(p.channel) {
		return false, nil
	}
	p.wireMu.Lock()
	defer p.wireMu.Unlock()
	if p.channelSceneBatch != nil {
		return false, fmt.Errorf("普通换线初始化批次不能嵌套")
	}
	p.channelSceneBatch = &currentChannelSceneBatch{started: time.Now()}
	return true, nil
}

// 调用方持有wireMu；保护层和原始帧都经过同一写出口，不替换连接对象。
func (s *Service) writeCurrentGameWireLocked(p *gameSession, wire []byte) (int, error) {
	if b := p.channelSceneBatch; b != nil {
		if b.err != nil {
			return 0, b.err
		}
		if len(wire) > currentChannelSceneBatchLimit-len(b.wire) {
			b.err = fmt.Errorf("普通换线初始化超过%d字节，拒绝发送不完整场景", currentChannelSceneBatchLimit)
			return 0, b.err
		}
		b.wire = append(b.wire, wire...)
		b.frames++
		return len(wire), nil
	}
	if p.nativeResultDelivery != nil {
		// 原始帧及保护控制不属于分解结果，先释放此前准备的应答。
		if err := s.flushNativeResultDeliveryLocked(p, "原始或保护控制报文"); err != nil {
			return 0, err
		}
	}
	return s.writeGameConnectionObserved(p.conn, wire, perfWireDirect)
}

func (s *Service) finishCurrentOrdinaryChannelSceneBatch(p *gameSession, routeErr error) error {
	p.wireMu.Lock()
	defer p.wireMu.Unlock()
	b := p.channelSceneBatch
	p.channelSceneBatch = nil
	if b == nil {
		return routeErr
	}
	if routeErr == nil {
		routeErr = b.err
	}
	if routeErr == nil && p.ctx != nil {
		routeErr = p.ctx.Err()
	}
	if routeErr == nil && len(b.wire) != 0 {
		n, err := s.writeGameConnectionObserved(p.conn, b.wire, perfWireScene)
		routeErr = err
		if routeErr == nil && n != len(b.wire) {
			routeErr = io.ErrShortWrite
		}
	}
	if routeErr != nil {
		_ = p.conn.Close() // 保护层已编码的失败批次不能在旧序列上继续使用。
		s.logGameEvent(p, "普通换线初始化批次失败", "报文数", b.frames, "字节数", len(b.wire), "原因", routeErr)
		return routeErr
	}
	for _, inner := range b.upper {
		s.logCurrentSharedInventoryPacketWritten(p, inner)
		s.logGoldDiagnosticPacketResult(p, inner, false)
	}
	s.logGameEvent(p, "普通换线初始化批次已写出", "报文数", b.frames, "字节数", len(b.wire), "准备毫秒", time.Since(b.started).Milliseconds(), "范围", "选角ACK至完整房间初始化，原报文顺序保持")
	return nil
}
