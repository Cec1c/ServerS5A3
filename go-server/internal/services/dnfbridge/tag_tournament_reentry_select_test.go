package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func TestTagPaidReturnReentryCreatesBothActorsBeforeSelector(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, p := tagResultActionFixture(t, mode, true)
			old := p.tagTournamentBattle
			request := old.prepared.runtime.Request
			if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			if err := s.commitPendingDungeonReturnBeforeTownEnterSelect(p, "帝国通关后立即重入回归"); err != nil {
				t.Fatal(err)
			}
			tagCompleteTownSceneCallback(t, s, p)
			if ready, reason := s.currentTownEnterSelectReady(p); !ready || p.tagTournamentBattle != nil {
				t.Fatal("实际回城没有释放上场挑战或恢复休息室", reason)
			}
			repos, _ := s.repositoryGroup()
			before, _, _ := repos.Character.Load(context.Background(), "29")
			backup, _, _ := repos.Character.Load(context.Background(), "60")
			p.conn.(*bufferConn).write.Reset()
			if err := s.sendEnterSelectDungeonState(p, "upper_15", false, true); err != nil {
				t.Fatal(err)
			}
			b := p.tagTournamentBattle
			if b == nil || b == old || !b.prepared.creationSent || b.prepared.entered || p.dungeon.runtime != nil {
				t.Fatal("15未恢复新挑战或提前进入战斗")
			}
			var order []uint16
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				switch packet.Header.MsgID {
				case 1389, 1390, 15, 27, 1458:
					order = append(order, packet.Header.MsgID)
				}
				rest = tail
			}
			if !reflect.DeepEqual(order, []uint16{1389, 1390, 15, 27}) {
				t.Fatal("已付费重入的角色创建晚于选择器，或伪造购买应答", order)
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.sendEnterSelectDungeonState(p, "upper_15_retry", false, true); err != nil || p.tagTournamentBattle != b {
				t.Fatal("重试15替换了已准备挑战", err)
			}
			if counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes()); counts[1389] != 0 || counts[1458] != 0 {
				t.Fatal("重试15重复创建角色或购买", counts)
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil || p.dungeon.runtime != b.prepared.runtime {
				t.Fatal("恢复后实际选图失败", err)
			}
			counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
			if counts[1389] != 0 || counts[1458] != 0 || counts[29] != 1 || counts[1402] != 1 {
				t.Fatal("选图重建角色或缺少初始出战通知", counts)
			}
			after, _, _ := repos.Character.Load(context.Background(), "29")
			other, _, _ := repos.Character.Load(context.Background(), "60")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(backup, other) {
				t.Fatal("免费重入改变队员属性或再次扣金币")
			}
			tagPlayMoveNext(t, s, p)
			if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil || b.run.Snapshot().Active != 1 {
				t.Fatal("重入后服务端切换处理失败", err)
			}
		})
	}
}

func TestTagSelectorEarlyRestoreLeavesUnpaidAndOrdinaryChannelsAlone(t *testing.T) {
	for _, scenario := range []string{"未购买", "普通频道", "休息室未就绪", "首次已创建"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, _ := tagPlayFixture(t)
			p.townSceneReadyCharacterID = p.selectedCharacterID
			p.townPositionSnapshot = p.townSelectorOriginSnapshot
			if scenario != "未购买" {
				if err := s.handleCurrentTagTournamentEnter(p, []byte{2}); err != nil || p.tagTournamentBattle == nil {
					t.Fatal(err)
				}
				if scenario != "首次已创建" {
					s.retireCurrentTagTournamentBattle(p)
				}
			}
			if scenario == "普通频道" {
				p.channel.Type = 0
				s.tagTournamentCatalog = nil
			}
			if scenario == "休息室未就绪" {
				p.townSceneReadyCharacterID = 0
			}
			old := p.tagTournamentBattle
			p.conn.(*bufferConn).write.Reset()
			if err := s.sendEnterSelectDungeonState(p, "upper_15", false, true); err != nil {
				t.Fatal(err)
			}
			counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
			if p.tagTournamentBattle != old || counts[1389] != 0 || counts[1458] != 0 || counts[15] != 1 {
				t.Fatal("不符合重入条件仍准备挑战，或破坏原选图应答", counts)
			}
		})
	}
}

func TestTagSelectorEarlyRestoreWriteFailureDoesNotOpenSelectorAndCanRetry(t *testing.T) {
	s, p, g := tagPlayFixture(t)
	if err := s.handleCurrentTagTournamentEnter(p, []byte{1}); err != nil || p.tagTournamentBattle == nil {
		t.Fatal(err)
	}
	s.retireCurrentTagTournamentBattle(p)
	p.townSceneReadyCharacterID = p.selectedCharacterID
	p.townPositionSnapshot = p.townSelectorOriginSnapshot
	before, _, _ := g.Character.Load(context.Background(), "29")
	failure := errors.New("隔离重入创建写失败")
	p.conn = &failNthDungeonWriteConn{failAt: 1, err: failure}
	if err := s.sendEnterSelectDungeonState(p, "upper_15", false, true); err != nil && !errors.Is(err, failure) {
		t.Fatal(err)
	}
	b := p.tagTournamentBattle
	if b == nil || b.prepared.creationSent || p.enterSelectDungeonContextSent {
		t.Fatal("创建写失败却提前打开选图或丢弃已恢复挑战")
	}
	p.conn = &bufferConn{}
	if err := s.sendEnterSelectDungeonState(p, "upper_15_retry", false, true); err != nil || p.tagTournamentBattle != b || !b.prepared.creationSent || !p.enterSelectDungeonContextSent {
		t.Fatal("写失败不能在同一挑战重试", err)
	}
	after, _, _ := g.Character.Load(context.Background(), "29")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("重试创建再次扣款")
	}
}
