// 本文件验证 DNFBRIDGE_PEER_SKILL_PAGE 开关与「队友 USERINFO 技能页块」的编码边界。
// 布局与分页规则见 peer_skill_page.go 文件头（A21 UserInfoSubtype1/3Builder + 本仓库
// scene_skill_protocol.go 的既有两页同步规则）。
package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	peerSkillPageTestCharacterID = "77"
	peerSkillPageTestTreeIndex   = 9
)

// preservePeerSkillPageEnv 记录并恢复环境变量，避免测试污染同包其他用例。
func preservePeerSkillPageEnv(t *testing.T) {
	t.Helper()
	original, hadOriginal := os.LookupEnv(peerSkillPageEnv)
	t.Cleanup(func() {
		if hadOriginal {
			_ = os.Setenv(peerSkillPageEnv, original)
			return
		}
		_ = os.Unsetenv(peerSkillPageEnv)
	})
}

func mustSetPeerSkillPageEnv(t *testing.T, value string) {
	t.Helper()
	if err := os.Setenv(peerSkillPageEnv, value); err != nil {
		t.Fatalf("set %s=%q: %v", peerSkillPageEnv, value, err)
	}
}

// peerSkillPageTestSkills 覆盖三条边界：有效条目、level=0 的条目、以及 level>0 的条目。
func peerSkillPageTestSkills() map[int64]dnfrepo.SkillState {
	return map[int64]dnfrepo.SkillState{
		10: {Level: 3, Enabled: true},
		20: {Level: 0, Enabled: true},
		30: {Level: 7, Enabled: true},
	}
}

// peerSkillPageTestPages 是按文档化分页规则（page0 = page1 = 全部 level>0 技能，升序）
// 得到的 block 后半段：page0 + page1。
func peerSkillPageTestPages() []byte {
	return []byte{
		2, 10, 0, 3, 30, 0, 7, // page0：count=2，(10,3),(30,7)
		2, 10, 0, 3, 30, 0, 7, // page1：与 page0 同内容
	}
}

func peerSkillPageTestLegacyRepo() fakeLegacyUserInfoRepo {
	return fakeLegacyUserInfoRepo{rows: map[string][]dnfrepo.LegacyUserInfoRow{
		"legacy_character_subtype1_fields": {{
			"skill_tree_index":        "9",
			"equipped_creature_level": "11",
		}},
	}}
}

func peerSkillPageTestCharacter() dnfrepo.CharacterRecord {
	return dnfrepo.CharacterRecord{
		CharacterID: peerSkillPageTestCharacterID,
		Name:        "hero",
		Job:         "11",
		Level:       56,
		Stats:       map[string]int64{"exp": 12345},
	}
}

// peerSkillPageTestService 组装一个带内存技能仓储的 Service；skills 为 nil 时技能仓储为空。
func peerSkillPageTestService(t *testing.T, skills map[int64]dnfrepo.SkillState) Service {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	if skills != nil {
		if err := repos.Skill.Save(context.Background(), dnfrepo.SkillRecord{
			CharacterID: peerSkillPageTestCharacterID,
			Skills:      skills,
		}); err != nil {
			t.Fatalf("seed skill record: %v", err)
		}
	}
	return Service{
		characterStats: testCharacterStatTable(t),
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repos, true
		},
	}
}

func peerSkillPageTestSubtype1Body(service *Service) []byte {
	return service.buildCSharpSelectedUserInfoBody(
		context.Background(),
		nil,
		peerSkillPageTestLegacyRepo(),
		1,
		peerSkillPageTestCharacter(),
		true,
		77,
		"hero",
	)
}

func peerSkillPageTestMode1Body(service *Service) []byte {
	return service.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		context.Background(),
		nil,
		peerSkillPageTestCharacter(),
		true,
		77,
		4,
		currentSceneObjectContext,
		true,
	)
}

func peerSkillPageTestMode3Body(service *Service) []byte {
	return service.buildCurrentSelectedUserInfoMode3BodyWithAdventureLevelInContext(
		context.Background(),
		nil,
		nil,
		peerSkillPageTestCharacter(),
		true,
		77,
		4,
		currentSceneObjectContext,
	)
}

