package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

type tagProjectionEquipmentChange struct {
	repo.EquipmentRepository
	target string
	calls  int
	fail   bool
}

func (r *tagProjectionEquipmentChange) Load(ctx context.Context, id string) (repo.EquipmentRecord, bool, error) {
	value, found, err := r.EquipmentRepository.Load(ctx, id)
	if id != r.target || err != nil {
		return value, found, err
	}
	r.calls++
	if r.fail {
		return repo.EquipmentRecord{}, false, errors.New("隔离穿戴读取失败")
	}
	if r.calls > 1 {
		value.UpdatedAt = value.UpdatedAt.Add(time.Second)
	}
	return value, found, nil
}

func TestTagCharacterProjectionRejectsEquipmentChangeOrReadFailureWithoutPayment(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "准备期间穿戴改变", true: "穿戴读取失败"}[fail], func(t *testing.T) {
			s, p, g, roster := tagCharacterProjectionFixture(t)
			g.Equipment = &tagProjectionEquipmentChange{EquipmentRepository: g.Equipment, target: "60", fail: fail}
			s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			before, _, _ := g.Account.Load(context.Background(), "a")
			if _, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster); err == nil {
				t.Fatal("不完整或变化的装备被当作可入场快照")
			}
			after, _, _ := g.Account.Load(context.Background(), "a")
			if !reflect.DeepEqual(before, after) || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("准备失败后仍付款或发包")
			}
		})
	}
}

func TestTagCharacterSkillsKeepIndependentActivePageAndNativeSlots(t *testing.T) {
	r := repo.SkillRecord{CharacterID: "60", Skills: map[int64]repo.SkillState{10: {Level: 12}, 20: {Level: 7}, 30: {Level: 1}}, Layouts: map[int]repo.SkillLayout{0: {0: 30}, 1: {198: 10, 6: 20}}, Trees: repo.SkillTreeState{Expanded: true, Active: 1, Other: repo.SkillProfile{Skills: map[int64]repo.SkillState{10: {Level: 99}}}}}
	before := repo.CloneSkill(r)
	rows, tree, err := currentTagTournamentCharacterSkills(r)
	want := []currentTagTournamentSkillWire{{6, 10, 12}, {12, 20, 7}, {255, 30, 1}}
	if err != nil || tree != 1 || !reflect.DeepEqual(rows, want) || !reflect.DeepEqual(r, before) {
		t.Fatal(rows, tree, err)
	}
	for name, change := range map[string]func(*repo.SkillRecord){
		"非法页":    func(r *repo.SkillRecord) { r.Trees.Active = 2 },
		"未购买第二页": func(r *repo.SkillRecord) { r.Trees.Expanded = false },
		"未学布局":   func(r *repo.SkillRecord) { r.Layouts[1][2] = 40 },
		"重复布局":   func(r *repo.SkillRecord) { r.Layouts[1][2] = 10 },
		"槽位越界":   func(r *repo.SkillRecord) { r.Layouts[1][204] = 30 },
		"编号越界":   func(r *repo.SkillRecord) { r.Skills[512] = repo.SkillState{Level: 1} },
		"等级越界":   func(r *repo.SkillRecord) { r.Skills[10] = repo.SkillState{Level: 256} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := repo.CloneSkill(r)
			change(&bad)
			if _, _, err := currentTagTournamentCharacterSkills(bad); err == nil {
				t.Fatal("非法技能资料未拒绝")
			}
		})
	}
}

