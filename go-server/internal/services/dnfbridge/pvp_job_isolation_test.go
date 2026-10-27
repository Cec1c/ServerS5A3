package dnfbridge

import (
	"context"
	"reflect"
	"strconv"
	"testing"
)

func TestPVPRoomCommandsCannotMutateOrdinaryTownCharactersOfAnyJob(t *testing.T) {
	for job := 0; job < 15; job++ {
		t.Run(strconv.Itoa(job), func(t *testing.T) {
			s := pvpRoomTestService(t)
			r, _ := s.repositoryGroup()
			ctx := context.Background()
			c, _, _ := r.Character.Load(ctx, "29")
			c.Job = strconv.Itoa(job)
			if err := r.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			beforeC, _, _ := r.Character.Load(ctx, "29")
			beforeS, _, _ := r.Skill.Load(ctx, "29")
			beforeI, _, _ := r.Inventory.Load(ctx, "29")
			p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, accountID: c.AccountID, channel: s.catalog.Channels()[0]}
			pvp := pvpGameplayModule()
			for _, op := range []uint16{50, 51, 52, 53, 54, 55, 56, 57, 59} {
				if err := pvp.LegacyHandlers[op](s, p, gameplayRequest{Opcode: op, Body: []byte{5, 0, 0, 0, 0, 0}}); err != nil {
					t.Fatal(err)
				}
			}
			afterC, _, _ := r.Character.Load(ctx, "29")
			afterS, _, _ := r.Skill.Load(ctx, "29")
			afterI, _, _ := r.Inventory.Load(ctx, "29")
			if p.pvpRoom != nil || len(s.pvpRooms) != 0 || !reflect.DeepEqual(beforeC, afterC) || !reflect.DeepEqual(beforeS, afterS) || !reflect.DeepEqual(beforeI, afterI) {
				t.Fatal("决斗请求修改了普通城镇职业状态")
			}
		})
	}
}

func TestPVPLobbyRejectsOrdinaryDungeonEntryBeforeCreatingDungeonState(t *testing.T) {
	s := pvpRoomTestService(t)
	p := &gameSession{conn: &bufferConn{}, selectedCharacterID: 29, accountID: "account-29"}
	for _, c := range s.catalog.Channels() {
		if isPVPChannel(c) {
			p.channel = c
			break
		}
	}
	if !isPVPChannel(p.channel) {
		t.Fatal("测试缺少决斗频道")
	}
	before := p.dungeon.runtime
	if err := s.handleDungeonSelectUpper(p, make([]byte, 20)); err != nil {
		t.Fatal(err)
	}
	if p.dungeon.runtime != before {
		t.Fatal("决斗大厅的普通选图创建了地下城")
	}
}
