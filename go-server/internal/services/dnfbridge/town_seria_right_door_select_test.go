package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// 操作者清单第 36 条「赛丽亚房间右边门应弹选择地图」的独立用例。
// 取证与判据逐条写在 town_seria_right_door_select.go 的文件头；本文件钉四件事：
//
//  1. 开关默认关闭，且只有显式的开启值才算开；
//  2. 默认档逐字节等于今天（右侧门触发时"今天发什么就还发什么"，含基线 hex）；
//  3. 开档下右侧门触发**恰好**走一次既有选择页发送路径（与 force_select 逐字节同一形状），
//     且不再发过图 op24；
//  4. 判据窄 + 触发区域字段缺失/选择页装不出来时 fail-closed（保持今天的行为）。
//
// 逐字节基线 hex（seriaRightDoorTodayBytesHex）的来源：同一份夹具、在**未加开关分支**的树上
// 跑 `handleTownSetUserArea(右侧门 walk-in 形状)` 抓到的原始回包，三条 upper 包依次为
// op22 用户位置 → op23 用户区域 → op24 场景过图。

// seriaRightDoorTestMapText 是 `map/cataclysm/town/elvengard/(r)new_seria_room.map` 的
// 真档结构切片：右侧门 `858 183 30 100 38 0`、传送阵目的地点触发 (433,311)、左门
// `49 224 34 52 101 0`，以及 import 进来的 `[is seria room warp]` 段落。
const seriaRightDoorTestMapText = "[import script]\n`Common/(R)Gate_Seria.map`\n" +
	"[town movable area]\n" +
	"858 183 30 100 38 0 433 311 0 0 39 0 433 311 0 0 40 0 49 224 34 52 101 0\n" +
	"[/town movable area]\n" +
	"[town movable area with animation]\n" +
	"[movable area]\n" +
	"[area]\n" +
	"363 327 150 40\n" +
	"[draw layer]\n" +
	"`[bottom]`\n" +
	"[is seria room warp]\n" +
	"1\n" +
	"[/movable area]\n" +
	"[/town movable area with animation]\n" +
	"[name]\n" +
	"`PVP无名`\n"

// seriaRightDoorMapWithoutRightDoorText 是同一间房去掉右侧门之后的地图文档：只剩左门与
// 传送阵的点触发（w=h=0），用来钉"触发区域字段缺失 ⇒ fail-closed"。
const seriaRightDoorMapWithoutRightDoorText = "[town movable area]\n" +
	"433 311 0 0 39 0 433 311 0 0 40 0 49 224 34 52 101 0\n" +
	"[/town movable area]\n" +
	"[name]\n" +
	"`没有右侧门`\n"

// seriaRightDoorTodayBytesHex 是开关关闭档右侧门 op36 的逐字节回包。
// r32按当前24.9原生读取器补齐op24行尾1，包长与校验和随之更新；op22/op23保持。
const seriaRightDoorTodayBytesHex = "00160019000000ee09848b00000000001d00c502e2000564000017001a000000e938d289" +
	"00000000001d002600c502e20005030018001a000000842b9fa20000260001001d00c502e200050301"

type seriaRightDoorPacket struct {
	msgID          uint16
	classification byte
	body           []byte
}

// newSeriaRightDoorTest 复用既有 town 夹具（角色 29 落 38/1 赛丽亚房间），按需装上
// "城镇场景已定型"的位置快照——它是选择页能装出来的前提（currentTownEnterSelectReady），
// 并注入房间地图文档（判据里那个 PVF 触发点的唯一来源）。
func newSeriaRightDoorTest(
	t *testing.T,
	townScenePositionBound bool,
	mapText string,
) (*Service, *gameSession) {
	t.Helper()
	service, session, _ := newTownMoveTest(t)
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"new_seria_room.map": mapText,
		"new_HendonMyre.map": mapText,
		"WestCoast.map":      mapText,
		"new_Elvengard.map":  mapText,
	})
	if townScenePositionBound {
		session.townMu.Lock()
		session.townPositionSnapshot = currentTownPositionSnapshot{
			CharacterID:   29,
			TownID:        38,
			AreaID:        1,
			PositionX:     450,
			PositionY:     234,
			MovementCode:  5,
			PositionValid: true,
		}
		session.townMu.Unlock()
	}
	return service, session
}

