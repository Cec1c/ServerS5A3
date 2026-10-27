package dnfbridge

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
)

const currentDamageFontSkinListMsgID uint16 = 1239

func (s *Service) sendDamageFontExpiredNotice(session *gameSession) error {
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("该伤害字体已超过配置的使用期限，无法使用。道具未消耗。"))
	if err != nil {
		return err
	}
	var writer packetWriter
	writer.writeByte(0)
	writer.writeUint16(0xffff)
	writer.writeByte(0)
	writer.writeRawDstr(encoded)
	writer.writeRawDstr(nil)
	return s.sendGameUpperRawClass(session, currentAreaChatNotifyOpcode, writer.bytes(), 0)
}

func buildCurrentDamageFontSkinListBody(stats map[string]int64, now time.Time) []byte {
	selected, entries := dnfinventory.DamageFontStateFromStats(stats, now)
	var writer packetWriter
	writer.writeUint16(selected)
	writer.writeUint16(uint16(len(entries)))
	for _, entry := range entries {
		writer.writeUint32(uint32(entry.FontIndex))
		writer.writeUint32(entry.ExpiresAt)
	}
	return writer.bytes()
}

func (s *Service) sendSelectedDamageFontState(session *gameSession, source string) error {
	if session == nil || session.selectedCharacterID == 0 {
		return fmt.Errorf("selected character is required for damage-font projection")
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return fmt.Errorf("character repository unavailable for damage-font projection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	record, found, err := repositories.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return fmt.Errorf("load damage-font character state: %w", err)
	}
	if !found {
		return fmt.Errorf("damage-font character %d not found", session.selectedCharacterID)
	}
	now := s.gameplayNow().UTC()
	selected, entries := dnfinventory.DamageFontStateFromStats(record.Stats, now)
	body := buildCurrentDamageFontSkinListBody(record.Stats, now)
	s.logPacketEvent("game-damage-font-state-send",
		"conn_id", session.connID,
		"source", source,
		"char_id", session.selectedCharacterID,
		"msg_id", currentDamageFontSkinListMsgID,
		"classification", 0,
		"selected_font_index", selected,
		"entry_count", len(entries),
		"body_len", len(body))
	return s.sendGameUpperRawClass(session, currentDamageFontSkinListMsgID, body, 0)
}
