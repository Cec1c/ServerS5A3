package dnfbridge

import (
	"context"
	pvp "longheng.io/server/internal/modules/dnf/pvp"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestPVPDojoPoseReportRequiresSettledWinnerAndAssignedCard(t *testing.T) {
	s := pvpRoomTestService(t)
	r, _ := s.repositoryGroup()
	now := s.gameplayNow()
	room := &currentPVPRoom{id: 1, battleMode: 2, matchingToken: 1, started: true, seatState: [8]byte{1, 2}, match: &currentPVPMatch{winner: 0, resultSent: true, settlementPending: true, startedAt: now.Add(-time.Minute), completedAt: now}}
	s.pvpDailyCatalog.Pools[2] = []pvp.Mission{{ID: 133, Tier: 2, Weight: 10, RankMax: 30, Condition: "win pose", Target: 1, Parameter: 3, Emblems: 100}}
	for seat, id := range []uint16{29, 30} {
		g := &gameSession{conn: &bufferConn{}, accountID: "pvp-" + strconv.Itoa(int(id)), selectedCharacterID: id, channel: s.catalog.Channels()[1], pvpRoom: room}
		g.channel.Type = 8
		room.members[seat] = &currentPVPMember{session: g, characterID: id}
	}
	if err := s.settleCurrentPVPDaily(room); err != nil {
		t.Fatal(err)
	}
	before, _, _ := r.Character.Load(context.Background(), "29")
	winner, loser := room.members[0].session, room.members[1].session
	for _, body := range [][]byte{{133, 0}, {133, 0, 3}, {132, 0, 0}} {
		if err := s.handleCurrentPVPMissionReport(winner, 377, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.handleCurrentPVPMissionReport(loser, 377, []byte{133, 0, 1}); err != nil {
		t.Fatal(err)
	}
	if len(room.match.poseReports[0])+len(room.match.poseReports[1]) != 0 {
		t.Fatal("无效姿态上报被接受")
	}
	if err := s.handleCurrentPVPMissionReport(winner, 377, []byte{133, 0, 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.handleCurrentPVPMissionReport(winner, 377, []byte{133, 0, 2}); err != nil {
		t.Fatal(err)
	}
	if room.match.poseReports[0][133] != 1 || len(room.match.poseReports[1]) != 0 {
		t.Fatal("同场重复或败者上报未隔离")
	}
	after, _, _ := r.Character.Load(context.Background(), "29")
	if reflect.DeepEqual(before.Stats, after.Stats) || after.Stats["pvp_daily_2_pose_mask"] != 2 || after.Stats[pvp.EmblemsKey] != 0 || room.match.settled[0].Rows[2].Progress != 0 {
		t.Fatal("真实结算中的姿态未计数，或一场展示多种姿态提前发奖")
	}
}

func TestPVPPoseThreeKindsAcrossCommittedWinsPaysOnce(t *testing.T) {
	s := pvpRoomTestService(t)
	s.pvpDailyCatalog.Pools[2] = []pvp.Mission{{ID: 133, Tier: 2, Weight: 10, RankMax: 30, Condition: "win pose", Target: 1, Parameter: 3, Emblems: 100}}
	now := s.gameplayNow()
	room := &currentPVPRoom{id: 1, battleMode: 2, matchingToken: 1, started: true, seatState: [8]byte{1, 2}}
	for seat, id := range []uint16{29, 30} {
		g := &gameSession{conn: &bufferConn{}, accountID: "pvp-" + strconv.Itoa(int(id)), selectedCharacterID: id, channel: s.catalog.Channels()[1], pvpRoom: room}
		g.channel.Type = 8
		room.members[seat] = &currentPVPMember{session: g, characterID: id}
	}
	for pose := byte(0); pose < 3; pose++ {
		room.match = &currentPVPMatch{winner: 0, resultSent: true, settlementPending: true, startedAt: now.Add(-time.Duration(4-pose) * time.Minute), completedAt: now.Add(-time.Duration(3-pose) * time.Second)}
		if err := s.settleCurrentPVPDaily(room); err != nil {
			t.Fatal(err)
		}
		winner := room.members[0].session
		winner.conn.(*bufferConn).write.Reset()
		for retry := 0; retry < 2; retry++ {
			if err := s.handleCurrentPVPMissionReport(winner, 377, []byte{133, 0, pose}); err != nil {
				t.Fatal(err)
			}
		}
		var updates, wallets int
		for _, p := range splitAllFinishBridgePackets(t, winner.conn.(*bufferConn).write.Bytes()) {
			if p.Header.MsgID == 578 {
				updates++
			}
			if p.Header.MsgID == 14 {
				wallets++
			}
		}
		if wallets != 1 || updates != map[bool]int{false: 0, true: 1}[pose == 2] {
			t.Fatal("姿态完成通知或钱包重发", updates, wallets)
		}
	}
	r, _ := s.repositoryGroup()
	character, _, _ := r.Character.Load(context.Background(), "29")
	if character.Stats[pvp.EmblemsKey] != 100 || character.Stats["pvp_daily_2_progress"] != 1 {
		t.Fatal("真实三场胜利未完成一轮姿态", character.Stats)
	}
}