// buildSeriaRightDoorWalkInRequest 是当前客户端"走进赛丽亚房间右侧那扇门"的现场形状：
// 目标 (town, area) + `opaque_u16_a` = 来源城镇 + `opaque_u16_b` = 来源区域 +
// `opaque_u32` = 0 + 尾字节 6（全库 packet_log 实测，见 town_seria_right_door_select.go）。
func buildSeriaRightDoorWalkInRequest(
	townID, areaID byte,
	x, y int16,
	direction byte,
	sourceTown, sourceArea uint16,
	opaqueU32 uint32,
	tail byte,
) []byte {
	body := buildTownMoveRequest(townID, areaID, x, y, direction)
	binary.LittleEndian.PutUint16(body[7:9], sourceTown)
	binary.LittleEndian.PutUint16(body[9:11], sourceArea)
	binary.LittleEndian.PutUint32(body[11:15], opaqueU32)
	body[15] = tail
	return body
}

// seriaRightDoorWalkInBody 是"从房间 38/1 走进右侧门 ⇒ 目标 38/0"的那条现场请求。
func seriaRightDoorWalkInBody() []byte {
	return buildSeriaRightDoorWalkInRequest(38, 0, 709, 226, 5, 38, 1, 0, currentSeriaRoomDoorWalkInTailByte)
}

// seriaRightDoorSentPackets 把整段上行回包拆成有序的 (msg_id, classification, body) 列表。
func seriaRightDoorSentPackets(t *testing.T, session *gameSession) []seriaRightDoorPacket {
	t.Helper()
	raw := session.conn.(*bufferConn).write.Bytes()
	out := make([]seriaRightDoorPacket, 0, 4)
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		out = append(out, seriaRightDoorPacket{
			msgID:          packet.Header.MsgID,
			classification: packet.Header.Classification,
			body:           packet.Body,
		})
		raw = rest
	}
	return out
}

func seriaRightDoorCountSelectorOpens(packets []seriaRightDoorPacket) int {
	count := 0
	for _, packet := range packets {
		if packet.msgID == uint16(dnfenum.CmdPacketEnterSelectDungeon) &&
			packet.classification == dnfproto.DefaultChannelClassification {
			count++
		}
	}
	return count
}

func seriaRightDoorCountTownTransitions(packets []seriaRightDoorPacket) int {
	count := 0
	for _, packet := range packets {
		if packet.msgID == currentSceneTransitionMsgID {
			count++
		}
	}
	return count
}

// 开关默认关闭；只有显式开启值才算开（未设置/打错字都等于今天）。
func TestSeriaRightDoorSelectSwitchIsExplicitOptIn(t *testing.T) {
	for _, value := range []string{"", "off", "0", "false", "no", "legacy", "typo"} {
		t.Setenv(seriaRightDoorSelectEnv, value)
		if seriaRightDoorSelectEnabled() {
			t.Fatalf("value %q must keep today's behaviour", value)
		}
	}
	// 大小写与首尾空白按工程惯例归一化。
	t.Setenv(seriaRightDoorSelectEnv, " ON ")
	if !seriaRightDoorSelectEnabled() {
		t.Fatal("an explicit on (trimmed, case-insensitive) must enable the experiment")
	}
	for _, value := range []string{"on", "1", "true", "yes", "enable", "enabled"} {
		t.Setenv(seriaRightDoorSelectEnv, value)
		if !seriaRightDoorSelectEnabled() {
			t.Fatalf("value %q must enable the experiment", value)
		}
	}
}

// 默认档（开关未设置）右侧门触发必须逐字节等于今天：一条 op22 + 一条 op23 + 一条 op24，
// 没有 op15、没有任何选择页包。
func TestSeriaRightDoorSelectOffKeepsTodaysBytes(t *testing.T) {
	t.Setenv(seriaRightDoorSelectEnv, "")
	service, session := newSeriaRightDoorTest(t, false, seriaRightDoorTestMapText)
	if err := service.handleTownSetUserArea(session, seriaRightDoorWalkInBody()); err != nil {
		t.Fatal(err)
	}
	got := session.conn.(*bufferConn).write.Bytes()
	want, err := hex.DecodeString(seriaRightDoorTodayBytesHex)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("default switch changed the right-door reply:\n got %x\nwant %x", got, want)
	}
	packets := seriaRightDoorSentPackets(t, session)
	if count := seriaRightDoorCountSelectorOpens(packets); count != 0 {
		t.Fatalf("the default switch must never open the selector, got %d op15 packet(s)", count)
	}
	if count := seriaRightDoorCountTownTransitions(packets); count != 1 {
		t.Fatalf("the default switch must keep exactly one town transition, got %d", count)
	}
}

