package dnfbridge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// 现场报障与定稿口径（三级落点）：
//  1. 一轮之前登录路径无条件写回"艾尔文防线的赛丽亚房间"(38/1 gate 450/234)；
//  2. 后来改成"恢复角色记录里持久化的 (town_id, area_id, pos_x, pos_y)"，于是登录会回到
//     上次的精确房间——实测落点是城镇街道 (`new_Elvengard.map`)、副本门口
//     (`new_D_Elvengard_L.map`)、阿法利亚营地街道 (`Alfhlyra_R.map`)；
//  3. 定稿三级落点（见 currentCharacterListLoginTransition）：
//     ① 该城镇有赛丽亚房间 → 落在该城镇的赛丽亚房间锚点；
//     ② 该城镇没有赛丽亚房间（运行时 PVF 实测 39~46 都没有）→ **留在该城镇**，
//     沿用持久化精确落点（fallback_reason=persisted_position）——
//     **这一级默认被门控关闭**（`DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep` 才打开）：
//     操作者 2026-09-26 报障「选择角色重新进入游戏会在上次下线的地方出现」，
//     而更早的定稿条目是「重新登陆固定刷新在艾尔文防线的赛利亚房间」（已完成），
//     所以默认走 ③；
//     ③ 持久化信息不可用、或 ② 被门控跳过 → 38/1。
//
// 下面这组用例逐条钉住分支、落点与日志字段；prev-village（op1425）语义不变也在这里断言。
// 运行时 PVF 的实测事实（只有 town 38 有赛丽亚房间）由
// initial_town_seria_room_real_pvf_test.go 用真实 Script.pvf 钉住。

const townSeriaFixtureElvengard = "[area]\n0 `new_Elvengard.map` `[normal]`\n[/area]\n" +
	"[area]\n1 `new_seria_room.map` `[gate]` 450 234\n[/area]\n" +
	"[name]\n`Elvengard`"

