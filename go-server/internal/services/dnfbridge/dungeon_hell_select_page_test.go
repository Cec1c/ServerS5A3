package dnfbridge

import (
	"bytes"
	"context"
	"strings"
	"testing"

	dnftown "longheng.io/server/internal/modules/dnf/town"
)

// 本文件钉住操作者报障「6 未完成深渊任务的角色深渊还是亮的」的修复层：
// **深渊资格必须在"开选择页"那一刻判出来、并随那一条 class0/op27
// （= 厂商 `NOTI 0x001B`）下发**，而不是等玩家点了「挑战深渊派对」（op16）才判。
//
// 厂商时序（只读引用 S4A21）：
//
//	DungeonEntryHandler.cs:723-726  进本选择页 handler 里 BuildHellPartySelectionState
//	DungeonEntryHandler.cs:780-786  blocked 槽位随 NOTI 0x001B 下发
//	DungeonEntryHandler.cs:1425-1463 这一页属于哪个区域 = 当前城镇区域的 `[dungeon gate]`

// hellSelectPageTestSource 给合成夹具补一份城镇目录：town 38 / area 1 声明
// `[dungeon gate] 1`，而合成 worldmap 的 area 1 正是
// `[hell dungeon] 1` + `[hell quest] 7777`（`hellPartyBridgePVF()`）。
// `[dungeon gate] N` 里的 N 就是 worldmap area id（同
// `town/principal_area_test.go` 的 `[dungeon gate] 39` = 地轨中心 area 39）。
func hellSelectPageTestSource(withGate bool) townMoveTestSource {
	gate := "`[normal]`"
	if withGate {
		gate = "`[dungeon gate]` 1"
	}
	return townMoveTestSource{
		"town/town.lst":      "38 `gate_town.twn`",
		"town/gate_town.twn": "[area]\n1 `gate_area.map` " + gate + "\n[/area]\n[name]\n`GateTown`",
	}
}

// hellSelectPageFixture 在既有深渊夹具上装一份城镇目录，并显式指定两条开关的档位。
func hellSelectPageFixture(
	t *testing.T,
	blockedSlotsMode hellBlockedSlotsMode,
	pageMode hellSelectPageEligibilityMode,
	questStatus string,
	withGate bool,
) (*Service, *gameSession, *bufferConn, *packetLogger, string) {
	t.Helper()
	service, session, conn, logger, logPath := hellBlockedSlotsFixtureWithPVF(
		t,
		hellBlockedSlotsBridgePVF(),
		blockedSlotsMode,
		hellBlockedSlotsQuestStates(map[string]string{"99": questStatus}),
		nil,
	)
	service.options.hellSelectPageEligibility = pageMode
	table, err := dnftown.Load(context.Background(), hellSelectPageTestSource(withGate), dnftown.Options{})
	if err != nil {
		t.Fatalf("load synthetic town catalog: %v", err)
	}
	service.townCatalog = table
	return service, session, conn, logger, logPath
}

// TestHellSelectPageEligibilityModeAndSwitchParsing 钉住开关解析：
// **只有显式 `legacy` 才回退**，其它任何取值（含 off/0/on/vendor）都是默认档。
func TestHellSelectPageEligibilityModeAndSwitchParsing(t *testing.T) {
	if hellSelectPageEligibilityEnv != "DNFBRIDGE_HELL_SELECT_PAGE_ELIGIBILITY" {
		t.Fatalf("env name = %q", hellSelectPageEligibilityEnv)
	}
	if got := readHellSelectPageEligibilityMode("legacy"); got != hellSelectPageEligibilityLegacy {
		t.Fatalf("readHellSelectPageEligibilityMode(legacy) = %q", got)
	}
	if got := readHellSelectPageEligibilityMode(" LEGACY "); got != hellSelectPageEligibilityLegacy {
		t.Fatalf("readHellSelectPageEligibilityMode( LEGACY ) = %q", got)
	}
	for _, value := range []string{"", "off", "0", "false", "on", "vendor", "1", "page"} {
		if got := readHellSelectPageEligibilityMode(value); got != hellSelectPageEligibilityVendor {
			t.Fatalf("readHellSelectPageEligibilityMode(%q) = %q want vendor (默认 = 修复档)",
				value, got)
		}
	}
	if !readHellSelectPageEligibilityMode("").evaluatesAtPageBuild() {
		t.Fatal("default mode must evaluate at page build")
	}
	if readHellSelectPageEligibilityMode("legacy").evaluatesAtPageBuild() {
		t.Fatal("legacy mode must not evaluate at page build")
	}
	var nilService *Service
	if nilService.hellSelectPageEligibilityGate() != hellSelectPageEligibilityVendor {
		t.Fatal("nil Service must default to vendor")
	}
}

