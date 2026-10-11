// 本文件把操作者 2026-09-28 的报障编码成回归钉子：
//
//	组队时队长（炽天使 / 女圣骑士）放 勇气祝福，队员**看得见特效**但**面板不动**，
//	且队员自己的 buff 栏里这条 buff **显示为 0 级**。
//
// 修法与档位表见 peer_skill_page.go 文件头。本文件的断言**全部走真实传播路径**：
// `partyDungeonDeathFixture`（真 Service + 真内存仓储 + 同一副本实例的两个会话）
// → `sendDungeonPartyPeerActorState`（真实队友 actor 投影）→ 读**队员 socket 上真正的帧字节**，
// 不 mock 任何一环。
package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	// 与 partyDungeonDeathFixture 的两个会话一致：leader = 99（施法者/被投影的 actor），
	// follower = 100（收包队员）。
	partyBuffCasterCharacterID = "99"
	partyBuffTargetCharacterID = "100"

	// 勇气祝福 = skill id 14（skill/atpriestskill.lst 第 14 项 ATPriest/BlessOfCourage.skl）。
	// 现场等级取 HK 库 dnf_skill_states character_id=3 skill_id=14 = 10（工具提示 Lv10(+3)，
	// +3 来自装备/装扮，不进技能页）。
	partyBuffValorBlessingSkillID = uint16(14)
	partyBuffValorBlessingLevel   = byte(10)

	// 修复前的队友 mode1 body 字节（改动前用同一条真实路径冻结：长度 + SHA-256）。
	// `off` 档必须逐字节回到这一串（=今天线上的字节）。
	partyBuffRollbackMode1Len    = 163
	partyBuffRollbackMode1SHA256 = "1e6e709f4116b68addc2db4c30e8d153f882ad8c434ee25a4b8c7f8944f41f8a"
)

// partyBuffCasterSkills 是操作者现场那条 炽天使 技能向量的真子集
// （HK 库 dnf_skill_states character_id=3 的逐行抄录，只取有代表性的 16 条）。
func partyBuffCasterSkills() map[int64]dnfrepo.SkillState {
	return map[int64]dnfrepo.SkillState{
		14:  {Level: 10, Enabled: true}, // 勇气祝福（本症状的主角）
		21:  {Level: 10, Enabled: true}, // 守护祝福（勇气祝福的前置技能）
		24:  {Level: 21, Enabled: true}, // 勇气圣歌
		26:  {Level: 1, Enabled: true},
		27:  {Level: 1, Enabled: true},
		33:  {Level: 1, Enabled: true},
		34:  {Level: 16, Enabled: true},
		35:  {Level: 18, Enabled: true},
		36:  {Level: 13, Enabled: true},
		107: {Level: 1, Enabled: true},
		169: {Level: 1, Enabled: true},
		174: {Level: 1, Enabled: true},
		179: {Level: 7, Enabled: true},
		197: {Level: 1, Enabled: true},
		501: {Level: 1, Enabled: true},
		511: {Level: 1, Enabled: true},
	}
}

// setPeerSkillPageTier 设置档位并在用例结束恢复（避免污染同包其他用例）。
func setPeerSkillPageTier(t *testing.T, value string) {
	t.Helper()
	original, had := os.LookupEnv(peerSkillPageEnv)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(peerSkillPageEnv, original)
			return
		}
		_ = os.Unsetenv(peerSkillPageEnv)
	})
	if value == "" {
		if err := os.Unsetenv(peerSkillPageEnv); err != nil {
			t.Fatalf("unset %s: %v", peerSkillPageEnv, err)
		}
		return
	}
	if err := os.Setenv(peerSkillPageEnv, value); err != nil {
		t.Fatalf("set %s=%q: %v", peerSkillPageEnv, value, err)
	}
}

