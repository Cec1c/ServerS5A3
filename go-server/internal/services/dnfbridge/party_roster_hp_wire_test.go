package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 本文件回答操作者文档里的两条组队缺陷，且**都在真实报文上**取证：
//
//  1. 「组队UI血量显示问题」——现场（`hud_b.png`）双开实测：**队员侧队伍框里自己那一格
//     红/蓝条是空的**，队长那一格正常。
//  2. 「邀请组队 被邀请者会成为队长」——父代理的实测已经证明 op10/op11 这条真实邀请链
//     建出来的队伍是 `leader_char_id=1 slot_zero_char_id=1`（邀请人=队长）。本文件补上
//     **"邀请到达时被邀请方自己已经在点/已经有一份本地队伍"** 这条分支的回归：被邀请方
//     先发 op12（队伍窗口本地建队）再接受邀请，权威队长仍必须是邀请人。
//
// 关于症状 1 的结论（写在这里，避免下一轮重新猜）：
//   - 服务端 op153（class0/0x0099，队员血条/槽位实时表）的行格式是
//     `u16 uid + u8 hp_percent + u8 helper + u8 slot`（当前 EXE `sub_1D800C0`；
//     同族 A21 `PartyRealtimeInfoBuilder` 逐字节一致）。**本包对该包的每一条出口**都会
//     把 0 归一化成 100（`party.memberPercentByte` / `appendRuntimePartyMember` /
//     `party.normalizeRuntimePartyMember`），所以服务端**不可能**发出 hp=0 的行；
//     现场 2026-09-24 22:53 的实抓也印证了这点：
//     op153 hex = `02 | 0300 64 00 00 | 0500 64 00 01`（两行都 hp=0x64=100）。
//     真正的现场证据（父代理 2026-09-26）也是"队长那一格正常、队员自己那一格空"。
//   - 两个收包者拿到的是**逐字节相同**的名单，唯一差别只在"谁是收包者"。
//     因此"服务端给队员那一行漏了/写零了 HP"这个假设被本文件的用例直接否证。
//   - 剩下的成因在客户端侧的自有成员对象绑定：当前客户端的队伍 HUD 刷新
//     （`sub_27310B0(hudContext, 1)`）与 `memberActor+0x498`（队伍记录指针）修复，
//     在兼容 DLL 里是**只在"class1/op12 SET_PARTY_INFO 成功 ACK"之后那一条 class0/op9**
//     上armed 的（见 client-patch/90CN.cpp 的 Proxy 派发 + g_partyHudRefreshPending）；
//     被邀请方从不发 op12（它只发 op11 接受邀请），所以它自己那一格没有队伍记录可读，
//     红/蓝条就是空的；而它看到的**队友那一格**由原生 op9 成员行建出来，所以正常。
//   => 这条**不是服务端组包能修的**：本文件把服务端该发的每一字节钉死，供客户端侧
//      修复（在被邀请方的 class1/op11 接受 ACK 之后也 arm 同一次 HUD 修复）之后回归。

// realtimeInfoRow 是 op153（class0/0x0099）的一行：
// `u16 uid + u8 hp_percent + u8 helper + u8 slot`。
type realtimeInfoRow struct {
	uid    uint16
	hp     byte
	helper byte
	slot   byte
}

// parseRealtimeInfoRows 解析 op153 body（`u8 count + count*5B`），行数不符即失败。
func parseRealtimeInfoRows(t *testing.T, body []byte) []realtimeInfoRow {
	t.Helper()
	if len(body) == 0 {
		t.Fatalf("op153 body is empty")
	}
	count := int(body[0])
	if len(body) != 1+count*5 {
		t.Fatalf("op153 body=% X want 1+%d*5 bytes", body, count)
	}
	rows := make([]realtimeInfoRow, 0, count)
	for index := 0; index < count; index++ {
		offset := 1 + index*5
		rows = append(rows, realtimeInfoRow{
			uid:    binary.LittleEndian.Uint16(body[offset : offset+2]),
			hp:     body[offset+2],
			helper: body[offset+3],
			slot:   body[offset+4],
		})
	}
	return rows
}

// newInviteAcceptSessions 造一对同频道、同城镇区域、已绑定角色的会话，
// 并把 op10/op11 需要的区域在线表准备好（`handleOnlineRequestPeer` 的 mode0 会查它）。
func newInviteAcceptSessions(t *testing.T) (*Service, *gameSession, *gameSession) {
	t.Helper()
	const channelID = byte(10)
	service, inviter, invitee, _ := newPartyFrameSessions(t, channelID, channelID)
	service.onlinePlayers = newOnlinePlayerManager()
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: inviter.selectedCharacterID,
		TownID:      38,
		AreaID:      0,
		ChannelID:   int(channelID),
		Session:     inviter,
	})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: invitee.selectedCharacterID,
		TownID:      38,
		AreaID:      0,
		ChannelID:   int(channelID),
		Session:     invitee,
	})
	return service, inviter, invitee
}