// TestHellSelectPageEvaluationFillsBlockedSlotsIntoTheOp27PageBody 是主回归：
// 未完成 `[hell quest]` 的角色**在开选择页那一步**就判出 blocked 槽位，
// 并且真的落进 class0/op27 的 List0（客户端画按钮之前就拿到它）。
func TestHellSelectPageEvaluationFillsBlockedSlotsIntoTheOp27PageBody(t *testing.T) {
	service, session, conn, logger, logPath := hellSelectPageFixture(
		t, hellBlockedSlotsOn, hellSelectPageEligibilityVendor, "active", true)

	service.refreshHellSelectPageEligibility(session, "upper_15_after_op15_ack_fatigue")
	if !service.hellBlockedSlotsPending(session) {
		t.Fatal("page-time eligibility did not record a blocked slot")
	}
	if err := service.sendCurrentDungeonContextOp27(session, "upper_15_after_op15_ack_fatigue"); err != nil {
		t.Fatalf("send op27: %v", err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, []uint16{0})

	logText := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"kind=game-hell-select-page-eligibility-evaluated",
		"source=upper_15_after_op15_ack_fatigue",
		"select_page_eligibility_mode=vendor",
		"dungeon_gate_area=1",
		"dungeon_gate_area_found=true",
		"hell_dungeon=true",
		"hell_quest_gate=true",
		"pvf_status=" + hellPVFStatusIDsParsed,
		"blocked_count=1",
		"blocked_slots=slot=0:quest=7777",
		"body_source=a21_build_hell_party_selection_state_at_enter_select_dungeon",
		"kind=game-hell-eligibility-blocked",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("page-time hell eligibility log missing %q:\n%s", want, logText)
		}
	}
	// 顺序：判定必须在**这一条** op27 之前（否则客户端还是先收到"全亮"的页面）。
	evaluatedAt := strings.Index(logText, "kind=game-hell-select-page-eligibility-evaluated")
	op27At := strings.Index(logText, "kind=game-upper-current-dungeon-context-op27-send")
	if evaluatedAt < 0 || op27At < 0 || evaluatedAt > op27At {
		t.Fatalf("page-time evaluation must precede the op27 page body: evaluated=%d op27=%d\n%s",
			evaluatedAt, op27At, logText)
	}
}

// TestHellSelectPageEligibleCharacterKeepsTheZeroBody 钉住反向：**完成任务的
// 角色一个字节都不变**（37 字节全零体），不会因为这次修复被误判成"不能打深渊"。
func TestHellSelectPageEligibleCharacterKeepsTheZeroBody(t *testing.T) {
	service, session, conn, logger, logPath := hellSelectPageFixture(
		t, hellBlockedSlotsOn, hellSelectPageEligibilityVendor, "completed", true)

	service.refreshHellSelectPageEligibility(session, "upper_15_after_op15_ack_fatigue")
	if service.hellBlockedSlotsPending(session) {
		t.Fatal("an eligible character must not produce a blocked slot")
	}
	if err := service.sendCurrentDungeonContextOp27(session, "upper_15_after_op15_ack_fatigue"); err != nil {
		t.Fatalf("send op27: %v", err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, nil)
	if !bytes.Equal(op27.Body, hellBlockedSlotsExpectedBody(nil)) {
		t.Fatalf("eligible character op27 body=%x want=%x", op27.Body, hellBlockedSlotsExpectedBody(nil))
	}
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logText, "kind=game-hell-select-page-eligibility-evaluated") ||
		!strings.Contains(logText, "blocked_count=0") ||
		!strings.Contains(logText, "reason=hell_party_all_members_eligible") {
		t.Fatalf("eligible page-time summary missing:\n%s", logText)
	}
}

