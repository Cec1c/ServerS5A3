package dnfbridge

import (
	"encoding/binary"
	"time"
)

// 本机战令窗口第一阶段：只回报未开放，不读写任务、付费权限或奖励。
const battlePassGameOpcode uint16 = 0x7f11

func decodeBattlePassPreviewRequest(body []byte) (gamePlayerStatsRequest, bool) {
	if len(body) != 12 || string(body[:4]) != "BPQ1" {
		return gamePlayerStatsRequest{}, false
	}
	r := gamePlayerStatsRequest{binary.LittleEndian.Uint32(body[4:8]), binary.LittleEndian.Uint32(body[8:12])}
	return r, r.id != 0 && r.epoch != 0
}

func (s *Service) handleBattlePassPreview(session *gameSession, body []byte) error {
	r, ok := decodeBattlePassPreviewRequest(body)
	if !ok || session == nil || !s.currentBattlePassEnabled() {
		return nil
	}
	if _, _, valid := s.playerStatsGameIdentity(session); !valid {
		return nil
	}
	// 会话事件循环串行访问；连续点击不增加查询或错误回包。
	now := time.Now()
	if !session.battlePassLast.IsZero() && now.Sub(session.battlePassLast) < time.Second {
		return nil
	}
	session.battlePassLast = now
	reply := make([]byte, 20)
	copy(reply, "BPR1")
	binary.LittleEndian.PutUint32(reply[4:8], r.id)
	binary.LittleEndian.PutUint32(reply[8:12], r.epoch)
	binary.LittleEndian.PutUint16(reply[12:14], 1) // 赛季未开放；没有奖励数据。
	return s.sendGameUpperFixed16Transport(session, battlePassGameOpcode, reply, 1, 0, false, "battlepass_preview_v1_fixed16_plain")
}
