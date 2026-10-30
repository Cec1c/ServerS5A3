package dnfbridge

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「绑定保护」（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND`）在**真实入口**
// （`handleSkillSwitchInventoryCommand` + 重投影边界）上的钉子。
//
// 操作者第六次报障（原文）：「**1 buff强化为空。登记成功后切换角色清空。**」
// 现场（HK `192.0.2.11`，`packet_log.txt` + `dnf_setting_values`，2026-09-28，
// 原始行与 SQL 见 `_dist/buff-swap-round3.md` §1/§2），全在同一角色 char 6 = `test78`
// （账号 `dnf:10011`）：
//
//	第 154407 行  16:15:41.520Z  `...-registration-commit` `commit_trigger=boundary_open_window`
//	              `window_slots=slot_12,slot_14,…,slot_23`、`written_entries=slot_12=0c003600,…`
//	              ⇒ 库里 11 行 ModeRegister，`Value ∈ {54..64}`（= list 1 里的换装物品槽位）。
//	第 174345 行  17:12:48.311Z  `...-recorded record_key=sentinel_052cffff` 且
//	              `registration_committed=true registration_kept_count=9
//	               registration_dropped_count=2
//	               registration_dropped_entries=slot_12=0c003600,slot_23=17003700`
//	              ⇒ 那 9 条 `Value == Slot` 的记录把 54..64 覆盖成 14..22，并删掉两行。
//	第 179531 行  17:21:24.576Z  `...-loadout-resend loadout_rows=9
//	               pairs=(14,14)(15,15)…(22,22)` ⇒ 面板拿到的 map 不再指向换装物品。
//
// 本文件判据 = ①settings 仓储里的 `slot_*` 集合与值；②那条边界**真的发出去的字节**
// （手写常量对拍）；③`packet_log.txt` 同格式的事件字段。