// loadTownSeriaFixtureService 用给定夹具替换服务的运行时 PVF 城镇表。
func loadTownSeriaFixtureService(t *testing.T, service *Service, source townMoveTestSource) {
	t.Helper()
	table, err := dnftown.Load(context.Background(), source, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service.townCatalog = table
}

// newRecordedPacketLog 把服务日志指向临时文件，返回读取函数。
func newRecordedPacketLog(t *testing.T, service *Service) func() string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger
	return func() string {
		t.Helper()
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
}

// 分支①：上次在城镇 41（阿法利亚营地），下次登录落点是 **41 的赛丽亚房间**。
// 夹具给 41 造了一个赛丽亚房间——运行时 PVF 的 41 没有（见 real-pvf 用例），
// 这里覆盖"用持久化 town_id 解析该城镇赛丽亚房间"这条分支。
func TestCharacterListLoginLandsInPersistedTownSeriaRoom(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	loadTownSeriaFixtureService(t, service, townMoveTestSource{
		"town/town.lst":          "38 `new_Elvengard.twn` 41 `new_Alfhlyra.twn`",
		"town/new_Elvengard.twn": townSeriaFixtureElvengard,
		"town/new_Alfhlyra.twn": "[area]\n0 `Cataclysm/Town/Alfhlyra/Alfhlyra_L.map` `[normal]`\n[/area]\n" +
			"[area]\n2 `Cataclysm/Town/Alfhlyra/new_seria_room.map` `[gate]` 733 214\n[/area]\n" +
			"[name]\n`Alfhlyra`",
	})
	readLog := newRecordedPacketLog(t, service)

	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	// 上次离线：城镇 41 的街道 area 0，坐标 900/120。
	character.Stats["town_id"] = 41
	character.Stats["area_id"] = 0
	character.Stats["pos_x"] = 900
	character.Stats["pos_y"] = 120
	character.Stats["direction"] = 6
	character.Stats["area_state"] = 1
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}

	updated, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(),
		session,
		29,
		character,
	)
	if err != nil {
		t.Fatalf("persisted-town login must not block the login route: %v", err)
	}
	if townID != 41 || areaID != 2 || mapPath != "Cataclysm/Town/Alfhlyra/new_seria_room.map" {
		t.Fatalf("login landed town=%d area=%d map=%q, want town 41's own Seria room 41/2", townID, areaID, mapPath)
	}
	if row.Value1 != 733 || row.Value2 != 214 || row.Value3 != newCharacterInitialDirection ||
		row.Value4 != newCharacterInitialAreaState {
		t.Fatalf("login row=%+v, want the Seria room gate 733/214 with the room defaults", row)
	}
	if updated.Stats["town_id"] != 41 || updated.Stats["area_id"] != 2 ||
		updated.Stats["pos_x"] != 733 || updated.Stats["pos_y"] != 214 {
		t.Fatalf("returned login state was not the town-41 Seria room=%+v", updated.Stats)
	}
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found || stored.Stats["town_id"] != 41 || stored.Stats["area_id"] != 2 ||
		stored.Stats["pos_x"] != 733 || stored.Stats["pos_y"] != 214 ||
		stored.Stats["direction"] != newCharacterInitialDirection ||
		stored.Stats["area_state"] != newCharacterInitialAreaState {
		t.Fatalf("persisted login location found=%t err=%v stats=%+v", found, err, stored.Stats)
	}
	// prev-village 语义不变：原点仍然是"落点之外的、可持久化的非赛丽亚位置"，
	// 也就是上次真实所在的 41/0 @900/120，op1425 仍然可以回到那里。
	if !session.townPrevVillageSnapshot.PositionValid ||
		session.townPrevVillageSnapshot.CharacterID != 29 ||
		session.townPrevVillageSnapshot.TownID != 41 ||
		session.townPrevVillageSnapshot.AreaID != 0 ||
		session.townPrevVillageSnapshot.PositionX != 900 ||
		session.townPrevVillageSnapshot.PositionY != 120 {
		t.Fatalf("prev-village origin after town-Seria login=%+v", session.townPrevVillageSnapshot)
	}
	if stored.Stats[currentTownPrevVillageTownStat] != 41 ||
		stored.Stats[currentTownPrevVillageAreaStat] != 0 ||
		stored.Stats[currentTownPrevVillagePosXStat] != 900 ||
		stored.Stats[currentTownPrevVillagePosYStat] != 120 ||
		stored.Stats[currentTownPrevVillageDirectionStat] != 6 {
		t.Fatalf("persisted prev-village origin stats=%+v", stored.Stats)
	}
	text := readLog()
	for _, want := range []string{
		"branch=" + currentTownLoginBranchTownSeriaRoom,
		"persisted_town_id=41",
		" char_id=29 ",
		"location_source=" + currentTownLoginLocationTownSeriaSource,
		"position_source=runtime_pvf_seria_area_gate",
		"seria_fallback=false",
		"private_room_instantiation=true",
		"map_path=Cataclysm/Town/Alfhlyra/new_seria_room.map",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("town-41 login log missing %q:\n%s", want, text)
		}
	}
}

// 分支①的城镇 38 形态：上次在艾尔文防线的街道（38/0）→ 登录落点是 **38/1 的赛丽亚房间 gate**
// （同一个城镇的赛丽亚房间），而不是上次那条街。
func TestCharacterListLoginLandsInTownSeriaRoomInsteadOfPersistedStreetRoom(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	readLog := newRecordedPacketLog(t, service)

	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["town_id"] = newCharacterInitialTownID
	character.Stats["area_id"] = 0
	character.Stats["pos_x"] = 55
	character.Stats["pos_y"] = 267
	character.Stats["direction"] = 5
	character.Stats["area_state"] = 3
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}

	_, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(),
		session,
		29,
		character,
	)
	if err != nil {
		t.Fatal(err)
	}
	if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
		row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY ||
		!townMapPathLooksLikeSeria(mapPath) {
		t.Fatalf("town-38 login town=%d area=%d row=%+v map=%q, want the Seria room", townID, areaID, row, mapPath)
	}
	if !session.townPrevVillageSnapshot.PositionValid ||
		session.townPrevVillageSnapshot.TownID != newCharacterInitialTownID ||
		session.townPrevVillageSnapshot.AreaID != 0 ||
		session.townPrevVillageSnapshot.PositionX != 55 ||
		session.townPrevVillageSnapshot.PositionY != 267 {
		t.Fatalf("prev-village origin must keep the real previous street room=%+v", session.townPrevVillageSnapshot)
	}
	text := readLog()
	for _, want := range []string{
		"branch=" + currentTownLoginBranchTownSeriaRoom,
		"persisted_town_id=" + strconv.Itoa(newCharacterInitialTownID),
		"location_source=" + currentTownLoginLocationTownSeriaSource,
		"seria_fallback=false",
		"fallback_reason= ",
		"map_path=new_seria_room.map",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("town-38 login log missing %q:\n%s", want, text)
		}
	}
}

