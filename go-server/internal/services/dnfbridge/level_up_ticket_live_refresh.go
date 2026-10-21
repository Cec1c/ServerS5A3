package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// S4A21的城镇升级先更新USERINFO subtype0、subtype1，再发送EXP。
// 当前90端保留自己的418读取契约，角色资料则使用同一已提交快照和已有
// 场景owner组包器；不重走选角/转场，也不借用其它版本的短应答。
func (s *Service) sendCurrentLevelUpTicketActorRefresh(ctx context.Context, p *gameSession, identity boundGameSessionCharacter, r inventory.LevelUpTicketResult) error {
	if !s.boundGameSessionCharacterCurrent(identity) || r.Character.CharacterID != strconv.Itoa(int(identity.character)) {
		return fmt.Errorf("升级券角色资料刷新绑定已变化")
	}
	owner := currentSelectedActorAppearanceOwnerContext(p)
	mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, p, identity.character, r.Character.Name, r.Character, true, owner)
	if err != nil {
		return err
	}
	var legacy dnfrepo.LegacyUserInfoRepository
	if repositories, ok := s.repositoryGroup(); ok {
		legacy = repositories.LegacyUserInfo
	}
	mode1 := s.buildCurrentSelectedUserInfoMode1BodyInContext(ctx, p, legacy, r.Character, true, identity.character, owner)
	if len(mode1) <= currentMode1ObjectTailOffset {
		return fmt.Errorf("升级券本体属性刷新正文缺失")
	}
	if !s.boundGameSessionCharacterCurrent(identity) || owner != currentSelectedActorAppearanceOwnerContext(p) {
		return fmt.Errorf("升级券角色资料加载期间场景绑定已变化")
	}
	s.logGameEvent(p, "升级券场景角色资料刷新", "character_id", identity.character, "level", r.Character.Level,
		"owner", owner, "mode0_length", len(mode0), "mode1_length", len(mode1), "sequence", "op418_op14_mode0_mode1_op37_op19_quests")
	for _, body := range [][]byte{mode0, mode1} {
		if err := s.sendGameUpperRawClass(p, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0); err != nil {
			return err
		}
	}
	// 完整mode1会重新应用装备对象，沿用既有同实例恢复，保留宠物与称号附魔。
	creature, err := s.currentEquippedCreatureForCharacterWithError(ctx, r.Character.CharacterID)
	if err != nil {
		return err
	}
	if creature.valid() {
		if err := s.sendSelectedEquippedCreatureItemRefresh(p, "level_up_ticket_after_actor_mode1"); err != nil {
			return err
		}
	}
	if err := s.sendSelectedWornTitleEnchantRowRefresh(p, "level_up_ticket_after_actor_mode1"); err != nil {
		return err
	}
	return s.sendBuffSwapReapply(p, "level_up_ticket_after_actor_mode0_mode1")
}
