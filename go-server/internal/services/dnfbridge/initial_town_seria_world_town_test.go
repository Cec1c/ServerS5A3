package dnfbridge

// class0/op1375「世界城镇上下文」回补的回归测试。
//
// 这个文件整块移植自同血统 2026-09-24 归档的
// go-server/internal/services/dnfbridge/initial_town_seria_world_town_test.go
// （10830 B / sha256 FED3152C…），在役树此前完全没有对应实现与测试。
//
// 被钉死的不变量（顺序即协议）：
//   - 冷登录进赛丽亚房间 38/1 时，下发序列必须是 noti0x16(op22) → noti0x17(op23)
//     → **noti1375（一个 LE i32 = 已验证的上一外城）** → op24。
//   - 1375 的 body 只能是"本角色已验证的上一村镇快照"里的城镇号；快照缺失、属于
//     别的角色、坐标无效、城镇/区域在运行时 PVF 里查不到、或快照本身就是赛丽亚房间
//     时，**一条都不发**（绝不编造默认值）。
//   - 这条通知不改变实际场景/受控 actor，也不改持久化角色行。
//   - 写失败必须让"初始位置通知"检查点保持未推进 ⇒ 重试能补发这 3 条。
//   - 回滚档（DNFBRIDGE_INITIAL_TOWN_SERIA_WORLD_TOWN=legacy/off/0/false/no）
//     必须逐字节回到修复前序列（只有 op22、op23）。

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestInitialTownSeriaWorldTownPrecedesRoomCommitAndPreservesLocation(t *testing.T) {
	t.Setenv(currentTownSeriaWorldTownEnv, "")
	for _, parentTown := range []byte{38, 39, 40} {
		t.Run(fmt.Sprintf("town_%d", parentTown), func(t *testing.T) {
			s, session, repositories := newTownMoveTest(t)
			prepareInitialTownSeriaWorldTownTest(session, parentTown)
			origin := session.townPrevVillageSnapshot
			before, found, err := repositories.Character.Load(context.Background(), "29")
			if err != nil || !found {
				t.Fatalf("load character found=%t err=%v", found, err)
			}
			row := currentSceneTransitionRow{ObjectOrResourceKey: 29, Value1: 450, Value2: 234, Value3: 5, Value4: 3}
			transition, err := buildCurrentSceneTransitionBody(38, 1, []currentSceneTransitionRow{row})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, 29, 38, 1, row); err != nil {
				t.Fatal(err)
			}
			if err := s.sendCurrentTownTransitionLocked(session, 29, 38, 1, transition); err != nil {
				t.Fatal(err)
			}
			conn := session.conn.(*bufferConn)
			stream := conn.write.Bytes()
			position, stream := splitCurrentGameServerUpperPacketAuto(t, stream)
			if position.Header.MsgID != 22 || !bytes.Equal(position.Body, buildCurrentTownUserPositionNotificationBody(29, 450, 234, 5)) {
				t.Fatalf("room op22 header=%+v body=%x", position.Header, position.Body)
			}
			area, stream := splitCurrentGameServerUpperPacketAuto(t, stream)
			if area.Header.MsgID != 23 || !bytes.Equal(area.Body, buildCurrentTownUserAreaNotificationBody(29, 38, 1, 450, 234, 5, 3)) {
				t.Fatalf("room op23 header=%+v body=%x", area.Header, area.Body)
			}
			worldTown, stream := splitCurrentGameServerUpperPacketAuto(t, stream)
			if worldTown.Header.Classification != 0 || worldTown.Header.MsgID != currentTownWorldTownContextNotificationMsgID ||
				!bytes.Equal(worldTown.Body, []byte{parentTown, 0, 0, 0}) {
				t.Fatalf("native i32 world town header=%+v body=%x parent=%d", worldTown.Header, worldTown.Body, parentTown)
			}
			commit, stream := splitCurrentGameServerUpperPacketAuto(t, stream)
			if commit.Header.MsgID != 24 || !bytes.Equal(commit.Body, transition) || len(stream) != 0 {
				t.Fatalf("room op24 header=%+v body=%x trailing=%x", commit.Header, commit.Body, stream)
			}
			if session.townPrevVillageSnapshot != origin {
				t.Fatalf("world-town initialization changed exit origin: %+v", session.townPrevVillageSnapshot)
			}
			after, found, err := repositories.Character.Load(context.Background(), "29")
			if err != nil || !found || !reflect.DeepEqual(before, after) {
				t.Fatalf("map-context notification changed persisted character: found=%t err=%v", found, err)
			}
			conn.write.Reset()
			if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, 29, 38, 1, row); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 {
				t.Fatalf("initial-location checkpoint replayed world-town notification: %x", conn.write.Bytes())
			}
		})
	}
}

