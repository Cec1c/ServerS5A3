package skillswitch

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 「绑定保护」（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`）在仓储层的钉子。
//
// 现场（HK `192.0.2.11`，`packet_log.txt` + `dnf_setting_values`，2026-09-28，
// 逐行原始证据见 `_dist/buff-swap-round3.md` §1/§2）。char 6 = `test78`：
//
//	16:15:41.501Z  边界提交 11 行 **ModeRegister**：`slot_12=0c003600`(54)、
//	               `slot_14=0e003e00`(62) … `slot_23=17003700`(55)
//	               —— `Value` 就是 list 1 里那 11 件换装物品的槽位
//	               （`_dist/weapon-state-final.md` §10.2 的实测）。
//	17:12:45–47Z   客户端推上来 9 条 **ModeConfirm**（`Value == Slot`，**不含绑定信息**）：
//	               `11031100 12031200 0e030e00 0f030f00 10031000 16031600 15031500
//	                13031300 14031400`（槽位 {14..22}）。
//	17:12:48.311Z  一条 `ModeClear`（`052cffff`，Slot=5 / Mode=0x2c / Value=0xffff）
//	               关闭了那个 9 条的窗口 ⇒ `registration_committed=true
//	               registration_kept_count=9 registration_dropped_count=2
//	               dropped_entries=slot_12=0c003600,slot_23=17003700`。
//
// 于是绑定的目标槽 54..64 被覆写成 14..22、`slot_12`/`slot_23` 被删掉，此后每一次重发
// 都是 `pairs=(14,14)…(22,22)`：map 的 value 不再指向 list 1 里的换装物品，
// 面板为空（操作者："1 buff强化为空"），而"清空"发生在**登记那一刻**，
// 比"切换角色"（17:12:53Z）早 5 秒。
//
// 本文件的判据一律是"settings scope 里**实际有哪些 `slot_*` 键、值是什么**"，
// 外加 `window_*` / `pending_*` 记账键，以及 `ApplyResult` / `RegistrationCommit`
// 自己报出来的 kept / dropped / protected（不是任何内存状态）。

// registrationBindLiveBodies 是 16:15:41.501Z 那次提交的 `written_entries`（逐字节照抄
// `packet_log.txt`），= 操作者在面板上选中的那 11 件换装（`Value ∈ {54..64}`）。
var registrationBindLiveBodies = []string{
	"0c003600", // slot 12 / mode 0 / value 54
	"0e003e00", // slot 14 / mode 0 / value 62
	"0f003d00", // slot 15 / mode 0 / value 61
	"10003900", // slot 16 / mode 0 / value 57
	"11003b00", // slot 17 / mode 0 / value 59
	"12003f00", // slot 18 / mode 0 / value 63
	"13003a00", // slot 19 / mode 0 / value 58
	"14004000", // slot 20 / mode 0 / value 64
	"15003c00", // slot 21 / mode 0 / value 60
	"16003800", // slot 22 / mode 0 / value 56
	"17003700", // slot 23 / mode 0 / value 55
}

// registrationBindLiveEntries 是那 11 行按 Slot 升序的落库字面形（手写常量）。
const registrationBindLiveEntries = "slot_12=0c003600,slot_14=0e003e00,slot_15=0f003d00," +
	"slot_16=10003900,slot_17=11003b00,slot_18=12003f00,slot_19=13003a00," +
	"slot_20=14004000,slot_21=15003c00,slot_22=16003800,slot_23=17003700"

// registrationBindConfirmBodies 是 17:12:45.073..17:12:47.763Z 那 9 条记录的**现场顺序**
// （`packet_log.txt` 第 174308..174340 行）。全部 `Value == Slot` ⇒ 不含绑定信息。
var registrationBindConfirmBodies = []string{
	"11031100", // slot 17
	"12031200", // slot 18
	"0e030e00", // slot 14
	"0f030f00", // slot 15
	"10031000", // slot 16
	"16031600", // slot 22
	"15031500", // slot 21
	"13031300", // slot 19
	"14031400", // slot 20
}

