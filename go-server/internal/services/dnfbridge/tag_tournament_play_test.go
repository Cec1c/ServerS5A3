package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func tagPlayTestSource(t *testing.T) townMoveTestSource {
	source := tagRosterTestSource(t)
	source[worldmap.DefaultWorldMapList] = "1 `test.wdm`\n"
	source["worldmap/test.wdm"] = "[name]\n`隔离帝国地图`\n[dungeon]\n320 -1 336 -1\n[/dungeon]\n"
	for i, id := range []int{320, 336} {
		count, first := 5, 11
		if i == 1 {
			count, first = 7, 31
		}
		start := 700 + i
		source["map/map.lst"] += fmt.Sprintf("%d `%d.pvf`\n", start, start)
		source[fmt.Sprintf("map/%d.pvf", start)] = fmt.Sprintf("[dungeon]\n%d\n[type]\n`[start]`\n", id)
		var specs strings.Builder
		fmt.Fprintf(&specs, "`map` 0 0 %d ", start)
		for r := 0; r < count; r++ {
			kind := "map"
			if r == count-1 {
				kind = "boss"
			}
			mapID := first + r
			if i == 0 && r == 4 {
				mapID = 16
			}
			fmt.Fprintf(&specs, "`%s` %d 0 %d ", kind, r+1, mapID)
		}
		source[fmt.Sprintf("dungeon/%d.pvf", id)] += fmt.Sprintf("[maze info]\n[size]\n%d 1\n[greed]\n`BB%sEE`\n[map specification]\n%s\n[/map specification]\n[start map]\n0 0\n[/start map]\n[boss map]\n%d 0\n[/boss map]\n", count+1, strings.Repeat("FF", count-1), specs.String(), count)
	}
	return source
}

func tagPlayFixture(t *testing.T) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, p, g, roster := tagCharacterProjectionFixture(t)
	ch := roster[0]
	ch.Stats["area_id"] = 0
	ch.Stats["pos_x"], ch.Stats["pos_y"] = 100, 200
	if err := g.Character.Save(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	roster[0] = ch
	logPath := filepath.Join(t.TempDir(), "帝国流程.log")
	logger, logErr := openPacketLogger(logPath)
	if logErr != nil {
		t.Fatal(logErr)
	}
	s.packetLog = logger
	t.Cleanup(func() {
		_ = logger.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			if len(data) > 6000 {
				data = data[len(data)-6000:]
			}
			t.Log(string(data))
		}
	})
	source := tagPlayTestSource(t)
	var err error
	s.tagTournamentCatalog, err = tagtournament.LoadCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapTable, s.worldMapResolver, s.dungeonMonsterTable = loadBridgeDungeonStaticData(t, bridgePVFSource(source))
	p.townSelectorOriginBound = true
	p.townSelectorOriginSnapshot = currentTownPositionSnapshot{CharacterID: 29, TownID: 48, PositionX: 100, PositionY: 200, PositionValid: true}
	o, err := s.currentTagTournamentOwner()
	if err != nil {
		t.Fatal(err)
	}
	if err = o.RegisterRosterFromSnapshot(context.Background(), "a", "29", roster); err != nil {
		t.Fatal(err)
	}
	return s, p, g
}

