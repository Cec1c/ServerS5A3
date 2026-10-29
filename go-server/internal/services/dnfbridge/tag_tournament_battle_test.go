package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func tagBattleBridgeFixture(t *testing.T, mode tagtournament.Mode) (*Service, *gameSession, repo.Group, *time.Time) {
	return tagBattleBridgeFixtureWithWeekly(t, mode, false)
}

func tagBattleBridgeFixtureWithWeekly(t *testing.T, mode tagtournament.Mode, weekly bool) (*Service, *gameSession, repo.Group, *time.Time) {
	t.Helper()
	s, p, g, roster := tagCharacterProjectionFixture(t)
	if weekly {
		source := tagRosterTestSource(t)
		source["etc/tagtournament/tagtournament.etc"] += "\n[tag tournament hard mode time attack standard rank]\n20000 240000\n"
		var err error
		s.tagTournamentCatalog, err = tagtournament.LoadCatalog(source)
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 19, 8, 0, 0, 0, time.FixedZone("北京时间", 8*3600))
	if weekly {
		now = time.Now()
	}
	clock := func() time.Time { return now }
	o, err := tagtournament.NewOwnerWithClock(s.tagTournamentCatalog, g, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err = o.RegisterRosterFromSnapshot(context.Background(), "a", "29", roster); err != nil {
		t.Fatal(err)
	}
	projections, err := s.prepareCurrentTagTournamentCharacters(context.Background(), p, roster)
	if err != nil {
		t.Fatal(err)
	}
	a, err := o.AdmitFromSnapshot(context.Background(), "a", "29", roster, mode)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.newCurrentTagTournamentBattle(p, o, a, projections)
	if err != nil {
		t.Fatal(err)
	}
	b.now = clock
	p.tagTournamentBattle = b
	return s, p, g, &now
}

func tagBattleBridgeClear(t *testing.T, s *Service, p *gameSession, now *time.Time, special bool) tagtournament.DailyEncounter {
	t.Helper()
	*now = now.Add(time.Second)
	e, _, err := s.beginCurrentTagTournamentRound(p, special)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if err = s.confirmCurrentTagTournamentRound(p, e.MapID, []uint32{e.MonsterID}, true); err != nil {
		t.Fatal(err)
	}
	return e
}

func tagBattleMailTotals(t *testing.T, g repo.Group) (int, map[int64]int64) {
	t.Helper()
	box, _, err := g.Mailbox.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	total := map[int64]int64{}
	for _, mail := range box.Mails {
		for _, item := range mail.Attachments {
			total[item.ItemID] += item.Count
		}
	}
	return len(box.Mails), total
}

func TestTagBattleRoutesBothTransportsAndKeepsLoginIdentity(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		t.Run(transport, func(t *testing.T) {
			s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Normal)
			identity, _ := s.boundGameSessionCharacterSnapshot(p)
			before, _, _ := g.Character.Load(context.Background(), "60")
			var handled bool
			var err error
			if transport == "legacy" {
				handled, err = currentGameplayModules.DispatchLegacy(s, p, 1465, []byte{1})
			} else {
				handled, err = currentGameplayModules.DispatchUpper(s, p, 1465, 1, []byte{1})
			}
			if !handled || err != nil {
				t.Fatal(handled, err)
			}
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 1402 || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, []byte{1}) || len(rest) != 0 {
				t.Fatal("切人原生通知错位", packet)
			}
			if p.tagTournamentBattle.run.Snapshot().Active != 1 || p.selectedCharacterID != 29 || p.characterGeneration != identity.generation || !s.boundGameSessionCharacterCurrent(identity) {
				t.Fatal("切人破坏登录会话身份")
			}
			after, _, _ := g.Character.Load(context.Background(), "60")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("切人改写角色持久属性")
			}
			p.conn.(*bufferConn).write.Reset()
			if err = s.handleCurrentTagTournamentTag(p, []byte{0}); err != nil {
				t.Fatal(err)
			}
			packet, _ = splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 1465 || !bytes.Equal(packet.Body, []byte{0, 3}) {
				t.Fatal("冷却时没有拒绝ACK解除原生pending", packet)
			}
			*now = now.Add(5 * time.Second)
			p.conn.(*bufferConn).write.Reset()
			if err = s.handleCurrentTagTournamentTag(p, []byte{0}); err != nil || p.tagTournamentBattle.run.Snapshot().Active != 0 {
				t.Fatal("冷却后不能切回", err)
			}
		})
	}
}

