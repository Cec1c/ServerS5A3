package dnfbridge

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

// op1428（ENUM_CMDPACKET_SKILL_SWITCH_INVENTORY）的回归钉子。
//
// 硬要求：
//  1. **默认档 = 修复档**：`DNFBRIDGE_SKILL_SWITCH_INVENTORY` 默认 `shape_guard`
//     （下发前按客户端 1428 入站解析器校验形状，形状不符 ⇒ 0 下行）；
//     `DNFBRIDGE_SKILL_SWITCH_RECORD_BODY` 默认 `v4`（真实包体 4 字节）；
//  2. **一键回退** `reference_drop` = 与改动前逐字节相同：
//     一个下行字节都不发、不落库，只留具名 + `body_hex` 的事件；
//     v6 = 上行读法逐字节回到改动前（6 字节行）；
//  3. 数据规则：引用了 job-scoped PVF 里不存在的技能号 ⇒ 具名拒绝、不落库。
//
// 下面的 body 全部是**现场真实样本**（HK `/opt/dnf90/logs/packet_log*.txt`
// 的 `type=1428` RECV 行尾 6 字节；本仓 RECV 行 `len=13+body_len`）。
// ⚠️ 那 6 字节 = **2 字节导线前缀（客户端 u32 序号域高 16 位）+ 4 字节真包体**
// （2026-09-28 实机更正；见 skillswitch/record.go 与
// skillswitch/record_body_layout_test.go）。所以"逐字节等于改动前"的断言显式选
// `LayoutV6`，而默认档 `LayoutV4` 的断言用去掉前 2 字节的 4 字节包体。

// 操作者 2026-09-27 的当前客户端 4 条（`conn_id=game-000016 char_id=1`）。
var skillSwitchInventoryCurrentClientBodies = []string{
	"00000c030c00",
	"000013031300",
	"000015031500",
	"000016031600",
}

// 2026-09-27 14:28 的 6 条（header_seq 337,338,339,340,348,349）整帧：
//
//	019405130000002fbf999d510100000e030e00
//	01940513000000add3528b520100000f030f00
//	01940513000000720002c65301000010031000
//	019405130000005c45f4dc5401000012031200
//	01940513000000550cf1dc5c0100001a031a00
//	01940513000000770b4c9f5d01000003030300
var skillSwitchInventoryLiveFourteenThirty = []string{
	"00000e030e00", "00000f030f00", "000010031000",
	"000012031200", "00001a031a00", "000003030300",
}

// `ModeClear` 枚举哨兵（8/8 同形）：整帧
// `0194051300000042b1b5e58f012600212cffff`。
const skillSwitchInventorySentinelBody = "2600212cffff"

// 四条边界的名字，与 skill_switch_inventory.go 顶部那张表的 source 前缀一致。
var skillSwitchInventoryBoundaries = []string{
	"relogin_op37_finish_loading",
	"select_character_initial_town_mode1_before_typed_op24",
	"select_character_or_dungeon_return_op30_after",
	"equip_unequip_op19_aligned_after_ack",
}

func skillSwitchInventoryBody(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex %q: %v", value, err)
	}
	return raw
}

// newSkillSwitchInventoryService 造一个带真实 job-scoped 技能目录的 service，
// 上行读法用**新默认档 v4**（4 字节真包体）。
// 目录只含现场出现过的技能号，4 与 10 故意缺席（用于具名拒绝）。
func newSkillSwitchInventoryService(
	t *testing.T,
	mode string,
	repos dnfrepo.Group,
) (*Service, *gameSession, *bufferConn, *packetLogger, string) {
	t.Helper()
	return newSkillSwitchInventoryServiceWithLayout(t, mode, skillswitch.LayoutV4, repos)
}

// newSkillSwitchInventoryServiceWithLayout 同上，但显式指定 op1428 上行读法档位
// （`LayoutV6` = 一键回退 = 逐字节改动前）。
func newSkillSwitchInventoryServiceWithLayout(
	t *testing.T,
	mode string,
	layout skillswitch.Layout,
	repos dnfrepo.Group,
) (*Service, *gameSession, *bufferConn, *packetLogger, string) {
	t.Helper()
	logger, logPath := newPeerEndpointTestLogger(t)
	t.Cleanup(func() { _ = logger.Close() })
	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:                            "dnf:10001",
			gameUpperHeader:                      gameUpperHeaderChannel13,
			gameUpperBodyCodec:                   gameUpperBodyCodecPlain,
			skillSwitchInventoryMode:             mode,
			skillSwitchInventoryRecordBodyLayout: layout,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
		packetLog:          logger,
		skillCatalog:       skillSwitchInventoryCatalog(t),
	}
	session := &gameSession{
		conn:                connection,
		connID:              "skill-switch-inventory-test",
		channel:             channelcatalog.Channel{ID: 10},
		selectedCharacterID: 1,
		accountID:           "dnf:10001",
	}
	return service, session, connection, logger, logPath
}

// skillSwitchInventoryExpectedBody 返回某个档位下"同一条现场样本"应当被投影/落库的字节形。
func skillSwitchInventoryExpectedBody(layout skillswitch.Layout, wireSixByteBody string) string {
	if layout == skillswitch.LayoutV6 {
		return wireSixByteBody
	}
	return wireSixByteBody[4:]
}

func newSkillSwitchInventoryRepos(t *testing.T) dnfrepo.Group {
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
	return repos
}

