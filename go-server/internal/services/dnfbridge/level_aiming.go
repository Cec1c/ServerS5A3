package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) currentLevelAimingCatalog(ctx context.Context) (*dnfonlineevent.LevelAimingCatalog, error) {
	s.levelAimingMu.Lock()
	defer s.levelAimingMu.Unlock()
	if s.levelAimingCatalog != nil {
		return s.levelAimingCatalog, nil
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return nil, nil
	}
	catalog, err := dnfonlineevent.LoadLevelAimingCatalog(ctx, archive)
	if err == nil {
		p := s.currentPortedPolicy()
		catalog.Starts, catalog.Ends = p.LevelAiming.Starts, p.LevelAiming.Ends
		s.levelAimingCatalog = catalog
	}
	return catalog, err
}

// 当前客户端的预约状态读取u32角色ID和八个字节；倒数第二字节决定界面状态。
func buildCurrentLevelAimingBody(state dnfonlineevent.LevelAimingState, character dnfrepo.CharacterRecord, selector bool) []byte {
	var w packetWriter
	id, _ := strconv.ParseUint(state.CharacterID, 10, 32)
	w.writeUint32(uint32(id))
	start, target, status, job := byte(1), byte(255), byte(255), byte(0)
	if state.CharacterID != "" && state.CharacterID == character.CharacterID {
		start = byte(state.Start)
		if state.Target != 0 {
			target = byte(state.Target)
		}
		job = packRosterJobGrow(byte(numericCharacterStat(character.Job)), byte(numericCharacterStatValue(character, "grow_type")))
		status = 3
		// 选角同步使用状态1，避免原生处理函数自动进入角色；预约应答使用状态3。
		if selector {
			status = 1
		}
	}
	w.writeByte(start)
	w.writeByte(target)
	w.writeByte(0)
	w.writeByte(0)
	w.writeByte(job)
	w.writeByte(0)
	w.writeByte(status)
	w.writeByte(byte(state.Reservations))
	return w.bytes()
}

func (s *Service) settleCurrentLevelAiming(ctx context.Context, session *gameSession, catalog *dnfonlineevent.LevelAimingCatalog, id uint16, register bool, target uint32) (dnfonlineevent.LevelAimingResult, error) {
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfonlineevent.LevelAimingResult{}, dnfonlineevent.ErrOwnerUnavailable
	}
	characterID := strconv.Itoa(int(id))
	now := s.gameplayNow()
	return dnfonlineevent.SettleLevelAiming(ctx, repos, catalog, dnfonlineevent.LevelAimingCommand{
		AccountID: s.accountIDForSession(session), CharacterID: characterID, Register: register, Target: target, Now: now,
		Validate: s.currentPortedRequestValidator(session, id, true),
		Deliver:  s.currentPortedMailDelivery(ctx, session, characterID, "Lv预约升级奖励", "预约升级奖励已发放，请查收附件。", "level_aiming_222"),
	})
}

func (s *Service) sendCurrentLevelAiming(session *gameSession) error {
	if !s.currentPortedActivityEnabled(222) {
		return nil
	}
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, err := s.currentLevelAimingCatalog(ctx)
	if err != nil || catalog == nil {
		return err
	}
	result, err := s.settleCurrentLevelAiming(ctx, session, catalog, session.selectedCharacterID, false, 0)
	if err != nil {
		return err
	}
	if err = s.sendGameUpperRawClass(session, 623, buildCurrentLevelAimingBody(result.LevelAimingState, result.Character, false), 0); err != nil {
		return err
	}
	if len(result.MailIDs) > 0 {
		return s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "level_aiming")
	}
	return nil
}