// 分支②默认被门控关闭（操作者 2026-09-26 报障「选择角色重新进入游戏会在上次下线的地方出现」；
// 更早的定稿条目是「重新登陆固定刷新在艾尔文防线的赛利亚房间」）：
// 该城镇在运行时 PVF 里没有赛丽亚房间（夹具里的 40 = 西海岸，只有街道与邮局）
// → **默认**不再沿用持久化精确落点，而是直接回落 38/1 的赛丽亚房间 gate，
// branch=elvengard_seria_fallback、seria_fallback=true。
func TestCharacterListLoginIgnoresPersistedTownPositionByDefault(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	readLog := newRecordedPacketLog(t, service)

	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	// 上次下线：城镇 40 的邮局 area 1，坐标 1016/189（= 该区域自己的 gate 落点）。
	character.Stats["town_id"] = 40
	character.Stats["area_id"] = 1
	character.Stats["pos_x"] = 1016
	character.Stats["pos_y"] = 189
	character.Stats["direction"] = 6
	character.Stats["area_state"] = 3
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}

	updated, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(),
		session,
		29,
		character,
	)
	if err != nil {
		t.Fatalf("default login must not block the route: %v", err)
	}
	if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
		row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY ||
		!townMapPathLooksLikeSeria(mapPath) {
		t.Fatalf(
			"default login town=%d area=%d row=%+v map=%q, want the 38/1 Seria room instead of the persisted 40/1 position",
			townID, areaID, row, mapPath,
		)
	}
	if updated.Stats["town_id"] != newCharacterInitialTownID ||
		updated.Stats["area_id"] != newCharacterInitialAreaID ||
		updated.Stats["pos_x"] != newCharacterInitialPosX ||
		updated.Stats["pos_y"] != newCharacterInitialPosY {
		t.Fatalf("default login did not persist the Seria room=%+v", updated.Stats)
	}
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found ||
		stored.Stats["town_id"] != newCharacterInitialTownID ||
		stored.Stats["area_id"] != newCharacterInitialAreaID ||
		stored.Stats["pos_x"] != newCharacterInitialPosX ||
		stored.Stats["pos_y"] != newCharacterInitialPosY {
		t.Fatalf("persisted default login location found=%t err=%v stats=%+v", found, err, stored.Stats)
	}
	text := readLog()
	for _, want := range []string{
		"branch=" + currentTownLoginBranchElvengardSeria,
		"persisted_town_id=40",
		"seria_fallback=true",
		"location_source=" + currentTownLoginLocationSeriaGateSource,
		"fallback_reason=persisted_town_position_gated_by_default_env_" + currentLoginPersistedTownEnv,
		"seria_room_reason=persisted_town_40_has_no_seria_room_in_runtime_pvf",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("default town-40 login log missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "branch="+currentTownLoginBranchPersistedPosition) {
		t.Fatalf("default login still reported the persisted-town branch:\n%s", text)
	}
}