func TestInitialTownSeriaWorldTownUsesAuthoritativeColdLoginOwner(t *testing.T) {
	t.Setenv(currentTownSeriaWorldTownEnv, "")
	// 归档那份测试把登录房间写死成 38/1 `new_seria_room.map`，那对 9/24 是成立的。
	// 在役树已经演进成"优先落在本城自己的赛丽亚房间"（initial_town_login_owner.go 的
	// 落点解析）：fixture 里只有 town 39 自带一个 `new_HendonMyre_seria.map`（39/2），
	// 其余落点回退到共用房间 38/1。这里按**在役真值**逐 (town, 登出位置) 钉死，
	// 同时仍然断言"落点必须是赛丽亚房间"这一条 1375 自己的前置条件。
	cases := []struct {
		parentTown   byte
		logoutInRoom bool
		wantTown     byte
		wantArea     byte
		wantMap      string
	}{
		{39, false, 39, 2, "new_HendonMyre_seria.map"},
		{40, false, 38, 1, "new_seria_room.map"},
		{39, true, 38, 1, "new_seria_room.map"},
		{40, true, 38, 1, "new_seria_room.map"},
	}
	for _, tc := range cases {
		parentTown, logoutInRoom := tc.parentTown, tc.logoutInRoom
		t.Run(fmt.Sprintf("town_%d_logout_in_room_%t", parentTown, logoutInRoom), func(t *testing.T) {
			s, session, repositories := newTownMoveTest(t)
			character, found, err := repositories.Character.Load(context.Background(), "29")
			if err != nil || !found {
				t.Fatalf("load character found=%t err=%v", found, err)
			}
			origin := currentTownPositionSnapshot{
				CharacterID: 29, TownID: parentTown, AreaID: 0,
				PositionX: 633, PositionY: 185, MovementCode: 7, PositionValid: true,
			}
			if logoutInRoom {
				currentTownPrevVillageApplyOriginStats(character.Stats, origin)
			} else {
				character.Stats["town_id"] = int64(parentTown)
				character.Stats["area_id"] = 0
				character.Stats["pos_x"] = 633
				character.Stats["pos_y"] = 185
				character.Stats["direction"] = 7
			}
			if err := repositories.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			// 以一个没有任何内存态旧城镇的新连接开始（真实冷登录）。登出在外城
			// 与"持久化的房间原点"两条路都必须经过既有登录事务，而不是测试专用绑定。
			_, townID, areaID, row, mapPath, err := s.currentCharacterListLoginTransition(context.Background(), session, 29, character)
			if err != nil {
				t.Fatal(err)
			}
			if townID != tc.wantTown || areaID != tc.wantArea || mapPath != tc.wantMap {
				t.Fatalf("login room=%d/%d map=%q, want %d/%d %q", townID, areaID, mapPath, tc.wantTown, tc.wantArea, tc.wantMap)
			}
			// 1375 自己的前置条件：登录落点必须是一座赛丽亚房间。
			if !townMapPathLooksLikeSeria(mapPath) {
				t.Fatalf("login map %q is not a Seria room, so the world-town notification must not be expected here", mapPath)
			}
			if session.townPrevVillageSnapshot != origin {
				t.Fatalf("cold login previous=%+v, want %+v (validated outdoor origin)", session.townPrevVillageSnapshot, origin)
			}
			session.initialTownRouteCharacterID = 29
			session.initialTownRouteStage = currentInitialTownRoutePlayerStatePrepared
			if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, 29, townID, areaID, row); err != nil {
				t.Fatal(err)
			}
			stream := session.conn.(*bufferConn).write.Bytes()
			for _, msgID := range []uint16{22, 23, currentTownWorldTownContextNotificationMsgID} {
				packet, next := splitCurrentGameServerUpperPacketAuto(t, stream)
				if packet.Header.MsgID != msgID {
					t.Fatalf("cold login packet=%+v want=%d", packet.Header, msgID)
				}
				if msgID == currentTownWorldTownContextNotificationMsgID && !bytes.Equal(packet.Body, []byte{parentTown, 0, 0, 0}) {
					t.Fatalf("cold login world town=%x, want %d", packet.Body, parentTown)
				}
				stream = next
			}
			if len(stream) != 0 || session.townPrevVillageSnapshot != origin {
				t.Fatalf("extra refresh=%x previous=%+v", stream, session.townPrevVillageSnapshot)
			}
		})
	}
}