// 开档下右侧门触发**恰好**走一次既有选择页发送路径，且与 force_select 用的那条
// `sendEnterSelectDungeonState(..., false, true)` 逐字节相同；同时不再发过图 op24。
func TestSeriaRightDoorSelectOnSendsExactlyTheForceSelectSelectorShape(t *testing.T) {
	t.Setenv(seriaRightDoorSelectEnv, "on")
	service, session := newSeriaRightDoorTest(t, true, seriaRightDoorTestMapText)
	if err := service.handleTownSetUserArea(session, seriaRightDoorWalkInBody()); err != nil {
		t.Fatal(err)
	}
	got := append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)

	// 对照：同一份夹具上直接调用 force_select 用的那条发送路径。
	referenceService, referenceSession := newSeriaRightDoorTest(t, true, seriaRightDoorTestMapText)
	if err := referenceService.sendEnterSelectDungeonState(
		referenceSession, forceSelectSourceForceSelectCommand, false, true); err != nil {
		t.Fatal(err)
	}
	want := referenceSession.conn.(*bufferConn).write.Bytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("right-door selector bytes must equal the force_select path:\n got %x\nwant %x", got, want)
	}

	packets := seriaRightDoorSentPackets(t, session)
	if count := seriaRightDoorCountSelectorOpens(packets); count != 1 {
		t.Fatalf("exactly one selector open expected, got %d", count)
	}
	if count := seriaRightDoorCountTownTransitions(packets); count != 0 {
		t.Fatalf("the experiment must replace the town transition, got %d op24 packet(s)", count)
	}
	if len(packets) != 3 {
		t.Fatalf("selector sequence length = %d, want 3 (op15 ack, fatigue, op27 context)", len(packets))
	}
	if packets[0].msgID != uint16(dnfenum.CmdPacketEnterSelectDungeon) ||
		packets[0].classification != dnfproto.DefaultChannelClassification ||
		!bytes.Equal(packets[0].body, upperSuccessBody(buildEnterSelectDungeonAckBody())) {
		t.Fatalf("op15 ack packet = %+v body=%x", packets[0], packets[0].body)
	}
	if packets[1].msgID != currentFatigueMsgID || len(packets[1].body) != 10 {
		t.Fatalf("fatigue packet = %+v body=%x", packets[1], packets[1].body)
	}
	if packets[2].msgID != currentDungeonContextMsgID || len(packets[2].body) != 37 {
		t.Fatalf("op27 context packet = %+v body=%x", packets[2], packets[2].body)
	}
	if !session.enterSelectDungeonSent || !session.enterSelectDungeonContextSent {
		t.Fatalf("selector state flags sent=%t context=%t",
			session.enterSelectDungeonSent, session.enterSelectDungeonContextSent)
	}
}