// 门控开关：DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep 时恢复"留在上次所在城镇、沿用持久化精确落点"
// 的旧口径（落点、日志字段、写回字节与门控前一致）。其余取值一律回到默认口径。
func TestCharacterListLoginPersistedTownEnvSwitchKeepsTown(t *testing.T) {
	for _, envValue := range []string{
		currentLoginPersistedTownKeepValue,
		"on",
		"1",
		"true",
		"yes",
	} {
		t.Run("keeps_town_"+envValue, func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			readLog := newRecordedPacketLog(t, service)
			t.Setenv(currentLoginPersistedTownEnv, envValue)

			character, found, err := repositories.Character.Load(context.Background(), "29")
			if err != nil || !found {
				t.Fatalf("load character found=%t err=%v", found, err)
			}
			character.Stats["town_id"] = 40
			character.Stats["area_id"] = 1
			character.Stats["pos_x"] = 1016
			character.Stats["pos_y"] = 189
			character.Stats["direction"] = 6
			character.Stats["area_state"] = 3
			if err := repositories.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}

			updated, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
				context.Background(),
				session,
				29,
				character,
			)
			if err != nil {
				t.Fatal(err)
			}
			if townID != 40 || areaID != 1 || mapPath != "WestCoast_post.map" {
				t.Fatalf("town-40 login town=%d area=%d map=%q, want to stay in town 40", townID, areaID, mapPath)
			}
			if row.Value1 != 1016 || row.Value2 != 189 || row.Value3 != 6 || row.Value4 != 3 {
				t.Fatalf("town-40 login row=%+v, want the persisted exact position", row)
			}
			if updated.Stats["town_id"] != 40 || updated.Stats["area_id"] != 1 ||
				updated.Stats["pos_x"] != 1016 || updated.Stats["pos_y"] != 189 ||
				updated.Stats["direction"] != 6 {
				t.Fatalf("town-40 login rewrote the persisted room=%+v", updated.Stats)
			}
			text := readLog()
			for _, want := range []string{
				"branch=" + currentTownLoginBranchPersistedPosition,
				"persisted_town_id=40",
				" char_id=29 town_id=40 area_id=1 ",
				"location_source=" + currentTownLoginLocationPersistedSource,
				"position_source=character_persisted_position",
				"seria_fallback=false",
				"fallback_reason=persisted_position",
				"seria_room_reason=persisted_town_40_has_no_seria_room_in_runtime_pvf",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("town-40 login log missing %q:\n%s", want, text)
				}
			}
		})
	}

	// 默认（未设置）与任何其它取值都走默认口径：不留在持久化城镇。
	for _, envValue := range []string{"", "0", "false", "off", "no", "garbage"} {
		t.Run("defaults_when_"+envValue, func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			t.Setenv(currentLoginPersistedTownEnv, envValue)

			character, found, err := repositories.Character.Load(context.Background(), "29")
			if err != nil || !found {
				t.Fatalf("load character found=%t err=%v", found, err)
			}
			character.Stats["town_id"] = 40
			character.Stats["area_id"] = 1
			character.Stats["pos_x"] = 1016
			character.Stats["pos_y"] = 189
			character.Stats["direction"] = 6
			character.Stats["area_state"] = 3
			if err := repositories.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}

			_, townID, areaID, _, _, err := service.currentCharacterListLoginTransition(
				context.Background(),
				session,
				29,
				character,
			)
			if err != nil {
				t.Fatal(err)
			}
			if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID {
				t.Fatalf("env=%q town=%d area=%d, want the default 38/1 Seria room", envValue, townID, areaID)
			}
		})
	}
}

