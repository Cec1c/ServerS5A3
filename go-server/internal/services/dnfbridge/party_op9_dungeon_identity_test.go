package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/party"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 本文件钉住 2026-09-24 双开"组队进图加载到 ~50% 客户端闪退"的根因与两条名单不变量。
//
// 现场（packet_log.txt 22:25 / 90CN_trace.log pid=11116）：
//   - 服务端在 op29（start map）之后发的下一包就是 class0/op9 kind0 队伍帧，
//     日志 `game-current-party-frame-op9-send ... owner_channel=0 local_channel=0
//     slot_count=2`（进副本后本地 town 上下文被置 0）；
//   - 客户端 trace 在 `town-copresence-dispatch phase=before opcode=9 ... owner=0
//     kind=0` 之后**没有 after**，紧接着 0xC0000005（同一条 op9 派发里崩）。
//
// 客户端读端（仓库既有 IDA 结论）：`sub_1D64CA0` 的 kind0 记录按"记录 owner 字节 vs
// 客户端当前频道"决定每个成员行后面有没有内联身份块
// （`u8 job / u8 level / u8 grow / u8 flag / u32 nameLen + raw name`）。owner=0 而客户端
// 频道非 0 → 走跨频道分支、要读身份块。旧代码把这个参照值传成了 owner 自己的副本，
// 于是这个分支永远不命中：客户端把我们的 6 字节行当身份块读，nameLength 读成越界值。

// upperFrame 是出站帧的极简视图（13 字节 game upper 头 + body）。
type upperFrame struct {
	msgID uint16
	body  []byte
}

// splitUpperFrames 顺序切分出站帧；帧头长度字段是 [3:7] 的 u32（含头总长），
// 与 `dnfproto.GameServerUpperHeaderSize` 一致。
func splitUpperFrames(t *testing.T, data []byte) []upperFrame {
	t.Helper()
	frames := make([]upperFrame, 0, 4)
	rest := data
	for len(rest) > 0 {
		if len(rest) < dnfproto.GameServerUpperHeaderSize {
			t.Fatalf("upper frame header truncated rest=%d", len(rest))
		}
		total := int(binary.LittleEndian.Uint32(rest[3:7]))
		if total < dnfproto.GameServerUpperHeaderSize || total > len(rest) {
			t.Fatalf("upper frame total=%d rest=%d", total, len(rest))
		}
		packet, err := dnfproto.ParseChannelPacket(rest[:total])
		if err != nil {
			t.Fatalf("parse upper frame: %v", err)
		}
		frames = append(frames, upperFrame{
			msgID: packet.Header.MsgID,
			body:  append([]byte(nil), packet.Body...),
		})
		rest = rest[total:]
	}
	return frames
}

type op9PartyRow struct {
	slot        byte
	uid         uint16
	hasIdentity bool
	job         byte
	level       byte
	grow        byte
	name        string
	state       byte
}

