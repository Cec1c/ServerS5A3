package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	repo "longheng.io/server/internal/modules/dnf/repository"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"strconv"
)

// 现用90原生C2S/38：u8,u16技能ID,u16,u16,u8,u16等级。其余字段仅保留为不透明数据；无新增请求或EXE改写。
func decodeSeriaColorlessSkill(body []byte) (id, level uint16, ok bool) {
	if len(body) != 10 {
		return 0, 0, false
	}
	id = binary.LittleEndian.Uint16(body[1:3])
	level = binary.LittleEndian.Uint16(body[8:10])
	return id, level, id > 0 && level > 0
}

// 消费原生施放通知；无色固定扣量和新手回归次数共同提交，MP沿用原处理。
func (s *Service) handleCurrentSeriaColorlessUseSkill(session *gameSession, request gameplayRequest) error {
	if session == nil || !requireDefaultGameplayClassification(s, session, request, "新手回归技能任务忽略", "技能消息分类不符") {
		return nil
	}
	id, _, ok := decodeSeriaColorlessSkill(request.Body)
	if !ok || request.Sequence == nil {
		return nil
	}
	bound, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || !s.currentPortedSessionCurrent(session, bound.character) {
		return nil
	}
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	r := session.dungeon.runtime
	if r == nil || r.Session == nil || r.Room == nil || r.ordinaryFinalRoomClearAccepted || r.tutorialFinalRoomClearAccepted || r.townReturnPending || r.AccountID != s.accountIDForSession(session) || !dungeonRuntimeOwnsCharacter(r, bound.character) {
		return nil
	}
	if r.seriaSkillSequenceSet && *request.Sequence <= r.seriaSkillSequence {
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
	if !def.Active || def.IsTPSkill() || !def.UsesColorlessCube || def.ColorlessCubeCount <= 0 {
		return nil
	}
	g, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("新手回归技能缺少仓储事务")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	characterID := strconv.Itoa(int(bound.character))
	now := s.gameplayNow()
	if currentCrystalContractPracticeRuntime(r) {
		return nil
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%s", session.connID, r.lifecycleToken, *request.Sequence, characterID)))
	result, err := dnfskill.ConsumeColorlessSkill(ctx, g, dnfskill.ColorlessUseCommand{
		AccountID: s.accountIDForSession(session), CharacterID: characterID, EventID: fmt.Sprintf("skill:%x", hash), Definition: def, DungeonID: r.Dungeon.ID, At: now,
		Current: func() bool { return !session.loginRevoked.Load() && s.boundGameSessionCharacterCurrent(bound) },
	})
	if err != nil {
		if errors.Is(err, dnfskill.ErrColorlessSkillInsufficient) {
			r.seriaSkillSequence = *request.Sequence
			r.seriaSkillSequenceSet = true
			_ = s.sendCurrentAccountCubeInventoryUpdate(session, repo.ColorlessCubeItemID, result.Slot, result.Remaining)
		}
		s.logGameEvent(session, "技能无色扣除未提交", "skill_id", id, "error", err)
		return nil
	}
	if result.Accepted {
		r.seriaSkillSequence = *request.Sequence
		r.seriaSkillSequenceSet = true
		s.logGameEvent(session, "技能无色扣除已提交", "skill_id", id, "amount", result.Spent, "remaining", result.Remaining, "replayed", result.Replayed)
		return s.sendCurrentAccountCubeInventoryUpdate(session, repo.ColorlessCubeItemID, result.Slot, result.Remaining)
	}
	return nil
}