// assertPeerSkillPageReplacesTwoZeroCounts 证明 on 档 body 只是把 off 档 body 里
// 那两个硬编码 0x00 计数字节替换成两页技能：块必须在整包里唯一、前面紧跟调用点原有的
// 位置字节（preceding），把块换回两个 0x00 后必须与 off 档 body 逐字节相同。
func assertPeerSkillPageReplacesTwoZeroCounts(t *testing.T, label string, onBody, offBody []byte, preceding byte) {
	t.Helper()
	pages := peerSkillPageTestPages()
	if count := bytes.Count(onBody, pages); count != 1 {
		t.Fatalf("%s: skill page block appears %d times, want exactly 1: %x", label, count, onBody)
	}
	start := bytes.Index(onBody, pages)
	if start == 0 || onBody[start-1] != preceding {
		t.Fatalf("%s: page block is not preceded by %#x: %x", label, preceding, onBody)
	}
	if got, want := len(onBody), len(offBody)-2+len(pages); got != want {
		t.Fatalf("%s: on body length = %d, want off(%d) - 2 count bytes + %d page bytes = %d",
			label, got, len(offBody), len(pages), want)
	}
	stripped := make([]byte, 0, len(offBody))
	stripped = append(stripped, onBody[:start]...)
	stripped = append(stripped, 0, 0)
	stripped = append(stripped, onBody[start+len(pages):]...)
	if !bytes.Equal(stripped, offBody) {
		t.Fatalf("%s: on body with the two skill pages restored to 0x00 counts is not byte-identical to the off body:\non=%x\noff=%x",
			label, stripped, offBody)
	}
}

func TestPeerSkillPageEnabledSwitchValues(t *testing.T) {
	preservePeerSkillPageEnv(t)

	// 默认（环境变量完全不设置）必须是**修复档 peer**：
	// 队友 actor body 写真实技能页（现场症状的修法），而 subtype1/mode3 两个未收窄调用点
	// 仍然是老口径（peerSkillPageEnabled() == false，逐字节不变）。
	if err := os.Unsetenv(peerSkillPageEnv); err != nil {
		t.Fatal(err)
	}
	if got := peerSkillPageModeNow(); got != peerSkillPageModePeer {
		t.Fatalf("%s unset must default to the fixed tier peer, got %s", peerSkillPageEnv, got)
	}
	if peerSkillPageEnabled() {
		t.Fatalf("%s unset must keep subtype1/mode3 byte-identical (all tier only)", peerSkillPageEnv)
	}

	// 回退字面量家族：一律逐字节回到改动前（三个调用点都不写技能页）。
	for _, value := range []string{"off", "OFF", "off ", "0", "false", "FALSE", "no", "No", "legacy", "disable", "disabled", "none"} {
		mustSetPeerSkillPageEnv(t, value)
		if got := peerSkillPageModeNow(); got != peerSkillPageModeOff {
			t.Fatalf("%s=%q must select the rollback tier off, got %s", peerSkillPageEnv, value, got)
		}
		if peerSkillPageEnabled() {
			t.Fatalf("%s=%q must stay disabled", peerSkillPageEnv, value)
		}
	}

	// 全量档家族：三个调用点都写技能页（A/B 用）。
	for _, value := range []string{"all", "ALL", " all ", "on", "ON", "On", "1", "true", "TRUE", "yes", "Yes", "  on  "} {
		mustSetPeerSkillPageEnv(t, value)
		if got := peerSkillPageModeNow(); got != peerSkillPageModeAll {
			t.Fatalf("%s=%q must select the all tier, got %s", peerSkillPageEnv, value, got)
		}
		if !peerSkillPageEnabled() {
			t.Fatalf("%s=%q must enable the skill pages", peerSkillPageEnv, value)
		}
	}

	// 显式写 peer + 无法识别的取值：都落在**默认档（修复档）**上 ——
	// 与同包默认开启族（party_dungeon_peer_position.go）同口径，只有显式 off 才回退。
	for _, value := range []string{"peer", "PEER", " peer ", "2", "enabled", "whatever"} {
		mustSetPeerSkillPageEnv(t, value)
		if got := peerSkillPageModeNow(); got != peerSkillPageModePeer {
			t.Fatalf("%s=%q must fall back to the default fixed tier peer, got %s", peerSkillPageEnv, value, got)
		}
	}
	if got := readPeerSkillPageMode("").String(); got != peerSkillPageValuePeer {
		t.Fatalf("empty value renders as %q, want %q", got, peerSkillPageValuePeer)
	}
	if got := peerSkillPageModeOff.String(); got != peerSkillPageValueOff {
		t.Fatalf("off tier renders as %q, want %q", got, peerSkillPageValueOff)
	}
	if got := peerSkillPageModeAll.String(); got != peerSkillPageValueAll {
		t.Fatalf("all tier renders as %q, want %q", got, peerSkillPageValueAll)
	}
}

