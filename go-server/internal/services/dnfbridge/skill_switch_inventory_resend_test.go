package dnfbridge

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// 「Buff 强化 / 技能换装」换装栏**跨选择角色重下发**（`DNFBRIDGE_SKILL_SWITCH_RESEND`）
// 的钉子。硬要求（对应 `_dist/buff-swap-lost-on-select.md`）：
//
//  1. **往返**：登记 → 从仓储重建（= 重新选角）→ 同一批行仍被服务，且下行是
//     **客户端入站形状** `u8 count + count×(u32,u32)`，逐字节等于**手写常量**；
//  2. **只在该发的边界发**：选角/进场景路径真的发出去；空换装**零字节**；
//  3. **回退字面量逐字节等于今天**：`shape_guard` 下本档一行代码都不执行；
//  4. **两个闸门分开**：4/6 字节的上行记录永远过不了 `1+8*count`。
//
// 现场来源（char 6 = `test78`，account `dnf:10011`，conn `game-000021`，
// 2026-09-28T13:52:40..44Z 客户端自己推上来的 14 条登记，与
// `packet_log.txt:101616..101949` 的 `hex=` 行尾 + HK 库 `dnf_setting_values`
// 的 `character:6:skill_switch_inventory` 14 条 `slot_*` 行逐条一致）。

// skillSwitchLoadoutLiveBodies 是操作者那次登记里 14 个槽位的**真包体**（4 字节）。
// 与库内 14 条状态行一一对应（`slot_3` 在库里是改动前的 6 字节行，
// dual-read 之后字段相同 ⇒ 这里用 4 字节规范形喂进去）。
var skillSwitchLoadoutLiveBodies = []string{
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

// skillSwitchLoadoutLiveSlots 是那 14 行的槽位（升序）——手写常量，不引用实现。
var skillSwitchLoadoutLiveSlots = []int{3, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 25}

// skillSwitchLoadoutExpectedBodyHex 是这 14 行在**默认档**（pair = 该行自己的
// `(Slot, Value)`，`DNFBRIDGE_SKILL_SWITCH_RESEND_PAIRS` 未设时的修复档）应当产生的
// **完整包体**：`u8 count=0x0e` + 14 组 `(Slot,Value)`（各 u32 LE）。
//
// 手写常量，构造代码写错时逐字节红灯。逐对的来源是那 14 条真记录的字段
// （`03012f00` → (3,47)、`0c003600` → (12,54)、`0d030d00` → (13,13)…）。
const skillSwitchLoadoutExpectedBodyHex = "0e" +
	"03000000" + "2f000000" + "0c000000" + "36000000" + "0d000000" + "0d000000" +
	"0e000000" + "3e000000" + "0f000000" + "3d000000" + "10000000" + "39000000" +
	"11000000" + "3b000000" + "12000000" + "3f000000" + "13000000" + "3a000000" +
	"14000000" + "40000000" + "15000000" + "3c000000" + "16000000" + "38000000" +
	"17000000" + "37000000" + "19000000" + "19000000"

// skillSwitchLoadoutExpectedPairsText 是同一个包体的可读形式（日志字段逐字比对用）。
const skillSwitchLoadoutExpectedPairsText = "(3,47)(12,54)(13,13)(14,62)(15,61)(16,57)(17,59)" +
	"(18,63)(19,58)(20,64)(21,60)(22,56)(23,55)(25,25)"

// skillSwitchLoadoutIdentityBodyHex / ...IdentityPairsText 是**一键回退字面量**
// `DNFBRIDGE_SKILL_SWITCH_RESEND_PAIRS=identity` 的字节形：恒等对 `(slot,slot)`，
// 逐字节等于上一版线上发出的那 15 帧（手写常量，回退必须可对拍）。
const skillSwitchLoadoutIdentityBodyHex = "0e" +
	"0300000003000000" + "0c0000000c000000" + "0d0000000d000000" + "0e0000000e000000" +
	"0f0000000f000000" + "1000000010000000" + "1100000011000000" + "1200000012000000" +
	"1300000013000000" + "1400000014000000" + "1500000015000000" + "1600000016000000" +
	"1700000017000000" + "1900000019000000"

const skillSwitchLoadoutIdentityPairsText = "(3,3)(12,12)(13,13)(14,14)(15,15)(16,16)(17,17)" +
	"(18,18)(19,19)(20,20)(21,21)(22,22)(23,23)(25,25)"

// skillSwitchLoadoutFixture 造一个带**这 14 个技能号**的 job-scoped 目录的 service
// （共享目录 fixture 只含现场最小集合，故意不动它）。
type skillSwitchLoadoutFixture struct {
	service    *Service
	session    *gameSession
	connection *bufferConn
	logger     *packetLogger
	logPath    string
	repos      dnfrepo.Group
}

func newSkillSwitchLoadoutFixture(t *testing.T) *skillSwitchLoadoutFixture {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "1",
		AccountID:   "dnf:10001",
		Job:         "14",
		Level:       90,
		Name:        "test2",
	}); err != nil {
		t.Fatal(err)
	}
	return newSkillSwitchLoadoutFixtureWithRepos(t, repos)
}