func TestInitialTownSeriaWorldTownRejectsUnavailableOrForeignOrigin(t *testing.T) {
	t.Setenv(currentTownSeriaWorldTownEnv, "")
	cases := []struct {
		name   string
		change func(*gameSession)
	}{
		{"missing", func(session *gameSession) { session.townPrevVillageSnapshot = currentTownPositionSnapshot{} }},
		{"foreign_character", func(session *gameSession) { session.townPrevVillageSnapshot.CharacterID = 30 }},
		{"invalid_position", func(session *gameSession) { session.townPrevVillageSnapshot.PositionValid = false }},
		{"unknown_town", func(session *gameSession) { session.townPrevVillageSnapshot.TownID = 255 }},
		{"unknown_area", func(session *gameSession) { session.townPrevVillageSnapshot.AreaID = 255 }},
		{"room_origin", func(session *gameSession) {
			session.townPrevVillageSnapshot.TownID = 38
			session.townPrevVillageSnapshot.AreaID = 1
		}},
		{"selected_character_mismatch", func(session *gameSession) { session.selectedCharacterID = 30 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, session, _ := newTownMoveTest(t)
			prepareInitialTownSeriaWorldTownTest(session, 40)
			tc.change(session)
			origin := session.townPrevVillageSnapshot
			if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, 29, 38, 1, currentSceneTransitionRow{}); err != nil {
				t.Fatal(err)
			}
			stream := session.conn.(*bufferConn).write.Bytes()
			for _, msgID := range []uint16{22, 23} {
				packet, next := splitCurrentGameServerUpperPacketAuto(t, stream)
				if packet.Header.MsgID != msgID {
					t.Fatalf("unexpected packet=%+v", packet.Header)
				}
				stream = next
			}
			if len(stream) != 0 || session.townPrevVillageSnapshot != origin {
				t.Fatalf("invented world town or changed invalid origin: packet=%x origin=%+v", stream, session.townPrevVillageSnapshot)
			}
		})
	}
}

func TestInitialTownSeriaWorldTownDoesNotRunOutsideInitialRoomBoundary(t *testing.T) {
	t.Setenv(currentTownSeriaWorldTownEnv, "")
	cases := []struct {
		name   string
		town   byte
		area   byte
		change func(*gameSession)
		count  int
	}{
		{"ordinary_town", 40, 0, func(*gameSession) {}, 2},
		{"unknown_room", 38, 255, func(*gameSession) {}, 2},
		{"route_character_mismatch", 38, 1, func(session *gameSession) { session.initialTownRouteCharacterID = 30 }, 0},
		{"player_state_not_prepared", 38, 1, func(session *gameSession) { session.initialTownRouteStage = currentInitialTownRouteActorBound }, 0},
		{"already_sent", 38, 1, func(session *gameSession) { session.initialTownLocationNotificationsSent = true }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, session, _ := newTownMoveTest(t)
			prepareInitialTownSeriaWorldTownTest(session, 40)
			tc.change(session)
			if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, 29, tc.town, tc.area, currentSceneTransitionRow{}); err != nil {
				t.Fatal(err)
			}
			stream := session.conn.(*bufferConn).write.Bytes()
			count := 0
			for len(stream) != 0 {
				packet, next := splitCurrentGameServerUpperPacketAuto(t, stream)
				if packet.Header.MsgID == currentTownWorldTownContextNotificationMsgID {
					t.Fatal("world-town initialization escaped initial Seria room boundary")
				}
				count++
				stream = next
			}
			if count != tc.count {
				t.Fatalf("packet count=%d want=%d", count, tc.count)
			}
		})
	}
}

