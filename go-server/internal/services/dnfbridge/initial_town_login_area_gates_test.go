package dnfbridge

import (
	"context"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// 登录落点的区域门槛门控（DNFBRIDGE_LOGIN_AREA_GATES，见 initial_town_login_area_gates.go）：
//
//	relaxed（默认）→ 不评估；log → 评估并记录但不改落点；strict → 评估并把被拒的落点
//	重定向到既有的 38/1 艾尔文防线赛丽亚房间。
//
// 运行时 PVF 的实测例：赫顿玛尔 town 39 / area 0 声明 `[need level] 17` + `[need quest] 3156`，
// Lv11 的角色不该落进去。下面的服务级用例用同样的形状（夹具城镇 40 的 area 1 加 `[need level] 17`、
// 角色等级 11）钉住 strict 的重定向与 relaxed 的逐字节不变。

// packetLogger 把最后一个字段的空值写成不带尾随空格的 `fallback=`（见上面 log 子用例的日志），
// 所以"空值"要用整串 token 匹配，不能用 `fallback= ` 这种带尾随空格的子串。
func loginAreaGateLogHasField(text, field, value string) bool {
	for _, token := range strings.Fields(text) {
		if token == field+"="+value {
			return true
		}
	}
	return false
}

// loginAreaGateLogFieldValue 取日志文本里第一个 `field=value` 的值。
// 这里用到的字段（分支名/来源名/原因串）本身不含空白，按空白切词足够。
func loginAreaGateLogFieldValue(text, field string) (string, bool) {
	for _, token := range strings.Fields(text) {
		if value, found := strings.CutPrefix(token, field+"="); found {
			return value, true
		}
	}
	return "", false
}

// 纯函数：`[need level]` 规则。need=0/缺失 → 不拒；level >= need → 不拒；level < need → 拒。
func TestLoginAreaGateLevelDenyReason(t *testing.T) {
	for _, test := range []struct {
		name           string
		minLevel       int64
		characterLevel int64
		wantReason     string
		wantDenied     bool
	}{
		{name: "no requirement declared", minLevel: 0, characterLevel: 1},
		{name: "no requirement declared high level", minLevel: 0, characterLevel: 60},
		{name: "level above requirement", minLevel: 17, characterLevel: 18},
		{name: "level equal to requirement", minLevel: 17, characterLevel: 17},
		{name: "level one below requirement", minLevel: 17, characterLevel: 16, wantReason: "level=16<17", wantDenied: true},
		{name: "level far below requirement", minLevel: 17, characterLevel: 11, wantReason: "level=11<17", wantDenied: true},
		{name: "requirement of one", minLevel: 1, characterLevel: 0, wantReason: "level=0<1", wantDenied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reason, denied := loginAreaGateLevelDenyReason(
				dnftown.Area{ID: 0, MinLevel: test.minLevel},
				test.characterLevel,
			)
			if denied != test.wantDenied || reason != test.wantReason {
				t.Fatalf(
					"minLevel=%d characterLevel=%d reason=%q denied=%t, want reason=%q denied=%t",
					test.minLevel, test.characterLevel, reason, denied, test.wantReason, test.wantDenied,
				)
			}
		})
	}
}

// 开关取值解析：strict/enforce/pvf → strict；log/dry-run → log；其余（含空值、垃圾值）→ relaxed。
func TestCurrentLoginAreaGatesMode(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: currentLoginAreaGatesRelaxed},
		{value: "relaxed", want: currentLoginAreaGatesRelaxed},
		{value: "RELAXED", want: currentLoginAreaGatesRelaxed},
		{value: "  relaxed  ", want: currentLoginAreaGatesRelaxed},
		{value: "off", want: currentLoginAreaGatesRelaxed},
		{value: "0", want: currentLoginAreaGatesRelaxed},
		{value: "garbage", want: currentLoginAreaGatesRelaxed},
		{value: "log", want: currentLoginAreaGatesLog},
		{value: "LOG", want: currentLoginAreaGatesLog},
		{value: "dry-run", want: currentLoginAreaGatesLog},
		{value: "Dry-Run", want: currentLoginAreaGatesLog},
		{value: "strict", want: currentLoginAreaGatesStrict},
		{value: "STRICT", want: currentLoginAreaGatesStrict},
		{value: "enforce", want: currentLoginAreaGatesStrict},
		{value: "pvf", want: currentLoginAreaGatesStrict},
		{value: " strict ", want: currentLoginAreaGatesStrict},
	} {
		t.Run("value_"+test.value, func(t *testing.T) {
			t.Setenv(currentLoginAreaGatesEnv, test.value)
			if mode := currentLoginAreaGatesMode(); mode != test.want {
				t.Fatalf("env=%q mode=%q, want %q", test.value, mode, test.want)
			}
		})
	}
}