func TestTagBattleRejectsUnprovedDeathsAndDoesNotAdvanceOrReward(t *testing.T) {
	s, p, g, _ := tagBattleBridgeFixture(t, tagtournament.Hard)
	if _, _, err := s.beginCurrentTagTournamentRound(p, false); err != nil {
		t.Fatal(err)
	}
	before := p.tagTournamentBattle.run.Snapshot()
	for _, body := range [][]byte{nil, {2}, {0}, {1}, {2, 0}} {
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleCurrentTagTournamentDeath(p, body); err != nil {
			t.Fatal(err)
		}
		packet, _ := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
		if packet.Header.MsgID != 1466 || !bytes.Equal(packet.Body, []byte{0, 3}) {
			t.Fatal("客户端单方死亡触发游戏状态切换", packet)
		}
	}
	if !reflect.DeepEqual(before, p.tagTournamentBattle.run.Snapshot()) {
		t.Fatal("伪造死亡改变生命、进度或冷却")
	}
	if count, _ := tagBattleMailTotals(t, g); count != 0 {
		t.Fatal("伪造死亡发奖")
	}
}

func TestTagBattleNormalSpecialChoiceAndMailAreExactlyOnce(t *testing.T) {
	s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Normal)
	for round := 0; round < 4; round++ {
		e := tagBattleBridgeClear(t, s, p, now, false)
		count, _ := tagBattleMailTotals(t, g)
		if count != round+1 {
			t.Fatal("普通未按本回合邮件结算", round, count)
		}
		if err := s.confirmCurrentTagTournamentRound(p, e.MapID, []uint32{e.MonsterID}, true); err != nil {
			t.Fatal(err)
		}
		if repeat, _ := tagBattleMailTotals(t, g); repeat != count {
			t.Fatal("重复Boss死亡重复邮件")
		}
	}
	if p.tagTournamentBattle.run.Snapshot().Phase != tagtournament.SpecialChoice {
		t.Fatal("第四关后没有明确选择王")
	}
	*now = now.Add(time.Second)
	e, reset, err := s.beginCurrentTagTournamentRound(p, false)
	if err != nil || reset || e.MonsterID != 0 || p.tagTournamentBattle.run.Snapshot().Phase != tagtournament.Finished {
		t.Fatal("放弃王仍自动进第五关", e, reset, err)
	}
	if count, _ := tagBattleMailTotals(t, g); count != 4 {
		t.Fatal("未打王仍发王奖励")
	}
	ch, _, _ := g.Character.Load(context.Background(), "29")
	if ch.Stats["gold"] != 850000 {
		t.Fatal("角色创建以外重复扣入场费")
	}
}

func TestTagBattleHardDeliversEachStageAndUsesHighestDailyTotal(t *testing.T) {
	s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Hard)
	for round := 0; round < 7; round++ {
		tagBattleBridgeClear(t, s, p, now, false)
		count, _ := tagBattleMailTotals(t, g)
		want := round + 1
		if count != want {
			t.Fatal("困难新阶段没有即时结算", round, count)
		}
	}
	count, total := tagBattleMailTotals(t, g)
	if count != 7 || total[100] != 27 || total[101] != 27 {
		t.Fatal("困难误将各档总额累加", count, total)
	}
	if err := s.finishCurrentTagTournamentBattle(p); err != nil {
		t.Fatal(err)
	}
	if again, value := tagBattleMailTotals(t, g); again != count || !reflect.DeepEqual(total, value) {
		t.Fatal("重复结束重复结算")
	}
}