func TestInitialTownSeriaWorldTownSendFailureKeepsLocationRetryable(t *testing.T) {
	t.Setenv(currentTownSeriaWorldTownEnv, "")
	s, session, _ := newTownMoveTest(t)
	prepareInitialTownSeriaWorldTownTest(session, 40)
	conn := &rejectInitialSeriaWorldTownConn{reject: true}
	session.conn = conn
	if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, 29, 38, 1, currentSceneTransitionRow{}); err == nil {
		t.Fatal("world-town write failure was hidden")
	}
	if session.initialTownLocationNotificationsSent {
		t.Fatal("failed world-town write advanced the initial-location checkpoint")
	}
	conn.write.Reset()
	conn.reject = false
	if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, 29, 38, 1, currentSceneTransitionRow{}); err != nil {
		t.Fatal(err)
	}
	stream := conn.write.Bytes()
	for _, msgID := range []uint16{22, 23, currentTownWorldTownContextNotificationMsgID} {
		packet, next := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.MsgID != msgID {
			t.Fatalf("retry packet=%+v want=%d", packet.Header, msgID)
		}
		stream = next
	}
	if len(stream) != 0 || !session.initialTownLocationNotificationsSent {
		t.Fatalf("retry checkpoint=%t trailing=%x", session.initialTownLocationNotificationsSent, stream)
	}
}

// TestInitialTownSeriaWorldTownLegacyRollbackIsByteIdentical 是逐字节回退证明：
// 回滚档下这条通知一条都不发，整条下发流必须与"修复前"完全相同 ——
// 前两帧逐字节一致，且没有第三帧。
func TestInitialTownSeriaWorldTownLegacyRollbackIsByteIdentical(t *testing.T) {
	build := func(t *testing.T, envValue string) []byte {
		t.Helper()
		s, session, _ := newTownMoveTest(t)
		prepareInitialTownSeriaWorldTownTest(session, 40)
		t.Setenv(currentTownSeriaWorldTownEnv, envValue)
		if err := s.sendCurrentInitialTownLocationNotificationsLocked(session, 29, 38, 1, currentSceneTransitionRow{}); err != nil {
			t.Fatal(err)
		}
		return append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
	}
	fixed := build(t, "")
	legacy := build(t, "legacy")
	if len(fixed) <= len(legacy) {
		t.Fatalf("fixed stream len=%d is not longer than legacy len=%d", len(fixed), len(legacy))
	}
	if !bytes.HasPrefix(fixed, legacy) {
		t.Fatalf("legacy rollback is not a byte prefix of the fixed stream:\n legacy=%x\n  fixed=%x", legacy, fixed)
	}
	// 回滚档必须恰好是 op22 + op23，没有别的帧。
	stream := legacy
	for _, msgID := range []uint16{22, 23} {
		packet, next := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.MsgID != msgID {
			t.Fatalf("legacy packet=%+v want=%d", packet.Header, msgID)
		}
		stream = next
	}
	if len(stream) != 0 {
		t.Fatalf("legacy rollback wrote extra bytes past op23: %x", stream)
	}
	// 修复档就是在 op23 之后多了这一条 i32 通知，body = 上一外城 40。
	rest := fixed[len(legacy):]
	worldTown, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if worldTown.Header.MsgID != currentTownWorldTownContextNotificationMsgID ||
		worldTown.Header.Classification != 0 ||
		!bytes.Equal(worldTown.Body, []byte{40, 0, 0, 0}) {
		t.Fatalf("fixed-only frame header=%+v body=%x, want class0 op%d body=28000000", worldTown.Header, worldTown.Body, currentTownWorldTownContextNotificationMsgID)
	}
	if len(rest) != 0 {
		t.Fatalf("fixed stream trailing=%x", rest)
	}
}

