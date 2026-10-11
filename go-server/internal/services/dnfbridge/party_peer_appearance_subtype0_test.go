package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住 2026-09-28 夜这一轮的三条修法：
//
//	① `DNFBRIDGE_PEER_APPEARANCE_SUBTYPE0_LIST` —— 队友投影补上"被投影角色自己的
//	   USERINFO subtype0 外观列表"这第二条载体（厂商同屏口径 TownHandler.cs:502-515）。
//	   mode0 那条载体的 delta 已经是 0（线上逐字节比对），缺的就是这一条。
//	② `DNFBRIDGE_PARTY_DUNGEON_RUN_KEY_TOWN_RESET` —— 解散/回城之后失效的副本运行键
//	   必须在**城镇上下文**被清掉，否则重新组队时新队伍会被误判成"还在上一场副本里"。
//	③ `DNFBRIDGE_TOWN_COPRESENCE_RESEND_ON_POST_TRANSITION` —— 城镇过场后状态链
//	   （op24/op35 重建）走的是快照版收尾，绕过了唯一的城镇补发调用 ⇒ 回城之后
//	   一次同屏投影都不补，要"来回离开城镇"才重新看见对方。
//
// 三条的夹具都取线上真值（角色 3 = test2 普通装扮 / 角色 6 = test78 八件克隆），
// 不发明任何形状；回退档必须逐字节回到改动前。

// peerSubtype0Fixture 造一个"队友投影"夹具：收包方（角色 99）+ 被投影角色（角色 100，
// 八件克隆装扮），并把被投影角色的穿戴记录写进内存仓库。
func peerSubtype0Fixture(t *testing.T) (*Service, *gameSession, *gameSession, dnfrepo.Group) {
	t.Helper()
	service, leader, follower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerSceneContext, false)
	repos, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture repository group unavailable")
	}
	record := peerAppearanceMemberRecord()
	record.CharacterID = "100"
	if err := repos.Equipment.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return service, leader, follower, repos
}

// peerSubtype0AppearanceRow 是测试侧对 23 字节外观行的独立解析（偏移来自厂商
// `CharacterAppearanceEntry.FromBytes`：slot@+0、display@+1、link@+14）。
func peerSubtype0AppearanceRow(t *testing.T, body []byte, index int) (slot int, display uint32, link uint32) {
	t.Helper()
	const fixedPrefix = 5
	if len(body) < fixedPrefix+4 {
		t.Fatalf("subtype0 body too short: %d", len(body))
	}
	nameLen := int(binary.LittleEndian.Uint32(body[fixedPrefix : fixedPrefix+4]))
	pos := fixedPrefix + 4 + nameLen + 6
	if pos >= len(body) {
		t.Fatalf("subtype0 appearance count offset %d beyond body %d", pos, len(body))
	}
	count := int(body[pos])
	pos++
	if index >= count {
		t.Fatalf("subtype0 row %d beyond count %d", index, count)
	}
	pos += index * currentSubtype0AppearanceEntryBytes
	if pos+currentSubtype0AppearanceEntryBytes > len(body) {
		t.Fatalf("subtype0 row %d beyond body", index)
	}
	entry := body[pos : pos+currentSubtype0AppearanceEntryBytes]
	return int(entry[0]),
		binary.LittleEndian.Uint32(entry[1:5]),
		binary.LittleEndian.Uint32(entry[14:18])
}

// peerSubtype0JudgementLine 从 packet log 里取那条判读事件。
func peerSubtype0JudgementLine(t *testing.T, logged string) string {
	t.Helper()
	for _, candidate := range strings.Split(logged, "\n") {
		if strings.Contains(candidate, "kind=game-party-peer-actor-appearance") {
			return candidate
		}
	}
	t.Fatalf("judgement event missing from packet log\n%s", logged)
	return ""
}