// skillSwitchInventoryCatalog 是 job 14 的最小技能目录，只含现场出现过的技能号
// （3/12/14/15/16/18/19/21/22/26）；skill 4 与 10 故意缺席，
// 用来做「未知技能号 ⇒ 具名拒绝」的证伪点。
func skillSwitchInventoryCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	present := []int{3, 12, 14, 15, 16, 18, 19, 21, 22, 26}
	source := initialEquipmentMemSource{}
	list := ""
	for _, id := range present {
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

// 现场 4 条当前客户端 body + 14:28 的 6 条，**两种档位**逐字段解码都必须与现场一致：
// v4（默认）读 4 字节真包体、v6（回退）逐字节复现改动前的 6 字节读法。
func TestSkillSwitchInventoryLiveBodiesParse(t *testing.T) {
	tests := []struct {
		body string
		want skillswitch.Record
	}{
		{"00000c030c00", skillswitch.Record{Slot: 12, Mode: 0x03, Value: 12}},
		{"000013031300", skillswitch.Record{Slot: 19, Mode: 0x03, Value: 19}},
		{"000015031500", skillswitch.Record{Slot: 21, Mode: 0x03, Value: 21}},
		{"000016031600", skillswitch.Record{Slot: 22, Mode: 0x03, Value: 22}},
		{"00000e030e00", skillswitch.Record{Slot: 14, Mode: 0x03, Value: 14}},
		{"00000f030f00", skillswitch.Record{Slot: 15, Mode: 0x03, Value: 15}},
		{"000010031000", skillswitch.Record{Slot: 16, Mode: 0x03, Value: 16}},
		{"000012031200", skillswitch.Record{Slot: 18, Mode: 0x03, Value: 18}},
		{"00001a031a00", skillswitch.Record{Slot: 26, Mode: 0x03, Value: 26}},
		{"000003030300", skillswitch.Record{Slot: 3, Mode: 0x03, Value: 3}},
		{skillSwitchInventorySentinelBody, skillswitch.Record{WirePrefix: 0x26, Slot: 0x21, Mode: 0x2c, Value: skillswitch.ValueNone}},
	}
	for _, test := range tests {
		body := skillSwitchInventoryBody(t, test.body)
		if len(body) != currentSkillSwitchInventoryBodySize {
			t.Fatalf("body %s length = %d", test.body, len(body))
		}
		// v4（默认档）：去掉前 2 个非包体字节，字段完全相同。
		got, err := skillswitch.ParseRecord(body)
		if err != nil {
			t.Fatalf("v4 body %s: %v", test.body, err)
		}
		want := test.want
		want.Layout = skillswitch.LayoutV4
		want.WirePrefix = 0
		if !got.SameFields(want) || got.Layout != skillswitch.LayoutV4 {
			t.Fatalf("v4 body %s parsed %+v, want %+v", test.body, got, want)
		}
		if got.Hex() != test.body[4:] {
			t.Fatalf("v4 body %s round trip = %s, want %s", test.body, got.Hex(), test.body[4:])
		}
		// v6（回退档）：逐字节等于改动前。
		legacyWant := want
		legacyWant.Layout = skillswitch.LayoutV6
		legacyWant.WirePrefix = test.want.WirePrefix
		legacy, err := skillswitch.ParseRecordIn(skillswitch.LayoutV6, body)
		if err != nil {
			t.Fatalf("v6 body %s: %v", test.body, err)
		}
		if legacy != legacyWant {
			t.Fatalf("v6 body %s parsed %+v, want %+v", test.body, legacy, legacyWant)
		}
		if legacy.Hex() != test.body {
			t.Fatalf("v6 body %s round trip = %s", test.body, legacy.Hex())
		}
	}
}

// 默认档（options 里的 mode 为空 = 零值 = 修复档）：收下 + 落库 + **本包内不回包**。
func TestSkillSwitchInventoryDefaultTierPersistsAndDoesNotReplyInline(t *testing.T) {
	repos := newSkillSwitchInventoryRepos(t)
	service, session, connection, logger, logPath := newSkillSwitchInventoryService(t, "", repos)
	if got := service.skillSwitchInventoryMode(); got != skillSwitchInventoryShapeGuardTier {
		t.Fatalf("zero-value mode = %q, want %q", got, skillSwitchInventoryShapeGuardTier)
	}
	for _, body := range skillSwitchInventoryCurrentClientBodies {
		handled, err := service.handleSkillSwitchInventoryCommand(
			session,
			currentSkillSwitchInventoryOpcode,
			skillSwitchInventoryBody(t, body),
		)
		if err != nil || !handled {
			t.Fatalf("body %s handled=%t err=%v", body, handled, err)
		}
	}
	// op1428 是上行推送，没有请求 id 可配对：这一条命令本身不下发任何字节，
	// 重投影发生在四条重新同步边界上（下一个用例）。
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("handleSkillSwitchInventoryCommand wrote %x, want nothing", got)
	}

	state, found, err := skillswitch.Load(context.Background(), repos, "1", skillswitch.LayoutV4)
	if err != nil || !found {
		t.Fatalf("skill switch state found=%t err=%v", found, err)
	}
	if len(state.Records) != len(skillSwitchInventoryCurrentClientBodies) {
		t.Fatalf("persisted %d records, want %d", len(state.Records), len(skillSwitchInventoryCurrentClientBodies))
	}

	log := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchInventoryObservedEvent,
		"type=1428",
		"runtime_cmd_name=ENUM_CMDPACKET_SKILL_SWITCH_INVENTORY",
		// `body_len`/`body_hex` 是**线上真正交来的切片**（现行分帧仍是 6 字节）……
		"body_len=6",
		"body_hex=00000c030c00",
		"body_layout=v4",
		"record_body_len=4",
		// ……而 `record_hex` 是去掉 2 个非包体字节之后的**真包体**。
		"record_hex=0c030c00",
		"wire_prefix=0",
		"wire_prefix_is_body=false",
		"slot=12",
		"sub_mode=3",
		"value=12",
		"value_none=false",
		"sentinel=false",
		"mode=" + skillSwitchInventoryShapeGuardTier,
		"EVENT kind=" + skillSwitchInventoryRecordedEvent,
		// 2026-09-29 修复档：不再把目录交给存在性门禁，于是走放行分支。
		"skill_gate=" + skillswitch.SkillGateCatalogAbsent,
		"skill_id=12",
		"source=current_exe_op1428",
		"record_body_layout=v4",
		"layout_upgraded=false",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("packet log missing %q; log=\n%s", want, log)
		}
	}
}

