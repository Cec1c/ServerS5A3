package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func TestTagFreshActorUsesIndependentSourcesAndNativeFreshState(t *testing.T) {
	s, p, g, roster := tagCharacterProjectionFixture(t)
	rows, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster)
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Account.Load(context.Background(), "a")
	actor, err := freshCurrentTagTournamentActor(rows[1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actor.stats, rows[1].stats) || !reflect.DeepEqual(actor.skills, rows[1].skills) || actor.base3A4 != 0 || actor.base3A6 != 0 || actor.base3A8 != 0 || actor.base234 != 0 || actor.base238 != 0 || actor.skillObjectKey != 0 || actor.privateCharacter || actor.extraSlotFlag || len(actor.auxiliaryHandles) != 0 {
		t.Fatal("新建对象混入其他角色或旧战斗状态", actor)
	}
	wire, err := s.prepareCurrentTagTournamentCharacterWire(p, rows)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(wire[1:3]) != currentSceneActorObjectKey(29) || wire[4] != 0 {
		t.Fatal("主对象或备用队员序号混用数据库槽位", wire[:5])
	}
	actor.stats[0] ^= 0xff
	actor.skills[0].level = 99
	actor.appearance[0].ItemIDOrIcon = 123456
	if actor.stats[0] == rows[1].stats[0] || rows[1].skills[0].level == 99 || rows[1].appearance[0].ItemIDOrIcon == 123456 {
		t.Fatal("新建对象与只读源共享可变切片")
	}
	after, _, _ := g.Account.Load(context.Background(), "a")
	if !reflect.DeepEqual(before, after) || p.conn.(*bufferConn).write.Len() != 0 || p.tagTournamentBattle != nil || p.selectedCharacterID != 29 {
		t.Fatal("正文准备提前扣费、发包或改写会话")
	}
	if target := os.Getenv("DNFBRIDGE_TAG_FRESH_CHARACTER_FIXTURES"); target != "" {
		// 完整正文来自真实仓储投影函数；这些仓储本身是隔离合成输入。
		n := int(binary.LittleEndian.Uint32(wire[5:9]))
		data, err := json.MarshalIndent([]map[string]any{{"owner": 29, "selector": 0, "name": wire[9 : 9+n], "body": wire, "actor": wire[9+n:]}}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTagFreshActorRejectsUnsupportedOrMismatchedStateBeforePayment(t *testing.T) {
	for _, name := range []string{"黑暗武士", "缔造者键位未准备", "未知装备位", "负装备位", "装备栏投影变化", "资料职业不符", "外账号", "非注册序号", "重复队员", "旧连接"} {
		t.Run(name, func(t *testing.T) {
			s, p, g, roster := tagCharacterProjectionFixture(t)
			rows, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "黑暗武士", "缔造者键位未准备":
				rows[1].job = 9
				if name == "缔造者键位未准备" {
					rows[1].job = 10
				}
				rows[1].character.Job = strconv.Itoa(int(rows[1].job))
			case "未知装备位":
				rows[1].character.Stats["ex_equip_slot_stat"] = 32
			case "负装备位":
				rows[1].character.Stats["ex_equip_slot_stat"] = -1
			case "装备栏投影变化":
				rows[1].extraSlots ^= 1
			case "资料职业不符":
				rows[1].job++
			case "外账号":
				rows[1].character.AccountID = "other"
			case "非注册序号":
				rows[1].character.Name = "更改后的姓名"
			case "重复队员":
				rows[1] = rows[0]
			case "旧连接":
				s.gameSessions[29] = &gameSession{}
			}
			before, _, _ := g.Character.Load(context.Background(), "29")
			if wire, err := s.prepareCurrentTagTournamentCharacterWire(p, rows); err == nil || len(wire) != 0 {
				t.Fatal("不完整状态被编码为可创建角色")
			}
			after, _, _ := g.Character.Load(context.Background(), "29")
			box, _, _ := g.Mailbox.Load(context.Background(), "29")
			if !reflect.DeepEqual(before, after) || len(box.Mails) != 0 || p.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("准备失败改变玩家状态")
			}
		})
	}
}

func TestTagFreshActorSupportsEitherRegisteredCharacterAsExistingActor(t *testing.T) {
	s, p, _, roster := tagCharacterProjectionFixture(t)
	rows, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster)
	if err != nil {
		t.Fatal(err)
	}
	// 对调注册位置不改变当前actor，也不能把注册位置当成紧凑列表序号。
	swapped := [2]currentTagTournamentCharacterProjection{rows[1], rows[0]}
	a, err := s.prepareCurrentTagTournamentCharacterWire(p, rows)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.prepareCurrentTagTournamentCharacterWire(p, swapped)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("注册顺序改变了主备绑定", err)
	}
}