// 判据窄性 + fail-closed：下表每一条都**不许**打开选择页；`wantTransition` 表示这一条
// 今天本来就该过图（不许被开关顺手改掉），`false` 表示它今天本来就被既有门槛拒绝。
func TestSeriaRightDoorSelectOnStaysNarrowAndFailsClosed(t *testing.T) {
	cases := []struct {
		name           string
		townSceneReady bool
		mapText        string
		currentTown    int64
		currentArea    int64
		body           []byte
		wantTransition bool
	}{
		{
			name:           "same_town_but_player_is_in_the_street",
			townSceneReady: true,
			mapText:        seriaRightDoorTestMapText,
			currentTown:    38,
			currentArea:    0,
			body:           buildSeriaRightDoorWalkInRequest(38, 0, 900, 250, 5, 38, 0, 0, 0),
			wantTransition: true,
		},
		{
			name:           "other_town_street_39",
			townSceneReady: true,
			mapText:        seriaRightDoorMapWithoutRightDoorText,
			currentTown:    39,
			currentArea:    0,
			body: buildSeriaRightDoorWalkInRequest(39, 0, 900, 250, 5, 39, 0, 0,
				currentSeriaRoomDoorWalkInTailByte),
			wantTransition: true,
		},
		{
			name:           "other_town_westcoast_gate_into_the_room",
			townSceneReady: true,
			mapText:        seriaRightDoorTestMapText,
			currentTown:    40,
			currentArea:    0,
			body: buildSeriaRightDoorWalkInRequest(40, 1, 523, 142, 5, 40, 0, 0,
				currentSeriaRoomDoorWalkInTailByte),
			wantTransition: true,
		},
		{
			name:           "room_left_door_to_the_couple_room",
			townSceneReady: true,
			mapText:        seriaRightDoorTestMapText,
			currentTown:    38,
			currentArea:    1,
			body: buildSeriaRightDoorWalkInRequest(101, 0, 66, 250, 5, 38, 1, 0,
				currentSeriaRoomDoorWalkInTailByte),
			wantTransition: false,
		},
		{
			name:           "room_warp_point_destination",
			townSceneReady: true,
			mapText:        seriaRightDoorTestMapText,
			currentTown:    38,
			currentArea:    1,
			body:           buildSeriaRightDoorWalkInRequest(39, 0, 433, 311, 5, 38, 1, 0, 5),
			// 传送阵选点那一类今天由 `LooksLikeTeleportArraySelection` 放行并照常过图，
			// 开关不许把它改成选择页。
			wantTransition: true,
		},
		{
			name:           "room_same_target_but_scene_finalizer_tail",
			townSceneReady: true,
			mapText:        seriaRightDoorTestMapText,
			currentTown:    38,
			currentArea:    1,
			body:           buildSeriaRightDoorWalkInRequest(38, 0, 900, 250, 5, 38, 1, 0, 0),
			wantTransition: true,
		},
		{
			name:           "room_same_target_but_teleport_potion_item_id",
			townSceneReady: true,
			mapText:        seriaRightDoorTestMapText,
			currentTown:    38,
			currentArea:    1,
			body: buildSeriaRightDoorWalkInRequest(38, 0, 900, 250, 5, 38, 1, 20276,
				currentSeriaRoomDoorWalkInTailByte),
			wantTransition: true,
		},
		{
			name:           "room_pvf_declares_no_walk_through_door_to_the_target",
			townSceneReady: true,
			mapText:        seriaRightDoorMapWithoutRightDoorText,
			currentTown:    38,
			currentArea:    1,
			body:           seriaRightDoorWalkInBody(),
			wantTransition: true,
		},
		{
			name:           "room_selector_context_not_ready",
			townSceneReady: false,
			mapText:        seriaRightDoorTestMapText,
			currentTown:    38,
			currentArea:    1,
			body:           seriaRightDoorWalkInBody(),
			wantTransition: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(seriaRightDoorSelectEnv, "on")
			service, session, repositories := newTownMoveTest(t)
			service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
				"new_seria_room.map": test.mapText,
				"new_HendonMyre.map": test.mapText,
				"WestCoast.map":      test.mapText,
				"new_Elvengard.map":  test.mapText,
			})
			if test.townSceneReady {
				session.townMu.Lock()
				session.townPositionSnapshot = currentTownPositionSnapshot{
					CharacterID:   29,
					TownID:        byte(test.currentTown),
					AreaID:        byte(test.currentArea),
					PositionX:     450,
					PositionY:     234,
					MovementCode:  5,
					PositionValid: true,
				}
				session.townMu.Unlock()
			}
			if test.currentTown != 38 || test.currentArea != 1 {
				character, found, err := repositories.Character.Load(context.Background(), "29")
				if err != nil || !found {
					t.Fatalf("load character found=%t err=%v", found, err)
				}
				character.Stats["town_id"] = test.currentTown
				character.Stats["area_id"] = test.currentArea
				if err := repositories.Character.Save(context.Background(), character); err != nil {
					t.Fatal(err)
				}
			}
			if err := service.handleTownSetUserArea(session, test.body); err != nil {
				t.Fatal(err)
			}
			packets := seriaRightDoorSentPackets(t, session)
			if count := seriaRightDoorCountSelectorOpens(packets); count != 0 {
				t.Fatalf("criterion is not narrow: %d selector packet(s) sent", count)
			}
			transitions := seriaRightDoorCountTownTransitions(packets)
			if test.wantTransition && transitions != 1 {
				t.Fatalf("today's transition must survive, got %d op24 packet(s)", transitions)
			}
			if !test.wantTransition && transitions != 0 {
				t.Fatalf("this input is rejected by today's guards, got %d op24 packet(s)", transitions)
			}
		})
	}
}

