package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

// 现用class0/1229处理器12EAA20读取一个模式字节。
const currentPVPDojoSkillStateMsgID uint16 = 1229

func (s *Service) handleCurrentPVPDojoSkillMode(session *gameSession, body []byte) error {
	if session == nil || session.selectedCharacterID == 0 || len(body) != 1 || body[0] < 1 || body[0] > 2 {
		return nil
	}
	if !currentPVPDojoScene(session) {
		return nil
	}
	s.pvpMu.Lock()
	inRoom := session.pvpRoom != nil
	s.pvpMu.Unlock()
	if inRoom {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if err := dnfpvp.ApplyDojoSkillChoice(ctx, repos, session.accountID,
		strconv.Itoa(int(session.selectedCharacterID)), body[0], s.gameplayNow()); err != nil {
		return err
	}
	if err := s.sendCurrentPVPDojoSkillMode(session); err != nil {
		return err
	}
	if isPVPChannel(session.channel) {
		return s.sendSelectedActorCurrentSceneSkillInfo(session, "道场复制选择后保持PK独立技能")
	}
	return nil
}

func (s *Service) sendCurrentPVPDojoSkillMode(session *gameSession) error {
	if session == nil || session.selectedCharacterID == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return nil
	}
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil || !found {
		return err
	}
	if character.AccountID != session.accountID {
		return nil
	}
	return s.sendGameUpperRawClass(session, currentPVPDojoSkillStateMsgID, []byte{dnfpvp.DojoSkillMode(character)}, 0)
}

func (s *Service) currentPVPDojoSkillsActive(session *gameSession) (bool, error) {
	if !currentPVPDojoScene(session) {
		return false, nil
	}
	session.townMu.Lock()
	position := session.townPositionSnapshot
	session.townMu.Unlock()
	if position.CharacterID != session.selectedCharacterID || !position.PositionValid || position.TownID != 10 || position.AreaID != 4 {
		return false, nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return false, fmt.Errorf("道场技能仓储不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	character, found, err := repos.Character.Load(ctx, strconv.Itoa(int(session.selectedCharacterID)))
	if err != nil {
		return false, err
	}
	if !found || character.AccountID != session.accountID {
		return false, fmt.Errorf("道场技能角色归属不符")
	}
	return dnfpvp.DojoSkillMode(character) == 0, nil
}

// 现用class0/1230处理器12EF240读取两页u8条数与槽位/技能/等级，
// 最后为SP0、SP1、TP0、TP1四个u16。边界沿用当前204槽位的可逆排列。
func buildCurrentPVPDojoSkills(record dnfrepo.SkillRecord) ([]byte, error) {
	var w packetWriter
	var points [2]dnfrepo.SkillPointState
	for tree := 0; tree < 2; tree++ {
		state, found := dnfrepo.SkillTree(record, tree)
		if !found {
			return nil, fmt.Errorf("道场技能页%d缺失", tree)
		}
		points[tree] = state.Points
		slots := make([]int, 0, len(record.Layouts[tree]))
		for slot, id := range record.Layouts[tree] {
			level := state.Skills[int64(id)].Level
			if slot < 0 || slot >= 204 || id >= 512 || level < 0 || level > 255 {
				return nil, fmt.Errorf("道场技能槽位或等级无效")
			}
			if level > 0 {
				slots = append(slots, slot)
			}
		}
		sort.Ints(slots)
		if len(slots) > 255 {
			return nil, fmt.Errorf("道场技能条数超出范围")
		}
		w.writeByte(byte(len(slots)))
		for _, slot := range slots {
			id := record.Layouts[tree][slot]
			wireSlot, err := currentSkillSlotToWire(slot)
			if err != nil {
				return nil, err
			}
			w.writeByte(byte(wireSlot))
			w.writeUint16(id)
			w.writeByte(byte(state.Skills[int64(id)].Level))
		}
	}
	for _, value := range []int{points[0].RemainingSP, points[1].RemainingSP, points[0].RemainingTP, points[1].RemainingTP} {
		if value < 0 || value > 65535 {
			return nil, fmt.Errorf("道场技能点超出原生范围")
		}
		w.writeUint16(uint16(value))
	}
	return w.bytes(), nil
}

func (s *Service) sendCurrentPVPDojoSkillsBeforeTransition(ctx context.Context, session *gameSession, character dnfrepo.CharacterRecord) error {
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil
	}
	owner, err := dnfquest.NewOwner(repos)
	if err != nil {
		return err
	}
	catalog, err := s.loadQuestCatalog(ctx)
	if err != nil {
		return err
	}
	quests, repaired, err := owner.RepairUnprogressedPVPTraining(ctx, catalog, session.accountID, character.CharacterID, s.gameplayNow())
	if err != nil {
		return err
	}
	if repaired {
		if err := s.sendCurrentActiveQuestSnapshotFromCommittedQuest(session, quests, "道场补齐旧版未推进的三项初始条件"); err != nil {
			return err
		}
	}
	record, err := skillcmd.PrepareDojoSkills(ctx, repos, session.accountID, character.CharacterID)
	if err != nil {
		return err
	}
	record, _, err = s.syncCurrentSceneSkillPointLedger(ctx, skillcmd.DojoRepositories(repos, session.accountID, character.CharacterID), character, record)
	if err != nil {
		return err
	}
	body, err := buildCurrentPVPDojoSkills(record)
	if err != nil {
		return err
	}
	if err := s.sendCurrentPVPDojoSkillMode(session); err != nil {
		return err
	}
	s.logGameEvent(session, "道场独立技能初始化", "char_id", session.selectedCharacterID, "body_len", len(body), "mode", dnfpvp.DojoSkillMode(character))
	return s.sendGameUpperRawClass(session, 1230, body, 0)
}

// 1230 是道场技能备份，不能代替重置后的技能窗口刷新。现用 2020660 的
// op19 成功路径经 229F4D0 清除窗口+434等待位，自动加点才能在下一帧继续。
func (s *Service) sendCurrentPVPDojoSkillWindow(ctx context.Context, session *gameSession, character dnfrepo.CharacterRecord) error {
	repos, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("道场技能仓储不可用")
	}
	record, err := skillcmd.PrepareDojoSkills(ctx, repos, session.accountID, character.CharacterID)
	if err != nil {
		return err
	}
	record, err = s.repairCurrentSceneSkillLevelCaps(ctx, skillcmd.DojoRepositories(repos, session.accountID, character.CharacterID), character, record)
	if err != nil {
		return err
	}
	active := dnfrepo.ActiveSkillTree(record)
	commands := s.loadSkillCommandProjection(ctx, repos, character.CharacterID)
	body, _, err := buildCurrentSceneSkillInfoBody(record, record.Layouts[active], commands)
	if err != nil {
		return err
	}
	s.logGameEvent(session, "道场加点窗口刷新", "char_id", session.selectedCharacterID, "msg_id", currentSkillInfoMsgID,
		"body_len", len(body), "技能数据来源", "独立道场加点", "用途", "解除自动加点等待并继续客户端加点请求")
	return s.sendGameUpperRawClass(session, currentSkillInfoMsgID, body, 0)
}

func currentPVPDojoScene(session *gameSession) bool {
	if session == nil || session.selectedCharacterID == 0 {
		return false
	}
	session.townMu.Lock()
	defer session.townMu.Unlock()
	p := session.townPositionSnapshot
	return p.PositionValid && p.CharacterID == session.selectedCharacterID && p.TownID == 10 && p.AreaID == 4 && session.townSceneReadyCharacterID == session.selectedCharacterID
}