// skillSwitchBindLiveBodies 是 16:15:41Z 那次提交的 `written_entries`（逐字节照抄）。
var skillSwitchBindLiveBodies = []string{
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

// skillSwitchBindConfirmBodies 是 17:12:45.073..17:12:47.763Z 那 9 条现场记录
// （`packet_log.txt` 第 174308..174340 行），全部 `Value == Slot` = 不含绑定信息。
var skillSwitchBindConfirmBodies = []string{
	"11031100", "12031200", "0e030e00", "0f030f00", "10031000",
	"16031600", "15031500", "13031300", "14031400",
}

// skillSwitchBindStraySentinelBody 是 17:12:48.292Z 那条 `ModeClear` 的真包体
// （`RECV … len=19 hex=019405130000007cc381903c000000052cffff`）。
const skillSwitchBindStraySentinelBody = "052cffff"

// skillSwitchBindBoundaryFrameHex 是**修复档**下这条边界应当发出的完整包体：
// `u8 count=11` + 11 组 `(Slot,Value)`（各 u32 LE），值来自操作者自己那 11 行。
// 手写常量（不引用实现），逐字节对拍。
const skillSwitchBindBoundaryFrameHex = "0b" +
	"0c000000" + "36000000" +
	"0e000000" + "3e000000" +
	"0f000000" + "3d000000" +
	"10000000" + "39000000" +
	"11000000" + "3b000000" +
	"12000000" + "3f000000" +
	"13000000" + "3a000000" +
	"14000000" + "40000000" +
	"15000000" + "3c000000" +
	"16000000" + "38000000" +
	"17000000" + "37000000"

// skillSwitchBindBoundaryPairsText 是同一批 pair 的可读形（日志字段逐字比对）。
const skillSwitchBindBoundaryPairsText = "(12,54)(14,62)(15,61)(16,57)(17,59)(18,63)" +
	"(19,58)(20,64)(21,60)(22,56)(23,55)"

// skillSwitchBindRollbackFrameHex 是**一键回退字面量**（`replace`）在同一条边界上发出的
// 那一帧的**线上实测原文**（`packet_log.txt` 第 179531 行的 `body_hex=`）：9 行恒等对。
const skillSwitchBindRollbackFrameHex = "09" +
	"0e000000" + "0e000000" + "0f000000" + "0f000000" + "10000000" + "10000000" +
	"11000000" + "11000000" + "12000000" + "12000000" + "13000000" + "13000000" +
	"14000000" + "14000000" + "15000000" + "15000000" + "16000000" + "16000000"

// skillSwitchBindRollbackPairsText 照抄第 179531 行的 `pairs=`。
const skillSwitchBindRollbackPairsText = "(14,14)(15,15)(16,16)(17,17)(18,18)(19,19)(20,20)(21,21)(22,22)"

// skillSwitchBindStoredConfirmRows 是**回退档**下落库的那 9 行（= 现场 SQL 原文）。
const skillSwitchBindStoredConfirmRows = "slot_14=0e030e00,slot_15=0f030f00,slot_16=10031000," +
	"slot_17=11031100,slot_18=12031200,slot_19=13031300,slot_20=14031400," +
	"slot_21=15031500,slot_22=16031600"

// skillSwitchBindStoredLiveRows 是**修复档**下落库的那 11 行（不受那 9 条影响）。
const skillSwitchBindStoredLiveRows = "slot_12=0c003600,slot_14=0e003e00,slot_15=0f003d00," +
	"slot_16=10003900,slot_17=11003b00,slot_18=12003f00,slot_19=13003a00," +
	"slot_20=14004000,slot_21=15003c00,slot_22=16003800,slot_23=17003700"

// newSkillSwitchBindFixture 造**现场形状**的 service + session：char 6 = `test78`
// （账号 `dnf:10011`）。
//
// 与线上唯一不同的字段是 `job`：线上 char 6 是 job 0，而共享的技能目录 fixture 是按
// job 14 建的，`Slot` 只用于 PVF 存在性门禁，不影响本文件任何判据。
func newSkillSwitchBindFixture(t *testing.T) *skillSwitchLoadoutFixture {
	t.Helper()
	return newSkillSwitchBindFixtureWithRepos(t, dnfrepomemory.NewMemoryGroup())
}

func newSkillSwitchBindFixtureWithRepos(t *testing.T, repos dnfrepo.Group) *skillSwitchLoadoutFixture {
	t.Helper()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "6",
		AccountID:   "dnf:10011",
		Job:         "14",
		Level:       90,
		Name:        "test78",
	}); err != nil {
		t.Fatal(err)
	}
	fixture := newSkillSwitchLoadoutFixtureWithRepos(t, repos)
	fixture.session.selectedCharacterID = 6
	fixture.session.accountID = "dnf:10011"
	fixture.service.options.accountID = "dnf:10011"
	return fixture
}

// seedSkillSwitchBindPreState 用 `union` 档把 16:15 那次登记的 11 行落进去，
// 再单独补上 `window_armed=1` —— 线上这个 scope 在 16:15:18.742Z 收到哨兵时就已经
// arm 了，所以 17:12 那 9 条记录是**直接进窗口**（不是进 pending）的。
func seedSkillSwitchBindPreState(t *testing.T, fixture *skillSwitchLoadoutFixture) {
	t.Helper()
	t.Setenv(skillswitch.RegistrationModeEnv, skillswitch.RegistrationUnion)
	for _, body := range skillSwitchBindLiveBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	t.Setenv(skillswitch.RegistrationModeEnv, "")
	if got := skillSwitchBindStoredRows(t, fixture.repos); got != skillSwitchBindStoredLiveRows {
		t.Fatalf("seeded state = %s\nwant           %s", got, skillSwitchBindStoredLiveRows)
	}
	stored, found, err := fixture.repos.Settings.Load(context.Background(), skillswitch.Scope("6"))
	if err != nil || !found {
		t.Fatalf("scope found=%t err=%v", found, err)
	}
	stored = dnfrepo.CloneSettings(stored)
	stored.Values["window_armed"] = "1"
	if err := fixture.repos.Settings.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
}