// TestPeerAppearanceSubtype0CarrierIsEmittedNextToMode0 钉住端到端：修复档下
// `sendDungeonPartyPeerActorState` 必须在 mode0/mode1 之后**多一条** `msg_id=2`
// 的 subtype0 体，且那条体里的外观列表带上 8 行克隆链接；回退档下这条包必须消失、
// 判读事件逐字节回到改动前那一行。
func TestPeerAppearanceSubtype0CarrierIsEmittedNextToMode0(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOn)

	send := func(t *testing.T, subtype0Switch string) (string, []upperFrame) {
		t.Helper()
		t.Setenv(currentPeerAppearanceSubtype0Env, subtype0Switch)
		logPath := filepath.Join(t.TempDir(), "packet.log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		service, leader, follower, _ := peerSubtype0Fixture(t)
		service.packetLog = logger
		if err := service.sendDungeonPartyPeerActorState(leader, follower, "test_subtype0_carrier"); err != nil {
			t.Fatalf("send peer actor state: %v", err)
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(logged), splitUpperFrames(t, leader.conn.(*bufferConn).write.Bytes())
	}

	fixed, fixedFrames := send(t, cloneAvatarAppearanceOn)
	line := peerSubtype0JudgementLine(t, fixed)
	for _, field := range []string{
		"appearance_subtype0_env=" + currentPeerAppearanceSubtype0Env,
		"appearance_subtype0_mode=on",
		"appearance_subtype0_sent=true",
		"appearance_subtype0_rows=13",
		"appearance_subtype0_link_rows=8",
		"peer_userinfo_carrier=peer_userinfo_subtype0_appearance_list_sent",
	} {
		if !strings.Contains(line, field) {
			t.Fatalf("fix line missing %q\nline=%s", field, line)
		}
	}
	if strings.Contains(line, "omitted=noti2_roster_appearance_table(login_only_carrier),") {
		t.Fatalf("fixed judgement event still lists the roster appearance carrier as omitted\nline=%s", line)
	}
	mode2 := 0
	for _, frame := range fixedFrames {
		if frame.msgID == 2 {
			mode2++
		}
	}
	if mode2 != 4 {
		t.Fatalf("fix档 msg_id=2 frames=%d want 4 (mode0 + mode1 + peer subtype0 + clone appearance replay)", mode2)
	}

	legacy, legacyFrames := send(t, cloneAvatarAppearanceLegacy)
	legacyLine := peerSubtype0JudgementLine(t, legacy)
	if strings.Contains(legacyLine, "appearance_subtype0_") {
		t.Fatalf("legacy judgement line must be byte-for-byte the pre-change line\nline=%s", legacyLine)
	}
	if !strings.Contains(legacyLine, "omitted=noti2_roster_appearance_table(login_only_carrier),") {
		t.Fatalf("legacy judgement line must still name the roster carrier as omitted\nline=%s", legacyLine)
	}
	legacyMode2 := 0
	for _, frame := range legacyFrames {
		if frame.msgID == 2 {
			legacyMode2++
		}
	}
	if legacyMode2 != 2 {
		t.Fatalf("legacy档 msg_id=2 frames=%d want 2 (mode0 + mode1 = today's bytes)", legacyMode2)
	}
}

// TestPeerAppearanceSubtype0CarriesClonePairOnTheVendorRowShape 钉住 23 字节行：
// 第 0 行必须是"显示被克隆件 101550581 + 链接克隆件自己 42601"（厂商
// Noti2InventoryProjectionBuilder.cs:250-275 的配对），且行数/链接行数与线上一致。
func TestPeerAppearanceSubtype0CarriesClonePairOnTheVendorRowShape(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)

	service, _, _, _ := peerSubtype0Fixture(t)
	record := peerAppearanceMemberRecord()
	record.CharacterID = "100"
	character := peerAppearanceCharacter(record)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 100}
	body, ok := service.buildPeerUserInfoSubtype0AppearanceBody(
		context.Background(), session, session, character, true, 100, character.Name,
	)
	if !ok || len(body) == 0 {
		t.Fatal("subtype0 appearance body was not built")
	}
	rows, linkRows, displayRows, parsed := currentSubtype0AppearanceBodyCompositionForLog(body)
	if !parsed {
		t.Fatalf("subtype0 body is not parseable: len=%d", len(body))
	}
	if rows != 13 {
		t.Fatalf("subtype0 rows=%d want 13 (8 clones + skin/aura/weapon-avatar/aura-anim/weapon)", rows)
	}
	if linkRows != len(peerAppearanceCloneRows) {
		t.Fatalf("subtype0 link rows=%d want %d", linkRows, len(peerAppearanceCloneRows))
	}
	if displayRows != rows {
		t.Fatalf("subtype0 display rows=%d want %d", displayRows, rows)
	}
	slot, display, link := peerSubtype0AppearanceRow(t, body, 0)
	if slot != 0 || display != 101550581 || link != 42601 {
		t.Fatalf("subtype0 row0 = slot=%d display=%d link=%d want 0/101550581/42601", slot, display, link)
	}
}

