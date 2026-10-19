package dnfbridge

import (
	"encoding/binary"
	"strings"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// DNF.exe 1EA4EDB and 1EA281D use 1CF3800, which reads the native channel
// number and accepts 75..77. Type 41 is the DPL preliminary tournament and
// additionally opens obsolete tournament assets; it is not this entry.
func isCurrentPVPTotalChannel(channel channelcatalog.Channel) bool {
	return isPVPChannel(channel) && channel.Type == 8 && channel.ID >= 75 && channel.ID <= 77
}

func isPVPChannel(channel channelcatalog.Channel) bool {
	return strings.EqualFold(strings.TrimSpace(channel.Group), "pvp")
}

func (s *Service) freeDuelAvailable() bool {
	if s == nil {
		return false
	}
	catalog := s.currentCatalog()
	if catalog == nil {
		return false
	}
	for _, channel := range catalog.Channels() {
		if isPVPChannel(channel) {
			return true
		}
	}
	return false
}

func (s *Service) handlePVPChannelInfo(session *gameSession, body []byte, transport string) error {
	requestValid := len(body) == 0
	success := requestValid && s.freeDuelAvailable()
	response := []byte{0, 0x15}
	if success {
		response = s.pvpAvailabilityBody(session)
	}
	if session != nil {
		s.logGameEvent(session, "game-pvp-channel-info",
			"transport", transport,
			"body_len", len(body),
			"available", s.freeDuelAvailable(),
			"success", success,
			"response_body", response)
	}
	return s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketPVPChannelInfo), response, 1)
}

// 当前 EXE 01fbb360：u32 保留值、u8 行数、每行三个 u32。
// 只公布本机目录中的真实频道，不制造不存在的监听端点。
func (s *Service) pvpAvailabilityBody(p *gameSession) []byte {
	ids := []int{}
	seen := map[int]bool{}
	if catalog := s.currentCatalog(); catalog != nil {
		for _, c := range catalog.Channels() {
			if (isPVPChannel(c) || p != nil && c.ID == p.channel.ID) && !seen[c.ID] && len(ids) < 255 {
				ids = append(ids, c.ID)
				seen[c.ID] = true
			}
		}
	}
	body := []byte{1, 0, 0, 0, 0, byte(len(ids))}
	for _, id := range ids {
		body = binary.LittleEndian.AppendUint32(body, uint32(id))
		body = binary.LittleEndian.AppendUint32(body, 0)
		body = binary.LittleEndian.AppendUint32(body, 1)
	}
	return body
}
