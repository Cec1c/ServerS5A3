package dnfbridge

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const attendanceMarblePVFPath = onlineevent.MarblePVFPath

func attendanceMarbleGameplayModule() gameplayModuleDefinition {
	info := uint16(dnfenum.CmdPacketAttendanceMarbleEventInfo)
	dice := uint16(dnfenum.CmdPacketAttendanceMarbleEventDice)
	doubleExit := uint16(dnfenum.CmdPacketAttendanceMarbleDoubleExit)
	return gameplayModuleDefinition{
		Name: "attendance-marble",
		LegacyHandlers: map[uint16]gameplayHandler{
			info: func(s *Service, session *gameSession, request gameplayRequest) error {
				return s.handleAttendanceMarbleInfo(session, request.Body)
			},
			dice: func(s *Service, session *gameSession, request gameplayRequest) error {
				return s.handleAttendanceMarbleDice(session, request.Body)
			},
			doubleExit: func(s *Service, session *gameSession, request gameplayRequest) error {
				return s.handleAttendanceMarbleDoubleExit(session, request.Body)
			},
		},
		UpperHandlers: map[uint16]gameplayHandler{
			info: defaultClassGameplayHandler("game-attendance-marble-info-class-blocked", "current_exe_attendance_marble_info_requires_default_class", func(s *Service, session *gameSession, body []byte) error {
				return s.handleAttendanceMarbleInfo(session, body)
			}),
			dice: defaultClassGameplayHandler("game-attendance-marble-dice-class-blocked", "current_exe_attendance_marble_dice_requires_default_class", func(s *Service, session *gameSession, body []byte) error {
				return s.handleAttendanceMarbleDice(session, body)
			}),
			doubleExit: defaultClassGameplayHandler("game-attendance-marble-double-exit-class-blocked", "current_exe_attendance_marble_double_exit_requires_default_class", func(s *Service, session *gameSession, body []byte) error {
				return s.handleAttendanceMarbleDoubleExit(session, body)
			}),
		},
	}
}

func (s *Service) attendanceMarbleFailure(session *gameSession, opcode uint16, err error) error {
	s.logGameEvent(session, "大富翁请求被拒绝", "opcode", opcode, "reason", err)
	code := byte(19)
	if errors.Is(err, onlineevent.ErrMarbleSpent) {
		code = 22
	}
	if errors.Is(err, onlineevent.ErrMarbleGold) {
		code = 9
	}
	return s.sendGameUpperRawClass(session, opcode, []byte{0, code}, 1)
}

func (s *Service) handleAttendanceMarbleInfo(session *gameSession, body []byte) error {
	if err := parseAttendanceMarbleRequestInfo(body); err != nil {
		return s.attendanceMarbleFailure(session, 629, err)
	}
	state, err := s.settleAttendanceMarble(session, nil, true)
	if err != nil {
		return s.attendanceMarbleFailure(session, 629, err)
	}
	s.logGameEvent(session, "大富翁状态已同步", "position", state.Position, "round", state.Round, "earned", state.Turns, "remaining", state.Remaining(), "clears", state.Clears)
	if err := s.sendGameUpperSuccess(session, 629, buildAttendanceMarbleInfoBody(state)); err != nil {
		return err
	}
	for i := 29; i <= 32; i++ {
		if state.PendingBuffs[i-29] > 0 {
			if err := s.sendAttendanceMarbleBuff(session, i, state.PendingBuffs[i-29]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) handleAttendanceMarbleDice(session *gameSession, body []byte) error {
	_, err := parseAttendanceMarbleRequestDice(body)
	var dice [2]byte
	if err == nil {
		dice, err = secureAttendanceMarbleDice()
	}
	var state attendanceMarbleState
	if err == nil {
		state, err = s.settleAttendanceMarble(session, &dice, false)
	}
	if err != nil {
		if sendErr := s.attendanceMarbleFailure(session, 630, err); sendErr != nil {
			return sendErr
		}
		return s.handleAttendanceMarbleInfo(session, nil)
	}
	// 原生630应答驱动双骰和棋盘移动，立即补629会在到达奖励格前重置动画。
	if err := s.sendGameUpperSuccess(session, 630, buildAttendanceMarbleDiceBody(dice, state.Remaining(), state.Round)); err != nil {
		return err
	}
	if state.GoldChanged {
		if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketWalkoutPartyMember), buildCurrentGoldStateBody(state.GoldAfter), 0); err != nil {
			return err
		}
	}
	s.logGameEvent(session, "大富翁掷骰已提交", "dice", dice, "position", state.Position, "round", state.Round, "remaining", state.Remaining(), "double", state.Double, "mail_count", len(state.MailIDs))
	if state.BuffIndex != 0 {
		if err := s.sendAttendanceMarbleBuff(session, state.BuffIndex, state.PendingBuffs[state.BuffIndex-29]); err != nil {
			return err
		}
	}
	if len(state.MailIDs) > 0 {
		return s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "attendance_marble")
	}
	return nil
}

func (s *Service) sendAttendanceMarbleBuff(session *gameSession, index int, remaining int64) error {
	var remove packetWriter
	remove.writeByte(1)
	remove.writeUint32(uint32(index))
	if err := s.sendGameUpperRawClass(session, 488, remove.bytes(), 0); err != nil {
		return err
	}
	var body packetWriter
	body.writeByte(1)
	body.writeUint32(uint32(index))
	body.writeUint32(uint32(remaining))
	body.writeUint32(0)
	body.writeUint32(0)
	return s.sendGameUpperRawClass(session, 487, body.bytes(), 0)
}