// 服务级：被评估的落点必须来自运行时 PVF 的 town 表；查不到该 (town, area) 时 evaluated=false（放行）。
func TestCurrentLoginAreaGateDenyReasonEvaluatesRuntimePVFArea(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)

	area, found := service.townArea(40, 1)
	if !found {
		t.Fatal("fixture town 40 area 1 is missing from the runtime PVF town table")
	}
	// 夹具的 40/1 没有 `[need level]`、也没有 `[need quest]`：两条规则都通过。
	if reason, evaluated := service.currentLoginAreaGateDenyReason(
		context.Background(), session, repositories, 29, 40, area, 1,
	); !evaluated || reason != "" {
		t.Fatalf("ungated area reason=%q evaluated=%t, want no deny", reason, evaluated)
	}

	// 区域门槛为纯数据判定：把同一个区域改成 `[need level] 17` 就必然被拒。
	gated := area
	gated.MinLevel = 17
	if reason, evaluated := service.currentLoginAreaGateDenyReason(
		context.Background(), session, repositories, 29, 40, gated, 11,
	); !evaluated || reason != "level=11<17" {
		t.Fatalf("level-gated area reason=%q evaluated=%t, want level=11<17", reason, evaluated)
	}

	// 任务仓储不可用（dnfrepo.Group{} 没有 Quest）时必须 **fail-open**：登录不可重试，
	// 宁可放行也不让操作者进不去游戏。
	questGated := area
	questGated.NeedQuests = []int64{3156}
	if reason, evaluated := service.currentLoginAreaGateDenyReason(
		context.Background(), session, dnfrepo.Group{}, 29, 40, questGated, 60,
	); !evaluated || reason != "" {
		t.Fatalf("missing quest repository reason=%q evaluated=%t, want fail-open", reason, evaluated)
	}

	// 有任务仓储、但该任务未完成 → 稳定的可 grep 原因。
	if reason, evaluated := service.currentLoginAreaGateDenyReason(
		context.Background(), session, repositories, 29, 40, questGated, 60,
	); !evaluated || reason != "quest=3156_not_cleared" {
		t.Fatalf("uncleared quest reason=%q evaluated=%t, want quest=3156_not_cleared", reason, evaluated)
	}
}

// 评估器只吃"已解析的区域"：调用方（currentCharacterListLoginTransition）先用 s.townArea
// 命中判定，没有命中的 (town, area) 就根本不评估（evaluated=false，放行）。
func TestCurrentLoginAreaGateDenyReasonLeavesUngatedAreasAlone(t *testing.T) {
	service, session, repositories := newTownMoveTest(t)
	if _, found := service.townArea(40, 99); found {
		t.Fatal("fixture unexpectedly declares town 40 area 99")
	}
	if reason, evaluated := service.currentLoginAreaGateDenyReason(
		context.Background(), session, repositories, 29, 40, dnftown.Area{ID: 99, MapPath: "not_in_pvf.map"}, 1,
	); !evaluated || reason != "" {
		t.Fatalf("plan-less area reason=%q evaluated=%t, want no deny", reason, evaluated)
	}
}