// parseOp9PartyFrame 按 `hasIdentity` 指定的分支解析 kind0 队伍帧的成员行。
// 分支判断属于**客户端**（记录 owner 字节 vs 客户端频道），所以由调用方给出。
// 返回值第二个是尾部 4 字节里的**队长槽位**（客户端画队长标记的那一行）。
func parseOp9PartyFrame(t *testing.T, body []byte, hasIdentity bool) ([]op9PartyRow, byte) {
	t.Helper()
	if len(body) < 30 || binary.LittleEndian.Uint16(body[0:2]) != 1 ||
		binary.LittleEndian.Uint16(body[2:4]) != currentSceneOp9StableSceneValue ||
		body[9] != currentSceneOp9ActorDisplayKind {
		t.Fatalf("op9 kind0 header body=% X", body)
	}
	nameLen := int(binary.LittleEndian.Uint32(body[12:16]))
	offset := 16 + nameLen + 13
	if offset >= len(body) {
		t.Fatalf("op9 party record offset=%d exceeds body len=%d body=% X", offset, len(body), body)
	}
	slotCount := int(body[offset])
	offset++
	rows := make([]op9PartyRow, 0, slotCount)
	for index := 0; index < slotCount; index++ {
		if offset+6 > len(body) {
			t.Fatalf("op9 party row %d truncated offset=%d body=% X", index, offset, body)
		}
		row := op9PartyRow{
			slot: body[offset],
			uid:  binary.LittleEndian.Uint16(body[offset+1 : offset+3]),
		}
		offset += 3
		if hasIdentity {
			row.hasIdentity = true
			row.job = body[offset]
			row.level = body[offset+1]
			row.grow = body[offset+2]
			offset += 4
			if offset+4 > len(body) {
				t.Fatalf("op9 party row %d identity name length truncated body=% X", index, body)
			}
			memberNameLen := int(binary.LittleEndian.Uint32(body[offset : offset+4]))
			offset += 4
			if memberNameLen < 0 || offset+memberNameLen > len(body) {
				t.Fatalf("op9 party row %d name len=%d exceeds body len=%d body=% X",
					index, memberNameLen, len(body), body)
			}
			row.name = string(body[offset : offset+memberNameLen])
			offset += memberNameLen
		}
		row.state = body[offset+1]
		offset += 3
		rows = append(rows, row)
	}
	if offset+4 != len(body) {
		t.Fatalf("op9 party post-slot tail offset=%d body len=%d body=% X", offset, len(body), body)
	}
	return rows, body[offset+1]
}

// newPartyFrameSessions 造两个同频道、已绑定角色的 session，返回服务与 party 名单状态。
// `townOwnerChannel` 传 0 即"进副本后的本地上下文"，传频道号即城镇上下文。
func newPartyFrameSessions(t *testing.T, channelID, townOwnerChannel byte) (*Service, *gameSession, *gameSession, alignedcmd.PartyState) {
	t.Helper()
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	channel := channelcatalog.Channel{ID: int(channelID), Type: 11, Name: "ch", Port: 10000}
	leader := &gameSession{
		conn:                  &bufferConn{},
		channel:               channel,
		selectedCharacterID:   1001,
		townActorOwnerChannel: townOwnerChannel,
	}
	member := &gameSession{
		conn:                  &bufferConn{},
		channel:               channel,
		selectedCharacterID:   1002,
		townActorOwnerChannel: townOwnerChannel,
	}
	service.bindGameSessionCharacter(leader, 1001)
	service.bindGameSessionCharacter(member, 1002)
	state := alignedcmd.PartyState{
		PartyID:    1,
		UserID:     1001,
		IsLeader:   false,
		MaxMembers: 4,
		UserState:  1,
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1001, UserState: 1},
			{UserID: 1002, UserState: 1},
		},
	}
	return service, leader, member, state
}

