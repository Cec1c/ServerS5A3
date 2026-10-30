package skillswitch

import (
	"context"
	"strconv"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

// 「登记窗口」（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION`）在**仓储层**的钉子。
//
// 判据一律是"settings scope 里**实际有哪些 `slot_*` 键、值是什么**"（不是任何标志位）。
// 现场形状取自 HK `192.0.2.11` 的 `character:6:skill_switch_inventory`
// （2026-09-28T13:52:44.958764974Z，14 条 `slot_*`），见
// `_dist/buff-swap-registration.md` §1 的原始 SQL 输出。

// registrationLiveBodies 是那 14 条真包体（4 字节规范形；`slot_3` 在库里是改动前的
// 6 字节行，dual-read 之后字段相同）。
var registrationLiveBodies = []string{
	"03012f00", // slot 3  / mode 1 / value 47
	"0c003600", // slot 12 / mode 0 / value 54
	"0d030d00", // slot 13 / mode 3 / value 13
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
	"19031900", // slot 25 / mode 3 / value 25
}

// registrationLiveSlots 是那 14 条按 Slot 升序的槽位（手写常量）。
var registrationLiveSlots = []int{3, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 25}

// registrationSentinelBody 是现场哨兵的规范 4 字节包体
// （6 字节形 `2600212cffff` = 2 字节导线前缀 + 它）。
const registrationSentinelBody = "212cffff"

// registrationSecondBurstBodies 是"第二次登记"：去掉 12 / 19 / 22，并把 14 的绑定
// 从 62 改成 60、把 21 换成 Mode 3 形 —— 于是提交时必须**删掉 6 行、留下 8 行**。
var registrationSecondBurstBodies = []string{
	"0e003c00", // slot 14 / mode 0 / value 60
	"0f003d00", // slot 15 / mode 0 / value 61
	"10003900", // slot 16 / mode 0 / value 57
	"11003b00", // slot 17 / mode 0 / value 59
	"12003f00", // slot 18 / mode 0 / value 63
	"14004000", // slot 20 / mode 0 / value 64
	"15031500", // slot 21 / mode 3 / value 21
	"17003700", // slot 23 / mode 0 / value 55
}

var registrationSecondBurstKeptSlots = []int{14, 15, 16, 17, 18, 20, 21, 23}

var registrationSecondBurstDroppedSlots = []int{3, 12, 13, 19, 22, 25}

// registrationCatalog 是 job 14 的目录，含那 14 个槽位引用的全部技能号
// （共享的 jobScopedCatalog 只含现场最小集合，刻意不动它）。
func registrationCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	source := skillSwitchTestSource{}
	list := ""
	for _, id := range registrationLiveSlots {
		doc := "skill/job14/skill" + strconv.Itoa(id) + ".skl"
		list += strconv.Itoa(id) + " `" + doc + "`\n"
		source[doc] = "[skill type]\n`active`\n"
	}
	source["skill/skilllist.lst"] = "14 `job14.lst`\n"
	source["skill/job14.lst"] = list
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// newRegistrationFixture 造一个 job 14 角色 + 注入目录的 owner（默认档 = 自读 env）。
func newRegistrationFixture(t *testing.T) (dnfrepo.Group, *Owner) {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "1",
		AccountID:   "dnf:10001",
		Job:         "14",
		Level:       90,
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: registrationCatalog(t)})
	if err != nil {
		t.Fatal(err)
	}
	return repos, owner
}

func applyRegistrationBody(t *testing.T, owner *Owner, body string) ApplyResult {
	t.Helper()
	record, err := ParseRecordIn(LayoutV4, mustDecode(t, body))
	if err != nil {
		t.Fatalf("parse %s: %v", body, err)
	}
	result, err := owner.Apply(context.Background(), "1", "dnf:10001", record)
	if err != nil {
		t.Fatalf("apply %s: %v", body, err)
	}
	return result
}