// partyBuffPeerMode1Body 走真实传播路径产出"队员实际收到的那一条 mode1 body"：
// 真 Service（真仓储 + 同一副本实例）→ 真队友 actor 投影 → 队员 socket 字节。
// seedCaster 为真时把施法者的技能行按现场数据写进技能仓储。
func partyBuffPeerMode1Body(t *testing.T, seedCaster bool) []byte {
	t.Helper()
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	if seedCaster {
		repos, ok := service.repositoryGroup()
		if !ok {
			t.Fatal("fixture has no repository group")
		}
		if err := repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{
			CharacterID: partyBuffCasterCharacterID,
			Skills:      partyBuffCasterSkills(),
		}); err != nil {
			t.Fatalf("seed caster skill rows: %v", err)
		}
	}
	conn := follower.conn.(*bufferConn)
	conn.write.Reset()
	if err := service.sendDungeonPartyPeerActorState(follower, leader, "test_party_buff_peer_actor"); err != nil {
		t.Fatalf("send peer actor state: %v", err)
	}
	frames := splitUpperFrames(t, conn.write.Bytes())
	if len(frames) != 3 {
		t.Fatalf("peer actor frames=%d want mode0 + mode1 + op9", len(frames))
	}
	return frames[1].body
}

// peerSkillPageBlockOffset 用 `off` 档 body 做对照，定位"队员收到的那条 mode1 body"里
// 技能页块的两个计数字节的起点：
//
//	off  : 0xff, 0x00, 0x00, <creature level> ...
//	修复 : 0xff, page0, page1,    <creature level> ...
//
// 两个 body 除"两个计数字节 → 两页技能"之外必须逐字节相同，所以第一个差异字节就是
// page0 的计数字节，且它前面那个字节必须仍是位置字节 0xff。返回 page0 计数字节下标。
func peerSkillPageBlockOffset(t *testing.T, label string, sentBody, rollbackBody []byte) int {
	t.Helper()
	if bytes.Equal(sentBody, rollbackBody) {
		t.Fatalf("%s: the body the member received is byte-identical to the rollback body "+
			"(len=%d): the caster's skill page was never sent, so the member's client still "+
			"resolves skill %d at level 0 and applies no stats (the reported defect)",
			label, len(sentBody), partyBuffValorBlessingSkillID)
	}
	prefix := 0
	for prefix < len(sentBody) && prefix < len(rollbackBody) && sentBody[prefix] == rollbackBody[prefix] {
		prefix++
	}
	if prefix == 0 || prefix >= len(sentBody) || prefix >= len(rollbackBody) {
		t.Fatalf("%s: bodies share no usable prefix (sent=%d rollback=%d)", label, len(sentBody), len(rollbackBody))
	}
	if got := sentBody[prefix-1]; got != peerSkillPageSelectedResourceGroup {
		t.Fatalf("%s: byte before the page counts = %#x, want the unchanged position byte %#x",
			label, got, peerSkillPageSelectedResourceGroup)
	}
	suffix := 0
	for suffix < len(sentBody)-prefix && suffix < len(rollbackBody)-prefix &&
		sentBody[len(sentBody)-1-suffix] == rollbackBody[len(rollbackBody)-1-suffix] {
		suffix++
	}
	if got, want := rollbackBody[prefix:len(rollbackBody)-suffix], []byte{0, 0}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("%s: rollback body's skill page slots = %x, want two 0x00 counts", label, got)
	}
	return prefix
}

// peerSkillPageParsePage 解一页：`u8 count` + count×{u16 skillId, u8 level}，返回条目与消耗字节数。
func peerSkillPageParsePage(t *testing.T, label string, body []byte, at int) ([]peerSkillPageEntry, int) {
	t.Helper()
	if at >= len(body) {
		t.Fatalf("%s: page at offset %d is past the body end (%d)", label, at, len(body))
	}
	count := int(body[at])
	consumed := 1 + count*3
	if at+consumed > len(body) {
		t.Fatalf("%s: page at offset %d claims %d entries but only %d bytes remain",
			label, at, count, len(body)-at-1)
	}
	entries := make([]peerSkillPageEntry, 0, count)
	for index := 0; index < count; index++ {
		start := at + 1 + index*3
		entries = append(entries, peerSkillPageEntry{
			SkillID: uint16(body[start]) | uint16(body[start+1])<<8,
			Level:   body[start+2],
		})
	}
	return entries, consumed
}