func (s *Service) sendCurrentLevelAimingSelector(session *gameSession) error {
	if !s.currentPortedActivityEnabled(222) {
		return nil
	}
	if session == nil || session.selectedCharacterID != 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	catalog, err := s.currentLevelAimingCatalog(ctx)
	if err != nil || catalog == nil {
		return err
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Account == nil || repos.Character == nil {
		return dnfonlineevent.ErrOwnerUnavailable
	}
	account, found, err := repos.Account.Load(ctx, s.accountIDForSession(session))
	if err != nil {
		return err
	}
	if !found {
		return dnfonlineevent.ErrOwnerUnavailable
	}
	state, err := dnfonlineevent.ReadLevelAimingState(account)
	if err != nil {
		return err
	}
	var ch dnfrepo.CharacterRecord
	if state.CharacterID != "" {
		ch, found, err = repos.Character.Load(ctx, state.CharacterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != account.AccountID {
			ch = dnfrepo.CharacterRecord{}
		}
	}
	if err := s.sendCurrentPortedSelectorCatalog(session); err != nil {
		return err
	}
	if !(dnfonlineevent.ActivityWindow{Starts: catalog.Starts, Ends: catalog.Ends}).ActiveAt(s.gameplayNow()) {
		// A nonzero gate prevents new event creation outside the configured dates.
		state.CharacterID = "65535"
		ch = dnfrepo.CharacterRecord{}
	}
	return s.sendGameUpperRawClass(session, 623, buildCurrentLevelAimingBody(state, ch, true), 0)
}

func (s *Service) handleCurrentLevelAiming(session *gameSession, body []byte) error {
	fail := func(err error) error {
		s.logGameEvent(session, "预约升级请求拒绝", "error", err, "body_len", len(body))
		return s.sendGameUpperRawClass(session, 689, buildCurrentLevelAimingResponse(false), 1)
	}
	if session == nil || len(body) != 16 || binary.LittleEndian.Uint32(body) != 222 || binary.LittleEndian.Uint32(body[12:]) != 0 {
		return fail(fmt.Errorf("invalid level aiming request"))
	}
	reference, target := binary.LittleEndian.Uint32(body[4:]), binary.LittleEndian.Uint32(body[8:])
	register := target == 255
	if target == 0 || (register && (reference == 0 || reference > 65535 || session.selectedCharacterID != 0 || session.levelAimingCreatedID != uint16(reference))) || (!register && reference >= defaultCharacterSlots) {
		return fail(fmt.Errorf("level aiming session/character mismatch"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	id := uint16(reference)
	if !register {
		// 新建预约角色传角色ID；选择目标时传选角槽位，城镇入口也沿用槽位。
		// 不能把找不到的槽位当作角色ID再尝试。
		repos, ok := s.repositoryGroup()
		if !ok || repos.Character == nil {
			return fail(dnfonlineevent.ErrOwnerUnavailable)
		}
		accountID := s.accountIDForSession(session)
		characters, err := repos.Character.ListByAccount(ctx, accountID, dnfrepo.DefaultCharacterSlotLimit)
		if err != nil {
			return fail(err)
		}
		character, _, found := selectCharacterRecord(characters, nil, selectCharacterRequest{slot: int(reference)})
		parsedID, err := strconv.ParseUint(character.CharacterID, 10, 16)
		if !found || character.AccountID != accountID || err != nil || parsedID == 0 {
			return fail(fmt.Errorf("level aiming slot %d has no owned character", reference))
		}
		id = uint16(parsedID)
		if session.selectedCharacterID != 0 && session.selectedCharacterID != id {
			return fail(fmt.Errorf("level aiming slot %d does not match selected character", reference))
		}
	}
	catalog, err := s.currentLevelAimingCatalog(ctx)
	if err != nil {
		return fail(err)
	}
	if catalog == nil {
		return fail(dnfonlineevent.ErrOwnerUnavailable)
	}
	if register {
		target = 0
	}
	result, err := s.settleCurrentLevelAiming(ctx, session, catalog, id, register, target)
	if err != nil {
		return fail(err)
	}
	if err = s.sendGameUpperRawClass(session, 689, buildCurrentLevelAimingResponse(true), 1); err != nil {
		return err
	}
	if register && session.selectedCharacterID == 0 {
		repos, _ := s.repositoryGroup()
		characters, err := s.listCharacters(ctx, repos, s.accountIDForSession(session))
		if err != nil {
			return err
		}
		if err = s.sendUpperRoster(session, characters); err != nil {
			return err
		}
	}
	if err = s.sendGameUpperRawClass(session, 623, buildCurrentLevelAimingBody(result.LevelAimingState, result.Character, false), 0); err != nil {
		return err
	}
	s.logGameEvent(session, "预约升级请求提交", "character_id", id, "request_reference", reference, "register", register, "target", result.Target, "reservations", result.Reservations, "mails", len(result.MailIDs))
	if len(result.MailIDs) > 0 && session.selectedCharacterID == id {
		return s.sendMailboxAlarmForSession(session, id, "level_aiming")
	}
	return nil
}

func buildCurrentLevelAimingResponse(success bool) []byte {
	if success {
		return []byte{1, 222, 0, 0, 0}
	}
	// class1通用分派先读取success和error，689业务回调随后读取活动ID和业务错误。
	// 省略通用error会把222的低字节吃掉，并使业务回调多读一个字节。
	return []byte{0, 1, 222, 0, 0, 0, 1}
}
