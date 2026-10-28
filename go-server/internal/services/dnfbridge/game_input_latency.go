package dnfbridge

import (
	"encoding/binary"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 读取协程只采时间，不拆包或写日志。此处在原消费路径记录慢读块，
// 不保留原始字节、账号或认证信息，也不改变原有坏帧、EOF及超时处理。
func (s *Service) logCurrentGameReadWait(session *gameSession, chunk nativeResultReadChunk) {
	if len(chunk.data) == 0 || chunk.readAt.IsZero() {
		return
	}
	wait := time.Since(chunk.readAt)
	if wait < 50*time.Millisecond {
		return
	}
	s.logGameEvent(session, "游戏读块消费等待耗时", "读取完成UTC", chunk.readAt.UTC().Format(time.RFC3339Nano),
		"等待微秒", wait.Microseconds(), "读块字节数", len(chunk.data))
}

// 所有帧拆出后至本条真正分派前的等待下界，覆盖前包事务与会话队列。
// 与读块等待起点不同，不能把两条最大值相加当作某只怪的白光时间。
func (s *Service) logCurrentDungeonDeathReadyWait(session *gameSession, packet dnfproto.LatestGameStreamPacket, readyAt time.Time) {
	if readyAt.IsZero() || time.Since(readyAt) < 50*time.Millisecond || packet.Kind != dnfproto.LatestGameStreamLegacy {
		return
	}
	p, err := dnfproto.ParseLegacyGamePacket(packet.Data)
	if err != nil || p.Header.Cmd != 1 || p.Header.Type != uint16(dnfenum.CmdPacketDieMonster) {
		return
	}
	var objectKey uint32
	if len(p.Body) >= 4 {
		objectKey = binary.LittleEndian.Uint32(p.Body[:4])
	}
	s.logGameEvent(session, "死亡请求同批等待耗时", "对象", objectKey, "header_seq", p.Header.Sequence,
		"本批就绪UTC", readyAt.UTC().Format(time.RFC3339Nano), "等待微秒", time.Since(readyAt).Microseconds())
}
