package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 当前原生op5的第8个可选字节是288参与标记，第7个仍属于决斗专用创建。
// 兼容已有八字节创建尾。第9字节重复客户端选中值，普通模式不据此切换职业。
func currentLevelChallengeCreation(options []byte) (bool, error) {
	if len(options) < 8 || options[7] == 0 {
		return false, nil
	}
	if (len(options) != 8 && len(options) != 9) || options[7] != 1 {
		return false, fmt.Errorf("升级大挑战创建标记无效")
	}
	for i, b := range options {
		// 0x36C3650无条件返回选中值；0x36C3500才按决斗开关返回该值或FF。
		// 未启用决斗时，第9字节可能保留之前的选择，不能把它当作独立模式。
		if i == 8 {
			continue
		}
		// 原生普通创建的决斗模式槽为0xFF；旧八字节明文请求也允许0。
		if i == 6 && b == 0xff {
			continue
		}
		if i != 7 && b != 0 {
			return false, fmt.Errorf("升级大挑战与其他创建模式冲突")
		}
	}
	return true, nil
}

func (s *Service) currentLevelChallengeCatalog(ctx context.Context) (*onlineevent.LevelChallengeCatalog, error) {
	s.levelChallengeMu.Lock()
	defer s.levelChallengeMu.Unlock()
	if s.levelChallengeCatalog != nil {
		return s.levelChallengeCatalog, nil
	}
	s.initialEquipmentMu.Lock()
	archive := s.initialEquipmentArchive
	s.initialEquipmentMu.Unlock()
	if archive == nil {
		return nil, nil
	}
	c, err := onlineevent.LoadLevelChallengeCatalog(ctx, archive)
	if err == nil {
		c.Window = s.currentPortedPolicy().LevelAiming
		s.levelChallengeCatalog = c
	}
	return c, err
}

// class0/815：历史创建数量、活动角色数量、u32选角显示序号（不是角色ID或SQL槽位）。
func buildCurrentLevelChallengeRoster(state onlineevent.LevelChallengeState, characters []repo.CharacterRecord, available bool) []byte {
	indexes := []uint32{}
	for index, ch := range characters {
		if index >= rosterCount(characters) {
			break
		}
		if _, ok := state[ch.CharacterID]; !ok || ch.Stats["delete_flag"] != 0 || ch.Stats[onlineevent.LevelChallengeRoleStat] != 1 {
			continue
		}
		indexes = append(indexes, uint32(index))
	}
	var w packetWriter
	// 255 is the native unavailable sentinel. Historical deleted roles keep
	// their receipts, while automatic mode can exceed the byte display range.
	count := byte(min(len(state), 254))
	if !available {
		count = 255
	}
	w.writeByte(count)
	w.writeByte(byte(len(indexes)))
	for _, index := range indexes {
		w.writeUint32(index)
	}
	return w.bytes()
}

