package dnfbridge

import (
	"context"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住 `DNFBRIDGE_LOGIN_FALLBACK_KEEPS_TOWN`：
// **第三级强制回退落地**（`elvengard_seria_fallback` / `forced_elvengard_seria`）
// 默认照旧把回退落点写回 `dnf_characters`；只有显式 `preserve` 才保留持久化真值。
//
// 现场缺陷（线上实测，2026-09-27）：
//
//	conn game-000016 char_id=3 `branch=elvengard_seria_fallback persisted_town_id=11
//	town_id=38 area_id=1 position_x=450 position_y=234 … persisted=true`
//	⇒ 玩家在素喃（town 11）下线，回退落地把 `town_id` 从 11 改成 38；此后第一级永远命中 38、
//	第二级再也轮不到（`DNFBRIDGE_LOGIN_PERSISTED_TOWN=keep` 形同虚设）。
//	厂商对拍见 `initial_town_login_owner.go` 里 `currentLoginFallbackKeepsTownEnv` 的注释。
//
// **默认档就是改动前行为**：既有用例
// `TestCharacterListLoginIgnoresPersistedTownPositionByDefault`（默认档必须写回 38/1）
// 的期望值一个都没改，本文件只新增 preserve 档的正面证据与"第一级/第二级/op36 不受影响"。

// TestLoginFallbackKeepsTownSwitchOnlyAcceptsExplicitPreserve 钉住开关语义：
// **只有显式 `preserve`** 才生效；其余任何取值（含 `on`/`true`/`1`）都是默认档。
func TestLoginFallbackKeepsTownSwitchOnlyAcceptsExplicitPreserve(t *testing.T) {
	for _, value := range []string{"", "   ", "off", "on", "1", "true", "yes", "keep", "strict"} {
		t.Run("value_"+strconv.Quote(value), func(t *testing.T) {
			t.Setenv(currentLoginFallbackKeepsTownEnv, value)
			if currentLoginFallbackKeepsTown() {
				t.Fatalf("%s=%q must stay on the default tier", currentLoginFallbackKeepsTownEnv, value)
			}
		})
	}
	for _, value := range []string{"preserve", "PRESERVE", "  Preserve  "} {
		t.Run("value_"+strconv.Quote(value), func(t *testing.T) {
			t.Setenv(currentLoginFallbackKeepsTownEnv, value)
			if !currentLoginFallbackKeepsTown() {
				t.Fatalf("%s=%q must enable the preserve tier", currentLoginFallbackKeepsTownEnv, value)
			}
		})
	}
}

// TestLoginFallbackBranchPredicateOnlyMatchesTheThirdLevel 钉住"只对第三级回退生效"：
// 判据是本次落点的 branch，**不是坐标**。
func TestLoginFallbackBranchPredicateOnlyMatchesTheThirdLevel(t *testing.T) {
	for _, branch := range []string{
		currentTownLoginBranchElvengardSeria,
		currentTownLoginBranchForcedElvengardSeria,
	} {
		if !currentTownLoginBranchIsForcedFallback(branch) {
			t.Fatalf("branch %q must be treated as the forced fallback", branch)
		}
	}
	for _, branch := range []string{
		currentTownLoginBranchTownSeriaRoom,
		currentTownLoginBranchPersistedPosition,
		"",
		"something_else",
	} {
		if currentTownLoginBranchIsForcedFallback(branch) {
			t.Fatalf("branch %q must NOT be treated as the forced fallback", branch)
		}
	}
}

// loginFallbackFixture 造出"回退档"输入：上次下线在城镇 40（夹具 PVF 里**没有**赛丽亚房间）
// 的邮局 area 1 坐标 1016/189 —— 与既有默认档用例同一份夹具与同一组数字。
func loginFallbackFixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	service, session, repositories := newTownMoveTest(t)
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
	return service, session, repositories
}

func loadStoredCharacter(t *testing.T, repositories dnfrepo.Group) dnfrepo.CharacterRecord {
	t.Helper()
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("reload character found=%t err=%v", found, err)
	}
	return stored
}

// (b) 回退档 + 默认/别名取值：列被写成回退落点（逐字节今天）。
func TestLoginFallbackDefaultStillOverwritesPersistedColumns(t *testing.T) {
	for _, value := range []string{"", "on", "true", "1", "off"} {
		t.Run("env_"+strconv.Quote(value), func(t *testing.T) {
			service, session, repositories := loginFallbackFixture(t)
			readLog := newRecordedPacketLog(t, service)
			t.Setenv(currentLoginFallbackKeepsTownEnv, value)

			character := loadStoredCharacter(t, repositories)
			if _, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
				context.Background(), session, 29, character,
			); err != nil {
				t.Fatalf("login must not block the route: %v", err)
			} else if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
				row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY ||
				!townMapPathLooksLikeSeria(mapPath) {
				t.Fatalf("landing town=%d area=%d row=%+v map=%q, want the 38/1 fallback", townID, areaID, row, mapPath)
			}

			stored := loadStoredCharacter(t, repositories)
			if stored.Stats["town_id"] != newCharacterInitialTownID ||
				stored.Stats["area_id"] != newCharacterInitialAreaID ||
				stored.Stats["pos_x"] != newCharacterInitialPosX ||
				stored.Stats["pos_y"] != newCharacterInitialPosY {
				t.Fatalf("default tier must still persist the fallback landing: %+v", stored.Stats)
			}
			text := readLog()
			for _, want := range []string{
				"branch=" + currentTownLoginBranchElvengardSeria,
				"persisted_town_id=40",
				"fallback_kept_persisted_town=false",
				"kept_persisted_location_bound=false",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("default tier log missing %q:\n%s", want, text)
				}
			}
		})
	}
}