// registrationBindConfirmEntries 是那 9 条**回退字面量**下落库的结果（= 今天库里那 9 行）。
const registrationBindConfirmEntries = "slot_14=0e030e00,slot_15=0f030f00,slot_16=10031000," +
	"slot_17=11031100,slot_18=12031200,slot_19=13031300,slot_20=14031400," +
	"slot_21=15031500,slot_22=16031600"

// registrationBindStrayClearBody 是 17:12:48.292Z 那条 `ModeClear` 的真包体
// （`RECV ... len=19 hex=019405130000007cc381903c000000052cffff` ⇒ 包体 `052cffff`）。
// 注意它的 `Slot` 是 **5**，破了 `ModeClear` 文档里"`Slot ≡ 0x21`"那条老不变量
// （全日志实测 `Slot ∈ {3,5,9,33}`）——哨兵判定只看 `Mode`，所以它照样是分隔符。
const registrationBindStrayClearBody = "052cffff"

// registrationBindLiveSlots 是那 11 行的槽位（升序，手写常量）。
var registrationBindLiveSlots = []int{12, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23}

// newRegistrationBindFixture 造**现场形状**的仓储：角色 6 = `test78`（账号 `dnf:10011`）。
//
// 唯一与线上不同的字段是 `job`：线上 char 6 是 job 0，而共享的技能目录 fixture
// （`registrationCatalog`）是按 job 14 建的，而 `Slot` 只用于 PVF 存在性门禁，
// 所以这里让 fixture 角色穿 job 14 —— 它不影响本文件的任何判据。
//
// 前态用 `union` 档落（= 与线上 `current_exe_op1428` 同形的键集合：`slot_*` +
// `record_*` 取证键 + `slot_count`/`source`/`updated_at`），随后**单独**补上
// `window_armed=1` —— 线上那个 scope 在 16:15:18.742Z 收到哨兵时就已经 arm 了，
// 这正是 17:12 那 9 条记录能直接进窗口（而不是进 pending）的原因。
func newRegistrationBindFixture(t *testing.T) (dnfrepo.Group, *Owner, *Owner) {
	t.Helper()
	repos, _ := newRegistrationFixture(t)
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "6",
		AccountID:   "dnf:10011",
		Job:         "14",
		Level:       90,
		Name:        "test78",
	}); err != nil {
		t.Fatal(err)
	}
	seeder, err := NewOwner(repos, OwnerOptions{
		Catalog:      registrationCatalog(t),
		Registration: RegistrationUnion,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range registrationBindLiveBodies {
		applyRegistrationBindBody(t, seeder, body)
	}
	armRegistrationBindScope(t, repos, "6")
	// 默认档 owner：`Registration` / `RegistrationArm` / `RegistrationBind` 全部留空 ⇒
	// 逐次自读 env（这就是"运维一个环境变量就能回退"的落点）。
	live, err := NewOwner(repos, OwnerOptions{Catalog: registrationCatalog(t)})
	if err != nil {
		t.Fatal(err)
	}
	return repos, live, seeder
}

// armRegistrationBindScope 把 `window_armed=1` 补进这个 scope（现场前态的一部分）。
func armRegistrationBindScope(t *testing.T, repos dnfrepo.Group, characterID string) {
	t.Helper()
	stored, found, err := repos.Settings.Load(context.Background(), Scope(characterID))
	if err != nil || !found {
		t.Fatalf("scope %s found=%t err=%v", Scope(characterID), found, err)
	}
	stored = dnfrepo.CloneSettings(stored)
	stored.Values[entryWindowArmed] = "1"
	if err := repos.Settings.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
}

// applyRegistrationBindBody 走**真实落库入口**（`Owner.Apply`）写一条现场记录。
// 账号用线上 char 6 的真实归属 `dnf:10011`（`applyBurstBody` 用 char 3 的 `dnf:10001`）。
func applyRegistrationBindBody(t *testing.T, owner *Owner, body string) ApplyResult {
	t.Helper()
	record, err := ParseRecordIn(LayoutV4, mustDecode(t, body))
	if err != nil {
		t.Fatalf("parse %s: %v", body, err)
	}
	result, err := owner.Apply(context.Background(), "6", "dnf:10011", record)
	if err != nil {
		t.Fatalf("apply %s: %v", body, err)
	}
	return result
}

// registrationBindSlotEntries 把 scope 里的 `slot_*` 渲染成 `slot_<n>=<hex>`（升序）。
func registrationBindSlotEntries(t *testing.T, repos dnfrepo.Group, characterID string) string {
	t.Helper()
	values := storedSlotValuesFor(t, repos, characterID)
	slots := make([]int, 0, len(values))
	for key := range values {
		slot, err := strconv.Atoi(strings.TrimPrefix(key, entrySlotPrefix))
		if err != nil {
			t.Fatalf("slot key %q is not numeric", key)
		}
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	parts := make([]string, 0, len(slots))
	for _, slot := range slots {
		key := slotEntryKey(byte(slot))
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, ",")
}

// TestRegistrationBindConfirmRecordsCannotDestroyTheLoadout 是**本档的主用例**：
// 复现 17:12 的完整序列（9 条不含绑定信息的记录 + 一条陌生的 `ModeClear`），
// 断言操作者登记的那 11 行**一个字节都没变**。
func TestRegistrationBindConfirmRecordsCannotDestroyTheLoadout(t *testing.T) {
	repos, live, _ := newRegistrationBindFixture(t)

	// 前态 = 现场：11 行 ModeRegister + `window_armed=1`。
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
		t.Fatalf("seeded state = %s\nwant             %s", got, registrationBindLiveEntries)
	}

	// 那 9 条 `Value == Slot` 的记录：照旧落取证键、照旧进窗口，但**不许改绑定**。
	for index, body := range registrationBindConfirmBodies {
		result := applyRegistrationBindBody(t, live, body)
		if result.BindingBearing {
			t.Fatalf("record #%d (%s) reported BindingBearing=true; Value == Slot is not a binding",
				index+1, body)
		}
		if !result.BindingPreserved {
			t.Fatalf("record #%d (%s) did not preserve the stored binding (preserved=%q)",
				index+1, body, result.PreservedHex)
		}
		if result.BindingPreserved && result.PreservedHex == "" {
			t.Fatalf("record #%d (%s) preserved a binding but reported no hex", index+1, body)
		}
	}
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
		t.Fatalf("after the 9 confirm records the loadout changed:\n got %s\nwant %s",
			got, registrationBindLiveEntries)
	}
	scope := burstScopeValues(t, repos, "6")
	// `Value == Slot` 的记录**照样**进窗口（"擦除"与"计数"是两件事）：
	// 这保证了后面那条哨兵面对的仍是"有记录的窗口"，也保证了护栏不会把
	// confirm-only 段变成"什么都不记"。
	if got := len(windowSlots(scope)); got != len(registrationBindConfirmBodies) {
		t.Fatalf("window slots = %d, want %d (confirm-only records still count)", got, len(registrationBindConfirmBodies))
	}
	if got := len(windowBindingSlots(scope)); got != 0 {
		t.Fatalf("window binding slots = %v, want none (the burst declares no binding)", got)
	}
	if scope[entryWindowCount] != "9" {
		t.Fatalf("%s = %q, want \"9\"", entryWindowCount, scope[entryWindowCount])
	}

	// 那条陌生的哨兵：提交是发生了（窗口非空），但它**删不掉任何绑定行**。
	clear := applyRegistrationBindBody(t, live, registrationBindStrayClearBody)
	if !clear.Sentinel || clear.RegistrationTrigger != RegistrationTriggerSentinel {
		t.Fatalf("the stray ModeClear must still close the window: %+v", clear)
	}
	if !clear.RegistrationCommitted {
		t.Fatal("a 9-record window must commit (the zero-record guard is not what this is about)")
	}
	if !clear.RegistrationConfirmOnly {
		t.Fatal("this commit must be reported as confirm-only (no binding in the window)")
	}
	if len(clear.RegistrationDropped) != 0 {
		t.Fatalf("the confirm-only commit deleted %d rows: %v", len(clear.RegistrationDropped), clear.RegistrationDropped)
	}
	// 窗口是 {14..22}，窗口**外**那两行正是现场被删掉的 `slot_12`(54) 与 `slot_23`(55)。
	// 它们携带绑定，而这一段登记一条绑定都没声明 ⇒ 保留（`protected`）。
	if got := len(clear.RegistrationProtected); got != 2 {
		t.Fatalf("protected rows = %d, want the 2 bound rows outside the window: %v",
			got, clear.RegistrationProtected)
	}
	if got := skillSwitchTriplesText(clear.RegistrationProtected); got != "(12,0,54)(23,0,55)" {
		t.Fatalf("protected triples = %s, want (12,0,54)(23,0,55) (today's dropped_triples)", got)
	}
	if got := strings.Join(skillSwitchEntryList(clear.RegistrationProtected), ","); got !=
		"slot_12=0c003600,slot_23=17003700" {
		t.Fatalf("protected entries = %s, want slot_12=0c003600,slot_23=17003700", got)
	}
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
		t.Fatalf("the stray sentinel destroyed the loadout:\n got %s\nwant %s",
			got, registrationBindLiveEntries)
	}

	// 边界（"切换角色再进入"）那一步：窗口已关、pending 为空 ⇒ 一个字节都不写。
	commit, committed := commitWindowFor(t, repos, "6", RegistrationWindow)
	if committed {
		t.Fatalf("the boundary must be a no-op after the sentinel commit: %+v", commit)
	}
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
		t.Fatalf("the boundary destroyed the loadout:\n got %s\nwant %s", got, registrationBindLiveEntries)
	}
}