// storedSlotValues 读回 scope 里的 `slot_<n>=<hex>`（`slot_count` 这类汇总键排除）。
func storedSlotValues(t *testing.T, repos dnfrepo.Group) map[string]string {
	t.Helper()
	stored, found, err := repos.Settings.Load(context.Background(), Scope("1"))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("scope not found")
	}
	out := make(map[string]string, len(stored.Values))
	for key, value := range stored.Values {
		if strings.HasPrefix(key, entrySlotPrefix) {
			if _, err := strconv.Atoi(strings.TrimPrefix(key, entrySlotPrefix)); err == nil {
				out[key] = value
			}
		}
	}
	return out
}

func storedScopeValues(t *testing.T, repos dnfrepo.Group) map[string]string {
	t.Helper()
	stored, found, err := repos.Settings.Load(context.Background(), Scope("1"))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("scope not found")
	}
	out := make(map[string]string, len(stored.Values))
	for key, value := range stored.Values {
		out[key] = value
	}
	return out
}

// commitWindow 走 `CommitRegistration`（边界那一步）。
func commitWindow(t *testing.T, repos dnfrepo.Group, mode string) (RegistrationCommit, bool) {
	t.Helper()
	commit, committed, err := CommitRegistration(context.Background(), repos, "1", LayoutV4, mode)
	if err != nil {
		t.Fatalf("CommitRegistration: %v", err)
	}
	return commit, committed
}

// TestRegistrationWindowCommitAppliesRemovals 是仓储层的主用例：
// 14 条登记 → 第二段（开局哨兵 + 8 条）→ 边界提交 ⇒ scope 里**恰好**剩 8 行，
// 被移除的 6 行真的没了，改过的绑定按新值落库。
func TestRegistrationWindowCommitAppliesRemovals(t *testing.T) {
	// 本用例钉的是**窗口替换（提交即删行）**那套语义（第二轮起）：把它按回退字面量显式
	// 打开（DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE 默认档 = 修复档 keep，一行都不删）。
	// 默认档下的新判据在 egistration_delete_test.go / skill_switch_inventory_delete_test.go。
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	repos, owner := newRegistrationFixture(t)
	for _, body := range registrationLiveBodies {
		applyRegistrationBody(t, owner, body)
	}
	// 第一段之后（还没见过哨兵）不算任何窗口：连 `window_*` 键都不该有。
	for key := range storedScopeValues(t, repos) {
		if strings.HasPrefix(key, entryWindowPrefix) {
			t.Fatalf("before the first sentinel the scope must gain no window key (found %q)", key)
		}
	}
	if got := len(storedSlotValues(t, repos)); got != len(registrationLiveSlots) {
		t.Fatalf("first registration stored %d rows, want %d", got, len(registrationLiveSlots))
	}

	// 第二段：开局哨兵（现场形）+ 8 条记录。
	applyRegistrationBody(t, owner, registrationSentinelBody)
	if got := storedScopeValues(t, repos)[entryWindowArmed]; got != "1" {
		t.Fatalf("the first sentinel must arm the window bookkeeping (%s=%q)", entryWindowArmed, got)
	}
	for _, body := range registrationSecondBurstBodies {
		applyRegistrationBody(t, owner, body)
	}
	// 窗口打开期间是并集（加法安全）：14 行仍在，只有值被改写。
	duringBurst := storedSlotValues(t, repos)
	if len(duringBurst) != len(registrationLiveSlots) {
		t.Fatalf("while the window is open the store must stay additive: %d rows, want %d",
			len(duringBurst), len(registrationLiveSlots))
	}
	// `slot_14` 那一条是**携带绑定**的记录（`0e003c00`，mode 0 / value 60）⇒ 照旧改写；
	// `slot_21` 那一条是 `15031500`（mode 3 / value 21，`Value == Slot`，**不含绑定信息**）
	// ⇒ 默认档（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND` 未设 / `keep`）**不覆盖**
	// 已落库的绑定 `15003c00`（这就是 2026-09-28T17:12:45Z 现场那 9 条 `ModeConfirm`
	// 把 `slot_14..22` 的绑定 54..64 全部改坏的那条路径，见
	// `_dist/buff-swap-round3.md`）。回退字面量 `replace` 下这里仍是 `15031500`
	// （钉在 `registration_bind_test.go`）。
	if duringBurst["slot_14"] != "0e003c00" || duringBurst["slot_21"] != "15003c00" {
		t.Fatalf("rows = %q / %q, want 0e003c00 (rebind) / 15003c00 (binding preserved)",
			duringBurst["slot_14"], duringBurst["slot_21"])
	}

	commit, committed := commitWindow(t, repos, RegistrationWindow)
	if !committed {
		t.Fatal("an armed window with 8 records must commit at the boundary")
	}
	if commit.WindowRecords != len(registrationSecondBurstKeptSlots) || commit.SlotCount != len(registrationSecondBurstKeptSlots) {
		t.Fatalf("commit = %d window records / %d slot count, want %d",
			commit.WindowRecords, commit.SlotCount, len(registrationSecondBurstKeptSlots))
	}
	if len(commit.Dropped) != len(registrationSecondBurstDroppedSlots) {
		t.Fatalf("commit dropped %d rows, want %d (%v)",
			len(commit.Dropped), len(registrationSecondBurstDroppedSlots), commit.Dropped)
	}

	after := storedSlotValues(t, repos)
	if len(after) != len(registrationSecondBurstKeptSlots) {
		t.Fatalf("committed slot rows = %d, want %d (values=%v)",
			len(after), len(registrationSecondBurstKeptSlots), after)
	}
	for _, slot := range registrationSecondBurstDroppedSlots {
		key := slotEntryKey(byte(slot))
		if value, present := after[key]; present {
			t.Fatalf("removed %s survived the commit (=%q)", key, value)
		}
	}
	for _, slot := range registrationSecondBurstKeptSlots {
		if _, present := after[slotEntryKey(byte(slot))]; !present {
			t.Fatalf("selected %s missing after the commit (values=%v)", slotEntryKey(byte(slot)), after)
		}
	}
	// 落库字面形与日志字段逐字对应（`slot_21` 是"不含绑定信息的记录没有改掉它"那一行）。
	if strings.Join(commit.KeptEntries, ",") !=
		"slot_14=0e003c00,slot_15=0f003d00,slot_16=10003900,slot_17=11003b00,"+
			"slot_18=12003f00,slot_20=14004000,slot_21=15003c00,slot_23=17003700" {
		t.Fatalf("kept entries = %v", commit.KeptEntries)
	}
	if strings.Join(commit.DroppedEntries, ",") !=
		"slot_3=03012f00,slot_12=0c003600,slot_13=0d030d00,slot_19=13003a00,slot_22=16003800,slot_25=19031900" {
		t.Fatalf("dropped entries = %v", commit.DroppedEntries)
	}
	// 被删掉的行仍然留在取证键里（可 grep、可追溯）。
	scope := storedScopeValues(t, repos)
	for _, body := range []string{"0c003600", "0d030d00", "13003a00"} {
		if _, present := scope[entryRecordPrefix+body]; !present {
			t.Fatalf("dropped row %s must stay traceable through %s", body, entryRecordPrefix+body)
		}
	}
	// 窗口已关：再提交一次什么都不做（不重复写库）。
	if _, committed := commitWindow(t, repos, RegistrationWindow); committed {
		t.Fatal("a closed window must not commit again")
	}
}

