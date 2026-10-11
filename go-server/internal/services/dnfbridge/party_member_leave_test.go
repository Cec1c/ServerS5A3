package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

// 现场缺陷（操作者原话）：**「p2 点击的是退出队伍，但是仍然在队伍里面」**。
// 下面这组用例钉住"队员 op13 离队"的端到端效果：
//   - 管理器里不再有这个角色；
//   - **队长的客户端收到移除记录**（改动前这条在代际漂移场景里一个包都没有）；
//   - 离队者自己的客户端拿到 op13 ACK + 自己的队伍记录被清掉。
//
// 双开会话（char 1001 = 队长 / char 1002 = 队员）与既有 party_online_*_test.go 同构：
// 直接用 RuntimePartyManager 建队并 publish，然后驱动 online handler。

type partyLeaveHarness struct {
	service    *Service
	leader     *gameSession
	member     *gameSession
	leaderConn *bufferConn
	memberConn *bufferConn
	partyID    uint16
}

func newPartyLeaveHarness(t *testing.T) *partyLeaveHarness {
	t.Helper()
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	channel := channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038}
	leaderConn, memberConn := &bufferConn{}, &bufferConn{}
	leader := &gameSession{conn: leaderConn, channel: channel, selectedCharacterID: 1001}
	member := &gameSession{conn: memberConn, channel: channel, selectedCharacterID: 1002}
	service.bindGameSessionCharacter(leader, 1001)
	service.bindGameSessionCharacter(member, 1002)

	leaderIdentity, ok := service.boundGameSessionCharacterSnapshot(leader)
	if !ok {
		t.Fatal("leader session is not bound")
	}
	memberIdentity, ok := service.boundGameSessionCharacterSnapshot(member)
	if !ok {
		t.Fatal("member session is not bound")
	}
	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: leaderIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create runtime party: %+v", created)
	}
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{
		UserID:            1002,
		SessionGeneration: memberIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1002, UserState: 1, HPPercent: 100, MPPercent: 100},
	})
	if !joined.OK || len(joined.Party.Members) != 2 || joined.Party.Leader != 1001 {
		t.Fatalf("join runtime party: ok=%t leader=%d members=%d", joined.OK, joined.Party.Leader, len(joined.Party.Members))
	}
	state := service.publishRuntimePartySnapshot(joined.Party)
	if err := service.sendManagedRuntimePartySnapshots(state); err != nil {
		t.Fatalf("initial party snapshots: %v", err)
	}
	leaderConn.write.Reset()
	memberConn.write.Reset()
	return &partyLeaveHarness{
		service:    service,
		leader:     leader,
		member:     member,
		leaderConn: leaderConn,
		memberConn: memberConn,
		partyID:    joined.Party.ID,
	}
}