// TestRegistrationBindProtectsRowsOutsideTheWindow 钉"窗口外那些绑定行被**保住**"
// 这一条（主用例里窗口恰好盖住了大部分行，这条用例把窗口挪到旁边）。
func TestRegistrationBindProtectsRowsOutsideTheWindow(t *testing.T) {
	repos, live, _ := newRegistrationBindFixture(t)

	// 只推 3 条不含绑定信息的记录（槽位 20/21/22），第 4 条是它们的收尾哨兵。
	for _, body := range []string{"14031400", "15031500", "16031600"} {
		result := applyRegistrationBindBody(t, live, body)
		if !result.BindingPreserved {
			t.Fatalf("%s did not report binding_preserved", body)
		}
	}
	clear := applyRegistrationBindBody(t, live, registrationBindStrayClearBody)
	if !clear.RegistrationCommitted || !clear.RegistrationConfirmOnly {
		t.Fatalf("expected a confirm-only commit, got %+v", clear)
	}
	want := []int{12, 14, 15, 16, 17, 18, 19, 23}
	if got := len(clear.RegistrationProtected); got != len(want) {
		t.Fatalf("protected rows = %d, want %d (%v)", got, len(want), clear.RegistrationProtected)
	}
	for index, record := range clear.RegistrationProtected {
		if int(record.Slot) != want[index] {
			t.Fatalf("protected[%d].Slot = %d, want %d", index, record.Slot, want[index])
		}
	}
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
		t.Fatalf("state = %s\nwant  %s", got, registrationBindLiveEntries)
	}
	// 被保住的行仍然可 grep：它们的字节留在取证键里。
	scope := burstScopeValues(t, repos, "6")
	if _, present := scope[entryRecordPrefix+"0c003600"]; !present {
		t.Fatal("the protected row must stay traceable through its record_* forensic key")
	}
}