func tagPlayChallenge(t *testing.T, s *Service, p *gameSession, mode tagtournament.Mode) {
	t.Helper()
	if err := s.handleCurrentTagTournamentEnter(p, []byte{byte(mode)}); err != nil {
		t.Fatal(err)
	}
	if p.tagTournamentBattle == nil || p.tagTournamentBattle.prepared == nil {
		t.Fatal("生产1458没有创建可进图挑战", "response_bytes", p.conn.(*bufferConn).write.Len())
	}
	seen := map[uint16]int{}
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		seen[packet.Header.MsgID]++
		if packet.Header.MsgID == 1389 && !bytes.Equal(packet.Body, p.tagTournamentBattle.prepared.characterBody) {
			t.Fatal("实际创建正文与冻结资料不一致")
		}
		if packet.Header.MsgID == 1458 && !bytes.Equal(packet.Body, []byte{1}) {
			t.Fatal("挑战未成功", packet)
		}
		rest = tail
	}
	if seen[1389] != 1 || seen[1390] != 1 || seen[1458] != 1 {
		t.Fatal("未实际发出创建/选项/成功", seen)
	}
	// 重试不得更换token或重复扣费。
	b := p.tagTournamentBattle
	if err := s.handleCurrentTagTournamentEnter(p, []byte{byte(mode)}); err != nil || p.tagTournamentBattle != b {
		t.Fatal("挑战重试未复用已提交凭据", err)
	}
	p.conn.(*bufferConn).write.Reset()
	request := b.prepared.runtime.Request
	if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil {
		t.Fatal(err)
	}
	if p.dungeon.runtime != b.prepared.runtime || !b.prepared.entered {
		t.Fatal("真实op16未绑定帝国地图")
	}
}

func tagPlayMoveNext(t *testing.T, s *Service, p *gameSession) {
	t.Helper()
	b := p.tagTournamentBattle
	next := b.run.Snapshot().Round + 1
	coordinate := b.prepared.layout.Rounds[next].Coordinate
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0], body[1] = byte(coordinate.X), byte(coordinate.Y)
	if err := s.handleDungeonMoveMap(p, body); err != nil {
		t.Fatal(err)
	}
	if b.run.Snapshot().Round != next || b.run.Snapshot().Phase != tagtournament.Fighting {
		t.Fatal("实际过图没有推进领域回合", b.run.Snapshot())
	}
}

func tagPlayKillBoss(t *testing.T, s *Service, p *gameSession) {
	t.Helper()
	b := p.tagTournamentBattle
	want := b.plan.Encounters[b.run.Snapshot().Round]
	var key uint32
	for _, monster := range p.dungeon.runtime.Room.Snapshot().Monsters {
		if monster.Spawn.MonsterID == int64(want.MonsterID) {
			key = monster.ObjectKey
		}
	}
	if key == 0 {
		t.Fatal("实际回合没有今日Boss")
	}
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
	binary.LittleEndian.PutUint32(body, key)
	binary.LittleEndian.PutUint16(body[4:], currentSceneActorObjectKey(p.selectedCharacterID))
	if err := s.handleDungeonMonsterDeath(p, body); err != nil {
		t.Fatal(err)
	}
	if b.run.Snapshot().Cleared != b.run.Snapshot().Round+1 {
		t.Fatal("生产死亡路径未完成回合", b.run.Snapshot())
	}
	if err := s.handleDungeonMonsterDeath(p, body); err != nil {
		t.Fatal("重复死亡补偿失败", err)
	}
}

func TestTagPlayActualCreationEntryMoveTagAndBossSettlement(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, p, g := tagPlayFixture(t)
			before, _, _ := g.Character.Load(context.Background(), "29")
			backup, _, _ := g.Character.Load(context.Background(), "60")
			tagPlayChallenge(t, s, p, mode)
			tagPlayMoveNext(t, s, p)
			if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil {
				t.Fatal(err)
			}
			if p.tagTournamentBattle.run.Snapshot().Active != 1 || p.selectedCharacterID != 29 {
				t.Fatal("备用角色未切入或会话身份改变")
			}
			for round := 0; round < len(p.tagTournamentBattle.plan.Encounters); round++ {
				if round > 0 {
					tagPlayMoveNext(t, s, p)
				}
				tagPlayKillBoss(t, s, p)
			}
			if p.tagTournamentBattle.run.Snapshot().Phase != tagtournament.Finished {
				t.Fatal("没有完成整场")
			}
			count, total := tagBattleMailTotals(t, g)
			if mode == tagtournament.Normal && count != 5 || mode == tagtournament.Hard && (count != 7 || total[100] != 27) {
				t.Fatal("真实路径邮件结算错误", count, total)
			}
			after, _, _ := g.Character.Load(context.Background(), "29")
			other, _, _ := g.Character.Load(context.Background(), "60")
			if after.Stats["gold"] != before.Stats["gold"]-150000 || after.Stats["fatigue"] != before.Stats["fatigue"] || after.Level != before.Level || !reflect.DeepEqual(backup, other) {
				t.Fatal("帝国修改了非门票角色资产", before, after, other)
			}
		})
	}
}