// skillSwitchBindStoredRows 把 scope 里的 `slot_*` 渲染成 `slot_<n>=<hex>`（升序）。
// 刻意**不**复用 `readStoredSlotValues`（map 无序，日志/失败信息不可逐字对拍）。
func skillSwitchBindStoredRows(t *testing.T, repos dnfrepo.Group) string {
	t.Helper()
	stored, found, err := repos.Settings.Load(context.Background(), skillswitch.Scope("6"))
	if err != nil || !found {
		t.Fatalf("scope found=%t err=%v", found, err)
	}
	parts := make([]string, 0, len(stored.Values))
	for _, slot := range []int{3, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 25} {
		key := "slot_" + strconv.Itoa(slot)
		if value, present := stored.Values[key]; present {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, ",")
}

// TestSkillSwitchBindConfirmBurstCannotDestroyTheLoadout 是**本档的主用例**：
// 走真实入口复现操作者的完整流程（登记 → 那 9 条 `Value == Slot` 的记录 + 一条陌生的
// `ModeClear` → 回到选择角色再进入），断言落库集合与重发字节**都还是那 11 对**。
func TestSkillSwitchBindConfirmBurstCannotDestroyTheLoadout(t *testing.T) {
	fixture := newSkillSwitchBindFixture(t)
	seedSkillSwitchBindPreState(t, fixture)

	for _, body := range skillSwitchBindConfirmBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	if got := skillSwitchBindStoredRows(t, fixture.repos); got != skillSwitchBindStoredLiveRows {
		t.Fatalf("after the 9 confirm records: %s\nwant                      %s",
			got, skillSwitchBindStoredLiveRows)
	}
	// 那条陌生的哨兵（`Slot=5`，破了"Slot ≡ 0x21"那条老不变量，但 `Mode` 仍是 0x2c）。
	applySkillSwitchRegistration(t, fixture, skillSwitchBindStraySentinelBody)
	if got := skillSwitchBindStoredRows(t, fixture.repos); got != skillSwitchBindStoredLiveRows {
		t.Fatalf("the stray sentinel destroyed the loadout: %s\nwant                      %s",
			got, skillSwitchBindStoredLiveRows)
	}

	// 写入方日志：每一条都自带"这一条含不含绑定 / 有没有保住已落库的绑定"。
	writerLog := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchInventoryRecordedEvent,
		"registration_bind=" + skillswitch.RegistrationBindKeep,
		"registration_bind_default=" + skillswitch.RegistrationBindKeep,
		"registration_bind_rollback_literal=" + skillswitch.RegistrationBindReplace,
		"binding_bearing=false",
		"binding_preserved=true",
		"preserved_hex=11003b00", // slot 17 原本的绑定（59）被保住（它是那 9 条之一）
		"preserved_hex=0e003e00", // slot 14 原本的绑定（62）同上
		// 哨兵那一行：提交**发生了**（窗口里有 9 条），但它是 confirm-only。
		"record_key=sentinel_052cffff",
		"registration_committed=true",
		"registration_confirm_only=true",
		"registration_protected_count=2",
		"registration_protected_entries=slot_12=0c003600,slot_23=17003700",
		"registration_dropped_count=0",
		"registration_dropped_entries=-",
		"registration_kept_count=9",
	} {
		if !strings.Contains(writerLog, want) {
			t.Fatalf("writer log missing %q; log=\n%s", want, writerLog)
		}
	}

	// 回到选择角色 → 重新进入：重投影边界。
	reentry := newSkillSwitchBindFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want exactly the one loadout frame", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBindBoundaryFrameHex {
		t.Fatalf("boundary body = %s\nwant            %s", got, skillSwitchBindBoundaryFrameHex)
	}
	boundaryLog := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchLoadoutResendEvent,
		"source=" + skillSwitchBurstBoundary,
		"loadout_rows=11",
		"pair_count=11",
		"pairs=" + skillSwitchBindBoundaryPairsText,
		"pair_value_source=record_value_field",
		"body_hex=" + skillSwitchBindBoundaryFrameHex,
	} {
		if !strings.Contains(boundaryLog, want) {
			t.Fatalf("resend log missing %q; log=\n%s", want, boundaryLog)
		}
	}
}