// **四条边界**：登记 → 从仓储重新读（模拟重新登录/重新选角）→ 投影 →
// 登记内容仍在，并且逐字节等于该档位的字节形。
//
// 两侧都钉：`LayoutV6` = 一键回退 = 改动前的 6 字节行（逐字节）；
// `LayoutV4`（默认）= 4 字节真包体。
func TestSkillSwitchInventorySurvivesAllFourResyncBoundaries(t *testing.T) {
	bodies := append(append([]string(nil), skillSwitchInventoryCurrentClientBodies...),
		skillSwitchInventoryLiveFourteenThirty...)

	for _, layout := range []skillswitch.Layout{skillswitch.LayoutV6, skillswitch.LayoutV4} {
		repos := newSkillSwitchInventoryRepos(t)
		writer, writerSession, _, _, _ := newSkillSwitchInventoryServiceWithLayout(t, "", layout, repos)
		for _, body := range bodies {
			if _, err := writer.handleSkillSwitchInventoryCommand(
				writerSession,
				currentSkillSwitchInventoryOpcode,
				skillSwitchInventoryBody(t, body),
			); err != nil {
				t.Fatalf("layout %s register body %s: %v", layout, body, err)
			}
		}

		// 期望的投影：按 Slot 升序、原样回放该档位的字节形。
		wantFrames := []string{
			"000003030300", "00000c030c00", "00000e030e00", "00000f030f00", "000010031000",
			"000012031200", "000013031300", "000015031500", "000016031600", "00001a031a00",
		}
		for index, want := range wantFrames {
			wantFrames[index] = skillSwitchInventoryExpectedBody(layout, want)
		}

		for _, boundary := range skillSwitchInventoryBoundaries {
			// 每条边界都是**新的 service + 新的 session**，但共用同一份仓储：
			// 这就是"重新登录/重新选角后从仓储重建"的机器可验形式。
			// 这条测试钉的是**逐帧回放机制**本身（四条边界各回放一次），所以显式选 `project` 档：
			// 新默认档 shape_guard 会因为"上行记录不是客户端 1428 入站形状"而一帧都不发，
			// 那是另一条契约，由 TestSkillSwitchInventoryShapeGuardBlocksEveryPersistedFrame 钉住。
			service, session, connection, logger, logPath := newSkillSwitchInventoryServiceWithLayout(
				t, skillSwitchInventoryProjectTier, layout, repos)
			if err := service.sendSkillSwitchInventoryProjection(session, boundary); err != nil {
				t.Fatalf("layout %s boundary %q projection error = %v", layout, boundary, err)
			}
			packets := drainUpperPackets(t, connection)
			if len(packets) != len(wantFrames) {
				t.Fatalf("layout %s boundary %q wrote %d packets, want %d",
					layout, boundary, len(packets), len(wantFrames))
			}
			for index, packet := range packets {
				if packet.msgID != currentSkillSwitchInventoryOpcode {
					t.Fatalf("layout %s boundary %q packet[%d] msg_id = %d, want %d",
						layout, boundary, index, packet.msgID, currentSkillSwitchInventoryOpcode)
				}
				if got := hex.EncodeToString(packet.body); got != wantFrames[index] {
					t.Fatalf("layout %s boundary %q packet[%d] body = %s, want %s (verbatim in that layout, ascending slot)",
						layout, boundary, index, got, wantFrames[index])
				}
			}
			log := readPeerEndpointTestLog(t, logger, logPath)
			for _, want := range []string{
				"EVENT kind=" + skillSwitchInventoryProjectionEvent,
				"source=" + boundary,
				"scope=character:1:skill_switch_inventory",
				"slot_count=10",
				"record_body_layout=" + layout.String(),
				"body_source=verbatim_replay_of_client_op1428_records",
			} {
				if !strings.Contains(log, want) {
					t.Fatalf("layout %s boundary %q log missing %q; log=\n%s", layout, boundary, want, log)
				}
			}
		}
	}
}

// 挂钩必须真的接在 game-legacy 分发链上（不是只有单元函数可用）。
func TestSkillSwitchInventoryIsWiredIntoLegacyDispatch(t *testing.T) {
	repos := newSkillSwitchInventoryRepos(t)
	service, session, connection, logger, logPath := newSkillSwitchInventoryService(t, "", repos)
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		currentSkillSwitchInventoryOpcode,
		skillSwitchInventoryBody(t, skillSwitchInventorySentinelBody),
	); err != nil {
		t.Fatalf("handleGameCommand error = %v", err)
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("op1428 must not write inline, wrote %x", got)
	}
	log := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(log, "EVENT kind="+skillSwitchInventoryObservedEvent) {
		t.Fatalf("dispatch chain did not reach the named owner; log=\n%s", log)
	}
	if !strings.Contains(log, "value_none=true") || !strings.Contains(log, "sentinel=true") {
		t.Fatalf("sentinel must be decoded as value_none/sentinel; log=\n%s", log)
	}
	if !strings.Contains(log, "wire_prefix=38") || !strings.Contains(log, "record_hex=212cffff") {
		t.Fatalf("the 2 wire-prefix bytes must be reported separately from the 4-byte body; log=\n%s", log)
	}
}