// 分支③：持久化信息不足以留在原城镇 → 回落既有的 38/1 赛丽亚房间，并写清原因。
//
// 这些子用例检验的是"第 2 级自己校验失败时的原因"，所以显式打开第 2 级的门控
// （DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep）；默认口径下第 2 级会被跳过、
// fallback_reason 变成门控原因，见 TestCharacterListLoginIgnoresPersistedTownPositionByDefault。
func TestCharacterListLoginFallsBackToInitialSeriaRoomAndLogsReason(t *testing.T) {
	t.Setenv(currentLoginPersistedTownEnv, currentLoginPersistedTownKeepValue)
	for _, test := range []struct {
		name             string
		mutate           func(stats map[string]int64)
		wantFallbackWord string
	}{
		{
			name:             "town id missing",
			mutate:           func(stats map[string]int64) { delete(stats, "town_id") },
			wantFallbackWord: "fallback_reason=persisted_town_id_missing",
		},
		{
			name:             "town id outside u8",
			mutate:           func(stats map[string]int64) { stats["town_id"] = 300 },
			wantFallbackWord: "fallback_reason=persisted_town_id_outside_u8",
		},
		{
			// 城镇 40 没有赛丽亚房间（分支②），但持久化区域也不存在 → 分支③。
			name:             "town without seria room and unknown area",
			mutate:           func(stats map[string]int64) { stats["town_id"] = 40; stats["area_id"] = 99 },
			wantFallbackWord: "fallback_reason=persisted_town_40_area_99_missing_from_runtime_pvf",
		},
		{
			name:             "town without seria room and no persisted position",
			mutate:           func(stats map[string]int64) { stats["town_id"] = 40; delete(stats, "pos_y") },
			wantFallbackWord: "fallback_reason=persisted_position_missing",
		},
		{
			name:             "unknown town and no persisted position",
			mutate:           func(stats map[string]int64) { stats["town_id"] = 99; stats["pos_x"] = 40000 },
			wantFallbackWord: "fallback_reason=persisted_position_outside_i16",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, session, repositories := newTownMoveTest(t)
			readLog := newRecordedPacketLog(t, service)

			character, found, err := repositories.Character.Load(context.Background(), "29")
			if err != nil || !found {
				t.Fatalf("load character found=%t err=%v", found, err)
			}
			// 起点：夹具里的城镇 39（有赛丽亚房间），保证"回落"不是因为没有别的选择。
			character.Stats["town_id"] = 39
			character.Stats["area_id"] = 0
			character.Stats["pos_x"] = 70
			character.Stats["pos_y"] = 80
			character.Stats["direction"] = 5
			character.Stats["area_state"] = 3
			test.mutate(character.Stats)
			if err := repositories.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}

			updated, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
				context.Background(),
				session,
				29,
				character,
			)
			if err != nil {
				t.Fatalf("fallback login must not block the route: %v", err)
			}
			if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
				row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY ||
				!townMapPathLooksLikeSeria(mapPath) {
				t.Fatalf("fallback login town=%d area=%d row=%+v map=%q", townID, areaID, row, mapPath)
			}
			if updated.Stats["town_id"] != newCharacterInitialTownID ||
				updated.Stats["area_id"] != newCharacterInitialAreaID ||
				updated.Stats["pos_x"] != newCharacterInitialPosX ||
				updated.Stats["pos_y"] != newCharacterInitialPosY {
				t.Fatalf("fallback login did not persist the fallback room=%+v", updated.Stats)
			}
			text := readLog()
			if !strings.Contains(text, "branch="+currentTownLoginBranchElvengardSeria) ||
				!strings.Contains(text, "location_source="+currentTownLoginLocationSeriaGateSource) ||
				!strings.Contains(text, "seria_fallback=true") ||
				!strings.Contains(text, test.wantFallbackWord) {
				t.Fatalf("fallback log missing expected fields %q:\n%s", test.wantFallbackWord, text)
			}
		})
	}
}