// newSkillSwitchLoadoutFixtureWithRepos 同上，但显式指定仓储 ——
// "重新选角"就是**新的 service/session + 同一份仓储**（与既有边界用例同口径）。
func newSkillSwitchLoadoutFixtureWithRepos(t *testing.T, repos dnfrepo.Group) *skillSwitchLoadoutFixture {
	t.Helper()
	logger, logPath := newPeerEndpointTestLogger(t)
	t.Cleanup(func() { _ = logger.Close() })
	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:                            "dnf:10001",
			gameUpperHeader:                      gameUpperHeaderChannel13,
			gameUpperBodyCodec:                   gameUpperBodyCodecPlain,
			skillSwitchInventoryMode:             skillSwitchInventoryShapeGuardTier,
			skillSwitchInventoryRecordBodyLayout: skillswitch.LayoutV4,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
		packetLog:          logger,
		skillCatalog:       skillSwitchLoadoutCatalog(t),
	}
	session := &gameSession{
		conn:                connection,
		connID:              "skill-switch-loadout-resend-test",
		channel:             channelcatalog.Channel{ID: 10},
		selectedCharacterID: 1,
		accountID:           "dnf:10001",
	}
	return &skillSwitchLoadoutFixture{
		service:    service,
		session:    session,
		connection: connection,
		logger:     logger,
		logPath:    logPath,
		repos:      repos,
	}
}

// skillSwitchLoadoutCatalog 是 job 14 的技能目录，含那 14 个槽位引用的全部技能号。
func skillSwitchLoadoutCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	source := initialEquipmentMemSource{}
	list := ""
	for _, id := range skillSwitchLoadoutLiveSlots {
		doc := "skill/job14/skill" + strconv.Itoa(id) + ".skl"
		list += strconv.Itoa(id) + " `" + doc + "`\n"
		source[doc] = "[skill type]\n`active`\n"
	}
	source["skill/skilllist.lst"] = "14 `job14.lst`\n"
	source["skill/job14.lst"] = list
	catalog, err := buildSkillCatalogFromSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// registerSkillSwitchLoadout 走**真实入口**（handleSkillSwitchInventoryCommand）
// 把 14 条登记落库，返回落库后从仓储读回的记录（= 客户端上行真正留下的状态）。
func registerSkillSwitchLoadout(t *testing.T, fixture *skillSwitchLoadoutFixture) skillswitch.State {
	t.Helper()
	for _, body := range skillSwitchLoadoutLiveBodies {
		if _, err := fixture.service.handleSkillSwitchInventoryCommand(
			fixture.session,
			currentSkillSwitchInventoryOpcode,
			skillSwitchInventoryBody(t, body),
		); err != nil {
			t.Fatalf("register body %s: %v", body, err)
		}
	}
	if got := fixture.connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("op1428 uplink must not answer inline, wrote %x", got)
	}
	state, found, err := skillswitch.Load(context.Background(), fixture.repos, "1", skillswitch.LayoutV4)
	if err != nil || !found {
		t.Fatalf("load persisted loadout found=%t err=%v", found, err)
	}
	return state
}