// 登录/选择角色边界走的是既有的技能信息投影函数：op19 之后必须跟着 op1428 回放。
// 两种档位都跑：v4（默认）4 字节真包体、v6（回退）改动前的 6 字节行。
func TestSkillSwitchInventoryProjectsInsideSceneSkillInfoSender(t *testing.T) {
	for _, layout := range []skillswitch.Layout{skillswitch.LayoutV4, skillswitch.LayoutV6} {
		repos := newSkillSwitchInventoryRepos(t)
		writer, writerSession, _, _, _ := newSkillSwitchInventoryServiceWithLayout(t, "", layout, repos)
		for _, body := range skillSwitchInventoryCurrentClientBodies {
			if _, err := writer.handleSkillSwitchInventoryCommand(
				writerSession,
				currentSkillSwitchInventoryOpcode,
				skillSwitchInventoryBody(t, body),
			); err != nil {
				t.Fatalf("layout %s register %s: %v", layout, body, err)
			}
		}

		catalog := skillSwitchInventoryCatalog(t)
		// 与 scene_skill_info_test.go 同一套最小装配：目录/初始技能表/技能仓储齐备。
		service := &Service{
			repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			initialSkillsByJob: map[byte][]initialSkillEntry{14: {{SkillID: 12, Level: 1}}},
			initialSPTable:     map[int]int{1: 20},
			skillCatalog:       catalog,
			options: options{
				accountID:                            "dnf:10001",
				gameUpperHeader:                      gameUpperHeaderChannel13,
				gameUpperBodyCodec:                   gameUpperBodyCodecPlain,
				skillSwitchInventoryMode:             skillSwitchInventoryProjectTier,
				skillSwitchInventoryRecordBodyLayout: layout,
			},
		}
		connection := &bufferConn{}
		session := &gameSession{
			conn:                connection,
			connID:              "skill-switch-inventory-scene",
			channel:             channelcatalog.Channel{ID: 10},
			selectedCharacterID: 1,
			accountID:           "dnf:10001",
		}
		character, found, err := repos.Character.Load(context.Background(), "1")
		if err != nil || !found {
			t.Fatalf("layout %s character found=%t err=%v", layout, found, err)
		}
		if err := service.sendCurrentSceneSkillInfo(session, context.Background(), character, "test_login"); err != nil {
			t.Fatalf("layout %s sendCurrentSceneSkillInfo error = %v", layout, err)
		}
		packets := drainUpperPackets(t, connection)
		if len(packets) < 2 {
			t.Fatalf("layout %s wrote %d packets, want the op19 skill info followed by op1428 replays",
				layout, len(packets))
		}
		if packets[0].msgID != currentSkillInfoMsgID {
			t.Fatalf("layout %s packet[0] msg_id = %d, want op19 %d",
				layout, packets[0].msgID, currentSkillInfoMsgID)
		}
		wantFrames := []string{"00000c030c00", "000013031300", "000015031500", "000016031600"}
		for index, want := range wantFrames {
			wantFrames[index] = skillSwitchInventoryExpectedBody(layout, want)
		}
		if len(packets)-1 != len(wantFrames) {
			t.Fatalf("layout %s wrote %d op1428 packets, want %d", layout, len(packets)-1, len(wantFrames))
		}
		for index, want := range wantFrames {
			packet := packets[index+1]
			if packet.msgID != currentSkillSwitchInventoryOpcode {
				t.Fatalf("layout %s packet[%d] msg_id = %d, want %d",
					layout, index+1, packet.msgID, currentSkillSwitchInventoryOpcode)
			}
			if got := hex.EncodeToString(packet.body); got != want {
				t.Fatalf("layout %s packet[%d] body = %s, want %s", layout, index+1, got, want)
			}
		}
	}
}

// 旧档 `reference_drop`：与改动前逐字节相同 —— 不下发、不落库，只留具名事件。
func TestSkillSwitchInventoryReferenceDropTierWritesNothing(t *testing.T) {
	repos := newSkillSwitchInventoryRepos(t)
	service, session, connection, logger, logPath := newSkillSwitchInventoryService(
		t, skillSwitchInventoryReferenceDrop, repos)
	handled, err := service.handleSkillSwitchInventoryCommand(
		session,
		currentSkillSwitchInventoryOpcode,
		skillSwitchInventoryBody(t, skillSwitchInventoryCurrentClientBodies[0]),
	)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("reference_drop must not write a packet, wrote %x", got)
	}
	if err := service.sendSkillSwitchInventoryProjection(session, "reference_drop_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("reference_drop must not project, wrote %x", got)
	}
	if _, found, err := repos.Settings.Load(context.Background(), skillswitch.Scope("1")); err != nil || found {
		t.Fatalf("reference_drop must not persist (found=%t err=%v)", found, err)
	}
	log := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(log, "mode=reference_drop") {
		t.Fatalf("reference_drop must still log the named observation; log=\n%s", log)
	}
}

// 取证档 `record`：落库，但一个字节都不发。
func TestSkillSwitchInventoryRecordOnlyTierPersistsWithoutSending(t *testing.T) {
	repos := newSkillSwitchInventoryRepos(t)
	service, session, connection, _, _ := newSkillSwitchInventoryService(
		t, skillSwitchInventoryRecordTier, repos)
	if _, err := service.handleSkillSwitchInventoryCommand(
		session,
		currentSkillSwitchInventoryOpcode,
		skillSwitchInventoryBody(t, skillSwitchInventoryCurrentClientBodies[0]),
	); err != nil {
		t.Fatalf("handle error = %v", err)
	}
	if err := service.sendSkillSwitchInventoryProjection(session, "record_only_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("record tier must not write a packet, wrote %x", got)
	}
	state, found, err := skillswitch.Load(context.Background(), repos, "1", skillswitch.LayoutV4)
	if err != nil || !found || len(state.Records) != 1 {
		t.Fatalf("record tier must persist one record (found=%t err=%v state=%+v)", found, err, state)
	}
}

// 数据规则（**回退档**）：引用了 job-scoped PVF 里不存在的技能号 ⇒ 具名拒绝、不落库。
//
// 2026-09-29：这条规则现在是 `DNFBRIDGE_SKILL_SWITCH_SKILL_GATE=job_scoped_pvf`
// 的回退档行为，不再是默认 —— 它曾把操作者的正常登记（杀意波动上行 26，而 PVF 里
// 它是 job 0 的 52）判成未知技能号而整条丢弃。默认档见紧随其后的
// TestSkillSwitchInventoryDefaultTierPersistsUnknownSkillID。
func TestSkillSwitchInventoryRejectsUnknownSkillID(t *testing.T) {
	t.Setenv("DNFBRIDGE_SKILL_SWITCH_SKILL_GATE", "job_scoped_pvf")
	repos := newSkillSwitchInventoryRepos(t)
	service, session, connection, logger, logPath := newSkillSwitchInventoryService(t, "", repos)
	// job 14 的目录里只有 3/12/14/15/16/18/19/21/22/26；skill 4 不存在。
	unknown := []byte{0x00, 0x00, 0x04, 0x03, 0x04, 0x00}
	handled, err := service.handleSkillSwitchInventoryCommand(session, currentSkillSwitchInventoryOpcode, unknown)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("rejected record must not write a packet, wrote %x", got)
	}
	if _, found, err := repos.Settings.Load(context.Background(), skillswitch.Scope("1")); err != nil || found {
		t.Fatalf("rejected record must not persist (found=%t err=%v)", found, err)
	}
	log := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchInventoryRejectedEvent,
		// 交来的切片仍是 6 字节（现行分帧），真包体是去掉前缀后的 4 字节。
		"body_hex=000004030400",
		"record_hex=04030400",
		"body_layout=v4",
		"slot=4",
		"reason=" + skillswitch.ErrSkillUnknown.Error(),
		"job=14",
		"skill=4",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("rejection log missing %q; log=\n%s", want, log)
		}
	}
}