// TestRegistrationWindowEmptyWindowNeverCommits 钉**裸哨兵不能清空换装表**。
func TestRegistrationWindowEmptyWindowNeverCommits(t *testing.T) {
	repos, owner := newRegistrationFixture(t)
	for _, body := range registrationLiveBodies {
		applyRegistrationBody(t, owner, body)
	}
	before := storedSlotValues(t, repos)
	for index := 0; index < 3; index++ {
		result := applyRegistrationBody(t, owner, registrationSentinelBody)
		if result.RegistrationCommitted {
			t.Fatalf("bare sentinel #%d reported a commit; an empty window must never commit", index+1)
		}
		if len(result.RegistrationDropped) != 0 {
			t.Fatalf("bare sentinel #%d dropped %v", index+1, result.RegistrationDropped)
		}
	}
	if _, committed := commitWindow(t, repos, RegistrationWindow); committed {
		t.Fatal("a boundary with an empty window must not commit")
	}
	after := storedSlotValues(t, repos)
	if len(after) != len(before) {
		t.Fatalf("bare sentinels changed the slot set: %d rows, want %d", len(after), len(before))
	}
	for key, value := range before {
		if after[key] != value {
			t.Fatalf("%s changed from %q to %q across bare sentinels", key, value, after[key])
		}
	}
}