func TestTagBattleRoundProofRejectsMapBossAndIncompleteRoom(t *testing.T) {
	for _, scenario := range []string{"错误地图", "错误Boss", "未清空", "尚未开场"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g, _ := tagBattleBridgeFixture(t, tagtournament.Normal)
			want := p.tagTournamentBattle.plan.Encounters[0]
			if scenario != "尚未开场" {
				if _, _, err := s.beginCurrentTagTournamentRound(p, false); err != nil {
					t.Fatal(err)
				}
			}
			mapID, monster, cleared := want.MapID, want.MonsterID, true
			switch scenario {
			case "错误地图":
				mapID++
			case "错误Boss":
				monster++
			case "未清空":
				cleared = false
			}
			if err := s.confirmCurrentTagTournamentRound(p, mapID, []uint32{monster}, cleared); err == nil {
				t.Fatal("无有效地图死亡证明仍推进")
			}
			if p.tagTournamentBattle.run.Snapshot().Cleared != 0 {
				t.Fatal("非法证明改变进度")
			}
			if count, _ := tagBattleMailTotals(t, g); count != 0 {
				t.Fatal("非法证明发奖")
			}
		})
	}
}

func TestTagBattleOldSessionCrossDayAndOrdinaryChannelAreIsolated(t *testing.T) {
	for _, scenario := range []string{"旧会话", "06点", "普通频道", "分类不符"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, _, now := tagBattleBridgeFixture(t, tagtournament.Normal)
			before := p.tagTournamentBattle.run.Snapshot()
			classification := byte(1)
			switch scenario {
			case "旧会话":
				s.gameSessions[29] = &gameSession{}
			case "06点":
				*now = now.Add(24 * time.Hour)
			case "普通频道":
				p.channel.Type = 0
			case "分类不符":
				classification = 0
			}
			if handled, err := currentGameplayModules.DispatchUpper(s, p, 1465, classification, []byte{0}); !handled || err != nil {
				t.Fatal(handled, err)
			}
			if !reflect.DeepEqual(before, p.tagTournamentBattle.run.Snapshot()) {
				t.Fatal("失效请求改变另一角色状态")
			}
			if scenario == "普通频道" || scenario == "分类不符" {
				if p.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("普通频道或错误分类泄漏帝国通知")
				}
			}
		})
	}
}

func TestTagBattleRoundRewardNativeCapacityAndExactBytes(t *testing.T) {
	body, err := buildCurrentTagTournamentRoundReward([]tagtournament.RewardRow{{ItemID: 100, Count: 3}, {ItemID: 101, Count: 4}})
	if err != nil || len(body) != 13 || body[0] != 2 || binary.LittleEndian.Uint32(body[1:]) != 100 || binary.LittleEndian.Uint16(body[5:]) != 3 || binary.LittleEndian.Uint32(body[7:]) != 101 || binary.LittleEndian.Uint16(body[11:]) != 4 {
		t.Fatal(body, err)
	}
	if zero, err := buildCurrentTagTournamentRoundReward([]tagtournament.RewardRow{{ItemID: 100}, {ItemID: 101}}); err != nil || zero[0] != 2 || binary.LittleEndian.Uint16(zero[5:]) != 0 || binary.LittleEndian.Uint16(zero[11:]) != 0 {
		t.Fatal("零数量图标不能保留真实道具", zero, err)
	}
	for _, rows := range [][]tagtournament.RewardRow{{{ItemID: 100, Count: 1}, {ItemID: 101, Count: 1}, {ItemID: 102, Count: 1}}, {{ItemID: 0, Count: 1}}, {{ItemID: 100, Count: 1}, {ItemID: 100, Count: 2}}} {
		if _, err := buildCurrentTagTournamentRoundReward(rows); err == nil {
			t.Fatal("允许原生越界或无效附件", rows)
		}
	}
	if path := os.Getenv("DNFBRIDGE_TAG_RUNTIME_NATIVE_FIXTURES"); path != "" {
		var fixtures []map[string]any
		for count := 0; count <= 2; count++ {
			rows := []tagtournament.RewardRow{{ItemID: 100, Count: 3}, {ItemID: 101, Count: 4}}[:count]
			wire, err := buildCurrentTagTournamentRoundReward(rows)
			if err != nil {
				t.Fatal(err)
			}
			fixtures = append(fixtures, map[string]any{"rows": rows, "body": wire})
		}
		encoded, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