// 数据规则（**默认 = 修复档**，2026-09-29）：同样的未知技能号必须**照常落库**。
//
// 修复点：客户端上行的 op1428 `slot` 并不是 job-scoped PVF 技能号，服务端却拿它
// 去技能表里查，于是把操作者的正常登记整条丢弃。真机证据（只读）：
//   - 上行 `record_hex=1a031a00` ⇒ slot=26，出现在「Buff 强化」里选中杀意波动时；
//   - 而真实 PVF 里 job 0 的杀意波动是 **id 52**（另有 id 150「强化 - 杀意波动」），
//     job 0 的技能号序列是 `1..25,27,29,30,…` —— **26 在 job 0 不存在**；
//   - 于是每次登录都 `ErrSkillUnknown` 具名拒绝（4 次登录拒 4 次）⇒ 从未落库
//     ⇒ 选择角色后重发里没有 26 ⇒ 操作者看到的面板是空的。
//
// 服务端不需要理解那个数字，它只需原样存下并原样补发，所以默认档不再提供目录。
func TestSkillSwitchInventoryDefaultTierPersistsUnknownSkillID(t *testing.T) {
	repos := newSkillSwitchInventoryRepos(t)
	service, session, _, logger, logPath := newSkillSwitchInventoryService(t, "", repos)
	// job 14 的目录里只有 3/12/14/15/16/18/19/21/22/26；skill 4 不存在。
	unknown := []byte{0x00, 0x00, 0x04, 0x03, 0x04, 0x00}
	handled, err := service.handleSkillSwitchInventoryCommand(session, currentSkillSwitchInventoryOpcode, unknown)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if _, found, err := repos.Settings.Load(context.Background(), skillswitch.Scope("1")); err != nil || !found {
		t.Fatalf("default tier must persist the record (found=%t err=%v)", found, err)
	}
	log := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchInventoryRecordedEvent,
		"record_hex=04030400",
		"slot=4",
		"skill_gate=" + skillswitch.SkillGateCatalogAbsent,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("default-tier log missing %q; log=\n%s", want, log)
		}
	}
}

// 形状不符也必须具名、带原因、零字节、不写库。**两侧都钉**：
//
//	长度 5（既非 4 也非 6）在两种档位下都必须被拒；
//	长度 4（v4 的规范包体）在 v6 回退档下必须被拒 —— 回退档的严格性与改动前一致。
func TestSkillSwitchInventoryUnexpectedBodyLenIsNamedAndWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		layout skillswitch.Layout
		body   []byte
	}{
		{name: "v4 default len 5", mode: "", layout: skillswitch.LayoutV4, body: []byte{0x00, 0x00, 0x0e, 0x03, 0x0e}},
		{name: "v4 record tier len 5", mode: skillSwitchInventoryRecordTier, layout: skillswitch.LayoutV4, body: []byte{0x00, 0x00, 0x0e, 0x03, 0x0e}},
		{name: "v6 rollback len 5", mode: "", layout: skillswitch.LayoutV6, body: []byte{0x00, 0x00, 0x0e, 0x03, 0x0e}},
		{name: "v6 rollback rejects the 4-byte body", mode: "", layout: skillswitch.LayoutV6, body: []byte{0x0c, 0x03, 0x0c, 0x00}},
	}
	for _, test := range cases {
		repos := newSkillSwitchInventoryRepos(t)
		service, session, connection, logger, logPath := newSkillSwitchInventoryServiceWithLayout(
			t, test.mode, test.layout, repos)
		handled, err := service.handleSkillSwitchInventoryCommand(
			session,
			currentSkillSwitchInventoryOpcode,
			test.body,
		)
		if err != nil || !handled {
			t.Fatalf("%s: handled=%t err=%v", test.name, handled, err)
		}
		if got := connection.write.Bytes(); len(got) != 0 {
			t.Fatalf("%s: wrote %x, want nothing", test.name, got)
		}
		if _, found, err := repos.Settings.Load(context.Background(), skillswitch.Scope("1")); err != nil || found {
			t.Fatalf("%s: wrote a settings row for a malformed body (found=%t err=%v)", test.name, found, err)
		}
		log := readPeerEndpointTestLog(t, logger, logPath)
		for _, want := range []string{
			"reason=unexpected_body_len",
			"body_layout=" + test.layout.String(),
			"body_hex=" + hex.EncodeToString(test.body),
		} {
			if !strings.Contains(log, want) {
				t.Fatalf("%s: missing %q; log=\n%s", test.name, want, log)
			}
		}
	}
}

// 别的 opcode 一个都不能被这条 owner 截走。
func TestSkillSwitchInventoryIgnoresOtherOpcodes(t *testing.T) {
	repos := newSkillSwitchInventoryRepos(t)
	service, session, _, _, _ := newSkillSwitchInventoryService(t, "", repos)
	for _, typ := range []uint16{
		uint16(dnfenum.CmdPacketMoveItemspace),
		uint16(dnfenum.CmdPacketRequestCardPick),
		uint16(dnfenum.CmdPacketClearQuestTicket),
	} {
		handled, err := service.handleSkillSwitchInventoryCommand(
			session, typ, skillSwitchInventoryBody(t, skillSwitchInventorySentinelBody))
		if err != nil || handled {
			t.Fatalf("type %d handled=%t err=%v, want untouched", typ, handled, err)
		}
	}
}