// 服务级重定向：夹具给城镇 40 的 area 1（西海岸邮局）加上 `[need level] 17`，
// 角色等级 11 且持久化落点就是 40/1。
//
//   - relaxed（默认）：落点与门控前逐字节一致（留在 40/1），日志里没有任何 gate 事件。
//   - log：评估出被拒、打 game-login-area-gate-denied，但落点仍然不变。
//   - strict：落点被换成既有的 38/1 赛丽亚房间 gate（450/234），并且是**被持久化**的那个；
//     日志里 fallback=elvengard_seria。
func TestCharacterListLoginAreaGateRedirectsGatedPersistedLanding(t *testing.T) {
	const gatedMinLevel = 17
	const characterLevel = 11
	loadService := func(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
		t.Helper()
		// 第 2 级（持久化精确落点）默认被 DNFBRIDGE_LOGIN_PERSISTED_TOWN 门控关闭，
		// 用例要先打开它才能构造"持久化落点本身撞门槛"这个现场。
		t.Setenv(currentLoginPersistedTownEnv, currentLoginPersistedTownKeepValue)
		service, session, repositories := newTownMoveTest(t)
		loadTownSeriaFixtureService(t, service, townMoveTestSource{
			"town/town.lst":          "38 `new_Elvengard.twn` 40 `new_WestCoast.twn`",
			"town/new_Elvengard.twn": townSeriaFixtureElvengard,
			// 与运行时 PVF 的赫顿玛尔 39/0 同一形状：`[need level]` 声明在 area 块里。
			"town/new_WestCoast.twn": "[area]\n1 `WestCoast_post.map`\n[need level]\n17\n[/need level]\n`[gate]` 1016 189\n[/area]\n" +
				"[name]\n`WestCoast`",
		})
		character, found, err := repositories.Character.Load(context.Background(), "29")
		if err != nil || !found {
			t.Fatalf("load character found=%t err=%v", found, err)
		}
		character.Level = characterLevel
		character.Stats["town_id"] = 40
		character.Stats["area_id"] = 1
		character.Stats["pos_x"] = 1016
		character.Stats["pos_y"] = 189
		character.Stats["direction"] = 6
		character.Stats["area_state"] = 3
		if err := repositories.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		area, found := service.townArea(40, 1)
		if !found || area.MinLevel != gatedMinLevel {
			t.Fatalf("fixture town 40 area 1 min_level=%d found=%t, want %d", area.MinLevel, found, gatedMinLevel)
		}
		return service, session, repositories
	}
	loadCharacter := func(t *testing.T, repositories dnfrepo.Group) dnfrepo.CharacterRecord {
		t.Helper()
		character, found, err := repositories.Character.Load(context.Background(), "29")
		if err != nil || !found {
			t.Fatalf("reload character found=%t err=%v", found, err)
		}
		return character
	}

	// relaxed（默认）：不评估、不改落点、不打 gate 事件。
	t.Run("relaxed_keeps_the_persisted_landing", func(t *testing.T) {
		service, session, repositories := loadService(t)
		readLog := newRecordedPacketLog(t, service)
		updated, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
			context.Background(), session, 29, loadCharacter(t, repositories),
		)
		if err != nil {
			t.Fatalf("relaxed login must not block the route: %v", err)
		}
		if townID != 40 || areaID != 1 || mapPath != "WestCoast_post.map" {
			t.Fatalf("relaxed login town=%d area=%d map=%q, want the persisted 40/1 landing", townID, areaID, mapPath)
		}
		if row.Value1 != 1016 || row.Value2 != 189 || row.Value3 != 6 || row.Value4 != 3 {
			t.Fatalf("relaxed login row=%+v, want the persisted exact position", row)
		}
		if updated.Stats["town_id"] != 40 || updated.Stats["area_id"] != 1 {
			t.Fatalf("relaxed login state=%+v, want the persisted 40/1 landing", updated.Stats)
		}
		text := readLog()
		if !strings.Contains(text, "branch="+currentTownLoginBranchPersistedPosition) ||
			!strings.Contains(text, "area_gates_mode="+currentLoginAreaGatesRelaxed) ||
			!strings.Contains(text, "area_gate_denied=false") {
			t.Fatalf("relaxed login log missing the neutral gate fields:\n%s", text)
		}
		if !loginAreaGateLogHasField(text, "area_gate_deny_reason", "") {
			t.Fatalf("relaxed login log must report an empty gate deny reason:\n%s", text)
		}
		if strings.Contains(text, "game-login-area-gate-denied") {
			t.Fatalf("relaxed login evaluated the area gates:\n%s", text)
		}
	})

	// log：评估出被拒、只记录，落点不变。
	t.Run("log_records_the_denial_without_moving_the_landing", func(t *testing.T) {
		service, session, repositories := loadService(t)
		readLog := newRecordedPacketLog(t, service)
		t.Setenv(currentLoginAreaGatesEnv, currentLoginAreaGatesLog)
		_, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
			context.Background(), session, 29, loadCharacter(t, repositories),
		)
		if err != nil {
			t.Fatalf("log mode login must not block the route: %v", err)
		}
		if townID != 40 || areaID != 1 || mapPath != "WestCoast_post.map" ||
			row.Value1 != 1016 || row.Value2 != 189 {
			t.Fatalf("log mode login town=%d area=%d row=%+v map=%q, want the persisted landing unchanged", townID, areaID, row, mapPath)
		}
		text := readLog()
		for _, want := range []string{
			"game-login-area-gate-denied",
			"mode=" + currentLoginAreaGatesLog,
			"town_id=40",
			"area_id=1",
			"reason=level=11<17",
			"need_quests=[]",
			"area_gates_mode=" + currentLoginAreaGatesLog,
			"area_gate_denied=true",
			"area_gate_deny_reason=level=11<17",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("log-mode login log missing %q:\n%s", want, text)
			}
		}
		// log 模式绝不重定向：fallback 字段必须是空值。
		if !loginAreaGateLogHasField(text, "fallback", "") {
			t.Fatalf("log-mode login log must report an empty fallback:\n%s", text)
		}
	})

	// strict：重定向到 38/1 赛丽亚房间，并把重定向后的落点写回。
	t.Run("strict_redirects_to_the_elvengard_seria_room", func(t *testing.T) {
		service, session, repositories := loadService(t)
		readLog := newRecordedPacketLog(t, service)
		t.Setenv(currentLoginAreaGatesEnv, currentLoginAreaGatesStrict)
		updated, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
			context.Background(), session, 29, loadCharacter(t, repositories),
		)
		if err != nil {
			t.Fatalf("strict login must not block the route: %v", err)
		}
		if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
			mapPath != "new_seria_room.map" ||
			row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY ||
			row.Value3 != newCharacterInitialDirection || row.Value4 != newCharacterInitialAreaState {
			t.Fatalf(
				"strict login town=%d area=%d row=%+v map=%q, want the 38/1 Elvengard Seria gate %d/%d",
				townID, areaID, row, mapPath, newCharacterInitialPosX, newCharacterInitialPosY,
			)
		}
		if updated.Stats["town_id"] != newCharacterInitialTownID ||
			updated.Stats["area_id"] != newCharacterInitialAreaID ||
			updated.Stats["pos_x"] != newCharacterInitialPosX ||
			updated.Stats["pos_y"] != newCharacterInitialPosY {
			t.Fatalf("strict login did not return the redirected landing=%+v", updated.Stats)
		}
		stored, found, err := repositories.Character.Load(context.Background(), "29")
		if err != nil || !found ||
			stored.Stats["town_id"] != newCharacterInitialTownID ||
			stored.Stats["area_id"] != newCharacterInitialAreaID ||
			stored.Stats["pos_x"] != newCharacterInitialPosX ||
			stored.Stats["pos_y"] != newCharacterInitialPosY {
			t.Fatalf("strict login persisted found=%t err=%v stats=%+v, want the redirected landing", found, err, stored.Stats)
		}
		text := readLog()
		for _, want := range []string{
			"game-login-area-gate-denied",
			"mode=" + currentLoginAreaGatesStrict,
			"town_id=40",
			"area_id=1",
			"reason=level=11<17",
			"fallback=" + currentLoginAreaGateFallback,
			"area_gates_mode=" + currentLoginAreaGatesStrict,
			"area_gate_denied=true",
			"area_gate_deny_reason=level=11<17",
			"branch=" + currentTownLoginBranchElvengardSeria,
			"location_source=" + currentTownLoginLocationSeriaGateSource,
			"position_source=runtime_pvf_seria_area_gate",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("strict login log missing %q:\n%s", want, text)
			}
		}
	})
}

