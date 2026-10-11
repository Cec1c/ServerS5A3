package dnfbridge

import "testing"

// 队员自己打开选择器时的 A/B 钉死（见 party_selector_member_self_prepare.go）：
//
//	off（默认）= 逐字节回到改动前：只记 non-leader-initiated、零发包、谁的状态都不动；
//	on        = 给**该发起者本人**补完整跟随者转换，同时队长侧零发包、队长状态零改动。
func TestNonLeaderSelectorSelfPrepareSwitchesBehavior(t *testing.T) {
	t.Run("off keeps the legacy zero-packet behavior", func(t *testing.T) {
		t.Setenv(partySelectorMemberSelfPrepareEnv, "off")
		_, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
		storeRuntimePartyState(leader, townCoPresencePartyState())
		storeRuntimePartyState(member, townCoPresencePartyState())
		leaderConn.write.Reset()
		memberConn.write.Reset()

		if err := service.synchronizeRuntimePartyMembersEnterSelect(member, "test_member_self_off"); err != nil {
			t.Fatalf("non-leader selector sync: %v", err)
		}
		if memberConn.write.Len() != 0 || leaderConn.write.Len() != 0 {
			t.Fatalf("off must not write packets: member=%d leader=%d",
				memberConn.write.Len(), leaderConn.write.Len())
		}
		if member.enterSelectDungeonContextSent || member.enterSelectDungeonSent || member.enterSelectDungeonAckSent {
			t.Fatalf("off must not install the selector on the requesting member: context=%t sent=%t ack=%t",
				member.enterSelectDungeonContextSent, member.enterSelectDungeonSent, member.enterSelectDungeonAckSent)
		}
		if leader.enterSelectDungeonSent || leader.enterSelectDungeonContextSent {
			t.Fatalf("off must leave the leader untouched: sent=%t context=%t",
				leader.enterSelectDungeonSent, leader.enterSelectDungeonContextSent)
		}
	})

	t.Run("on prepares the member itself and never the leader", func(t *testing.T) {
		t.Setenv(partySelectorMemberSelfPrepareEnv, "on")
		_, service, leader, member, leaderConn, memberConn := newTownCoPresenceTestService(t)
		storeRuntimePartyState(leader, townCoPresencePartyState())
		storeRuntimePartyState(member, townCoPresencePartyState())
		if ready, reason := service.currentTownEnterSelectReady(member); !ready {
			t.Fatalf("member fixture is not enter-select ready: %s", reason)
		}
		leaderConn.write.Reset()
		memberConn.write.Reset()

		if err := service.synchronizeRuntimePartyMembersEnterSelect(member, "test_member_self_on"); err != nil {
			t.Fatalf("non-leader selector sync: %v", err)
		}
		if memberConn.write.Len() == 0 {
			t.Fatal("on must give the requesting member the full follower selector transition")
		}
		if !member.enterSelectDungeonContextSent || !member.enterSelectDungeonSent || !member.enterSelectDungeonAckSent {
			t.Fatalf("member selector state incomplete: context=%t sent=%t ack=%t",
				member.enterSelectDungeonContextSent, member.enterSelectDungeonSent, member.enterSelectDungeonAckSent)
		}
		if leaderConn.write.Len() != 0 {
			t.Fatalf("the leader must not receive any packet through this path: %d bytes", leaderConn.write.Len())
		}
		if leader.enterSelectDungeonSent || leader.enterSelectDungeonContextSent {
			t.Fatalf("the leader must not be dragged into the follower selector state: sent=%t context=%t",
				leader.enterSelectDungeonSent, leader.enterSelectDungeonContextSent)
		}
	})
}

// 开关取值口径：只有显式 on/1/true/yes 才打开。
func TestPartySelectorMemberSelfPrepareSwitchValues(t *testing.T) {
	for _, value := range []string{"", "off", "0", "false", "no", "relaxed"} {
		t.Setenv(partySelectorMemberSelfPrepareEnv, value)
		if partySelectorMemberSelfPrepareEnabled() {
			t.Fatalf("value %q must keep the legacy behavior", value)
		}
	}
	for _, value := range []string{"on", "ON", "1", "true", "yes"} {
		t.Setenv(partySelectorMemberSelfPrepareEnv, value)
		if !partySelectorMemberSelfPrepareEnabled() {
			t.Fatalf("value %q must enable the member self-prepare", value)
		}
	}
}
