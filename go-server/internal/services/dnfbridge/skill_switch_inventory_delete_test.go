package dnfbridge

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「删除语义」（`DNFBRIDGE_SKILL_SWITCH_REGISTRATION_DELETE`）在**真实入口**上的钉子。
//
// 现场（HK `192.0.2.11`，`packet_log.txt` 第 197164..197200 行，2026-09-28）：
//
//	18:38:57.864 .. 18:39:00.547Z  char 1 = `test1` 推 9 条（8 条确认 + **1 条真绑定**
//	    `0c001400` = slot 12 -> value 20 —— 他在面板里只改了**一行**）
//	18:39:04.552Z 收尾哨兵 `212cffff` ⇒ `registration_kept_count=8
//	    registration_dropped_count=12`，`registration_dropped_entries=slot_1=01030100,…,
//	    slot_25=19031900` ⇒ 集合从 **20 行缩到 8 行**。
//
// 操作者原话：「选择角色后 面板里原本有的那些行消失了」。本轮默认档改成**永不删行**。

// skillSwitchDeletePreStateRows 是那次登记之前的 20 行（v4 规范形，现场逐字）。
var skillSwitchDeletePreStateRows = map[string]string{
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

// skillSwitchDeleteBurstBodies 是现场那 9 条包体（顺序照抄）。
var skillSwitchDeleteBurstBodies = []string{
	"11031100", "10031000", "12031200", "0e030e00", "0f030f00",
	"0c030c00", "13031300", "14031400", "0c001400",
}

// skillSwitchDeleteRetainedEntries 是现场那次提交删掉的 12 行（照抄 `registration_dropped_entries`），
// 默认档下它们必须**还在**，并出现在 `registration_retained_entries` 里。
const skillSwitchDeleteRetainedEntries = "slot_1=01030100,slot_2=02030200,slot_3=03030300," +
	"slot_4=04030400,slot_5=05030500,slot_6=06030600,slot_7=07030700,slot_13=0d030d00," +
	"slot_21=15031500,slot_22=16031600,slot_23=17031700,slot_25=19031900"

// skillSwitchDeleteKeepFrameHex 是**默认档**下那条边界应当发出的包体：
// **20 组 pair**（`slot 12` 是操作者改过的 `-> 20`，其余原样），手写常量。
const skillSwitchDeleteKeepFrameHex = "14" +
	"01000000" + "01000000" + "02000000" + "02000000" + "03000000" + "03000000" +
	"04000000" + "04000000" + "05000000" + "05000000" + "06000000" + "06000000" +
	"07000000" + "07000000" + "0c000000" + "14000000" + "0d000000" + "0d000000" +
	"0e000000" + "0e000000" + "0f000000" + "0f000000" + "10000000" + "10000000" +
	"11000000" + "11000000" + "12000000" + "12000000" + "13000000" + "13000000" +
	"14000000" + "14000000" + "15000000" + "15000000" + "16000000" + "16000000" +
	"17000000" + "17000000" + "19000000" + "19000000"

// skillSwitchDeleteKeepPairsText 是同一帧的可读形。
const skillSwitchDeleteKeepPairsText = "(1,1)(2,2)(3,3)(4,4)(5,5)(6,6)(7,7)(12,20)(13,13)(14,14)" +
	"(15,15)(16,16)(17,17)(18,18)(19,19)(20,20)(21,21)(22,22)(23,23)(25,25)"

// skillSwitchDeleteTodayFrameHex 是**一键回退字面量** `replace` 下那一帧的**线上实测原文**
// （18:39:08.536Z / 18:39:08.628Z / 18:48:44.067Z 三次都是它）：8 组 pair。
const skillSwitchDeleteTodayFrameHex = "08" +
	"0c000000" + "14000000" + "0e000000" + "0e000000" + "0f000000" + "0f000000" +
	"10000000" + "10000000" + "11000000" + "11000000" + "12000000" + "12000000" +
	"13000000" + "13000000" + "14000000" + "14000000"

// skillSwitchDeleteTodayPairsText 照抄线上那三次重发行的 `pairs=`。
const skillSwitchDeleteTodayPairsText = "(12,20)(14,14)(15,15)(16,16)(17,17)(18,18)(19,19)(20,20)"

// seedSkillSwitchDeletePreState 把现场那 20 行 + `window_armed=1` 写进 char 1 的 scope。
func seedSkillSwitchDeletePreState(t *testing.T, repos dnfrepo.Group) {
	t.Helper()
	values := map[string]string{
		"source":       skillswitch.SourceCurrentEXE,
		"slot_count":   "20",
		"updated_at":   "2026-09-28T16:15:36.096620903Z",
		"window_armed": "1",
		"window_count": "0",
	}
	for key, value := range skillSwitchDeletePreStateRows {
		values[key] = value
	}
	if err := repos.Settings.Save(context.Background(), dnfrepo.SettingsRecord{
		Scope:  skillswitch.Scope("1"),
		Values: values,
	}); err != nil {
		t.Fatalf("seed scope: %v", err)
	}
}

// registerSkillSwitchDeleteBurst 走真实入口推现场那 9 条 + 收尾哨兵。
func registerSkillSwitchDeleteBurst(t *testing.T, fixture *skillSwitchLoadoutFixture) {
	t.Helper()
	for _, body := range skillSwitchDeleteBurstBodies {
		applySkillSwitchRegistration(t, fixture, body)
	}
	applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)
}