func (s *Service) activateAttendanceMarbleDungeonBuffs(session *gameSession, runtime *runtimeDungeonState) error {
	if runtime.attendanceMarbleBuffsLoaded || currentAttendanceMarbleTrainingRoom(runtime) {
		return nil
	}
	// No event marker means ordinary entry needs no additional asset transaction.
	pending := false
	for i := 29; i <= 32; i++ {
		pending = pending || runtime.Character.Stats[attendanceMarbleBuffKey(i)] > 0
	}
	if !pending {
		runtime.attendanceMarbleBuffsLoaded = true
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	mask, err := onlineevent.ConsumeMarbleDungeonBuffs(ctx, repos, s.accountIDForSession(session), strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return err
	}
	runtime.attendanceMarbleBuffs, runtime.attendanceMarbleBuffsLoaded = mask, true
	for i := 29; i <= 32; i++ {
		if runtime.Character.Stats[attendanceMarbleBuffKey(i)] > 0 {
			runtime.Character.Stats[attendanceMarbleBuffKey(i)]--
		}
	}
	return nil
}

func (s *Service) handleAttendanceMarbleDoubleExit(session *gameSession, body []byte) error {
	if len(body) != 0 {
		return nil
	}
	// Opcode 641 has no native response parser.
	if _, err := s.settleAttendanceMarble(session, nil, true); err != nil {
		s.logGameEvent(session, "大富翁双倍机会关闭暂缓", "reason", err)
	}
	return nil
}

func secureAttendanceMarbleDice() ([2]byte, error) {
	var dice [2]byte
	for i := range dice {
		value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(6))
		if err != nil {
			return dice, err
		}
		dice[i] = byte(value.Int64() + 1)
	}
	return dice, nil
}

func (s *Service) currentAttendanceMarbleCatalog() (*attendanceMarbleCatalog, error) {
	s.attendanceMarbleMu.Lock()
	defer s.attendanceMarbleMu.Unlock()
	if s.attendanceMarbleCatalog != nil {
		return s.attendanceMarbleCatalog, nil
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return nil, nil
	}
	text, err := archive.ReadText(attendanceMarblePVFPath)
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse(attendanceMarblePVFPath, text)
	if err != nil {
		return nil, err
	}
	c, err := parseAttendanceMarbleCatalog(doc)
	if err != nil {
		return nil, err
	}
	s.attendanceMarbleCatalog = c
	return c, nil
}

func parseAttendanceMarbleCatalog(doc *dnfpvf.Document) (*attendanceMarbleCatalog, error) {
	return onlineevent.ParseMarbleCatalog(doc)
}

func (s *Service) settleAttendanceMarble(session *gameSession, dice *[2]byte, closeDouble bool) (attendanceMarbleState, error) {
	if s == nil || session == nil || session.selectedCharacterID == 0 {
		return attendanceMarbleState{}, onlineevent.ErrOwnerUnavailable
	}
	if !s.currentAttendanceMarbleEnabled() {
		return attendanceMarbleState{}, fmt.Errorf("大富翁活动尚未启用")
	}
	c, err := s.currentAttendanceMarbleCatalog()
	if err != nil {
		return attendanceMarbleState{}, err
	}
	repos, ok := s.repositoryGroup()
	if !ok || c == nil {
		return attendanceMarbleState{}, onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	now := s.gameplayNow()
	characterID := strconv.Itoa(int(session.selectedCharacterID))
	return onlineevent.SettleMarble(ctx, repos, c, onlineevent.MarbleCommand{
		AccountID: s.accountIDForSession(session), CharacterID: characterID, Now: now, Dice: dice, CloseDouble: closeDouble,
		Deliver: func(mailboxes dnfrepo.MailboxRepository, reward onlineevent.AttendancePVFItemReward, key string) (string, error) {
			items, err := s.currentPVFItemCatalog()
			if err != nil {
				return "", err
			}
			definition, err := items.ResolveItem(uint32(reward.ItemID))
			if err != nil {
				return "", err
			}
			definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
			if err != nil {
				return "", err
			}
			if definition.Kind != dungeonDropItemStackable || (!definition.ExpirationDate.IsZero() && !definition.ExpirationDate.After(now)) {
				return "", fmt.Errorf("marble reward %d expired or unsupported", reward.ItemID)
			}
			mailID, err := dnfrepo.AppendSystemMail(ctx, mailboxes, dnfrepo.SystemMailDelivery{
				RecipientCharacterID: characterID, Title: "幸运热舞派对奖励", Body: "热舞奖励已发放，请查收附件。", Source: "attendance_marble", CompletionKey: key, CreatedAt: now,
				Attachments: []dnfrepo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count, ExpireAt: definition.ExpirationDate}},
			})
			if err != nil {
				return "", err
			}
			mailbox, found, err := mailboxes.Load(ctx, characterID)
			if err != nil {
				return "", err
			}
			if !found {
				return "", onlineevent.ErrMarbleState
			}
			mail := mailbox.Mails[mailID]
			mail.ExpireAt = mail.CreatedAt.Add(time.Duration(c.PostalDays) * 24 * time.Hour)
			delete(mail.Metadata, "unlimited")
			mailbox.Mails[mailID] = mail
			return mailID, dnfrepo.SaveMailboxFields(ctx, mailboxes, mailbox, dnfrepo.MailboxFieldMails)
		},
	})
}