// TestRegistrationBindRollbackLiteralIsByteForByteToday 钉**一键回退字面量**
// `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND=replace`：同一条现场序列下，
// 落库**逐字节等于线上今天那一份**（= §2 的 SQL 原文）。
func TestRegistrationBindRollbackLiteralIsByteForByteToday(t *testing.T) {
	// 本用例钉的是**窗口替换（提交即删行）**那套语义（第二轮起）：把它按回退字面量显式
	// 打开（DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE 默认档 = 修复档 keep，一行都不删）。
	// 默认档下的新判据在 egistration_delete_test.go / skill_switch_inventory_delete_test.go。
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	t.Setenv(RegistrationBindEnv, RegistrationBindReplace)
	repos, live, _ := newRegistrationBindFixture(t)

	for _, body := range registrationBindConfirmBodies {
		result := applyRegistrationBindBody(t, live, body)
		if result.BindingPreserved {
			t.Fatalf("the rollback literal must not preserve bindings (record %s)", body)
		}
	}
	// 哨兵**还没到**：那 9 条只是把同槽位的值覆写掉，`slot_12` / `slot_23` 还在
	// （这正是"覆盖"与"删除"是两件事"的现场形状）。
	if got := len(storedSlotValuesFor(t, repos, "6")); got != len(registrationBindLiveSlots) {
		t.Fatalf("rollback rows before the sentinel = %d, want %d", got, len(registrationBindLiveSlots))
	}
	clear := applyRegistrationBindBody(t, live, registrationBindStrayClearBody)
	if !clear.RegistrationCommitted || clear.RegistrationConfirmOnly {
		t.Fatalf("rollback commit = %+v, want a plain replace commit", clear)
	}
	if len(clear.RegistrationDropped) != 2 {
		t.Fatalf("rollback dropped %d rows, want today's 2: %v", len(clear.RegistrationDropped), clear.RegistrationDropped)
	}
	if got := len(clear.RegistrationProtected); got != 0 {
		t.Fatalf("the rollback literal must never protect anything (got %d)", got)
	}
	// = 现场 SQL（`dnf_setting_values`，`character:6:skill_switch_inventory`）那 9 行。
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindConfirmEntries {
		t.Fatalf("rollback final state = %s\nwant today's           %s", got, registrationBindConfirmEntries)
	}
	// 今天线上 `game-skill-switch-inventory-recorded` 第 174345 行那一段原文的对拍。
	if got := skillSwitchTriplesText(clear.RegistrationDropped); got != "(12,0,54)(23,0,55)" {
		t.Fatalf("rollback dropped_triples = %s, want (12,0,54)(23,0,55)", got)
	}
}

