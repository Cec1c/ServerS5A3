package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

// doorArrivalFixture 造一个**共享实例**里的两个成员（1001/1002），供"门口就位"选票
// （op45 证据）用例使用。实例与房间对象复用既有 partyDungeonInstanceFixture。
func doorArrivalFixture(t *testing.T) (*Service, *gameSession, *gameSession, runtimeDungeonRoomKey, runtimeDungeonRoomKey) {
	t.Helper()
	service, leader, follower, leaderRuntime, followerRuntime, scene := partyDungeonInstanceFixture(t, 7, 4242)
	// 让会话真的持有这两份运行时（fixture 只把它们当局部变量返回）。
	leader.dungeon.mu.Lock()
	leader.dungeon.runtime = leaderRuntime
	leader.dungeon.mu.Unlock()
	follower.dungeon.mu.Lock()
	follower.dungeon.runtime = followerRuntime
	follower.dungeon.mu.Unlock()
	if instance := service.adoptPartyDungeonInstance(leader, leaderRuntime, scene); instance == nil {
		t.Fatal("first member did not publish a party dungeon instance")
	}
	if instance := service.adoptPartyDungeonInstance(follower, followerRuntime, scene); instance == nil {
		t.Fatal("second member did not join the party dungeon instance")
	}
	source := runtimeDungeonRoomKeyFromScene(scene)
	target := runtimeDungeonRoomKey{X: source.X + 1, Y: source.Y, MapID: source.MapID + 1}
	return service, leader, follower, source, target
}

// op45 的坐标字段（body[2:6]/[6:10]，current EXE sub_17C9D80）是副本里唯一带已证实偏移的
// 位置上报：按 (源房间,目标房间) 归并成"就位选票"，同一成员重复请求只算一票，
// 不同房间对各自计数，并且**不放行任何过图**（enforced=false）。
func TestPartyDungeonDoorArrivalRecordsDistinctMemberVotes(t *testing.T) {
	service, leader, follower, source, _ := doorArrivalFixture(t)
	request := dungeoncmd.MoveMapRequest{NextX: 1, NextY: 1, PositionX: 1231, PositionY: 221, MoveKind: 0}

	leader.dungeon.mu.Lock()
	service.recordCurrentDungeonDoorArrivalLocked(leader, source, request)
	leader.dungeon.mu.Unlock()

	leaderRuntime := leader.dungeon.runtime
	if leaderRuntime == nil || leaderRuntime.partyInstance == nil {
		t.Fatal("leader runtime lost its shared instance")
	}
	// 选票键 = (源房间, 目标房间)：目标房间取自请求的 NextX/NextY（byte），MapID 未知为 0。
	arrivalKey := partyDungeonDoorArrivalKey{
		Source: source,
		Target: runtimeDungeonRoomKey{X: int64(request.NextX), Y: int64(request.NextY)},
	}
	leaderRuntime.partyInstance.mu.Lock()
	votes := leaderRuntime.partyInstance.doorArrivals[arrivalKey]
	recorded, hasRecord := votes[leader.selectedCharacterID]
	members := len(leaderRuntime.partyInstance.members)
	leaderRuntime.partyInstance.mu.Unlock()
	if !hasRecord || len(votes) != 1 || members != 2 {
		t.Fatalf("first op45 vote recorded=%t distinct=%d members=%d, want true/1/2", hasRecord, len(votes), members)
	}
	if recorded.X != 1231 || recorded.Y != 221 || recorded.MoveKind != 0 {
		t.Fatalf("recorded arrival=%+v want 1231/221/kind0", recorded)
	}

	// 同一成员重复点门：仍只算一票（后一条覆盖前一条的位置）。
	leader.dungeon.mu.Lock()
	service.recordCurrentDungeonDoorArrivalLocked(
		leader, source,
		dungeoncmd.MoveMapRequest{NextX: 1, NextY: 1, PositionX: 1200, PositionY: 200},
	)
	leader.dungeon.mu.Unlock()
	leaderRuntime.partyInstance.mu.Lock()
	votes = leaderRuntime.partyInstance.doorArrivals[arrivalKey]
	repeated := votes[leader.selectedCharacterID]
	leaderRuntime.partyInstance.mu.Unlock()
	if len(votes) != 1 || repeated.X != 1200 {
		t.Fatalf("repeat vote counted=%d x=%d, want 1/1200", len(votes), repeated.X)
	}

	// 第二个成员就位 ⇒ 2/2（操作者规则在 2 人时要求全到）。
	follower.dungeon.mu.Lock()
	service.recordCurrentDungeonDoorArrivalLocked(
		follower, source,
		dungeoncmd.MoveMapRequest{NextX: 1, NextY: 1, PositionX: 409, PositionY: 260},
	)
	follower.dungeon.mu.Unlock()
	followerRuntime := follower.dungeon.runtime
	followerRuntime.partyInstance.mu.Lock()
	votes = followerRuntime.partyInstance.doorArrivals[arrivalKey]
	// op45 的"门口坐标"属于**源房间**，绝不写进"当前房间实时坐标"（那是 op35 的语义）。
	positions := len(followerRuntime.partyInstance.positions)
	followerRuntime.partyInstance.mu.Unlock()
	if len(votes) != 2 {
		t.Fatalf("distinct member votes=%d want 2", len(votes))
	}
	if positions != 0 {
		t.Fatalf("op45 arrival must not be recorded as the live room position: %d rows", positions)
	}
}

