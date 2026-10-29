package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guardianshield"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type currentTagTournamentShieldProjection struct {
	deck                  guardianshield.Deck
	quest                 repo.QuestRecord
	questRead, questFound bool
}

// 24F9424..24F9486的五个u32交给守护者19B32C0及盾牌窗口611，
// 与class0/580相同；此前将其称为缔造者能量的判断不成立。
// 这里只读每名角色已保存且已解锁的盾栏，不生成或交换任何装备。
func (s *Service) prepareCurrentTagTournamentShield(ctx context.Context, g repo.Group, row *currentTagTournamentCharacterProjection) error {
	grow, err := guardianshield.EligibleGrowType(row.character)
	if errors.Is(err, guardianshield.ErrCharacterIneligible) {
		return nil
	}
	if err != nil {
		return err
	}
	deck, err := guardianshield.LoadDeck(row.character)
	if err != nil {
		return err
	}
	projection := &currentTagTournamentShieldProjection{deck: deck}
	var resolver alignedcmd.GuardianShieldResolver
	if deck != (guardianshield.Deck{}) {
		resolver, err = s.newCurrentGuardianShieldResolver(ctx)
		if err != nil {
			return err
		}
	}
	seen := map[uint32]bool{}
	for _, id := range deck {
		if id == 0 {
			continue
		}
		if seen[id] {
			return fmt.Errorf("帝国竞技场守护者盾栏重复")
		}
		seen[id] = true
		resolution, err := resolver(ctx, id, grow)
		if err != nil {
			return err
		}
		if resolution.ItemID != id {
			return fmt.Errorf("帝国竞技场盾栏物品与PVF目录不符")
		}
		if resolution.UnlockKind == alignedcmd.GuardianShieldUnlockQuest && !projection.questRead {
			if g.Quest == nil {
				return guardianshield.ErrQuestRepositoryRequired
			}
			r, found, e := g.Quest.Load(ctx, row.character.CharacterID)
			if e != nil {
				return e
			}
			if found && r.CharacterID != row.character.CharacterID {
				return fmt.Errorf("帝国竞技场盾栏任务不属于该队员")
			}
			projection.quest, projection.questRead, projection.questFound = repo.CloneQuest(r), true, found
		}
		quests := projection.quest
		if !projection.questFound {
			quests = repo.QuestRecord{CharacterID: row.character.CharacterID}
		}
		if err := guardianshield.ValidateUnlock(row.character, quests, resolution); err != nil {
			return err
		}
	}
	main, equipped := guardianshield.MainProjectionItemID(row.equipment)
	if deck[0] != 0 && (!equipped || main != deck[0]) || deck[0] == 0 && equipped {
		return fmt.Errorf("帝国竞技场主盾穿戴与该队员盾栏不一致")
	}
	row.shield = projection
	return nil
}

func currentTagTournamentShieldSourceCurrent(ctx context.Context, g repo.Group, row currentTagTournamentCharacterProjection) error {
	if row.shield == nil || !row.shield.questRead {
		return nil
	}
	if g.Quest == nil {
		return guardianshield.ErrQuestRepositoryRequired
	}
	r, found, err := g.Quest.Load(ctx, row.character.CharacterID)
	if err != nil {
		return err
	}
	if found != row.shield.questFound || !reflect.DeepEqual(r, row.shield.quest) {
		return fmt.Errorf("帝国竞技场准备期间盾栏解锁任务变化，请重试")
	}
	return nil
}
