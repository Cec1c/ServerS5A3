package dnfbridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

const (
	currentTagTournamentSkillCommandNotification uint16 = 1391
	currentTagTournamentBuffSwapNotification     uint16 = 1393
)

type currentTagTournamentSkillState struct {
	commands, buffSwap, darkKnight []byte
	buffSources                    [2]buffswap.OwnedProjection
}

// 145CAF0读取count及每名队员在双角色容器的位置，随后按slot/u16技能/等级/count/指令
// 读取；slot和等级在该处理器内丢弃，仍使用该队员1389的真实值。
func writeCurrentTagTournamentSkillCommands(w *packetWriter, row currentTagTournamentCharacterProjection, settings repo.SettingsRecord, found bool) error {
	commands := map[uint16][]byte{}
	if found {
		if settings.Scope != skillcmd.SkillCommandScope(row.character.CharacterID) {
			return fmt.Errorf("帝国竞技场技能指令记录不属于该队员")
		}
		for key, value := range settings.Values {
			if !strings.HasPrefix(key, "command_") || key == "command_count" {
				continue
			}
			id, err := strconv.ParseUint(strings.TrimPrefix(key, "command_"), 10, 16)
			if err != nil {
				return fmt.Errorf("帝国竞技场技能指令编号损坏")
			}
			command, err := hex.DecodeString(strings.TrimSpace(value))
			if err != nil || len(command) == 0 || len(command) > 32 {
				return fmt.Errorf("帝国竞技场技能%d指令存档损坏", id)
			}
			if _, exists := commands[uint16(id)]; exists {
				return fmt.Errorf("帝国竞技场技能指令编号重复")
			}
			commands[uint16(id)] = command
		}
	}
	// 已删除或当前页未学习的旧指令不下发，也不改写原设置。
	var skills []currentTagTournamentSkillWire
	for _, skill := range row.skills {
		// 连招根由1392写入子技能，不能用普通指令记录冒充连招。
		if row.darkKnight != nil && row.darkKnight.roots[skill.id] {
			continue
		}
		if len(commands[skill.id]) != 0 {
			skills = append(skills, skill)
		}
	}
	if len(skills) > 255 {
		return fmt.Errorf("帝国竞技场技能指令超过原生计数")
	}
	w.writeByte(byte(len(skills)))
	for _, skill := range skills {
		command := commands[skill.id]
		w.writeByte(skill.slot)
		w.writeUint16(skill.id)
		w.writeByte(skill.level)
		w.writeByte(byte(len(command)))
		w.writeBytes(command)
	}
	return nil
}