// TestSkillSwitchLoadoutResendSurvivesCharacterSelectRoundTrip 是这条缺陷的**主用例**：
// 登记 14 行 → 换一个 service/session（= 重新选角，同一份仓储）→ 选角边界投影 →
// 同一批行仍被服务，且下行是客户端入站形状、逐字节等于手写常量。
func TestSkillSwitchLoadoutResendSurvivesCharacterSelectRoundTrip(t *testing.T) {
	writer := newSkillSwitchLoadoutFixture(t)
	before := registerSkillSwitchLoadout(t, writer)
	if len(before.Records) != len(skillSwitchLoadoutLiveSlots) {
		t.Fatalf("persisted %d rows, want %d", len(before.Records), len(skillSwitchLoadoutLiveSlots))
	}
	if got := before.Source; got != skillswitch.SourceCurrentEXE {
		t.Fatalf("persisted loadout source = %q, want %q", got, skillswitch.SourceCurrentEXE)
	}
	if before.UpdatedAt.IsZero() {
		t.Fatal("persisted loadout must carry the save timestamp (loadout_saved_at)")
	}

	// 重新选角：新的 service + 新的 session，共用同一份仓储。
	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, writer.repos)
	after, found, err := skillswitch.Load(context.Background(), writer.repos, "1", skillswitch.LayoutV4)
	if err != nil || !found {
		t.Fatalf("re-load persisted loadout found=%t err=%v", found, err)
	}
	if len(after.Records) != len(before.Records) {
		t.Fatalf("re-entry lost rows: %d -> %d", len(before.Records), len(after.Records))
	}
	for index := range before.Records {
		if !before.Records[index].SameFields(after.Records[index]) {
			t.Fatalf("row[%d] changed across the round trip: %v -> %v",
				index, before.Records[index], after.Records[index])
		}
	}

	const boundary = "initial_town_before_final_transition_after_mode1_without_mode3_before_typed_op24_skill_switch_inventory"
	if err := reentry.service.sendSkillSwitchInventoryProjection(reentry.session, boundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("character-select boundary wrote %d packets, want exactly the one loadout frame", len(packets))
	}
	if packets[0].msgID != currentSkillSwitchInventoryOpcode {
		t.Fatalf("loadout frame msg_id = %d, want %d", packets[0].msgID, currentSkillSwitchInventoryOpcode)
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchLoadoutExpectedBodyHex {
		t.Fatalf("loadout frame body = %s\nwant                %s", got, skillSwitchLoadoutExpectedBodyHex)
	}
	if len(packets[0].body) != 1+8*len(skillSwitchLoadoutLiveSlots) {
		t.Fatalf("loadout frame len = %d, want %d (u8 count + count×(u32,u32))",
			len(packets[0].body), 1+8*len(skillSwitchLoadoutLiveSlots))
	}
	if packets[0].body[0] != byte(len(skillSwitchLoadoutLiveSlots)) {
		t.Fatalf("count byte = %d, want %d", packets[0].body[0], len(skillSwitchLoadoutLiveSlots))
	}
	if !skillSwitchDownlinkFrameValid(packets[0].body) {
		t.Fatal("the loadout frame must pass the 1+8*count gate by construction")
	}

	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchLoadoutResendEvent,
		"char_id=1",
		"scope=character:1:skill_switch_inventory",
		"source=" + boundary,
		"loadout_rows=14",
		"pair_count=14",
		"count=14",
		"body_len=113",
		"shape_guard=pass",
		"downlink_shape_gate=len_eq_1_plus_8_times_count",
		"uplink_record_gate=len_in_4_or_6",
		"pair_mapping=slot_records_value",
		"pair_value_source=record_value_field",
		"pairs_switch=" + skillSwitchLoadoutPairsRecord,
		"pairs_switch_default=" + skillSwitchLoadoutPairsRecord,
		"pairs_rollback_literal=" + skillSwitchLoadoutPairsIdentity,
		"family2_values_ever_sent=true",
		"pairs=" + skillSwitchLoadoutExpectedPairsText,
		"body_hex=" + skillSwitchLoadoutExpectedBodyHex,
		"loadout_source=" + skillswitch.SourceCurrentEXE,
		"loadout_saved_at=" + after.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		"resent_at=",
		"msg_id=1428",
		"classification=0",
		"body_source=built_from_persisted_skill_switch_rows",
		"resend_switch=" + skillSwitchLoadoutResendSlots,
		"resend_switch_default=" + skillSwitchLoadoutResendSlots,
		"resend_rollback_literal=" + skillSwitchLoadoutResendShapeGuard,
		"supersedes_candidate_pairs=false",
		"proof_state=unproven_pair_semantics",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("loadout resend log missing %q; log=\n%s", want, log)
		}
	}
	// 上行回放路径**照旧全部被拦**：这 14 行一条也没走老路（两个闸门分开的证据）。
	for _, want := range []string{
		"EVENT kind=" + skillSwitchInventoryProjectionBlockedEvent,
		"slot_count=14",
		"sent_count=0",
		"blocked_count=14",
		"uplink_record_gate_result=true",
		"downlink_frame_gate_result=false",
		"reason=body_shape_not_client_s2c_map",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("shape-guard block log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchLoadoutResendEmitsOnLegacySceneReadyBoundary 钉"选角/进场景这条路上
// 真的发出去"——走**客户端自己的 legacy type1345「场景就绪」**边界（重选角色时
// op37/op30 都不会发生，这条是重选角色唯一到达的边界）。
func TestSkillSwitchLoadoutResendEmitsOnLegacySceneReadyBoundary(t *testing.T) {
	writer := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, writer)

	reentry := newSkillSwitchLoadoutFixtureWithRepos(t, writer.repos)
	reentry.service.repositoryProvider = func() (dnfrepo.Group, bool) { return writer.repos, true }
	armSceneReadyAckBoundary(reentry.session)

	if err := reentry.service.handleLegacyGamePacket(reentry.session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatalf("legacy type1345 boundary: %v", err)
	}
	packets := drainUpperPackets(t, reentry.connection)
	if len(packets) != 1 {
		t.Fatalf("type1345 boundary wrote %d packets, want exactly the one loadout frame", len(packets))
	}
	if packets[0].msgID != currentSkillSwitchInventoryOpcode {
		t.Fatalf("loadout frame msg_id = %d, want %d", packets[0].msgID, currentSkillSwitchInventoryOpcode)
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchLoadoutExpectedBodyHex {
		t.Fatalf("loadout frame body = %s\nwant                %s", got, skillSwitchLoadoutExpectedBodyHex)
	}
	log := readPeerEndpointTestLog(t, reentry.logger, reentry.logPath)
	if !strings.Contains(log, "EVENT kind="+skillSwitchLoadoutResendEvent) ||
		!strings.Contains(log, "source=legacy_type1345_scene_ready_ack_skill_switch_inventory_after_scene_ready") {
		t.Fatalf("type1345 boundary must log the loadout resend with its source; log=\n%s", log)
	}
}

// TestSkillSwitchLoadoutResendEmptyLoadoutEmitsNothing 钉**空换装零字节**：
// 没有落库行时一个字节都不发，只留既有 skipped 事件（`slot_count=0`）。
func TestSkillSwitchLoadoutResendEmptyLoadoutEmitsNothing(t *testing.T) {
	fixture := newSkillSwitchLoadoutFixture(t)
	if err := fixture.service.sendSkillSwitchInventoryProjection(fixture.session, "empty_loadout_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	if packets := drainUpperPackets(t, fixture.connection); len(packets) != 0 {
		t.Fatalf("empty loadout wrote %d packets, want 0", len(packets))
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	if strings.Contains(log, skillSwitchLoadoutResendEvent) {
		t.Fatalf("empty loadout must not log %s; log=\n%s", skillSwitchLoadoutResendEvent, log)
	}
	for _, want := range []string{
		"EVENT kind=" + skillSwitchInventorySkippedEvent,
		"slot_count=0",
		"reason=no_persisted_skill_switch_inventory",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("empty loadout log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchLoadoutResendNotOnForensicTiers 钉层与层的关系：取证/回退档
// （`project` = 原样回放取证档）**不叠加**本档，逐字节保持改动前。
func TestSkillSwitchLoadoutResendNotOnForensicTiers(t *testing.T) {
	writer := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, writer)

	forensic := newSkillSwitchLoadoutFixtureWithRepos(t, writer.repos)
	forensic.service.repositoryProvider = func() (dnfrepo.Group, bool) { return writer.repos, true }
	forensic.service.options.skillSwitchInventoryMode = skillSwitchInventoryProjectTier
	if err := forensic.service.sendSkillSwitchInventoryProjection(forensic.session, "project_tier_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, forensic.connection)
	if len(packets) != len(skillSwitchLoadoutLiveSlots) {
		t.Fatalf("project tier wrote %d packets, want %d verbatim uplink replays",
			len(packets), len(skillSwitchLoadoutLiveSlots))
	}
	for index, packet := range packets {
		want := skillSwitchLoadoutLiveBodiesSorted(t)[index]
		if got := hex.EncodeToString(packet.body); got != want {
			t.Fatalf("project tier packet[%d] body = %s, want %s", index, got, want)
		}
	}
	log := readPeerEndpointTestLog(t, forensic.logger, forensic.logPath)
	if strings.Contains(log, skillSwitchLoadoutResendEvent) {
		t.Fatalf("forensic tiers must not gain the loadout resend; log=\n%s", log)
	}
}

// skillSwitchLoadoutLiveBodiesSorted 返回那 14 条真包体按槽位升序的 hex
// （仓储 Load 已按 Slot 升序，这里手写排序以免引用实现顺序）。
func skillSwitchLoadoutLiveBodiesSorted(t *testing.T) []string {
	t.Helper()
	bySlot := make(map[int]string, len(skillSwitchLoadoutLiveBodies))
	for _, body := range skillSwitchLoadoutLiveBodies {
		raw := skillSwitchInventoryBody(t, body)
		bySlot[int(raw[0])] = body
	}
	out := make([]string, 0, len(skillSwitchLoadoutLiveSlots))
	for _, slot := range skillSwitchLoadoutLiveSlots {
		body, present := bySlot[slot]
		if !present {
			t.Fatalf("live bodies have no slot %d", slot)
		}
		out = append(out, body)
	}
	return out
}

// TestSkillSwitchLoadoutResendRollbackLiteralIsByteForByteToday 钉**一键回退**：
// `shape_guard`（+ 实验默认关）= 零字节、零本档日志 —— 与改动前逐字节相同；
// 实验同时打开时，线上下来的字节与今天那帧**逐字节相同**（手写常量）。
func TestSkillSwitchLoadoutResendRollbackLiteralIsByteForByteToday(t *testing.T) {
	writer := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, writer)

	for _, literal := range []string{
		skillSwitchLoadoutResendShapeGuard, "off", "TODAY", "legacy", "rollback", "0", "disable",
	} {
		t.Setenv(skillSwitchLoadoutResendEnv, literal)
		rollback := newSkillSwitchLoadoutFixtureWithRepos(t, writer.repos)
		rollback.service.repositoryProvider = func() (dnfrepo.Group, bool) { return writer.repos, true }
		if err := rollback.service.sendSkillSwitchInventoryProjection(rollback.session, "rollback_boundary"); err != nil {
			t.Fatalf("literal %q projection error = %v", literal, err)
		}
		if packets := drainUpperPackets(t, rollback.connection); len(packets) != 0 {
			t.Fatalf("literal %q wrote %d packets, want 0 (byte-for-byte today)", literal, len(packets))
		}
		log := readPeerEndpointTestLog(t, rollback.logger, rollback.logPath)
		if strings.Contains(log, skillSwitchLoadoutResendEvent) {
			t.Fatalf("literal %q must not log %s; log=\n%s", literal, skillSwitchLoadoutResendEvent, log)
		}
		if !strings.Contains(log, "EVENT kind="+skillSwitchInventoryProjectionBlockedEvent) ||
			!strings.Contains(log, "sent_count=0") {
			t.Fatalf("literal %q must keep today's block-only behaviour; log=\n%s", literal, log)
		}
	}

	// 回退档 + 候选实验（线上现行组合）：字节与今天**逐字节相同**。
	t.Setenv(skillSwitchLoadoutResendEnv, skillSwitchLoadoutResendShapeGuard)
	withCandidate := newSkillSwitchLoadoutFixtureWithRepos(t, writer.repos)
	withCandidate.service.repositoryProvider = func() (dnfrepo.Group, bool) { return writer.repos, true }
	withCandidate.service.options.skillSwitchCandidatePairsMode = skillSwitchCandidatePairsIdentity12
	if err := withCandidate.service.sendSkillSwitchInventoryProjection(withCandidate.session, "rollback_with_candidate"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, withCandidate.connection)
	if len(packets) != 1 {
		t.Fatalf("rollback + candidate wrote %d packets, want exactly today's one candidate frame", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchCandidateIdentity12BodyHex {
		t.Fatalf("rollback + candidate body = %s\nwant today's      %s", got, skillSwitchCandidateIdentity12BodyHex)
	}
}

// TestSkillSwitchLoadoutResendAfterCandidateFrameWinsTheMap 钉两者同时在场的顺序：
// 候选实验帧在前、落库重下发帧**在后**（客户端那张 map 上本帧胜出），
// 且事件里 `supersedes_candidate_pairs=true`。
func TestSkillSwitchLoadoutResendAfterCandidateFrameWinsTheMap(t *testing.T) {
	writer := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, writer)

	both := newSkillSwitchLoadoutFixtureWithRepos(t, writer.repos)
	both.service.repositoryProvider = func() (dnfrepo.Group, bool) { return writer.repos, true }
	both.service.options.skillSwitchCandidatePairsMode = skillSwitchCandidatePairsIdentity12
	if err := both.service.sendSkillSwitchInventoryProjection(both.session, "both_switches_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, both.connection)
	if len(packets) != 2 {
		t.Fatalf("both switches wrote %d packets, want 2 (candidate then loadout)", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchCandidateIdentity12BodyHex {
		t.Fatalf("packet[0] = %s, want the candidate frame first", got)
	}
	if got := hex.EncodeToString(packets[1].body); got != skillSwitchLoadoutExpectedBodyHex {
		t.Fatalf("packet[1] = %s\nwant the loadout frame last %s", got, skillSwitchLoadoutExpectedBodyHex)
	}
	log := readPeerEndpointTestLog(t, both.logger, both.logPath)
	if !strings.Contains(log, "supersedes_candidate_pairs=true") {
		t.Fatalf("the loadout event must record that it supersedes the candidate frame; log=\n%s", log)
	}
}

// TestSkillSwitchInventoryGuardSeparation 钉**两个闸门分开**（本档的根因修复）：
// 上行记录体（4/6 字节）与下行帧（`1+8*count`）的判据互不重叠，
// 且没有任何长度会被两边同时接受。
func TestSkillSwitchInventoryGuardSeparation(t *testing.T) {
	cases := []struct {
		name        string
		body        []byte
		uplinkValid bool
		frameValid  bool
	}{
		{name: "v4 record body (the live 4-byte op1428 record)", body: skillSwitchInventoryBody(t, "0c003600"), uplinkValid: true, frameValid: false},
		{name: "v6 wire body (2 prefix bytes + 4-byte record)", body: skillSwitchInventoryBody(t, "26000c003600"), uplinkValid: true, frameValid: false},
		{name: "count 0 frame (1 byte)", body: []byte{0}, uplinkValid: false, frameValid: true},
		{name: "count 14 frame (the loadout resend body)", body: skillSwitchInventoryBody(t, skillSwitchLoadoutExpectedBodyHex), uplinkValid: false, frameValid: true},
		{name: "count 12 candidate frame", body: skillSwitchInventoryBody(t, skillSwitchCandidateIdentity12BodyHex), uplinkValid: false, frameValid: true},
		{name: "count 38 short frame (what 0x26 would demand)", body: []byte{38, 0, 0, 0, 0}, uplinkValid: false, frameValid: false},
	}
	for _, test := range cases {
		if got := skillSwitchUplinkRecordBodyValid(len(test.body)); got != test.uplinkValid {
			t.Fatalf("%s: uplink record gate(%d bytes) = %v, want %v",
				test.name, len(test.body), got, test.uplinkValid)
		}
		if got := skillSwitchDownlinkFrameValid(test.body); got != test.frameValid {
			t.Fatalf("%s: downlink frame gate(%d bytes) = %v, want %v",
				test.name, len(test.body), got, test.frameValid)
		}
	}
	// 判据集合本身不重叠：4 与 6 永远不可能是 1+8c（这是"默认档结构性 0 下行"的算术根因），
	// 也没有任何长度会被两道闸门同时接受。
	for length := 0; length <= 64; length++ {
		frameValid := length > 0 && (length-1)%8 == 0
		if skillSwitchUplinkRecordBodyValid(length) && frameValid {
			t.Fatalf("length %d is accepted by both gates", length)
		}
	}
	for _, length := range []int{1, 9, 17, 113} {
		if skillSwitchUplinkRecordBodyValid(length) {
			t.Fatalf("downlink frame length %d must never pass the uplink record gate", length)
		}
	}
}

// TestSkillSwitchLoadoutPairMappingInvariants 是**表驱动**的 pair 不变量，
// 两个档位各钉一遍：
//
//	默认档 record   —— pair = 该行自己的 `(Slot, Value)`，**忠实回显**客户端上行字段；
//	回退档 identity —— pair = `(Slot, Slot)`，逐字节等于上一版线上那 15 帧。
//
// 记录取**操作者现场那 14 条**（含 `Value ∈ {47,54..64}`），所以"发出去的值到底
// 是不是客户端自己发上来的"是拿真数据钉的：把映射写反（两个档位互换）两条断言
// 会同时红灯；把 `Value` 换成任何别的来源（证伪变体 `falsify-pair-value.go`）
// 默认档那半会红灯。
func TestSkillSwitchLoadoutPairMappingInvariants(t *testing.T) {
	records := make([]skillswitch.Record, 0, len(skillSwitchLoadoutLiveBodies))
	for _, body := range skillSwitchLoadoutLiveBodies {
		record, err := skillswitch.ParseRecord(skillSwitchInventoryBody(t, body))
		if err != nil {
			t.Fatalf("parse live body %s: %v", body, err)
		}
		records = append(records, record)
	}
	if len(records) != len(skillSwitchLoadoutLiveSlots) {
		t.Fatalf("live records = %d, want %d", len(records), len(skillSwitchLoadoutLiveSlots))
	}

	// 默认档：pair = (Slot, Value)，逐条等于那条真记录的字段。
	pairs, err := skillSwitchLoadoutPairs(records, skillSwitchLoadoutPairsRecord)
	if err != nil {
		t.Fatalf("pairs error = %v", err)
	}
	if len(pairs) != len(records) {
		t.Fatalf("pairs = %d, want one pair per persisted row (%d)", len(pairs), len(records))
	}
	for index, pair := range pairs {
		if pair.Slot != uint32(records[index].Slot) {
			t.Fatalf("pair[%d] slot = %d, want %d", index, pair.Slot, records[index].Slot)
		}
		if pair.Value != uint32(records[index].Value) {
			t.Fatalf("pair[%d] = (%d,%d), want the record's own value (%d,%d)",
				index, pair.Slot, pair.Value, records[index].Slot, records[index].Value)
		}
	}
	// 现场那 11 条 Mode 0 行的 Value ∈ {54..64} 加一条 47：默认档**如实回显**
	// （这正是"操作者刚选的槽位"那一半信息），所以这个探针必须是 true；
	// 回退档必须 false。两个方向都钉，免得某个档位悄悄变成另一个。
	if !skillSwitchLoadoutPairsCarryFamily2Values(records, skillSwitchLoadoutPairsRecord) {
		t.Fatal("the default tier echoes the record Value (47/54..64); the probe must report true")
	}
	if skillSwitchLoadoutPairsCarryFamily2Values(records, skillSwitchLoadoutPairsIdentity) {
		t.Fatal("the identity rollback literal must never carry a record Value")
	}

	// 回退档：恒等对，且与默认档在 Mode 0 行上**必须不同**（不然两档没区别）。
	identityPairs, err := skillSwitchLoadoutPairs(records, skillSwitchLoadoutPairsIdentity)
	if err != nil {
		t.Fatalf("identity pairs error = %v", err)
	}
	differs := false
	for index, pair := range identityPairs {
		if pair.Slot != uint32(records[index].Slot) || pair.Value != pair.Slot {
			t.Fatalf("identity pair[%d] = (%d,%d), want (slot,slot)", index, pair.Slot, pair.Value)
		}
		if pair.Value != uint32(records[index].Value) {
			differs = true
		}
	}
	if !differs {
		t.Fatal("the two tiers must differ on the live Mode 0 rows (value != slot)")
	}

	// 逐字节确定性 + 两个档位各自的完整包体（手写常量）。
	first, err := buildSkillSwitchLoadoutBody(records, skillSwitchLoadoutPairsRecord)
	if err != nil {
		t.Fatalf("build error = %v", err)
	}
	second, err := buildSkillSwitchLoadoutBody(records, skillSwitchLoadoutPairsRecord)
	if err != nil {
		t.Fatalf("rebuild error = %v", err)
	}
	if hex.EncodeToString(first) != hex.EncodeToString(second) {
		t.Fatal("the same persisted rows must always produce the same frame bytes")
	}
	if got := hex.EncodeToString(first); got != skillSwitchLoadoutExpectedBodyHex {
		t.Fatalf("default-tier body = %s\nwant                %s", got, skillSwitchLoadoutExpectedBodyHex)
	}
	identityBody, err := buildSkillSwitchLoadoutBody(records, skillSwitchLoadoutPairsIdentity)
	if err != nil {
		t.Fatalf("identity build error = %v", err)
	}
	if got := hex.EncodeToString(identityBody); got != skillSwitchLoadoutIdentityBodyHex {
		t.Fatalf("identity-tier body = %s\nwant                 %s", got, skillSwitchLoadoutIdentityBodyHex)
	}
	if len(first) != len(identityBody) {
		t.Fatalf("both tiers must produce the same frame length: %d vs %d", len(first), len(identityBody))
	}

	if _, err := buildSkillSwitchLoadoutBody(nil, skillSwitchLoadoutPairsRecord); err == nil {
		t.Fatal("an empty loadout must fail construction (zero bytes, not a count=0 frame)")
	}
	tooMany := make([]skillswitch.Record, 0x100)
	for index := range tooMany {
		tooMany[index] = skillswitch.Record{Slot: byte(index)}
	}
	if _, err := buildSkillSwitchLoadoutBody(tooMany, skillSwitchLoadoutPairsRecord); err == nil {
		t.Fatal("256 rows do not fit the client u8 count and must fail construction")
	}
}

// TestSkillSwitchLoadoutResendModeNormalization 钉开关口径：
// **只有回退字面量才回退**，未设置/空/未知取值一律修复档（house idiom）。
func TestSkillSwitchLoadoutResendModeNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: skillSwitchLoadoutResendSlots},
		{value: "slots", want: skillSwitchLoadoutResendSlots},
		{value: "  SLOTS  ", want: skillSwitchLoadoutResendSlots},
		{value: "on", want: skillSwitchLoadoutResendSlots},
		{value: "fix", want: skillSwitchLoadoutResendSlots},
		{value: "resend", want: skillSwitchLoadoutResendSlots},
		{value: "typo", want: skillSwitchLoadoutResendSlots},
		{value: skillSwitchLoadoutResendShapeGuard, want: skillSwitchLoadoutResendShapeGuard},
		{value: "off", want: skillSwitchLoadoutResendShapeGuard},
		{value: "TODAY", want: skillSwitchLoadoutResendShapeGuard},
		{value: "legacy", want: skillSwitchLoadoutResendShapeGuard},
		{value: "rollback", want: skillSwitchLoadoutResendShapeGuard},
		{value: "0", want: skillSwitchLoadoutResendShapeGuard},
		{value: "disable", want: skillSwitchLoadoutResendShapeGuard},
	} {
		if got := normalizeSkillSwitchLoadoutResendMode(test.value); got != test.want {
			t.Fatalf("normalizeSkillSwitchLoadoutResendMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
	// env 未设置 ⇒ 修复档；nil Service 什么都不做。
	t.Setenv(skillSwitchLoadoutResendEnv, "")
	if got := currentSkillSwitchLoadoutResendMode(); got != skillSwitchLoadoutResendSlots {
		t.Fatalf("unset env mode = %q, want the fixed tier %q", got, skillSwitchLoadoutResendSlots)
	}
	if ((*Service)(nil)).skillSwitchLoadoutResendActive() {
		t.Fatal("a nil Service must never resend")
	}
	// 零值 Service 的档位也必须是修复档（"默认档就是修复档"在零值路径上同样成立）；
	// 它没有仓储，所以调用方在 Load 那一步就短路了，本档拿不到状态、不会发。
	if got := (&Service{}).skillSwitchLoadoutResendMode(); got != skillSwitchLoadoutResendSlots {
		t.Fatalf("zero-value Service mode = %q, want %q", got, skillSwitchLoadoutResendSlots)
	}
	// 非默认 tier（取证/回退档）不叠加本档。
	for _, tier := range []string{
		skillSwitchInventoryProjectTier,
		skillSwitchInventoryBeforeTransitionTier,
		skillSwitchInventoryRecordTier,
		skillSwitchInventoryReferenceDrop,
	} {
		service := &Service{options: options{skillSwitchInventoryMode: tier}}
		if service.skillSwitchLoadoutResendActive() {
			t.Fatalf("tier %q must not gain the loadout resend", tier)
		}
	}
	if !(&Service{options: options{skillSwitchInventoryMode: skillSwitchInventoryShapeGuardTier}}).skillSwitchLoadoutResendActive() {
		t.Fatal("the default tier must be active by default (default = fixed)")
	}

	// pair 档位同口径：只有回退字面量才回退，未知取值一律修复档。
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: skillSwitchLoadoutPairsRecord},
		{value: "record", want: skillSwitchLoadoutPairsRecord},
		{value: "  RECORD  ", want: skillSwitchLoadoutPairsRecord},
		{value: "on", want: skillSwitchLoadoutPairsRecord},
		{value: "fix", want: skillSwitchLoadoutPairsRecord},
		{value: "slot_value", want: skillSwitchLoadoutPairsRecord},
		{value: "typo", want: skillSwitchLoadoutPairsRecord},
		{value: skillSwitchLoadoutPairsIdentity, want: skillSwitchLoadoutPairsIdentity},
		{value: "slot", want: skillSwitchLoadoutPairsIdentity},
		{value: "off", want: skillSwitchLoadoutPairsIdentity},
		{value: "TODAY", want: skillSwitchLoadoutPairsIdentity},
		{value: "legacy", want: skillSwitchLoadoutPairsIdentity},
		{value: "rollback", want: skillSwitchLoadoutPairsIdentity},
		{value: "0", want: skillSwitchLoadoutPairsIdentity},
		{value: "disable", want: skillSwitchLoadoutPairsIdentity},
	} {
		if got := normalizeSkillSwitchLoadoutPairsMode(test.value); got != test.want {
			t.Fatalf("normalizeSkillSwitchLoadoutPairsMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
	t.Setenv(skillSwitchLoadoutPairsEnv, "")
	if got := currentSkillSwitchLoadoutPairsMode(); got != skillSwitchLoadoutPairsRecord {
		t.Fatalf("unset pairs env = %q, want the fixed tier %q", got, skillSwitchLoadoutPairsRecord)
	}
	if got := (&Service{}).skillSwitchLoadoutPairsMode(); got != skillSwitchLoadoutPairsRecord {
		t.Fatalf("zero-value Service pairs mode = %q, want %q", got, skillSwitchLoadoutPairsRecord)
	}
	if got := ((*Service)(nil)).skillSwitchLoadoutPairsMode(); got != skillSwitchLoadoutPairsIdentity {
		t.Fatalf("nil Service pairs mode = %q, want the identity fallback", got)
	}
}

// TestSkillSwitchLoadoutPairsRollbackLiteralIsByteForBytePreviousRelease 钉
// `DNFBRIDGE_SKILL_SWITCH_RESEND_PAIRS=identity` 这一半的**独立**回退：
//
//   - 帧字节逐字节等于上一版线上那 15 帧（手写常量 `...IdentityBodyHex`）；
//   - 事件里 `pair_mapping` / `pair_value_source` / `family2_values_ever_sent`
//     如实改成恒等那一套（不能出现"字节是恒等对、日志说是 record"这种自相矛盾）。
//
// 只翻这一个 env ⇒ 另一半（重发开关、登记窗口）保持修复档，A/B 可以一次测一半。
func TestSkillSwitchLoadoutPairsRollbackLiteralIsByteForBytePreviousRelease(t *testing.T) {
	writer := newSkillSwitchLoadoutFixture(t)
	registerSkillSwitchLoadout(t, writer)

	for _, literal := range []string{
		skillSwitchLoadoutPairsIdentity, "slot", "OFF", "today", "legacy", "rollback", "0", "disable",
	} {
		t.Setenv(skillSwitchLoadoutPairsEnv, literal)
		rollback := newSkillSwitchLoadoutFixtureWithRepos(t, writer.repos)
		rollback.service.repositoryProvider = func() (dnfrepo.Group, bool) { return writer.repos, true }
		if err := rollback.service.sendSkillSwitchInventoryProjection(rollback.session, "pairs_rollback_boundary"); err != nil {
			t.Fatalf("literal %q projection error = %v", literal, err)
		}
		packets := drainUpperPackets(t, rollback.connection)
		if len(packets) != 1 {
			t.Fatalf("literal %q wrote %d packets, want exactly the one loadout frame", literal, len(packets))
		}
		if got := hex.EncodeToString(packets[0].body); got != skillSwitchLoadoutIdentityBodyHex {
			t.Fatalf("literal %q body = %s\nwant previous release %s", literal, got, skillSwitchLoadoutIdentityBodyHex)
		}
		log := readPeerEndpointTestLog(t, rollback.logger, rollback.logPath)
		for _, want := range []string{
			"EVENT kind=" + skillSwitchLoadoutResendEvent,
			"pairs_switch=" + skillSwitchLoadoutPairsIdentity,
			"pair_mapping=slot_identity",
			"pair_value_source=slot_field_not_record_value",
			"pair_evidence=key_set_from_persisted_op1428_rows_value_reuses_slot",
			"family2_values_ever_sent=false",
			"pairs=" + skillSwitchLoadoutIdentityPairsText,
			"body_hex=" + skillSwitchLoadoutIdentityBodyHex,
		} {
			if !strings.Contains(log, want) {
				t.Fatalf("literal %q log missing %q; log=\n%s", literal, want, log)
			}
		}
	}
	// 未设 env ⇒ 修复档（默认档就是修复档，回退只认字面量）。
	t.Setenv(skillSwitchLoadoutPairsEnv, "")
	if got := currentSkillSwitchLoadoutPairsMode(); got != skillSwitchLoadoutPairsRecord {
		t.Fatalf("unset pairs env = %q, want %q", got, skillSwitchLoadoutPairsRecord)
	}
}