func tagCharacterProjectionFixture(t *testing.T) (*Service, *gameSession, repo.Group, [2]repo.CharacterRecord) {
	t.Helper()
	s, p, g := newTagRosterBridgeTest(t)
	p.accountFromToken = true
	var roster [2]repo.CharacterRecord
	for i, id := range []string{"29", "60"} {
		ch, _, _ := g.Character.Load(context.Background(), id)
		ch.Stats["grow_type"] = int64(0x20 + i + 1)
		ch.Stats["stat_hp_max"] = int64(20000 + i*1000)
		ch.Stats["stat_mp_max"] = int64(10000 + i*1000)
		if err := g.Character.Save(context.Background(), ch); err != nil {
			t.Fatal(err)
		}
		roster[i] = repo.CloneCharacter(ch)
		if err := g.Equipment.Save(context.Background(), repo.EquipmentRecord{CharacterID: id}); err != nil {
			t.Fatal(err)
		}
		r := repo.SkillRecord{CharacterID: id, Skills: map[int64]repo.SkillState{10: {Level: i + 1}}, Layouts: map[int]repo.SkillLayout{0: {198: 10}}}
		if err := g.Skill.Save(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		// 合成仓储使用已保存的原生文本键位；真实PVF专项另测无存档默认值。
		if err := g.Settings.Save(context.Background(), repo.SettingsRecord{Scope: characterHotkeyConfigScope(id), Values: map[string]string{"hotkey_config_hex": hex.EncodeToString([]byte("I01\t30\nI02\t31\n"))}}); err != nil {
			t.Fatal(err)
		}
	}
	return s, p, g, roster
}

func TestTagCharacterProjectionLoadsBothRealRecordsWithoutMutatingSessionOrAssets(t *testing.T) {
	s, p, g, roster := tagCharacterProjectionFixture(t)
	account, _, _ := g.Account.Load(context.Background(), "a")
	rows, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range rows {
		if row.character.CharacterID != roster[i].CharacterID || row.grow != byte(0x21+i) || binary.LittleEndian.Uint32(row.stats) != uint32(20000+i*1000) || len(row.skills) != 1 || row.skills[0].level != byte(i+1) || row.skills[0].slot != 6 || len(row.appearance) != 14 {
			t.Fatal("队员资料串用或缺失", i, row)
		}
	}
	rows[0].character.Stats["gold"] = 0
	rows[0].skillRecord.Skills[10] = repo.SkillState{Level: 99}
	rows[0].stats[0] = 0
	for i, ch := range roster {
		actual, _, _ := g.Character.Load(context.Background(), ch.CharacterID)
		if !reflect.DeepEqual(actual, ch) {
			t.Fatal("准备写入角色", i)
		}
	}
	after, _, _ := g.Account.Load(context.Background(), "a")
	if p.selectedCharacterID != 29 || p.characterGeneration != 1 || p.conn.(*bufferConn).write.Len() != 0 || !reflect.DeepEqual(account, after) {
		t.Fatal("只读准备改写会话、账号或发送未经核实的1389")
	}
}

func TestTagCharacterProjectionRejectsForeignStaleAndMissingSources(t *testing.T) {
	for _, scenario := range []string{"其他账号", "改名", "删除", "缺职业", "缺转职", "缺属性", "缺技能", "旧连接", "战斗中", "未鉴权", "重复队员"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g, roster := tagCharacterProjectionFixture(t)
			ch := repo.CloneCharacter(roster[1])
			switch scenario {
			case "其他账号":
				ch.AccountID = "b"
			case "改名":
				ch.Name = "新名字"
			case "删除":
				ch.Stats["delete_flag"] = 1
			case "缺职业":
				ch.Job = ""
			case "缺转职":
				delete(ch.Stats, "grow_type")
			case "缺属性":
				delete(ch.Stats, "stat_hp_max")
			case "缺技能":
				g.Skill = nil
				s.repositoryProvider = func() (repo.Group, bool) { return g, true }
			case "旧连接":
				s.gameSessions[29] = &gameSession{}
			case "战斗中":
				p.dungeon.runtime = &runtimeDungeonState{}
			case "未鉴权":
				p.accountFromToken = false
			case "重复队员":
				roster[1] = roster[0]
			}
			if err := g.Character.Save(context.Background(), ch); err != nil {
				t.Fatal(err)
			}
			if _, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster); err == nil {
				t.Fatal("接受缺失或已失效的双角色资料", scenario)
			}
			if p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("失败后发送正文")
			}
		})
	}
}