// peerSkillPageSentPages 解出"队员收到的那条 body"里的 page0 / page1。
func peerSkillPageSentPages(t *testing.T, sentBody, rollbackBody []byte) ([]peerSkillPageEntry, []peerSkillPageEntry) {
	t.Helper()
	offset := peerSkillPageBlockOffset(t, "peer mode1", sentBody, rollbackBody)
	page0, consumed0 := peerSkillPageParsePage(t, "page0", sentBody, offset)
	page1, consumed1 := peerSkillPageParsePage(t, "page1", sentBody, offset+consumed0)
	if rest := len(sentBody) - offset - consumed0 - consumed1; rest != len(rollbackBody)-offset-2 {
		t.Fatalf("page block accounting mismatch: %d trailing bytes, want %d",
			rest, len(rollbackBody)-offset-2)
	}
	if len(page0) != len(page1) {
		t.Fatalf("page0 entries=%d page1 entries=%d, want the same two-page rule", len(page0), len(page1))
	}
	return page0, page1
}

// peerSkillPageLevel 在条目里取某技能的等级（不存在 = 0，即修复前客户端看到的值）。
func peerSkillPageLevel(entries []peerSkillPageEntry, skillID uint16) byte {
	for _, entry := range entries {
		if entry.SkillID == skillID {
			return entry.Level
		}
	}
	return 0
}

// peerSkillPageBuffStatDelta 是"客户端会为收包队友移动哪些属性"的判据，读的是生产侧的
// 只读命名表（peerSkillPagePartyBuffSkills）。等级 0（= 修复前的线格式）必须**没有**属性增量。
func peerSkillPageBuffStatDelta(t *testing.T, skillID int64, level byte) (string, bool) {
	t.Helper()
	if level == 0 {
		return "", false
	}
	for _, buff := range peerSkillPagePartyBuffSkills {
		if buff.SkillID == skillID {
			return buff.Stats, buff.Stats != ""
		}
	}
	t.Fatalf("skill %d is not in peerSkillPagePartyBuffSkills; the real case must be table-backed", skillID)
	return "", false
}

// TestPeerSkillPagePartyMemberReceivesCasterBuffLevelThroughRealPropagation 是操作者的**真实用例**：
// 炽天使 勇气祝福 Lv10 打在队友身上 —— 队友收到的那条 mode1 body 必须带
// (skill 14 → level 10)，于是客户端按 10 级算属性；回退档下同一条 body 里它是 0 级（= 现场症状）。
func TestPeerSkillPagePartyMemberReceivesCasterBuffLevelThroughRealPropagation(t *testing.T) {
	// 回退档：今天线上的字节 —— 收包队员按 0 级算，面板不动。
	setPeerSkillPageTier(t, peerSkillPageValueOff)
	rollbackBody := partyBuffPeerMode1Body(t, true)

	// 默认档（不设置 env）：修复档应当把施法者的真实技能页投给队友。
	setPeerSkillPageTier(t, "")
	sentBody := partyBuffPeerMode1Body(t, true)

	page0, page1 := peerSkillPageSentPages(t, sentBody, rollbackBody)

	// 1) 队员侧最终生效的等级：等级 0 是现场症状，必须变成施法者的真实等级。
	levelSent := peerSkillPageLevel(page0, partyBuffValorBlessingSkillID)
	if levelSent == 0 {
		t.Fatalf("member still applies 勇气祝福 (skill %d) at level 0; page0=%+v", partyBuffValorBlessingSkillID, page0)
	}
	if levelSent != partyBuffValorBlessingLevel {
		t.Fatalf("member applies 勇气祝福 level=%d, want the caster's persisted level %d",
			levelSent, partyBuffValorBlessingLevel)
	}
	if got := peerSkillPageLevel(page1, partyBuffValorBlessingSkillID); got != partyBuffValorBlessingLevel {
		t.Fatalf("page1 勇气祝福 level=%d, want %d (two-page rule: page0 == page1)", got, partyBuffValorBlessingLevel)
	}
	// 回退档里同一个技能必须是 0 级 —— 这就是"修复前"的对照，也是 delta 的起点：
	// 回退档 body 在该偏移上就是两个 0x00 计数（page0 = page1 = 空），客户端只能按 0 级算。
	rollbackOffset := peerSkillPageBlockOffset(t, "peer mode1 rollback", sentBody, rollbackBody)
	rollbackPage0, rollbackConsumed := peerSkillPageParsePage(t, "rollback page0", rollbackBody, rollbackOffset)
	if len(rollbackPage0) != 0 || rollbackConsumed != 1 {
		t.Fatalf("rollback page0=%+v consumed=%d, want an empty page (count 0)", rollbackPage0, rollbackConsumed)
	}
	if got := peerSkillPageLevel(rollbackPage0, partyBuffValorBlessingSkillID); got != 0 {
		t.Fatalf("rollback tier reported 勇气祝福 level=%d, want 0 (the reported defect)", got)
	}

	// 2) 等级与**技能仓储里那个施法者**的真实等级一致（不是写死的常量）。
	persisted := partyBuffCasterSkills()[int64(partyBuffValorBlessingSkillID)]
	if int(levelSent) != persisted.Level {
		t.Fatalf("level sent=%d, want the caster's stored level %d", levelSent, persisted.Level)
	}

	// 3) 期望的属性增量：等级 > 0 ⇒ 客户端按 PVF 把这几项加上去；等级 0 ⇒ 一项都不动。
	stats, applied := peerSkillPageBuffStatDelta(t, int64(partyBuffValorBlessingSkillID), levelSent)
	if !applied || stats == "" {
		t.Fatalf("level %d produced no stat delta", levelSent)
	}
	for _, want := range []string{"physical_attack", "magical_attack", "independent_attack", "strength", "intelligence", "hit_rate"} {
		if !strings.Contains(stats, want) {
			t.Fatalf("勇气祝福 Lv%d stat delta %q is missing %q (the operator-visible panel stats)",
				levelSent, stats, want)
		}
	}
	if _, appliedAtZero := peerSkillPageBuffStatDelta(t, int64(partyBuffValorBlessingSkillID), 0); appliedAtZero {
		t.Fatal("level 0 must produce no stat delta; that is exactly the reported defect")
	}
}