// 线上 A/B 开关：DNFBRIDGE_LOGIN_SERIA_FALLBACK=elvengard-seria 时无条件走 38/1，
// 即使该城镇有赛丽亚房间（默认关闭，不设置时行为不受影响）。
func TestCharacterListLoginForcedElvengardSeriaFallbackEnvSwitch(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	loadTownSeriaFixtureService(t, service, townMoveTestSource{
		"town/town.lst":          "38 `new_Elvengard.twn` 41 `new_Alfhlyra.twn`",
		"town/new_Elvengard.twn": townSeriaFixtureElvengard,
		"town/new_Alfhlyra.twn": "[area]\n0 `Cataclysm/Town/Alfhlyra/Alfhlyra_L.map` `[normal]`\n[/area]\n" +
			"[area]\n2 `Cataclysm/Town/Alfhlyra/new_seria_room.map` `[gate]` 733 214\n[/area]\n" +
			"[name]\n`Alfhlyra`",
	})

	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["town_id"] = 41
	character.Stats["area_id"] = 0
	character.Stats["pos_x"] = 900
	character.Stats["pos_y"] = 120
	character.Stats["direction"] = 6
	character.Stats["area_state"] = 1
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}

	// 默认关闭：仍然落在城镇 41 自己的赛丽亚房间。
	readDefaultLog := newRecordedPacketLog(t, service)
	_, townID, areaID, _, _, err := service.currentCharacterListLoginTransition(context.Background(), session, 29, character)
	if err != nil {
		t.Fatal(err)
	}
	if townID != 41 || areaID != 2 {
		t.Fatalf("default login town=%d area=%d, want 41/2 before the env switch is enabled", townID, areaID)
	}
	offText := readDefaultLog()
	if !strings.Contains(offText, "branch="+currentTownLoginBranchTownSeriaRoom) {
		t.Fatalf("default login did not use the town Seria room:\n%s", offText)
	}

	// 打开开关：强制 38/1（packetLogger 关闭后不再接受写入，所以换一个新日志器）。
	readForcedLog := newRecordedPacketLog(t, service)
	t.Setenv(currentLoginForcedSeriaFallbackEnv, currentLoginForcedSeriaFallbackValue)
	_, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(context.Background(), session, 29, character)
	if err != nil {
		t.Fatal(err)
	}
	if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
		row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY ||
		!townMapPathLooksLikeSeria(mapPath) {
		t.Fatalf("forced login town=%d area=%d row=%+v map=%q, want the Elvengard Seria gate", townID, areaID, row, mapPath)
	}
	forcedText := readForcedLog()
	if !strings.Contains(forcedText, "branch="+currentTownLoginBranchForcedElvengardSeria) ||
		!strings.Contains(forcedText, "location_source="+currentTownLoginLocationSeriaGateSource) ||
		!strings.Contains(forcedText, "fallback_reason=forced_by_env_"+currentLoginForcedSeriaFallbackEnv) {
		t.Fatalf("forced login log missing the A/B switch fields:\n%s", forcedText)
	}
}

// 回落落点本身必须来自运行时 PVF：38/1 没有 gate 时登录路径直接失败，不允许编坐标。
func TestCharacterListLoginRequiresInitialSeriaGateInRuntimePVF(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	loadTownSeriaFixtureService(t, service, townMoveTestSource{
		"town/town.lst":          "38 `new_Elvengard.twn`",
		"town/new_Elvengard.twn": "[area]\n0 `new_Elvengard.map` `[normal]`\n[/area]\n[name]\n`Elvengard`",
	})
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if _, _, _, _, _, err := service.currentCharacterListLoginTransition(
		context.Background(), session, 29, character,
	); err == nil {
		t.Fatal("login without a runtime-PVF 38/1 Seria gate unexpectedly succeeded")
	}
}

func TestCurrentPersistedTownLoginTownIDRejectsMissingOrInvalidTown(t *testing.T) {
	valid := func() dnfrepo.CharacterRecord {
		return dnfrepo.CharacterRecord{
			CharacterID: "29",
			Stats:       map[string]int64{"town_id": 41},
		}
	}
	if townID, ok, reason := currentPersistedTownLoginTownID(valid()); !ok || reason != "" || townID != 41 {
		t.Fatalf("valid persisted town=%d ok=%t reason=%q", townID, ok, reason)
	}
	if _, ok, reason := currentPersistedTownLoginTownID(dnfrepo.CharacterRecord{Stats: nil}); ok ||
		reason != "selected_character_stats_missing" {
		t.Fatalf("nil stats ok=%t reason=%q", ok, reason)
	}
	for _, test := range []struct {
		name   string
		mutate func(stats map[string]int64)
		reason string
	}{
		{name: "missing", mutate: func(stats map[string]int64) { delete(stats, "town_id") }, reason: "persisted_town_id_missing"},
		{name: "above u8", mutate: func(stats map[string]int64) { stats["town_id"] = 256 }, reason: "persisted_town_id_outside_u8"},
		{name: "negative", mutate: func(stats map[string]int64) { stats["town_id"] = -1 }, reason: "persisted_town_id_outside_u8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := valid()
			test.mutate(record.Stats)
			if _, ok, reason := currentPersistedTownLoginTownID(record); ok || reason != test.reason {
				t.Fatalf("ok=%t reason=%q want %q", ok, reason, test.reason)
			}
		})
	}
}