// requestPeerPartyInviteBody 是 class1/op10 mode0 邀请：u16 target + u8 mode + u32 v0 + u16 v1 + u32 v2。
func requestPeerPartyInviteBody(targetID uint16) []byte {
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body[0:2], targetID)
	body[2] = 0 // ENUM_PEER_REQUEST_TYPE PARTY
	return body
}

// op12CreatePartyBody 是现场/夹具里已验证的 op12 SET_PARTY_INFO 请求体。
func op12CreatePartyBody() []byte {
	return []byte{0x00, 0x03, 0x04, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x9c, 0x00}
}

// inviteThenAccept 走**真实两条命令**：邀请方 op10 mode0 邀请，被邀请方 op11 mode0 接受。
// 返回前把两边的出站缓冲清空，因此调用方拿到的字节就是"接受之后的服务端扇出"。
func inviteThenAccept(t *testing.T, service *Service, inviter, invitee *gameSession) {
	t.Helper()
	handled, err := service.handleAlignedGameCommand(
		inviter,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketRequestPeer),
		requestPeerPartyInviteBody(invitee.selectedCharacterID),
	)
	if err != nil || !handled {
		t.Fatalf("inviter op10 handled=%t err=%v", handled, err)
	}
	inviter.conn.(*bufferConn).write.Reset()
	invitee.conn.(*bufferConn).write.Reset()

	handled, err = service.handleAlignedGameCommand(
		invitee,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketResponsePeer),
		buildResponsePeerBody(inviter.selectedCharacterID, 0, 0),
	)
	if err != nil || !handled {
		t.Fatalf("invitee op11 handled=%t err=%v", handled, err)
	}
}

// assertRealtimeRosterCarriesFullMemberHP 检查一个收包者出站里**每一条队伍名册** op153：
// 两行、行 0 = 邀请人(uid=1001, hp=100, slot=0)、行 1 = 被邀请人(uid=1002, hp=100, slot=1)，
// 且没有任何一行 hp==0（hp==0 就是"血条空"）。
//
// `count=0` 的 op153（`BuildEmptyRealtimeInfo`，body=`00`）**不算名册**：它是"加入之前先清掉
// 被邀请方那份过期本地队伍投影"的既有原语（party_online.go 的 stale-projection clear，
// 只在被邀请方自己已有非零队伍号时出现），本身不带任何成员行。这里跳过它，
// 但对**每一条真正的名册**要求两行都在、都满血。
//
// 返回实际检查过的名册帧数，调用方断言 >0，避免"没有包也算过"。
func assertRealtimeRosterCarriesFullMemberHP(t *testing.T, label string, raw []byte) int {
	t.Helper()
	frames := splitUpperFrames(t, raw)
	checked := 0
	for _, frame := range frames {
		if frame.msgID != 0x0099 || len(frame.body) == 0 || frame.body[0] == 0 {
			continue
		}
		rows := parseRealtimeInfoRows(t, frame.body)
		if len(rows) != 2 {
			t.Fatalf("%s op153 rows=%d want 2 (party of two) body=% X", label, len(rows), frame.body)
		}
		want := []realtimeInfoRow{
			{uid: 1001, hp: 100, helper: 0, slot: 0},
			{uid: 1002, hp: 100, helper: 0, slot: 1},
		}
		for index, row := range rows {
			if row.uid != want[index].uid || row.slot != want[index].slot {
				t.Fatalf("%s op153 row[%d]=%+v want uid=%d slot=%d body=% X",
					label, index, row, want[index].uid, want[index].slot, frame.body)
			}
			// 队员自己那一行**必须**在名单里且带满血：这一条就是操作者报的
			// "队员侧自己那一格血条是空的"在服务端侧的判据。
			if row.hp == 0 {
				t.Fatalf("%s op153 row[%d] uid=%d carries hp=0 (empty HP bar) body=% X",
					label, index, row.uid, frame.body)
			}
			if row.hp != want[index].hp {
				t.Fatalf("%s op153 row[%d] uid=%d hp=%d want %d body=% X",
					label, index, row.uid, row.hp, want[index].hp, frame.body)
			}
			if row.helper != 0 {
				t.Fatalf("%s op153 row[%d] helper=%d want 0 body=% X", label, index, row.helper, frame.body)
			}
		}
		checked++
	}
	return checked
}

