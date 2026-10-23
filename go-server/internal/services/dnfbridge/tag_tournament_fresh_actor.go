package dnfbridge

import (
	"fmt"
	"reflect"
	"strconv"
)

// freshCurrentTagTournamentActor只用于休息室新建的备用角色，不能用于恢复
// 战斗中的对象。3BF20A0 -> 3BF15E0证实模板+234/+238和保护bool+3DC
// 的初值为0；24F9030 -> 2960980证实独立技能管理器首字段初值为0。
// 辅助对象列表属于本次战斗的对象句柄，新创建时为空，不能放宠物itemID。
func freshCurrentTagTournamentActor(row currentTagTournamentCharacterProjection) (currentTagTournamentActorWire, error) {
	if row.character.CharacterID == "" || row.character.Level != int(row.level) || row.character.Job != strconv.Itoa(int(row.job)) || row.character.Stats["grow_type"] != int64(row.grow) {
		return currentTagTournamentActorWire{}, fmt.Errorf("帝国竞技场新建角色与已读取的职业/等级资料不符")
	}
	if row.job == 9 && (row.darkKnight == nil || len(row.darkKnight.body) == 0) {
		return currentTagTournamentActorWire{}, fmt.Errorf("帝国竞技场黑暗武士缺少独立连招页")
	}
	bank := byte(0)
	if row.job == 10 {
		bank = 1
	}
	if row.hotkeys == nil || row.hotkeys.bank != bank || len(row.hotkeys.body) <= 5 || row.hotkeys.body[0] != bank {
		return currentTagTournamentActorWire{}, fmt.Errorf("帝国竞技场角色独立键位尚未准备")
	}
	if row.job == 12 && (row.grow&15 == 1 || row.grow&15 == 2) && row.shield == nil {
		return currentTagTournamentActorWire{}, fmt.Errorf("帝国竞技场守护者独立盾栏尚未准备")
	}
	// +1AC的0x10是现用客户端耳环栏位，与普通角色投影使用同一位集。
	// 继续复用currentExtraEquipmentSlotState处理已提交栏位及光环权限，
	// 不把耳环位误当作后续独立bool，也不把旧投影与变化后的来源混用。
	if value := row.character.Stats["ex_equip_slot_stat"]; value < 0 || value & ^int64(0x1f) != 0 || row.extraSlots != currentExtraEquipmentSlotState(row.character) {
		return currentTagTournamentActorWire{}, fmt.Errorf("帝国竞技场存在尚未核实的附加装备栏状态")
	}
	if value := row.character.Stats["clone_title_item_id"]; value < 0 || value > int64(^uint32(0)) {
		return currentTagTournamentActorWire{}, fmt.Errorf("帝国竞技场幻化称号编号越界")
	}
	a := currentTagTournamentActorWire{
		level: row.level, job: row.job, grow: row.grow,
		stats: append([]byte(nil), row.stats...), extraSlots: row.extraSlots,
		appearance: cloneRosterEquipSummary(row.appearance),
		cloneTitle: statU32(row.character, "clone_title_item_id", 0),
		equipment:  append([]currentMode1EquipmentObjectRow(nil), row.equipmentRows...),
		skillTree:  row.skillTree, skills: append([]currentTagTournamentSkillWire(nil), row.skills...),
		creatureItem: row.creature.itemID, creatureHandle: row.creature.serialOrHandle,
	}
	if row.shield != nil {
		a.shieldDeck = [5]uint32(row.shield.deck)
	}
	if _, err := buildCurrentTagTournamentActorWire(a); err != nil {
		return currentTagTournamentActorWire{}, err
	}
	return a, nil
}

// 同时核对两名角色，以当前已存在actor的对象键承载备用角色；不临时改
// selectedCharacterID，不用数据库槽位替代该连接实际公告过的紧凑序号。
func (s *Service) prepareCurrentTagTournamentCharacterWire(p *gameSession, rows [2]currentTagTournamentCharacterProjection) ([]byte, error) {
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok || !s.isCurrentTagTournamentChannel(p) {
		return nil, fmt.Errorf("帝国竞技场新建角色会话已失效")
	}
	advertised, _, err := s.currentTagTournamentSentRoster(p)
	if err != nil {
		return nil, err
	}
	current := strconv.Itoa(int(identity.character))
	backup := -1
	for i, row := range rows {
		if row.character.AccountID != p.accountID {
			return nil, fmt.Errorf("帝国竞技场新建角色不属于当前账号")
		}
		if _, err := freshCurrentTagTournamentActor(row); err != nil {
			return nil, err
		}
		if row.job == 9 && row.character.CharacterID == current {
			// 1389的主角色分支复用已有本体，不重写它的技能槽。备用对象
			// 可以在创建时整理槽位，当前本体必须已经通过普通场景同步。
			original, _, err := currentTagTournamentCharacterSkills(row.skillRecord)
			if err != nil || !reflect.DeepEqual(original, row.skills) {
				return nil, fmt.Errorf("帝国竞技场当前黑暗武士技能槽尚未同步，请先重新登录该角色")
			}
		}
		if row.character.CharacterID != current {
			backup = i
		}
	}
	if backup < 0 || rows[0].character.CharacterID == rows[1].character.CharacterID || rows[1-backup].character.CharacterID != current {
		return nil, fmt.Errorf("帝国竞技场新建资料必须包含当前角色及一名不同队员")
	}
	row := rows[backup]
	selector := -1
	for i, sent := range advertised {
		if sent.CharacterID == row.character.CharacterID && sent.Slot == row.character.Slot && sent.Name == row.character.Name {
			selector = i
			break
		}
	}
	if selector < 0 || selector >= 255 {
		return nil, fmt.Errorf("帝国竞技场备用角色不在当前已发名单")
	}
	actor, err := freshCurrentTagTournamentActor(row)
	if err != nil {
		return nil, err
	}
	wire, err := buildCurrentTagTournamentCharacterWire(currentSceneActorObjectKey(identity.character), byte(selector), row.character.Name, actor)
	if err != nil {
		return nil, err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil, fmt.Errorf("帝国竞技场新建资料准备期间会话已变化")
	}
	return wire, nil
}
