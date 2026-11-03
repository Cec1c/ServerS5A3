package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) initializePortedActivityPolicy(ctx context.Context) error {
	r, ok := s.repositoryGroup()
	if !ok {
		return nil
	}
	p, err := onlineevent.LoadPortedActivityPolicy(ctx, r.Settings)
	if err != nil {
		return fmt.Errorf("读取四项移植活动策略: %w", err)
	}
	s.portedActivityPolicy = &p
	// 劳动节使用既有目录和邮件。222/1054的原生结构已核对，活动仍默认关闭；
	// 回归使用现用1369/1429/1430契约，活动仍默认关闭，开测前核对接收许可。
	s.portedActivityNativeReady = map[uint16]bool{2348: true, 222: true, 1054: true, 288: true, 1370: true}
	if s.currentPortedActivityConfigured(288) && (s.currentPortedActivityConfigured(222) || s.currentPortedActivityConfigured(116)) {
		return fmt.Errorf("升级大挑战288不能与旧预约222/116同时开启")
	}
	if s.currentPortedActivityConfigured(222) && s.currentPortedActivityConfigured(116) {
		return fmt.Errorf("预约升级222和旧预约116不能同时开启")
	}
	return nil
}

func (s *Service) currentPortedPolicy() onlineevent.PortedActivityPolicy {
	if s != nil && s.portedActivityPolicy != nil {
		return *s.portedActivityPolicy
	}
	return onlineevent.DefaultPortedActivityPolicy()
}

func (s *Service) currentPortedActivityEnabled(id uint16) bool {
	return s.currentPortedActivityConfigured(id) && s.portedActivityNativeReady[id]
}
func (s *Service) currentPortedActivityConfigured(id uint16) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && s.portedActivityPolicy != nil && s.activityControls != nil && s.activityControls.Enabled(id)
}

func (s *Service) preloadPortedActivities(ctx context.Context) error {
	if s.currentPortedActivityConfigured(288) {
		if _, err := s.currentLevelChallengeCatalog(ctx); err != nil {
			return err
		}
	}
	if s.currentPortedActivityConfigured(1370) || s.currentPortedActivityConfigured(1054) {
		if _, _, err := s.currentReturneeLuckyCatalogs(ctx); err != nil {
			return err
		}
	}
	if s.currentPortedActivityConfigured(222) {
		if _, err := s.currentLevelAimingCatalog(ctx); err != nil {
			return err
		}
	}
	if s.currentPortedActivityConfigured(116) {
		if _, err := s.currentLegacyLevelCatalog(ctx); err != nil {
			return err
		}
	}
	if s.currentPortedActivityConfigured(2348) {
		if _, err := s.currentLaborDayCatalog(ctx); err != nil {
			return err
		}
	}
	return nil
}

// 职业编号沿用当前jobmap；仅枚举本版本的基础、转职及两次觉醒状态。
func currentAllLuckyProfessions() [][2]uint32 {
	var rows [][2]uint32
	for job := 0; job <= jobmap.MaxID; job++ {
		if jobmap.Valid(job) {
			for awaken := 0; awaken <= 2; awaken++ {
				for grow := 0; grow <= 5; grow++ {
					rows = append(rows, [2]uint32{uint32(job), uint32(awaken<<4 | grow)})
				}
			}
		}
	}
	return rows
}

func (s *Service) currentPortedSessionCurrent(session *gameSession, id uint16) bool {
	if session == nil || id == 0 || session.selectedCharacterID != id || session.loginRevoked.Load() || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return false
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	return ok && identity.character == id && s.boundGameSessionCharacterCurrent(identity)
}

func (s *Service) currentPortedReservationSessionCurrent(session *gameSession, id uint16) bool {
	if session == nil || session.loginRevoked.Load() || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return false
	}
	if session.selectedCharacterID == 0 {
		return id != 0
	}
	return s.currentPortedSessionCurrent(session, id)
}