// (a) 回退档 + preserve：回退落地后 `dnf_characters` 的四列**一个都不变**，
// 日志给出可 grep 的"本次没有覆盖"字段与被保留下来的值；
// 而**本次会话仍然落在 38/1**（op24 的 town/area/pos/map 与默认档逐字节一致）。
func TestLoginFallbackPreserveKeepsPersistedColumns(t *testing.T) {
	service, session, repositories := loginFallbackFixture(t)
	readLog := newRecordedPacketLog(t, service)
	t.Setenv(currentLoginFallbackKeepsTownEnv, currentLoginFallbackKeepsTownValue)

	character := loadStoredCharacter(t, repositories)
	updated, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(), session, 29, character,
	)
	if err != nil {
		t.Fatalf("preserve tier must not block the route: %v", err)
	}

	// 会话落点与默认档完全一致：仍然是 38/1 的赛丽亚房间。
	if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
		row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY ||
		row.Value3 != newCharacterInitialDirection || row.Value4 != newCharacterInitialAreaState ||
		!townMapPathLooksLikeSeria(mapPath) {
		t.Fatalf("preserve tier landing town=%d area=%d row=%+v map=%q, want the unchanged 38/1 fallback",
			townID, areaID, row, mapPath)
	}
	// 返回给调用方的内存记录也必须是回退落点（下游场景/角色对象投影与默认档同形）。
	if updated.Stats["town_id"] != newCharacterInitialTownID ||
		updated.Stats["area_id"] != newCharacterInitialAreaID ||
		updated.Stats["pos_x"] != newCharacterInitialPosX ||
		updated.Stats["pos_y"] != newCharacterInitialPosY {
		t.Fatalf("preserve tier session record=%+v, want the fallback landing", updated.Stats)
	}

	// **库里一个字段都没被覆盖。**
	stored := loadStoredCharacter(t, repositories)
	if stored.Stats["town_id"] != 40 || stored.Stats["area_id"] != 1 ||
		stored.Stats["pos_x"] != 1016 || stored.Stats["pos_y"] != 189 ||
		stored.Stats["direction"] != 6 || stored.Stats["area_state"] != 3 {
		t.Fatalf("preserve tier overwrote the persisted location: %+v", stored.Stats)
	}

	text := readLog()
	for _, want := range []string{
		"branch=" + currentTownLoginBranchElvengardSeria,
		"persisted_town_id=40",
		"fallback_kept_persisted_town=true",
		"kept_persisted_location_bound=true",
		"kept_town_id=40",
		"kept_area_id=1",
		"kept_position_x=1016",
		"kept_position_y=189",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("preserve tier log missing %q:\n%s", want, text)
		}
	}
}

