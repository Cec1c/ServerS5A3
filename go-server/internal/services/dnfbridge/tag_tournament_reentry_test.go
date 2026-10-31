package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func TestTagPaidReentryBuildsActorsBeforeMapWithout1458OrSecondFee(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, p, g := tagPlayFixture(t)
			ctx := context.Background()
			if err := s.handleCurrentTagTournamentEnter(p, []byte{byte(mode)}); err != nil || p.tagTournamentBattle == nil {
				t.Fatal("首次购买未成功", err)
			}
			old := p.tagTournamentBattle
			request := old.prepared.runtime.Request
			s.retireCurrentTagTournamentBattle(p)
			if p.tagTournamentBattle != nil || p.dungeon.runtime != nil {
				t.Fatal("断线后仍保留连接挑战")
			}
			before, _, _ := g.Character.Load(ctx, "29")
			backup, _, _ := g.Character.Load(ctx, "60")
			p.conn.(*bufferConn).write.Reset()
			// 已付费原生只发16；15已发过上下文，不能依赖再收1458。
			p.enterSelectDungeonContextSent = true
			if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil {
				t.Fatal(err)
			}
			b := p.tagTournamentBattle
			if b == nil || b == old || !b.prepared.entered || p.dungeon.runtime != b.prepared.runtime || b.plan.Mode != mode {
				t.Fatal("已支付重登后选图没有恢复完整挑战")
			}
			seen := map[uint16]int{}
			var order []uint16
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				seen[packet.Header.MsgID]++
				if packet.Header.MsgID == 1389 || packet.Header.MsgID == 16 || packet.Header.MsgID == 27 || packet.Header.MsgID == 28 || packet.Header.MsgID == 29 {
					order = append(order, packet.Header.MsgID)
				}
				if packet.Header.MsgID == 16 && (packet.Header.Classification != 1 || packet.Body[0] != 1) {
					t.Fatal("16没有按自身协议成功应答")
				}
				rest = tail
			}
			if seen[1389] != 1 || seen[1390] != 1 || seen[1458] != 0 || !reflect.DeepEqual(order, []uint16{1389, 16, 27, 28, 29}) {
				t.Fatal("恢复重发购买ACK、重复创建或创建晚于地图", seen, order)
			}
			after, _, _ := g.Character.Load(ctx, "29")
			other, _, _ := g.Character.Load(ctx, "60")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(backup, other) {
				t.Fatal("已付费恢复再次扣款或改写队员")
			}
			account, _, _ := g.Account.Load(ctx, "a")
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil {
				t.Fatal(err)
			}
			packet, tail := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			latest, _, _ := g.Account.Load(ctx, "a")
			if packet.Header.MsgID != 16 || !bytes.Equal(packet.Body, []byte{0, 0x13, 0}) || len(tail) != 0 || !reflect.DeepEqual(account, latest) {
				t.Fatal("重复16更新凭据或重建角色")
			}
			tagPlayMoveNext(t, s, p)
			if err := s.handleCurrentTagTournamentTag(p, []byte{1}); err != nil || b.run.Snapshot().Active != 1 {
				t.Fatal("恢复后无法使用原生备用位置切人", err)
			}
		})
	}
}

func TestTagPaidReentryRejectsUnpaidAndWrongSelectionWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"未购买", "模式不符", "难度不符", "额外模式", "队伍未完成", "不在休息室"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, g := tagPlayFixture(t)
			ctx := context.Background()
			request := dungeoncmd.SelectDungeonRequest{DungeonID: 320, RuntimeToken: 0xffff}
			if scenario != "未购买" {
				if err := s.handleCurrentTagTournamentEnter(p, []byte{1}); err != nil || p.tagTournamentBattle == nil {
					t.Fatal(err)
				}
				request = p.tagTournamentBattle.prepared.runtime.Request
				s.retireCurrentTagTournamentBattle(p)
			}
			switch scenario {
			case "模式不符":
				request.DungeonID = 336
			case "难度不符":
				request.Difficulty = 1
			case "额外模式":
				request.SpecialMode = 1
			case "队伍未完成":
				p.currentRenameRoster = nil
				s.tagTournamentSentRosters = nil
			case "不在休息室":
				ch, _, _ := g.Character.Load(ctx, "29")
				ch.Stats["town_id"] = 1
				if err := g.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := g.Account.Load(ctx, "a")
			ch, _, _ := g.Character.Load(ctx, "29")
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(request)); err != nil {
				t.Fatal(err)
			}
			packet, tail := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
			if packet.Header.MsgID != 16 || !bytes.Equal(packet.Body, []byte{0, 0x13, 0}) || len(tail) != 0 || p.tagTournamentBattle != nil || p.dungeon.runtime != nil {
				t.Fatal("非法重入改变状态或进入地图", packet)
			}
			after, _, _ := g.Account.Load(ctx, "a")
			got, _, _ := g.Character.Load(ctx, "29")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(ch, got) {
				t.Fatal("非法重入改写金币或账本")
			}
		})
	}
}