// 回归（线上实测）：**strict + 没有被拒 ⇒ 落点必须与 relaxed 逐字节相同**。
//
// 现场：DNFBRIDGE_LOGIN_AREA_GATES=strict、持久化城镇 38、落点就是 38/1 赛丽亚房间
// （450/234）的普通登录，登录事件里的 branch 从 town_seria_room 变成了
// elvengard_seria_fallback，而同一次评估**根本没有拒绝**（`game-login-area-gate-denied`
// 的 reason 为空，level 90 >= need_level 1）。
//
// 根因见 initial_town_login_owner.go 的门控块：调用方把评估器的第二个返回值
// evaluated（"有没有真的评估过"）当成了 denied，而它在所有路径上都是 true。
//
// 夹具自带的形状与线上完全一致：城镇 38 的 area 1 就是 `new_seria_room.map` 的赛丽亚房间
// （`[gate]` 450 234），角色 29 持久化在 38/1，两条门槛规则都没有声明
// （MinLevel=0、NeedQuests 为空）。
func TestCharacterListLoginAreaGateStrictKeepsPassingLanding(t *testing.T) {
	type loginOutcome struct {
		townID         byte
		areaID         byte
		row            currentSceneTransitionRow
		mapPath        string
		branch         string
		locationSource string
		positionSource string
		fallbackReason string
	}
	// runLogin 在指定模式下跑一次完整的角色列表登录，返回落点与日志。
	// 每次都用新的 newTownMoveTest 夹具，所以 relaxed 与 strict 的输入状态完全相同。
	runLogin := func(t *testing.T, mode string) (loginOutcome, string) {
		t.Helper()
		t.Setenv(currentLoginAreaGatesEnv, mode)
		service, session, repositories := newTownMoveTest(t)
		readLog := newRecordedPacketLog(t, service)
		character, found, err := repositories.Character.Load(context.Background(), "29")
		if err != nil || !found {
			t.Fatalf("load character found=%t err=%v", found, err)
		}
		_, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
			context.Background(), session, 29, character,
		)
		if err != nil {
			t.Fatalf("%s login must not block the route: %v", mode, err)
		}
		text := readLog()
		outcome := loginOutcome{townID: townID, areaID: areaID, row: row, mapPath: mapPath}
		for field, target := range map[string]*string{
			"branch":          &outcome.branch,
			"location_source": &outcome.locationSource,
			"position_source": &outcome.positionSource,
			"fallback_reason": &outcome.fallbackReason,
		} {
			value, found := loginAreaGateLogFieldValue(text, field)
			if !found {
				t.Fatalf("%s login log has no %s field:\n%s", mode, field, text)
			}
			*target = value
		}
		return outcome, text
	}

	relaxedOutcome, relaxedLog := runLogin(t, currentLoginAreaGatesRelaxed)
	strictOutcome, strictLog := runLogin(t, currentLoginAreaGatesStrict)

	// 硬口径（不依赖任何一次运行的自我一致）：夹具里城镇 38 有赛丽亚房间、角色持久化在
	// 38/1、两条门槛都不构成拒绝 —— relaxed 与 strict 都必须落在 38/1 赛丽亚房间。
	wantOutcome := loginOutcome{
		townID: newCharacterInitialTownID,
		areaID: newCharacterInitialAreaID,
		row: currentSceneTransitionRow{
			ObjectOrResourceKey: 29,
			Value1:              newCharacterInitialPosX,
			Value2:              newCharacterInitialPosY,
			Value3:              newCharacterInitialDirection,
			Value4:              newCharacterInitialAreaState,
		},
		mapPath:        "new_seria_room.map",
		branch:         currentTownLoginBranchTownSeriaRoom,
		locationSource: currentTownLoginLocationTownSeriaSource,
		positionSource: "runtime_pvf_seria_area_gate",
		fallbackReason: "",
	}
	if relaxedOutcome != wantOutcome {
		t.Fatalf("relaxed login outcome=%+v, want %+v", relaxedOutcome, wantOutcome)
	}
	if strictOutcome.townID != wantOutcome.townID || strictOutcome.areaID != wantOutcome.areaID ||
		strictOutcome.row.Value1 != wantOutcome.row.Value1 ||
		strictOutcome.row.Value2 != wantOutcome.row.Value2 ||
		strictOutcome.row.Value3 != wantOutcome.row.Value3 ||
		strictOutcome.row.Value4 != wantOutcome.row.Value4 {
		t.Fatalf(
			"strict login with no denial landed town=%d area=%d row=%+v, want the relaxed landing %d/%d @%d,%d",
			strictOutcome.townID, strictOutcome.areaID, strictOutcome.row,
			wantOutcome.townID, wantOutcome.areaID, wantOutcome.row.Value1, wantOutcome.row.Value2,
		)
	}
	if strictOutcome != relaxedOutcome {
		t.Fatalf(
			"strict login with no denial outcome=%+v, want the relaxed outcome %+v (evaluated must not be treated as denied)",
			strictOutcome, relaxedOutcome,
		)
	}

	// 评估通过时只打 game-login-area-gate-evaluated：**绝不能**出现 reason 为空的 -denied 事件。
	if strings.Contains(strictLog, "game-login-area-gate-denied") {
		t.Fatalf("strict login with no denial emitted a -denied event:\n%s", strictLog)
	}
	for _, want := range []string{
		"game-login-area-gate-evaluated",
		"mode=" + currentLoginAreaGatesStrict,
		"town_id=" + strconv.Itoa(int(newCharacterInitialTownID)),
		"area_id=" + strconv.Itoa(int(newCharacterInitialAreaID)),
		"need_level=0",
		"need_quests=[]",
	} {
		if !strings.Contains(strictLog, want) {
			t.Fatalf("strict login log missing %q:\n%s", want, strictLog)
		}
	}
	if !loginAreaGateLogHasField(strictLog, "denied", "false") {
		t.Fatalf("strict login evaluation must report denied=false:\n%s", strictLog)
	}
	// 登录事件里的三个门控字段口径不变：strict 模式、未拒、原因为空。
	for _, want := range []string{
		"area_gates_mode=" + currentLoginAreaGatesStrict,
		"area_gate_denied=false",
	} {
		if !strings.Contains(strictLog, want) {
			t.Fatalf("strict login log missing %q:\n%s", want, strictLog)
		}
	}
	if !loginAreaGateLogHasField(strictLog, "area_gate_deny_reason", "") {
		t.Fatalf("strict login with no denial must report an empty gate deny reason:\n%s", strictLog)
	}

	// relaxed 仍然完全不评估：连 evaluated 事件都不许有。
	if strings.Contains(relaxedLog, "game-login-area-gate") {
		t.Fatalf("relaxed login evaluated the area gates:\n%s", relaxedLog)
	}

	// log + 没有被拒：落点同样一个字都不许动，只记一条 evaluated。
	logOutcome, logLog := runLogin(t, currentLoginAreaGatesLog)
	if logOutcome != relaxedOutcome {
		t.Fatalf("log login with no denial outcome=%+v, want the relaxed outcome %+v", logOutcome, relaxedOutcome)
	}
	if strings.Contains(logLog, "game-login-area-gate-denied") {
		t.Fatalf("log login with no denial emitted a -denied event:\n%s", logLog)
	}
	if !strings.Contains(logLog, "game-login-area-gate-evaluated") ||
		!loginAreaGateLogHasField(logLog, "mode", currentLoginAreaGatesLog) {
		t.Fatalf("log login with no denial must record the evaluation:\n%s", logLog)
	}
}