func TestTagPlayRejectsSkippedRoomsAndWrongDeathSelector(t *testing.T) {
	s, p, g := tagPlayFixture(t)
	tagPlayChallenge(t, s, p, tagtournament.Hard)
	body := make([]byte, dungeoncmd.MoveMapRequestSize)
	body[0] = 2
	if err := s.handleDungeonMoveMap(p, body); err != nil {
		t.Fatal(err)
	}
	if p.tagTournamentBattle.run.Snapshot().Round != -1 {
		t.Fatal("越关成功")
	}
	tagPlayMoveNext(t, s, p)
	if err := s.handleCurrentTagTournamentDeath(p, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if p.tagTournamentBattle.run.Snapshot().Vitals[1].HP == 0 {
		t.Fatal("非当前队员被置死")
	}
	if err := s.handleCurrentTagTournamentDeath(p, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if p.tagTournamentBattle.run.Snapshot().Vitals[0].HP != 0 {
		t.Fatal("实际1466没有记录当前队员死亡")
	}
	if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentTagTournamentDeath(p, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if p.tagTournamentBattle.run.Snapshot().Phase != tagtournament.Defeated {
		t.Fatal("全员死亡没有结束")
	}
	if count, _ := tagBattleMailTotals(t, g); count != 0 {
		t.Fatal("没有击杀却获得奖励")
	}
}

func TestTagPlayDisconnectSettlesHardConfirmedProgress(t *testing.T) {
	s, p, g := tagPlayFixture(t)
	tagPlayChallenge(t, s, p, tagtournament.Hard)
	for i := 0; i < 2; i++ {
		tagPlayMoveNext(t, s, p)
		tagPlayKillBoss(t, s, p)
	}
	if count, _ := tagBattleMailTotals(t, g); count != 2 {
		t.Fatal("已通过两阶段没有即时发邮件")
	}
	s.retireCurrentTagTournamentBattle(p)
	count, total := tagBattleMailTotals(t, g)
	if p.tagTournamentBattle != nil || p.selectedCharacterID != 29 || count != 2 || total[100] != 6 || total[101] != 6 {
		t.Fatal("断线未结算已确认回合或改变登录角色", count, total)
	}
	s.retireCurrentTagTournamentBattle(p)
	if again, _ := tagBattleMailTotals(t, g); again != count {
		t.Fatal("重复结束连接重复发奖")
	}
}

func TestTagPlayHardDefeatMailsConfirmedRoundsWithoutFalseClearUI(t *testing.T) {
	s, p, g := tagPlayFixture(t)
	tagPlayChallenge(t, s, p, tagtournament.Hard)
	for i := 0; i < 2; i++ {
		tagPlayMoveNext(t, s, p)
		tagPlayKillBoss(t, s, p)
	}
	tagPlayMoveNext(t, s, p)
	p.conn.(*bufferConn).write.Reset()
	for _, call := range []func() error{
		func() error { return s.handleCurrentTagTournamentDeath(p, []byte{0}) },
		func() error { return s.handleCurrentTagTournamentTag(p, []byte{1}) },
		func() error { return s.handleCurrentTagTournamentDeath(p, []byte{1}) },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := p.tagTournamentBattle.run.Snapshot()
	if snapshot.Phase != tagtournament.Defeated || snapshot.Cleared != 2 {
		t.Fatal("全灭错误推进了未通关回合", snapshot)
	}
	count, total := tagBattleMailTotals(t, g)
	if count != 2 || total[100] != 6 || total[101] != 6 {
		t.Fatal("全灭没有按已通过回合结算", count, total)
	}
	for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		if packet.Header.MsgID == 1397 {
			t.Fatal("中途全灭被投影成当前回合通关")
		}
		rest = tail
	}
}