// 回归①（闪退）：进副本上下文（记录 owner=0）下，队伍帧的每个成员行**必须**带
// 内联身份块——客户端仍按登录频道（非 0）比较，一定会走跨频道分支读它。
func TestDungeonContextPartyFrameCarriesMemberIdentityBlockForClientChannel(t *testing.T) {
	const channelID = byte(10)
	service, leader, member, state := newPartyFrameSessions(t, channelID, currentSceneObjectContext)

	if sent, err := service.sendCurrentPartyFrameProjection(member, state, "test_dungeon_post_start_map"); err != nil || !sent {
		t.Fatalf("dungeon party frame sent=%t err=%v", sent, err)
	}
	conn := member.conn.(*bufferConn)
	frames := splitUpperFrames(t, conn.write.Bytes())
	if len(frames) != 1 || frames[0].msgID != 0x0009 {
		t.Fatalf("dungeon party frame count=%d frames=%+v", len(frames), frames)
	}
	body := frames[0].body
	if body[7] != currentSceneObjectContext {
		t.Fatalf("dungeon record owner byte=%02x want 00 body=% X", body[7], body)
	}
	// 参照值必须是客户端频道（10），不是 owner 的副本（0）——否则这个分支永远不命中。
	if got := currentClientSelectedChannel(member); got != channelID {
		t.Fatalf("client selected channel=%d want=%d", got, channelID)
	}
	rows, selected := parseOp9PartyFrame(t, body, true)
	if len(rows) != 2 {
		t.Fatalf("dungeon party rows=%d want 2 body=% X", len(rows), body)
	}
	for index, row := range rows {
		if !row.hasIdentity {
			t.Fatalf("dungeon party row[%d] lost the inline identity block body=% X", index, body)
		}
	}
	if rows[0].slot != 0 || rows[0].uid != 1001 || rows[1].slot != 1 || rows[1].uid != 1002 {
		t.Fatalf("dungeon party slot table=%+v want inviter 1001 in slot 0", rows)
	}
	// 客户端读端语义字段（队长标记行）必须与 0 号位一致：0 号位就是权威队长。
	if selected != 0 {
		t.Fatalf("dungeon party leader slot=%d want 0 (slot zero row = inviter) body=% X", selected, body)
	}
	if rows[selected].uid != 1001 {
		t.Fatalf("dungeon party leader slot=%d names uid=%d want inviter 1001", selected, rows[selected].uid)
	}
	// 行宽：跨频道分支 = 3（slot+uid）+ 4（job/level/grow/flag）+ 4（nameLen）+ name + 3（state）。
	outerNameLen := int(binary.LittleEndian.Uint32(body[12:16]))
	wantLen := 16 + outerNameLen + 13 + 1
	for _, row := range rows {
		wantLen += 14 + len(row.name)
	}
	wantLen += 4
	if len(body) != wantLen {
		t.Fatalf("dungeon party frame len=%d want=%d body=% X", len(body), wantLen, body)
	}
	_ = leader
}

// 回归①的反面：城镇上下文（记录 owner == 客户端频道）必须保持原来的 6 字节行，
// 不能因为这次修复改变城镇里已经正常的名单线格式。
func TestTownContextPartyFrameKeepsSameChannelSlotRows(t *testing.T) {
	const channelID = byte(10)
	service, leader, member, state := newPartyFrameSessions(t, channelID, channelID)

	if sent, err := service.sendCurrentPartyFrameProjection(leader, state, "test_town_party_frame"); err != nil || !sent {
		t.Fatalf("town party frame sent=%t err=%v", sent, err)
	}
	conn := leader.conn.(*bufferConn)
	frames := splitUpperFrames(t, conn.write.Bytes())
	if len(frames) != 1 || frames[0].msgID != 0x0009 {
		t.Fatalf("town party frame count=%d frames=%+v", len(frames), frames)
	}
	body := frames[0].body
	if body[7] != channelID {
		t.Fatalf("town record owner byte=%02x want=%02x body=% X", body[7], channelID, body)
	}
	rows, selected := parseOp9PartyFrame(t, body, false)
	if len(rows) != 2 || rows[0].uid != 1001 || rows[1].uid != 1002 {
		t.Fatalf("town party rows=%+v want inviter 1001 in slot 0", rows)
	}
	for index, row := range rows {
		if row.hasIdentity {
			t.Fatalf("town party row[%d] must stay 6 bytes body=% X", index, body)
		}
	}
	if selected != 0 || rows[selected].uid != 1001 {
		t.Fatalf("town party leader slot=%d (uid=%d) want 0 = inviter 1001", selected, rows[selected].uid)
	}
	outerNameLen := int(binary.LittleEndian.Uint32(body[12:16]))
	if want := 16 + outerNameLen + 13 + 1 + 12 + 4; len(body) != want {
		t.Fatalf("town party frame len=%d want=%d body=% X", len(body), want, body)
	}
	_ = member
}

