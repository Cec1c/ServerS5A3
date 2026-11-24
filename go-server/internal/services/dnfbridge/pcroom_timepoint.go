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
	settlement, settleErr := s.settleCurrentSpendTimeClockLocked(session, s.gameplayNow())
	if settleErr != nil {
		s.logCurrentPCRoomSettlementDeferred(session, "spend_time_clock", settleErr)
	} else if settlement.claimErr != nil {
		s.logCurrentPCRoomSettlementDeferred(session, "spend_time_claim", settlement.claimErr)
	}
	// 228 面板状态自己还要结一次账，这一步失败同样只降级成"面板显示旧值"。
	// 旧实现把上面两个错误直接 return 给 read loop，read loop 当致命错误关连接，
	// 客户端表现就是"网络中断"——现场时间线见下方 helper 注释。
	if sendErr := s.sendCurrentPCRoomTimePoint(session, c, daily, period); sendErr != nil {
		s.logCurrentPCRoomSettlementDeferred(session, "pcroom_state", sendErr)
	}
	return true, nil
}

// logCurrentPCRoomSettlementDeferred 记录"在线奖励面板结算失败但会话继续"。
//
// 2026-11-21 现场（90CN，账号 msp / dnf:10001，22:30 挂机到次日 07:16）：
//
//	07:16:4x    RECV type=632 ENUM_CMDPACKET_GET_PCROOM_TIME_POINT_ITEM（点开面板）
//	07:16:46.767 WARN dnfbridge handle game packet failed
//	             error="online-event observation is older than persisted progress"
//	07:16:47.7   网关 Game 连接已关闭（上行 broken pipe / 下行 eof）
//	             ⇒ 客户端弹「网络中断」，整夜在线被当成掉线
//
// 当晚同一错误在全服出现 40 次、全部落在 06:00 服务日边界之后；而网关泡点、
// 反外挂上报、TCP 存活时长都证明网络一直是好的。面板数字陈旧只是 UI 层的
// 事实，绝不能作为致命错误关掉会话——同文件 574-590 行的 PC 房/马战初始化
// 早已按"降级 + 继续"处理，这里是同一口径。
func (s *Service) logCurrentPCRoomSettlementDeferred(session *gameSession, stage string, err error) {
	if s == nil || err == nil {
		return
	}
	now := s.gameplayNow()
	fields := []any{
		"stage", stage,
		"now", now.Format(time.RFC3339),
		"impact", "panel_stale_session_continues",
		"error", err,
	}
	if session == nil {
		s.logWarn("dnfbridge pcroom timepoint settlement deferred", fields...)
		return
	}
	args := append([]any{"conn_id", session.connID, "char_id", session.selectedCharacterID}, fields...)
	if c := session.spendTime.catalog; c != nil {
		args = append(args,
			"event_id", c.definition.ID,
			"service_day", c.definition.ServiceDay(now),
			"anchor", spendTimeAnchorText(session.spendTime.anchor),
			"online_seconds", session.spendTime.onlineSeconds,
			"completed_stages", session.spendTime.completedStages,
		)
	}
	s.logWarn("dnfbridge pcroom timepoint settlement deferred", args...)
}

// spendTimeAnchorText 把观察游标写进日志。零值代表"这条会话还没建立游标"，
// 与"游标停在昨天"是两种完全不同的现场，必须能一眼分开。
func spendTimeAnchorText(anchor time.Time) string {
	if anchor.IsZero() {
		return "zero"
	}
	return anchor.Format(time.RFC3339)
}
