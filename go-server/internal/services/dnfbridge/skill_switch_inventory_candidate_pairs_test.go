package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// op1428 **下行候选对实验**（`DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS`）的钉子。
//
// 证据（`_dist/buff-pairs-measurement.md` §6/§7）：
//
//   - 客户端 1428 入站解析器形状**已实测**：`u8 count + count×(u32 key,u32 value)`，
//     body 长度必须恰好 `1+8*count`（DNF.exe RVA 0x565660，活进程逐条核对）；
//   - 但 `(key,value)` 填什么**没有拿到**：恒等 12 对只是"结构上最像"的候选
//     （实机里真有 12 项 `std::map<u32,u32>`，key==value，项数与 slot_count=12 吻合），
//     **未被证明**就是 manager 329 的 map；
//   - family-2「slot→源槽位」`(12,54)(14,62)…` 已被 1.39 GB 全内存扫描**证伪**（0 命中）。
//
// 因此本实验**默认关**，且必须：开时才发**恰好一帧**、形状**按构造**合法、
// 关时逐字节等于今天。

// skillSwitchCandidateIdentity12BodyHex 是 count=12 + 12 组恒等对的**完整包体 hex**
// （u8 count=0x0c，随后每组 `u32 LE key, u32 LE value`）——手写常量，不引用实现，
// 这样构造代码写错时测试会逐字节红灯。
const skillSwitchCandidateIdentity12BodyHex = "0c" +
	"0c0000000c000000" + "0e0000000e000000" + "0f0000000f000000" + "1000000010000000" +
	"1100000011000000" + "1200000012000000" + "1300000013000000" + "1500000015000000" +
	"1600000016000000" + "1700000017000000" + "1900000019000000" + "1a0000001a000000"