// 另一对 (源房间,目标房间) 是另一份选票。
func TestPartyDungeonDoorArrivalKeysByRoomPair(t *testing.T) {
	_, _, follower, source, target := doorArrivalFixture(t)
	follower.dungeon.mu.Lock()
	instance := follower.dungeon.runtime.partyInstance
	instance.mu.Lock()
	if instance.doorArrivals == nil {
		instance.doorArrivals = make(map[partyDungeonDoorArrivalKey]map[uint16]partyDungeonDoorArrival)
	}
	instance.mu.Unlock()
	votes, _ := follower.dungeon.runtime.recordPartyDungeonDoorArrival(
		follower.selectedCharacterID, source, target, partyDungeonDoorArrival{X: 1, Y: 2},
	)
	otherVotes, _ := follower.dungeon.runtime.recordPartyDungeonDoorArrival(
		follower.selectedCharacterID,
		runtimeDungeonRoomKey{X: 5, Y: 5, MapID: 1},
		runtimeDungeonRoomKey{X: 6, Y: 6},
		partyDungeonDoorArrival{X: 3, Y: 4},
	)
	follower.dungeon.mu.Unlock()
	if votes != 1 || otherVotes != 1 {
		t.Fatalf("votes=%d other=%d want 1/1 (each room pair has its own ballot)", votes, otherVotes)
	}
}

// 操作者期望的门规则阈值（3 人三取二、4 人四取三）只用于日志判读，本轮不放行。
func TestPartyDungeonDoorArrivalQuorumMatchesOperatorRule(t *testing.T) {
	cases := map[int]int{0: 0, 1: 0, 2: 2, 3: 2, 4: 3, 5: 3}
	for members, want := range cases {
		if got := partyDungeonDoorArrivalQuorum(members); got != want {
			t.Fatalf("quorum(%d)=%d want %d", members, got, want)
		}
	}
}

// 服务端 P2P 中继的流量计数：有数据报就累加，客户端从没发过就恒为 0。
// 这是判定线上"队友不动"到底是"服务端位置转发缺失"还是"P2P 通道没建起来"的唯一可观测手段。
func TestCurrentPartyUDPRelayCountsForwardedDatagrams(t *testing.T) {
	portStart := testCurrentPartyUDPRelayPortStart(t)
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", portStart, 12, nil)
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	defer func() { _ = relay.Close() }()
	if total := relay.ForwardedDatagramTotal(); total != 0 {
		t.Fatalf("fresh relay total=%d want 0", total)
	}
	leg := &currentPartyUDPRelayLeg{}
	if datagrams, bytesForwarded, first := leg.noteForwarded(24); datagrams != 1 || bytesForwarded != 24 || !first {
		t.Fatalf("first noteForwarded = %d/%d/%t want 1/24/true", datagrams, bytesForwarded, first)
	}
	if datagrams, bytesForwarded, first := leg.noteForwarded(8); datagrams != 2 || bytesForwarded != 32 || first {
		t.Fatalf("second noteForwarded = %d/%d/%t want 2/32/false", datagrams, bytesForwarded, first)
	}
	relay.forwardedTotal.Add(2)
	if total := relay.ForwardedDatagramTotal(); total != 2 {
		t.Fatalf("relay total=%d want 2", total)
	}
}
