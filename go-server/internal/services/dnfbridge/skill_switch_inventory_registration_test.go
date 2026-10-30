package dnfbridge

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「Buff 强化 / 技能换装」**登记窗口**（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION`）的钉子。
//
// 这一档修的是操作者第四次报障：「选择角色后**刚选择的装备没有登记**」。
// 现场事实（HK `192.0.2.11`，`packet_log.txt`，2026-09-28，报告
// `_dist/buff-swap-registration.md` §1/§2 有逐行原始输出）：
//
//	char 6 = `test78`（job 0）最后一次完整登记在 13:52:42.240..44.977Z，
//	客户端推上来 **11 个槽位** {12,14,15,16,17,18,19,20,21,22,23}；
//	而 `character:6:skill_switch_inventory` 里躺着 **14 条** `slot_*` 行 ——
//	`slot_3` / `slot_13` / `slot_25` 是更早两次登记留下的、这次**没有选**的槽位，
//	它们被 13:52:48.9 / 13:52:49.0 两条边界上的重下发帧又登记了回去。
//
// 客户端每次登记都推**全集**并且带 `21 2c ff ff` 哨兵（现场 5/5 段都有），
// 所以哨兵可以当**分隔符**：窗口里的记录 = 这一段登记，提交 = 把状态键集合换成窗口。

// skillSwitchSentinelWireBody 是现场哨兵的 6 字节形（`26 00` = 分帧残留的序号高位
// + 真包体 `21 2c ff ff`），与库里 `record_*` 取证键、`packet_log.txt` 的 `body_hex` 同形。
const skillSwitchSentinelWireBody = "2600212cffff"

// skillSwitchRegistrationSecondBurstBodies 是**第二次登记**：在操作者那 11 条
// Mode 0 行里**去掉 12、19、23**（= 操作者改选），保留 7 条并**改一条绑定**
// （14 从 62 改到 60）。手写现场形，不引用实现常量。
var skillSwitchRegistrationSecondBurstBodies = []string{
	"0e003c00", // slot 14 / mode 0 / value 60  ← 绑定改了
	"0f003d00", // slot 15 / mode 0 / value 61
	"10003900", // slot 16 / mode 0 / value 57
	"11003b00", // slot 17 / mode 0 / value 59
	"12003f00", // slot 18 / mode 0 / value 63
	"14004000", // slot 20 / mode 0 / value 64
	"15031500", // slot 21 / mode 3 / value 21
	"17003700", // slot 23 / mode 0 / value 55  ← 保留（对照组）
}

// secondBurstKeptSlots 是上面那一批应当**留下**的槽位（升序）。
var secondBurstKeptSlots = []int{14, 15, 16, 17, 18, 20, 21, 23}

// secondBurstDroppedSlots 是那 14 条里**没有被这次登记选中**、必须被删掉的槽位。
var secondBurstDroppedSlots = []int{3, 12, 13, 19, 22, 25}

// skillSwitchRegistrationBody 把 4 字节包体包成 handler 实际收到的 6 字节切片
// （2 字节导线前缀 + 4 字节真包体，dual-read 会去掉前 2 个）。前缀取现场值 `0e00`。
func skillSwitchRegistrationBody(t *testing.T, body string) []byte {
	t.Helper()
	if len(body) == 12 {
		// 已经是 6 字节形。
		return skillSwitchInventoryBody(t, body)
	}
	return skillSwitchInventoryBody(t, "0e00"+body)
}

// applySkillSwitchRegistration writes one op1428 record through the **real** entry point.
func applySkillSwitchRegistration(t *testing.T, fixture *skillSwitchLoadoutFixture, body string) {
	t.Helper()
	if _, err := fixture.service.handleSkillSwitchInventoryCommand(
		fixture.session,
		currentSkillSwitchInventoryOpcode,
		skillSwitchRegistrationBody(t, body),
	); err != nil {
		t.Fatalf("handleSkillSwitchInventoryCommand(%s): %v", body, err)
	}
}

// registerSkillSwitchSecondBurst 推"第二次登记"：哨兵 + 8 条记录（现场形）。
func registerSkillSwitchSecondBurst(t *testing.T, fixture *skillSwitchLoadoutFixture) {
	t.Helper()
	applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)
	for _, body := range skillSwitchRegistrationSecondBurstBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
}

// readStoredSlotValues 读回落库的 `slot_<n>=<hex>`（升序），**直接来自 settings 仓储**
// ——"登记到底写进去了什么"的判据是它，不是任何内存状态。
// `slot_count` 这类汇总键刻意排除（它读起来也带 `slot_` 前缀）。
func readStoredSlotValues(t *testing.T, repos dnfrepo.Group, characterID string) map[string]string {
	t.Helper()
	values := readStoredScopeValues(t, repos, characterID)
	out := make(map[string]string, len(values))
	for key, value := range values {
		if strings.HasPrefix(key, "slot_") && key != "slot_count" {
			out[key] = value
		}
	}
	return out
}

// readStoredScopeValues 读回整个 scope 的键值（判定 `window_*` 记账键用）。
func readStoredScopeValues(t *testing.T, repos dnfrepo.Group, characterID string) map[string]string {
	t.Helper()
	stored, found, err := repos.Settings.Load(context.Background(), skillswitch.Scope(characterID))
	if err != nil {
		t.Fatalf("settings load: %v", err)
	}
	if !found {
		t.Fatalf("settings scope %s not found", skillswitch.Scope(characterID))
	}
	values := make(map[string]string, len(stored.Values))
	for key, value := range stored.Values {
		values[key] = value
	}
	return values
}

// encodeRecordValuePairs 是测试侧的**独立**编码器：把显式给出的 `(slot,value)` 列表
// 编成客户端入站形状 `u8 count + count×(u32 LE,u32 LE)`。刻意不复用实现里的
// `buildSkillSwitchLoadoutBody`，这样"帧字节对不对"不是拿实现证明实现。
func encodeRecordValuePairs(t *testing.T, pairs []struct{ slot, value uint32 }) string {
	t.Helper()
	body := make([]byte, 1+8*len(pairs))
	body[0] = byte(len(pairs))
	for index, pair := range pairs {
		offset := 1 + 8*index
		putU32LE(body[offset:offset+4], pair.slot)
		putU32LE(body[offset+4:offset+8], pair.value)
	}
	return hex.EncodeToString(body)
}

func putU32LE(dst []byte, value uint32) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value >> 16)
	dst[3] = byte(value >> 24)
}

// TestSkillSwitchRegistrationReplacesRemovedSlots 是这一档的**主用例**：
// 走真实入口登记操作者那 14 条 → 再推一段"改选过"的登记（哨兵 + 8 条）→
// 边界投影时提交窗口 ⇒
//
//  1. settings 里的 `slot_*` 集合**恰好**等于第二次登记选中的 8 个槽位
//     （被移除的 6 个真的没了，不是"还在但没发"）；
//  2. 改过的绑定按新值落库（`slot_14` 从 `0e003e00`(62) 变成 `0e003c00`(60)）；
//  3. 边界帧只带这 8 对，且每对都是**落库行自己的 `(Slot, Value)`**。
//
// 注：那 8 条里 `15031500` 是 `Value == Slot`（不含绑定信息）的记录，按第三轮的
// `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`（默认 `keep`）它**不许改掉** `slot_21`
// 已登记的绑定 60 ⇒ 这条用例在 `slot_21` 上的期望是 `15003c00` / `(21,60)`；
// 回退字面量 `replace` 下才是改动前的 `15031500` / `(21,21)`
// （钉在 `skill_switch_inventory_bind_test.go`）。
func TestSkillSwitchRegistrationReplacesRemovedSlots(t *testing.T) {
	// 本用例钉的是"窗口提交 = 替换（删掉窗口外的行）"那套旧语义与它的线上字节。
	// 第五轮把默认档改成 `keep`（窗口提交一行都不删），所以这里显式把删除语义按
	// **回退字面量**打开（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）；默认档下的
	// 新判据在 `skill_switch_inventory_delete_test.go`（仓内对应 `registration_delete_test.go`）。
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	fixture := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, fixture)

	first := readStoredSlotValues(t, fixture.repos, "1")
	if len(first) != len(skillSwitchLoadoutLiveSlots) {
		t.Fatalf("first registration stored %d slot rows, want %d", len(first), len(skillSwitchLoadoutLiveSlots))
	}
	if first["slot_14"] != "0e003e00" {
		t.Fatalf("slot_14 = %q before the second registration, want 0e003e00", first["slot_14"])
	}

	registerSkillSwitchSecondBurst(t, fixture)

	// 窗口打开期间状态只能被**改写**、一条都不能少（加法安全）：
	// 第二次登记覆盖了 14/21 两行的值，但没有新增槽位。
	storedBeforeCommit := readStoredSlotValues(t, fixture.repos, "1")
	if len(storedBeforeCommit) != len(skillSwitchLoadoutLiveSlots) {
		t.Fatalf("while the window is open the store must stay additive: %d rows, want %d",
			len(storedBeforeCommit), len(skillSwitchLoadoutLiveSlots))
	}
	if storedBeforeCommit["slot_14"] != "0e003c00" {
		t.Fatalf("slot_14 = %q after the new binding, want 0e003c00", storedBeforeCommit["slot_14"])
	}
	if storedBeforeCommit["slot_21"] != "15003c00" {
		// `15031500` 是 `Value == Slot`（不含绑定信息）的记录；默认档下它不许改掉
		// `slot_21` 已经登记好的绑定（60）—— 见 `_dist/buff-swap-round3.md`。
		// 回退字面量 `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND=replace` 下这里才是 `15031500`。
		t.Fatalf("slot_21 = %q after the new record, want the preserved 15003c00", storedBeforeCommit["slot_21"])
	}

	// 回到选择角色 → 再进入：新的 service/session + 同一份仓储，走边界投影。
	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
	reentry.service.repositoryProvider = func() (dnfrepo.Group, bool) { return fixture.repos, true }
	const boundary = "initial_town_before_final_transition_after_mode1_without_mode3_before_typed_op24_skill_switch_inventory"
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, boundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}

	// 1) 落库集合 = 恰好第二次登记选中的那 8 个槽位。
	after := readStoredSlotValues(t, fixture.repos, "1")
	if len(after) != len(secondBurstKeptSlots) {
		t.Fatalf("committed slot rows = %d, want %d (values=%v)", len(after), len(secondBurstKeptSlots), after)
	}
	for _, slot := range secondBurstKeptSlots {
		key := "slot_" + itoa(slot)
		if _, present := after[key]; !present {
			t.Fatalf("committed rows are missing the selected %s; values=%v", key, after)
		}
	}
	for _, slot := range secondBurstDroppedSlots {
		key := "slot_" + itoa(slot)
		if value, present := after[key]; present {
			t.Fatalf("removed %s survived the registration window commit (=%q); values=%v", key, value, after)
		}
	}
	// 改过的绑定确实按新值落库（decoded = slot 14 / mode 0 / value 60）。
	record, err := skillswitch.ParseRecordIn(skillswitch.LayoutV4, mustDecodeHex(t, after["slot_14"]))
	if err != nil {
		t.Fatalf("parse committed slot_14: %v", err)
	}
	if record.Slot != 14 || record.Mode != skillswitch.ModeRegister || record.Value != 60 {
		t.Fatalf("committed slot_14 decoded = %+v, want slot=14 mode=0 value=60", record)
	}

	// 2) 边界帧 = 8 对，逐对是落库行自己的 (Slot, Value)。
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want exactly the one loadout frame", len(packets))
	}
	body := packets[0].body
	if body[0] != byte(len(secondBurstKeptSlots)) {
		t.Fatalf("count byte = %d, want %d", body[0], len(secondBurstKeptSlots))
	}
	if !skillSwitchDownlinkFrameValid(body) {
		t.Fatal("the committed frame must pass the 1+8*count gate")
	}
	wantPairs := []struct{ slot, value uint32 }{
		{14, 60}, {15, 61}, {16, 57}, {17, 59}, {18, 63}, {20, 64}, {21, 60}, {23, 55},
	}
	for index, want := range wantPairs {
		offset := 1 + 8*index
		slot := uint32(body[offset]) | uint32(body[offset+1])<<8 | uint32(body[offset+2])<<16 | uint32(body[offset+3])<<24
		value := uint32(body[offset+4]) | uint32(body[offset+5])<<8 | uint32(body[offset+6])<<16 | uint32(body[offset+7])<<24
		if slot != want.slot || value != want.value {
			t.Fatalf("frame pair[%d] = (%d,%d), want (%d,%d)", index, slot, value, want.slot, want.value)
		}
	}
	if len(body) != 1+8*len(secondBurstKeptSlots) {
		t.Fatalf("frame len = %d, want %d", len(body), 1+8*len(secondBurstKeptSlots))
	}

	// 3) 日志：提交事件把"收到多少条 / 解码后的三元组 / 写了什么 / 删了什么"都写出来。
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchRegistrationCommitEvent,
		"char_id=1",
		"scope=character:1:skill_switch_inventory",
		"source=" + boundary,
		"registration_switch=" + skillswitch.RegistrationWindow,
		"registration_switch_default=" + skillswitch.RegistrationWindow,
		"registration_rollback_literal=" + skillswitch.RegistrationUnion,
		"registration_tier=sentinel_and_boundary_commit",
		"window_records=8",
		"window_slots=slot_14,slot_15,slot_16,slot_17,slot_18,slot_20,slot_21,slot_23",
		"slot_count=8",
		"committed=true",
		"kept_triples=(14,0,60)(15,0,61)(16,0,57)(17,0,59)(18,0,63)(20,0,64)(21,0,60)(23,0,55)",
		"dropped_count=6",
		"dropped_triples=(3,1,47)(12,0,54)(13,3,13)(19,0,58)(22,0,56)(25,3,25)",
		"written_entries=slot_14=0e003c00,slot_15=0f003d00,slot_16=10003900,slot_17=11003b00," +
			"slot_18=12003f00,slot_20=14004000,slot_21=15003c00,slot_23=17003700",
		"dropped_entries=slot_3=03012f00,slot_12=0c003600,slot_13=0d030d00," +
			"slot_19=13003a00,slot_22=16003800,slot_25=19031900",
		"record_body_layout=v4",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("registration commit log missing %q; log=\n%s", want, log)
		}
	}
	// 同一次边界上重下发帧只带被选中的那 8 对。
	for _, want := range []string{
		"EVENT kind=" + skillSwitchLoadoutResendEvent,
		"loadout_rows=8",
		"pairs=(14,60)(15,61)(16,57)(17,59)(18,63)(20,64)(21,60)(23,55)",
		"pair_mapping=slot_records_value",
		"pair_value_source=record_value_field",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("resend log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchRegistrationEmptyWindowNeverCommits 钉**裸哨兵不能清空换装表**：
// 现场 09:08:27.525 / 09:08:28.058 那两条"只有哨兵、后面没有记录"的包，
// 若按"清空"处理会把操作者刚登记的整套换装抹掉（上一轮拒绝当清空处理的理由）。
func TestSkillSwitchRegistrationEmptyWindowNeverCommits(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, fixture)
	before := readStoredSlotValues(t, fixture.repos, "1")

	// 连推 3 条裸哨兵（现场形），一条记录都不跟。
	for index := 0; index < 3; index++ {
		applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)
	}
	if got := readStoredSlotValues(t, fixture.repos, "1"); len(got) != len(before) {
		t.Fatalf("a bare sentinel changed the slot set: %d rows, want %d", len(got), len(before))
	}

	// 边界也不许把它们删掉。
	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, "empty_window_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	after := readStoredSlotValues(t, fixture.repos, "1")
	if len(after) != len(before) {
		t.Fatalf("bare sentinels + boundary changed the slot set: %d rows, want %d", len(after), len(before))
	}
	for key, value := range before {
		if after[key] != value {
			t.Fatalf("%s changed from %q to %q across bare sentinels", key, value, after[key])
		}
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	if strings.Contains(log, skillSwitchRegistrationCommitEvent) {
		t.Fatalf("a bare sentinel must not log a commit; log=\n%s", log)
	}
}

// TestSkillSwitchRegistrationNoSentinelIsByteForByteToday 钉**没有哨兵 ⇒ 逐字节等于
// 改动前**：某些客户端版本可能不发哨兵，那条路径必须是"加法安全"的并集，
// 而不是"从来不提交所以永远删不掉"的死代码。
func TestSkillSwitchRegistrationNoSentinelIsByteForByteToday(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)
	// 只推"第二次登记"那 8 条记录，**不推哨兵**。
	for _, body := range skillSwitchRegistrationSecondBurstBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	stored := readStoredSlotValues(t, fixture.repos, "1")
	if len(stored) != len(skillSwitchRegistrationSecondBurstBodies) {
		t.Fatalf("sentinel-less burst stored %d rows, want %d", len(stored), len(skillSwitchRegistrationSecondBurstBodies))
	}
	settings, _, err := fixture.repos.Settings.Load(context.Background(), skillswitch.Scope("1"))
	if err != nil {
		t.Fatal(err)
	}
	for key := range settings.Values {
		if strings.HasPrefix(key, "window_") {
			t.Fatalf("a sentinel-less burst must not open a window (key %q written)", key)
		}
	}

	// 边界：没有窗口 ⇒ 不提交、不删、不发生成事件。
	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, "no_sentinel_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	after := readStoredSlotValues(t, fixture.repos, "1")
	if len(after) != len(stored) {
		t.Fatalf("boundary changed a sentinel-less burst: %d rows, want %d", len(after), len(stored))
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	if strings.Contains(log, skillSwitchRegistrationCommitEvent) {
		t.Fatalf("no window was ever opened, so no commit may be logged; log=\n%s", log)
	}
}

// TestSkillSwitchRegistrationUnionRollbackLiteralIsByteForByteToday 钉**一键回退**：
// `DNFBRIDGE_SKILL_SWITCH_REGISTRATION=union` 时哨兵被忽略、集合只增不减、
// 一个 `window_*` 键都不写 —— 逐字节等于改动前（也就是上一轮线上那 15 帧的来源）。
func TestSkillSwitchRegistrationUnionRollbackLiteralIsByteForByteToday(t *testing.T) {
	for _, literal := range []string{
		skillswitch.RegistrationUnion, "off", "TODAY", "legacy", "rollback", "additive", "0", "disable",
	} {
		t.Setenv(skillswitch.RegistrationModeEnv, literal)
		fixture := newSkillSwitchLoadoutFixture(t)
		registerSkillSwitchLoadout(t, fixture)
		registerSkillSwitchSecondBurst(t, fixture)

		stored := readStoredSlotValues(t, fixture.repos, "1")
		// union = 并集：14 条一条都没被删（第二次登记只覆盖/新增）。
		if len(stored) != len(skillSwitchLoadoutLiveSlots) {
			t.Fatalf("literal %q: union tier stored %d rows, want the untouched %d (values=%v)",
				literal, len(stored), len(skillSwitchLoadoutLiveSlots), stored)
		}
		for _, slot := range secondBurstDroppedSlots {
			key := "slot_" + itoa(slot)
			if _, present := stored[key]; !present {
				t.Fatalf("literal %q: union tier dropped %s (it must never delete)", literal, key)
			}
		}
		settings, _, err := fixture.repos.Settings.Load(context.Background(), skillswitch.Scope("1"))
		if err != nil {
			t.Fatal(err)
		}
		for key := range settings.Values {
			if strings.HasPrefix(key, "window_") {
				t.Fatalf("literal %q: union tier must not write window keys (found %q)", literal, key)
			}
		}

		// recorded 事件在**写入方**那个 session 的日志里（回退档每次落库都自带档位）。
		writerLog := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
		for _, want := range []string{
			"registration_switch=" + skillswitch.RegistrationUnion,
			"registration_committed=false",
			"registration_window_count=0",
			"registration_kept_count=0",
			"registration_dropped_count=0",
		} {
			if !strings.Contains(writerLog, want) {
				t.Fatalf("literal %q writer log missing %q; log=\n%s", literal, want, writerLog)
			}
		}

		// 边界仍然按**全部 14 行**重下发（union = 并集，第二次登记只是改写了
		// 14/21 两行的值）。期望字节用**测试侧独立编码器**从显式 pair 列表算出来，
		// 不是拿实现的常量对拍。
		reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
		if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, "union_rollback_boundary"); err != nil {
			t.Fatalf("literal %q projection error = %v", literal, err)
		}
		packets := drainUpperPackets(t, reentry.connection)
		if len(packets) != 1 {
			t.Fatalf("literal %q wrote %d packets, want 1", literal, len(packets))
		}
		wantUnionBody := encodeRecordValuePairs(t, []struct{ slot, value uint32 }{
			{3, 47}, {12, 54}, {13, 13}, {14, 60}, {15, 61}, {16, 57}, {17, 59},
			{18, 63}, {19, 58}, {20, 64}, {21, 21}, {22, 56}, {23, 55}, {25, 25},
		})
		if got := hex.EncodeToString(packets[0].body); got != wantUnionBody {
			t.Fatalf("literal %q body = %s\nwant union-tier %s", literal, got, wantUnionBody)
		}
		log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
		if strings.Contains(log, skillSwitchRegistrationCommitEvent) {
			t.Fatalf("literal %q must not log a registration commit; log=\n%s", literal, log)
		}
	}
	t.Setenv(skillswitch.RegistrationModeEnv, "")
	if got := skillswitch.CurrentRegistrationMode(); got != skillswitch.RegistrationWindow {
		t.Fatalf("unset registration env = %q, want %q", got, skillswitch.RegistrationWindow)
	}
}

// TestSkillSwitchRegistrationSentinelOnlyTierKeepsBoundaryWindowOpen 钉**保守档**：
// 只认哨兵分隔符，边界不提交 —— 用在"担心 burst 被截断、宁可让移除晚一轮生效"的场合。
// 两个档位在同一条现场形状上对比（开局哨兵 + 一段**没有收尾哨兵**的登记）：
// `window` 档在边界就剪枝，`sentinel_only` 档要等下一个哨兵。
func TestSkillSwitchRegistrationSentinelOnlyTierKeepsBoundaryWindowOpen(t *testing.T) {
	// 本用例钉的是"窗口提交 = 替换（删掉窗口外的行）"那套旧语义与它的线上字节。
	// 第五轮把默认档改成 `keep`（窗口提交一行都不删），所以这里显式把删除语义按
	// **回退字面量**打开（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）；默认档下的
	// 新判据在 `skill_switch_inventory_delete_test.go`（仓内对应 `registration_delete_test.go`）。
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	for _, test := range []struct {
		mode               string
		boundaryCommitted  bool
		rowsAfterBoundary  int
		rowsAfterNextFlush int
	}{
		{mode: skillswitch.RegistrationWindow, boundaryCommitted: true, rowsAfterBoundary: len(secondBurstKeptSlots), rowsAfterNextFlush: len(secondBurstKeptSlots)},
		{mode: skillswitch.RegistrationSentinelOnly, boundaryCommitted: false, rowsAfterBoundary: len(skillSwitchLoadoutLiveSlots), rowsAfterNextFlush: len(secondBurstKeptSlots)},
	} {
		t.Setenv(skillswitch.RegistrationModeEnv, test.mode)
		fixture := newSkillSwitchLoadoutFixture(t)
		registerSkillSwitchLoadout(t, fixture)
		// 开局哨兵（现场形：哨兵在最前面）+ 一段没有收尾哨兵的登记。
		applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)
		for _, body := range skillSwitchRegistrationSecondBurstBodies {
			applySkillSwitchRegistration(t, fixture, body)
		}

		reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
		if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, "sentinel_only_boundary"); err != nil {
			t.Fatalf("mode %q projection error = %v", test.mode, err)
		}
		stored := readStoredSlotValues(t, fixture.repos, "1")
		if len(stored) != test.rowsAfterBoundary {
			t.Fatalf("mode %q: %d rows after the boundary, want %d",
				test.mode, len(stored), test.rowsAfterBoundary)
		}
		log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
		committed := strings.Contains(log, skillSwitchRegistrationCommitEvent)
		if committed != test.boundaryCommitted {
			t.Fatalf("mode %q: boundary committed=%t, want %t; log=\n%s",
				test.mode, committed, test.boundaryCommitted, log)
		}

		// 下一个哨兵（在**新的** session 上推，证明窗口是落库的、不是内存的）才提交。
		next := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
		applySkillSwitchRegistration(t, next, skillSwitchSentinelWireBody)
		after := readStoredSlotValues(t, fixture.repos, "1")
		if len(after) != test.rowsAfterNextFlush {
			t.Fatalf("mode %q: %d rows after the next sentinel, want %d (values=%v)",
				test.mode, len(after), test.rowsAfterNextFlush, after)
		}
	}
}

// TestSkillSwitchRegistrationTruncatedBurstCommitsTheObservedPrefix 把**截断**行为
// 显式钉成一条已知性质（不是意外）：
//
// 客户端没有"这段集合推完了"的信号（收尾哨兵现场 5 段里只有 1 段有），所以
// "操作者移除了 X"与"这段推送被截断、X 还没上来"在报文层面**不可区分**。
// 默认档的选择是"边界提交已收到的前缀"，代价是：被截掉的那些槽位这一轮会从集合里
// 消失（客户端下一次完整登记会补回来）。这条用例把代价写死，并同时钉住
// "窗口打开期间状态只增不减"这条加法安全性（截断发生时并集仍然是完整的）。
func TestSkillSwitchRegistrationTruncatedBurstCommitsTheObservedPrefix(t *testing.T) {
	// 本用例钉的是"窗口提交 = 替换（删掉窗口外的行）"那套旧语义与它的线上字节。
	// 第五轮把默认档改成 `keep`（窗口提交一行都不删），所以这里显式把删除语义按
	// **回退字面量**打开（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）；默认档下的
	// 新判据在 `skill_switch_inventory_delete_test.go`（仓内对应 `registration_delete_test.go`）。
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	fixture := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, fixture)

	// 一段被截断的登记：哨兵 + 只收到前 3 条。
	applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)
	for _, body := range skillSwitchRegistrationSecondBurstBodies[:3] {
		applySkillSwitchRegistration(t, fixture, body)
	}
	// 截断期间（窗口还没提交）：状态仍然是**并集**，一条都没丢。
	duringBurst := readStoredSlotValues(t, fixture.repos, "1")
	if len(duringBurst) != len(skillSwitchLoadoutLiveSlots) {
		t.Fatalf("while the window is open the store must stay additive: %d rows, want %d",
			len(duringBurst), len(skillSwitchLoadoutLiveSlots))
	}

	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, "truncated_burst_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	after := readStoredSlotValues(t, fixture.repos, "1")
	if len(after) != 3 {
		t.Fatalf("the committed prefix = %d rows, want the 3 observed records (values=%v)", len(after), after)
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchRegistrationCommitEvent,
		"window_records=3",
		"dropped_count=11",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("truncated-burst log missing %q; log=\n%s", want, log)
		}
	}
	// 被截掉的行**没有从取证里消失**：它们仍然在 record_* 取证键里可 grep。
	settings, _, err := fixture.repos.Settings.Load(context.Background(), skillswitch.Scope("1"))
	if err != nil {
		t.Fatal(err)
	}
	forensic := 0
	for key := range settings.Values {
		if strings.HasPrefix(key, "record_") {
			forensic++
		}
	}
	if forensic == 0 {
		t.Fatal("dropped rows must remain traceable through the record_* forensic keys")
	}
}

// TestSkillSwitchRegistrationRoundTripThroughRealEntryPoint 是交付物要的往返钉子：
// 走**真实入口** `handleSkillSwitchInventoryCommand` 落操作者那 14 条记录，
// 断言"新登记按解码后的值落库"，然后换 service/session（= 重新选角）在边界上
// 断言重下发帧带着这些值。
func TestSkillSwitchRegistrationRoundTripThroughRealEntryPoint(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, fixture)

	stored := readStoredSlotValues(t, fixture.repos, "1")
	wantValues := map[string]string{
		"slot_3": "03012f00", "slot_12": "0c003600", "slot_13": "0d030d00",
		"slot_14": "0e003e00", "slot_15": "0f003d00", "slot_16": "10003900",
		"slot_17": "11003b00", "slot_18": "12003f00", "slot_19": "13003a00",
		"slot_20": "14004000", "slot_21": "15003c00", "slot_22": "16003800",
		"slot_23": "17003700", "slot_25": "19031900",
	}
	if len(stored) != len(wantValues) {
		t.Fatalf("stored %d rows, want %d", len(stored), len(wantValues))
	}
	for key, want := range wantValues {
		if stored[key] != want {
			t.Fatalf("%s = %q, want %q (decoded values must be persisted verbatim)", key, stored[key], want)
		}
	}

	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, "round_trip_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("round trip wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchLoadoutExpectedBodyHex {
		t.Fatalf("round-trip body = %s\nwant              %s", got, skillSwitchLoadoutExpectedBodyHex)
	}
}

// TestSkillSwitchUplinkGateAcceptsLegalRecordAndRejectsMalformed 钉上行闸门本身：
// 4 字节规范包体与 6 字节现行分片**收**，其余一律具名拒绝（`ErrRecordShape`），
// 并且下行闸门在同一批长度上的判定**一个字节都没变**。
func TestSkillSwitchUplinkGateAcceptsLegalRecordAndRejectsMalformed(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)

	legal := []struct {
		name string
		body string
		slot byte
	}{
		{name: "4-byte canonical record body", body: "0c003600", slot: 12},
		{name: "6-byte record as the shipped framing delivers it", body: "0e000c003600", slot: 12},
	}
	for _, test := range legal {
		body := skillSwitchInventoryBody(t, test.body)
		if !skillSwitchUplinkRecordBodyValid(len(body)) {
			t.Fatalf("%s: uplink gate rejected a legal %d-byte record", test.name, len(body))
		}
		handled, err := fixture.service.handleSkillSwitchInventoryCommand(
			fixture.session, currentSkillSwitchInventoryOpcode, body)
		if err != nil || !handled {
			t.Fatalf("%s: handled=%t err=%v, want handled without error", test.name, handled, err)
		}
		record, parseErr := skillswitch.ParseRecordIn(skillswitch.LayoutV4, body)
		if parseErr != nil {
			t.Fatalf("%s: parse error = %v", test.name, parseErr)
		}
		if record.Slot != test.slot || record.Mode != skillswitch.ModeRegister || record.Value != 54 {
			t.Fatalf("%s: decoded %+v, want slot=%d mode=0 value=54", test.name, record, test.slot)
		}
	}

	for _, malformed := range []string{"0c", "0c0036", "0c00360000", "0e000c0036", "0e000c00360000"} {
		body := skillSwitchInventoryBody(t, malformed)
		if skillSwitchUplinkRecordBodyValid(len(body)) {
			t.Fatalf("uplink gate accepted the malformed %d-byte body %s", len(body), malformed)
		}
		if _, err := skillswitch.ParseRecordIn(skillswitch.LayoutV4, body); err == nil {
			t.Fatalf("ParseRecordIn accepted the malformed %d-byte body %s", len(body), malformed)
		} else if !strings.Contains(err.Error(), "skill switch record shape is invalid") {
			t.Fatalf("body %s: error = %v, want the named ErrRecordShape", malformed, err)
		}
		// handler 仍然"具名处理"（handled=true）但不落库：它记 observed + reason。
		handled, err := fixture.service.handleSkillSwitchInventoryCommand(
			fixture.session, currentSkillSwitchInventoryOpcode, body)
		if err != nil || !handled {
			t.Fatalf("malformed %s: handled=%t err=%v, want handled without error", malformed, handled, err)
		}
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	if !strings.Contains(log, "reason=unexpected_body_len") {
		t.Fatalf("a malformed uplink record must be logged by name; log=\n%s", log)
	}
	// 下行闸门不变：它只认 1+8*count。
	for _, length := range []int{4, 6, 5, 7} {
		if skillSwitchDownlinkFrameValid(make([]byte, length)) {
			t.Fatalf("downlink gate changed: it accepted a %d-byte frame", length)
		}
	}
	for count := 0; count <= 16; count++ {
		frame := make([]byte, 1+8*count)
		frame[0] = byte(count)
		if !skillSwitchDownlinkFrameValid(frame) {
			t.Fatalf("downlink gate rejected a legal count=%d frame", count)
		}
	}
}

// itoa 是给 `"slot_" + itoa(n)` 用的极小整数格式化（不引入 strconv 的重复导入噪音）。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// mustDecodeHex 把已落库的 hex 值解成字节（测试里断言解码用）。
func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		t.Fatalf("decode %q: %v", value, err)
	}
	return raw
}