// 回归②（"被邀请人成为队长" / "队长标记画在第 2 行"）：接受邀请之后，被邀请方收到的
// **每一份**名单都必须把邀请人放在 0 号位（队长位），被邀请方自己的记录只能是 1 号位；
// 实时表 op153 同理。**并且客户端读端语义字段（op9 尾部第二个字节 = 队长标记所在行）
// 必须与 0 号位一致**——现场 2026-09-24 22:53 双开（packet_log.20260924-225314.txt）
// 名单本身是对的，但这一字节被写成"记录自己那条 actor 的槽位"，被邀请方自己那帧于是
// 指向第 2 行，被邀请人那一行被画上队长标记。
//
// 现场 22:25 双开：邀请人=角色 2（先 op12 建队、再 op10 mode0 邀请），被邀请人=角色 3
// （op11 mode0 接受），服务端判定 leader=2、3 在槽位 1。
//
// 两种上下文都跑：城镇（owner == 客户端频道，客户端按"同频道"读 6 字节行）与
// **进副本（owner=0，客户端按"跨频道"读身份块）**。
func TestInviteeRosterFramesKeepInviterInSlotZero(t *testing.T) {
	const channelID = byte(10)
	t.Run("town_context_same_channel_rows", func(t *testing.T) {
		assertInviteeRosterKeepsInviterInSlotZero(t, channelID, channelID, false)
	})
	t.Run("dungeon_context_cross_channel_identity_rows", func(t *testing.T) {
		assertInviteeRosterKeepsInviterInSlotZero(t, channelID, currentSceneObjectContext, true)
	})
}

func assertInviteeRosterKeepsInviterInSlotZero(t *testing.T, channelID, townOwnerChannel byte, hasIdentity bool) {
	t.Helper()
	service, inviter, invitee, _ := newPartyFrameSessions(t, channelID, townOwnerChannel)

	inviterIdentity, _ := service.boundGameSessionCharacterSnapshot(inviter)
	inviteeIdentity, _ := service.boundGameSessionCharacterSnapshot(invitee)
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
		t.Fatal("record invite rejected")
	}
	invitee.conn.(*bufferConn).write.Reset()
	if err := service.handleOnlineResponsePeer(invitee, 0x000b, buildResponsePeerBody(1001, 0, 0)); err != nil {
		t.Fatalf("response peer: %v", err)
	}

	state := runtimePartyStateSnapshot(invitee)
	if state.UserID != 1001 || state.IsLeader {
		t.Fatalf("invitee client state=%+v want party owner 1001 and IsLeader=false", state)
	}
	inviteeObjectKey := currentSceneActorObjectKey(1002)
	frames := splitUpperFrames(t, invitee.conn.(*bufferConn).write.Bytes())
	partyFrames, realtimeFrames, ownRecordFrames := 0, 0, 0
	for _, frame := range frames {
		switch frame.msgID {
		case 0x0009:
			if len(frame.body) < 10 || frame.body[9] != currentSceneOp9ActorDisplayKind {
				continue
			}
			// 按**客户端读法**解析：owner 字节与客户端频道不同就走跨频道身份块分支。
			rows, selected := parseOp9PartyFrame(t, frame.body, hasIdentity)
			if len(rows) == 0 {
				continue
			}
			partyFrames++
			if rows[0].slot != 0 || rows[0].uid != 1001 {
				t.Fatalf("invitee op9 slot 0 = %+v want inviter 1001 (frames=% X)", rows[0], frame.body)
			}
			for _, row := range rows {
				if row.uid == 1002 && row.slot == 0 {
					t.Fatalf("invitee op9 promoted the invitee to slot 0: %+v body=% X", rows, frame.body)
				}
			}
			// 客户端读端语义字段（队长标记行）必须与 0 号位一致——不论这一帧描述的是
			// 邀请人自己还是被邀请方的 actor（进副本时会双向各投影一次）。
			if selected != rows[0].slot {
				t.Fatalf("invitee op9 leader slot=%d want slot 0 of row 0 (%+v) body=% X",
					selected, rows[0], frame.body)
			}
			if rows[selected].uid != 1001 {
				t.Fatalf("invitee op9 leader slot=%d names uid=%d want inviter 1001 body=% X",
					selected, rows[selected].uid, frame.body)
			}
			// 描述"被邀请人自己这个 actor"的记录也只能把队长标记指向 0 号位。
			if binary.LittleEndian.Uint16(frame.body[4:6]) == inviteeObjectKey {
				ownRecordFrames++
				if selected != 0 {
					t.Fatalf("invitee's own op9 record leader slot=%d want 0 body=% X", selected, frame.body)
				}
			}
		case 0x0099:
			if len(frame.body) < 6 {
				continue
			}
			count := int(frame.body[0])
			if count < 2 || 1+count*5 > len(frame.body) {
				continue
			}
			realtimeFrames++
			firstUID := binary.LittleEndian.Uint16(frame.body[1:3])
			firstSlot := frame.body[5]
			secondUID := binary.LittleEndian.Uint16(frame.body[6:8])
			secondSlot := frame.body[10]
			if firstUID != 1001 || firstSlot != 0 || secondUID != 1002 || secondSlot != 1 {
				t.Fatalf("invitee op153 rows=(%d,%d)(%d,%d) want (1001,0)(1002,1) body=% X",
					firstUID, firstSlot, secondUID, secondSlot, frame.body)
			}
		}
	}
	if partyFrames == 0 || realtimeFrames == 0 || ownRecordFrames == 0 {
		t.Fatalf("invitee roster frames op9=%d op153=%d own_op9=%d want at least one each",
			partyFrames, realtimeFrames, ownRecordFrames)
	}
}

