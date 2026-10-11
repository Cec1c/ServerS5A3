package skillswitch

import (
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 「删除语义」（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）在仓储层的钉子。
//
// 现场（HK `109.244.63.71`，`packet_log.txt` 第 197164..197200 行 + `dnf_setting_values`，
// 2026-09-28。逐字原文见 `_dist/buff-swap-round6.md` §1）：
//
//	18:38:57.864 .. 18:39:00.547Z  char 1 = `test1` 推 9 条记录
//	    （8 条 `ModeConfirm` 槽位 12/14/15/16/17/18/19/20 + **1 条真绑定**
//	     `0c001400` = slot 12 / mode 0 / value 20 —— 他在面板里只改了**一行**）
//	18:39:04.552Z 收尾哨兵 `212cffff` 关闭窗口：
//	    registration_kept_count=8 registration_dropped_count=12
//	    registration_kept_triples=(12,0,20)(14,3,14)…(20,3,20)
//	    registration_dropped_triples=(1,3,1)…(7,3,7)(13,3,13)(21,3,21)(22,3,22)(23,3,23)(25,3,25)
//	    registration_dropped_entries=slot_1=01030100,…,slot_25=19031900
//
// ⇒ 一次"只改了 1 行"的登记把另外 **12 行删掉了**（20 → 8）。操作者对此的原话是
// "选择角色后 面板里原本有的那些行消失了"。本轮把默认档改成**永不删行**。

// registrationDeletePreStateRows 是 char 1 那次登记**之前**的 20 行
// （值按 v4 规范形；`registration_dropped_entries` + 18:38:52 的 20 对重发逐字对齐）。
var registrationDeletePreStateRows = map[string]string{
	"slot_1":  "01030100",
	"slot_2":  "02030200",
	"slot_3":  "03030300",
	"slot_4":  "04030400",
	"slot_5":  "05030500",
	"slot_6":  "06030600",
	"slot_7":  "07030700",
	"slot_12": "0c030c00",
	"slot_13": "0d030d00",
	"slot_14": "0e030e00",
	"slot_15": "0f030f00",
	"slot_16": "10031000",
	"slot_17": "11031100",
	"slot_18": "12031200",
	"slot_19": "13031300",
	"slot_20": "14031400",
	"slot_21": "15031500",
	"slot_22": "16031600",
	"slot_23": "17031700",
	"slot_25": "19031900",
}

// registrationDeleteBurstBodies 是那次登记的 9 条包体（现场顺序，4 字节规范形）。
var registrationDeleteBurstBodies = []string{
	"11031100", // slot 17 / mode 3 / value 17
	"10031000", // slot 16
	"12031200", // slot 18
	"0e030e00", // slot 14
	"0f030f00", // slot 15
	"0c030c00", // slot 12（先是一条不含绑定的确认）
	"13031300", // slot 19
	"14031400", // slot 20
	"0c001400", // slot 12 / mode 0 / value 20 ← 操作者改的那一行（唯一携带绑定的记录）
}

// registrationDeleteDroppedTriples 是现场那次提交删掉的那 12 行（照抄日志字段）。
const registrationDeleteDroppedTriples = "(1,3,1)(2,3,2)(3,3,3)(4,3,4)(5,3,5)(6,3,6)(7,3,7)" +
	"(13,3,13)(21,3,21)(22,3,22)(23,3,23)(25,3,25)"

// seedRegistrationDeletePreState 把现场那 20 行 + `window_armed=1` 写进 scope。
func seedRegistrationDeletePreState(t *testing.T, repos dnfrepo.Group) {
	t.Helper()
	values := map[string]string{
		"source":       SourceCurrentEXE,
		"slot_count":   "20",
		"updated_at":   "2026-09-28T16:15:36.096620903Z",
		"window_armed": "1",
		"window_count": "0",
	}
	for key, value := range registrationDeletePreStateRows {
		values[key] = value
	}
	if err := repos.Settings.Save(context.Background(), dnfrepo.SettingsRecord{
		Scope:  Scope("1"),
		Values: values,
	}); err != nil {
		t.Fatalf("seed scope: %v", err)
	}
}

// applyRegistrationDeleteBurst 推现场那 9 条记录 + 收尾哨兵。
func applyRegistrationDeleteBurst(t *testing.T, owner *Owner) {
	t.Helper()
	for _, body := range registrationDeleteBurstBodies {
		applyRegistrationBody(t, owner, body)
	}
	applyRegistrationBody(t, owner, registrationSentinelBody)
}

// TestRegistrationDeleteKeepRetainsTheTwelveRows 是**本档主用例**：
// 现场那一次"只改一行"的登记之后，原来那 20 行必须**一行都不少**。
func TestRegistrationDeleteKeepRetainsTheTwelveRows(t *testing.T) {
	repos, owner := newRegistrationFixture(t)
	seedRegistrationDeletePreState(t, repos)

	applyRegistrationDeleteBurst(t, owner)

	after := storedSlotValues(t, repos)
	if len(after) != len(registrationDeletePreStateRows) {
		t.Fatalf("rows after the burst = %d, want all %d (values=%v)",
			len(after), len(registrationDeletePreStateRows), after)
	}
	for key, want := range registrationDeletePreStateRows {
		if key == "slot_12" {
			// 操作者改过的那一行当然要变（`0c001400` = slot 12 -> value 20）。
			if got := after[key]; got != "0c001400" {
				t.Fatalf("%s = %q after the operator's rebind, want 0c001400", key, got)
			}
			continue
		}
		if got := after[key]; got != want {
			t.Fatalf("%s = %q, want the untouched %q", key, got, want)
		}
	}
	scope := storedScopeValues(t, repos)
	if scope[entrySlotCount] != "20" {
		t.Fatalf("slot_count = %q, want 20 (nothing may be deleted)", scope[entrySlotCount])
	}
	// 同一次提交在 `commitRegistrationWindow` 里报出来的账：
	// kept=8 / dropped=0 / retained=12（= 旧语义本来会删掉的那 12 行）。
	if scope[entryWindowCount] != "0" {
		t.Fatalf("window_count = %q, want 0 after the commit", scope[entryWindowCount])
	}
}

// TestRegistrationDeleteKeepReportsRetainedRows 钉"提交账目"：
// 同一条边界（`CommitRegistration`）在默认档下 `Dropped` 恒空、`Retained` 恰好是那 12 行；
// 回退字面量 `replace` 下才是现场那一版（dropped=12、集合 20→8）。
func TestRegistrationDeleteKeepReportsRetainedRows(t *testing.T) {
	repos, owner := newRegistrationFixture(t)
	seedRegistrationDeletePreState(t, repos)
	applyRegistrationDeleteBurst(t, owner)

	commit, committed := commitWindow(t, repos, RegistrationWindow)
	if committed {
		// 哨兵已经把窗口关掉了；边界再提交一次应当是 no-op（不重复写库）。
		t.Fatalf("the sentinel already closed the window: %+v", commit)
	}
	if got := len(storedSlotValues(t, repos)); got != 20 {
		t.Fatalf("rows = %d, want 20", got)
	}

	// 第二条路径：**边界**关闭窗口（不进哨兵那条）。
	repos2, owner2 := newRegistrationFixture(t)
	seedRegistrationDeletePreState(t, repos2)
	for _, body := range registrationDeleteBurstBodies {
		applyRegistrationBody(t, owner2, body)
	}
	commit2, committed2 := commitWindow(t, repos2, RegistrationWindow)
	if !committed2 {
		t.Fatal("the boundary must still commit the open window")
	}
	if commit2.Delete != RegistrationDeleteKeep {
		t.Fatalf("commit delete tier = %q, want %q", commit2.Delete, RegistrationDeleteKeep)
	}
	if len(commit2.Dropped) != 0 {
		t.Fatalf("default tier dropped %d rows, want 0: %v", len(commit2.Dropped), commit2.Dropped)
	}
	if len(commit2.Retained) != 12 {
		t.Fatalf("retained = %d rows, want the 12 the old semantics would have deleted: %v",
			len(commit2.Retained), commit2.Retained)
	}
	if got := skillSwitchTriplesText(commit2.Retained); got != registrationDeleteDroppedTriples {
		t.Fatalf("retained triples = %s\nwant               %s", got, registrationDeleteDroppedTriples)
	}
	if got := len(storedSlotValues(t, repos2)); got != 20 {
		t.Fatalf("rows after the boundary commit = %d, want 20", got)
	}
	if commit2.SlotCount != 20 {
		t.Fatalf("slot_count = %d, want 20", commit2.SlotCount)
	}
}

// TestRegistrationDeleteRollbackLiteralIsByteForByteToday 钉**一键回退字面量** `replace`：
// 同一条现场序列下，落库**恰好等于线上那一份**（20 → 8，dropped=12）。
func TestRegistrationDeleteRollbackLiteralIsByteForByteToday(t *testing.T) {
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	repos, owner := newRegistrationFixture(t)
	seedRegistrationDeletePreState(t, repos)

	for _, body := range registrationDeleteBurstBodies {
		applyRegistrationBody(t, owner, body)
	}
	result := applyRegistrationBody(t, owner, registrationSentinelBody)
	if !result.RegistrationCommitted {
		t.Fatal("the sentinel must commit the open window")
	}
	if result.RegistrationDelete != RegistrationDeleteReplace {
		t.Fatalf("delete tier = %q, want %q", result.RegistrationDelete, RegistrationDeleteReplace)
	}
	if got := len(result.RegistrationDropped); got != 12 {
		t.Fatalf("dropped = %d, want today's 12: %v", got, result.RegistrationDropped)
	}
	if got := skillSwitchTriplesText(result.RegistrationDropped); got != registrationDeleteDroppedTriples {
		t.Fatalf("dropped triples = %s\nwant               %s", got, registrationDeleteDroppedTriples)
	}
	if got := len(result.RegistrationRetained); got != 0 {
		t.Fatalf("the rollback literal must not retain anything (got %d)", got)
	}
	after := storedSlotValues(t, repos)
	if len(after) != 8 {
		t.Fatalf("rows = %d, want today's 8 (values=%v)", len(after), after)
	}
	if got := after["slot_12"]; got != "0c001400" {
		t.Fatalf("slot_12 = %q, want 0c001400", got)
	}
	if _, present := after["slot_1"]; present {
		t.Fatal("the rollback literal must delete slot_1 (today's bytes)")
	}
}

// TestNormalizeRegistrationDeleteTable 钉开关口径：只有回退字面量才离开修复档。
func TestNormalizeRegistrationDeleteTable(t *testing.T) {
	for _, value := range []string{"", "keep", "KEEP", "add", "unknown"} {
		if got := NormalizeRegistrationDelete(value); got != RegistrationDeleteKeep {
			t.Fatalf("NormalizeRegistrationDelete(%q) = %q, want %q", value, got, RegistrationDeleteKeep)
		}
	}
	for _, value := range []string{"replace", "REPLACE", "window", "today", "live", "legacy", "rollback", "0", "disable"} {
		if got := NormalizeRegistrationDelete(value); got != RegistrationDeleteReplace {
			t.Fatalf("NormalizeRegistrationDelete(%q) = %q, want %q", value, got, RegistrationDeleteReplace)
		}
	}
	t.Setenv(RegistrationDeleteEnv, "")
	if got := CurrentRegistrationDelete(); got != RegistrationDeleteKeep {
		t.Fatalf("CurrentRegistrationDelete() = %q, want %q", got, RegistrationDeleteKeep)
	}
	var nilOwner *Owner
	if got := nilOwner.registrationDelete(); got != RegistrationDeleteKeep {
		t.Fatalf("nil owner delete tier = %q, want %q", got, RegistrationDeleteKeep)
	}
	t.Setenv(RegistrationDeleteEnv, "replace")
	if got := CurrentRegistrationDelete(); got != RegistrationDeleteReplace {
		t.Fatalf("CurrentRegistrationDelete() = %q, want %q", got, RegistrationDeleteReplace)
	}
}
