package dnfbridge

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住 `DNFBRIDGE_LOGIN_SERIA_ENTRY_TOWN`：**登录直接落在赛丽亚房间**时，
// 把"进门来源城镇"（`session.townSeriaRoomEntrySnapshot`）补绑成"上一次所在城镇"，
// 于是登录后第一次走**下方门**（op1425）回到来源城镇里"通往本房间"的那扇门，
// 而不是房间自己所在城镇 38 的固定外门口 —— 即今天线上的错行为。
//
// 现场缺陷（`_dist/seria-door-diagnosis.md` §0/§4，线上实测 2026-09-28 conn game-001151 char 6）：
//
//	:55275  登录→直接落在房间→走下方门  landing_source=seria_room_outer_door entry_used=false
//	        ⇒ 38/0 艾尔文防线 (622,196)   ← 报障
//	:60167  走门进房→再走下方门          landing_source=seria_room_entry_town_door entry_used=true
//	        ⇒ 11/0 素喃 (2032,185)        ← 操作者要的行为，机制本来就存在
//
// 根因：`townSeriaRoomEntrySnapshot` 全仓只有 `town_move.go:184`（op36 走门进房）一个绑定点，
// 登录路径一个都没有；`town_prev_village.go:253-262` 于是必然取"没有进门来源"的失败分支。
//
// 三条用例对应任务书 (a)/(b)/(c)：
//   - (a) 默认档：登录落房间 + 有效原点 ⇒ 补绑 ⇒ op1425 落来源城镇门口（seria_room_entry_town_door）；
//   - (b) `off`/`legacy` 档：逐字节今天（不绑 ⇒ seria_room_outer_door = 38/0 (622,196)）；
//   - (c) 原点无效、或落点根本不是赛丽亚房间 ⇒ 一位都不绑。
//
// 反证（本次交付报告里有命令与输出）：把 `DNFBRIDGE_LOGIN_SERIA_ENTRY_TOWN=off` 注入进程后再跑
// (a)，断言立刻失败 —— 也就是说 (a) 确实在测新增的那条分支，不是恒真。

// loginSeriaEntryTownFixture 造出"登录直接落在赛丽亚房间、且有一个已校验过的上一位置"的输入：
// 角色 29 上次下线在**城镇 40 的主街 area 0**（夹具 `newTownMoveTest`：town 40 没有赛丽亚房间）
// 的 (posX,posY)，方向 5、area_state 3 ⇒ 默认三级落点必然落在 38/1 的房间，
// 而 prev-village 原点 = 40/0 的持久化坐标（source=character_list_login_previous_non_seria_location）。
//
// 门的 PVF 用既有夹具（`town_seria_entry_town_exit_test.go`）：
// `WestCoast.map` 里通往 38/1 的门 = `[area] 473 127 100 30` + `[move town] 38 1` ⇒ 中心 (523,142)。
// 落点 (523,142) **刻意不等于**原点 (1500,500)：这样"走了来源城镇的门口"与"把原点坐标当落点"
// 在断言层面可区分。
func loginSeriaEntryTownFixture(
	t *testing.T,
	townID, areaID, posX, posY int64,
) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	// 出房间的档位固定在**默认档 entry_town**（本文件的开关是另一个 env，两者互不干扰）。
	t.Setenv(currentTownSeriaExitDoorEnv, "")
	service, session, repositories := newTownMoveTest(t)
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["town_id"] = townID
	character.Stats["area_id"] = areaID
	character.Stats["pos_x"] = posX
	character.Stats["pos_y"] = posY
	character.Stats["direction"] = 5
	character.Stats["area_state"] = 3
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	service.seriaRoomDoorMapReader = fakePVFMapReader(t, map[string]string{
		"WestCoast.map":      westCoastMapPVFText,
		"WestCoast_post.map": westCoastPostMapPVFText,
		"new_Elvengard.map":  elvengardMapPVFText,
	})
	return service, session, repositories
}