// TestRegistrationWindowNoSentinelWritesNoKeys 钉**没有哨兵 ⇒ 一个键都不新增**
// （某些客户端版本可能不发哨兵；那条路径必须是真的逐字节等于改动前）。
func TestRegistrationWindowNoSentinelWritesNoKeys(t *testing.T) {
	repos, owner := newRegistrationFixture(t)
	// 先按"会发哨兵"的客户端记一段（哨兵在最前面 = 现场形状）。
	for _, body := range append([]string{registrationSentinelBody}, registrationLiveBodies...) {
		applyRegistrationBody(t, owner, body)
	}
	scope := storedScopeValues(t, repos)
	if !strings.HasPrefix(scope[entryWindowArmed], "1") {
		t.Fatalf("a sentinel-seeing character must be armed (%q=%q)", entryWindowArmed, scope[entryWindowArmed])
	}
	// 关掉角色 1 的窗口（提交那 14 条，一条都不删）。
	if _, committed := commitWindow(t, repos, RegistrationWindow); !committed {
		t.Fatal("character 1's open window must commit at the boundary")
	}
	if got := len(storedSlotValuesFor(t, repos, "1")); got != len(registrationLiveSlots) {
		t.Fatalf("character 1 committed %d rows, want %d", got, len(registrationLiveSlots))
	}

	// 新角色 2：从不发哨兵。
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "2", AccountID: "dnf:10001", Job: "14", Level: 90,
	}); err != nil {
		t.Fatal(err)
	}
	for _, body := range registrationSecondBurstBodies {
		record, err := ParseRecordIn(LayoutV4, mustDecode(t, body))
		if err != nil {
			t.Fatal(err)
		}
		result, err := owner.Apply(context.Background(), "2", "dnf:10001", record)
		if err != nil {
			t.Fatalf("character 2 apply %s: %v", body, err)
		}
		if result.RegistrationWindowCount != 0 || result.RegistrationMode != RegistrationWindow {
			t.Fatalf("character 2 must not open a window: %+v", result)
		}
	}
	stored, found, err := repos.Settings.Load(context.Background(), Scope("2"))
	if err != nil || !found {
		t.Fatalf("character 2 scope found=%t err=%v", found, err)
	}
	for key := range stored.Values {
		if strings.HasPrefix(key, entryWindowPrefix) {
			t.Fatalf("a sentinel-less character must gain no window key (found %q)", key)
		}
	}
	if _, committed, err := CommitRegistration(context.Background(), repos, "2", LayoutV4, RegistrationWindow); err != nil || committed {
		t.Fatalf("character 2 commit committed=%t err=%v, want false/nil (nothing armed)", committed, err)
	}
	if got := len(storedSlotValuesFor(t, repos, "2")); got != len(registrationSecondBurstBodies) {
		t.Fatalf("character 2 stored %d rows, want %d", got, len(registrationSecondBurstBodies))
	}
	// 角色 1 的窗口已经关了：再提交一次必须什么都不做。
	if _, committed := commitWindow(t, repos, RegistrationWindow); committed {
		t.Fatal("character 1's window is closed; a second commit must be a no-op")
	}
}

// storedSlotValuesFor 是 storedSlotValues 的按角色版本。
func storedSlotValuesFor(t *testing.T, repos dnfrepo.Group, characterID string) map[string]string {
	t.Helper()
	stored, found, err := repos.Settings.Load(context.Background(), Scope(characterID))
	if err != nil || !found {
		t.Fatalf("scope %s found=%t err=%v", Scope(characterID), found, err)
	}
	out := make(map[string]string, len(stored.Values))
	for key, value := range stored.Values {
		if strings.HasPrefix(key, entrySlotPrefix) {
			if _, err := strconv.Atoi(strings.TrimPrefix(key, entrySlotPrefix)); err == nil {
				out[key] = value
			}
		}
	}
	return out
}