// TestSkillSwitchDeleteKeepKeepsTheTwentyRows 是**本档主用例**：
// 现场那次"只改一行"的登记之后，20 行必须一行不少，边界重发也必须是 20 对。
func TestSkillSwitchDeleteKeepKeepsTheTwentyRows(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)
	seedSkillSwitchDeletePreState(t, fixture.repos)

	registerSkillSwitchDeleteBurst(t, fixture)

	after := readStoredSlotValues(t, fixture.repos, "1")
	if len(after) != len(skillSwitchDeletePreStateRows) {
		t.Fatalf("rows after the burst = %d, want all %d (values=%v)",
			len(after), len(skillSwitchDeletePreStateRows), after)
	}
	for key, want := range skillSwitchDeletePreStateRows {
		if key == "slot_12" {
			if got := after[key]; got != "0c001400" {
				t.Fatalf("slot_12 = %q, want the operator's 0c001400", got)
			}
			continue
		}
		if got := after[key]; got != want {
			t.Fatalf("%s = %q, want the untouched %q", key, got, want)
		}
	}

	// 写入方日志：默认档下 dropped 恒 0，那 12 行改记在 retained 里。
	writerLog := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"record_key=sentinel_212cffff",
		"registration_committed=true",
		"registration_kept_count=8",
		"registration_delete=" + skillswitch.RegistrationDeleteKeep,
		"registration_delete_default=" + skillswitch.RegistrationDeleteKeep,
		"registration_delete_rollback_literal=" + skillswitch.RegistrationDeleteReplace,
		"registration_dropped_count=0",
		"registration_retained_count=12",
		"registration_retained_entries=" + skillSwitchDeleteRetainedEntries,
	} {
		if !strings.Contains(writerLog, want) {
			t.Fatalf("writer log missing %q; log=\n%s", want, writerLog)
		}
	}

	// 回到选择角色 → 重新进入：边界重发必须是那 20 对。
	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want exactly the one loadout frame", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchDeleteKeepFrameHex {
		t.Fatalf("boundary body = %s\nwant            %s", got, skillSwitchDeleteKeepFrameHex)
	}
	boundaryLog := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"loadout_rows=20",
		"pairs=" + skillSwitchDeleteKeepPairsText,
		"body_hex=" + skillSwitchDeleteKeepFrameHex,
	} {
		if !strings.Contains(boundaryLog, want) {
			t.Fatalf("resend log missing %q; log=\n%s", want, boundaryLog)
		}
	}
}

// TestSkillSwitchDeleteRollbackLiteralIsByteForByteToday 钉**一键回退字面量** `replace`：
// 落库与下行**逐字节等于线上今天这一份**（20 → 8，那一帧 `08 0c000000 14000000 …`）。
func TestSkillSwitchDeleteRollbackLiteralIsByteForByteToday(t *testing.T) {
	t.Setenv(skillswitch.RegistrationDeleteEnv, skillswitch.RegistrationDeleteReplace)
	fixture := newSkillSwitchLoadoutFixture(t)
	seedSkillSwitchDeletePreState(t, fixture.repos)

	registerSkillSwitchDeleteBurst(t, fixture)

	if got := len(readStoredSlotValues(t, fixture.repos, "1")); got != 8 {
		t.Fatalf("rows = %d, want today's 8", got)
	}
	writerLog := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"registration_delete=" + skillswitch.RegistrationDeleteReplace,
		"registration_dropped_count=12",
		"registration_retained_count=0",
		"registration_dropped_entries=" + skillSwitchDeleteRetainedEntries,
	} {
		if !strings.Contains(writerLog, want) {
			t.Fatalf("rollback writer log missing %q; log=\n%s", want, writerLog)
		}
	}

	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, fixture.repos)
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchDeleteTodayFrameHex {
		t.Fatalf("rollback body = %s\nwant today's   %s", got, skillSwitchDeleteTodayFrameHex)
	}
	boundaryLog := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"loadout_rows=8",
		"pairs=" + skillSwitchDeleteTodayPairsText,
		"body_hex=" + skillSwitchDeleteTodayFrameHex,
	} {
		if !strings.Contains(boundaryLog, want) {
			t.Fatalf("rollback resend log missing %q; log=\n%s", want, boundaryLog)
		}
	}
}

// TestSkillSwitchDeleteKeepStillBoundsTheWindow 钉"永不删行"**没有**把窗口记账户搞坏：
// 裸哨兵（零记录窗口）照旧不提交、`slot_count` 不动。
func TestSkillSwitchDeleteKeepStillBoundsTheWindow(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)
	seedSkillSwitchDeletePreState(t, fixture.repos)

	for index := 0; index < 3; index++ {
		applySkillSwitchRegistration(t, fixture, skillSwitchSentinelWireBody)
	}
	after := readStoredSlotValues(t, fixture.repos, "1")
	if len(after) != len(skillSwitchDeletePreStateRows) {
		t.Fatalf("bare sentinels changed the slot set: %d rows, want %d",
			len(after), len(skillSwitchDeletePreStateRows))
	}
	writerLog := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	if strings.Contains(writerLog, "registration_committed=true") {
		t.Fatalf("a bare sentinel must never commit; log=\n%s", writerLog)
	}
	if !strings.Contains(writerLog, "registration_retained_count=0") {
		t.Fatalf("a bare sentinel must retain nothing; log=\n%s", writerLog)
	}
}