// TestPeerSkillPageEffectCannotBeSentWithZeroLevel 钉住不变式：
// **只要技能页（= 特效等级载荷）被投出去，页里就不可能存在 0 级条目**；
// 而且投递确实发生了（页非空），所以"只发特效、等级为零载荷"的那种组合构造不出来。
func TestPeerSkillPageEffectCannotBeSentWithZeroLevel(t *testing.T) {
	setPeerSkillPageTier(t, peerSkillPageValueOff)
	rollbackBody := partyBuffPeerMode1Body(t, true)
	setPeerSkillPageTier(t, "")
	sentBody := partyBuffPeerMode1Body(t, true)

	page0, page1 := peerSkillPageSentPages(t, sentBody, rollbackBody)
	if len(page0) == 0 {
		t.Fatal("default tier sent an empty skill page: the member's client has no level payload at all")
	}
	for _, entry := range append(append([]peerSkillPageEntry(nil), page0...), page1...) {
		if entry.Level == 0 {
			t.Fatalf("skill page carries a zero-level entry %+v: effect without a level payload", entry)
		}
	}
	// 规整函数本身也不许放行 0 级/负级行。
	entries := peerSkillPageEntries(map[int64]dnfrepo.SkillState{
		14: {Level: 0},
		15: {Level: -3},
		16: {Level: 1},
	})
	for _, entry := range entries {
		if entry.Level == 0 || entry.SkillID == 14 || entry.SkillID == 15 {
			t.Fatalf("entries=%+v must skip every level<=0 row", entries)
		}
	}
	// 队长的 勇气祝福 在 pg0 里必须非 0 —— 这条就是操作者能看见的那条 buff。
	if got := peerSkillPageLevel(page0, partyBuffValorBlessingSkillID); got == 0 {
		t.Fatalf("勇气祝福 level payload is zero: %+v", page0)
	}
}