func TestPeerSkillPageBlockEncodingSkipsZeroLevelAndSortsAscending(t *testing.T) {
	var w packetWriter
	writePeerSkillPageBlock(&w, peerSkillPageTestTreeIndex, peerSkillPageTestSkills())
	body := w.bytes()

	want := append([]byte{peerSkillPageTestTreeIndex}, peerSkillPageTestPages()...)
	if !bytes.Equal(body, want) {
		t.Fatalf("skill page block = %x, want %x", body, want)
	}
	// 文档化分页规则是「两页同内容」，所以长度 = tree index(1) + page0(1+2*3) + page1(1+2*3)。
	if got, wantLen := len(body), 1+(1+2*3)+(1+2*3); got != wantLen {
		t.Fatalf("skill page block length = %d, want %d", got, wantLen)
	}
	if got := body[1]; got != 2 {
		t.Fatalf("page0 count = %d, want 2 (level=0 entry must be skipped)", got)
	}
	if got := binary.LittleEndian.Uint16(body[2:4]); got != 10 {
		t.Fatalf("page0 first skill id = %d, want 10 (ascending)", got)
	}
	if got := binary.LittleEndian.Uint16(body[5:7]); got != 30 {
		t.Fatalf("page0 second skill id = %d, want 30 (ascending, 20 skipped)", got)
	}
	if got := body[1+(1+2*3)]; got != 2 {
		t.Fatalf("page1 count = %d, want 2 (same content as page0)", got)
	}
}

func TestPeerSkillPageBlockWithNoLearnedSkillsKeepsEmptyPages(t *testing.T) {
	for _, skills := range []map[int64]dnfrepo.SkillState{
		nil,
		{},
		{10: {Level: 0}, 11: {Level: -2}},
	} {
		var w packetWriter
		writePeerSkillPageBlock(&w, peerSkillPageSelectedResourceGroup, skills)
		want := []byte{peerSkillPageSelectedResourceGroup, 0, 0}
		if body := w.bytes(); !bytes.Equal(body, want) {
			t.Fatalf("empty skill page block for %v = %x, want %x", skills, body, want)
		}
	}
}

func TestPeerSkillPageEntriesSaturatesLevelAndSkipsUnencodableIDs(t *testing.T) {
	entries := peerSkillPageEntries(map[int64]dnfrepo.SkillState{
		-1:      {Level: 5},   // 负 id 无法编码 → 跳过
		7:       {Level: 300}, // > u8 → 饱和到 255（不回绕）
		0x10000: {Level: 5},   // > u16 → 跳过
		9:       {Level: 0},   // level 0 → 跳过
		65535:   {Level: 1},
	})
	want := []peerSkillPageEntry{{SkillID: 7, Level: 255}, {SkillID: 65535, Level: 1}}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v, want %+v", entries, want)
	}
	for index := range want {
		if entries[index] != want[index] {
			t.Fatalf("entry[%d] = %+v, want %+v", index, entries[index], want[index])
		}
	}
}

func TestPeerSkillPageBlockCapsAt255Entries(t *testing.T) {
	skills := make(map[int64]dnfrepo.SkillState, 300)
	for id := int64(1); id <= 300; id++ {
		skills[id] = dnfrepo.SkillState{Level: 1, Enabled: true}
	}
	var w packetWriter
	writePeerSkillPageBlock(&w, peerSkillPageSelectedResourceGroup, skills)
	body := w.bytes()

	const pageWireLen = 1 + peerSkillPageMaxEntries*3
	if got, want := len(body), 1+pageWireLen*2; got != want {
		t.Fatalf("capped skill page block length = %d, want %d", got, want)
	}
	if got := body[1]; got != 255 {
		t.Fatalf("page0 count = %d, want 255 (cap)", got)
	}
	if got := body[1+pageWireLen]; got != 255 {
		t.Fatalf("page1 count = %d, want 255 (cap)", got)
	}
	// 升序截断：第一页的最后一条必须是 skill 255，不能溢出到 256+。
	page0End := 1 + pageWireLen
	last := body[page0End-3 : page0End]
	if !bytes.Equal(last, []byte{0xff, 0x00, 0x01}) {
		t.Fatalf("page0 last entry = %x, want 0xff0001 (skill 255 level 1)", last)
	}
}

func TestPeerSkillPageSubtype1BodyStaysByteIdenticalWhenOff(t *testing.T) {
	preservePeerSkillPageEnv(t)
	if err := os.Unsetenv(peerSkillPageEnv); err != nil {
		t.Fatal(err)
	}

	seeded := peerSkillPageTestService(t, peerSkillPageTestSkills())
	baseline := peerSkillPageTestSubtype1Body(&seeded)
	// 改动前的现行为：tree index 之后就是两个 0x00（见 userinfo_selected_subtype1.go:57-59 的旧实现）。
	if got := baseline[110]; got != peerSkillPageTestTreeIndex {
		t.Fatalf("subtype1 skill_tree_index = %d, want %d", got, peerSkillPageTestTreeIndex)
	}
	if baseline[111] != 0 || baseline[112] != 0 {
		t.Fatalf("subtype1 skill page counts = %d/%d, want 0/0 at offsets 111/112", baseline[111], baseline[112])
	}
	if got := baseline[113]; got != 11 {
		t.Fatalf("subtype1 equipped creature level = %d, want 11", got)
	}

	for _, value := range []string{"off", "0", "false", "no"} {
		mustSetPeerSkillPageEnv(t, value)
		if body := peerSkillPageTestSubtype1Body(&seeded); !bytes.Equal(body, baseline) {
			t.Fatalf("%s=%q changed the subtype1 body:\n got=%x\nwant=%x", peerSkillPageEnv, value, body, baseline)
		}
	}

	// 技能仓储里存在真实技能行时，off 档仍然不许改变任何字节。
	empty := peerSkillPageTestService(t, nil)
	if body := peerSkillPageTestSubtype1Body(&empty); !bytes.Equal(body, baseline) {
		t.Fatalf("skill rows leaked into the off-mode subtype1 body:\n seeded=%x\n empty=%x", baseline, body)
	}
}