// TestSkillSwitchCandidatePairsIdentity12Frame 钉住实验帧的**逐字节**形状与那次唯一的发送：
// 一帧、msg_id=1428、classification=0、body = count + 12 组恒等对、且通过既有形状闸门。
//
// ⚠️ 2026-09-28 起**产品默认**还会在同一条边界上多发一帧"落库换装重下发"
// （`DNFBRIDGE_SKILL_SWITCH_RESEND`，见 skill_switch_inventory_resend.go）。
// 本用例要钉的是**实验帧自己**，所以把那条产品帧按它自己的回退字面量关掉；
// 两者同时在场的顺序由 TestSkillSwitchLoadoutResendAfterCandidateFrameWinsTheMap 钉住。
func TestSkillSwitchCandidatePairsIdentity12Frame(t *testing.T) {
	t.Setenv(skillSwitchLoadoutResendEnv, skillSwitchLoadoutResendShapeGuard)
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

	// shape_guard（默认闸门）+ identity12（实验）：这条边界上**只**应出现实验帧。
	service, session, connection, logger, logPath := newSkillSwitchInventoryService(t, "", repos)
	service.options.skillSwitchCandidatePairsMode = skillSwitchCandidatePairsIdentity12
	if err := service.sendSkillSwitchInventoryProjection(session, "candidate_pairs_boundary"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, connection)
	if len(packets) != 1 {
		t.Fatalf("identity12 wrote %d packets, want exactly 1 (the candidate frame)", len(packets))
	}
	packet := packets[0]
	if packet.msgID != currentSkillSwitchInventoryOpcode {
		t.Fatalf("candidate packet msg_id = %d, want %d", packet.msgID, currentSkillSwitchInventoryOpcode)
	}
	if got := hex.EncodeToString(packet.body); got != skillSwitchCandidateIdentity12BodyHex {
		t.Fatalf("candidate body = %s\nwant           %s", got, skillSwitchCandidateIdentity12BodyHex)
	}
	if len(packet.body) != 1+8*12 {
		t.Fatalf("candidate body len = %d, want %d (u8 count + count×(u32,u32))", len(packet.body), 1+8*12)
	}
	if packet.body[0] != 12 {
		t.Fatalf("candidate count byte = %d, want 12", packet.body[0])
	}
	if !skillSwitchInventoryFrameShapeValid(packet.body) {
		t.Fatal("the candidate frame must pass the shipped shape guard by construction")
	}
	// 逐对反解，钉住那 12 组恒等对（含"不含 family-2 的 54/62/…"这条永不再犯的边界）。
	wantPairs := []struct{ key, value uint32 }{
		{12, 12}, {14, 14}, {15, 15}, {16, 16}, {17, 17}, {18, 18},
		{19, 19}, {21, 21}, {22, 22}, {23, 23}, {25, 25}, {26, 26},
	}
	for index, want := range wantPairs {
		offset := 1 + 8*index
		key := binary.LittleEndian.Uint32(packet.body[offset : offset+4])
		value := binary.LittleEndian.Uint32(packet.body[offset+4 : offset+8])
		if key != want.key || value != want.value {
			t.Fatalf("pair[%d] = (%d,%d), want (%d,%d)", index, key, value, want.key, want.value)
		}
		if value == 54 || value == 62 || value == 61 {
			t.Fatalf("pair[%d] carries a falsified family-2 value %d; those must never be sent", index, value)
		}
	}

	log := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchCandidatePairsSendEvent,
		"candidate=identity12",
		"pair_count=12",
		"body_len=97",
		"shape_guard=pass",
		"msg_id=1428",
		"classification=0",
		"source=candidate_pairs_boundary",
		"scope=character:1:skill_switch_inventory",
		"proof_state=unproven_candidate",
		"downlink_count_semantics=unverified",
		// 线上判读就靠这一行里的完整 hex。
		"body_hex=" + skillSwitchCandidateIdentity12BodyHex,
		"pairs=(12,12)(14,14)(15,15)(16,16)(17,17)(18,18)(19,19)(21,21)(22,22)(23,23)(25,25)(26,26)",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("candidate send log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchCandidatePairsDefaultOffSendsNothing 是**实验默认档**的契约：
// 不开实验 ⇒ 一个实验帧都不发，而且整条上行/下行字节流与实验出现之前逐字节相同
// （回放帧被形状闸门拦成 0 字节；产品侧的"落库换装重下发"按它自己的回退字面量关掉，
// 那条产品契约由 TestSkillSwitchLoadoutResend* 单独钉）。
func TestSkillSwitchCandidatePairsDefaultOffSendsNothing(t *testing.T) {
	t.Setenv(skillSwitchLoadoutResendEnv, skillSwitchLoadoutResendShapeGuard)
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

	for _, mode := range []string{"", skillSwitchCandidatePairsOff, "OFF", "identity13", "typo"} {
		service, session, connection, logger, logPath := newSkillSwitchInventoryService(t, "", repos)
		service.options.skillSwitchCandidatePairsMode = mode
		if got := service.skillSwitchCandidatePairsMode(); got != skillSwitchCandidatePairsOff {
			t.Fatalf("mode %q normalized to %q, want %q", mode, got, skillSwitchCandidatePairsOff)
		}
		if err := service.sendSkillSwitchInventoryProjection(session, "candidate_pairs_off_boundary"); err != nil {
			t.Fatalf("mode %q projection error = %v", mode, err)
		}
		if packets := drainUpperPackets(t, connection); len(packets) != 0 {
			t.Fatalf("mode %q wrote %d packets, want 0 (byte-identical to today)", mode, len(packets))
		}
		log := readPeerEndpointTestLog(t, logger, logPath)
		if strings.Contains(log, skillSwitchCandidatePairsSendEvent) {
			t.Fatalf("mode %q must not log %s; log=\n%s", mode, skillSwitchCandidatePairsSendEvent, log)
		}
		// 既有的"全部被形状闸门拦下"契约不受影响。
		if !strings.Contains(log, skillSwitchInventoryProjectionBlockedEvent) {
			t.Fatalf("mode %q must still log the shape-guard block; log=\n%s", mode, log)
		}
	}
}

// TestSkillSwitchCandidatePairsOnEveryLegitBoundary 钉住"挂在既有合法投影边界上"：
// 五条边界各发**恰好一帧**实验帧（同一个 service/session，逐条边界调用）。
// 产品侧的重下发帧按回退字面量关掉，让本用例只钉实验帧（见上一个用例的说明）。
func TestSkillSwitchCandidatePairsOnEveryLegitBoundary(t *testing.T) {
	t.Setenv(skillSwitchLoadoutResendEnv, skillSwitchLoadoutResendShapeGuard)
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
	service, session, connection, logger, logPath := newSkillSwitchInventoryService(t, "", repos)
	service.options.skillSwitchCandidatePairsMode = skillSwitchCandidatePairsIdentity12

	boundaries := append(append([]string(nil), skillSwitchInventoryBoundaries...),
		"legacy_type1345_scene_ready_ack_skill_switch_inventory_after_scene_ready")
	for _, boundary := range boundaries {
		if err := service.sendSkillSwitchInventoryProjection(session, boundary); err != nil {
			t.Fatalf("boundary %q projection error = %v", boundary, err)
		}
		packets := drainUpperPackets(t, connection)
		if len(packets) != 1 {
			t.Fatalf("boundary %q wrote %d packets, want exactly the one candidate frame", boundary, len(packets))
		}
		if got := hex.EncodeToString(packets[0].body); got != skillSwitchCandidateIdentity12BodyHex {
			t.Fatalf("boundary %q candidate body = %s", boundary, got)
		}
	}
	log := readPeerEndpointTestLog(t, logger, logPath)
	for _, boundary := range boundaries {
		if !strings.Contains(log, "source="+boundary) {
			t.Fatalf("candidate frame missing the %q source; log=\n%s", boundary, log)
		}
	}
	if got := strings.Count(log, "EVENT kind="+skillSwitchCandidatePairsSendEvent); got != len(boundaries) {
		t.Fatalf("candidate send events = %d, want %d", got, len(boundaries))
	}
}

// TestSkillSwitchCandidatePairTableInvariants 是**表驱动**的不变量：
// 候选表里每一条候选都必须构造出满足客户端解析器形状的包体；
// 且表里**不得**出现已被证伪的 family-2 值域。
func TestSkillSwitchCandidatePairTableInvariants(t *testing.T) {
	if len(skillSwitchCandidatePairSets) == 0 {
		t.Fatal("candidate pair table must not be empty")
	}
	identity, present := skillSwitchCandidatePairSets[skillSwitchCandidatePairsIdentity12]
	if !present {
		t.Fatalf("table must contain %q", skillSwitchCandidatePairsIdentity12)
	}
	if len(identity) != 12 {
		t.Fatalf("identity12 has %d pairs, want 12 (slot_count=12)", len(identity))
	}
	for name, pairs := range skillSwitchCandidatePairSets {
		if name == skillSwitchCandidatePairsOff {
			t.Fatalf("the off tier must never be a table key (it would make the experiment default-on)")
		}
		body, err := buildSkillSwitchCandidatePairsBody(pairs)
		if err != nil {
			t.Fatalf("candidate %q build error = %v", name, err)
		}
		if !skillSwitchInventoryFrameShapeValid(body) {
			t.Fatalf("candidate %q body (len %d, count %d) would be rejected by the shape guard",
				name, len(body), body[0])
		}
		if len(body) != 1+8*len(pairs) || int(body[0]) != len(pairs) {
			t.Fatalf("candidate %q body len %d / count %d do not match %d pairs",
				name, len(body), body[0], len(pairs))
		}
		for _, pair := range pairs {
			// family-2「slot → 源槽位」= (slot, 54/55/…/64)：0 命中，已证伪，禁止再进表。
			if pair.Value >= 54 && pair.Value <= 64 {
				t.Fatalf("candidate %q carries the falsified family-2 value %d (never send those pairs)",
					name, pair.Value)
			}
		}
	}
	// 构造失败的两侧：0 组与 256 组都必须具名报错（不构造半截帧）。
	if _, err := buildSkillSwitchCandidatePairsBody(nil); err == nil {
		t.Fatal("empty candidate must fail to build")
	}
	tooMany := make([]skillSwitchCandidatePair, 256)
	if _, err := buildSkillSwitchCandidatePairsBody(tooMany); err == nil {
		t.Fatal("a 256-pair candidate must fail to build (u8 count)")
	}
}

// TestSkillSwitchCandidatePairsSendIsGatedByInventorySwitch 钉住两层开关的关系：
// `DNFBRIDGE_SKILL_SWITCH_INVENTORY` 的 `record`/`reference_drop` 档根本不投影
// ⇒ 实验帧也不会发（实验不能绕过"这个功能关掉"这件事）。
func TestSkillSwitchCandidatePairsSendIsGatedByInventorySwitch(t *testing.T) {
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
	// reference_drop（不落库档）：实验帧不得出现（连落库都没有）。
	service, session, connection, logger, logPath := newSkillSwitchInventoryService(
		t, skillSwitchInventoryReferenceDrop, repos)
	service.options.skillSwitchCandidatePairsMode = skillSwitchCandidatePairsIdentity12
	if err := service.sendSkillSwitchInventoryProjection(session, "reference_drop_with_candidate"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	if packets := drainUpperPackets(t, connection); len(packets) != 0 {
		t.Fatalf("reference_drop wrote %d packets even with the candidate switch on", len(packets))
	}
	if log := readPeerEndpointTestLog(t, logger, logPath); strings.Contains(log, skillSwitchCandidatePairsSendEvent) {
		t.Fatalf("reference_drop must not send the candidate frame; log=\n%s", log)
	}

	// 同一份仓储、record 档（只落库不下发）：同样零字节。
	service, session, connection, _, _ = newSkillSwitchInventoryService(t, "", repos)
	service.options.skillSwitchInventoryMode = skillSwitchInventoryRecordTier
	service.options.skillSwitchCandidatePairsMode = skillSwitchCandidatePairsIdentity12
	if err := service.sendSkillSwitchInventoryProjection(session, "record_with_candidate"); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	if packets := drainUpperPackets(t, connection); len(packets) != 0 {
		t.Fatalf("record tier wrote %d packets even with the candidate switch on", len(packets))
	}
}

// TestSkillSwitchCandidatePairsUnprovenStateIsHonest 钉住"未证实"这件事写进了代码与日志：
// 候选对不是修复，证据状态必须显式出现在发送事件里。
func TestSkillSwitchCandidatePairsUnprovenStateIsHonest(t *testing.T) {
	body, err := buildSkillSwitchCandidatePairsBody(skillSwitchCandidatePairSets[skillSwitchCandidatePairsIdentity12])
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(body); got != skillSwitchCandidateIdentity12BodyHex {
		t.Fatalf("built body = %s", got)
	}
	if !strings.Contains(skillSwitchCandidatePairsSendEvent, "candidate-pairs-send") {
		t.Fatalf("event name must stay greppable: %q", skillSwitchCandidatePairsSendEvent)
	}
	// 传输层仍走既有的 class-0 raw 通道（与 shape_guard 判定的那批同一函数），
	// 而零值 Service 的候选档必须是**关**。
	if got := (&Service{}).skillSwitchCandidatePairsMode(); got != skillSwitchCandidatePairsOff {
		t.Fatalf("zero-value Service candidate mode = %q, want %q", got, skillSwitchCandidatePairsOff)
	}
}