// 145DC60的每名队员部分与现用远端角色Buff换装正文相同：u16技能、
// u8行数，逐行槽/来源列表/u16来源槽/119字节物品及原生可选扩展块。
// 复用拥有者事务和物品序列化，不临时切换会话选中ID或给备用角色造库存。
func (s *Service) prepareCurrentTagTournamentSkillState(ctx context.Context, p *gameSession, characters [2]currentTagTournamentCharacterProjection) (currentTagTournamentSkillState, error) {
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if ctx == nil || !bound || !s.isCurrentTagTournamentChannel(p) {
		return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场技能资料会话失效")
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Character == nil || g.Settings == nil || g.Skill == nil || g.Equipment == nil || g.Pet == nil {
		return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场技能资料仓储不可用")
	}
	advertised, _, err := s.currentTagTournamentSentRoster(p)
	if err != nil {
		return currentTagTournamentSkillState{}, err
	}
	selectors := [2]byte{}
	for i, row := range characters {
		selector := -1
		for j, ch := range advertised {
			if ch.CharacterID == row.character.CharacterID && ch.Name == row.character.Name && ch.Slot == row.character.Slot {
				selector = j
				break
			}
		}
		if selector < 0 || selector >= 255 || i == 1 && byte(selector) == selectors[0] {
			return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场技能资料队员不在已发名单或重复")
		}
		selectors[i] = byte(selector)
	}
	nativeIDs, err := currentTagTournamentNativeRoleIDs(identity.character, [2]repo.CharacterRecord{characters[0].character, characters[1].character})
	if err != nil {
		return currentTagTournamentSkillState{}, err
	}
	var commands, swaps, combos packetWriter
	comboCount := byte(0)
	for _, row := range characters {
		if row.job == 9 {
			if row.darkKnight == nil || len(row.darkKnight.body) == 0 {
				return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场黑暗武士缺少独立连招页")
			}
			comboCount++
		}
	}
	if comboCount != 0 {
		combos.writeByte(comboCount)
	}
	commands.writeByte(2)
	swaps.writeByte(2)
	var saved [2]repo.SettingsRecord
	var exists [2]bool
	var swapSources [2]buffswap.OwnedProjection
	for i, row := range characters {
		if row.character.AccountID != p.accountID {
			return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场技能资料账号不符")
		}
		if err := currentTagTournamentDarkKnightSourceCurrent(ctx, g, row); err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if err := currentTagTournamentShieldSourceCurrent(ctx, g, row); err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if err := currentTagTournamentHotkeySourceCurrent(ctx, g, row); err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if row.darkKnight != nil {
			combos.writeByte(nativeIDs[i])
			combos.writeBytes(row.darkKnight.body)
		}
		r, found, err := g.Settings.Load(ctx, skillcmd.SkillCommandScope(row.character.CharacterID))
		if err != nil {
			return currentTagTournamentSkillState{}, err
		}
		saved[i], exists[i] = repo.CloneSettings(r), found
		commands.writeByte(nativeIDs[i])
		if err = writeCurrentTagTournamentSkillCommands(&commands, row, r, found); err != nil {
			return currentTagTournamentSkillState{}, err
		}
		part, source, err := s.currentTagTournamentBuffSwapBody(ctx, p, row)
		if err != nil {
			return currentTagTournamentSkillState{}, err
		}
		swapSources[i] = source
		swaps.writeByte(nativeIDs[i])
		swaps.writeBytes(part)
	}
	for i, row := range characters {
		ch, found, err := g.Character.Load(ctx, row.character.CharacterID)
		if err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if !found || !reflect.DeepEqual(ch, row.character) {
			return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场准备技能资料期间角色变化")
		}
		sk, found, err := g.Skill.Load(ctx, ch.CharacterID)
		if err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if !found || !reflect.DeepEqual(sk, row.skillRecord) {
			return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场准备技能资料期间技能页变化")
		}
		equipment, _, err := g.Equipment.Load(ctx, ch.CharacterID)
		if err != nil {
			return currentTagTournamentSkillState{}, err
		}
		pet, _, err := g.Pet.Load(ctx, ch.CharacterID)
		if err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if !reflect.DeepEqual(equipment, row.equipment) || !reflect.DeepEqual(pet, row.petRecord) {
			return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场准备技能资料期间穿戴或宠物变化")
		}
		r, found, err := g.Settings.Load(ctx, skillcmd.SkillCommandScope(ch.CharacterID))
		if err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if found != exists[i] || !reflect.DeepEqual(r, saved[i]) {
			return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场准备期间技能指令变化")
		}
		_, source, err := s.currentTagTournamentBuffSwapBody(ctx, p, row)
		if err != nil {
			return currentTagTournamentSkillState{}, err
		}
		// 宠物有效期投影会随当前秒变化；比较脱离事务的持久来源，不能将
		// 同一只宠物的倒计时当作另一连接修改资料。
		if !reflect.DeepEqual(source, swapSources[i]) {
			return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场准备期间Buff换装资料变化")
		}
		if err := currentTagTournamentDarkKnightSourceCurrent(ctx, g, row); err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if err := currentTagTournamentShieldSourceCurrent(ctx, g, row); err != nil {
			return currentTagTournamentSkillState{}, err
		}
		if err := currentTagTournamentHotkeySourceCurrent(ctx, g, row); err != nil {
			return currentTagTournamentSkillState{}, err
		}
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return currentTagTournamentSkillState{}, fmt.Errorf("帝国竞技场准备技能资料期间会话变化")
	}
	return currentTagTournamentSkillState{commands: commands.bytes(), buffSwap: swaps.bytes(), darkKnight: combos.bytes(), buffSources: swapSources}, nil
}

func (s *Service) currentTagTournamentBuffSwapBody(ctx context.Context, p *gameSession, row currentTagTournamentCharacterProjection) ([]byte, buffswap.OwnedProjection, error) {
	r := csharpLegacyUserInfoReader{ctx: ctx, service: s, session: p, characterID: row.character.CharacterID}
	source, err := r.loadNativeBuffOwnedProjection()
	if err != nil {
		return nil, buffswap.OwnedProjection{}, err
	}
	state, items, err := r.nativeBuffSwapUserInfoRows()
	if err != nil {
		return nil, buffswap.OwnedProjection{}, err
	}
	if state.Skill != 0 {
		learned := false
		for _, skill := range row.skills {
			learned = learned || skill.id == state.Skill
		}
		if !learned {
			return nil, buffswap.OwnedProjection{}, fmt.Errorf("帝国竞技场Buff换装选定技能不在该队员当前页")
		}
	}
	if len(items) > buffswap.SlotCount {
		return nil, buffswap.OwnedProjection{}, fmt.Errorf("帝国竞技场Buff换装行数越界")
	}
	var w packetWriter
	w.writeUint16(state.Skill)
	w.writeByte(byte(len(items)))
	for _, item := range items {
		w.writeByte(item.slot)
		w.writeByte(item.binding.List)
		w.writeUint16(item.binding.Index)
		w.writeBytes(item.entry.data[:])
		if item.rawBlocks {
			writeCurrentAvatarItemListOptionalBlobs(&w, item.entry)
		}
	}
	return w.bytes(), source, nil
}