// 新手首场景走地下城，不能等城镇在线时钟初始化后才发816。
// 这里仅通知本次选中角色的参与状态；邮件仍由原来的结算入口处理。
func (s *Service) sendCurrentLevelChallengeSelectedState(session *gameSession) error {
	if !s.currentPortedActivityEnabled(288) || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	id := session.selectedCharacterID
	if !s.currentPortedSessionCurrent(session, id) {
		return onlineevent.ErrOwnerUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	c, err := s.currentLevelChallengeCatalog(ctx)
	if err != nil {
		return err
	}
	r, ok := s.repositoryGroup()
	if c == nil || !ok || r.Account == nil || r.Character == nil {
		return onlineevent.ErrOwnerUnavailable
	}
	account, found, err := r.Account.Load(ctx, s.accountIDForSession(session))
	if err != nil {
		return err
	}
	if !found {
		return onlineevent.ErrOwnerUnavailable
	}
	ch, found, err := r.Character.Load(ctx, strconv.Itoa(int(id)))
	if err != nil {
		return err
	}
	if !found || ch.AccountID != account.AccountID || ch.Stats["delete_flag"] != 0 {
		return onlineevent.ErrOwnerUnavailable
	}
	state, err := onlineevent.ReadLevelChallengeState(account)
	if err != nil {
		return err
	}
	_, registered := state[ch.CharacterID]
	body, err := buildCurrentLevelChallengeState(registered && ch.Stats[onlineevent.LevelChallengeRoleStat] == 1 && c.Window.ActiveAt(s.gameplayNow()), c)
	if err != nil {
		return err
	}
	if !s.currentPortedSessionCurrent(session, id) {
		return onlineevent.ErrOwnerUnavailable
	}
	return s.sendGameUpperRawClass(session, 816, body, 0)
}

// class0/816第二字段供max_level_reward_count显示，绝不是角色ID。
func buildCurrentLevelChallengeState(registered bool, c *onlineevent.LevelChallengeCatalog) ([]byte, error) {
	var count int64
	for _, row := range c.Main {
		count += row.Item.Count
	}
	if count < 0 || count > 0x7fffffff {
		return nil, fmt.Errorf("升级大挑战满级奖励数量溢出")
	}
	var w packetWriter
	flag := byte(0)
	if registered {
		flag = 1
	}
	w.writeByte(flag)
	w.writeUint32(uint32(count))
	return w.bytes(), nil
}

func (s *Service) sendCurrentLevelChallengeSelector(session *gameSession) error {
	if !s.currentPortedActivityEnabled(288) || session == nil || session.selectedCharacterID != 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	c, err := s.currentLevelChallengeCatalog(ctx)
	if err != nil {
		return err
	}
	if c == nil {
		return onlineevent.ErrOwnerUnavailable
	}
	r, ok := s.repositoryGroup()
	if !ok || r.Account == nil || r.Character == nil {
		return onlineevent.ErrOwnerUnavailable
	}
	account, found, err := r.Account.Load(ctx, s.accountIDForSession(session))
	if err != nil {
		return err
	}
	if !found {
		return onlineevent.ErrOwnerUnavailable
	}
	state, err := onlineevent.ReadLevelChallengeState(account)
	if err != nil {
		return err
	}
	characters, err := s.listCharacters(ctx, r, account.AccountID)
	if err != nil {
		return err
	}
	if err = s.sendCurrentPortedSelectorCatalog(session); err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, 815, buildCurrentLevelChallengeRoster(state, characters, c.Window.ActiveAt(s.gameplayNow())), 0)
}

func (s *Service) sendCurrentLevelChallenge(session *gameSession) error {
	if !s.currentPortedActivityEnabled(288) || session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentSpendTimeWriteTimeout)
	defer cancel()
	c, err := s.currentLevelChallengeCatalog(ctx)
	if err != nil {
		return err
	}
	if c == nil {
		return onlineevent.ErrOwnerUnavailable
	}
	r, ok := s.repositoryGroup()
	if !ok {
		return onlineevent.ErrOwnerUnavailable
	}
	id := strconv.Itoa(int(session.selectedCharacterID))
	main := s.currentPortedMailDelivery(ctx, session, id, c.MainSender, c.MainMessage, "level_challenge_288")
	bonus := s.currentPortedMailDelivery(ctx, session, id, c.BonusSender, c.BonusMessage, "level_challenge_288")
	result, err := onlineevent.SettleLevelChallenge(ctx, r, c, onlineevent.LevelChallengeCommand{
		AccountID: s.accountIDForSession(session), CharacterID: id, Now: s.gameplayNow(),
		Validate: s.currentPortedRequestValidator(session, session.selectedCharacterID, false),
		Deliver: func(m repo.MailboxRepository, reward onlineevent.AttendancePVFItemReward, key string, isBonus bool) (string, error) {
			if isBonus {
				return bonus(m, reward, key)
			}
			return main(m, reward, key)
		},
	})
	if err != nil {
		return err
	}
	body, err := buildCurrentLevelChallengeState(result.Registered && c.Window.ActiveAt(s.gameplayNow()), c)
	if err != nil {
		return err
	}
	if err = s.sendGameUpperRawClass(session, 816, body, 0); err != nil {
		return err
	}
	if len(result.MailIDs) > 0 {
		return s.sendMailboxAlarmForSession(session, session.selectedCharacterID, "level_challenge_288")
	}
	return nil
}