// TestSkillSwitchBindStraySentinelAloneCannotDestroyTheLoadout 是任务书点名的那两个
// wipe 形状：**孤立哨兵**（窗口零记录）与**空窗口**。两者都必须零写入。
func TestSkillSwitchBindStraySentinelAloneCannotDestroyTheLoadout(t *testing.T) {
	fixture := newSkillSwitchBindFixture(t)
	seedSkillSwitchBindPreState(t, fixture)

	for i := 0; i < 2; i++ {
		applySkillSwitchRegistration(t, fixture, skillSwitchBindStraySentinelBody)
	}
	applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)
	if got := skillSwitchBindStoredRows(t, fixture.repos); got != skillSwitchBindStoredLiveRows {
		t.Fatalf("bare sentinels changed the state: %s\nwant                      %s",
			got, skillSwitchBindStoredLiveRows)
	}
	writerLog := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"record_key=sentinel_052cffff",
		"record_key=sentinel_212cffff",
		"registration_committed=false",
		"registration_trigger=-",
	} {
		if !strings.Contains(writerLog, want) {
			t.Fatalf("writer log missing %q; log=\n%s", want, writerLog)
		}
	}
	if strings.Contains(writerLog, "registration_committed=true") {
		t.Fatalf("a bare sentinel must never commit; log=\n%s", writerLog)
	}

	// 换角色再进入：重发帧仍是那 11 对。
	reentry := newSkillSwitchBindFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBindBoundaryFrameHex {
		t.Fatalf("body = %s\nwant  %s", got, skillSwitchBindBoundaryFrameHex)
	}
}

// TestSkillSwitchBindRollbackLiteralIsByteForByteToday 钉**一键回退字面量**
// `DNFBRIDGE_SKILL_SWITCH_REGISTRATION_BIND=replace`：同一条现场序列下落库与下行
// **逐字节等于线上今天这一份**（9 行 + 那一帧 `090e0000…`）。
func TestSkillSwitchBindRollbackLiteralIsByteForByteToday(t *testing.T) {
	// 本用例钉的是"窗口提交 = 替换（删掉窗口外的行）"那套旧语义与它的线上字节。
	// 第五轮把默认档改成 `keep`（窗口提交一行都不删），所以这里显式把删除语义按
	// **回退字面量**打开（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）；默认档下的
	// 新判据在 `skill_switch_inventory_delete_test.go`（仓内对应 `registration_delete_test.go`）。
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	t.Setenv(skillswitch.RegistrationBindEnv, skillswitch.RegistrationBindReplace)
	// 复现"改动前那一版"的字节需要**两个**回退字面量：本档（同槽位覆盖 + 窗口全量替换）
	// 与 `DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE=off`（第四轮新增的"读取视图绑定还原"，
	// 那一版二进制没有它）。两个都设上 ⇒ 下行逐字节等于线上今天这一份。
	t.Setenv(skillswitch.BindingRestoreEnv, skillswitch.BindingRestoreOff)
	fixture := newSkillSwitchBindFixture(t)
	seedSkillSwitchBindPreState(t, fixture)
	for _, body := range skillSwitchBindConfirmBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	applySkillSwitchRegistration(t, fixture, skillSwitchBindStraySentinelBody)
	if got := skillSwitchBindStoredRows(t, fixture.repos); got != skillSwitchBindStoredConfirmRows {
		t.Fatalf("rollback state = %s\nwant today's    %s", got, skillSwitchBindStoredConfirmRows)
	}
	writerLog := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"binding_preserved=false",
		"registration_confirm_only=false",
		"registration_dropped_count=2",
		"registration_dropped_entries=slot_12=0c003600,slot_23=17003700",
		"registration_protected_count=0",
	} {
		if !strings.Contains(writerLog, want) {
			t.Fatalf("rollback writer log missing %q; log=\n%s", want, writerLog)
		}
	}

	reentry := newSkillSwitchBindFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBindRollbackFrameHex {
		t.Fatalf("rollback body = %s\nwant today's   %s", got, skillSwitchBindRollbackFrameHex)
	}
	boundaryLog := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"loadout_rows=9",
		"pairs=" + skillSwitchBindRollbackPairsText,
		"body_hex=" + skillSwitchBindRollbackFrameHex,
	} {
		if !strings.Contains(boundaryLog, want) {
			t.Fatalf("rollback resend log missing %q; log=\n%s", want, boundaryLog)
		}
	}
}

