package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func TestTagNativeRolesKeepRootZeroRegardlessOfRegistrationOrder(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		s, p, _, roster := tagCharacterProjectionFixture(t)
		if reversed {
			roster[0], roster[1] = roster[1], roster[0]
		}
		rows, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := currentTagTournamentNativeRoleIDs(p.selectedCharacterID, roster)
		if err != nil {
			t.Fatal(err)
		}
		for i, row := range roster {
			want := byte(1)
			if row.CharacterID == "29" {
				want = 0
			}
			if ids[i] != want {
				t.Fatal("注册顺序改变原生位置", ids, roster)
			}
		}
		wire, err := s.prepareCurrentTagTournamentCharacterOptions(context.Background(), p, rows)
		if err != nil || !bytes.Equal(wire, []byte{2, ids[0], 0, 0, 0, 0, ids[1], 0, 0, 0, 0}) {
			t.Fatal("1390没有对应真实actor容器", wire, err)
		}
		state, err := s.prepareCurrentTagTournamentSkillState(context.Background(), p, rows)
		if err != nil || !bytes.Equal(state.commands, []byte{2, ids[0], 0, ids[1], 0}) || !bytes.Equal(state.buffSwap, []byte{2, ids[0], 0, 0, 0, ids[1], 0, 0, 0}) {
			t.Fatal("技能及换装使用名单序号", state, err)
		}
	}
	for _, rows := range [][2]repo.CharacterRecord{{}, {{CharacterID: "29"}, {CharacterID: "29"}}, {{CharacterID: "60"}, {CharacterID: "61"}}} {
		if _, err := currentTagTournamentNativeRoleIDs(29, rows); err == nil {
			t.Fatal("原生映射接受缺失或重复身份", rows)
		}
	}
}

func TestTagNativeCreationSurvivesEntryRetryAndTwoWayTag(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		s, p, _ := tagPlayFixture(t)
		p.conn.(*bufferConn).write.Reset()
		for i := 0; i < 2; i++ {
			if err := s.handleCurrentTagTournamentEnter(p, []byte{byte(mode)}); err != nil {
				t.Fatal(err)
			}
		}
		count := func(op uint16) int {
			n := 0
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				if packet.Header.MsgID == op {
					n++
				}
				rest = tail
			}
			return n
		}
		if count(1389) != 1 || count(1390) != 1 || count(1458) != 2 {
			t.Fatal("挑战重试重建已创建容器")
		}
		b := p.tagTournamentBattle
		if b == nil || !b.prepared.creationSent || b.nativeRoleIDs != ([2]byte{0, 1}) {
			t.Fatal("创建映射未提交")
		}
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(b.prepared.runtime.Request)); err != nil {
			t.Fatal(err)
		}
		if count(1389) != 0 || count(1390) != 0 || count(1391) != 0 || count(1393) != 0 {
			t.Fatal("场景落位后破坏双角色初始化")
		}
		tagPlayMoveNext(t, s, p)
		now := b.now()
		b.now = func() time.Time { return now }
		for _, target := range []byte{1, 0} {
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentTagTournamentTag(p, []byte{target}); err != nil {
				t.Fatal(err)
			}
			var projection []byte
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				if packet.Header.MsgID == 1402 {
					projection = append(projection, packet.Body...)
				}
				rest = tail
			}
			if !bytes.Equal(projection, []byte{target}) || b.run.Snapshot().Active != int(target) || p.selectedCharacterID != 29 {
				t.Fatal("原生0/1往返切换失败", target, projection, b.run.Snapshot())
			}
			now = now.Add(10 * time.Second)
		}
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentTagTournamentTag(p, []byte{2}); err != nil || count(1465) != 1 || b.run.Snapshot().Active != 0 {
			t.Fatal("名单位置2误当作原生队员")
		}
		if target := os.Getenv("DNFBRIDGE_TAG_SWITCH_FIXTURES"); target != "" {
			raw, _ := json.Marshal(map[string]any{"native_ids": b.nativeRoleIDs, "options": b.prepared.optionBody, "commands": b.prepared.skillState.commands, "buff_swap": b.prepared.skillState.buffSwap})
			if err := os.WriteFile(target, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
