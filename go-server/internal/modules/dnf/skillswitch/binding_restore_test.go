package skillswitch

import (
	"context"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 「绑定还原」（`DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE`）在仓储层的钉子。
//
// 现场（HK `192.0.2.11`，2026-09-28，逐行原始证据见 `_dist/buff-swap-round4.md`）。
// `keep` 档上线（17:45:20Z 重启）之后运维重登记了一次，**什么都没再被破坏**
// （`registration_dropped_count=0`），但那一批记录全是 `Value == Slot`（不含绑定），
// 于是重发帧是 `pairs=(12,12)(13,13)…(25,25)` —— 这些 value 指向主背包的 12..25 号格子，
// 而 char 6 的 11 件换装物品在主背包的 **54..64** 号格子。面板因此为空。
//
// 本文件的判据 = `Load` 读出来的 `State.Records` 的**值**，以及"DB 一个字节都没动"。

// bindingRestoreLiveRows 是现场 `character:6:skill_switch_inventory` 现在那 13 行
// `slot_*`（`updated_at=2026-09-28T17:50:30.499032224Z`，SQL 原文照抄）。
var bindingRestoreLiveRows = map[string]string{
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

// bindingRestoreLiveForensics 是同一个 scope 里、**客户端自己**为这些槽位声明过绑定的
// 那些 `record_<hex>` 取证键（从 09:03 / 13:52 / 16:15 三段 ModeRegister 来，
// `source=current_exe_op1428`）。值是 `Apply` 写下的人类可读形，判据只看键名。
var bindingRestoreLiveForensics = map[string]string{
	"record_0c003600": "wire_prefix=38 slot=12 sub_mode=0x0 value=54",
	"record_0e003e00": "wire_prefix=38 slot=14 sub_mode=0x0 value=62",
	"record_0f003d00": "wire_prefix=38 slot=15 sub_mode=0x0 value=61",
	"record_10003900": "wire_prefix=38 slot=16 sub_mode=0x0 value=57",
	"record_11003b00": "wire_prefix=38 slot=17 sub_mode=0x0 value=59",
	"record_12003f00": "wire_prefix=38 slot=18 sub_mode=0x0 value=63",
	"record_13003a00": "wire_prefix=38 slot=19 sub_mode=0x0 value=58",
	"record_14004000": "wire_prefix=38 slot=20 sub_mode=0x0 value=64",
	"record_15003c00": "wire_prefix=38 slot=21 sub_mode=0x0 value=60",
	"record_16003800": "wire_prefix=38 slot=22 sub_mode=0x0 value=56",
	"record_17003700": "wire_prefix=38 slot=23 sub_mode=0x0 value=55",
	// 不含绑定的取证键（客户端后来的 ModeConfirm 段）——它们**不是**候选。
	"record_0c030c00":   "wire_prefix=0 slot=12 sub_mode=0x3 value=12",
	"record_0e030e00":   "wire_prefix=0 slot=14 sub_mode=0x3 value=14",
	"record_17031700":   "wire_prefix=0 slot=23 sub_mode=0x3 value=23",
	"sentinel_042cffff": "wire_prefix=0 slot=4 sub_mode=0x2c value=65535",
}

// bindingRestoreLiveExpected 是修复档下 `Load` 应当给出的 13 条记录（升序）。
// 11 条是补回来的绑定（value = 主背包 54..64 号格子），slot 13 / 25 保持原样。
const bindingRestoreLiveExpected = "(12,0,54)(13,3,13)(14,0,62)(15,0,61)(16,0,57)(17,0,59)" +
	"(18,0,63)(19,0,58)(20,0,64)(21,0,60)(22,0,56)(23,0,55)(25,3,25)"

// seedBindingRestoreScope 把现场那 13 行 + 取证键原样写进一个 scope。
func seedBindingRestoreScope(t *testing.T, repos dnfrepo.Group, characterID string, rows map[string]string) {
	t.Helper()
	values := map[string]string{
		"source":       SourceCurrentEXE,
		"slot_count":   "13",
		"updated_at":   "2026-09-28T17:50:30.499032224Z",
		"window_armed": "1",
		"window_count": "0",
	}
	for key, value := range rows {
		values[key] = value
	}
	for key, value := range bindingRestoreLiveForensics {
		values[key] = value
	}
	if err := repos.Settings.Save(context.Background(), dnfrepo.SettingsRecord{
		Scope:  Scope(characterID),
		Values: values,
	}); err != nil {
		t.Fatalf("seed scope %s: %v", Scope(characterID), err)
	}
}

func bindingRestoreTriples(records []Record) string {
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, "("+itoaTest(int(record.Slot))+","+itoaTest(int(record.Mode))+","+
			itoaTest(int(record.Value))+")")
	}
	return strings.Join(parts, "")
}

