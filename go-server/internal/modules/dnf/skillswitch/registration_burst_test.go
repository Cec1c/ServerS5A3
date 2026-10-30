package skillswitch

import (
	"context"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 「arm 之前那一段 burst」（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`）在仓储层的钉子。
//
// 现场（HK `192.0.2.11`，`packet_log.txt`，2026-09-28，逐行原始证据见
// `_dist/buff-swap-registration-commit.md` §1/§2）：
//
//	char 3 = `test2`（job 14）在 15:35:02.929..15:35:05.201Z 推了 **8 条** op1428 记录
//	（全部 Mode 3：槽位 13/15/16/17/18/19/20/21），**15:35:06.848Z 才推那一个**
//	`21 2c ff ff` 哨兵（= 这一段的最后一个包），15:35:09.908Z 是重投影边界。
//	那 8 条到达时这个 scope 里 `window_armed` **还不存在**（上一次哨兵 14:26:13.673Z
//	是旧二进制处理的，只留下 `sentinel_212cffff` 取证键），于是 8 条一条都没进窗口
//	（`registration_window_count=0`），收尾哨兵与随后的边界面对的都是**空窗口**
//	⇒ `game-skill-switch-inventory-registration-commit` 全日志 0 条，
//	库里仍是并集：`slot_14 = 0e030e00`（= 操作者刚刚移除的那个槽位）。
//
// 本文件的判据一律是"settings scope 里**实际有哪些 `slot_*` 键、值是什么**"，
// 加上 `pending_*` / `window_*` 记账键的有无（不是任何内存状态）。

// registrationBurstOperatorRows 是现场 `character:3:skill_switch_inventory` 里
// 14:26 那次登记留下的 9 行（SQL 原文照抄，见报告 §2）。
var registrationBurstOperatorRows = map[string]string{
	"slot_13": "0d030d00",
	"slot_14": "0e030e00",
	"slot_15": "0f030f00",
	"slot_16": "10031000",
	"slot_17": "11031100",
	"slot_18": "12031200",
	"slot_19": "13031300",
	"slot_20": "14031400",
	"slot_21": "15031500",
}

// registrationBurstSeedBodies 是 14:26 那次登记的**现场顺序**（哨兵在最前，
// 之后 9 条记录的先后顺序照抄 `packet_log.txt` 第 105105..105279 行）。
var registrationBurstSeedBodies = []string{
	registrationSentinelBody, // 14:26:13.673Z（旧二进制：只留 sentinel_* 取证键）
	"14031400",               // 14:26:15.602Z slot 20
	"0d030d00",               // 14:26:16.076Z slot 13
	"13031300",               // 14:26:16.413Z slot 19
	"15031500",               // 14:26:16.685Z slot 21
	"12031200",               // 14:26:17.108Z slot 18
	"0e030e00",               // 14:26:17.349Z slot 14
	"0f030f00",               // 14:26:17.559Z slot 15
	"10031000",               // 14:26:17.792Z slot 16
	"11031100",               // 14:26:18.005Z slot 17
}

// registrationBurstOperatorBodies 是**操作者 15:35 那次登记**推上来的 8 条记录，
// 顺序照抄 `packet_log.txt` 第 140899..140968 行。槽位集合 = {13,15,16,17,18,19,20,21}
// ⇒ 相对上面那 9 行**移除了 slot 14**，这正是操作者在面板上做的那个移除。
var registrationBurstOperatorBodies = []string{
	"11031100", // 15:35:02.929Z slot 17
	"10031000", // 15:35:03.176Z slot 16
	"12031200", // 15:35:03.350Z slot 18
	"0f030f00", // 15:35:03.784Z slot 15
	"14031400", // 15:35:04.446Z slot 20
	"13031300", // 15:35:04.722Z slot 19
	"15031500", // 15:35:04.924Z slot 21
	"0d030d00", // 15:35:05.201Z slot 13
}

// registrationBurstSelectedEntries 是那 8 条按 Slot 升序的落库字面形。
const registrationBurstSelectedEntries = "slot_13=0d030d00,slot_15=0f030f00," +
	"slot_16=10031000,slot_17=11031100,slot_18=12031200,slot_19=13031300," +
	"slot_20=14031400,slot_21=15031500"

// registrationBurstSelectedTriples 是同一批的解码三元组（全部 Mode 3）。
const registrationBurstSelectedTriples = "(13,3,13)(15,3,15)(16,3,16)(17,3,17)" +
	"(18,3,18)(19,3,19)(20,3,20)(21,3,21)"

// newRegistrationBurstFixture 造一个**现场形状**的仓储：角色 3 = `test2`（job 14），
// 并且先用 `union` 档把 14:26 那次登记落进去 —— 那正是**旧二进制**的行为
// （写 `slot_*` 与 `sentinel_*` 取证键、**不写** `window_armed` / `pending_*`），
// 于是重建出 15:35:02 那一刻的真实前态。
func newRegistrationBurstFixture(t *testing.T, arm string) (dnfrepo.Group, *Owner) {
	t.Helper()
	repos, _ := newRegistrationFixture(t)
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   "dnf:10001",
		Job:         "14",
		Level:       90,
		Name:        "test2",
	}); err != nil {
		t.Fatal(err)
	}
	catalog := registrationCatalog(t)
	seeder, err := NewOwner(repos, OwnerOptions{Catalog: catalog, Registration: RegistrationUnion})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range registrationBurstSeedBodies {
		applyBurstBody(t, seeder, "3", body)
	}
	// 前态必须**恰好**是现场那 9 行 + `sentinel_*` 取证键，且没有任何记账键。
	if got := storedSlotValuesFor(t, repos, "3"); len(got) != len(registrationBurstOperatorRows) {
		t.Fatalf("seeded rows = %d, want %d (values=%v)", len(got), len(registrationBurstOperatorRows), got)
	}
	for key, want := range registrationBurstOperatorRows {
		if got := storedSlotValuesFor(t, repos, "3")[key]; got != want {
			t.Fatalf("seeded %s = %q, want %q (the live SQL row)", key, got, want)
		}
	}
	scope := burstScopeValues(t, repos, "3")
	if !sentinelForensicsPresent(scope) {
		t.Fatal("the seeded scope must carry the pre-fix sentinel forensic key (sentinel_212cffff)")
	}
	if windowArmed(scope) || len(pendingBurstSlots(scope)) != 0 {
		t.Fatalf("the live pre-state has no bookkeeping keys, got window_armed=%q pending=%v",
			scope[entryWindowArmed], pendingBurstSlots(scope))
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: catalog, Registration: RegistrationWindow, RegistrationArm: arm})
	if err != nil {
		t.Fatal(err)
	}
	return repos, owner
}

func applyBurstBody(t *testing.T, owner *Owner, characterID string, body string) ApplyResult {
	t.Helper()
	record, err := ParseRecordIn(LayoutV4, mustDecode(t, body))
	if err != nil {
		t.Fatalf("parse %s: %v", body, err)
	}
	result, err := owner.Apply(context.Background(), characterID, "dnf:10001", record)
	if err != nil {
		t.Fatalf("apply %s: %v", body, err)
	}
	return result
}

func burstScopeValues(t *testing.T, repos dnfrepo.Group, characterID string) map[string]string {
	t.Helper()
	stored, found, err := repos.Settings.Load(context.Background(), Scope(characterID))
	if err != nil || !found {
		t.Fatalf("scope %s found=%t err=%v", Scope(characterID), found, err)
	}
	out := make(map[string]string, len(stored.Values))
	for key, value := range stored.Values {
		out[key] = value
	}
	return out
}

// TestRegistrationPreArmBurstCommitsAtBoundary 是本档的**主用例**（操作者的实际用法）：
// 一段 8 条记录 + **一个收尾哨兵**（现场 15:35 char 3 的形状）必须在重投影边界上提交，
// 落库集合**恰好**等于面板上选中的 8 个槽位（移除的 slot 14 真的没了）。
func TestRegistrationPreArmBurstCommitsAtBoundary(t *testing.T) {
	// 本用例钉的是**窗口替换（提交即删行）**那套语义（第二轮起）：把它按回退字面量显式
	// 打开（DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE 默认档 = 修复档 keep，一行都不删）。
	// 默认档下的新判据在 egistration_delete_test.go / skill_switch_inventory_delete_test.go。
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	repos, owner := newRegistrationBurstFixture(t, RegistrationArmRecord)

	// 1) 8 条记录：arm 还没打开（这个 scope 只差 window_armed 那一个键），
	//    于是它们进的是 `pending_*`，不是 `window_*`。
	for index, body := range registrationBurstOperatorBodies {
		result := applyBurstBody(t, owner, "3", body)
		if result.RegistrationCommitted {
			t.Fatalf("record #%d must not commit anything", index+1)
		}
		if result.RegistrationWindowCount != 0 {
			t.Fatalf("record #%d opened a window (%d) while the scope is not armed",
				index+1, result.RegistrationWindowCount)
		}
		if result.RegistrationPendingCount != index+1 {
			t.Fatalf("record #%d pending burst = %d, want %d", index+1, result.RegistrationPendingCount, index+1)
		}
	}
	beforeSentinel := burstScopeValues(t, repos, "3")
	if windowArmed(beforeSentinel) {
		t.Fatal("the scope must stay unarmed until the sentinel arrives")
	}
	for key := range beforeSentinel {
		if strings.HasPrefix(key, entryWindowPrefix) {
			t.Fatalf("no window key may exist before the sentinel (found %q)", key)
		}
	}
	if got := len(pendingBurstSlots(beforeSentinel)); got != len(registrationBurstOperatorBodies) {
		t.Fatalf("pending burst = %d slots, want %d", got, len(registrationBurstOperatorBodies))
	}
	// 加法安全：窗口还没提交时那 9 行一条都没少（slot 14 仍在，值没变）。
	if got := storedSlotValuesFor(t, repos, "3"); len(got) != len(registrationBurstOperatorRows) {
		t.Fatalf("while the burst is pending the store must stay additive: %d rows, want %d",
			len(got), len(registrationBurstOperatorRows))
	}

	// 2) 收尾哨兵：arm 打开，但**它关闭的窗口是空的**（这一段记录在它之前就到了）
	//    ⇒ 这一条哨兵不提交（"空窗口永不提交"原封不动）。
	sentinel := applyBurstBody(t, owner, "3", registrationSentinelBody)
	if sentinel.RegistrationCommitted {
		t.Fatal("the arming sentinel must not commit: the window after it is empty")
	}
	if len(sentinel.RegistrationDropped) != 0 {
		t.Fatalf("the arming sentinel dropped %v", sentinel.RegistrationDropped)
	}
	armed := burstScopeValues(t, repos, "3")
	if !windowArmed(armed) {
		t.Fatalf("the sentinel must arm the bookkeeping (%s=%q)", entryWindowArmed, armed[entryWindowArmed])
	}
	if got := len(pendingBurstSlots(armed)); got != len(registrationBurstOperatorBodies) {
		t.Fatalf("the pending burst must survive the arming sentinel: %d slots, want %d",
			got, len(registrationBurstOperatorBodies))
	}

	// 3) 重投影边界（= 操作者回到选择角色再进入）：窗口为空、pending 非空
	//    ⇒ 提升 pending 再提交。**这就是修复点**（上一版在这里什么都不做）。
	commit, committed := commitWindowFor(t, repos, "3", RegistrationWindow)
	if !committed {
		t.Fatal("the boundary must commit the pre-arm burst of a single-sentinel registration")
	}
	if commit.Trigger != RegistrationTriggerBoundaryPreArmBurst {
		t.Fatalf("commit trigger = %q, want %q", commit.Trigger, RegistrationTriggerBoundaryPreArmBurst)
	}
	if commit.Promoted != len(registrationBurstOperatorBodies) {
		t.Fatalf("promoted = %d, want %d", commit.Promoted, len(registrationBurstOperatorBodies))
	}
	if commit.WindowRecords != len(registrationBurstOperatorBodies) ||
		commit.SlotCount != len(registrationBurstOperatorBodies) {
		t.Fatalf("commit = %d window records / %d slot count, want %d",
			commit.WindowRecords, commit.SlotCount, len(registrationBurstOperatorBodies))
	}
	if strings.Join(commit.KeptEntries, ",") != registrationBurstSelectedEntries {
		t.Fatalf("kept entries = %v, want %s", commit.KeptEntries, registrationBurstSelectedEntries)
	}
	if strings.Join(commit.DroppedEntries, ",") != "slot_14=0e030e00" {
		t.Fatalf("dropped entries = %v, want exactly the removed slot_14", commit.DroppedEntries)
	}
	if got := skillSwitchTriplesText(commit.Dropped); got != "(14,3,14)" {
		t.Fatalf("dropped triples = %s, want (14,3,14)", got)
	}

	// 4) 落库集合**恰好**是选中的 8 行：移除真的生效了。
	after := storedSlotValuesFor(t, repos, "3")
	if len(after) != len(registrationBurstOperatorBodies) {
		t.Fatalf("committed rows = %d, want %d (values=%v)", len(after), len(registrationBurstOperatorBodies), after)
	}
	if value, present := after["slot_14"]; present {
		t.Fatalf("the removed slot_14 survived the commit (=%q); values=%v", value, after)
	}
	for key, want := range map[string]string{
		"slot_13": "0d030d00", "slot_15": "0f030f00", "slot_16": "10031000",
		"slot_17": "11031100", "slot_18": "12031200", "slot_19": "13031300",
		"slot_20": "14031400", "slot_21": "15031500",
	} {
		if after[key] != want {
			t.Fatalf("committed %s = %q, want %q", key, after[key], want)
		}
	}
	// 被删掉的那一行仍然留在 `record_*` 取证键里（可 grep、可追溯）。
	final := burstScopeValues(t, repos, "3")
	if _, present := final[entryRecordPrefix+"0e030e00"]; !present {
		t.Fatalf("the dropped row must stay traceable through %s0e030e00", entryRecordPrefix)
	}
	// 记账键收尾：窗口与 pending 都清了，`window_armed` 保留（这个客户端会发哨兵）。
	if !windowArmed(final) {
		t.Fatal("window_armed must survive the commit")
	}
	if got := len(pendingBurstSlots(final)); got != 0 {
		t.Fatalf("pending burst = %d slots after the commit, want 0", got)
	}
	if got := len(windowSlots(final)); got != 0 {
		t.Fatalf("window = %d slots after the commit, want 0", got)
	}
	// 窗口已关：再提交一次什么都不做（不重复写库）。
	if _, committed := commitWindowFor(t, repos, "3", RegistrationWindow); committed {
		t.Fatal("a closed window must not commit again")
	}
}

// TestRegistrationPreArmBurstRollbackLiteralKeepsTodaysRows 钉**一键回退字面量**
// `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM=sentinel`：arm 之前那一段什么都不记，
// 于是边界上还是什么都不提交 —— 与**线上当前这一版**逐字节相同：
// `character:3` 里仍是那 9 行并集（`slot_14 = 0e030e00` 留着），
// 一个 `pending_*` 键都不新增。
func TestRegistrationPreArmBurstRollbackLiteralKeepsTodaysRows(t *testing.T) {
	for _, arm := range []string{RegistrationArmSentinel, "armed", "TODAY", "live", "legacy", "rollback", "0", "off", "disable"} {
		repos, owner := newRegistrationBurstFixture(t, arm)
		for _, body := range registrationBurstOperatorBodies {
			result := applyBurstBody(t, owner, "3", body)
			if result.RegistrationArm != RegistrationArmSentinel {
				t.Fatalf("arm literal %q: result arm = %q, want %q", arm, result.RegistrationArm, RegistrationArmSentinel)
			}
			if result.RegistrationPendingCount != 0 {
				t.Fatalf("arm literal %q: the pre-arm burst must not be captured (%d slots)",
					arm, result.RegistrationPendingCount)
			}
		}
		applyBurstBody(t, owner, "3", registrationSentinelBody)
		scope := burstScopeValues(t, repos, "3")
		for key := range scope {
			if strings.HasPrefix(key, entryPendingPrefix) {
				t.Fatalf("arm literal %q: no pending key may be written (found %q)", arm, key)
			}
		}
		if _, committed := commitWindowFor(t, repos, "3", RegistrationWindow); committed {
			t.Fatalf("arm literal %q: the boundary must be a no-op (that is today's bytes)", arm)
		}
		after := storedSlotValuesFor(t, repos, "3")
		if len(after) != len(registrationBurstOperatorRows) {
			t.Fatalf("arm literal %q: %d rows, want today's %d (values=%v)",
				arm, len(after), len(registrationBurstOperatorRows), after)
		}
		for key, want := range registrationBurstOperatorRows {
			if after[key] != want {
				t.Fatalf("arm literal %q: %s = %q, want today's %q", arm, key, after[key], want)
			}
		}
	}
}

// TestRegistrationPreArmBurstWithoutSentinelForensicsStillCommits 钉**没有哨兵取证键的
// 老 scope 也一样被覆盖**（现场 `character:16` 就是这种：4 行 `slot_*`、一个
// `sentinel_*` 键都没有 —— 它的行是很早以前落的）。这类 scope **不能**靠取证键
// 来判断"客户端会不会发哨兵"，所以 pending 记账不以取证键为前提；
// 提交仍然以 `window_armed` 为前提，于是这个判据不会把从不发哨兵的客户端带上车。
func TestRegistrationPreArmBurstWithoutSentinelForensicsStillCommits(t *testing.T) {
	// 本用例钉的是**窗口替换（提交即删行）**那套语义（第二轮起）：把它按回退字面量显式
	// 打开（DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE 默认档 = 修复档 keep，一行都不删）。
	// 默认档下的新判据在 egistration_delete_test.go / skill_switch_inventory_delete_test.go。
	t.Setenv(RegistrationDeleteEnv, RegistrationDeleteReplace)
	repos, _ := newRegistrationFixture(t)
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "3", AccountID: "dnf:10001", Job: "14", Level: 90,
	}); err != nil {
		t.Fatal(err)
	}
	catalog := registrationCatalog(t)
	// 只落记录、**不落哨兵**（= 现场 character:16 的形状）。
	seeder, err := NewOwner(repos, OwnerOptions{Catalog: catalog, Registration: RegistrationUnion})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range registrationBurstSeedBodies[1:] {
		applyBurstBody(t, seeder, "3", body)
	}
	if sentinelForensicsPresent(burstScopeValues(t, repos, "3")) {
		t.Fatal("the fixture must not carry a sentinel forensic key")
	}
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog: catalog, Registration: RegistrationWindow, RegistrationArm: RegistrationArmRecord,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 一段 8 条记录 + 一个收尾哨兵：即使没有取证键，这段 burst 也要进 pending。
	for index, body := range registrationBurstOperatorBodies {
		result := applyBurstBody(t, owner, "3", body)
		if result.RegistrationWindowCount != 0 {
			t.Fatalf("record #%d opened a window while the scope is not armed", index+1)
		}
		if result.RegistrationPendingCount != index+1 {
			t.Fatalf("record #%d pending burst = %d, want %d", index+1, result.RegistrationPendingCount, index+1)
		}
	}
	scope := burstScopeValues(t, repos, "3")
	for key := range scope {
		if strings.HasPrefix(key, entryWindowPrefix) {
			t.Fatalf("no window key may exist before the sentinel (found %q)", key)
		}
	}
	applyBurstBody(t, owner, "3", registrationSentinelBody)
	commit, committed := commitWindowFor(t, repos, "3", RegistrationWindow)
	if !committed || commit.Trigger != RegistrationTriggerBoundaryPreArmBurst {
		t.Fatalf("committed=%t trigger=%q, want the pre-arm burst to commit", committed, commit.Trigger)
	}
	after := storedSlotValuesFor(t, repos, "3")
	if len(after) != len(registrationBurstOperatorBodies) {
		t.Fatalf("committed rows = %d, want %d (values=%v)", len(after), len(registrationBurstOperatorBodies), after)
	}
	if _, present := after["slot_14"]; present {
		t.Fatalf("the removed slot_14 survived the commit (values=%v)", after)
	}
}

// TestRegistrationPreArmBurstWithoutAnySentinelWritesNoWindowKeys 钉
// **一段从头到尾没有哨兵的 burst ⇒ 一个 `window_*` 键都不写、边界不提交**：
// `window_armed` 只有真哨兵才会写下，提交两步都以它为前提，所以那条路径上的
// `slot_*` 集合与下行字节与改动前逐字节相同（`pending_*` 只是记账键，不进任何判据）。
func TestRegistrationPreArmBurstWithoutAnySentinelWritesNoWindowKeys(t *testing.T) {
	repos, _ := newRegistrationFixture(t)
	owner, err := NewOwner(repos, OwnerOptions{
		Catalog: registrationCatalog(t), Registration: RegistrationWindow, RegistrationArm: RegistrationArmRecord,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range registrationBurstOperatorBodies {
		result := applyBurstBody(t, owner, "1", body)
		if result.RegistrationWindowCount != 0 || result.RegistrationCommitted {
			t.Fatalf("a sentinel-less burst must not touch the window: %+v", result)
		}
	}
	scope := burstScopeValues(t, repos, "1")
	for key := range scope {
		if strings.HasPrefix(key, entryWindowPrefix) {
			t.Fatalf("a sentinel-less burst must not write a window key (found %q)", key)
		}
	}
	if _, committed := commitWindowFor(t, repos, "1", RegistrationWindow); committed {
		t.Fatal("nothing was ever armed, so the boundary must not commit")
	}
	if got := len(storedSlotValuesFor(t, repos, "1")); got != len(registrationBurstOperatorBodies) {
		t.Fatalf("stored %d rows, want %d", got, len(registrationBurstOperatorBodies))
	}
}

// TestRegistrationPreArmBurstNeverWipesOnBareSentinels 钉**裸哨兵仍然不能清空换装表**：
// 只有哨兵、后面没有记录（现场 09:08:27.525 / 09:08:28.058 那两条）时，
// pending 与窗口都为空 ⇒ 一个 `slot_*` 都不动。
func TestRegistrationPreArmBurstNeverWipesOnBareSentinels(t *testing.T) {
	repos, owner := newRegistrationBurstFixture(t, RegistrationArmRecord)
	before := storedSlotValuesFor(t, repos, "3")
	for index := 0; index < 3; index++ {
		result := applyBurstBody(t, owner, "3", registrationSentinelBody)
		if result.RegistrationCommitted || len(result.RegistrationDropped) != 0 {
			t.Fatalf("bare sentinel #%d reported a commit/drop: %+v", index+1, result)
		}
	}
	if _, committed := commitWindowFor(t, repos, "3", RegistrationWindow); committed {
		t.Fatal("a boundary with an empty window and an empty pre-arm burst must not commit")
	}
	after := storedSlotValuesFor(t, repos, "3")
	if len(after) != len(before) {
		t.Fatalf("bare sentinels changed the slot set: %d rows, want %d", len(after), len(before))
	}
	for key, value := range before {
		if after[key] != value {
			t.Fatalf("%s changed from %q to %q across bare sentinels", key, value, after[key])
		}
	}
}

// TestRegistrationPreArmBurstTwoConsecutiveSentinelsDiscardThePreArmBurst 钉那条
// "连续两个分隔符之间没有记录 ⇒ 丢掉 arm 之前那一段"的规则（它是两个
// `EmptyWindowNeverCommits` 安全用例保持绿色的根据，见现场 09:08:27.525 /
// 09:08:28.058 那两条连着来的哨兵）。这条规则只会**少提交**，不会多删一行：
// 丢掉之后边界上窗口与 pending 都是空的 ⇒ 一个 `slot_*` 都不动。
func TestRegistrationPreArmBurstTwoConsecutiveSentinelsDiscardThePreArmBurst(t *testing.T) {
	repos, owner := newRegistrationBurstFixture(t, RegistrationArmRecord)
	// 一段 arm 之前的 burst（8 条），紧跟**两个**哨兵，中间没有记录。
	for _, body := range registrationBurstOperatorBodies {
		applyBurstBody(t, owner, "3", body)
	}
	applyBurstBody(t, owner, "3", registrationSentinelBody)
	applyBurstBody(t, owner, "3", registrationSentinelBody)
	if got := len(pendingBurstSlots(burstScopeValues(t, repos, "3"))); got != 0 {
		t.Fatalf("the second consecutive delimiter must drop the stale pre-arm burst (%d slots left)", got)
	}
	if _, committed := commitWindowFor(t, repos, "3", RegistrationWindow); committed {
		t.Fatal("after the discard the boundary must be a no-op (no wipe, no prune)")
	}
	after := storedSlotValuesFor(t, repos, "3")
	if len(after) != len(registrationBurstOperatorRows) {
		t.Fatalf("stored %d rows, want the untouched %d (values=%v)",
			len(after), len(registrationBurstOperatorRows), after)
	}
	if after["slot_14"] != "0e030e00" {
		t.Fatalf("slot_14 = %q, want the untouched 0e030e00", after["slot_14"])
	}
}

// TestNormalizeRegistrationArmTable 钉开关口径：只有回退字面量才离开修复档。
func TestNormalizeRegistrationArmTable(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: RegistrationArmRecord},
		{value: RegistrationArmRecord, want: RegistrationArmRecord},
		{value: "  RECORD  ", want: RegistrationArmRecord},
		{value: "burst", want: RegistrationArmRecord},
		{value: "on", want: RegistrationArmRecord},
		{value: "fix", want: RegistrationArmRecord},
		{value: "typo", want: RegistrationArmRecord},
		{value: RegistrationArmSentinel, want: RegistrationArmSentinel},
		{value: "SENTINEL", want: RegistrationArmSentinel},
		{value: "armed", want: RegistrationArmSentinel},
		{value: "window_only", want: RegistrationArmSentinel},
		{value: "today", want: RegistrationArmSentinel},
		{value: "live", want: RegistrationArmSentinel},
		{value: "legacy", want: RegistrationArmSentinel},
		{value: "rollback", want: RegistrationArmSentinel},
		{value: "0", want: RegistrationArmSentinel},
		{value: "off", want: RegistrationArmSentinel},
		{value: "disable", want: RegistrationArmSentinel},
	} {
		if got := NormalizeRegistrationArm(test.value); got != test.want {
			t.Fatalf("NormalizeRegistrationArm(%q) = %q, want %q", test.value, got, test.want)
		}
	}
	if got := ((*Owner)(nil)).registrationArm(); got != RegistrationArmRecord {
		t.Fatalf("nil owner arm = %q, want %q", got, RegistrationArmRecord)
	}
	// 空 options ⇒ 自读 env；env 未设 ⇒ 修复档。
	t.Setenv(RegistrationArmEnv, "")
	if got := (&Owner{}).registrationArm(); got != RegistrationArmRecord {
		t.Fatalf("unset env arm = %q, want the fixed tier %q", got, RegistrationArmRecord)
	}
	t.Setenv(RegistrationArmEnv, RegistrationArmSentinel)
	if got := (&Owner{}).registrationArm(); got != RegistrationArmSentinel {
		t.Fatalf("rollback literal arm = %q, want %q", got, RegistrationArmSentinel)
	}
}

// commitWindowFor 是 `CommitRegistration` 的按角色版本。
func commitWindowFor(t *testing.T, repos dnfrepo.Group, characterID string, mode string) (RegistrationCommit, bool) {
	t.Helper()
	commit, committed, err := CommitRegistration(context.Background(), repos, characterID, LayoutV4, mode)
	if err != nil {
		t.Fatalf("CommitRegistration(%s): %v", characterID, err)
	}
	return commit, committed
}

// skillSwitchTriplesText 把记录渲染成 `(slot,mode,value)`（与 bridge 日志同形）。
func skillSwitchTriplesText(records []Record) string {
	if len(records) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, "("+strconv.Itoa(int(record.Slot))+","+
			strconv.Itoa(int(record.Mode))+","+strconv.Itoa(int(record.Value))+")")
	}
	return strings.Join(parts, "")
}