// assertOp9RosterHasLeaderInSlotZero 检查一个收包者出站里每一条 op9 kind0 队伍帧：
// 成员表两行（1001@slot0、1002@slot1），且客户端读端语义字段（尾部第二个字节 =
// 画队长标记的那一行）必须指向 0 号位——否则队长标记会画到被邀请人那一行。
func assertOp9RosterHasLeaderInSlotZero(t *testing.T, label string, raw []byte, hasIdentity bool) int {
	t.Helper()
	frames := splitUpperFrames(t, raw)
	checked := 0
	for _, frame := range frames {
		if frame.msgID != 0x0009 || len(frame.body) < 10 ||
			frame.body[9] != currentSceneOp9ActorDisplayKind {
			continue
		}
		rows, leaderSlot := parseOp9PartyFrame(t, frame.body, hasIdentity)
		if len(rows) != 2 || rows[0].uid != 1001 || rows[1].uid != 1002 {
			t.Fatalf("%s op9 rows=%+v want inviter 1001 in slot 0 body=% X", label, rows, frame.body)
		}
		if rows[0].slot != 0 || rows[1].slot != 1 {
			t.Fatalf("%s op9 slots=%+v want 0/1 body=% X", label, rows, frame.body)
		}
		if leaderSlot != 0 || rows[leaderSlot].uid != 1001 {
			t.Fatalf("%s op9 leader slot=%d (uid=%d) want 0 = inviter 1001 body=% X",
				label, leaderSlot, rows[leaderSlot].uid, frame.body)
		}
		checked++
	}
	return checked
}

// TestInviteAcceptRealtimeRosterCarriesFullMemberHP 是操作者「组队UI血量显示问题」的
// 服务端侧判据：走真实 op10→op11 邀请/接受，然后逐个字节检查**双方**收到的每一条
// op153 —— 队员自己那一行必须在名单里、slot 必须是 1、hp 必须是 100（满血），
// 且不允许出现 hp==0 的行。
//
// 现场背景（父代理 2026-09-26 双开）：`hud_b.png` 里队员侧队伍框"自己那一格红/蓝条无填充"，
// 队长那一格正常；当时最容易的假设是"服务端给队员那一行写了 0 或者漏了那一行"。
// 本用例把这个假设钉死为否：服务端两边发的是同一份、两行、都满血的名单。
func TestInviteAcceptRealtimeRosterCarriesFullMemberHP(t *testing.T) {
	service, inviter, invitee := newInviteAcceptSessions(t)
	inviteThenAccept(t, service, inviter, invitee)

	manager := service.runtimePartyManagerForService()
	snapshot, found := manager.SnapshotByUser(1001, boundGeneration(t, service, inviter))
	if !found || snapshot.Leader != 1001 || len(snapshot.Members) != 2 {
		t.Fatalf("party snapshot=%+v found=%t want leader 1001 with two members", snapshot, found)
	}

	inviterFrames := assertRealtimeRosterCarriesFullMemberHP(t, "inviter", inviter.conn.(*bufferConn).write.Bytes())
	inviteeFrames := assertRealtimeRosterCarriesFullMemberHP(t, "invitee", invitee.conn.(*bufferConn).write.Bytes())
	if inviterFrames == 0 || inviteeFrames == 0 {
		t.Fatalf("op153 roster frames inviter=%d invitee=%d want at least one for each",
			inviterFrames, inviteeFrames)
	}

	// 城镇上下文：记录 owner == 客户端频道 ⇒ 成员行是 6 字节短行（无内联身份块）。
	inviterOp9 := assertOp9RosterHasLeaderInSlotZero(t, "inviter", inviter.conn.(*bufferConn).write.Bytes(), false)
	inviteeOp9 := assertOp9RosterHasLeaderInSlotZero(t, "invitee", invitee.conn.(*bufferConn).write.Bytes(), false)
	if inviterOp9 == 0 || inviteeOp9 == 0 {
		t.Fatalf("op9 roster frames inviter=%d invitee=%d want at least one for each", inviterOp9, inviteeOp9)
	}
}

