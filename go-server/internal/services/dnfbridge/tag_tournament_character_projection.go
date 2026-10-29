package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 创建备用actor前的只读资料；这里每个字段都有已存在的角色/装备/技能来源。
// 与已发选角表的身份缓存分开，不能把缓存的部分外观当成完整穿戴状态。
// 尚未完成来源核对的1389运行字段不在本结构中，调用方不能通过零值补齐。
type currentTagTournamentCharacterProjection struct {
	character                               repo.CharacterRecord
	equipment                               repo.EquipmentRecord
	skillRecord                             repo.SkillRecord
	petRecord                               repo.PetRecord
	level, job, grow, extraSlots, skillTree byte
	stats                                   []byte
	appearance                              []repo.CharacterRosterEquipSummary
	equipmentRows                           []currentMode1EquipmentObjectRow
	skills                                  []currentTagTournamentSkillWire
	creature                                currentEquippedCreatureSnapshot
	darkKnight                              *currentTagTournamentDarkKnightProjection
	shield                                  *currentTagTournamentShieldProjection
	hotkeys                                 *currentTagTournamentHotkeyProjection
}

// 22BEDB0的count/tree/slot/id/level来自当前独立技能页。布局使用现用204槽
// 可逆映射，无布局的已学技能写FF，不借另一名角色或另一技能页补等级。
func currentTagTournamentCharacterSkills(record repo.SkillRecord) ([]currentTagTournamentSkillWire, byte, error) {
	if record.CharacterID == "" || record.Trees.Active > 1 || !record.Trees.Expanded && record.Trees.Active != 0 {
		return nil, 0, fmt.Errorf("帝国竞技场技能身份或当前技能页无效")
	}
	tree := byte(repo.ActiveSkillTree(record))
	byID := map[uint16]byte{}
	for slot, id := range record.Layouts[int(tree)] {
		wire, err := currentSkillSlotToWire(slot)
		if err != nil || id >= 512 {
			return nil, 0, fmt.Errorf("帝国竞技场技能布局越界")
		}
		learned, found := record.Skills[int64(id)]
		if !found || learned.Level <= 0 {
			return nil, 0, fmt.Errorf("帝国竞技场技能布局引用未学习技能")
		}
		if _, found := byID[id]; found {
			return nil, 0, fmt.Errorf("帝国竞技场同一技能占用重复槽位")
		}
		byID[id] = byte(wire)
	}
	ids := make([]int, 0, len(record.Skills))
	for id, learned := range record.Skills {
		if id < 0 || id >= 512 || learned.Level < 0 || learned.Level > 255 {
			return nil, 0, fmt.Errorf("帝国竞技场技能编号或等级不能由现用原生格式表达")
		}
		if learned.Level > 0 {
			ids = append(ids, int(id))
		}
	}
	if len(ids) > 255 {
		return nil, 0, fmt.Errorf("帝国竞技场已学技能超过原生单字节计数")
	}
	sort.Ints(ids)
	rows := make([]currentTagTournamentSkillWire, 0, len(ids))
	for _, id := range ids {
		slot := byte(255)
		if value, found := byID[uint16(id)]; found {
			slot = value
		}
		rows = append(rows, currentTagTournamentSkillWire{slot: slot, id: uint16(id), level: byte(record.Skills[int64(id)].Level)})
	}
	return rows, tree, nil
}