func (s *Service) currentPortedMailDelivery(ctx context.Context, session *gameSession, id, title, body, source string) func(repo.MailboxRepository, onlineevent.AttendancePVFItemReward, string) (string, error) {
	return func(box repo.MailboxRepository, reward onlineevent.AttendancePVFItemReward, key string) (string, error) {
		if reward.ItemID <= 0 || reward.ItemID > 0xffffffff || reward.Count <= 0 || reward.Count > 0x7fffffff {
			return "", onlineevent.ErrRewardInvalid
		}
		catalog, err := s.currentPVFItemCatalog()
		if err != nil {
			return "", err
		}
		definition, err := catalog.ResolveItem(uint32(reward.ItemID))
		if err != nil {
			return "", err
		}
		now := s.gameplayNow()
		definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
		if err != nil {
			return "", err
		}
		if definition.Kind != dungeonDropItemStackable && definition.Kind != dungeonDropItemEquipment {
			return "", fmt.Errorf("活动奖励%d类型暂不支持", reward.ItemID)
		}
		stack := currentPVFGrantIdentity(definition)
		stack.Count = reward.Count
		if definition.Kind == dungeonDropItemEquipment {
			if reward.Count != 1 {
				return "", fmt.Errorf("装备奖励%d数量必须为1", reward.ItemID)
			}
			stack.Extra["equipment_type"] = definition.EquipmentType
			stack.Extra["durability"] = strconv.Itoa(int(definition.Durability))
			stack.Extra["max_durability"] = strconv.Itoa(int(definition.Durability))
			stack.Extra["seal_flag"] = strconv.Itoa(int(currentNewPVFEquipmentSealFlag(definition)))
		}
		stack, _ = applyCurrentPVFItemExpirationAt(stack, definition, now)
		return repo.AppendSystemMail(ctx, box, repo.SystemMailDelivery{RecipientCharacterID: id, Title: title, Body: body, Source: source, CompletionKey: key, CreatedAt: now,
			Attachments: []repo.MailAttachment{{ItemID: reward.ItemID, Count: reward.Count, ExpireAt: stack.ExpireAt, Extra: stack.Extra}}})
	}
}

type currentPortedCatalog struct {
	LevelAiming, LegacyLevel, LevelChallenge bool
	LaborDay                                 *onlineevent.LaborDayCatalog
}

func (c currentPortedCatalog) HasLevel(id uint16) bool {
	return id == 222 && c.LevelAiming || id == 116 && c.LegacyLevel || id == 288 && c.LevelChallenge
}
func writeCurrentPortedActivityBase(w *packetWriter, id uint16) {
	w.writeUint16(id)
	w.writeUint32(currentSpendTimeBaseCatalogValue)
	w.writeRawDstr(nil)
	w.writeRawDstr(nil)
	w.writeByte(0)
}
func (s *Service) currentPortedCatalogAt(now time.Time) currentPortedCatalog {
	p := s.currentPortedPolicy()
	c := currentPortedCatalog{LevelAiming: s.currentPortedActivityEnabled(222) && p.LevelAiming.ActiveAt(now), LegacyLevel: s.currentPortedActivityEnabled(116) && p.LevelAiming.ActiveAt(now)}
	c.LevelChallenge = s.currentPortedActivityEnabled(288) && p.LevelAiming.ActiveAt(now)
	if s.currentPortedActivityEnabled(2348) && p.LaborDay.ActiveAt(now) {
		s.laborDayMu.Lock()
		c.LaborDay = s.laborDayCatalog
		s.laborDayMu.Unlock()
	}
	return c
}

func (s *Service) sendCurrentPortedSelectorCatalog(session *gameSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	c, available, err := s.currentSpendTimeRuntimeCatalogIfAvailable(ctx)
	if err != nil {
		return err
	}
	if !available {
		return onlineevent.ErrOwnerUnavailable
	}
	send, ready := s.beginCurrentSpendTimeEventInfo(session)
	if !ready {
		return onlineevent.ErrOwnerUnavailable
	}
	if !send {
		return nil
	}
	b, _, err := s.buildCurrentCompleteActivityInfo(session, c, false)
	if err != nil {
		s.finishCurrentSpendTimeEventInfo(session, false)
		return err
	}
	b, err = zlibCompress(b)
	if err != nil {
		s.finishCurrentSpendTimeEventInfo(session, false)
		return err
	}
	err = s.sendCurrentProtectedClass0Packet(session, currentSpendTimeEventInfoMsgID, b, currentSpendTimeEventInfoCodec, "预约活动选角完整目录")
	s.finishCurrentSpendTimeEventInfo(session, err == nil)
	if err == nil {
		session.activityCatalogDayEvent = s.currentDayDungeonEvent()
	}
	return err
}

