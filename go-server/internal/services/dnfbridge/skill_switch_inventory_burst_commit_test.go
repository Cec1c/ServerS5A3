package dnfbridge

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「arm 之前那一段 burst」（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM`）在**真实入口**
// （`handleSkillSwitchInventoryCommand` + 重投影边界）上的钉子。
//
// 这一档修的是操作者第五次报障：「**我重新登记完 buff 换装后，点击选择角色后重新进入
// 该角色，界面被清空。**」现场（HK `192.0.2.11`，`packet_log.txt`，2026-09-28）：
//
//	第 140899..140968 行  15:35:02.929..15:35:05.201Z  char 3 推 8 条 op1428 记录
//	第 140996 行          15:35:06.848Z               **一个** `21 2c ff ff` 哨兵（最后）
//	第 141300/141303 行   15:35:09.908Z               重投影边界（选角后重新进入）
//
// `game-skill-switch-inventory-registration-commit` 全日志 **0 条**：
// 那 8 条到达时 `window_armed` 还不存在（上一次哨兵 14:26:13.673Z 是旧二进制处理的，
// 只留下 `sentinel_212cffff` 取证键），于是 8 条都没进窗口；收尾哨兵与随后的边界
// 面对的都是空窗口 ⇒ 不提交 ⇒ 库里仍是并集（`slot_14 = 0e030e00` 留着，
// 而操作者这次**没有选** slot 14）。
//
// 现场字符：char 3 = `test2`（job 14），账号 `dnf:10001`。

// skillSwitchBurstSeedBodies 是 14:26 那次登记（= 本档要重建的**前态**），
// 顺序照抄现场：哨兵在最前，之后 9 条记录（`slot 20/13/19/21/18/14/15/16/17`）。
var skillSwitchBurstSeedBodies = []string{
	skillSwitchSentinelWireBody,
	"14031400", "0d030d00", "13031300", "15031500", "12031200",
	"0e030e00", "0f030f00", "10031000", "11031100",
}

// skillSwitchBurstOperatorBodies 是操作者 15:35 那次登记推上来的 8 条记录，
// 顺序照抄现场（第 140899..140968 行）。槽位 = {13,15,16,17,18,19,20,21}。
var skillSwitchBurstOperatorBodies = []string{
	"11031100", "10031000", "12031200", "0f030f00",
	"14031400", "13031300", "15031500", "0d030d00",
}

// skillSwitchBurstSentinelWireBody 是 char 3 那一个收尾哨兵的**现场字节**
// （`0e00` = 分帧残留在包体前的 2 字节，不是字段；真包体恒为 `212cffff`）。
const skillSwitchBurstSentinelWireBody = "0e00212cffff"

// skillSwitchBurstCommittedBodyHex 是修复档下这条边界应当发出的**完整包体**：
// `u8 count=8` + 8 组 `(Slot,Value)`（各 u32 LE），槽位 = 操作者选中的那 8 个。
// 手写常量（不引用实现），逐字节对拍。
const skillSwitchBurstCommittedBodyHex = "08" +
	"0d000000" + "0d000000" + "0f000000" + "0f000000" +
	"10000000" + "10000000" + "11000000" + "11000000" +
	"12000000" + "12000000" + "13000000" + "13000000" +
	"14000000" + "14000000" + "15000000" + "15000000"

// skillSwitchBurstCommittedPairsText 是同一批 pair 的可读形（日志字段逐字比对）。
const skillSwitchBurstCommittedPairsText = "(13,13)(15,15)(16,16)(17,17)(18,18)(19,19)(20,20)(21,21)"

// skillSwitchBurstUnionBodyHex 是**线上当前这一版**（= 回退字面量 `sentinel`）
// 在同一个边界上发出的那**一帧的实测原文**（`packet_log.txt` 第 141303 行的
// `body_hex=`）：9 行并集，`slot_14` 还在（`0e000000 0e000000`）。
const skillSwitchBurstUnionBodyHex = "09" +
	"0d000000" + "0d000000" + "0e000000" + "0e000000" +
	"0f000000" + "0f000000" + "10000000" + "10000000" +
	"11000000" + "11000000" + "12000000" + "12000000" +
	"13000000" + "13000000" + "14000000" + "14000000" +
	"15000000" + "15000000"

// skillSwitchBurstUnionPairsText 是那一帧的 pair 文本（照抄第 141303 行的 `pairs=`）。
const skillSwitchBurstUnionPairsText = "(13,13)(14,14)(15,15)(16,16)(17,17)(18,18)(19,19)(20,20)(21,21)"

// skillSwitchBurstBoundary 是现场那两条边界的名字（第 141300/141408 行）。
const skillSwitchBurstBoundary = "initial_town_before_final_transition_after_mode1_without_mode3_before_typed_op24_skill_switch_inventory"

// newSkillSwitchBurstFixture 造**现场字符**（char 3 = test2 / job 14 / dnf:10001）的
// service + session，并把 14:26 那次登记以前态的字节落进去。
//
// 关键：前态必须与线上 `character:3:skill_switch_inventory` 逐字节一致 ——
// 9 行 `slot_*` + `sentinel_212cffff` 取证键，**没有** `window_armed` / `window_*` /
// `pending_*`。用 `union` 档做种子正是这个原因：它写的键与**旧二进制**完全相同
// （哨兵只留取证键、不 arm），于是 "15:35:02 那一刻的真实状态" 在测试里可重建。
func newSkillSwitchBurstFixture(t *testing.T) *skillSwitchLoadoutFixture {
	t.Helper()
	return newSkillSwitchBurstFixtureWithRepos(t, dnfrepomemory.NewMemoryGroup())
}

func newSkillSwitchBurstFixtureWithRepos(t *testing.T, repos dnfrepo.Group) *skillSwitchLoadoutFixture {
	t.Helper()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   "dnf:10001",
		Job:         "14",
		Level:       90,
		Name:        "test2",
	}); err != nil {
		t.Fatal(err)
	}
	fixture := newSkillSwitchLoadoutFixtureWithRepos(t, repos)
	fixture.session.selectedCharacterID = 3
	return fixture
}

// seedSkillSwitchBurstPreState 用 `union` 档落 14:26 那次登记（= 旧二进制的字节），
// 再把档位恢复到修复档。`wantSentinel` 决定这次种子是否包含哨兵（= scope 里有没有
// `sentinel_*` 取证键）。返回时 scope 里应当只有 9 行 `slot_*`（+ 可选的 `sentinel_*`）。
func seedSkillSwitchBurstPreState(t *testing.T, fixture *skillSwitchLoadoutFixture, bodies []string, wantSentinel bool) {
	t.Helper()
	t.Setenv(skillswitch.RegistrationModeEnv, skillswitch.RegistrationUnion)
	for _, body := range bodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	t.Setenv(skillswitch.RegistrationModeEnv, "")
	stored := readStoredSlotValues(t, fixture.repos, "3")
	if len(stored) != 9 {
		t.Fatalf("seeded %d rows, want the live 9 (values=%v)", len(stored), stored)
	}
	if stored["slot_14"] != "0e030e00" {
		t.Fatalf("seeded slot_14 = %q, want the live 0e030e00", stored["slot_14"])
	}
	scope := readStoredScopeValues(t, fixture.repos, "3")
	if _, present := scope["sentinel_212cffff"]; present != wantSentinel {
		t.Fatalf("sentinel_212cffff present=%t, want %t; scope=%v", present, wantSentinel, scope)
	}
	if scope["window_armed"] != "" {
		t.Fatalf("the live pre-state is NOT armed (window_armed=%q): that is the whole defect",
			scope["window_armed"])
	}
}

// TestSkillSwitchRegistrationPreArmBurstCommitsOnSingleSentinel 是本档的**主用例**，
// 走真实入口复现操作者的实际用法：一段 8 条记录 + **一个收尾哨兵**，
// 然后"回到选择角色再进入"（新的 service/session + 同一份仓储）。
func TestSkillSwitchRegistrationPreArmBurstCommitsOnSingleSentinel(t *testing.T) {
	// 本用例钉的是"窗口提交 = 替换（删掉窗口外的行）"那套旧语义与它的线上字节。
	// 第五轮把默认档改成 `keep`（窗口提交一行都不删），所以这里显式把删除语义按
	// **回退字面量**打开（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）；默认档下的
	// 新判据在 `skill_switch_inventory_delete_test.go`（仓内对应 `registration_delete_test.go`）。
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	fixture := newSkillSwitchBurstFixture(t)
	seedSkillSwitchBurstPreState(t, fixture, skillSwitchBurstSeedBodies, true)

	// 操作者的那一段登记：8 条记录，然后**一个**收尾哨兵。
	for _, body := range skillSwitchBurstOperatorBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	// 这 8 条进的是 `pending_*`（arm 还没打开、但这个 scope 带着哨兵取证键）。
	midScope := readStoredScopeValues(t, fixture.repos, "3")
	if midScope["window_armed"] != "" {
		t.Fatal("the scope must stay unarmed until the sentinel arrives")
	}
	pending := 0
	for key := range midScope {
		if strings.HasPrefix(key, "pending_") && key != "pending_count" {
			pending++
		}
	}
	if pending != len(skillSwitchBurstOperatorBodies) {
		t.Fatalf("pending burst = %d slots, want %d (scope=%v)", pending, len(skillSwitchBurstOperatorBodies), midScope)
	}
	for key := range midScope {
		if strings.HasPrefix(key, "window_") {
			t.Fatalf("no window key may exist before the sentinel (found %q)", key)
		}
	}
	// 加法安全：窗口还没提交 ⇒ 仍然 9 行（slot_14 还在，这就是"并集"那一半）。
	if got := readStoredSlotValues(t, fixture.repos, "3"); len(got) != 9 {
		t.Fatalf("while the burst is pending the store must stay additive: %d rows, want 9", len(got))
	}
	applySkillSwitchRegistration(t, fixture, skillSwitchBurstSentinelWireBody)
	if got := readStoredSlotValues(t, fixture.repos, "3"); len(got) != 9 {
		t.Fatalf("the arming sentinel must not commit/prune anything: %d rows, want 9", len(got))
	}

	// 回到选择角色 → 重新进入该角色：重投影边界。
	reentry := newSkillSwitchBurstFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}

	// 1) 落库集合**恰好**等于面板上选中的那 8 个槽位：移除真的生效了。
	after := readStoredSlotValues(t, fixture.repos, "3")
	if len(after) != len(skillSwitchBurstOperatorBodies) {
		t.Fatalf("committed slot rows = %d, want %d (values=%v)", len(after), len(skillSwitchBurstOperatorBodies), after)
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
			t.Fatalf("committed %s = %q, want %q (the operator's own selection)", key, after[key], want)
		}
	}

	// 2) 同一条边界上的下行帧 = 那 8 对，逐字节等于手写常量。
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want exactly the one loadout frame", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBurstCommittedBodyHex {
		t.Fatalf("boundary body = %s\nwant             %s", got, skillSwitchBurstCommittedBodyHex)
	}
	if body := packets[0].body; body[0] != byte(len(skillSwitchBurstOperatorBodies)) {
		t.Fatalf("count byte = %d, want %d", body[0], len(skillSwitchBurstOperatorBodies))
	}
	if !skillSwitchDownlinkFrameValid(packets[0].body) {
		t.Fatal("the committed frame must pass the 1+8*count gate")
	}

	// 3) 提交事件把"角色 / 收到多少条 / 解码三元组 / 写了什么 / 删了什么 /
	//    是哪条边界提交的 / 开关"全部写出来。
	commitLog := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchRegistrationCommitEvent,
		"char_id=3",
		"scope=character:3:skill_switch_inventory",
		"source=" + skillSwitchBurstBoundary,
		"registration_switch=" + skillswitch.RegistrationWindow,
		"registration_switch_default=" + skillswitch.RegistrationWindow,
		"registration_rollback_literal=" + skillswitch.RegistrationUnion,
		"registration_tier=sentinel_and_boundary_commit",
		"registration_arm=" + skillswitch.RegistrationArmRecord,
		"registration_arm_default=" + skillswitch.RegistrationArmRecord,
		"registration_arm_rollback_literal=" + skillswitch.RegistrationArmSentinel,
		"commit_trigger=" + skillswitch.RegistrationTriggerBoundaryPreArmBurst,
		"promoted_records=8",
		"window_records=8",
		"window_slots=slot_13,slot_15,slot_16,slot_17,slot_18,slot_19,slot_20,slot_21",
		"slot_count=8",
		"committed=true",
		"kept_triples=(13,3,13)(15,3,15)(16,3,16)(17,3,17)(18,3,18)(19,3,19)(20,3,20)(21,3,21)",
		"dropped_count=1",
		"dropped_triples=(14,3,14)",
		"written_entries=slot_13=0d030d00,slot_15=0f030f00,slot_16=10031000," +
			"slot_17=11031100,slot_18=12031200,slot_19=13031300,slot_20=14031400,slot_21=15031500",
		"dropped_entries=slot_14=0e030e00",
		"record_body_layout=v4",
		"body_source=persisted_skill_switch_rows_after_window_commit",
	} {
		if !strings.Contains(commitLog, want) {
			t.Fatalf("registration commit log missing %q; log=\n%s", want, commitLog)
		}
	}
	// 4) 同一条边界上的重下发帧只带被选中的那 8 对。
	for _, want := range []string{
		"EVENT kind=" + skillSwitchLoadoutResendEvent,
		"loadout_rows=8",
		"pairs=" + skillSwitchBurstCommittedPairsText,
		"pair_mapping=slot_records_value",
		"pair_value_source=record_value_field",
		"body_hex=" + skillSwitchBurstCommittedBodyHex,
	} {
		if !strings.Contains(commitLog, want) {
			t.Fatalf("resend log missing %q; log=\n%s", want, commitLog)
		}
	}
	// 5) 写入方那 8 条 `recorded` 行如实写出"这一段进了 pending、还没 arm"。
	writerLog := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchInventoryRecordedEvent,
		"char_id=3",
		"registration_arm=" + skillswitch.RegistrationArmRecord,
		"registration_pending_count=8",
		"registration_window_count=0",
		"registration_committed=false",
		// 收尾哨兵那一行：arm 打开了，但它关的窗口是空的 ⇒ 不提交。
		"record_key=sentinel_212cffff",
		"registration_trigger=-",
	} {
		if !strings.Contains(writerLog, want) {
			t.Fatalf("writer log missing %q; log=\n%s", want, writerLog)
		}
	}
}

// TestSkillSwitchRegistrationPreArmBurstRollbackLiteralIsTodaysBytes 钉**一键回退
// 字面量** `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_ARM=sentinel`：同一条现场序列下，
// 落库与下行**逐字节等于线上当前这一版**（那一帧的原文就在报告里）。
func TestSkillSwitchRegistrationPreArmBurstRollbackLiteralIsTodaysBytes(t *testing.T) {
	t.Setenv(skillswitch.RegistrationArmEnv, skillswitch.RegistrationArmSentinel)
	fixture := newSkillSwitchBurstFixture(t)
	seedSkillSwitchBurstPreState(t, fixture, skillSwitchBurstSeedBodies, true)
	for _, body := range skillSwitchBurstOperatorBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	applySkillSwitchRegistration(t, fixture, skillSwitchBurstSentinelWireBody)

	scope := readStoredScopeValues(t, fixture.repos, "3")
	for key := range scope {
		if strings.HasPrefix(key, "pending_") {
			t.Fatalf("the rollback literal must not write pending keys (found %q)", key)
		}
	}

	reentry := newSkillSwitchBurstFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	// 并集留存：9 行，slot_14 仍在（= 操作者"移除了但没生效"的现场症状）。
	after := readStoredSlotValues(t, fixture.repos, "3")
	if len(after) != 9 {
		t.Fatalf("rollback literal kept %d rows, want today's 9 (values=%v)", len(after), after)
	}
	if after["slot_14"] != "0e030e00" {
		t.Fatalf("rollback literal slot_14 = %q, want today's 0e030e00", after["slot_14"])
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBurstUnionBodyHex {
		t.Fatalf("rollback body = %s\nwant today's %s", got, skillSwitchBurstUnionBodyHex)
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	if strings.Contains(log, skillSwitchRegistrationCommitEvent) {
		t.Fatalf("the rollback literal must not commit (that is today's bytes); log=\n%s", log)
	}
	for _, want := range []string{
		"EVENT kind=" + skillSwitchLoadoutResendEvent,
		"loadout_rows=9",
		"pairs=" + skillSwitchBurstUnionPairsText,
		"body_hex=" + skillSwitchBurstUnionBodyHex,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("rollback resend log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchRegistrationBurstWithoutSentinelIsTodaysBytes 钉`没有哨兵 ⇒ 逐字节