// TestSkillSwitchBindConfirmOnlyWindowStillAllowsRemovals 钉"移除照旧生效"：
// 一段**全是** `Value == Slot` 的登记（现场 char 3 @15:35）仍然能在边界上删掉
// 操作者移除的那个槽位 —— `keep` 档只保护**携带绑定**的行。
func TestSkillSwitchBindConfirmOnlyWindowStillAllowsRemovals(t *testing.T) {
	// 本用例钉的是"窗口提交 = 替换（删掉窗口外的行）"那套旧语义与它的线上字节。
	// 第五轮把默认档改成 `keep`（窗口提交一行都不删），所以这里显式把删除语义按
	// **回退字面量**打开（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）；默认档下的
	// 新判据在 `skill_switch_inventory_delete_test.go`（仓内对应 `registration_delete_test.go`）。
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	fixture := newSkillSwitchBurstFixture(t)
	seedSkillSwitchBurstPreState(t, fixture, skillSwitchBurstSeedBodies, true)
	for _, body := range skillSwitchBurstOperatorBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	applySkillSwitchRegistration(t, fixture, skillSwitchBurstSentinelWireBody)

	reentry := newSkillSwitchBurstFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	after := readStoredSlotValues(t, fixture.repos, "3")
	if len(after) != 8 {
		t.Fatalf("rows = %d, want the operator's 8 (values=%v)", len(after), after)
	}
	if value, present := after["slot_14"]; present {
		t.Fatalf("the removed slot_14 survived (=%q): the confirm-only guard must not block removals", value)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBurstCommittedBodyHex {
		t.Fatalf("body = %s\nwant  %s", got, skillSwitchBurstCommittedBodyHex)
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"commit_trigger=" + skillswitch.RegistrationTriggerBoundaryPreArmBurst,
		"confirm_only=true",
		"committed_window_binding_slots=-",
		"protected_count=0",
		"dropped_entries=slot_14=0e030e00",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("commit log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchBindBindingBearingBurstStillReplacesAtTheBoundary 钉另一半：
// **携带绑定**的登记在真实入口 + 边界上仍按改动前的语义全量替换（现场 char 6 @16:15：
// 11 行 ModeRegister 删掉了更早三次登记留下的 slot_3 / slot_13 / slot_25）。
func TestSkillSwitchBindBindingBearingBurstStillReplacesAtTheBoundary(t *testing.T) {
	// 本用例钉的是"窗口提交 = 替换（删掉窗口外的行）"那套旧语义与它的线上字节。
	// 第五轮把默认档改成 `keep`（窗口提交一行都不删），所以这里显式把删除语义按
	// **回退字面量**打开（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）；默认档下的
	// 新判据在 `skill_switch_inventory_delete_test.go`（仓内对应 `registration_delete_test.go`）。
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	fixture := newSkillSwitchBindFixture(t)
	// 前态：14 行并集（把 char 6 更早那三行也落上），且**未 arm**。
	t.Setenv(skillswitch.RegistrationModeEnv, skillswitch.RegistrationUnion)
	for _, body := range append([]string{"03012f00", "0d030d00", "19031900"}, skillSwitchBindLiveBodies...) {
		applySkillSwitchRegistration(t, fixture, body)
	}
	t.Setenv(skillswitch.RegistrationModeEnv, "")
	if got := len(readStoredSlotValues(t, fixture.repos, "6")); got != 14 {
		t.Fatalf("seed rows = %d, want 14", got)
	}
	// 现场顺序：开局哨兵（arm）→ 11 条 ModeRegister → 边界提交。
	applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)
	for _, body := range skillSwitchBindLiveBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	reentry := newSkillSwitchBindFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	if got := skillSwitchBindStoredRows(t, fixture.repos); got != skillSwitchBindStoredLiveRows {
		t.Fatalf("state = %s\nwant  %s", got, skillSwitchBindStoredLiveRows)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchBindBoundaryFrameHex {
		t.Fatalf("body = %s\nwant  %s", got, skillSwitchBindBoundaryFrameHex)
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchRegistrationCommitEvent,
		"commit_trigger=" + skillswitch.RegistrationTriggerBoundaryWindow,
		"confirm_only=false",
		"protected_count=0",
		"dropped_entries=slot_3=03012f00,slot_13=0d030d00,slot_25=19031900",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("commit log missing %q; log=\n%s", want, log)
		}
	}
}