// TestHellSelectPageLegacyModeKeepsTodaysEmptyPageBody 是**回退档的逐字节钉子**：
// `legacy` 档下页面那条 op27 的 blocked 列表恒空（= 今天的行为，按钮依旧亮的那个缺陷），
// 而且不会有任何本文件新增的事件。
func TestHellSelectPageLegacyModeKeepsTodaysEmptyPageBody(t *testing.T) {
	service, session, conn, logger, logPath := hellSelectPageFixture(
		t, hellBlockedSlotsOn, hellSelectPageEligibilityLegacy, "active", true)

	service.refreshHellSelectPageEligibility(session, "upper_15_after_op15_ack_fatigue")
	if service.hellBlockedSlotsPending(session) {
		t.Fatal("legacy mode must not evaluate at page build")
	}
	if err := service.sendCurrentDungeonContextOp27(session, "upper_15_after_op15_ack_fatigue"); err != nil {
		t.Fatalf("send op27: %v", err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, nil)
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, "game-hell-select-page-eligibility-evaluated") {
		t.Fatalf("legacy mode emitted the new page-time event:\n%s", logText)
	}
	if strings.Contains(logText, "game-hell-eligibility-evaluated") {
		t.Fatalf("legacy mode evaluated hell eligibility at all:\n%s", logText)
	}
}

// TestHellSelectPageWithoutDungeonGateStaysFailOpen 钉住"不在副本门口 ⇒ 不判、不置灰"：
// 街上/赛丽亚房间/酒馆那一页与深渊无关，这一层必须一个槽位都不 block（误报比漏报糟）。
func TestHellSelectPageWithoutDungeonGateStaysFailOpen(t *testing.T) {
	service, session, conn, logger, logPath := hellSelectPageFixture(
		t, hellBlockedSlotsOn, hellSelectPageEligibilityVendor, "active", false)

	service.refreshHellSelectPageEligibility(session, "upper_15_after_op15_ack_fatigue")
	if service.hellBlockedSlotsPending(session) {
		t.Fatal("a non-dungeon-gate area must not produce blocked slots")
	}
	if err := service.sendCurrentDungeonContextOp27(session, "upper_15_after_op15_ack_fatigue"); err != nil {
		t.Fatalf("send op27: %v", err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, nil)
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logText, "dungeon_gate_area_found=false") ||
		!strings.Contains(logText, "reason=hell_party_current_area_is_not_a_dungeon_gate") {
		t.Fatalf("non-gate page must report the fail-open reason:\n%s", logText)
	}
}

// TestHellSelectPageEvaluationRequiresTheBlockedSlotsProjection 钉住两条开关的独立性：
// `DNFBRIDGE_HELL_BLOCKED_SLOTS` 关着时（blocked 列表没有下发通道）本层完全不介入 ——
// 不读库、不写缓存、不记新事件，逐字节等于今天。
func TestHellSelectPageEvaluationRequiresTheBlockedSlotsProjection(t *testing.T) {
	service, session, conn, logger, logPath := hellSelectPageFixture(
		t, hellBlockedSlotsOff, hellSelectPageEligibilityVendor, "active", true)

	service.refreshHellSelectPageEligibility(session, "upper_15_after_op15_ack_fatigue")
	if service.hellBlockedSlotsPending(session) {
		t.Fatal("blocked-slots projection is off; nothing may be recorded")
	}
	if err := service.sendCurrentDungeonContextOp27(session, "upper_15_after_op15_ack_fatigue"); err != nil {
		t.Fatalf("send op27: %v", err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, nil)
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, "game-hell-select-page-eligibility-evaluated") ||
		strings.Contains(logText, "game-hell-eligibility-evaluated") {
		t.Fatalf("blocked-slots off must not evaluate hell eligibility:\n%s", logText)
	}
}