// 转让队长/调整位置之后，队伍状态里的成员切片顺序不一定等于"队长优先"（权威队长由
// `state.UserID` 给出）。op153（0x0099 血条/槽位表）与 op9 名册必须用**同一份**槽位顺序：
// 客户端把两者按同一条队伍框行序解释，顺序不一致时血条会绑到错误的队伍框行上。
// 现场（2026-09-25 双开）：委任队长后血条 UI 出错，两个队员的框互相交换。
func TestRuntimePartyRealtimeInfoUsesRosterSlotOrder(t *testing.T) {
	state := alignedcmd.PartyState{
		PartyID: 7,
		UserID:  1001, // 权威队长
		Members: []alignedcmd.PartyMemberState{
			{UserID: 1002, HPPercent: 80}, // 切片顺序里队长不在第一行
			{UserID: 1001, HPPercent: 55},
		},
	}
	roster := runtimePartyMembers(state)
	if len(roster) != 2 || roster[0].UserID != 1001 {
		t.Fatalf("roster order=%+v want the authoritative leader first", roster)
	}
	body := runtimePartyRealtimeInfoBody(state)
	if len(body) != 1+2*5 || body[0] != 2 {
		t.Fatalf("op153 body=% X want count=2 and 5 bytes per member", body)
	}
	type realtimeRow struct {
		uid  uint16
		hp   byte
		slot byte
	}
	rows := make([]realtimeRow, 0, 2)
	for index := 0; index < 2; index++ {
		offset := 1 + index*5
		rows = append(rows, realtimeRow{
			uid:  binary.LittleEndian.Uint16(body[offset : offset+2]),
			hp:   body[offset+2],
			slot: body[offset+4],
		})
	}
	if rows[0].uid != 1001 || rows[0].slot != 0 || rows[1].uid != 1002 || rows[1].slot != 1 {
		t.Fatalf("op153 rows=%+v want leader 1001 at slot 0, 1002 at slot 1", rows)
	}
	if rows[0].hp != 55 || rows[1].hp != 80 {
		t.Fatalf("op153 hp rows=%+v want each member's HP on its own roster row", rows)
	}
	// 模块侧仍按切片下标写 slot：bridge 出口必须先对齐顺序，不能直接把状态丢给它。
	if raw := party.BuildSingleMemberRealtimeInfo(state); len(raw) > 5 && raw[5] == 0 {
		t.Logf("module-side slot bytes are slice indices: % X", raw)
	}
}
