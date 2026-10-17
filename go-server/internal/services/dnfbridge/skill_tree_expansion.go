package dnfbridge

import (
	"context"
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

const currentSkillTreeExpansionItemID uint32 = 821

func currentIsSkillTreeExpansionItem(definition dungeonDropItemDefinition) bool {
	return definition.ItemID == currentSkillTreeExpansionItemID && definition.Kind == dungeonDropItemStackable &&
		strings.EqualFold(strings.ReplaceAll(definition.PVFPath, "\\", "/"), "stackable/cash/skilltree_plusitem.stk")
}

func (s *Service) currentSkillTreeExpansionRules(ctx context.Context, repositories dnfrepo.Group, characterID uint16) (skillcmd.OwnerOptions, error) {
	catalog, initial, points := s.skillResetOwnerRules(ctx, repositories, characterID)
	if catalog == nil || initial == nil || points == nil {
		return skillcmd.OwnerOptions{}, fmt.Errorf("技能类型扩展所需的职业初始技能或SP/TP配置未就绪")
	}
	return skillcmd.OwnerOptions{Catalog: catalog, InitialLevels: initial, PointBaseline: points}, nil
}

// 只从技能仓储已提交的购买资格投影，不信任历史镜像表的默认0。
func (r *csharpLegacyUserInfoReader) skillTreeWireIndex(character dnfrepo.CharacterRecord) byte {
	if r == nil || r.service == nil {
		return 0xff
	}
	repos, ok := r.service.repositoryGroupFor(r.ctx)
	if !ok || repos.Skill == nil {
		return 0xff
	}
	record, found, err := repos.Skill.Load(r.ctx, character.CharacterID)
	if err != nil || !found {
		return 0xff
	}
	return dnfrepo.SkillTreeWireIndex(record)
}

func (s *Service) currentSendSkillTreeExpansionRefresh(ctx context.Context, session *gameSession) error {
	if session == nil || session.selectedCharacterID == 0 {
		return errCurrentCeraShopOwnerUnavailable
	}
	charID, name, character, found := s.selectedCharacterForEnter(ctx, session)
	if !found || charID != session.selectedCharacterID {
		return errCurrentCeraShopOwnerUnavailable
	}
	ownerChannel := currentTownActorOwnerContext(session)
	if ownerChannel == currentSceneObjectContext {
		return fmt.Errorf("技能扩展刷新等待角色场景就绪")
	}
	body, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, session, charID, name, character, true, ownerChannel)
	if err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, uint16(dnfenum.CmdPacketSetUDPIPPort), body, 0); err != nil {
		return err
	}

	return s.sendSelectedActorCurrentSceneSkillInfo(session, "购买技能类型扩展后刷新")
}

// 仅接受由持久化技能记录投影的明确资格，历史默认页码不解锁。
func currentActorSkillTreeWireIndex(character dnfrepo.CharacterRecord) byte {
	if character.Stats["skill_tree_expanded"] != 1 {
		return 0xff
	}
	index, present := character.Stats["skill_tree_index"]
	if !present || index < 0 || index > 1 {
		return 0xff
	}
	return byte(index)
}