// loginIntoTheSeriaRoom 跑一次角色列表登录落点解析，并钉住"落点确实是 38/1 的赛丽亚房间"。
func loginIntoTheSeriaRoom(t *testing.T, service *Service, session *gameSession, repositories dnfrepo.Group) {
	t.Helper()
	character := loadStoredCharacter(t, repositories)
	_, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(), session, 29, character,
	)
	if err != nil {
		t.Fatalf("login must not block the route: %v", err)
	}
	if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
		row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY ||
		!townMapPathLooksLikeSeria(mapPath) {
		t.Fatalf("login landing town=%d area=%d row=%+v map=%q, want the 38/1 Seria room",
			townID, areaID, row, mapPath)
	}
}

// lastSeriaRoomExitBody 发一次 op1425（下方门），返回它回答的 op24 body。
func lastSeriaRoomExitBody(t *testing.T, service *Service, session *gameSession) []byte {
	t.Helper()
	connection := session.conn.(*bufferConn)
	connection.write.Reset()
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketPrevVillage),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.MsgID != currentSceneTransitionMsgID || len(rest) != 0 {
		t.Fatalf("prev-village header=%+v trailing=%x", packet.Header, rest)
	}
	return packet.Body
}

// wantSeriaRoomExitBody 造出期望的 op24 body。
func wantSeriaRoomExitBody(t *testing.T, townID, areaID byte, x, y uint16, direction, areaState byte) []byte {
	t.Helper()
	body, err := buildCurrentSceneTransitionBody(townID, areaID, []currentSceneTransitionRow{{
		ObjectOrResourceKey: 29,
		Value1:              x,
		Value2:              y,
		Value3:              direction,
		Value4:              areaState,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// (a) 修复档（默认）：登录落进赛丽亚房间时补绑进门来源城镇，
// 登录后第一次走下方门 ⇒ 来源城镇 40 的门口 40/0 (523,142)，`seria_room_entry_town_door`。
func TestLoginSeriaRoomBindsTheEntryTownForTheFirstRoomExit(t *testing.T) {
	service, session, repositories := loginSeriaEntryTownFixture(t, 40, 0, 1500, 500)
	readLog := newRecordedPacketLog(t, service)

	loginIntoTheSeriaRoom(t, service, session, repositories)
	// ★ 本次修复的绑定点：登录落点是赛丽亚房间 ⇒ 进门来源 = 上一次所在城镇 40/0 (1500,500)。
	if entry := session.townSeriaRoomEntrySnapshot; !entry.PositionValid ||
		entry.CharacterID != 29 || entry.TownID != 40 || entry.AreaID != 0 ||
		entry.PositionX != 1500 || entry.PositionY != 500 {
		t.Fatalf("login must bind the Seria-room entry origin to the previous town 40/0 (1500,500), got %+v", entry)
	}
	// prev-village 语义不变：原点仍然绑成同一个已校验过的上一位置。
	if origin := session.townPrevVillageSnapshot; !origin.PositionValid ||
		origin.TownID != 40 || origin.AreaID != 0 || origin.PositionX != 1500 || origin.PositionY != 500 {
		t.Fatalf("prev-village snapshot must stay the previous town 40/0 (1500,500), got %+v", origin)
	}

	body := lastSeriaRoomExitBody(t, service, session)
	// 来源城镇 40 的主街 `WestCoast.map` 里"通往 38/1"的门 = `473 127 100 30` ⇒ 中心 (523,142)。
	if want := wantSeriaRoomExitBody(t, 40, 0, 523, 142, 5, 3); !bytes.Equal(body, want) {
		t.Fatalf("first room exit body=%x want the origin town's Seria door 40/0 (523,142) body=%x", body, want)
	}
	stored := loadStoredCharacter(t, repositories)
	if stored.Stats["town_id"] != 40 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 523 || stored.Stats["pos_y"] != 142 ||
		stored.Stats["direction"] != 5 {
		t.Fatalf("first room exit must persist the origin town's door 40/0 (523,142): %+v", stored.Stats)
	}

	text := readLog()
	for _, want := range []string{
		// 修复生效的第一判据（任务书要求新增的可观测字段）：
		"seria_entry_town_bound=true",
		// 出口回答的真源（`town_prev_village.go:244`），与线上 :60167 同一条分支：
		"landing_source=seria_room_entry_town_door",
		"entry_used=true",
		"entry_town_id=40",
		"entry_area_id=0",
		"entry_position_x=1500",
		"entry_position_y=500",
		"entry_position_valid=true",
		"door_town_id=40",
		"door_area_id=0",
		"door_map_path=WestCoast.map",
		"door_trigger_x=473",
		"door_trigger_y=127",
		"door_trigger_width=100",
		"door_trigger_height=30",
		"door_trigger_section=town_movable_area_with_animation",
		"seria_exit_mode=entry_town",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("fixed login-then-exit log missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "landing_source=seria_room_outer_door") {
		t.Fatalf("fixed behaviour still answered the room town's outer door:\n%s", text)
	}
}

// (b) 一键回退档 `off`/`legacy`（及别名 0/false/no）：逐字节今天的口径 ——
// 不绑进门来源 ⇒ op1425 走 `town_prev_village.go:253-262` 的失败分支 ⇒
// 房间所在城镇 38 的固定外门口 38/0 (622,196)，`landing_source=seria_room_outer_door`。
func TestLoginSeriaRoomEntryTownSwitchOffKeepsTodaysOuterDoor(t *testing.T) {
	for _, value := range []string{"off", "legacy", "0", "false", "no", "OFF", " Legacy "} {
		t.Run("switch_"+strconv.Quote(value), func(t *testing.T) {
			service, session, repositories := loginSeriaEntryTownFixture(t, 40, 0, 1500, 500)
			readLog := newRecordedPacketLog(t, service)
			t.Setenv(currentLoginSeriaEntryTownEnv, value)

			loginIntoTheSeriaRoom(t, service, session, repositories)
			if entry := session.townSeriaRoomEntrySnapshot; entry.PositionValid {
				t.Fatalf("switch=%q must not bind the Seria-room entry origin, got %+v", value, entry)
			}
			// prev-village 原点照旧绑（本开关只管"进门来源城镇"这一份快照）。
			if origin := session.townPrevVillageSnapshot; !origin.PositionValid || origin.TownID != 40 {
				t.Fatalf("switch=%q must keep binding the prev-village origin, got %+v", value, origin)
			}

			body := lastSeriaRoomExitBody(t, service, session)
			if want := wantSeriaRoomExitBody(t, 38, 0, 622, 196, 5, 3); !bytes.Equal(body, want) {
				t.Fatalf("switch=%q exit body=%x want today's outer door 38/0 (622,196) body=%x", value, body, want)
			}
			stored := loadStoredCharacter(t, repositories)
			if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 0 ||
				stored.Stats["pos_x"] != 622 || stored.Stats["pos_y"] != 196 {
				t.Fatalf("switch=%q must persist today's outer door 38/0 (622,196): %+v", value, stored.Stats)
			}

			text := readLog()
			for _, want := range []string{
				"seria_entry_town_bound=false",
				"landing_source=seria_room_outer_door",
				"entry_used=false",
				"seria_entry_resolve_reason=no_seria_room_entry_origin_for_this_character",
				"door_town_id=38",
				"entry_position_valid=false",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("switch=%q rollback log missing %q:\n%s", value, want, text)
				}
			}
			if strings.Contains(text, "landing_source=seria_room_entry_town_door") {
				t.Fatalf("switch=%q still used the entry town's door:\n%s", value, text)
			}
		})
	}
}

// (c1) 没有有效原点时一位都不绑：上次下线就在 38/1 的房间里 ⇒ 第一来源被
// `currentTownPrevVillageOriginIsPersistable` 拒绝（origin 自己就是赛丽亚房间），
// 第二来源（持久化 `prev_village_*`）也不存在 ⇒ `prevVillageOrigin.PositionValid=false`。
// 落点仍然是 38/1 的房间，出房间照旧是房间所在城镇的外门口（与改动前逐字节一致）。
func TestLoginSeriaRoomEntryTownStaysUnboundWithoutAValidOrigin(t *testing.T) {
	service, session, repositories := loginSeriaEntryTownFixture(t, 38, 1, 450, 234)
	readLog := newRecordedPacketLog(t, service)

	loginIntoTheSeriaRoom(t, service, session, repositories)
	if session.townPrevVillageSnapshot.PositionValid {
		t.Fatalf("a persisted Seria-room position must not become the prev-village origin: %+v",
			session.townPrevVillageSnapshot)
	}
	if entry := session.townSeriaRoomEntrySnapshot; entry.PositionValid {
		t.Fatalf("without a valid origin nothing may be bound, got %+v", entry)
	}

	body := lastSeriaRoomExitBody(t, service, session)
	if want := wantSeriaRoomExitBody(t, 38, 0, 622, 196, 5, 3); !bytes.Equal(body, want) {
		t.Fatalf("exit body=%x want the unchanged outer door 38/0 (622,196) body=%x", body, want)
	}
	text := readLog()
	if !strings.Contains(text, "seria_entry_town_bound=false") ||
		!strings.Contains(text, "landing_source=seria_room_outer_door") ||
		!strings.Contains(text, "entry_used=false") {
		t.Fatalf("no-origin login-then-exit must stay byte-identical to today:\n%s", text)
	}
}

// (c2) 落点**不是**赛丽亚房间时一位都不绑：`DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep` 让第二级生效，
// 角色留在城镇 40 的主街（`WestCoast.map`，不是赛丽亚房间）—— 即使原点有效也不许补绑。
// 街上走 op1425 照旧被 `current_area_is_not_seria_room` 拒（既有行为，一行不改）。
func TestLoginSeriaRoomEntryTownStaysUnboundWhenTheLandingIsNotASeriaRoom(t *testing.T) {
	service, session, repositories := loginSeriaEntryTownFixture(t, 40, 0, 1500, 500)
	readLog := newRecordedPacketLog(t, service)
	t.Setenv(currentLoginPersistedTownEnv, currentLoginPersistedTownKeepValue)

	character := loadStoredCharacter(t, repositories)
	_, townID, areaID, _, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(), session, 29, character,
	)
	if err != nil {
		t.Fatalf("second-level login must not block the route: %v", err)
	}
	if townID != 40 || areaID != 0 || townMapPathLooksLikeSeria(mapPath) {
		t.Fatalf("landing town=%d area=%d map=%q, want the persisted street 40/0 WestCoast.map",
			townID, areaID, mapPath)
	}
	if !session.townPrevVillageSnapshot.PositionValid {
		t.Fatalf("the previous town origin must still be bound: %+v", session.townPrevVillageSnapshot)
	}
	if entry := session.townSeriaRoomEntrySnapshot; entry.PositionValid {
		t.Fatalf("a non-Seria landing must not bind the Seria-room entry origin, got %+v", entry)
	}

	if err := service.handleCurrentPrevVillage(session, nil); err != nil {
		t.Fatal(err)
	}
	text := readLog()
	if !strings.Contains(text, "seria_entry_town_bound=false") ||
		!strings.Contains(text, "game-town-prev-village-blocked") ||
		!strings.Contains(text, "reason=current_area_is_not_seria_room") {
		t.Fatalf("street login must stay unbound and keep today's op1425 rejection:\n%s", text)
	}
}

// 开关语义：**默认（未设置/空/未知值）= 修复档**；只有显式回退取值才关闭。
// 取值集合与既有 `DNFBRIDGE_TOWN_SERIA_EXIT_DOOR` / `DNFBRIDGE_LOGIN_PERSISTED_TOWN`
// 同一约定（回退档 = off/legacy/0/false/no）。
func TestLoginSeriaEntryTownSwitchDefaultsToTheFix(t *testing.T) {
	for _, value := range []string{"", "   ", "on", "true", "1", "yes", "entry_town", "garbage", "OF"} {
		t.Run("enabled_"+strconv.Quote(value), func(t *testing.T) {
			t.Setenv(currentLoginSeriaEntryTownEnv, value)
			if !currentLoginSeriaEntryTownEnabled() {
				t.Fatalf("%s=%q must stay on the fixed default tier",
					currentLoginSeriaEntryTownEnv, value)
			}
		})
	}
	for _, value := range []string{"off", "OFF", " off ", "legacy", "Legacy", "0", "false", "no"} {
		t.Run("legacy_"+strconv.Quote(value), func(t *testing.T) {
			t.Setenv(currentLoginSeriaEntryTownEnv, value)
			if currentLoginSeriaEntryTownEnabled() {
				t.Fatalf("%s=%q must fall back to today's behaviour",
					currentLoginSeriaEntryTownEnv, value)
			}
		})
	}
}