// TestRegistrationBindArmedEmptyWindowStrayClearCommitsNothing 是任务书点名要证伪的
// 那个假设：**"一条陌生的哨兵 + 空窗口把换装清空"**。结论是**它不成立** ——
// `commitRegistrationWindow` 对空窗口直接返回 committed=false，
// 所以两种哨兵形状（老 `212cffff` 与现场新的 `052cffff`）都是零写入。
func TestRegistrationBindArmedEmptyWindowStrayClearCommitsNothing(t *testing.T) {
	repos, live, _ := newRegistrationBindFixture(t)
	for _, body := range []string{registrationSentinelBody, registrationBindStrayClearBody} {
		result := applyRegistrationBindBody(t, live, body)
		if result.RegistrationCommitted {
			t.Fatalf("a %s sentinel with an empty window committed (kept=%v dropped=%v)",
				body, result.RegistrationKept, result.RegistrationDropped)
		}
		if result.RegistrationWindowCount != 0 {
			t.Fatalf("a %s sentinel reported window_count=%d, want 0", body, result.RegistrationWindowCount)
		}
		if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
			t.Fatalf("a %s sentinel changed the state:\n got %s\nwant %s", body, got, registrationBindLiveEntries)
		}
	}
	// 边界那一步同样什么都不做。
	if _, committed := commitWindowFor(t, repos, "6", RegistrationWindow); committed {
		t.Fatal("an empty window must not commit at the boundary either")
	}
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
		t.Fatalf("state = %s\nwant  %s", got, registrationBindLiveEntries)
	}
}

