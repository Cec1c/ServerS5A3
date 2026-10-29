package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func tagResultActionFixture(t *testing.T, mode tagtournament.Mode, complete bool) (*Service, *gameSession) {
	t.Helper()
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
	if complete {
		for range p.tagTournamentBattle.plan.Encounters {
			tagPlayMoveNext(t, s, p)
			tagPlayKillBoss(t, s, p)
		}
	}
	p.conn.(*bufferConn).write.Reset()
	return s, p
}

func tagActionPacketCounts(t *testing.T, data []byte) map[uint16]int {
	t.Helper()
	out := map[uint16]int{}
	for len(data) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
		out[packet.Header.MsgID]++
		if packet.Header.MsgID == 72 && !bytes.Equal(packet.Body, buildCurrentDungeonOp72SuccessBody(1, 2)) {
			t.Fatal(packet)
		}
		data = rest
	}
	return out
}

func TestTagResultPageObserved72ReturnsTownAndCommitsOnce(t *testing.T) {
	for _, mode := range []tagtournament.Mode{tagtournament.Normal, tagtournament.Hard} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, p := tagResultActionFixture(t, mode, true)
			runtime := p.dungeon.runtime
			if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
			if counts[72] != 1 || counts[24] != 1 || counts[42] != 0 || counts[2] < 2 || !runtime.townReturnPending || runtime.townReturnRequestMsgID != 72 || p.tagTournamentBattle != nil || p.dungeon.runtime != nil || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted {
				t.Fatal(counts)
			}
			if runtime.settlementCardLayoutSent || runtime.settlementClearRewardSent {
				t.Fatal("帝国回城进入了普通翻牌结算")
			}
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			counts = tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
			if counts[72] != 0 || counts[24] != 0 {
				t.Fatal("重复返回重发确认或场景", counts)
			}
			if err := s.commitPendingDungeonReturnBeforeTownEnterSelect(p, "帝国72实测回归城镇确认"); err != nil {
				t.Fatal(err)
			}
			if p.tagTournamentBattle != nil || p.dungeon.runtime != nil {
				t.Fatal("72实际回城后仍保留旧挑战")
			}
			tagCompleteTownSceneCallback(t, s, p)
			p.conn.(*bufferConn).write.Reset()
			if err := s.handleCurrentTagTournamentRosterList(p, nil); err != nil || p.conn.(*bufferConn).write.Len() == 0 {
				t.Fatal("实际回城后队伍设置没有发出资料", err)
			}
		})
	}
}

func TestTagResultPageDoesNotBypassUnfinishedBattleOrAcceptRetryOptions(t *testing.T) {
	s, p := tagResultActionFixture(t, tagtournament.Hard, false)
	if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil || p.conn.(*bufferConn).write.Len() != 0 || p.dungeon.runtime.townReturnPending {
		t.Fatal("未通关可伪造结果页返回", err)
	}
	for range p.tagTournamentBattle.plan.Encounters {
		tagPlayMoveNext(t, s, p)
		tagPlayKillBoss(t, s, p)
	}
	for _, option := range []byte{0, 1, 3} {
		p.conn.(*bufferConn).write.Reset()
		if err := s.handleDungeonEplpCommand(p, []byte{1, option}); err != nil || p.conn.(*bufferConn).write.Len() != 0 || p.dungeon.runtime.townReturnPending {
			t.Fatal(option, err)
		}
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.handleDungeonEplpCommand(p, []byte{2, 2}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitCurrentGameServerUpperPacketAuto(t, p.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != 72 || !bytes.Equal(packet.Body, buildCurrentDungeonOp72SuccessBody(2, 2)) || len(rest) != 0 || p.dungeon.runtime.settlementCardExitAckSent || p.dungeon.runtime.townReturnPending {
		t.Fatal("悬停消耗了真实返回", packet)
	}
}

func TestTagResultPageReturnWriteFailureCanRetry(t *testing.T) {
	for _, failAt := range []int{3, 4} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			s, p := tagResultActionFixture(t, tagtournament.Hard, true)
			failure := errors.New("隔离帝国72返回写失败")
			p.conn = &failNthDungeonWriteConn{failAt: failAt, err: failure}
			if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			runtime := p.dungeon.runtime
			if runtime == nil || p.tagTournamentBattle == nil || runtime.townReturnOp24Sent || runtime.settlementCardExitAckSent != (failAt == 4) {
				t.Fatal("失败丢失状态或提前标记24成功")
			}
			p.conn = &bufferConn{}
			if err := s.handleDungeonEplpCommand(p, []byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
			wantACK := 1
			if failAt == 4 {
				wantACK = 0
			}
			if counts[72] != wantACK || counts[24] != 1 || !runtime.townReturnOp24Sent {
				t.Fatal(counts)
			}
		})
	}
}

func TestTagFirstLoadingRetriesOnlyInitialRoleWithoutRecreatingCharacters(t *testing.T) {
	s, p := tagResultActionFixture(t, tagtournament.Hard, false)
	b := p.tagTournamentBattle
	if !b.prepared.initialTagLoadPending {
		t.Fatal("首次读条未安排补投")
	}
	p.currentFinishLoadingStateSent, p.currentFinishLoadingCompletionSent, p.postFinishLoadingPlayerStateSent = true, true, true
	if err := s.sendCurrentFinishLoadingCharacterState(p, "帝国首次37回归"); err != nil {
		t.Fatal(err)
	}
	counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[1402] != 1 || counts[1394] != 1 || counts[1389] != 0 || b.prepared.initialTagLoadPending || !b.run.Snapshot().NextTag.IsZero() {
		t.Fatal(counts)
	}
	p.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentFinishLoadingCharacterState(p, "帝国重复37回归"); err != nil || p.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("重复读条重投", err)
	}
}

func TestTagFirstLoadingProjectionFailureRetriesAfterOrdinaryTailCommitted(t *testing.T) {
	s, p := tagResultActionFixture(t, tagtournament.Hard, false)
	p.currentFinishLoadingStateSent, p.currentFinishLoadingCompletionSent, p.postFinishLoadingPlayerStateSent = true, true, true
	failure := errors.New("隔离37尾包后1402写失败")
	p.conn = &failNthDungeonWriteConn{failAt: 2, err: failure}
	if err := s.sendCurrentFinishLoadingCharacterState(p, "帝国37补投失败"); !errors.Is(err, failure) || !p.tagTournamentBattle.prepared.initialTagLoadPending {
		t.Fatal(err)
	}
	p.conn = &bufferConn{}
	if err := s.sendCurrentFinishLoadingCharacterState(p, "帝国37补投重试"); err != nil {
		t.Fatal(err)
	}
	counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
	if counts[1394] != 1 || counts[1402] != 1 || counts[1389] != 0 || p.tagTournamentBattle.prepared.initialTagLoadPending || !p.tagTournamentBattle.run.Snapshot().NextTag.IsZero() {
		t.Fatal(counts)
	}
}
