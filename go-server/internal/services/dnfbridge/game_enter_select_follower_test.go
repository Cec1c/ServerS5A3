package dnfbridge

import (
	"testing"
)

// 操作者报的"开始冒险没反应 / 进图加载条"里，队员侧这条链路此前没有直接用例。
// 第一条规则：**只有队长打开选择器才能把队友带进选择器**——队员自己打开城镇地图
// 绝不能把队长推进"跟随者"状态（历史上这正是进图崩溃的根因之一）。
func TestSynchronizeRuntimePartyMembersEnterSelectIgnoresNonLeader(t *testing.T) {
	_, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	storeRuntimePartyState(leader, townCoPresencePartyState())
	storeRuntimePartyState(member, townCoPresencePartyState())
	leaderConn.write.Reset()
	memberConn.write.Reset()

	if err := service.synchronizeRuntimePartyMembersEnterSelect(member, "test_non_leader_selector"); err != nil {
		t.Fatalf("non-leader selector sync: %v", err)
	}
	if leaderConn.write.Len() != 0 || memberConn.write.Len() != 0 {
		t.Fatalf("non-leader selector sync wrote packets: leader=%d member=%d",
			leaderConn.write.Len(), memberConn.write.Len())
	}
	if leader.enterSelectDungeonSent || leader.enterSelectDungeonContextSent {
		t.Fatalf("leader was dragged into the follower selector state: sent=%t context=%t",
			leader.enterSelectDungeonSent, leader.enterSelectDungeonContextSent)
	}
	if member.enterSelectDungeonSent || member.enterSelectDungeonContextSent {
		t.Fatalf("the requesting member must not install its own selector through this path: sent=%t context=%t",
			member.enterSelectDungeonSent, member.enterSelectDungeonContextSent)
	}
}

// 第二条规则：队长打开选择器时，同区域且城镇就绪的队员要拿到完整的原生选择器
// 序列（区域脱离 + 角色重建 + op15 成功 + 疲劳/权限 + 选择器上下文），并且
// **发起者自己不会再收到这些包**（这些是给跟随者的）。
func TestSynchronizeRuntimePartyMembersEnterSelectDrivesFollower(t *testing.T) {
	_, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
	storeRuntimePartyState(leader, townCoPresencePartyState())
	storeRuntimePartyState(member, townCoPresencePartyState())
	if ready, reason := service.currentTownEnterSelectReady(member); !ready {
		t.Fatalf("member fixture is not enter-select ready: %s", reason)
	}
	leaderConn.write.Reset()
	memberConn.write.Reset()

	if err := service.synchronizeRuntimePartyMembersEnterSelect(leader, "test_leader_selector"); err != nil {
		t.Fatalf("leader selector sync: %v", err)
	}
	if memberConn.write.Len() == 0 {
		t.Fatal("follower received no selector transition packets")
	}
	if leaderConn.write.Len() != 0 {
		t.Fatalf("the initiating leader must not receive the follower fan-out: %d bytes", leaderConn.write.Len())
	}
	if !member.enterSelectDungeonContextSent || !member.enterSelectDungeonSent || !member.enterSelectDungeonAckSent {
		t.Fatalf("follower selector state incomplete: context=%t sent=%t ack=%t",
			member.enterSelectDungeonContextSent, member.enterSelectDungeonSent, member.enterSelectDungeonAckSent)
	}
	packets := drainUpperPackets(t, memberConn)
	if len(packets) != 3 || packets[0].msgID != 15 || packets[1].msgID != currentFatigueMsgID || packets[2].msgID != currentDungeonContextMsgID {
		t.Fatalf("follower selector must retain town actor and permission cache: %+v", packets)
	}
	if member.preDungeonContextPlayerStateSent || member.selectPreviewActorRemoved {
		t.Fatal("opening the selector replaced or removed the follower actor")
	}
}

// 第三条规则：队员自己还没把城镇场景收尾时，必须先"延迟"而不是硬塞选择器
// （否则它会在没有场景所有者的时候被推着进选择器）。
func TestPrepareRuntimePartyFollowerEnterSelectDefersWhenTownNotReady(t *testing.T) {
	_, service, _, member, _, memberConn := newTownCoPresenceTestService(t)
	storeRuntimePartyState(member, townCoPresencePartyState())
	member.townMu.Lock()
	member.townSceneReadyCharacterID = 0
	member.townMu.Unlock()
	memberConn.write.Reset()

	prepared, err := service.prepareRuntimePartyFollowerEnterSelect(member, 1001, 77, "test_follower_not_ready")
	if err != nil {
		t.Fatalf("deferred follower prepare: %v", err)
	}
	if prepared {
		t.Fatal("a follower without a finalized town scene must not be prepared")
	}
	if memberConn.write.Len() != 0 {
		t.Fatalf("deferred follower received %d bytes", memberConn.write.Len())
	}
	if member.enterSelectDungeonSent || member.enterSelectDungeonContextSent {
		t.Fatalf("deferred follower flags changed: sent=%t context=%t",
			member.enterSelectDungeonSent, member.enterSelectDungeonContextSent)
	}
}

// 已经装过选择器（两个标记都在）时是幂等的：不会重复下发一遍。
func TestPrepareRuntimePartyFollowerEnterSelectIsIdempotent(t *testing.T) {
	_, service, _, member, _, memberConn := newTownCoPresenceTestService(t)
	storeRuntimePartyState(member, townCoPresencePartyState())
	member.enterSelectDungeonContextSent = true
	member.enterSelectDungeonSent = true
	memberConn.write.Reset()

	prepared, err := service.prepareRuntimePartyFollowerEnterSelect(member, 1001, 77, "test_follower_already_prepared")
	if err != nil || !prepared {
		t.Fatalf("already prepared follower: prepared=%t err=%v", prepared, err)
	}
	if memberConn.write.Len() != 0 {
		t.Fatalf("already prepared follower received %d duplicate bytes", memberConn.write.Len())
	}
}
