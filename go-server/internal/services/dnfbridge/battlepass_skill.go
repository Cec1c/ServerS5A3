package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"path"
	"strconv"
	"strings"
)

// 现用90原生C2S/38：u8,u16技能ID,u16,u16,u8,u16等级。其余字段仅保留为不透明数据；无新增请求或EXE改写。
func decodeBattlePassSkill(body []byte) (id, level uint16, ok bool) {
	if len(body) != 10 {
		return 0, 0, false
	}
	id = binary.LittleEndian.Uint16(body[1:3])
	level = binary.LittleEndian.Uint16(body[8:10])
	return id, level, id > 0 && level > 0
}

func battlePassSkillKind(def dnfskill.Skill) uint32 {
	if !def.Active || def.IsTPSkill() {
		return 0
	}
	if strings.EqualFold(path.Base(strings.ReplaceAll(def.Path, "\\", "/")), "quickstanding.skl") {
		return 4
	}
	if def.UsesColorlessCube {
		return 15
	}
	return 0
}

// 消费原生施放通知；无色固定扣量、实际账本和战令次数共同提交，MP沿用原处理。
func (s *Service) handleCurrentBattlePassUseSkill(session *gameSession, request gameplayRequest) error {
	if session == nil || !requireDefaultGameplayClassification(s, session, request, "战令技能任务忽略", "技能消息分类不符") {
		return nil
	}
	id, _, ok := decodeBattlePassSkill(request.Body)
	if !ok || request.Sequence == nil {
		return nil
	}
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	r := session.dungeon.runtime
	if r == nil || r.Session == nil || r.Room == nil || r.ordinaryFinalRoomClearAccepted || r.tutorialFinalRoomClearAccepted || r.townReturnPending || r.AccountID != identity.AccountID || !dungeonRuntimeOwnsCharacter(r, bound.character) {
		return nil
	}
	if r.battlePassSkillSequenceSet && *request.Sequence <= r.battlePassSkillSequence {
		return nil
	}
	high, initialized := session.guild.purchaseSequence.currentHigh()
	if !initialized || *request.Sequence != high {
		return nil
	}
	job, ok := characterJobByte(r.Character)
	if !ok {
		return nil
	}
	s.initialSkillsMu.Lock()
	catalog := s.skillCatalog
	s.initialSkillsMu.Unlock()
	def, found := catalog.Find(job, id)
	if !found {
		return nil
	}
	kind := battlePassSkillKind(def)
	if kind == 0 {
		return nil
	}
	g, ok := s.repositoryGroup()
	if !ok || g.CharacterProgression == nil {
		return fmt.Errorf("战令技能缺少角色技能事务")
	}
	if err := s.flushCurrentDungeonDeathBurstLocked(session); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(session.ctx, createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(bound.character))
	now := s.gameplayNow()
	if kind == 15 {
		if currentCrystalContractPracticeRuntime(r) {
			return nil
		}
		hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%s", session.connID, r.lifecycleToken, *request.Sequence, characterID)))
		result, err := dnfskill.ConsumeColorlessSkill(ctx, g, dnfskill.ColorlessUseCommand{
			AccountID: identity.AccountID, CharacterID: characterID, EventID: fmt.Sprintf("skill:%x", hash), Definition: def, DungeonID: r.Dungeon.ID, At: now,
			Current: func() bool { return !session.loginRevoked.Load() && s.boundGameSessionCharacterCurrent(bound) },
		})
		if err != nil {
			if errors.Is(err, dnfskill.ErrColorlessSkillInsufficient) {
				r.battlePassSkillSequence = *request.Sequence
				r.battlePassSkillSequenceSet = true
				_ = s.sendCurrentAccountCubeInventoryUpdate(session, repo.ColorlessCubeItemID, result.Slot, result.Remaining)
			}
			s.logGameEvent(session, "技能无色扣除未提交", "skill_id", id, "error", err)
			return nil
		}
		if result.Accepted {
			r.battlePassSkillSequence = *request.Sequence
			r.battlePassSkillSequenceSet = true
			s.logGameEvent(session, "技能无色扣除已提交", "skill_id", id, "amount", result.Spent, "remaining", result.Remaining, "replayed", result.Replayed)
			return s.sendCurrentAccountCubeInventoryUpdate(session, repo.ColorlessCubeItemID, result.Slot, result.Remaining)
		}
		return nil
	}
	accepted := false
	err := g.CharacterProgression.WithinCharacterProgression(ctx, characterID, func(chars repo.CharacterRepository, skills repo.SkillRepository) error {
		if session.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(bound) {
			return nil
		}
		ch, found, err := chars.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != identity.AccountID || ch.CharacterID != r.Character.CharacterID {
			return nil
		}
		currentJob, ok := characterJobByte(ch)
		if !ok || currentJob != job {
			return nil
		}
		record, found, err := skills.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		learned := record.Skills[int64(id)]
		if !learned.Enabled || learned.Level <= 0 || ch.Level < def.RequiredLevel {
			return nil
		}
		ch = repo.CloneCharacter(ch)
		param := int64(3037)
		if kind == 4 {
			param = 190
		} // 配置中的通用受身蹲伏任务参数，职业技能由PVF路径确认。
		repo.AddBattlePassCounter(&ch, kind, param, 1, now)
		ch.UpdatedAt = now
		if err := repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	if err != nil {
		s.logGameEvent(session, "战令技能计数失败", "skill_id", id, "error", err)
		return nil
	}
	if accepted {
		r.battlePassSkillSequence = *request.Sequence
		r.battlePassSkillSequenceSet = true
		s.logGameEvent(session, "战令技能计数成功", "skill_id", id, "kind", kind)
	}
	return nil
}