// TestInviteeOwnLocalPartyCannotStealLeadershipOnAccept 覆盖父代理点名的"另一条路径"：
// **被邀请方自己在点**。被邀请方先发 op12（队伍窗口本地建队 ⇒ 它自己是一份单人队伍的队长），
// 紧接着收到邀请并接受（op11）。权威队伍必须建在**邀请人**名下：邀请人仍是队长、
// 仍占 0 号位，被邀请方是 1 号位的普通成员。
//
// 为什么值得单独钉：`party/handler.go handleSetPartyInfo` 会把会话态写成
// `UserID = 自己 / IsLeader = true`，而 `bootstrapManagedRuntimePartyForSession` 又是
// 拿 `state.UserID` 当队长去 EnsureLeader 的 —— 两者叠加正是"被邀请者成为队长"的经典形态。
// bridge 的 `ensureManagedRuntimePartyForSession` / `createOrJoinManagedRuntimeParty`
// 必须把这个本地投影压回权威值（本用例就是这条不变量的守卫）。
//
// 与 `TestManagedPartyInviteTargetKeepsLeadershipOverAcceptorTransientSetPartyInfo`
// （party_manager_runtime_test.go）的关系：那条直接调内部函数、只看状态；
// 本用例**走真实 op10/op11 命令**，并把两个收包者出站里的 op9/op153 名单一起钉住。
func TestInviteeOwnLocalPartyCannotStealLeadershipOnAccept(t *testing.T) {
	service, inviter, invitee := newInviteAcceptSessions(t)

	// ① 被邀请方先 op12：本地队伍窗口建队，服务端会话态因此短暂自称队长。
	handled, err := service.handleAlignedGameCommand(
		invitee,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketSetPartyInfo),
		op12CreatePartyBody(),
	)
	if err != nil || !handled {
		t.Fatalf("invitee op12 handled=%t err=%v", handled, err)
	}
	manager := service.runtimePartyManagerForService()
	inviteeIdentity, _ := service.boundGameSessionCharacterSnapshot(invitee)
	localParty, localFound := manager.SnapshotByUser(1002, inviteeIdentity.generation)
	if !localFound || localParty.Leader != 1002 {
		t.Fatalf("invitee local op12 party=%+v found=%t want a local lobby it leads (this is the risky precondition)",
			localParty, localFound)
	}
	localPartyID := localParty.ID

	// ② 真实邀请 + 接受。
	inviteThenAccept(t, service, inviter, invitee)

	// ③ 权威归属：邀请人是队长，两人的当前队伍是**新的一份**（不是被邀请方那份本地队）。
	inviterIdentity, _ := service.boundGameSessionCharacterSnapshot(inviter)
	joinedInviter, inviterFound := manager.SnapshotByUser(1001, inviterIdentity.generation)
	joinedInvitee, inviteeFound := manager.SnapshotByUser(1002, inviteeIdentity.generation)
	if !inviterFound || !inviteeFound {
		t.Fatalf("after accept: inviterFound=%t inviteeFound=%t", inviterFound, inviteeFound)
	}
	if joinedInviter.ID != joinedInvitee.ID {
		t.Fatalf("members are in different parties inviter=%d invitee=%d", joinedInviter.ID, joinedInvitee.ID)
	}
	if joinedInviter.Leader != 1001 {
		t.Fatalf("authoritative leader=%d want the INVITER 1001 (invitee must never become leader)",
			joinedInviter.Leader)
	}
	if joinedInviter.ID == localPartyID {
		t.Fatalf("party id=%d is still the invitee's own local lobby: the inviter joined the invitee instead of the reverse",
			localPartyID)
	}

	// ④ 被邀请方自己的会话态：归属 1001、自己不是队长。
	inviteeState := runtimePartyStateSnapshot(invitee)
	if inviteeState.UserID != 1001 || inviteeState.IsLeader {
		t.Fatalf("invitee session state=%+v want party owner 1001 and IsLeader=false", inviteeState)
	}
	// ⑤ 两份名单都必须把邀请人放在 0 号位、队长标记指向 0 号位。
	if got := assertOp9RosterHasLeaderInSlotZero(t, "inviter", inviter.conn.(*bufferConn).write.Bytes(), false); got == 0 {
		t.Fatal("inviter received no op9 roster after the join")
	}
	if got := assertOp9RosterHasLeaderInSlotZero(t, "invitee", invitee.conn.(*bufferConn).write.Bytes(), false); got == 0 {
		t.Fatal("invitee received no op9 roster after the join")
	}
	if got := assertRealtimeRosterCarriesFullMemberHP(t, "invitee", invitee.conn.(*bufferConn).write.Bytes()); got == 0 {
		t.Fatal("invitee received no op153 roster after the join")
	}
}

// boundGeneration 取一个会话当前权威角色的会话代际（测试里用来查 manager 快照）。
func boundGeneration(t *testing.T, service *Service, session *gameSession) uint64 {
	t.Helper()
	identity, ok := service.boundGameSessionCharacterSnapshot(session)
	if !ok {
		t.Fatalf("session char=%d is not bound", session.selectedCharacterID)
	}
	return identity.generation
}