// TestRegistrationBindConfirmOnlyBurstStillRemovesSlotOnlyRows 钉"移除照旧生效"：
// 现场 char 3 @15:35 那一段**全是** `Value == Slot` 的记录，操作者移除的 slot 14
// 必须真的被删掉（这是上一轮的交付行为，本档不许把它弄丢）。
func TestRegistrationBindConfirmOnlyBurstStillRemovesSlotOnlyRows(t *testing.T) {
	// 本用例钉的是**窗口替换（提交即删行）**那套语义（第二轮起）：把它按回退字面量显式
	// 打开（DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE 默认档 = 修复档 keep，一行都不删）。
	// 默认档下的新判据在 egistration_delete_test.go / skill_switch_inventory_delete_test.go。
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	repos, owner := newRegistrationBurstFixture(t, RegistrationArmRecord)
	for _, body := range registrationBurstOperatorBodies {
		applyBurstBody(t, owner, "3", body)
	}
	applyBurstBody(t, owner, "3", registrationSentinelBody)

	commit, committed := commitWindowFor(t, repos, "3", RegistrationWindow)
	if !committed {
		t.Fatal("the pre-arm burst of a single-sentinel registration must still commit")
	}
	if !commit.ConfirmOnly {
		t.Fatal("this burst declares no binding, so the commit must be reported confirm-only")
	}
	if got := len(commit.Kept); got != 8 {
		t.Fatalf("kept %d rows, want the operator's 8", got)
	}
	if got := strings.Join(commit.KeptEntries, ","); got != registrationBurstSelectedEntries {
		t.Fatalf("kept entries = %s\nwant          %s", got, registrationBurstSelectedEntries)
	}
	if got := strings.Join(commit.DroppedEntries, ","); got != "slot_14=0e030e00" {
		t.Fatalf("dropped entries = %s, want slot_14=0e030e00", got)
	}
	if got := len(commit.Protected); got != 0 {
		t.Fatalf("protected = %v, want none (the removed row carries no binding)", commit.Protected)
	}
	if got := storedSlotValuesFor(t, repos, "3"); len(got) != 8 {
		t.Fatalf("stored %d rows, want 8 (values=%v)", len(got), got)
	}
	if _, present := storedSlotValuesFor(t, repos, "3")["slot_14"]; present {
		t.Fatal("the removed slot_14 survived: the confirm-only guard must not block removals")
	}
}

// TestRegistrationBindBindingBearingBurstStillReplaces 钉另一半：**携带绑定**的登记
// 语义与改动前逐字节相同（现场 char 6 @16:15：11 行 ModeRegister 删掉了
// 更早三次登记留下的 slot_3 / slot_13 / slot_25）。
func TestRegistrationBindBindingBearingBurstStillReplaces(t *testing.T) {
	// 本用例钉的是**窗口替换（提交即删行）**那套语义（第二轮起）：把它按回退字面量显式
	// 打开（DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE 默认档 = 修复档 keep，一行都不删）。
	// 默认档下的新判据在 egistration_delete_test.go / skill_switch_inventory_delete_test.go。
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	repos, live, seeder := newRegistrationBindFixture(t)
	// 先把 16:15 之前那三行陈旧数据补上（`union` 档 ⇒ 不碰任何窗口记账键）。
	for _, body := range []string{"03012f00", "0d030d00", "19031900"} {
		applyRegistrationBindBody(t, seeder, body)
	}
	if got := len(storedSlotValuesFor(t, repos, "6")); got != len(registrationLiveSlots) {
		t.Fatalf("seed rows = %d, want %d", got, len(registrationLiveSlots))
	}
	// 11 条 ModeRegister（= 现场 16:15:20..23Z 那一段，全部携带绑定）。
	for _, body := range registrationBindLiveBodies {
		result := applyRegistrationBindBody(t, live, body)
		if !result.BindingBearing {
			t.Fatalf("%s must be classified as binding-bearing", body)
		}
		if result.BindingPreserved {
			t.Fatalf("%s carries a binding: nothing may be preserved for it", body)
		}
	}
	scope := burstScopeValues(t, repos, "6")
	if got := len(windowBindingSlots(scope)); got != len(registrationBindLiveBodies) {
		t.Fatalf("window binding slots = %d, want %d", got, len(registrationBindLiveBodies))
	}
	// 收尾哨兵（现场 16:15:41.5Z 是边界提交；这里用哨兵关闭同一批窗口，判据相同）。
	clear := applyRegistrationBindBody(t, live, registrationSentinelBody)
	if !clear.RegistrationCommitted || clear.RegistrationConfirmOnly {
		t.Fatalf("commit = %+v, want a plain replace commit", clear)
	}
	if got := len(clear.RegistrationProtected); got != 0 {
		t.Fatalf("protected = %v, want none", clear.RegistrationProtected)
	}
	if got := strings.Join(skillSwitchEntryList(clear.RegistrationDropped), ","); got !=
		"slot_3=03012f00,slot_13=0d030d00,slot_25=19031900" {
		t.Fatalf("dropped entries = %s, want the three stale rows", got)
	}
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
		t.Fatalf("state = %s\nwant  %s", got, registrationBindLiveEntries)
	}
}