func TestPeerSkillPageSubtype1BodyCarriesSkillPagesWhenOn(t *testing.T) {
	preservePeerSkillPageEnv(t)
	if err := os.Unsetenv(peerSkillPageEnv); err != nil {
		t.Fatal(err)
	}
	service := peerSkillPageTestService(t, peerSkillPageTestSkills())
	offBody := peerSkillPageTestSubtype1Body(&service)

	mustSetPeerSkillPageEnv(t, "on")
	onBody := peerSkillPageTestSubtype1Body(&service)

	if got := onBody[110]; got != peerSkillPageTestTreeIndex {
		t.Fatalf("on subtype1 skill_tree_index = %d, want %d at offset 110", got, peerSkillPageTestTreeIndex)
	}
	if !bytes.Equal(onBody[111:125], peerSkillPageTestPages()) {
		t.Fatalf("on subtype1 skill pages = %x, want %x", onBody[111:125], peerSkillPageTestPages())
	}
	if got := onBody[125]; got != 11 {
		t.Fatalf("on subtype1 equipped creature level = %d, want 11", got)
	}
	assertPeerSkillPageReplacesTwoZeroCounts(t, "subtype1", onBody, offBody, peerSkillPageTestTreeIndex)
}

func TestPeerSkillPageMode1AndMode3BodiesCarrySkillPagesWhenOn(t *testing.T) {
	preservePeerSkillPageEnv(t)
	if err := os.Unsetenv(peerSkillPageEnv); err != nil {
		t.Fatal(err)
	}
	service := peerSkillPageTestService(t, peerSkillPageTestSkills())
	empty := peerSkillPageTestService(t, nil)

	for _, tc := range []struct {
		label string
		build func(*Service) []byte
	}{
		{"mode1", peerSkillPageTestMode1Body},
		{"mode3", peerSkillPageTestMode3Body},
	} {
		t.Run(tc.label, func(t *testing.T) {
			mustSetPeerSkillPageEnv(t, "off")
			offBody := tc.build(&service)
			// off 档：选中资源组字节之后仍是两个 0x00，且技能行不参与。
			if body := tc.build(&empty); !bytes.Equal(body, offBody) {
				t.Fatalf("off %s body differs when skill rows exist", tc.label)
			}

			mustSetPeerSkillPageEnv(t, "on")
			onBody := tc.build(&service)
			assertPeerSkillPageReplacesTwoZeroCounts(t, tc.label, onBody, offBody, peerSkillPageSelectedResourceGroup)
		})
	}
}

func TestPeerSkillPageLoadFallsBackToReaderCharacterID(t *testing.T) {
	preservePeerSkillPageEnv(t)
	service := peerSkillPageTestService(t, peerSkillPageTestSkills())
	reader := csharpLegacyUserInfoReader{
		ctx:         context.Background(),
		characterID: peerSkillPageTestCharacterID,
		service:     &service,
	}
	// 空 characterID 时按 reader 登记的 charID 读（mode1/mode3 尾块没有角色记录参数）。
	if got := len(reader.loadPeerSkillPageSkills("")); got != len(peerSkillPageTestSkills()) {
		t.Fatalf("fallback skill rows = %d, want %d", got, len(peerSkillPageTestSkills()))
	}
	if got := reader.loadPeerSkillPageSkills(peerSkillPageTestCharacterID); len(got) == 0 {
		t.Fatal("explicit character id returned no skill rows")
	}
	// 未知角色 / 无仓储组都降级为 nil（空技能页），不 panic。
	if got := reader.loadPeerSkillPageSkills("9999"); got != nil {
		t.Fatalf("unknown character skill rows = %v, want nil", got)
	}
	var bare csharpLegacyUserInfoReader
	if got := bare.loadPeerSkillPageSkills(peerSkillPageTestCharacterID); got != nil {
		t.Fatalf("service-less reader skill rows = %v, want nil", got)
	}
}
