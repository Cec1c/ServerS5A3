package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"

	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const currentPCRoomTimePointEventID uint16 = 228
const currentPCRoomTimePointStateID uint16 = 561
const currentPCRoomTimePointRequestID uint16 = 632

// v146 binds event 228 to native window 39 at the common activity click site.
// The route remains a known client key; other events keep their own windows.
func writeCurrentPCRoomTimePointActivityBase(writer *packetWriter) {
	writer.writeUint16(currentPCRoomTimePointEventID)
	writer.writeUint32(currentSpendTimeBaseCatalogValue)
	writer.writeRawDstr(nil)
	writer.writeRawDstr(nil)
	writer.writeByte(1)
	writer.writeByte(0)
	writer.writeByte(4)
	writeClientNameDstr(writer, "累计在线奖励")
	writer.writeRawDstr(nil)
	writeClientNameDstr(writer, "在线达到每日阶段后领取奖励，累计达标天数另有奖励。")
	writer.writeUint32(0)
	writer.writeUint32(0)
	writer.writeAsciiDstr(currentSpendTimeEventRoute)
	writer.writeRawDstr(nil)
	writer.writeByte(1)
}

// 原生状态读取器消费17字节。PVF每日仅一轮，偏移9的零基轮次保持0。
func buildCurrentPCRoomTimePointBody(s dnfonlineevent.PCRoomTimePointSnapshot) []byte {
	b := make([]byte, 17)
	binary.LittleEndian.PutUint32(b, s.Seconds)
	binary.LittleEndian.PutUint32(b[4:], s.Total)
	b[8] = s.DailyMask
	binary.LittleEndian.PutUint32(b[13:], s.PeriodMask)
	return b
}

func parseCurrentPCRoomTimePointRequest(body []byte) (daily byte, period int, err error) {
	period = -1
	if len(body) != 2 {
		return 0, -1, fmt.Errorf("pcroom request length=%d", len(body))
	}
	if body[0] == 0 && body[1] == 255 {
		return 0, -1, nil
	}
	if (body[0] == 1 || body[0] == 2 || body[0] == 4 || body[0] == 8) && body[1] == 255 {
		return body[0], -1, nil
	}
	if body[0] == 16 && body[1] < 4 {
		return 0, int(body[1]), nil
	}
	return 0, -1, fmt.Errorf("pcroom request action=%02x index=%02x", body[0], body[1])
}

func (s *Service) settleCurrentPCRoomTimePoint(ctx context.Context, accountID, characterID string, c *currentSpendTimeRuntimeCatalog, now time.Time, daily byte, period int, validate func() bool) (dnfonlineevent.PCRoomTimePointSnapshot, error) {
	repos, ok := s.repositoryGroup()
	if !ok || c == nil || c.nativePCRoom == nil {
		return dnfonlineevent.PCRoomTimePointSnapshot{}, dnfonlineevent.ErrOwnerUnavailable
	}
	return dnfonlineevent.SettlePCRoomTimePoint(ctx, repos, c.nativePCRoom, dnfonlineevent.PCRoomTimePointCommand{
		AccountID: accountID, CharacterID: characterID, Now: now, ClockDefinition: c.definition, LegacyCumulative: c.cumulative,
		DailyClaim: daily, PeriodClaim: period, Validate: validate,
		Deliver: func(mailboxes dnfrepo.MailboxRepository, reward dnfonlineevent.AttendancePVFItemReward, key string) (string, error) {
			items, err := s.currentPVFItemCatalog()
			if err != nil {
				return "", err
			}
			definition, err := items.ResolveItem(uint32(reward.ItemID))
			if err != nil {
				return "", err
			}
			definition, err = currentPVFItemDefinitionForGrantAt(definition, now.UTC())
			if err != nil {
				return "", err
			}
			if definition.Kind != dungeonDropItemStackable || (!definition.ExpirationDate.IsZero() && !definition.ExpirationDate.After(now)) {
				return "", fmt.Errorf("pcroom reward %d expired or unsupported", reward.ItemID)
			}
			return dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{
				RecipientCharacterID: characterID, Title: "累计在线得好礼", Body: "累计在线活动奖励已发放，请查收附件。", Source: "pcroom_timepoint_228", CompletionKey: key, CreatedAt: now,
				Attachments: []dnfrepo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count, ExpireAt: definition.ExpirationDate}},
			})
		},
	})
}

func (s *Service) sendCurrentPCRoomTimePoint(session *gameSession, c *currentSpendTimeRuntimeCatalog, daily byte, period int) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || !session.loginPacketSeen || !session.accountFromToken || !s.currentSpendTimeEventEnabled() {
		return dnfonlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	state, err := s.settleCurrentPCRoomTimePoint(ctx, s.accountIDForSession(session), strconv.Itoa(int(identity.character)), c, s.gameplayNow(), daily, period, func() bool { return s.boundGameSessionCharacterCurrent(identity) && s.currentSpendTimeEventEnabled() })
	if err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return dnfonlineevent.ErrOwnerUnavailable
	}
	if err := s.sendGameUpperRawClass(session, currentPCRoomTimePointStateID, buildCurrentPCRoomTimePointBody(state), 0); err != nil {
		return err
	}
	s.logGameEvent(session, "原生在线奖励状态下发", "event_id", currentPCRoomTimePointEventID, "char_id", identity.character, "seconds", state.Seconds, "total", state.Total, "daily_mask", state.DailyMask, "period_mask", state.PeriodMask, "mail_count", len(state.MailIDs))
	// 按钮上行632后以561刷新；该EXE没有查到对应632成功体处理器，
	// class0/632属于其它功能，不能借用同号通知伪造领取应答。
	if len(state.MailIDs) > 0 {
		return s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "pcroom_timepoint_228")
	}
	return nil
}

func (s *Service) handleCurrentPCRoomTimePoint(session *gameSession, op uint16, classification byte, body []byte) (bool, error) {
	if s == nil || s.options.currentClientVersion != "official-24.9" || op != currentPCRoomTimePointRequestID {
		return false, nil
	}
	if classification != 1 || !s.currentSpendTimeEventEnabled() {
		return true, nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		return true, nil
	}
	daily, period, err := parseCurrentPCRoomTimePointRequest(body)
	if err != nil {
		s.logGameEvent(session, "原生在线奖励请求拒绝", "error", err)
		return true, nil
	}
	session.spendTime.mu.Lock()
	defer session.spendTime.mu.Unlock()
	c := session.spendTime.catalog
	if c == nil || c.nativePCRoom == nil || session.spendTime.characterID != session.selectedCharacterID || session.spendTime.characterGeneration != session.characterGeneration {
		return true, nil
	}
	settlement, err := s.settleCurrentSpendTimeClockLocked(session, s.gameplayNow())
	if err != nil {
		return true, err
	}
	if settlement.claimErr != nil {
		return true, settlement.claimErr
	}
	return true, s.sendCurrentPCRoomTimePoint(session, c, daily, period)
}