// TestRegistrationUnionModeIsByteForByteToday 钉**一键回退**：`union` 档下
// `Apply` 整段跳过窗口记账（连 `window_*` 都不建），`CommitRegistration` 一个字节都不写，
// 状态键集合永远是并集 —— 逐字节等于改动前。
func TestRegistrationUnionModeIsByteForByteToday(t *testing.T) {
	for _, literal := range []string{
		RegistrationUnion, "off", "TODAY", "legacy", "rollback", "additive", "0", "disable",
	} {
		t.Setenv(RegistrationModeEnv, literal)
		repos, owner := newRegistrationFixture(t)
		if got := owner.registrationMode(); got != RegistrationUnion {
			t.Fatalf("literal %q owner mode = %q, want %q", literal, got, RegistrationUnion)
		}
		for _, body := range append([]string{registrationSentinelBody}, registrationLiveBodies...) {
			result := applyRegistrationBody(t, owner, body)
			if result.RegistrationMode != "" || result.RegistrationWindowCount != 0 || result.RegistrationCommitted {
				t.Fatalf("literal %q must not touch the registration window: %+v", literal, result)
			}
		}
		for _, body := range registrationSecondBurstBodies {
			applyRegistrationBody(t, owner, body)
		}
		// 第二段的 8 条里 12/19/22 没被重发，但一条都不许被删。
		stored := storedSlotValues(t, repos)
		if len(stored) != len(registrationLiveSlots) {
			t.Fatalf("literal %q: union tier stored %d rows, want %d", literal, len(stored), len(registrationLiveSlots))
		}
		for _, slot := range registrationSecondBurstDroppedSlots {
			if _, present := stored[slotEntryKey(byte(slot))]; !present {
				t.Fatalf("literal %q: union tier dropped %s", literal, slotEntryKey(byte(slot)))
			}
		}
		for key := range storedScopeValues(t, repos) {
			if strings.HasPrefix(key, entryWindowPrefix) {
				t.Fatalf("literal %q: union tier wrote window key %q", literal, key)
			}
		}
		if _, committed, err := CommitRegistration(context.Background(), repos, "1", LayoutV4, literal); err != nil || committed {
			t.Fatalf("literal %q: CommitRegistration committed=%t err=%v, want false/nil", literal, committed, err)
		}
	}
	t.Setenv(RegistrationModeEnv, "")
	if got := CurrentRegistrationMode(); got != RegistrationWindow {
		t.Fatalf("unset env = %q, want the fixed tier %q", got, RegistrationWindow)
	}
}

// TestRegistrationSentinelOnlyModeNeverCommitsAtBoundary 钉保守档只在哨兵上提交。
func TestRegistrationSentinelOnlyModeNeverCommitsAtBoundary(t *testing.T) {
	// 本用例钉的是**窗口替换（提交即删行）**那套语义（第二轮起）：把它按回退字面量显式
	// 打开（DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE 默认档 = 修复档 keep，一行都不删）。
	// 默认档下的新判据在 egistration_delete_test.go / skill_switch_inventory_delete_test.go。
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	t.Setenv(RegistrationModeEnv, RegistrationSentinelOnly)
	repos, owner := newRegistrationFixture(t)
	for _, body := range registrationLiveBodies {
		applyRegistrationBody(t, owner, body)
	}
	applyRegistrationBody(t, owner, registrationSentinelBody)
	for _, body := range registrationSecondBurstBodies {
		applyRegistrationBody(t, owner, body)
	}
	if _, committed := commitWindow(t, repos, RegistrationSentinelOnly); committed {
		t.Fatal("sentinel_only must not commit at a boundary")
	}
	if got := len(storedSlotValues(t, repos)); got != len(registrationLiveSlots) {
		t.Fatalf("sentinel_only stored %d rows, want the untouched %d", got, len(registrationLiveSlots))
	}
	// 下一个哨兵才提交（"移除晚一轮生效"）。
	result := applyRegistrationBody(t, owner, registrationSentinelBody)
	if !result.RegistrationCommitted {
		t.Fatal("the next sentinel must commit the pending window in the sentinel_only tier")
	}
	if got := len(storedSlotValues(t, repos)); got != len(registrationSecondBurstKeptSlots) {
		t.Fatalf("after the sentinel commit: %d rows, want %d", got, len(registrationSecondBurstKeptSlots))
	}
}