func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestBindingRestoreUsesTheClientsOwnLastBinding 是**主用例**：现场那 13 行
// `Value == Slot` 的行里，11 行按客户端自己上一次的绑定声明被补回来，
// 而 DB 里一个字节都没动。
func TestBindingRestoreUsesTheClientsOwnLastBinding(t *testing.T) {
	repos, _ := newSkillSwitchFixture(t)
	seedBindingRestoreScope(t, repos, "6", bindingRestoreLiveRows)

	state, found, err := Load(context.Background(), repos, "6", LayoutV4)
	if err != nil || !found {
		t.Fatalf("Load found=%t err=%v", found, err)
	}
	if got := bindingRestoreTriples(state.Records); got != bindingRestoreLiveExpected {
		t.Fatalf("records = %s\nwant      %s", got, bindingRestoreLiveExpected)
	}
	if len(state.Restored) != 11 {
		t.Fatalf("restored = %d rows, want the 11 live bindings (%v)", len(state.Restored), state.Restored)
	}
	// 补回来的 value 必须**恰好**是现场换装物品所在的那 11 个主背包格子。
	wantValues := []int{54, 62, 61, 57, 59, 63, 58, 64, 60, 56, 55}
	for index, record := range state.Restored {
		if int(record.Value) != wantValues[index] {
			t.Fatalf("restored[%d] = (slot %d, value %d), want value %d",
				index, record.Slot, record.Value, wantValues[index])
		}
		if !record.BindingBearing() {
			t.Fatalf("restored[%d] must carry a binding", index)
		}
	}

	// DB 一个字节都没动（这一档是**只读**的读取视图修补）。
	stored, found, err := repos.Settings.Load(context.Background(), Scope("6"))
	if err != nil || !found {
		t.Fatalf("reload found=%t err=%v", found, err)
	}
	for key, want := range bindingRestoreLiveRows {
		if got := stored.Values[key]; got != want {
			t.Fatalf("DB row %s = %q, want the untouched %q (this tier must not write)", key, got, want)
		}
	}
	if got := stored.Values["updated_at"]; got != "2026-09-28T17:50:30.499032224Z" {
		t.Fatalf("DB updated_at = %q, want the untouched live value", got)
	}
	// 再读一次结果完全一致（确定性，无副作用）。
	again, _, err := Load(context.Background(), repos, "6", LayoutV4)
	if err != nil {
		t.Fatal(err)
	}
	if bindingRestoreTriples(again.Records) != bindingRestoreLiveExpected {
		t.Fatalf("second Load = %s, want the same result", bindingRestoreTriples(again.Records))
	}
}

// TestBindingRestoreRollbackLiteralIsByteForByteToday 钉**一键回退字面量** `off`：
// `Load` 原样返回现场那 13 行（= 线上当前这一版的字节），一条都不补。
func TestBindingRestoreRollbackLiteralIsByteForByteToday(t *testing.T) {
	t.Setenv(BindingRestoreEnv, BindingRestoreOff)
	repos, _ := newSkillSwitchFixture(t)
	seedBindingRestoreScope(t, repos, "6", bindingRestoreLiveRows)

	state, found, err := Load(context.Background(), repos, "6", LayoutV4)
	if err != nil || !found {
		t.Fatalf("Load found=%t err=%v", found, err)
	}
	if len(state.Restored) != 0 {
		t.Fatalf("rollback literal restored %d rows, want 0", len(state.Restored))
	}
	want := "(12,3,12)(13,3,13)(14,3,14)(15,3,15)(16,3,16)(17,3,17)" +
		"(18,3,18)(19,3,19)(20,3,20)(21,3,21)(22,3,22)(23,3,23)(25,3,25)"
	if got := bindingRestoreTriples(state.Records); got != want {
		t.Fatalf("rollback records = %s\nwant today's      %s", got, want)
	}
	for _, record := range state.Records {
		if record.BindingBearing() {
			t.Fatalf("the rollback literal must not produce a binding (%+v)", record)
		}
	}
}

// TestBindingRestoreNeverResurrectsDeletedSlots 钉边界 1：**被删掉的槽位不复活**。
// 现场 17:12:48Z 之后、17:50 重登记之前的状态是 9 行（12 / 23 已被删），
// 那 9 行里的绑定同样只在取证键里 —— 但本档只补"已经有行"的槽位。
func TestBindingRestoreNeverResurrectsDeletedSlots(t *testing.T) {
	repos, _ := newSkillSwitchFixture(t)
	rows := make(map[string]string, len(bindingRestoreLiveRows))
	for key, value := range bindingRestoreLiveRows {
		// 17:12:48Z 那次提交删掉了 12 / 23；13 / 25 是 17:50 重登记才又出现的槽位
		// —— 这里造"只有 14..22 有行"的那一份状态（= 17:12:50 .. 17:50:30 的库）。
		if key == "slot_12" || key == "slot_23" || key == "slot_13" || key == "slot_25" {
			continue
		}
		rows[key] = value
	}
	seedBindingRestoreScope(t, repos, "6", rows)

	state, found, err := Load(context.Background(), repos, "6", LayoutV4)
	if err != nil || !found {
		t.Fatalf("Load found=%t err=%v", found, err)
	}
	if len(state.Records) != 9 {
		t.Fatalf("records = %d, want the 9 rows that actually exist", len(state.Records))
	}
	for _, record := range state.Records {
		if record.Slot == 12 || record.Slot == 23 {
			t.Fatalf("slot %d was resurrected from a forensic key; want no resurrection", record.Slot)
		}
	}
	// 9 行里的 9 条绑定照样被补回来（它们都有行）。
	if len(state.Restored) != 9 {
		t.Fatalf("restored = %d, want 9 (every existing row had a client binding)", len(state.Restored))
	}
	for _, record := range state.Restored {
		if !record.BindingBearing() {
			t.Fatalf("restored row must carry a binding: %+v", record)
		}
	}
}