// TestSkillSwitchInventoryFrameShapeGuard 钉住形状闸门的判定：客户端 1428 入站解析器读的是
// `u8 count` + `count × (u32 key, u32 value)`，所以一帧合法当且仅当长度恰好是 1+8*count。
//
// 反证口径：表里同时有 true/false 两侧；把闸门反过来写（例如只判 len>0）会让 false 侧立刻失败。
// 我们自己的上行记录（v4 的 4 字节包体、v6 的 6 字节行）必须**全部**落在 false 侧 ——
// 这正是"投影填不上 buff 栏、还会把客户端入站流读乱"的那批字节。
func TestSkillSwitchInventoryFrameShapeGuard(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
		want  bool
	}{
		{name: "nil", frame: nil, want: false},
		{name: "empty", frame: []byte{}, want: false},
		{name: "our 6-byte uplink record starting with 0x26", frame: []byte{0x26, 0x00, 0x00, 0x00, 0x00, 0x00}, want: false},
		{name: "our 6-byte uplink record starting with 0x00", frame: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, want: false},
		{name: "our v4 4-byte body mode confirm", frame: []byte{0x0c, 0x03, 0x0c, 0x00}, want: false},
		{name: "our v4 4-byte body slot 16", frame: []byte{0x10, 0x03, 0x10, 0x00}, want: false},
		{name: "our v4 sentinel body", frame: []byte{0x21, 0x2c, 0xff, 0xff}, want: false},
		{name: "count 0 exact", frame: []byte{0x00}, want: true},
		{name: "count 0 with trailing byte", frame: []byte{0x00, 0x01}, want: false},
		{name: "count 1 exact", frame: append([]byte{0x01}, make([]byte, 8)...), want: true},
		{name: "count 1 short by one", frame: append([]byte{0x01}, make([]byte, 7)...), want: false},
		{name: "count 1 long by one", frame: append([]byte{0x01}, make([]byte, 9)...), want: false},
		{name: "count 12 exact (12 slots)", frame: append([]byte{12}, make([]byte, 96)...), want: true},
		{name: "count 38 truncated to 5 bytes (what 0x26 would demand)", frame: []byte{38, 0, 0, 0, 0}, want: false},
	}
	// 候选对实验帧（identity12，97 字节）必须是 true 侧 —— 它是我们唯一合法形状的下行帧。
	candidate, err := buildSkillSwitchCandidatePairsBody(skillSwitchCandidatePairSets[skillSwitchCandidatePairsIdentity12])
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate) != 1+8*12 {
		t.Fatalf("identity12 candidate frame len = %d, want %d", len(candidate), 1+8*12)
	}
	cases = append(cases, struct {
		name  string
		frame []byte
		want  bool
	}{name: "identity12 candidate frame", frame: candidate, want: true})

	for _, c := range cases {
		if got := skillSwitchInventoryFrameShapeValid(c.frame); got != c.want {
			t.Fatalf("%s: valid(%x) = %v, want %v", c.name, c.frame, got, c.want)
		}
	}
}

// TestSkillSwitchInventoryShapeGuardBlocksEveryPersistedFrame 是 shape_guard 档里
// **回放路径**的契约：落库的帧全部来自客户端上行记录（v4 = 4 字节包体，
// v6 = 6 字节行），没有一帧满足 `len == 1+8*count` ⇒ 回放路径**一个字节都不发**，
// 只记一条 projection-blocked。**两种档位都钉**（换档不会意外放开下行）。
//
// ⚠️ 2026-09-28 起，默认档还会走**另一条**路径发一帧合法状态帧
// （`DNFBRIDGE_SKILL_SWITCH_RESEND`，把落库换装行按客户端入站形状重发，
// 见 skill_switch_inventory_resend.go）。本用例钉的是"上行记录永远过不了
// 下行形状闸门"这一条，所以把那条产品帧按回退字面量关掉；
// "默认档恰好发一帧合法帧、且这 14 行仍然全部被拦"由
// TestSkillSwitchLoadoutResendEmitsOnCharacterSelectBoundary 钉住。
//
// 反证口径：把闸门去掉（回到 project 档）这条测试立刻失败 —— 同一份仓储、同一个边界，
// project 档会写出 len(frames) 个包（由 TestSkillSwitchInventorySurvivesAllFourResyncBoundaries 钉住）。
func TestSkillSwitchInventoryShapeGuardBlocksEveryPersistedFrame(t *testing.T) {
	t.Setenv(skillSwitchLoadoutResendEnv, skillSwitchLoadoutResendShapeGuard)
	for _, layout := range []skillswitch.Layout{skillswitch.LayoutV4, skillswitch.LayoutV6} {
		repos := newSkillSwitchInventoryRepos(t)
		writer, writerSession, _, _, _ := newSkillSwitchInventoryServiceWithLayout(
			t, skillSwitchInventoryProjectTier, layout, repos)
		for _, body := range skillSwitchInventoryCurrentClientBodies {
			if _, err := writer.handleSkillSwitchInventoryCommand(
				writerSession,
				currentSkillSwitchInventoryOpcode,
				skillSwitchInventoryBody(t, body),
			); err != nil {
				t.Fatalf("layout %s register body %s: %v", layout, body, err)
			}
		}

		service, session, connection, logger, logPath := newSkillSwitchInventoryServiceWithLayout(t, "", layout, repos)
		if got := service.skillSwitchInventoryMode(); got != skillSwitchInventoryShapeGuardTier {
			t.Fatalf("layout %s default mode = %q, want %q", layout, got, skillSwitchInventoryShapeGuardTier)
		}
		if err := service.sendSkillSwitchInventoryProjection(session, "shape_guard_boundary"); err != nil {
			t.Fatalf("layout %s shape_guard projection error = %v", layout, err)
		}
		if packets := drainUpperPackets(t, connection); len(packets) != 0 {
			t.Fatalf("layout %s shape_guard wrote %d packets, want 0 (uplink records never match 1+8*count)",
				layout, len(packets))
		}
		log := readPeerEndpointTestLog(t, logger, logPath)
		if !strings.Contains(log, skillSwitchInventoryProjectionBlockedEvent) {
			t.Fatalf("layout %s shape_guard must log %s; log:\n%s",
				layout, skillSwitchInventoryProjectionBlockedEvent, log)
		}
		wantBlockedBodyLen := skillswitch.RecordBodySize
		if layout == skillswitch.LayoutV6 {
			wantBlockedBodyLen = skillswitch.RecordWireSize
		}
		for _, want := range []string{
			"reason=body_shape_not_client_s2c_map",
			"record_body_layout=" + layout.String(),
			"blocked_body_len=" + strconv.Itoa(wantBlockedBodyLen),
		} {
			if !strings.Contains(log, want) {
				t.Fatalf("layout %s blocked event missing %q; log:\n%s", layout, want, log)
			}
		}
	}
}

func TestSkillSwitchInventoryModeNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: skillSwitchInventoryShapeGuardTier},
		{value: "project", want: skillSwitchInventoryProjectTier},
		{value: "verbatim", want: skillSwitchInventoryProjectTier},
		{value: "  LEGACY_PROJECT  ", want: skillSwitchInventoryProjectTier},
		{value: "on", want: skillSwitchInventoryShapeGuardTier},
		{value: "fix", want: skillSwitchInventoryShapeGuardTier},
		{value: "  SHAPE_GUARD  ", want: skillSwitchInventoryShapeGuardTier},
		{value: "typo", want: skillSwitchInventoryShapeGuardTier},
		{value: "before_transition_only", want: skillSwitchInventoryBeforeTransitionTier},
		{value: "early_only", want: skillSwitchInventoryBeforeTransitionTier},
		{value: "project_before_transition", want: skillSwitchInventoryBeforeTransitionTier},
		{value: "no_scene_ready_projection", want: skillSwitchInventoryBeforeTransitionTier},
		{value: "  EARLY_ONLY  ", want: skillSwitchInventoryBeforeTransitionTier},
		{value: "record", want: skillSwitchInventoryRecordTier},
		{value: "persist_only", want: skillSwitchInventoryRecordTier},
		{value: "reference_drop", want: skillSwitchInventoryReferenceDrop},
		{value: "off", want: skillSwitchInventoryReferenceDrop},
		{value: "0", want: skillSwitchInventoryReferenceDrop},
		{value: "disable", want: skillSwitchInventoryReferenceDrop},
		{value: "legacy", want: skillSwitchInventoryReferenceDrop},
		{value: "  REFERENCE_DROP  ", want: skillSwitchInventoryReferenceDrop},
	} {
		if got := normalizeSkillSwitchInventoryMode(test.value); got != test.want {
			t.Fatalf("normalizeSkillSwitchInventoryMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
	// options 零值必须落到**修复档**：这是"默认档就是修复档"的机器可验形式。
	// 修复档现在是 shape_guard（先与客户端 1428 入站解析器对齐形状，再决定发不发）。
	zero := &Service{}
	if got := zero.skillSwitchInventoryMode(); got != skillSwitchInventoryShapeGuardTier {
		t.Fatalf("zero-value options mode = %q, want %q", got, skillSwitchInventoryShapeGuardTier)
	}
	if !zero.skillSwitchInventoryProjects() {
		t.Fatal("zero-value options must project")
	}
	if !zero.skillSwitchInventoryProjectsAfterSceneReady() {
		t.Fatal("zero-value options must project after the legacy type1345 scene-ready ack")
	}
	if (&Service{options: options{skillSwitchInventoryMode: skillSwitchInventoryReferenceDrop}}).skillSwitchInventoryProjects() {
		t.Fatal("reference_drop must not project")
	}
	if (&Service{options: options{skillSwitchInventoryMode: skillSwitchInventoryRecordTier}}).skillSwitchInventoryProjects() {
		t.Fatal("record tier must not project")
	}
	// before_transition_only 是**只少一条**的回退档：四条老边界照发，
	// 只有 type1345 之后那条不发。
	beforeTransition := &Service{options: options{
		skillSwitchInventoryMode: skillSwitchInventoryBeforeTransitionTier,
	}}
	if !beforeTransition.skillSwitchInventoryProjects() {
		t.Fatal("before_transition_only must still project on the four legacy boundaries")
	}
	if beforeTransition.skillSwitchInventoryProjectsAfterSceneReady() {
		t.Fatal("before_transition_only must not project after the legacy type1345 scene-ready ack")
	}

	// op1428 **上行读法**：零值 = v4（修复档，真实包体 4 字节），
	// 只有显式 v6 才回退；未知取值一律回落修复档。
	if got := zero.skillSwitchInventoryRecordBodyLayout(); got != skillswitch.LayoutV4 {
		t.Fatalf("zero-value record body layout = %q, want %q", got, skillswitch.LayoutV4)
	}
	for _, test := range []struct {
		value string
		want  skillswitch.Layout
	}{
		{value: "", want: skillswitch.LayoutV4},
		{value: "v4", want: skillswitch.LayoutV4},
		{value: "fix", want: skillswitch.LayoutV4},
		{value: "typo", want: skillswitch.LayoutV4},
		{value: "v6", want: skillswitch.LayoutV6},
		{value: "legacy", want: skillswitch.LayoutV6},
		{value: "  TODAY  ", want: skillswitch.LayoutV6},
	} {
		service := &Service{options: options{
			skillSwitchInventoryRecordBodyLayout: skillswitch.NormalizeLayout(test.value),
		}}
		if got := service.skillSwitchInventoryRecordBodyLayout(); got != test.want {
			t.Fatalf("record body layout for %q = %q, want %q", test.value, got, test.want)
		}
	}

	// 候选对实验：零值 / off / 未知取值 = 关；只有候选表里的名字才算开。
	if got := zero.skillSwitchCandidatePairsMode(); got != skillSwitchCandidatePairsOff {
		t.Fatalf("zero-value candidate pairs mode = %q, want %q", got, skillSwitchCandidatePairsOff)
	}
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: skillSwitchCandidatePairsOff},
		{value: "off", want: skillSwitchCandidatePairsOff},
		{value: "OFF", want: skillSwitchCandidatePairsOff},
		{value: "identity12", want: skillSwitchCandidatePairsIdentity12},
		{value: "  IDENTITY12  ", want: skillSwitchCandidatePairsIdentity12},
		{value: "identity13", want: skillSwitchCandidatePairsOff},
		{value: "family2", want: skillSwitchCandidatePairsOff},
		{value: "on", want: skillSwitchCandidatePairsOff},
	} {
		if got := normalizeSkillSwitchCandidatePairsMode(test.value); got != test.want {
			t.Fatalf("normalizeSkillSwitchCandidatePairsMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

// **第五条边界**（2026-09-28，操作者「4buff依旧失效 选择角色后清空」）：
// 客户端自己的 legacy type1345「场景就绪」ack 之后必须再投影一次。
//
// 现场判据（HK `/opt/dnf90/logs/packet_log.txt`）：
//
//	:24865 game-skill-switch-inventory-projection-send … char_id=6 slot_count=11
//	      source=initial_town_before_final_transition_after_mode1_without_mode3_before_typed_op24…
//	:24938 game-legacy-town-scene-ready-ack-accepted conn_id=game-000018 … type=1345
//	:24941 game-upper-select-character-scene-tail-send … source=legacy_type1345_scene_ready_ack
//
// 即：同一个「重选角色」上，客户端在 :24938 才宣布场景就绪，而换装栏只在
// :24865（typed op24 之前）投影过一次；op37/op30 两条边界在重选角色时不会发生。
func TestSkillSwitchInventoryProjectsAfterLegacySceneReadyAck(t *testing.T) {
	for _, layout := range []skillswitch.Layout{skillswitch.LayoutV4, skillswitch.LayoutV6} {
		repos := newSkillSwitchInventoryRepos(t)
		writer, writerSession, _, _, _ := newSkillSwitchInventoryServiceWithLayout(t, "", layout, repos)
		for _, body := range skillSwitchInventoryCurrentClientBodies {
			if _, err := writer.handleSkillSwitchInventoryCommand(
				writerSession,
				currentSkillSwitchInventoryOpcode,
				skillSwitchInventoryBody(t, body),
			); err != nil {
				t.Fatalf("layout %s register body %s: %v", layout, body, err)
			}
		}

		// 新的 service + 新的 session、共用同一份仓储 = "重选角色后从仓储重建"。
		// 显式选 `project`：这条测试钉的是 type1345 之后**补第二条回放**的边界语义，
		// 与形状闸门是两件事（默认档下这条边界同样会被闸门拦成 0 字节）。
		service, session, connection, logger, logPath := newSkillSwitchInventoryServiceWithLayout(
			t, skillSwitchInventoryProjectTier, layout, repos)
		armSceneReadyAckBoundary(session)

		if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
			t.Fatalf("layout %s legacy type1345 boundary: %v", layout, err)
		}

		packets := drainUpperPackets(t, connection)
		if len(packets) != len(skillSwitchInventoryCurrentClientBodies) {
			t.Fatalf("layout %s type1345 boundary wrote %d packets, want %d (the switch-inventory replay)",
				layout, len(packets), len(skillSwitchInventoryCurrentClientBodies))
		}
		for index, packet := range packets {
			if packet.msgID != currentSkillSwitchInventoryOpcode {
				t.Fatalf("layout %s type1345 packet[%d] msg_id = %d, want %d",
					layout, index, packet.msgID, currentSkillSwitchInventoryOpcode)
			}
			want := skillSwitchInventoryExpectedBody(layout, skillSwitchInventoryCurrentClientBodies[index])
			if got := hex.EncodeToString(packet.body); got != want {
				t.Fatalf("layout %s type1345 packet[%d] body = %s, want %s (verbatim in that layout, ascending slot)",
					layout, index, got, want)
			}
		}
		log := readPeerEndpointTestLog(t, logger, logPath)
		for _, want := range []string{
			"EVENT kind=" + skillSwitchInventoryProjectionEvent,
			"source=legacy_type1345_scene_ready_ack_skill_switch_inventory_after_scene_ready",
			"scope=character:1:skill_switch_inventory",
			"slot_count=4",
			"record_body_layout=" + layout.String(),
		} {
			if !strings.Contains(log, want) {
				t.Fatalf("layout %s type1345 boundary log missing %q; log=\n%s", layout, want, log)
			}
		}

		// 同一次进城里的**重复** ack 不许再补一次：这是"一个包不许多发"的钉子。
		repeatBefore := connection.write.Len()
		if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
			t.Fatalf("layout %s repeated legacy type1345 boundary: %v", layout, err)
		}
		if got := connection.write.Bytes(); len(got) != repeatBefore {
			t.Fatalf("layout %s repeated type1345 grew the stream from %d to %d bytes: %x",
				layout, repeatBefore, len(got), got)
		}
	}
}

// 回退档 `before_transition_only`：type1345 之后**一个换装栏字节都不发**，
// 但四条老边界照发（见 TestSkillSwitchInventorySurvivesAllFourResyncBoundaries
// 与 ModeNormalization 的同档断言）。
func TestSkillSwitchInventoryBeforeTransitionTierSkipsSceneReadyReplay(t *testing.T) {
	repos := newSkillSwitchInventoryRepos(t)
	writer, writerSession, _, _, _ := newSkillSwitchInventoryService(t, "", repos)
	for _, body := range skillSwitchInventoryCurrentClientBodies {
		if _, err := writer.handleSkillSwitchInventoryCommand(
			writerSession,
			currentSkillSwitchInventoryOpcode,
			skillSwitchInventoryBody(t, body),
		); err != nil {
			t.Fatalf("register body %s: %v", body, err)
		}
	}

	service, session, connection, _, _ := newSkillSwitchInventoryService(
		t,
		skillSwitchInventoryBeforeTransitionTier,
		repos,
	)
	armSceneReadyAckBoundary(session)

	if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatalf("legacy type1345 boundary: %v", err)
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("before_transition_only wrote %d bytes at the type1345 boundary: %x", len(got), got)
	}
}

// armSceneReadyAckBoundary 把 session 摆到「选择角色 / 进城已完成 typed op24，
// 客户端刚发来 legacy type1345 场景就绪 ack」那一刻，并让那条边界上除换装栏
// 之外的投影全部短路（它们各自有专门的用例）。
func armSceneReadyAckBoundary(session *gameSession) {
	session.initialTownRouteCharacterID = session.selectedCharacterID
	session.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	session.initialTownLegacySceneReadyAccepted = false
	session.sceneBootstrapTailSent = true
	session.sceneBootstrapTailDeferred = false
	session.initialTownAdventureOverheadRefreshSent = true
	session.initialTownCombatPowerAffixesSent = true
}