// TestNormalizeRegistrationModeTable 钉开关口径：只有回退/保守字面量才离开修复档。
func TestNormalizeRegistrationModeTable(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: RegistrationWindow},
		{value: "window", want: RegistrationWindow},
		{value: "  WINDOW  ", want: RegistrationWindow},
		{value: "on", want: RegistrationWindow},
		{value: "fix", want: RegistrationWindow},
		{value: "burst", want: RegistrationWindow},
		{value: "typo", want: RegistrationWindow},
		{value: RegistrationSentinelOnly, want: RegistrationSentinelOnly},
		{value: "SENTINEL_ONLY", want: RegistrationSentinelOnly},
		{value: "sentinel", want: RegistrationSentinelOnly},
		{value: "safe", want: RegistrationSentinelOnly},
		{value: RegistrationUnion, want: RegistrationUnion},
		{value: "off", want: RegistrationUnion},
		{value: "TODAY", want: RegistrationUnion},
		{value: "legacy", want: RegistrationUnion},
		{value: "rollback", want: RegistrationUnion},
		{value: "additive", want: RegistrationUnion},
		{value: "0", want: RegistrationUnion},
		{value: "disable", want: RegistrationUnion},
	} {
		if got := NormalizeRegistrationMode(test.value); got != test.want {
			t.Fatalf("NormalizeRegistrationMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
	if got := ((*Owner)(nil)).registrationMode(); got != RegistrationWindow {
		t.Fatalf("nil owner mode = %q, want %q", got, RegistrationWindow)
	}
}

// TestRegistrationWindowSentinelFirstOrLast 钉"分隔符"这条规则**不依赖**哨兵到底是
// "开始"还是"结束"：同一个客户端（已经见过至少一次哨兵）把同一批记录推上来，
// 哨兵在最前面与在最后面，提交出来的集合是同一批。
//
// 唯一的前提是"记账开关"已经打开（这个角色见过一次哨兵）—— 那是 arming 的定义，
// 不是本用例的漏洞：没见过哨兵的客户端根本不记账（见
// `TestRegistrationWindowNoSentinelWritesNoKeys`）。
func TestRegistrationWindowSentinelFirstOrLast(t *testing.T) {
	// 本用例钉的是**窗口替换（提交即删行）**那套语义（第二轮起）：把它按回退字面量显式
	// 打开（DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE 默认档 = 修复档 keep，一行都不删）。
	// 默认档下的新判据在 egistration_delete_test.go / skill_switch_inventory_delete_test.go。
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	for _, test := range []struct {
		name     string
		sequence []string
	}{
		{
			name:     "sentinel first (live 13:52 char6 shape)",
			sequence: append([]string{registrationSentinelBody}, registrationSecondBurstBodies...),
		},
		{
			name:     "sentinel last (live 09:08 char6 shape)",
			sequence: append(append([]string(nil), registrationSecondBurstBodies...), registrationSentinelBody),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repos, owner := newRegistrationFixture(t)
			// 先按现场形状把"这个客户端会发哨兵"这件事记下来，并在边界上关掉这一段
			// （现场两次登记之间一定隔着一个重投影边界）。
			for _, body := range append([]string{registrationSentinelBody}, registrationLiveBodies...) {
				applyRegistrationBody(t, owner, body)
			}
			if _, committed := commitWindow(t, repos, RegistrationWindow); !committed {
				t.Fatal("the first registration must commit at the boundary")
			}
			for _, body := range test.sequence {
				applyRegistrationBody(t, owner, body)
			}
			// 边界关闭仍然打开的窗口（"哨兵在最后"那种形状其实已经被哨兵关掉了，
			// 这一步是幂等的 no-op；两种形状最终状态必须一样）。
			if _, committed, err := CommitRegistration(context.Background(), repos, "1", LayoutV4, RegistrationWindow); err != nil {
				t.Fatalf("CommitRegistration: %v", err)
			} else if committed && test.sequence[len(test.sequence)-1] == registrationSentinelBody {
				t.Fatal("a trailing sentinel already closed the window; the boundary must be a no-op")
			}
			if got := len(storedSlotValues(t, repos)); got != len(registrationSecondBurstKeptSlots) {
				t.Fatalf("committed %d rows, want %d", got, len(registrationSecondBurstKeptSlots))
			}
		})
	}
}