// 回归：赛丽亚房间属于**别的城镇**时，strict 也必须守住"评估通过 ⇒ 不改落点"。
//
// 夹具的城镇 41（阿法利亚营地）自己有一个赛丽亚房间（41/2 `…/new_seria_room.map`，
// `[gate]` 733/214），角色持久化在 41/0。把 evaluated 当成 denied 时，这里会从
// "41/2 自己的赛丽亚房间"被错误地搬到"38/1 艾尔文防线赛丽亚房间"——这正是本次缺陷
// 在线上会造成的真实回归（对落点恰好是 38/1 的角色只是 branch 变了，对别的城镇则是搬走）。
func TestCharacterListLoginAreaGateStrictKeepsOtherTownSeriaRoom(t *testing.T) {
	t.Setenv(currentLoginAreaGatesEnv, currentLoginAreaGatesStrict)
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
	character.Stats["town_id"] = 41
	character.Stats["area_id"] = 0
	character.Stats["pos_x"] = 900
	character.Stats["pos_y"] = 120
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}

	_, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(), session, 29, character,
	)
	if err != nil {
		t.Fatalf("strict login must not block the route: %v", err)
	}
	// 先把日志收干净（readLog 会关闭文件），再断言，避免失败时留下未关闭的日志文件。
	text := readLog()
	if townID != 41 || areaID != 2 ||
		mapPath != "Cataclysm/Town/Alfhlyra/new_seria_room.map" ||
		row.Value1 != 733 || row.Value2 != 214 {
		t.Fatalf(
			"strict login with no denial landed town=%d area=%d row=%+v map=%q, want town 41's own Seria room 41/2 @733,214",
			townID, areaID, row, mapPath,
		)
	}
	if !strings.Contains(text, "branch="+currentTownLoginBranchTownSeriaRoom) ||
		!strings.Contains(text, "location_source="+currentTownLoginLocationTownSeriaSource) {
		t.Fatalf("strict login with no denial must keep the town-41 Seria-room branch:\n%s", text)
	}
	if strings.Contains(text, "game-login-area-gate-denied") ||
		strings.Contains(text, "branch="+currentTownLoginBranchElvengardSeria) {
		t.Fatalf("strict login with no denial was wrongly redirected to Elvengard:\n%s", text)
	}
}

