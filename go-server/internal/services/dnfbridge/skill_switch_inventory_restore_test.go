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

// 「绑定还原」（`DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE`）在**真实边界**上的钉子。
//
// 现场（HK `192.0.2.11`，2026-09-28，原始行见 `_dist/buff-swap-round4.md`）：
// `keep` 档上线（服务 17:45:20Z 重启）后，运维在 17:50:26..17:50:30Z 重登记了一次，
// 那 13 条记录**全是 `Value == Slot`**（`ModeConfirm`，不含绑定），落库
// `registration_dropped_count=0`（没有任何东西被破坏），于是 17:50:35.867Z 起的每一次
// 重发都是 `pairs=(12,12)(13,13)…(25,25)` —— 这些 value 指向主背包（list 0）的
// 12..25 号格子，而 char 6 的 11 件换装物品在主背包的 **54..64** 号格子。
// 面板 = op1428 map × 容器 ⇒ value 指不到物品 ⇒ **界面为空**。
//
// 本文件的判据 = 那条边界**真的发出去的字节**（手写常量对拍）+ 落库未被改写 + 事件字段。

// skillSwitchRestoreLiveRows 是现场 `character:6:skill_switch_inventory` 现在那 13 行。
var skillSwitchRestoreLiveRows = map[string]string{
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

// skillSwitchRestoreLiveForensics 是客户端自己为这些槽位声明过绑定的取证键。
var skillSwitchRestoreLiveForensics = map[string]string{
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
}

// skillSwitchRestoreFrameHex 是**修复档**下这条边界应当发出的完整包体：13 组 pair，
// 其中 11 组的 value 重新指向换装物品所在的背包格子（54..64），13 / 25 保持原样。
// 手写常量（不引用实现），逐字节对拍。
const skillSwitchRestoreFrameHex = "0d" +
	"0c000000" + "36000000" + // slot 12 -> 54
	"0d000000" + "0d000000" + // slot 13 -> 13
	"0e000000" + "3e000000" + // slot 14 -> 62
	"0f000000" + "3d000000" + // slot 15 -> 61
	"10000000" + "39000000" + // slot 16 -> 57
	"11000000" + "3b000000" + // slot 17 -> 59
	"12000000" + "3f000000" + // slot 18 -> 63
	"13000000" + "3a000000" + // slot 19 -> 58
	"14000000" + "40000000" + // slot 20 -> 64
	"15000000" + "3c000000" + // slot 21 -> 60
	"16000000" + "38000000" + // slot 22 -> 56
	"17000000" + "37000000" + // slot 23 -> 55
	"19000000" + "19000000" // slot 25 -> 25

// skillSwitchRestorePairsText 是同一帧的可读形（日志字段逐字比对）。
const skillSwitchRestorePairsText = "(12,54)(13,13)(14,62)(15,61)(16,57)(17,59)(18,63)" +
	"(19,58)(20,64)(21,60)(22,56)(23,55)(25,25)"

// skillSwitchRestoreTodayFrameHex 是**一键回退字面量**（`off`）下同一条边界发出的那一帧的
// **线上实测原文**（`packet_log.txt` 17:50:35.867Z / 18:02:17.450Z 等 8 条）：13 组恒等对。
const skillSwitchRestoreTodayFrameHex = "0d" +
	"0c000000" + "0c000000" + "0d000000" + "0d000000" + "0e000000" + "0e000000" +
	"0f000000" + "0f000000" + "10000000" + "10000000" + "11000000" + "11000000" +
	"12000000" + "12000000" + "13000000" + "13000000" + "14000000" + "14000000" +
	"15000000" + "15000000" + "16000000" + "16000000" + "17000000" + "17000000" +
	"19000000" + "19000000"

// skillSwitchRestoreTodayPairsText 照抄线上那 8 条重发行的 `pairs=`。
const skillSwitchRestoreTodayPairsText = "(12,12)(13,13)(14,14)(15,15)(16,16)(17,17)" +
	"(18,18)(19,19)(20,20)(21,21)(22,22)(23,23)(25,25)"

// skillSwitchRestoreExpectedEntries 是补回来的那 11 条绑定（升序，= 帧里 value 54..64 的那些）。
const skillSwitchRestoreExpectedEntries = "slot_12=0c003600,slot_14=0e003e00,slot_15=0f003d00," +
	"slot_16=10003900,slot_17=11003b00,slot_18=12003f00,slot_19=13003a00," +
	"slot_20=14004000,slot_21=15003c00,slot_22=16003800,slot_23=17003700"

// seedSkillSwitchRestoreScope 把现场那 13 行 + 取证键原样写进 char 6 的 scope。
func seedSkillSwitchRestoreScope(t *testing.T, repos dnfrepo.Group) {
	t.Helper()
	values := map[string]string{
		"source":       skillswitch.SourceCurrentEXE,
		"slot_count":   "13",
		"updated_at":   "2026-09-28T17:50:30.499032224Z",
		"window_armed": "1",
		"window_count": "0",
	}
	for key, value := range skillSwitchRestoreLiveRows {
		values[key] = value
	}
	for key, value := range skillSwitchRestoreLiveForensics {
		values[key] = value
	}
	if err := repos.Settings.Save(context.Background(), dnfrepo.SettingsRecord{
		Scope:  skillswitch.Scope("6"),
		Values: values,
	}); err != nil {
		t.Fatalf("seed scope: %v", err)
	}
}

// TestSkillSwitchBindingRestoreFrameCarriesTheRealInventorySlots 是**本档主用例**：
// 现场那一份"13 行都不含绑定"的状态，在选角/进场景边界上重发的帧必须重新指向
// 换装物品所在的背包格子（54..64），并且**不写库**。
func TestSkillSwitchBindingRestoreFrameCarriesTheRealInventorySlots(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	fixture := newSkillSwitchBindFixtureWithRepos(t, repos)
	seedSkillSwitchRestoreScope(t, repos)

	if err := fixture.service.sendSkillSwitchInventoryProjection(fixture.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, fixture.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want exactly the one loadout frame", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchRestoreFrameHex {
		t.Fatalf("boundary body = %s\nwant            %s", got, skillSwitchRestoreFrameHex)
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchLoadoutResendEvent,
		"loadout_rows=13",
		"pair_count=13",
		"pairs=" + skillSwitchRestorePairsText,
		"body_hex=" + skillSwitchRestoreFrameHex,
		"binding_restore=" + skillswitch.BindingRestoreRestore,
		"binding_restore_default=" + skillswitch.BindingRestoreRestore,
		"binding_restore_rollback_literal=" + skillswitch.BindingRestoreOff,
		"restored_count=11",
		"restored_bindings=" + skillSwitchRestoreExpectedEntries,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("resend log missing %q; log=\n%s", want, log)
		}
	}

	// **不写库**：DB 里那 13 行一个字节都没动。
	stored, found, err := repos.Settings.Load(context.Background(), skillswitch.Scope("6"))
	if err != nil || !found {
		t.Fatalf("reload found=%t err=%v", found, err)
	}
	for key, want := range skillSwitchRestoreLiveRows {
		if got := stored.Values[key]; got != want {
			t.Fatalf("DB row %s = %q, want the untouched %q (this tier is read-only)", key, got, want)
		}
	}
}