// TestInitialTownSeriaWorldTownEnvGateLiterals 钉死闸门字面量集合：默认与任意
// 未识别值都是修复档；legacy/off/0/false/no（大小写与空白不敏感）是回滚档。
func TestInitialTownSeriaWorldTownEnvGateLiterals(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", true}, {"on", true}, {"native", true}, {"whatever", true},
		{"legacy", false}, {"LEGACY", false}, {" off ", false},
		{"0", false}, {"false", false}, {"no", false},
	} {
		t.Run(fmt.Sprintf("value_%q", tc.value), func(t *testing.T) {
			t.Setenv(currentTownSeriaWorldTownEnv, tc.value)
			if got := currentTownSeriaWorldTownEnabled(); got != tc.want {
				t.Fatalf("enabled=%t, want %t for %q", got, tc.want, tc.value)
			}
			wantMode := "legacy_disabled"
			if tc.want {
				wantMode = "native_i32_previous_town"
			}
			if got := currentTownSeriaWorldTownMode(); got != wantMode {
				t.Fatalf("mode=%q, want %q for %q", got, wantMode, tc.value)
			}
		})
	}
}

// TestRealScriptPVFTown38ExcludesUserPretownInfo 用**真实运行时 Script.pvf** 钉死
// "为什么需要这条 1375"的原生事实：当前 PVF 的城镇 38 文档（`town/new_Elvengard.twn`）
// 第一行就是 `[exclude user pretown info]`，而外城 39/40 的文档没有这个标记。
// 原生 setter sub_3406800 对带该标记的城镇**不覆盖**已有缓存 —— 于是冷登录时缓存
// 不会被 op24（城镇 38）填上，必须由这条通知先用"已验证的上一外城"补进去。
//
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf 路径> go test ./internal/services/dnfbridge -run RealScriptPVFTown38
func TestRealScriptPVFTown38ExcludesUserPretownInfo(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the [exclude user pretown info] anchor")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	const pretownAnchor = "[exclude user pretown info]"
	document, err := archive.ReadText("town/new_Elvengard.twn")
	if err != nil {
		t.Fatalf("read town 38 document: %v", err)
	}
	if !strings.Contains(document, pretownAnchor) {
		t.Fatalf("real PVF town 38 (town/new_Elvengard.twn) lost %q; the op%d world-town context would then be redundant", pretownAnchor, currentTownWorldTownContextNotificationMsgID)
	}
	for _, outside := range []struct {
		townID int64
		path   string
	}{
		{39, "town/new_HendonMyre.twn"},
		{40, "town/new_WestCoast.twn"},
	} {
		text, err := archive.ReadText(outside.path)
		if err != nil {
			t.Fatalf("read town %d document: %v", outside.townID, err)
		}
		if strings.Contains(text, pretownAnchor) {
			t.Fatalf("real PVF town %d (%s) now carries %q; outdoor owning towns must stay projectable", outside.townID, outside.path, pretownAnchor)
		}
	}
}

func prepareInitialTownSeriaWorldTownTest(session *gameSession, townID byte) {
	session.initialTownRouteCharacterID = 29
	session.initialTownRouteStage = currentInitialTownRoutePlayerStatePrepared
	session.townPrevVillageSnapshot = currentTownPositionSnapshot{
		CharacterID: 29, TownID: townID, AreaID: 0,
		PositionX: 612, PositionY: 244, MovementCode: 5, PositionValid: true,
	}
}

type rejectInitialSeriaWorldTownConn struct {
	bufferConn
	reject bool
}

func (c *rejectInitialSeriaWorldTownConn) Write(data []byte) (int, error) {
	if c.reject && len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == currentTownWorldTownContextNotificationMsgID {
		return 0, errors.New("test world-town notification write failure")
	}
	return c.bufferConn.Write(data)
}