// dispatchMemberLeave 走真实入口（handleAlignedGameCommand → handleOnlinePartyCommand），
// 而不是直接调 handler，这样"离队请求被上游静默吞掉"这类回归也会被这条用例抓住。
func (h *partyLeaveHarness) dispatchMemberLeave(t *testing.T) {
	t.Helper()
	handled, err := h.service.handleAlignedGameCommand(
		h.member,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil {
		t.Fatalf("member leave party: %v", err)
	}
	if !handled {
		t.Fatal("LeaveParty must be handled by the online party bridge")
	}
}

// TestMemberLeavePartyRemovesMemberAndNotifiesLeader 是基线用例：代际一致时
// "队员点退出队伍"必须做到双方视图一致。
func TestMemberLeavePartyRemovesMemberAndNotifiesLeader(t *testing.T) {
	h := newPartyLeaveHarness(t)
	memberIdentity, _ := h.service.boundGameSessionCharacterSnapshot(h.member)

	h.dispatchMemberLeave(t)

	if snapshot, found := h.service.runtimePartyManagerForService().SnapshotByUser(1002, memberIdentity.generation); found {
		t.Fatalf("member is still inside the runtime party: %+v", snapshot)
	}
	if h.leaderConn.write.Len() == 0 {
		t.Fatal("leader received no packet after the member left: the leader still renders the member")
	}
	leaderRest := assertRuntimePartyRemoved(t, h.leaderConn.write.Bytes(), 1002, 0)
	if len(leaderRest) != 0 {
		t.Fatalf("leader trailing bytes = %d", len(leaderRest))
	}
	ack, memberRest := splitGameServerUpperPacket(t, h.memberConn.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketLeaveParty) || !bytesEqualByte(ack.Body, 1) {
		t.Fatalf("member leave ack = msg %d body=% X", ack.Header.MsgID, ack.Body)
	}
	memberRest = assertRuntimePartyRemoved(t, memberRest, 1002, 0)
	if len(memberRest) != 0 {
		t.Fatalf("member trailing bytes = %d", len(memberRest))
	}
	if state := runtimePartyStateSnapshot(h.member); state.PartyID != 0 {
		t.Fatalf("member client projection was not cleared: %+v", state)
	}
}

// TestMemberLeavePartyRepairsManagerGenerationDrift 是本缺陷的**红转绿**用例。
//
// 场景：队员的会话代际在组队之后变了（频道重连 / 重选角 / 同角色旧新两个 socket），
// 而管理器里记的还是旧的代际。此时改动前的代码：
//
//	leaveManagedRuntimeParty → manager.Leave(1002, 新代际) → "stale_session" → removed=false
//	→ 只清 p2 自己的客户端投影 + 回 op13 ACK，**一个包都不发给队长**，管理器里 p2 还在队里。
//
// 这正是"p2 点击的是退出队伍，但是仍然在队伍里面"。
func TestMemberLeavePartyRepairsManagerGenerationDrift(t *testing.T) {
	h := newPartyLeaveHarness(t)

	// 让队员会话的代际前进一代：管理器里仍是组队时的旧代际。
	if drifted := advanceGameSessionCharacterGeneration(h.member); drifted == 0 {
		t.Fatal("advance member session generation failed")
	}
	memberIdentity, ok := h.service.boundGameSessionCharacterSnapshot(h.member)
	if !ok {
		t.Fatal("member session is no longer the authoritative session for 1002")
	}
	if _, found := h.service.runtimePartyManagerForService().SnapshotByUser(1002, memberIdentity.generation); found {
		t.Fatal("precondition failed: the manager already matches the drifted generation")
	}
	if _, found := h.service.runtimePartyManagerForService().SnapshotByUser(1002, 0); !found {
		t.Fatal("precondition failed: the manager no longer holds this member at all")
	}

	h.dispatchMemberLeave(t)

	manager := h.service.runtimePartyManagerForService()
	if snapshot, found := manager.SnapshotByUser(1002, 0); found {
		t.Fatalf("member stayed inside the runtime party after leave: %+v", snapshot)
	}
	// 队长必须被通知：这是改动前失败的那条断言。
	if h.leaderConn.write.Len() == 0 {
		t.Fatal("leader received no removal after the member left: the leader still renders the member in its party window")
	}
	leaderRest := assertRuntimePartyRemoved(t, h.leaderConn.write.Bytes(), 1002, 0)
	if len(leaderRest) != 0 {
		t.Fatalf("leader trailing bytes = %d", len(leaderRest))
	}
	// 离队者自己也要回到"无队伍"状态。
	if state := runtimePartyStateSnapshot(h.member); state.PartyID != 0 {
		t.Fatalf("member client projection was not cleared: %+v", state)
	}
	ack, memberRest := splitGameServerUpperPacket(t, h.memberConn.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketLeaveParty) || !bytesEqualByte(ack.Body, 1) {
		t.Fatalf("member leave ack = msg %d body=% X", ack.Header.MsgID, ack.Body)
	}
	memberRest = assertRuntimePartyRemoved(t, memberRest, 1002, 0)
	if len(memberRest) != 0 {
		t.Fatalf("member trailing bytes = %d", len(memberRest))
	}
}

// TestMemberLeavePartyLegacySwitchKeepsOldBehavior 钉住一键回退：
// DNFBRIDGE_PARTY_LEAVE_REPAIR=legacy 时代际漂移仍回到"只清自己、不通知队长"的旧行为。
func TestMemberLeavePartyLegacySwitchKeepsOldBehavior(t *testing.T) {
	if got := readPartyLeaveRepairMode(""); got != partyLeaveRepairOn {
		t.Fatalf("default party leave repair mode = %v, want repair", got)
	}
	if got := readPartyLeaveRepairMode(" LEGACY "); got != partyLeaveRepairLegacy {
		t.Fatalf("legacy party leave repair mode = %v, want legacy", got)
	}

	h := newPartyLeaveHarness(t)
	h.service.options.partyLeaveRepair = partyLeaveRepairLegacy
	if drifted := advanceGameSessionCharacterGeneration(h.member); drifted == 0 {
		t.Fatal("advance member session generation failed")
	}

	h.dispatchMemberLeave(t)

	if h.leaderConn.write.Len() != 0 {
		t.Fatalf("legacy mode must not notify the leader: %d bytes", h.leaderConn.write.Len())
	}
}

// TestMemberLeavePartyStaleRetiredSessionStillIgnored 是**防回归**用例：退役（被替代）
// 的旧 socket 发来的离队请求必须继续被整条消费掉，不能摘掉替代会话的成员身份。
// 这条语义是修复"按角色无条件离队"时唯一需要守住的边界。
func TestMemberLeavePartyStaleRetiredSessionStillIgnored(t *testing.T) {
	h := newPartyLeaveHarness(t)
	manager := h.service.runtimePartyManagerForService()

	// 队员换了一个新 socket（频道重连）：新 socket 成为权威会话，旧 socket 退役。
	// 管理器里的成员代际会被 RebindSession 换到新会话那一代，所以下面一律用
	// generation=0 的"这个角色是否还在某个队伍里"判据（见 SnapshotByUser 的语义）。
	replacementConn := &bufferConn{}
	replacement := &gameSession{
		conn:                replacementConn,
		channel:             h.member.channel,
		selectedCharacterID: 1002,
		channelReconnect:    true,
	}
	h.service.bindGameSessionCharacter(replacement, 1002)
	if _, found := manager.SnapshotByUser(1002, 0); !found {
		t.Fatal("precondition failed: the replacement session is not in the party")
	}

	handled, err := h.service.handleAlignedGameCommand(
		h.member,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil {
		t.Fatalf("retired session leave: %v", err)
	}
	if !handled {
		t.Fatal("a retired session's leave request must still be consumed, not fall through")
	}
	if _, found := manager.SnapshotByUser(1002, 0); !found {
		t.Fatal("a retired socket removed the replacement session's party membership")
	}

	// 而替代会话自己发离队必须生效。
	handled, err = h.service.handleAlignedGameCommand(
		replacement,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketLeaveParty),
		nil,
	)
	if err != nil || !handled {
		t.Fatalf("replacement session leave: handled=%t err=%v", handled, err)
	}
	if snapshot, found := manager.SnapshotByUser(1002, 0); found {
		t.Fatalf("replacement session's leave did not remove the member: %+v", snapshot)
	}
}

func bytesEqualByte(body []byte, want byte) bool {
	return len(body) == 1 && body[0] == want
}