// 分支②用的持久化落点解析器仍然按运行时 PVF 严格校验（这是两轮前已被确认可用的行为）。
func TestCurrentPersistedTownLoginLocationRejectsMissingOrInvalidStats(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	valid := func() dnfrepo.CharacterRecord {
		return dnfrepo.CharacterRecord{
			CharacterID: "29",
			Stats: map[string]int64{
				"town_id":    39,
				"area_id":    0,
				"pos_x":      70,
				"pos_y":      80,
				"direction":  5,
				"area_state": 3,
			},
		}
	}

	location, ok, reason := service.currentPersistedTownLoginLocation(29, valid())
	if !ok || reason != "" || location.TownID != 39 || location.AreaID != 0 ||
		location.PositionX != 70 || location.PositionY != 80 ||
		location.Direction != 5 || location.AreaState != 3 ||
		location.MapPath != "new_HendonMyre.map" ||
		location.Source != currentTownLoginLocationPersistedSource ||
		location.Branch != currentTownLoginBranchPersistedPosition {
		t.Fatalf("valid persisted location=%+v ok=%t reason=%q", location, ok, reason)
	}

	if _, ok, reason := service.currentPersistedTownLoginLocation(0, valid()); ok ||
		reason != "selected_character_stats_missing" {
		t.Fatalf("zero character id ok=%t reason=%q", ok, reason)
	}
	if _, ok, reason := service.currentPersistedTownLoginLocation(29, dnfrepo.CharacterRecord{Stats: nil}); ok ||
		reason != "selected_character_stats_missing" {
		t.Fatalf("nil stats ok=%t reason=%q", ok, reason)
	}

	for _, test := range []struct {
		key      string
		value    int64
		deleteIt bool
		reason   string
	}{
		{key: "town_id", deleteIt: true, reason: "persisted_town_id_missing"},
		{key: "town_id", value: 256, reason: "persisted_town_id_outside_u8"},
		{key: "town_id", value: -1, reason: "persisted_town_id_outside_u8"},
		{key: "area_id", deleteIt: true, reason: "persisted_area_id_missing"},
		{key: "area_id", value: 300, reason: "persisted_area_id_outside_u8"},
		{key: "pos_x", deleteIt: true, reason: "persisted_position_missing"},
		{key: "pos_y", value: 40000, reason: "persisted_position_outside_i16"},
		{key: "pos_y", value: -40000, reason: "persisted_position_outside_i16"},
		{key: "direction", deleteIt: true, reason: "persisted_direction_missing_or_outside_u8"},
		{key: "direction", value: 256, reason: "persisted_direction_missing_or_outside_u8"},
		{key: "area_state", deleteIt: true, reason: "persisted_area_state_missing_or_outside_u8"},
		{key: "area_state", value: 999, reason: "persisted_area_state_missing_or_outside_u8"},
		{key: "area_id", value: 99, reason: "persisted_town_39_area_99_missing_from_runtime_pvf"},
	} {
		record := valid()
		if test.deleteIt {
			delete(record.Stats, test.key)
		} else {
			record.Stats[test.key] = test.value
		}
		if _, ok, reason := service.currentPersistedTownLoginLocation(29, record); ok || reason != test.reason {
			t.Fatalf("key=%s value=%d ok=%t reason=%q want %q", test.key, test.value, ok, reason, test.reason)
		}
	}
}

