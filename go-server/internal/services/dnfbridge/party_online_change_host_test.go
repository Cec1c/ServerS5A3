package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 操作者报的"队长无法移交"走的就是这条路径：op121 由队长发出，body 一个字节是队伍框槽位。
// 这里直接驱动 online handler（此前没有任何直接用例），钉住三件事：
//   - 不是队长的人发这条请求 → 一个包都不发、队伍不动；
//   - 队长发出 → 新队长成为权威队长，但保留自己原来的固定槽位；
//   - 两个会话都收到新快照。
func TestHandleOnlineChangePartyHostPreservesFixedSlots(t *testing.T) {
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
	if manager == nil {
		t.Fatal("runtime party manager is unavailable")
	}
	settings := alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1}
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: leaderIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
	}, settings)
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
	initial := service.publishRuntimePartySnapshot(joined.Party)
	if err := service.sendManagedRuntimePartySnapshots(initial); err != nil {
		t.Fatalf("initial party snapshots: %v", err)
	}
	leaderConn.write.Reset()
	memberConn.write.Reset()

	// 非队长发起：队伍必须保持原样，并且**要回一个失败包**——线上实测里被邀请方
	// 误以为自己能委任队长时会发这条请求，服务端此前静默丢弃，玩家看到的就是
	// "委任队长功能没有反应"。
	if err := service.handleOnlineChangePartyHost(member, []byte{0}); err != nil {
		t.Fatalf("non-leader host change: %v", err)
	}
	if memberConn.write.Len() == 0 {
		t.Fatal("non-leader host change must answer a failure ack, not stay silent")
	}
	failure, failureRest := splitGameServerUpperPacket(t, memberConn.write.Bytes())
	if failure.Header.MsgID != uint16(dnfenum.CmdPacketChangeHost) ||
		failure.Header.Classification != dnfproto.DefaultChannelClassification ||
		string(failure.Body) != string([]byte{0, partyCommandFailureGeneric}) {
		t.Fatalf("non-leader failure ack = class %d msg %d body % X",
			failure.Header.Classification, failure.Header.MsgID, failure.Body)
	}
	if len(failureRest) != 0 {
		t.Fatalf("non-leader failure ack left %d trailing bytes", len(failureRest))
	}
	if leaderConn.write.Len() != 0 {
		t.Fatalf("non-leader host change still wrote to the leader: %d bytes", leaderConn.write.Len())
	}
	if snapshot, found := manager.SnapshotByUser(1001, leaderIdentity.generation); !found || snapshot.Leader != 1001 {
		t.Fatalf("non-leader request changed the manager party: found=%t snapshot=%+v", found, snapshot)
	}
	memberConn.write.Reset()

	// 队长把队长交给槽位 1 上的成员。
	if err := service.handleOnlineChangePartyHost(leader, []byte{1}); err != nil {
		t.Fatalf("change party host: %v", err)
	}

	if snapshot, found := manager.SnapshotByUser(1002, memberIdentity.generation); !found || snapshot.Leader != 1002 {
		t.Fatalf("manager did not move leadership: found=%t snapshot=%+v", found, snapshot)
	}
	if oldSnapshot, found := manager.SnapshotByUser(1001, leaderIdentity.generation); found && oldSnapshot.Leader == 1001 {
		t.Fatalf("former leader still owns a party with itself as leader: %+v", oldSnapshot)
	}

	memberState := runtimePartyStateSnapshot(member)
	if memberState.UserID != 1002 || !memberState.IsLeader {
		t.Fatalf("new leader client state = %+v, want char 1002 as leader", memberState)
	}
	members := runtimePartyMembers(memberState)
	if len(members) != 2 || members[0].UserID != 1001 || members[0].Slot != 0 ||
		members[1].UserID != 1002 || members[1].Slot != 1 {
		t.Fatalf("slot order after transfer = %+v, want former leader at slot 0 and new leader at slot 1", members)
	}
	if leaderRow, ok := runtimePartyMemberBySlot(memberState, 1); !ok || leaderRow.UserID != 1002 {
		t.Fatalf("leader row by fixed slot = %+v ok=%t, want char 1002 at slot 1", leaderRow, ok)
	}
	if leaderConn.write.Len() == 0 || memberConn.write.Len() == 0 {
		t.Fatalf("both sessions must receive the fresh party snapshot: leader=%d member=%d",
			leaderConn.write.Len(), memberConn.write.Len())
	}
}

// 客户端有时按"队伍框第几个"发 1 基序号：两名成员（槽 0/1）时它可能发 2。
// 这时槽位精确匹配会落空，必须走"按槽位顺序的下标"回退，否则玩家看到的就是"移交没反应"。
func TestHandleOnlineChangePartyHostAcceptsOneBasedSlotIndex(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	channel := channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038}
	leader := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1001}
	member := &gameSession{conn: &bufferConn{}, channel: channel, selectedCharacterID: 1002}
	service.bindGameSessionCharacter(leader, 1001)
	service.bindGameSessionCharacter(member, 1002)

	leaderIdentity, _ := service.boundGameSessionCharacterSnapshot(leader)
	memberIdentity, _ := service.boundGameSessionCharacterSnapshot(member)
	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: leaderIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{
		UserID:            1002,
		SessionGeneration: memberIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1002, UserState: 1},
	})
	if !joined.OK || len(joined.Party.Members) != 2 {
		t.Fatalf("join runtime party: %+v", joined)
	}
	slots := runtimePartySlotList(joined.Party)
	t.Logf("member slots before transfer: %s", slots)

	// 2 = "第 2 个成员"（1 基）；槽位精确匹配不到（成员在槽 0/1），必须靠顺序回退命中。
	if err := service.handleOnlineChangePartyHost(leader, []byte{2}); err != nil {
		t.Fatalf("change party host with one-based index: %v", err)
	}
	if snapshot, found := manager.SnapshotByUser(1002, memberIdentity.generation); !found || snapshot.Leader != 1002 {
		t.Fatalf("one-based slot index was not resolved to the second member: found=%t snapshot=%+v", found, snapshot)
	}
	members := runtimePartyMembers(runtimePartyStateSnapshot(member))
	if len(members) != 2 || members[0].UserID != 1001 || members[1].UserID != 1002 || members[1].Slot != 1 {
		t.Fatalf("slot order after one-based transfer = %+v, want the new leader at its fixed slot 1", members)
	}
}