func (s *Service) prepareCurrentTagTournamentCharacters(ctx context.Context, p *gameSession, expected [2]repo.CharacterRecord) ([2]currentTagTournamentCharacterProjection, error) {
	var result [2]currentTagTournamentCharacterProjection
	if ctx == nil {
		return result, fmt.Errorf("帝国竞技场双角色资料缺少读取上下文")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !s.isCurrentTagTournamentChannel(p) || !bound || !p.loginPacketSeen || !p.accountFromToken || !currentTagTournamentLobbyInteractionReady(p) || expected[0].CharacterID == expected[1].CharacterID {
		return result, fmt.Errorf("帝国竞技场双角色资料会话或休息室状态无效")
	}
	if expected[0].CharacterID != strconv.Itoa(int(identity.character)) && expected[1].CharacterID != strconv.Itoa(int(identity.character)) {
		return result, fmt.Errorf("帝国竞技场当前角色不在注册队伍中")
	}
	// 一次准备内共用同一份装备/角色读取，结束时再绕过缓存核对来源变化。
	ctx, _ = withEquipMode0RepositoryMemo(ctx)
	repositories, ok := s.repositoryGroupFor(ctx)
	if !ok || repositories.Character == nil || repositories.Equipment == nil || repositories.Skill == nil || repositories.Pet == nil {
		return result, fmt.Errorf("帝国竞技场双角色资料仓储不可用")
	}
	for i, old := range expected {
		ch, found, err := repositories.Character.Load(ctx, old.CharacterID)
		if err != nil {
			return result, err
		}
		id, parseErr := strconv.ParseUint(ch.CharacterID, 10, 16)
		job, jobErr := strconv.ParseUint(ch.Job, 10, 8)
		grow, hasGrow := ch.Stats["grow_type"]
		if !found || parseErr != nil || id == 0 || strconv.FormatUint(id, 10) != ch.CharacterID || ch.CharacterID != old.CharacterID || ch.AccountID != p.accountID || ch.Name != old.Name || ch.Name == "" || ch.Slot < 0 || ch.Slot != old.Slot || ch.Stats["delete_flag"] != 0 || ch.Stats["delete_time"] != 0 || ch.Level < 90 || ch.Level > 255 || jobErr != nil || job > 15 || !hasGrow || grow < 0 || grow > 127 {
			return result, fmt.Errorf("帝国竞技场队员身份、等级或职业资料已变更")
		}
		equipment, equipmentFound, err := repositories.Equipment.Load(ctx, ch.CharacterID)
		if err != nil {
			return result, err
		}
		if equipmentFound && equipment.CharacterID != ch.CharacterID {
			return result, fmt.Errorf("帝国竞技场穿戴记录不属于该队员")
		}
		record, found, err := repositories.Skill.Load(ctx, ch.CharacterID)
		if err != nil {
			return result, err
		}
		if !found || record.CharacterID != ch.CharacterID {
			return result, fmt.Errorf("帝国竞技场队员缺少自身技能记录")
		}
		skills, tree, err := currentTagTournamentCharacterSkills(record)
		if err != nil {
			return result, err
		}
		pet, petFound, err := repositories.Pet.Load(ctx, ch.CharacterID)
		if err != nil {
			return result, err
		}
		if petFound && pet.CharacterID != ch.CharacterID {
			return result, fmt.Errorf("帝国竞技场宠物记录不属于该队员")
		}
		creature, err := s.currentEquippedCreatureForCharacterWithError(ctx, ch.CharacterID)
		if err != nil {
			return result, err
		}
		// 已选角色辅助函数只允许p.selectedCharacterID，备用队员必须从其自身
		// 穿戴记录构造，不能临时改会话ID或借当前角色外观。
		appearance, err := buildCurrentActorMode0AppearanceSummaryFromEquipment(equipment)
		if err != nil {
			return result, err
		}
		applyCurrentCloneTitleAppearance(appearance, ch.Stats["clone_title_item_id"])
		pvfStats, hasStats := s.characterPVFStatsForUserInfo(ctx, p, ch, true)
		reader := csharpLegacyUserInfoReader{ctx: ctx, repo: repositories.LegacyUserInfo, characterID: ch.CharacterID, service: s, session: p, pvfStats: pvfStats, hasPVFStats: hasStats}
		statRow := reader.one("legacy_character_subtype1_fields", csharpUserInfoSubtype1Columns())
		stats := reader.buildCurrentUserInfoMode1StatBlob(statRow, ch)
		if len(stats) != currentMode1StatBlobWireSize || binary.LittleEndian.Uint32(stats[:4]) == 0 || binary.LittleEndian.Uint32(stats[4:8]) == 0 {
			return result, fmt.Errorf("帝国竞技场队员缺少真实生命或魔法属性")
		}
		allRows := reader.currentMode1EquipmentObjectRows()
		createRows := currentMode1EquipmentCreateRows(allRows)
		if len(currentMode1EquipmentDeferredUpdateRows(allRows)) != 0 {
			return result, fmt.Errorf("帝国竞技场队员装备仍需未核实的创建后更新")
		}
		result[i] = currentTagTournamentCharacterProjection{character: repo.CloneCharacter(ch), equipment: repo.CloneEquipment(equipment), skillRecord: repo.CloneSkill(record), petRecord: repo.ClonePet(pet), level: byte(ch.Level), job: byte(job), grow: byte(grow), extraSlots: currentExtraEquipmentSlotState(ch), skillTree: tree, stats: append([]byte(nil), stats...), appearance: cloneRosterEquipSummary(appearance), equipmentRows: createRows, skills: skills, creature: creature}
		if err := s.prepareCurrentTagTournamentDarkKnight(ctx, repositories, &result[i]); err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
		if err := s.prepareCurrentTagTournamentShield(ctx, repositories, &result[i]); err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
		if err := s.prepareCurrentTagTournamentHotkeys(ctx, repositories, &result[i]); err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
	}
	raw, _ := s.repositoryGroup()
	for _, row := range result {
		ch, found, err := raw.Character.Load(ctx, row.character.CharacterID)
		if err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
		eq, _, err := raw.Equipment.Load(ctx, row.character.CharacterID)
		if err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
		skills, skillFound, err := raw.Skill.Load(ctx, row.character.CharacterID)
		if err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
		pet, _, err := raw.Pet.Load(ctx, row.character.CharacterID)
		if err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
		if !found || !skillFound || !reflect.DeepEqual(ch, row.character) || !reflect.DeepEqual(eq, row.equipment) || !reflect.DeepEqual(skills, row.skillRecord) || !reflect.DeepEqual(pet, row.petRecord) {
			return [2]currentTagTournamentCharacterProjection{}, fmt.Errorf("帝国竞技场准备期间角色、穿戴、技能或宠物已变化，请重试")
		}
		if err := currentTagTournamentDarkKnightSourceCurrent(ctx, raw, row); err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
		if err := currentTagTournamentShieldSourceCurrent(ctx, raw, row); err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
		if err := currentTagTournamentHotkeySourceCurrent(ctx, raw, row); err != nil {
			return [2]currentTagTournamentCharacterProjection{}, err
		}
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return [2]currentTagTournamentCharacterProjection{}, fmt.Errorf("帝国竞技场准备期间角色会话已失效")
	}
	return result, nil
}