// 城镇赛丽亚房间的解析与落点：有 gate 用 gate，没有 gate 用该 area 自己声明的默认落点
// （`[normal]` 后直接跟两个整数），两者都没有就判定为"没有落点"（于是进入分支②）。
func TestCurrentTownSeriaRoomResolvesGateOrDeclaredDefaultPosition(t *testing.T) {
	service, _, _ := newTownMoveTest(t)
	loadTownSeriaFixtureService(t, service, townMoveTestSource{
		"town/town.lst":          "38 `new_Elvengard.twn` 41 `new_Alfhlyra.twn` 42 `new_Underfoot.twn` 43 `new_StormPass.twn`",
		"town/new_Elvengard.twn": townSeriaFixtureElvengard,
		"town/new_Alfhlyra.twn": "[area]\n0 `Cataclysm/Town/Alfhlyra/Alfhlyra_L.map` `[normal]`\n[/area]\n" +
			"[area]\n2 `Cataclysm/Town/Alfhlyra/new_seria_room.map` `[gate]` 733 214\n[/area]\n" +
			"[name]\n`Alfhlyra`",
		// 只有默认落点、没有 gate 的赛丽亚房间（`[normal]` 后直接跟坐标）。
		"town/new_Underfoot.twn": "[area]\n0 `Cataclysm/Town/Underfoot/Underfoot_north.map` `[normal]`\n[/area]\n" +
			"[area]\n1 `Cataclysm/Town/Underfoot/new_seria_room.map` `[normal]` 594 234\n[/area]\n" +
			"[name]\n`Underfoot`",
		// 只有 `[normal]` 标记、没有任何坐标的赛丽亚房间：没有落点。
		"town/new_StormPass.twn": "[area]\n0 `Cataclysm/Town/StormPass/new_seria_room.map` `[normal]`\n[/area]\n" +
			"[name]\n`StormPass`",
	})

	room, found := service.currentTownSeriaRoomArea(42)
	if !found || room.ID != 1 || room.MapPath != "Cataclysm/Town/Underfoot/new_seria_room.map" ||
		room.DefaultPosition == nil || room.DefaultPosition.X != 594 || room.DefaultPosition.Y != 234 ||
		room.Gate != nil {
		t.Fatalf("default-position Seria room=%+v found=%t", room, found)
	}
	location, ok, reason := service.currentTownSeriaRoomLoginLocation(42)
	if !ok || reason != "" || location.TownID != 42 || location.AreaID != 1 ||
		location.PositionX != 594 || location.PositionY != 234 ||
		location.PositionSource != "runtime_pvf_seria_area_default_position" ||
		location.Source != currentTownLoginLocationTownSeriaSource ||
		location.Branch != currentTownLoginBranchTownSeriaRoom {
		t.Fatalf("default-position login location=%+v ok=%t reason=%q", location, ok, reason)
	}

	room, found = service.currentTownSeriaRoomArea(38)
	if !found || room.ID != newCharacterInitialAreaID || room.Gate == nil ||
		room.Gate.X != newCharacterInitialPosX || room.Gate.Y != newCharacterInitialPosY {
		t.Fatalf("town 38 Seria room=%+v found=%t", room, found)
	}

	location, ok, reason = service.currentTownSeriaRoomLoginLocation(41)
	if !ok || reason != "" || location.TownID != 41 || location.AreaID != 2 ||
		location.PositionX != 733 || location.PositionY != 214 ||
		location.PositionSource != "runtime_pvf_seria_area_gate" {
		t.Fatalf("town 41 Seria room location=%+v ok=%t reason=%q", location, ok, reason)
	}

	if _, found := service.currentTownSeriaRoomArea(99); found {
		t.Fatal("unknown town unexpectedly resolved a Seria room")
	}
	if _, ok, reason := service.currentTownSeriaRoomLoginLocation(43); ok ||
		reason != "persisted_town_43_seria_area_0_has_no_landing_position" {
		t.Fatalf("Seria room without a landing position ok=%t reason=%q", ok, reason)
	}
}