// TestPeerSkillPageRollbackLiteralReproducesTodaysBytes 钉住回退字面量：
// `off`（以及它的同族取值）必须逐字节回到改动前冻结的那条队友 mode1 body —— 长度与 SHA-256 都对上，
// 且技能仓储里有没有技能行都不影响字节。
func TestPeerSkillPageRollbackLiteralReproducesTodaysBytes(t *testing.T) {
	setPeerSkillPageTier(t, peerSkillPageValueOff)
	baselineSeeded := partyBuffPeerMode1Body(t, true)
	baselineEmpty := partyBuffPeerMode1Body(t, false)
	if len(baselineEmpty) != partyBuffRollbackMode1Len {
		t.Fatalf("rollback body length=%d, want the frozen pre-change %d", len(baselineEmpty), partyBuffRollbackMode1Len)
	}
	sum := sha256.Sum256(baselineEmpty)
	if got := hex.EncodeToString(sum[:]); got != partyBuffRollbackMode1SHA256 {
		t.Fatalf("rollback body sha256=%s, want the frozen pre-change %s", got, partyBuffRollbackMode1SHA256)
	}
	if string(baselineSeeded) != string(baselineEmpty) {
		t.Fatal("rollback tier leaked skill rows into the peer mode1 body")
	}
	// 位置算术：技能页槽位（位置字节 + 两个计数字节）之后固定还有 20 字节尾部
	// （creature.level + 5×count/flag + 3×u32 + 2×byte），所以槽位就是 body[len-23..len-21]。
	// 现场那条 `mode1_body_len=1333` 的队员 body 于是是 body[1310]=0xff、body[1311]=body[1312]=0x00
	// —— 即"队员收到的这个 actor 的技能表是空的"，这正是"buff 显示 0 级、属性不动"的线格式证据。
	slot := len(baselineEmpty) - 23
	if slot < 0 || baselineEmpty[slot] != peerSkillPageSelectedResourceGroup ||
		baselineEmpty[slot+1] != 0 || baselineEmpty[slot+2] != 0 {
		t.Fatalf("rollback tier skill page slot at len-23 = %x, want %02x0000",
			baselineEmpty[slot:slot+3], peerSkillPageSelectedResourceGroup)
	}
	for _, value := range []string{"off", "OFF", "0", "false", "no", "legacy", "disable", "disabled", "none", "off "} {
		setPeerSkillPageTier(t, value)
		body := partyBuffPeerMode1Body(t, true)
		if string(body) != string(baselineEmpty) {
			t.Fatalf("%s=%q changed the peer mode1 body", peerSkillPageEnv, value)
		}
	}
}

// TestPeerSkillPageSelfBodiesStayByteIdenticalUnderDefaultTier 钉住收窄范围：
// 默认档只动"发给队友"的那一条 body；发给本人的 mode1、subtype1、mode3 全部逐字节等于回退档。
func TestPeerSkillPageSelfBodiesStayByteIdenticalUnderDefaultTier(t *testing.T) {
	service := peerSkillPageTestService(t, peerSkillPageTestSkills())

	setPeerSkillPageTier(t, peerSkillPageValueOff)
	offMode1 := peerSkillPageTestMode1Body(&service) // 无 ctx 队友标记 = 发给本人的那条
	offSubtype1 := peerSkillPageTestSubtype1Body(&service)
	offMode3 := peerSkillPageTestMode3Body(&service)

	setPeerSkillPageTier(t, "")
	if body := peerSkillPageTestMode1Body(&service); string(body) != string(offMode1) {
		t.Fatalf("default tier changed the SELF mode1 body:\n got=%x\nwant=%x", body, offMode1)
	}
	if body := peerSkillPageTestSubtype1Body(&service); string(body) != string(offSubtype1) {
		t.Fatalf("default tier changed the subtype1 body:\n got=%x\nwant=%x", body, offSubtype1)
	}
	if body := peerSkillPageTestMode3Body(&service); string(body) != string(offMode3) {
		t.Fatalf("default tier changed the mode3 body:\n got=%x\nwant=%x", body, offMode3)
	}

	// 同一个 mode1 构造器，带上队友标记后必须写技能页（证明判据是"发给谁"，不是"哪个函数"）。
	setPeerSkillPageTier(t, peerSkillPageValueOff)
	offPeer := peerSkillPageTestPeerMode1Body(&service)
	setPeerSkillPageTier(t, "")
	onPeer := peerSkillPageTestPeerMode1Body(&service)
	if string(onPeer) == string(offPeer) {
		t.Fatal("peer-directed mode1 body did not change under the default (fixed) tier")
	}
	assertPeerSkillPageReplacesTwoZeroCounts(t, "peer mode1", onPeer, offPeer, peerSkillPageSelectedResourceGroup)
}

// peerSkillPageTestPeerMode1Body 与生产队友路径同形：同一构造器 + 队友 ctx 标记。
func peerSkillPageTestPeerMode1Body(service *Service) []byte {
	ctx := withDungeonPartyPeerSkillPageTarget(context.Background(), 100)
	return service.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		ctx,
		nil,
		peerSkillPageTestCharacter(),
		true,
		77,
		4,
		currentSceneObjectContext,
		true,
	)
}