// skillSwitchEntryList 把一批记录渲染成 `slot_<n>=<hex>`（读取当前落库字节）。
func skillSwitchEntryList(records []Record) []string {
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, slotEntryKey(record.Slot)+"="+record.Hex())
	}
	return parts
}

// TestRegistrationBindPromotionKeepsTheClassification 钉"分类标记跟着 pending 一起被
// 提升"：arm 之前那一段（`pending_*`）里的记录在边界上被提升成窗口时，
// "这一段有没有声明绑定"必须**一起**过去，否则护栏会在提升处失效。
func TestRegistrationBindPromotionKeepsTheClassification(t *testing.T) {
	repos, _, seeder := newRegistrationBindFixture(t)
	// 取消 arm，重建成"arm 之前"的前态（现场 15:35 char 3 的形状）。
	stored, found, err := repos.Settings.Load(context.Background(), Scope("6"))
	if err != nil || !found {
		t.Fatalf("scope found=%t err=%v", found, err)
	}
	stored = dnfrepo.CloneSettings(stored)
	delete(stored.Values, entryWindowArmed)
	if err := repos.Settings.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	live, err := NewOwner(repos, OwnerOptions{Catalog: registrationCatalog(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range registrationBindConfirmBodies {
		result := applyRegistrationBindBody(t, live, body)
		if result.RegistrationPendingCount == 0 {
			t.Fatalf("%s must land in the pre-arm burst (pending)", body)
		}
	}
	scope := burstScopeValues(t, repos, "6")
	if len(windowSlots(scope)) != 0 {
		t.Fatalf("no window key may exist before the sentinel (scope=%v)", scope)
	}
	if got := len(pendingBurstSlots(scope)); got != len(registrationBindConfirmBodies) {
		t.Fatalf("pending = %d, want %d", got, len(registrationBindConfirmBodies))
	}
	// 收尾哨兵：第一次见到哨兵只 arm（窗口为空 ⇒ 不提交），pending 原样留着
	// —— 现场 15:35 char 3 就是这一形，只是那时紧跟着边界。
	armed := applyRegistrationBindBody(t, live, registrationSentinelBody)
	if !windowArmed(burstScopeValues(t, repos, "6")) {
		t.Fatal("the trailing sentinel must arm the window bookkeeping")
	}
	if armed.RegistrationCommitted {
		t.Fatalf("the arming sentinel must not commit anything: %+v", armed)
	}
	commit, committed := commitWindowFor(t, repos, "6", RegistrationWindow)
	if !committed {
		t.Fatal("the boundary must promote and commit the pre-arm burst")
	}
	if commit.Trigger != RegistrationTriggerBoundaryPreArmBurst {
		t.Fatalf("trigger = %q, want %q", commit.Trigger, RegistrationTriggerBoundaryPreArmBurst)
	}
	if !commit.ConfirmOnly {
		t.Fatal("the promoted burst declares no binding: the commit must stay confirm-only")
	}
	if len(commit.Dropped) != 0 {
		t.Fatalf("drops = %v, want none (the confirm-only guard must survive the promotion)", commit.Dropped)
	}
	// 窗口 {14..22} 之外的两行（slot_12 / slot_23）携带绑定 ⇒ 被保住。
	if got := strings.Join(skillSwitchEntryList(commit.Protected), ","); got !=
		"slot_12=0c003600,slot_23=17003700" {
		t.Fatalf("protected entries = %s, want slot_12=0c003600,slot_23=17003700", got)
	}
	if got := registrationBindSlotEntries(t, repos, "6"); got != registrationBindLiveEntries {
		t.Fatalf("state = %s\nwant  %s", got, registrationBindLiveEntries)
	}
	_ = seeder
}

// TestRegistrationBindBearingFlagMatchesTheLiveShapes 钉分类函数本身：
// 现场两种形态 18/18 各归一边，哨兵不算绑定。
func TestRegistrationBindBearingFlagMatchesTheLiveShapes(t *testing.T) {
	for _, body := range registrationBindLiveBodies {
		record, err := ParseRecordIn(LayoutV4, mustDecode(t, body))
		if err != nil {
			t.Fatalf("parse %s: %v", body, err)
		}
		if !record.BindingBearing() {
			t.Fatalf("%s (Mode 0, Value != Slot) must be binding-bearing", body)
		}
	}
	for _, body := range append(append([]string{}, registrationBindConfirmBodies...),
		registrationBurstOperatorBodies...) {
		record, err := ParseRecordIn(LayoutV4, mustDecode(t, body))
		if err != nil {
			t.Fatalf("parse %s: %v", body, err)
		}
		if record.BindingBearing() {
			t.Fatalf("%s (Value == Slot) must NOT be binding-bearing", body)
		}
	}
	for _, body := range []string{registrationSentinelBody, registrationBindStrayClearBody} {
		record, err := ParseRecordIn(LayoutV4, mustDecode(t, body))
		if err != nil {
			t.Fatalf("parse %s: %v", body, err)
		}
		if !record.Sentinel() {
			t.Fatalf("%s must be a sentinel", body)
		}
		if record.BindingBearing() {
			t.Fatalf("%s is a sentinel and must not be binding-bearing", body)
		}
	}
}

// TestNormalizeRegistrationBindTable 钉开关口径：只有回退字面量才离开修复档。
func TestNormalizeRegistrationBindTable(t *testing.T) {
	for _, value := range []string{"", "keep", "bind", "KEYS", "protected", "unknown"} {
		if got := NormalizeRegistrationBind(value); got != RegistrationBindKeep {
			t.Fatalf("NormalizeRegistrationBind(%q) = %q, want %q", value, got, RegistrationBindKeep)
		}
	}
	for _, value := range []string{"replace", "REPLACE", "overwrite", "today", "live", "legacy", "rollback", "0", "off", "disable"} {
		if got := NormalizeRegistrationBind(value); got != RegistrationBindReplace {
			t.Fatalf("NormalizeRegistrationBind(%q) = %q, want %q", value, got, RegistrationBindReplace)
		}
	}
	// nil owner / 未设 env ⇒ 修复档（房子口径：默认档就是修复档）。
	t.Setenv(RegistrationBindEnv, "")
	var nilOwner *Owner
	if got := nilOwner.registrationBind(); got != RegistrationBindKeep {
		t.Fatalf("nil owner bind tier = %q, want %q", got, RegistrationBindKeep)
	}
	if got := CurrentRegistrationBind(); got != RegistrationBindKeep {
		t.Fatalf("CurrentRegistrationBind() = %q, want %q", got, RegistrationBindKeep)
	}
	t.Setenv(RegistrationBindEnv, "replace")
	if got := CurrentRegistrationBind(); got != RegistrationBindReplace {
		t.Fatalf("CurrentRegistrationBind() = %q, want %q", got, RegistrationBindReplace)
	}
}