// 等于今天`这条安全性质在**有历史行**的 scope 上（不必是空库）：一段一个哨兵都没有的
// burst ⇒ 不 arm、不写任何 `window_*` 键、边界不提交、下行帧仍是并集那一帧
// （= 现场 `body_hex=090d0000...`，`slot_14` 还在）。
func TestSkillSwitchRegistrationBurstWithoutSentinelIsTodaysBytes(t *testing.T) {
	fixture := newSkillSwitchBurstFixture(t)
	seedSkillSwitchBurstPreState(t, fixture, skillSwitchBurstSeedBodies, true)
	for _, body := range skillSwitchBurstOperatorBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	// 刻意**不推哨兵**。
	scope := readStoredScopeValues(t, fixture.repos, "3")
	if scope["window_armed"] != "" {
		t.Fatalf("a sentinel-less burst must not arm anything (window_armed=%q)", scope["window_armed"])
	}
	for key := range scope {
		if strings.HasPrefix(key, "window_") {
			t.Fatalf("a sentinel-less burst must not write a window key (found %q)", key)
		}
	}
	reentry := newSkillSwitchBurstFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	if got := len(readStoredSlotValues(t, fixture.repos, "3")); got != 9 {
		t.Fatalf("stored %d rows, want the untouched 9", got)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBurstUnionBodyHex {
		t.Fatalf("body = %s\nwant today's %s", got, skillSwitchBurstUnionBodyHex)
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	if strings.Contains(log, skillSwitchRegistrationCommitEvent) {
		t.Fatalf("nothing was armed, so no commit may be logged; log=\n%s", log)
	}
}

// TestSkillSwitchRegistrationBurstWithoutSentinelForensicsStillCommits 覆盖现场
// `character:16` 那种老 scope（有历史 `slot_*` 行、**一个 `sentinel_*` 取证键都没有**）：
// 它同样不能被"取证键判据"漏掉 —— 一段 8 条记录 + 一个收尾哨兵照样在边界上提交。
func TestSkillSwitchRegistrationBurstWithoutSentinelForensicsStillCommits(t *testing.T) {
	// 本用例钉的是"窗口提交 = 替换（删掉窗口外的行）"那套旧语义与它的线上字节。
	// 第五轮把默认档改成 `keep`（窗口提交一行都不删），所以这里显式把删除语义按
	// **回退字面量**打开（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）；默认档下的
	// 新判据在 `skill_switch_inventory_delete_test.go`（仓内对应 `registration_delete_test.go`）。
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	fixture := newSkillSwitchBurstFixture(t)
	// 只落 9 条记录、不落哨兵 ⇒ 没有任何 sentinel_* 取证键（= 现场 char 16 的形状）。
	seedSkillSwitchBurstPreState(t, fixture, skillSwitchBurstSeedBodies[1:], false)
	for _, body := range skillSwitchBurstOperatorBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	applySkillSwitchRegistration(t, fixture, skillSwitchBurstSentinelWireBody)

	reentry := newSkillSwitchBurstFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	after := readStoredSlotValues(t, fixture.repos, "3")
	if len(after) != len(skillSwitchBurstOperatorBodies) {
		t.Fatalf("committed rows = %d, want %d (values=%v)", len(after), len(skillSwitchBurstOperatorBodies), after)
	}
	if value, present := after["slot_14"]; present {
		t.Fatalf("the removed slot_14 survived the commit (=%q)", value)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBurstCommittedBodyHex {
		t.Fatalf("body = %s\nwant %s", got, skillSwitchBurstCommittedBodyHex)
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchRegistrationCommitEvent,
		"char_id=3",
		"commit_trigger=" + skillswitch.RegistrationTriggerBoundaryPreArmBurst,
		"window_slots=slot_13,slot_15,slot_16,slot_17,slot_18,slot_19,slot_20,slot_21",
		"dropped_entries=slot_14=0e030e00",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("registration commit log missing %q; log=\n%s", want, log)
		}
	}
}