// TestPeerSkillPageEmitsCasterTargetSkillLevelEvent 钉住线上判读事件：
// `packet_log.txt` 里必须能直接 grep 到 caster / target / skill id / 发出的等级 / 生效的效果。
func TestPeerSkillPageEmitsCasterTargetSkillLevelEvent(t *testing.T) {
	setPeerSkillPageTier(t, "")
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	repos, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture has no repository group")
	}
	if err := repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{
		CharacterID: partyBuffCasterCharacterID,
		Skills:      partyBuffCasterSkills(),
	}); err != nil {
		t.Fatalf("seed caster skill rows: %v", err)
	}
	path := filepath.Join(t.TempDir(), "packet_log.party-buff-test.txt")
	logger, err := openPacketLogger(path)
	if err != nil {
		t.Fatalf("open packet logger: %v", err)
	}
	if logger == nil {
		t.Fatal("packet logger was nil")
	}
	service.packetLog = logger

	conn := follower.conn.(*bufferConn)
	conn.write.Reset()
	if err := service.sendDungeonPartyPeerActorState(follower, leader, "test_party_buff_event"); err != nil {
		t.Fatalf("send peer actor state: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("close packet logger: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read packet log: %v", err)
	}
	log := string(raw)
	buffLine := ""
	buffLines := 0
	for _, line := range strings.Split(log, "\n") {
		if !strings.HasPrefix(line, "EVENT kind=game-dungeon-party-buff-skill-level-send") {
			continue
		}
		buffLines++
		if strings.Contains(line, "skill_id=14 ") {
			buffLine = line
		}
	}
	if buffLine == "" {
		t.Fatalf("no game-dungeon-party-buff-skill-level-send event for skill 14 in the packet log:\n%s", log)
	}
	if buffLines != 3 {
		t.Fatalf("buff skill level events=%d, want one line per party-buff skill (14/21/24):\n%s", buffLines, log)
	}
	for _, want := range []string{
		"caster_char_id=" + partyBuffCasterCharacterID,
		"target_char_id=" + partyBuffTargetCharacterID,
		"skill_id=14",
		"level_sent=10",
		"level_without_fix=0",
		"effect_applied=physical_attack,magical_attack,independent_attack,strength,intelligence,hit_rate",
		"effect_owner=target_client_local_pvf",
		"env=" + peerSkillPageEnv,
		"mode=peer",
	} {
		if !strings.Contains(buffLine, want) {
			t.Fatalf("buff event line %q is missing %q", buffLine, want)
		}
	}
	for _, want := range []string{
		"EVENT kind=game-dungeon-party-skill-page-send",
		"caster_char_id=" + partyBuffCasterCharacterID,
		"target_char_id=" + partyBuffTargetCharacterID,
		"entry_count=16",
		"14:10",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("skill page delivery event is missing %q:\n%s", want, log)
		}
	}
	// 回退档一条都不许写（off 档不读技能仓储、不写事件）。
	setPeerSkillPageTier(t, peerSkillPageValueOff)
	offLog := filepath.Join(t.TempDir(), "packet_log.party-buff-off.txt")
	offLogger, err := openPacketLogger(offLog)
	if err != nil {
		t.Fatalf("open off packet logger: %v", err)
	}
	service.packetLog = offLogger
	conn.write.Reset()
	if err := service.sendDungeonPartyPeerActorState(follower, leader, "test_party_buff_event_off"); err != nil {
		t.Fatalf("send peer actor state (off): %v", err)
	}
	if err := offLogger.Close(); err != nil {
		t.Fatalf("close off packet logger: %v", err)
	}
	offRaw, err := os.ReadFile(offLog)
	if err != nil {
		t.Fatalf("read off packet log: %v", err)
	}
	offText := string(offRaw)
	if strings.Contains(offText, "game-dungeon-party-skill-page-send") ||
		strings.Contains(offText, "game-dungeon-party-buff-skill-level-send") ||
		strings.Contains(offText, "game-peer-skill-page-projected") {
		t.Fatalf("off tier wrote skill-page events:\n%s", offText)
	}
}
