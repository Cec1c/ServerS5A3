package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

func buildResponsePeerBody(targetID uint16, mode byte, value uint32) []byte {
	body := make([]byte, 7)
	binary.LittleEndian.PutUint16(body[:2], targetID)
	body[2] = mode
	binary.LittleEndian.PutUint32(body[3:7], value)
	return body
}

// 操作者报的"邀请"这条链路：A 邀请 B、B 点接受（class1/op11，mode 0，value 0）。
// 这条 online handler 此前没有直接用例，这里钉住归属：B 加入 **A 的队伍**、A 仍是队长、
// B 的客户端状态里队长在槽位 0，双方都收到新快照。
func TestHandleOnlineResponsePeerJoinsInviteeIntoInviterParty(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	channel := channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038}
	inviterConn, inviteeConn := &bufferConn{}, &bufferConn{}
	inviter := &gameSession{conn: inviterConn, channel: channel, selectedCharacterID: 1001}
	invitee := &gameSession{conn: inviteeConn, channel: channel, selectedCharacterID: 1002}
	service.bindGameSessionCharacter(inviter, 1001)
	service.bindGameSessionCharacter(invitee, 1002)

	inviterIdentity, inviterBound := service.boundGameSessionCharacterSnapshot(inviter)
	inviteeIdentity, inviteeBound := service.boundGameSessionCharacterSnapshot(invitee)
	if !inviterBound || !inviteeBound {
		t.Fatal("both sessions must be bound before the invite flow")
	}
	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: inviterIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("create inviter lobby: %+v", created)
	}
	if !manager.RecordInvite(inviteeIdentity.character, inviteeIdentity.generation,
		inviterIdentity.character, inviterIdentity.generation, created.Party.ID, 0) {
		t.Fatal("record invite: the inviter/invitee generations were rejected")
	}

	inviterConn.write.Reset()
	inviteeConn.write.Reset()
	if err := service.handleOnlineResponsePeer(invitee, uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(1001, 0, 0)); err != nil {
		t.Fatalf("response peer: %v", err)
	}

	snapshot, found := manager.SnapshotByUser(1001, inviterIdentity.generation)
	if !found || len(snapshot.Members) != 2 || snapshot.Leader != 1001 {
		t.Fatalf("invitee must join the inviter's party with the inviter as leader: found=%t %+v", found, snapshot)
	}
	inviteeState := runtimePartyStateSnapshot(invitee)
	// 客户端态里的 UserID 是**队伍归属者（队长）**，不是"我自己"——`StateFor` 就是
	// 这么写的（state.UserID = s.Leader，IsLeader = leader == userID）。所以这里
	// 期望的是：归属 1001、自己不是队长。
	if inviteeState.UserID != 1001 || inviteeState.IsLeader {
		t.Fatalf("invitee client state = %+v, want party owner 1001 and IsLeader=false", inviteeState)
	}
	if inviteeState.PartyID == 0 {
		t.Fatalf("invitee client state kept no party id: %+v", inviteeState)
	}
	members := runtimePartyMembers(inviteeState)
	if len(members) != 2 || members[0].UserID != 1001 || members[1].UserID != 1002 {
		t.Fatalf("slot order after accept = %+v, want inviter in slot 0", members)
	}
	if inviteeConn.write.Len() == 0 || inviterConn.write.Len() == 0 {
		t.Fatalf("both sessions must receive frames: inviter=%d invitee=%d",
			inviterConn.write.Len(), inviteeConn.write.Len())
	}
	// 加入之后必须**无条件**再给被邀请方画一次权威名单（op153+op9）：
	// 现场（2026-09-24 21:47）被邀请方界面上自己仍是队长，而服务端判定是对的——它那份
	// "我是队长"来自本地 op12 投影，服务端无从感知，所以"检测到旧投影才清理"不能作为
	// 唯一保障。观察点：被邀请方出站里 op153（0x0099）至少出现两次（扇出一次 + 补发一次）。
	if got := countUpperFramesByMsgID(inviteeConn.write.Bytes(), 0x0099); got < 2 {
		t.Fatalf("invitee op153 roster frames=%d, want join fan-out plus authoritative re-send", got)
	}
}

// countUpperFramesByMsgID 统计出站字节里某个 msg id 的头前缀出现次数。
// server16 头的前三字节是 `00 <msg u16 LE>`（op153 = 00 99 00）；这里只做
// "同一条名单是否被下发了两次"的行为观测，不解析帧边界。
func countUpperFramesByMsgID(data []byte, msgID uint16) int {
	lo := byte(msgID & 0xff)
	hi := byte(msgID >> 8)
	count := 0
	for index := 0; index+2 < len(data); index++ {
		if data[index] == 0x00 && data[index+1] == lo && data[index+2] == hi {
			count++
		}
	}
	return count
}

// 没有待处理邀请时的 op11 必须完全静默：既不能回 ACK，更不能把人拉进任何队伍
// （否则任何一次"接受"都可能凭空组队）。
func TestHandleOnlineResponsePeerWithoutPendingInviteStaysSilent(t *testing.T) {
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	channel := channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038}
	inviterConn, inviteeConn := &bufferConn{}, &bufferConn{}
	inviter := &gameSession{conn: inviterConn, channel: channel, selectedCharacterID: 1001}
	invitee := &gameSession{conn: inviteeConn, channel: channel, selectedCharacterID: 1002}
	service.bindGameSessionCharacter(inviter, 1001)
	service.bindGameSessionCharacter(invitee, 1002)

	inviterIdentity, _ := service.boundGameSessionCharacterSnapshot(inviter)
	manager := service.runtimePartyManagerForService()
	if created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: inviterIdentity.generation,
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1}); !created.OK {
		t.Fatalf("create inviter lobby: %+v", created)
	}
	inviterConn.write.Reset()
	inviteeConn.write.Reset()

	if err := service.handleOnlineResponsePeer(invitee, uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(1001, 0, 0)); err != nil {
		t.Fatalf("response peer without invite: %v", err)
	}
	if inviteeConn.write.Len() != 0 || inviterConn.write.Len() != 0 {
		t.Fatalf("invite-less accept wrote packets: inviter=%d invitee=%d",
			inviterConn.write.Len(), inviteeConn.write.Len())
	}
	if snapshot, found := manager.SnapshotByUser(1001, inviterIdentity.generation); !found || len(snapshot.Members) != 1 {
		t.Fatalf("invite-less accept mutated the lobby: found=%t %+v", found, snapshot)
	}
}