// (c) 第一级 / 第二级落点在**任何档位**下都照旧写库：
// `preserve` 只认第三级回退的 branch，绝不影响正常持久化。
func TestLoginFirstAndSecondLevelAlwaysPersistRegardlessOfFallbackSwitch(t *testing.T) {
	t.Run("first_level_town_seria_room", func(t *testing.T) {
		service, session, repositories := newTownMoveTest(t)
		readLog := newRecordedPacketLog(t, service)
		t.Setenv(currentLoginFallbackKeepsTownEnv, currentLoginFallbackKeepsTownValue)

		// 城镇 38 **有**赛丽亚房间 ⇒ 第一级；持久化落点先设成主街 38/0，
		// 这样"写库有没有发生"可观测（38/0 → 38/1）。
		character := loadStoredCharacter(t, repositories)
		character.Stats["town_id"] = 38
		character.Stats["area_id"] = 0
		character.Stats["pos_x"] = 622
		character.Stats["pos_y"] = 196
		character.Stats["direction"] = 0
		character.Stats["area_state"] = 3
		if err := repositories.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}

		if _, townID, areaID, _, _, err := service.currentCharacterListLoginTransition(
			context.Background(), session, 29, character,
		); err != nil {
			t.Fatal(err)
		} else if townID != 38 || areaID != 1 {
			t.Fatalf("first level town=%d area=%d, want 38/1", townID, areaID)
		}

		stored := loadStoredCharacter(t, repositories)
		if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 ||
			stored.Stats["pos_x"] != 450 || stored.Stats["pos_y"] != 234 {
			t.Fatalf("first level must still persist under preserve: %+v", stored.Stats)
		}
		text := readLog()
		if !strings.Contains(text, "branch="+currentTownLoginBranchTownSeriaRoom) ||
			!strings.Contains(text, "fallback_kept_persisted_town=false") {
			t.Fatalf("first level log wrong under preserve:\n%s", text)
		}
	})

	t.Run("second_level_persisted_position", func(t *testing.T) {
		service, session, repositories := newTownMoveTest(t)
		readLog := newRecordedPacketLog(t, service)
		t.Setenv(currentLoginFallbackKeepsTownEnv, currentLoginFallbackKeepsTownValue)
		t.Setenv(currentLoginPersistedTownEnv, currentLoginPersistedTownKeepValue)

		character := loadStoredCharacter(t, repositories)
		character.Stats["town_id"] = 40
		character.Stats["area_id"] = 1
		character.Stats["pos_x"] = 1016
		character.Stats["pos_y"] = 189
		character.Stats["direction"] = 6
		character.Stats["area_state"] = 3
		if err := repositories.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}

		if _, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
			context.Background(), session, 29, character,
		); err != nil {
			t.Fatal(err)
		} else if townID != 40 || areaID != 1 || row.Value1 != 1016 || row.Value2 != 189 ||
			mapPath != "WestCoast_post.map" {
			t.Fatalf("second level town=%d area=%d row=%+v map=%q, want the persisted 40/1 exact position",
				townID, areaID, row, mapPath)
		}

		stored := loadStoredCharacter(t, repositories)
		if stored.Stats["town_id"] != 40 || stored.Stats["area_id"] != 1 ||
			stored.Stats["pos_x"] != 1016 || stored.Stats["pos_y"] != 189 {
			t.Fatalf("second level must keep the persisted location: %+v", stored.Stats)
		}
		text := readLog()
		if !strings.Contains(text, "branch="+currentTownLoginBranchPersistedPosition) ||
			!strings.Contains(text, "fallback_kept_persisted_town=false") {
			t.Fatalf("second level log wrong under preserve:\n%s", text)
		}
	})

	t.Run("second_level_unusable_still_uses_the_third_level_gate", func(t *testing.T) {
		// 持久化区域在运行时 PVF 里不存在 ⇒ 第二级解析失败 ⇒ 仍然走第三级；
		// preserve 档在这一支**照旧生效**（判据是 branch，不是失败原因）。
		service, session, repositories := newTownMoveTest(t)
		readLog := newRecordedPacketLog(t, service)
		t.Setenv(currentLoginFallbackKeepsTownEnv, currentLoginFallbackKeepsTownValue)
		t.Setenv(currentLoginPersistedTownEnv, currentLoginPersistedTownKeepValue)

		character := loadStoredCharacter(t, repositories)
		character.Stats["town_id"] = 40
		character.Stats["area_id"] = 9
		character.Stats["pos_x"] = 1016
		character.Stats["pos_y"] = 189
		if err := repositories.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}

		if _, townID, areaID, _, _, err := service.currentCharacterListLoginTransition(
			context.Background(), session, 29, character,
		); err != nil {
			t.Fatal(err)
		} else if townID != 38 || areaID != 1 {
			t.Fatalf("unusable second level town=%d area=%d, want the 38/1 fallback", townID, areaID)
		}
		stored := loadStoredCharacter(t, repositories)
		if stored.Stats["town_id"] != 40 || stored.Stats["area_id"] != 9 {
			t.Fatalf("preserve tier must keep the unusable persisted town too: %+v", stored.Stats)
		}
		text := readLog()
		if !strings.Contains(text, "fallback_kept_persisted_town=true") ||
			!strings.Contains(text, "kept_area_id=9") {
			t.Fatalf("third-level gate log wrong for an unusable persisted area:\n%s", text)
		}
	})
}

// (d) 玩家在会话里**自己过图**（op36 / `handleTownSetUserArea`）与登录回退无关，
// 在任何档位下都必须照旧写库。
func TestPlayerTownMoveStillPersistsUnderFallbackPreserve(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	t.Setenv(currentLoginFallbackKeepsTownEnv, currentLoginFallbackKeepsTownValue)

	// 从 38/1 走到 38/0（同城镇普通过图），夹具里 town_id=38 == 请求城镇 ⇒ 不触发跨城镇判据。
	if err := service.handleTownSetUserArea(session, buildTownMoveRequest(38, 0, 900, 250, 5)); err != nil {
		t.Fatal(err)
	}
	stored := loadStoredCharacter(t, repositories)
	if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 900 || stored.Stats["pos_y"] != 250 ||
		stored.Stats["direction"] != 5 {
		t.Fatalf("op36 must still persist under preserve: %+v", stored.Stats)
	}
}