// TestBindingRestoreNeverOverwritesARealBinding 钉边界 2：已经携带绑定的行原样返回。
func TestBindingRestoreNeverOverwritesARealBinding(t *testing.T) {
	repos, _ := newSkillSwitchFixture(t)
	rows := map[string]string{"slot_12": "0c003600"} // 12 -> 54（真绑定）
	seedBindingRestoreScope(t, repos, "6", rows)

	state, found, err := Load(context.Background(), repos, "6", LayoutV4)
	if err != nil || !found {
		t.Fatalf("Load found=%t err=%v", found, err)
	}
	if len(state.Records) != 1 || state.Records[0].Value != 54 {
		t.Fatalf("records = %v, want the stored binding (12,54)", state.Records)
	}
	if len(state.Restored) != 0 {
		t.Fatalf("restored = %v, want none (the row already carries a binding)", state.Restored)
	}
}

// TestBindingRestoreNeverUnionsAForensicOnlyScope 钉边界 1 的另一半：
// 只有取证键、没有 `slot_*` 行的 scope 仍然投影 **0 帧**（`record_*` 不是状态源）。
func TestBindingRestoreNeverUnionsAForensicOnlyScope(t *testing.T) {
	repos, _ := newSkillSwitchFixture(t)
	values := map[string]string{"source": SourceCurrentEXE}
	for key, value := range bindingRestoreLiveForensics {
		values[key] = value
	}
	if err := repos.Settings.Save(context.Background(), dnfrepo.SettingsRecord{
		Scope:  Scope("6"),
		Values: values,
	}); err != nil {
		t.Fatal(err)
	}
	state, found, err := Load(context.Background(), repos, "6", LayoutV4)
	if err != nil || !found {
		t.Fatalf("Load found=%t err=%v", found, err)
	}
	if len(state.Records) != 0 || len(state.Frames()) != 0 || len(state.Restored) != 0 {
		t.Fatalf("forensic-only scope projected %d records / %d frames / %d restored, want 0/0/0",
			len(state.Records), len(state.Frames()), len(state.Restored))
	}
}

// TestBindingRestoreTieBreakIsDeterministic 钉边界 3：同槽位有多条绑定候选时，
// 取 hex 升序的第一条（确定性、可复现）。
func TestBindingRestoreTieBreakIsDeterministic(t *testing.T) {
	repos, _ := newSkillSwitchFixture(t)
	values := map[string]string{
		"slot_14":         "0e030e00", // 不含绑定
		"record_0e003e00": "x",        // 14 -> 62
		"record_0e003c00": "x",        // 14 -> 60
		"source":          SourceCurrentEXE,
	}
	if err := repos.Settings.Save(context.Background(), dnfrepo.SettingsRecord{
		Scope:  Scope("6"),
		Values: values,
	}); err != nil {
		t.Fatal(err)
	}
	state, _, err := Load(context.Background(), repos, "6", LayoutV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Records) != 1 || state.Records[0].Value != 60 {
		t.Fatalf("restored value = %v, want 60 (`0e003c00` sorts first)", state.Records)
	}
}

// TestNormalizeBindingRestoreTable 钉开关口径：只有回退字面量才离开修复档。
func TestNormalizeBindingRestoreTable(t *testing.T) {
	for _, value := range []string{"", "restore", "RESTORE", "rebind", "unknown"} {
		if got := NormalizeBindingRestore(value); got != BindingRestoreRestore {
			t.Fatalf("NormalizeBindingRestore(%q) = %q, want %q", value, got, BindingRestoreRestore)
		}
	}
	for _, value := range []string{"off", "OFF", "today", "live", "legacy", "rollback", "0", "disable", "none", "keep"} {
		if got := NormalizeBindingRestore(value); got != BindingRestoreOff {
			t.Fatalf("NormalizeBindingRestore(%q) = %q, want %q", value, got, BindingRestoreOff)
		}
	}
	t.Setenv(BindingRestoreEnv, "")
	if got := CurrentBindingRestore(); got != BindingRestoreRestore {
		t.Fatalf("CurrentBindingRestore() = %q, want %q", got, BindingRestoreRestore)
	}
	t.Setenv(BindingRestoreEnv, "off")
	if got := CurrentBindingRestore(); got != BindingRestoreOff {
		t.Fatalf("CurrentBindingRestore() = %q, want %q", got, BindingRestoreOff)
	}
}
