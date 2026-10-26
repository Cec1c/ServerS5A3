package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestRealPVFCurrentFiendWarFailurePresentationNoSettlementUI(t *testing.T) {
	for _, subscribed := range []bool{false, true} {
		t.Run(map[bool]string{false: "新连接恢复失败", true: "已订阅战况"}[subscribed], func(t *testing.T) {
			f, peers := actualCurrentFiendWar(t, 1)
			p := peers[0]
			conn := p.conn.(*bufferConn)
			b := f.state.Battle
			f.state.Started = true
			now := time.Now()
			b.EnterAfter, b.Deadline = now.Add(-10*time.Minute), now.Add(10*time.Minute)
			if subscribed {
				if err := f.service.sendCurrentRaidProgressProjection(p, *f.state, false, false); err != nil {
					t.Fatal(err)
				}
				conn.write.Reset()
			}
			b.TimedOut, b.FiendWar.Failed, b.FiendWarEndedAt = true, true, now
			before, _, _ := f.repos.Inventory.Load(context.Background(), "19")
			// 复现现场两个战况发布者：它们不能提前打开结算UI或重复播失败。
			for i := 0; i < 2; i++ {
				if err := f.service.sendCurrentRaidProgressProjection(p, *f.state, false, false); err != nil {
					t.Fatal(err)
				}
			}
			board := append([]byte(nil), conn.write.Bytes()...)
			for len(board) > 0 {
				packet, rest := splitGameServerUpperPacket(t, board)
				if packet.Header.MsgID == 587 || packet.Header.MsgID == 601 || packet.Header.MsgID == currentFiendWarStateNotice {
					t.Fatal("失败战况重放了开始/结算/失败状态，终局结果未独占呈现", packet)
				}
				board = rest
			}
			conn.write.Reset()
			for i := 0; i < 2; i++ {
				if err := f.service.sendCurrentAntonTerminalResult(p, *f.state); err != nil {
					t.Fatal(err)
				}
			}
			result, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			state, rest := splitGameServerUpperPacket(t, rest)
			if result.Header.MsgID != 601 || result.Header.Classification != 0 || len(result.Body) != 10 || result.Body[0] != 3 || result.Body[9] != 1 || state.Header.MsgID != currentFiendWarStateNotice || !bytes.Equal(state.Body, []byte{6, 0}) || len(rest) != 0 {
				t.Fatal("必须只发一次601类型3，再发1484状态6", result, state, rest)
			}
			after, _, _ := f.repos.Inventory.Load(context.Background(), "19")
			if !reflect.DeepEqual(before, after) || b.RewardFlow[0] != nil || len(f.service.raidWeeklyIntents) != 0 || b.PhaseCompleted {
				t.Fatal("失败动画制造了物品、通关奖励或周记录")
			}
		})
	}
}

func TestRealPVFCurrentFiendWarSuccessResultPresentationUnchanged(t *testing.T) {
	f, peers := actualCurrentFiendWar(t, 1)
	p := peers[0]
	f.state.Started = true
	b := f.state.Battle
	now := time.Now()
	b.EnterAfter, b.Deadline, b.PhaseCompletedAt = now.Add(-10*time.Minute), now.Add(10*time.Minute), now
	b.PhaseCompleted, b.FiendWar.Complete = true, true
	b.ClearedRuns = map[string]currentRaidClearedRun{}
	for i := uint32(0); i < b.AntonRewards.Phases[0].MinimumClears; i++ {
		b.ClearedRuns[fmt.Sprint(i)] = currentRaidClearedRun{DungeonID: 4978, Attempt: 1, Participants: []uint16{p.selectedCharacterID}}
	}
	if err := f.service.freezeCurrentFiendWarRewardFlowLocked(*f.state); err != nil {
		t.Fatal(err)
	}
	if _, errs := f.service.commitCurrentRaidWeeklyIntents(now); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := f.service.sendCurrentAntonTerminalResult(p, *f.state); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
	if packet.Header.MsgID != currentFiendWarStateNotice || !bytes.Equal(packet.Body, []byte{3, 0}) {
		t.Fatal("成功前置状态被失败修复影响", packet)
	}
	packet, rest = splitGameServerUpperPacket(t, rest)
	if packet.Header.MsgID != 601 || packet.Body[0] != 0 {
		t.Fatal("原成功类型被改动", packet)
	}
	packet, rest = splitGameServerUpperPacket(t, rest)
	if packet.Header.MsgID != 587 || !bytes.Equal(packet.Body, []byte{3, 0}) || len(rest) != 0 {
		t.Fatal("原成功阶段呈现被改动", packet)
	}
}