// TestPeerAppearanceSubtype0RollbackKeepsTheBodyIdentical 钉住回退档的**字节**判据：
// 关掉本开关时 subtype0 体根本不构造（nil），因此下发序列与改动前逐字节相同。
func TestPeerAppearanceSubtype0RollbackKeepsTheBodyIdentical(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)

	service, leader, follower, _ := peerSubtype0Fixture(t)
	record := peerAppearanceMemberRecord()
	record.CharacterID = "100"
	character := peerAppearanceCharacter(record)
	// 窄门的判据来自真实 mode0 体（19 字节外观表的 LinkItemId 行数），所以夹具也要
	// 用与记录 CharacterID 一致的那份（peerAppearanceMode0Body 按记录 id 查库）。
	cloneBody := peerAppearanceMode0Body(t, peerAppearanceMemberRecord())
	t.Setenv(currentPeerAppearanceSubtype0Env, cloneAvatarAppearanceLegacy)
	if body := service.peerActorUserInfoSubtype0BodyForProjection(
		context.Background(), leader, follower, character, true, 100, character.Name, cloneBody,
	); body != nil {
		t.Fatalf("legacy档 must not build the subtype0 carrier, got %d bytes", len(body))
	}
	t.Setenv(currentPeerAppearanceSubtype0Env, cloneAvatarAppearanceOn)
	if body := service.peerActorUserInfoSubtype0BodyForProjection(
		context.Background(), leader, follower, character, true, 100, character.Name, cloneBody,
	); len(body) == 0 {
		t.Fatal("fix档 must build the subtype0 carrier for a clone wearer")
	}
	// 窄门：没有克隆行的角色（现场=队长 test2 的普通装扮）必须一个字节都不多。
	plainBody := peerAppearanceMode0Body(t, peerAppearanceLeaderRecord())
	if body := service.peerActorUserInfoSubtype0BodyForProjection(
		context.Background(), leader, follower, character, true, 100, character.Name, plainBody,
	); body != nil {
		t.Fatalf("a peer without clone avatar rows must not receive the second carrier, got %d bytes", len(body))
	}
}

// TestPeerAppearanceSubtype0SwitchRejectsUnknownValue 与同包既有开关同口径：
// 非法值回退到 on 且 bad=true；legacy 归回退档。
func TestPeerAppearanceSubtype0SwitchRejectsUnknownValue(t *testing.T) {
	t.Setenv(currentPeerAppearanceSubtype0Env, "maybe")
	enabled, raw, bad := currentPeerAppearanceSubtype0Setting()
	if !enabled || !bad || raw != "maybe" {
		t.Fatalf("unknown setting = enabled=%t raw=%q bad=%t, want true/maybe/true", enabled, raw, bad)
	}
	t.Setenv(currentPeerAppearanceSubtype0Env, cloneAvatarAppearanceLegacy)
	enabled, raw, bad = currentPeerAppearanceSubtype0Setting()
	if enabled || bad || raw != cloneAvatarAppearanceLegacy {
		t.Fatalf("legacy setting = enabled=%t raw=%q bad=%t, want false/legacy/false", enabled, raw, bad)
	}
}