// strict + **任务侧**拒绝（`[need quest]` 未完成）仍然必须重定向 —— 修"评估通过也被当成被拒"
// 时不能把真正的拒绝一起放过：拒绝的判据是拒绝原因非空，而任务原因的形态是
// quest=N_not_cleared（与等级原因的 level=X<Y 不同）。
func TestCharacterListLoginAreaGateStrictRedirectsUnclearedQuestLanding(t *testing.T) {
	t.Setenv(currentLoginAreaGatesEnv, currentLoginAreaGatesStrict)
	// 第 2 级（持久化精确落点）默认被门控关闭，先打开它才能构造"持久化落点撞任务门槛"的现场。
	t.Setenv(currentLoginPersistedTownEnv, currentLoginPersistedTownKeepValue)
	service, session, repositories := newTownMoveTest(t)
	loadTownSeriaFixtureService(t, service, townMoveTestSource{
		"town/town.lst":          "38 `new_Elvengard.twn` 40 `new_WestCoast.twn`",
		"town/new_Elvengard.twn": townSeriaFixtureElvengard,
		// 西海岸邮局 40/1 声明 `[need quest] 3156`：夹具角色 29 没有完成该任务。
		"town/new_WestCoast.twn": "[area]\n1 `WestCoast_post.map`\n[need quest]\n3156\n[/need quest]\n`[gate]` 1016 189\n[/area]\n" +
			"[name]\n`WestCoast`",
	})
	readLog := newRecordedPacketLog(t, service)
	character, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = 60
	character.Stats["town_id"] = 40
	character.Stats["area_id"] = 1
	character.Stats["pos_x"] = 1016
	character.Stats["pos_y"] = 189
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	area, found := service.townArea(40, 1)
	if !found || len(area.NeedQuests) == 0 {
		t.Fatalf("fixture town 40 area 1 need_quests=%v found=%t, want the declared quest gate", area.NeedQuests, found)
	}

	_, townID, areaID, row, mapPath, err := service.currentCharacterListLoginTransition(
		context.Background(), session, 29, character,
	)
	if err != nil {
		t.Fatalf("strict login must not block the route: %v", err)
	}
	text := readLog()
	if townID != newCharacterInitialTownID || areaID != newCharacterInitialAreaID ||
		mapPath != "new_seria_room.map" ||
		row.Value1 != newCharacterInitialPosX || row.Value2 != newCharacterInitialPosY {
		t.Fatalf(
			"strict quest-denied login landed town=%d area=%d row=%+v map=%q, want the 38/1 Elvengard Seria gate %d/%d",
			townID, areaID, row, mapPath, newCharacterInitialPosX, newCharacterInitialPosY,
		)
	}
	for _, want := range []string{
		"game-login-area-gate-denied",
		"mode=" + currentLoginAreaGatesStrict,
		"reason=quest=3156_not_cleared",
		"fallback=" + currentLoginAreaGateFallback,
		"area_gate_denied=true",
		"area_gate_deny_reason=quest=3156_not_cleared",
		"branch=" + currentTownLoginBranchElvengardSeria,
		"position_source=runtime_pvf_seria_area_gate",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("strict quest-denied login log missing %q:\n%s", want, text)
		}
	}
}
