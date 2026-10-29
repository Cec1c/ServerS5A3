package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

func TestTagInitialRoleIsProjectedAfterOrdinaryPlacementWithoutTagCooldown(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, p, _ := tagPlayFixture(t)
			tagPlayChallenge(t, s, p, mode)
			var order []uint16
			for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
				packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				order = append(order, packet.Header.MsgID)
				if packet.Header.MsgID == 1402 && (packet.Header.Classification != 0 || !bytes.Equal(packet.Body, []byte{0})) {
					t.Fatal("初始出战位置无效", packet)
				}
				if packet.Header.MsgID == 1389 || packet.Header.MsgID == 1465 {
					t.Fatal("进图重建角色或伪造Tab上行应答")
				}
				rest = tail
			}
			if len(order) < 3 || !reflect.DeepEqual(order[len(order)-2:], []uint16{1394, 1402}) {
				t.Fatal("没有在普通落位完成后选择唯一出战者", order)
			}
			if snapshot := p.tagTournamentBattle.run.Snapshot(); snapshot.Active != 0 || !snapshot.NextTag.IsZero() {
				t.Fatal("初始化修改出战者或制造Tag冷却", snapshot)
			}
		})
	}
}

func TestTagHardRoundIconsAndTerminalPacketOnlyOnce(t *testing.T) {
	s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Hard)
	b := p.tagTournamentBattle
	for i := 0; i < 7; i++ {
		p.conn.(*bufferConn).write.Reset()
		tagBattleBridgeClear(t, s, p, now, false)
		var notice, end []byte
		for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
			packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
			if packet.Header.MsgID == 1397 {
				notice = packet.Body
			}
			if packet.Header.MsgID == 1396 {
				end = packet.Body
			}
			rest = tail
		}
		want, _ := s.tagTournamentCatalog.HardRewardTotal(i + 1)
		if i > 0 {
			prior, _ := s.tagTournamentCatalog.HardRewardTotal(i)
			for n := range want {
				want[n].Count -= prior[n].Count
			}
		}
		body, _ := buildCurrentTagTournamentRoundReward(want)
		if !bytes.Equal(notice, body) {
			t.Fatal("困难显示没有采用本阶段真实交付差额", i, notice, body)
		}
		if (end != nil) != (i == 6) {
			t.Fatal("通关通知提前或遗漏", i, end)
		}
		if count, _ := tagBattleMailTotals(t, g); count != i+1 {
			t.Fatal("每阶段未即时发放邮件", count)
		}
		if i == 6 && (len(end) != 16 || binary.LittleEndian.Uint32(end[8:]) != 13000) {
			t.Fatal("首回合至末次击杀的固定用时错误", end)
		}
	}
	if count, _ := tagBattleMailTotals(t, g); count != 7 {
		t.Fatal("阶段邮件缺失或重复", count)
	}
	frozen := append([]byte(nil), b.completionBody...)
	*now = now.Add(time.Minute)
	p.conn.(*bufferConn).write.Reset()
	if err := s.flushCurrentTagTournamentSettlement(p, b); err != nil || p.conn.(*bufferConn).write.Len() != 0 || !bytes.Equal(frozen, b.completionBody) {
		t.Fatal("重复补偿重发终局或延长通关计时", err)
	}
}

func TestTagCompletionSocketRetryUsesFrozenTimeAndDoesNotRepeatMail(t *testing.T) {
	s, p, g, now := tagBattleBridgeFixture(t, tagtournament.Hard)
	for i := 0; i < 6; i++ {
		tagBattleBridgeClear(t, s, p, now, false)
	}
	*now = now.Add(time.Second)
	e, _, err := s.beginCurrentTagTournamentRound(p, false)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	failure := errors.New("隔离终局包写失败")
	c := &failNthDungeonWriteConn{failAt: 4, err: failure}
	p.conn = c
	if err = s.confirmCurrentTagTournamentRound(p, e.MapID, []uint32{e.MonsterID}, true); !errors.Is(err, failure) {
		t.Fatal("未命中终局通知失败", err)
	}
	b := p.tagTournamentBattle
	frozen := append([]byte(nil), b.completionBody...)
	if b.completionSent || len(frozen) != 16 {
		t.Fatal("丢失终局重试状态")
	}
	*now = now.Add(time.Minute)
	p.tagTournamentLobbyDay = b.plan.ServiceDay
	c.write.Reset()
	if err = s.refreshCurrentTagTournamentLobby(p, *now); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, c.write.Bytes())
	if packet.Header.MsgID != 1396 || !bytes.Equal(packet.Body, frozen) || len(rest) != 0 || !b.completionSent {
		t.Fatal("重试未只补固定终局", packet)
	}
	if count, _ := tagBattleMailTotals(t, g); count != 7 {
		t.Fatal("通知重试重复邮件", count)
	}
}

func TestTagReturnCommitReleasesOldRunAndAllowsPaidReentry(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, p, g := tagPlayFixture(t)
			enableTownMoveSkillProjection(t, s, g, "29")
			ch, _, err := g.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			ch.Stats["direction"], ch.Stats["area_state"] = 0, 0
			if err = g.Character.Save(context.Background(), ch); err != nil {
				t.Fatal(err)
			}
			tagPlayChallenge(t, s, p, mode)
			old := p.tagTournamentBattle
			for range old.plan.Encounters {
				tagPlayMoveNext(t, s, p)
				tagPlayKillBoss(t, s, p)
			}
			before, _, _ := g.Character.Load(context.Background(), "29")
			mails, total := tagBattleMailTotals(t, g)
			if err := s.handleDungeonGiveupGame(p, nil); err != nil {
				t.Fatal(err)
			}
			if p.tagTournamentBattle != old || p.dungeon.runtime == nil || !p.dungeon.runtime.townReturnPending {
				t.Fatal("回城发送阶段提前释放挑战或未挂起")
			}
			if err := s.commitPendingDungeonReturnBeforeTownEnterSelect(p, "帝国回归场景确认"); err != nil {
				t.Fatal(err)
			}
			if p.tagTournamentBattle != nil || p.dungeon.runtime != nil {
				t.Fatal("已确认回城仍留下旧挑战")
			}
			// 城镇完整尾包由真实场景回调负责；夹具在已确认回城后提供其完成状态。
			p.sceneBootstrapTailSent = true
			p.sceneBootstrapTailDeferred = false
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(old.prepared.runtime.Request)); err != nil {
				t.Fatal(err)
			}
			if p.tagTournamentBattle == nil || p.tagTournamentBattle == old || !p.tagTournamentBattle.prepared.entered {
				t.Fatal("回城后未创建下一场")
			}
			tagPlayAdvanceClock(t, s, p)
			for range p.tagTournamentBattle.plan.Encounters {
				tagPlayMoveNext(t, s, p)
				tagPlayKillBoss(t, s, p)
			}
			after, _, _ := g.Character.Load(context.Background(), "29")
			again, totals := tagBattleMailTotals(t, g)
			if before.Stats["gold"] != after.Stats["gold"] || mails != again || !reflect.DeepEqual(total, totals) {
				t.Fatal("再次入场扣款或重复发奖")
			}
		})
	}
}

func TestTagCompletionRejectsInvalidClock(t *testing.T) {
	now := time.Now()
	for _, span := range [][2]time.Time{{{}, now}, {now, now.Add(-time.Second)}, {now, now.Add(30 * 24 * time.Hour)}} {
		if _, err := buildCurrentTagTournamentCompletion(span[0], span[1]); err == nil {
			t.Fatal("允许无效终局时钟")
		}
	}
}