func (s *Service) handleCurrentPortedActivityRequest(session *gameSession, op uint16, class byte, body []byte) (bool, error) {
	// Newer official EXEs use 1429 for returnee guidance. The native90
	// wire profile uses 1429 for the task-manual main-story sweep instead;
	// gameplay version alone cannot select this overlapping command.
	if s.options.currentClientVersion == "official-24.9" &&
		((op == currentReturneeGuideID && !s.uses90NativeClient()) || op == currentReturneeClaimID) {
		if class != 1 {
			return true, nil
		}
		return true, s.handleCurrentReturneeCommand(session, op, body)
	}
	id := uint16(0)
	switch op {
	case 689:
		if len(body) >= 4 && binary.LittleEndian.Uint32(body) == 222 {
			id = 222
		}
	case 567, 1122:
		id = 116
	}
	if session == nil {
		return id != 0, nil
	}
	if id == 0 || !s.currentPortedActivityEnabled(id) {
		return false, nil
	}
	if class != 1 {
		return true, nil
	}
	if !s.currentPortedReservationSessionCurrent(session, session.selectedCharacterID) {
		// 选角创建登记允许零selected，仍须真实登录账号。
		if id != 222 || !s.currentPortedReservationSessionCurrent(session, 1) {
			if id == 222 {
				return true, s.sendGameUpperRawClass(session, op, buildCurrentLevelAimingResponse(false), 1)
			}
			return true, s.sendGameUpperRawClass(session, op, []byte{0, 3}, 1)
		}
	}
	switch op {
	case 689:
		return true, s.handleCurrentLevelAiming(session, body)
	default:
		return true, s.handleCurrentLegacyLevel(session, op, body)
	}
}

// 调用方是角色事件循环；在线时钟持有spendTime.mu，刷新不进入杀怪逐只事务。
func (s *Service) refreshCurrentPortedActivities(session *gameSession, force bool) {
	if s.portedActivityPolicy == nil || session == nil || session.selectedCharacterID == 0 {
		return
	}
	now := s.gameplayNow()
	if !force && now.Before(session.portedActivityNextRefresh) {
		return
	}
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"回归", func() error {
			if err := s.prepareCurrentReturnee(session); err != nil {
				return err
			}
			return s.sendCurrentReturneeState(session, force)
		}},
		{"幸运角色", func() error { return s.sendCurrentLuckyCharacterState(session) }},
		{"预约升级", func() error { return s.sendCurrentLevelAiming(session) }},
		{"升级大挑战", func() error { return s.sendCurrentLevelChallenge(session) }},
	} {
		if err := step.run(); err != nil {
			s.logGameEvent(session, "移植活动状态刷新暂缓", "活动", step.name, "error", err)
		}
	}
	session.portedActivityNextRefresh = now.Add(time.Minute)
}

func (s *Service) settlePortedPresenceLocked(ctx context.Context, session *gameSession, now time.Time, force bool) {
	if !s.currentPortedActivityEnabled(2348) || session.selectedCharacterID == 0 {
		return
	}
	from := session.laborDayAnchor
	if !force && !from.IsZero() && now.Sub(from) < time.Minute {
		return
	}
	c, err := s.currentLaborDayCatalog(ctx)
	if err == nil {
		err = s.settleCurrentLaborDay(ctx, session, c, from, now, from.IsZero())
	}
	if err != nil {
		s.logGameEvent(session, "劳动节在线结算暂缓", "error", err)
		return
	}
	session.laborDayAnchor = now
}

// 验证闭包捕获请求时的账号、选角和代次，不能在事务中重新绑定为另一个有效会话。
func (s *Service) currentPortedRequestValidator(session *gameSession, id uint16, selector bool) func() bool {
	if session == nil {
		return func() bool { return false }
	}
	generation, selected, account := session.characterGeneration, session.selectedCharacterID, session.accountID
	return func() bool {
		if session.characterGeneration != generation || session.selectedCharacterID != selected || session.accountID != account {
			return false
		}
		if selector && selected == 0 {
			return s.currentPortedReservationSessionCurrent(session, id)
		}
		return s.currentPortedSessionCurrent(session, id)
	}
}