// TestSkillSwitchBindingRestoreRollbackLiteralIsByteForByteToday 钉**一键回退字面量**
// `DNFBRIDGE_SKILL_SWITCH_BINDING_RESTORE=off`：同一条边界上发出的那一帧
// **逐字节等于线上今天这一份**（13 组恒等对）。
func TestSkillSwitchBindingRestoreRollbackLiteralIsByteForByteToday(t *testing.T) {
	t.Setenv(skillswitch.BindingRestoreEnv, skillswitch.BindingRestoreOff)
	repos := dnfrepomemory.NewMemoryGroup()
	fixture := newSkillSwitchBindFixtureWithRepos(t, repos)
	seedSkillSwitchRestoreScope(t, repos)

	if err := fixture.service.sendSkillSwitchInventoryProjection(fixture.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, fixture.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchRestoreTodayFrameHex {
		t.Fatalf("rollback body = %s\nwant today's   %s", got, skillSwitchRestoreTodayFrameHex)
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"pairs=" + skillSwitchRestoreTodayPairsText,
		"body_hex=" + skillSwitchRestoreTodayFrameHex,
		"binding_restore=" + skillswitch.BindingRestoreOff,
		"restored_count=0",
		"restored_bindings=-",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("rollback resend log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchBindingRestoreIsANoOpForTheOperatorSelection 钉"还原不会破坏真实登记"：
// 走真实入口登记操作者那 11 条**携带绑定**的行（ModeRegister）⇒ 边界帧逐字节等于
// 那 11 对，`restored_count=0`（没有任何东西需要还原）。
func TestSkillSwitchBindingRestoreIsANoOpForTheOperatorSelection(t *testing.T) {
	fixture := newSkillSwitchBindFixture(t)
	seedSkillSwitchBindPreState(t, fixture)

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
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"pairs=" + skillSwitchBindBoundaryPairsText,
		"restored_count=0",
		"restored_bindings=-",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("resend log missing %q; log=\n%s", want, log)
		}
	}
}