// 门的判据只吃 PVF：同一间房、两份地图 ⇒ 两个不同矩形；缺行/重复行/目标是房间自己
// 一律 fail-closed。坐标来自文档，一个都不写死。
func TestSeriaRoomExitDoorFollowsRuntimePVF(t *testing.T) {
	room := dnftown.Area{ID: 1, MapPath: "new_seria_room.map", Kind: "gate"}
	service, _, _ := newTownMoveTest(t)

	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"new_seria_room.map": seriaRightDoorTestMapText,
	})
	door, found, reason := service.currentSeriaRoomExitDoorForRequest(room, 38, 0)
	if !found || reason != "" {
		t.Fatalf("right door found=%t reason=%q", found, reason)
	}
	if door.X != 858 || door.Y != 183 || door.Width != 30 || door.Height != 100 ||
		door.Section != "town_movable_area" || door.TownID != 38 || door.AreaID != 0 {
		t.Fatalf("right door=%+v want the PVF row 858 183 30 100 -> 38/0", door)
	}

	// 另一份文档：同一目标、不同矩形 ⇒ 判据跟着 PVF 变（钉子：不许写死坐标）。
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"new_seria_room.map": "[town movable area]\n700 200 40 60 38 0\n[/town movable area]\n",
	})
	moved, found, reason := service.currentSeriaRoomExitDoorForRequest(room, 38, 0)
	if !found || reason != "" || moved.X != 700 || moved.Y != 200 || moved.Width != 40 || moved.Height != 60 {
		t.Fatalf("door must follow the PVF trigger: found=%t reason=%q door=%+v", found, reason, moved)
	}

	// 左门是**另一条**记录：目标 101/0 命中左门，不会被当成右侧门。
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"new_seria_room.map": seriaRightDoorTestMapText,
	})
	left, found, reason := service.currentSeriaRoomExitDoorForRequest(room, 101, 0)
	if !found || reason != "" || left.X != 49 || left.Y != 224 || left.Width != 34 || left.Height != 52 {
		t.Fatalf("left door found=%t reason=%q door=%+v", found, reason, left)
	}

	// 缺行：只剩传送阵的点触发（w=h=0）与左门时，目标 38/0 没有对应的"走过去的门"。
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"new_seria_room.map": seriaRightDoorMapWithoutRightDoorText,
	})
	if _, found, reason := service.currentSeriaRoomExitDoorForRequest(room, 38, 0); found ||
		reason != "seria_room_map_declares_no_walk_through_door_to_the_requested_target" {
		t.Fatalf("missing door found=%t reason=%q", found, reason)
	}

	// 重复行：同一目标两扇门 ⇒ 判据不唯一，fail-closed。
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"new_seria_room.map": "[town movable area]\n700 200 40 60 38 0 858 183 30 100 38 0\n[/town movable area]\n",
	})
	if _, found, reason := service.currentSeriaRoomExitDoorForRequest(room, 38, 0); found ||
		reason != "seria_room_map_declares_multiple_walk_through_doors_to_the_requested_target" {
		t.Fatalf("duplicate doors found=%t reason=%q", found, reason)
	}

	// 目标是这间房自己 ⇒ 不是"走出去的门"。
	if _, found, reason := service.currentSeriaRoomExitDoorForRequest(room, 38, 1); found ||
		reason != "request_target_is_the_room_itself" {
		t.Fatalf("self target found=%t reason=%q", found, reason)
	}

	// 目标超出 u8 ⇒ 失败关闭并给出原因。
	if _, found, reason := service.currentSeriaRoomExitDoorForRequest(room, 38, 300); found ||
		reason != "request_target_outside_u8" {
		t.Fatalf("out-of-range target found=%t reason=%q", found, reason)
	}

	// 文档读不出来 ⇒ 失败关闭（绝不用坐标兜）。
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{})
	if _, found, reason := service.currentSeriaRoomExitDoorForRequest(room, 38, 0); found ||
		reason != "seria_room_map_document_unavailable_in_runtime_pvf" {
		t.Fatalf("missing document found=%t reason=%q", found, reason)
	}
}
