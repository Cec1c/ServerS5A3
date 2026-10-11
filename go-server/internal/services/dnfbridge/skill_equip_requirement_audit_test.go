package dnfbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场形状（2026-09-28 线上库 + 线上 Script.pvf，逐字节取自 dump）：
//   - 101040493 圣耀救赎光剑 `equipment/character/swordman/weapon/beamsword/101040493.equ`
//     里有 `[required job skill] `[swordman]` 33 `[demonic swordman]` 33 `[at swordman]` 95`；
//   - 101020668 无惧者钝器 `equipment/character/swordman/weapon/club/101020668.equ`
//     里**整段不存在**（这就是操作者"钝器不掉、光剑掉"的物品级差别）。
const (
	auditBeamswordPath  = "equipment/character/swordman/weapon/beamsword/101040493.equ"
	auditClubPath       = "equipment/character/swordman/weapon/club/101020668.equ"
	auditBeamswordID    = 101040493
	auditClubID         = 101020668
	auditLightSwordID   = 33
	auditBluntMasteryID = 15
)

// auditWeaponTextSource 是 `equipRequirementTextSource` 的最小假档：只放两件武器的段落。
type auditWeaponTextSource map[string]string

func (s auditWeaponTextSource) ReadText(relativePath string) (string, error) {
	key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(relativePath), "\\", "/"))
	text, ok := s[key]
	if !ok {
		return "", os.ErrNotExist
	}
	return text, nil
}

func auditWeaponSource() auditWeaponTextSource {
	return auditWeaponTextSource{
		strings.ToLower(auditBeamswordPath): "[name]\n`圣耀救赎光剑`\n[required job skill]\n`[swordman]` 33 `[demonic swordman]` 33 `[at swordman]` 95\n[/required job skill]\n[durability]\n48\n",
		strings.ToLower(auditClubPath):      "[name]\n`无惧者钝器`\n[durability]\n45\n",
	}
}

// 开关默认档 = 开（多写日志、不改字节），回退档字面量 = off。
func TestSkillEquipRequirementAuditSwitchDefaultsOnAndHasAnExplicitRollback(t *testing.T) {
	t.Setenv(skillEquipRequirementAuditEnv, "")
	if !skillEquipRequirementAuditEnabled() {
		t.Fatal("empty env must keep the audit on (default = the new observable)")
	}
	for _, value := range []string{"0", "off", "false", "no", "OFF"} {
		t.Setenv(skillEquipRequirementAuditEnv, value)
		if skillEquipRequirementAuditEnabled() {
			t.Fatalf("rollback literal %q must disable the audit", value)
		}
	}
	t.Setenv(skillEquipRequirementAuditEnv, "1")
	if !skillEquipRequirementAuditEnabled() {
		t.Fatal("explicit 1 must keep the audit on")
	}
	if skillEquipRequirementAuditReferenceDrop != "off" {
		t.Fatalf("rollback literal = %q, want off", skillEquipRequirementAuditReferenceDrop)
	}
}

// 只有"PVF 里点名了掌握技能"的穿戴行进判读：光剑进、钝器不进。
func TestWornEquipmentRequiredSkillRowsOnlyPicksItemsThatNameASkill(t *testing.T) {
	entries := map[string]dnfrepo.EquipmentEntry{
		"12": {SlotIndex: 12, ItemID: auditBeamswordID, Extra: map[string]string{"pvf_path": auditBeamswordPath}},
		"11": {SlotIndex: 11, ItemID: auditClubID, Extra: map[string]string{"pvf_path": auditClubPath}},
	}
	rows, unreadable := wornEquipmentRequiredSkillRows(auditWeaponSource(), entries)
	if unreadable != 0 {
		t.Fatalf("unreadable=%d, want 0", unreadable)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%+v, want only the beamsword (the club declares no requirement)", rows)
	}
	row := rows[0]
	if row.Slot != 12 || row.ItemID != auditBeamswordID {
		t.Fatalf("row=%+v, want slot 12 item %d", row, auditBeamswordID)
	}
	if len(row.RequiredIDs) != 2 || row.RequiredIDs[0] != auditLightSwordID || row.RequiredIDs[1] != 95 {
		t.Fatalf("required ids=%v, want [33 95] (deduplicated, ascending)", row.RequiredIDs)
	}
}

// 判据是**存在性**：等级只影响数值。level 10 满足；等级 0 / 缺行都不满足。
func TestEvaluateEquipRequirementsIsPresenceOnly(t *testing.T) {
	rows := []loginEquipRequiredSkillRow{{
		Slot: 12, ItemID: auditBeamswordID, PVFPath: auditBeamswordPath, RequiredIDs: []int64{auditLightSwordID},
	}}
	served := evaluateEquipRequirements(rows, map[int64]int{auditLightSwordID: 10})
	if len(served) != 1 || !served[0].ServedOK || served[0].Missing != nil {
		t.Fatalf("level 10 must satisfy the presence-only requirement: %+v", served)
	}
	if got := loginEquipRequirementRowsText(served); got != "12:101040493:req[33]:served[33:10]:missing[-]:served" {
		t.Fatalf("rows text = %q", got)
	}
	for name, levels := range map[string]map[int64]int{
		"absent":     {},
		"zero":       {auditLightSwordID: 0},
		"other only": {auditBluntMasteryID: 27},
	} {
		missing := evaluateEquipRequirements(rows, levels)
		if len(missing) != 1 || missing[0].ServedOK {
			t.Fatalf("%s: %+v, want an unsatisfied row", name, missing)
		}
		if got := loginEquipRequirementRowsText(missing); got != "12:101040493:req[33]:served[-]:missing[33]:missing" {
			t.Fatalf("%s: rows text = %q", name, got)
		}
	}
	// 钝器那种"没有要求"的行走不进判读，所以它永远不会被判成 missing。
	if got := loginEquipRequirementRowsText(nil); got != "none" {
		t.Fatalf("empty rows text = %q, want none", got)
	}
}

// `[required job skill]` 是**按职业分行的候选表**（现场光剑：`[swordman] 33
// [demonic swordman] 33 [at swordman] 95`）。角色只拥有自己 job 列表里的技能，
// 所以"任意一个被下发"就是满足；另一个职业那一份落在 missing[] 里只是信息。
func TestEvaluateEquipRequirementsTreatsTheJobListAsAlternatives(t *testing.T) {
	rows := []loginEquipRequiredSkillRow{{
		Slot: 12, ItemID: auditBeamswordID, PVFPath: auditBeamswordPath, RequiredIDs: []int64{33, 95},
	}}
	statuses := evaluateEquipRequirements(rows, map[int64]int{33: 10})
	if len(statuses) != 1 || !statuses[0].ServedOK {
		t.Fatalf("skill 33 alone must satisfy the row: %+v", statuses)
	}
	if got := loginEquipRequirementRowsText(statuses); got != "12:101040493:req[33,95]:served[33:10]:missing[95]:served" {
		t.Fatalf("rows text = %q", got)
	}
}

func TestServedSkillLevelsTextIsDeterministic(t *testing.T) {
	if got := servedSkillLevelsText(nil); got != "none" {
		t.Fatalf("empty = %q", got)
	}
	got := servedSkillLevelsText(map[int64]int{33: 10, 1: 1, 27: 27, 4: 0})
	if got != "1:1,27:27,33:10" {
		t.Fatalf("served levels text = %q, want 1:1,27:27,33:10 (ascending, level 0 dropped)", got)
	}
}

// 开关关掉之后，登录期一条审计行都不许出现（回退档必须逐字节回到今天）。
func TestAuditLoginEquipRequirementsIsSilentWhenTheRollbackLiteralIsSet(t *testing.T) {
	t.Setenv(skillEquipRequirementAuditEnv, skillEquipRequirementAuditReferenceDrop)
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{packetLog: logger}
	session := &gameSession{connID: "audit-off"}
	service.auditLoginEquipRequirements(
		session,
		context.Background(),
		dnfrepo.CharacterRecord{CharacterID: "6", Job: "0"},
		currentSceneSkillInfoProjection{characterID: "6", job: 0, learnedLevels: map[int64]int{auditLightSwordID: 10}},
		"test_audit_off",
	)
	log := readPeerEndpointTestLog(t, logger, logPath)
	for _, event := range []string{skillEquipRequirementAuditEvent, skillEquipRequirementMissingEvent, skillEquipRequirementSkippedEvent} {
		if strings.Contains(log, event) {
			t.Fatalf("rollback literal must not log %s; log=\n%s", event, log)
		}
	}
}

// 默认档：仓储/PVF 读不到时只记 skipped，绝不改任何下发内容、也绝不报错。
func TestAuditLoginEquipRequirementsRecordsSkippedInsteadOfFailing(t *testing.T) {
	t.Setenv(skillEquipRequirementAuditEnv, "")
	logger, logPath := newPeerEndpointTestLogger(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	service := &Service{
		packetLog:          logger,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{connID: "audit-skip"}
	service.auditLoginEquipRequirements(
		session,
		context.Background(),
		dnfrepo.CharacterRecord{CharacterID: "no-equipment-row", Job: "0"},
		currentSceneSkillInfoProjection{characterID: "no-equipment-row", job: 0},
		"test_audit_skip",
	)
	log := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(log, skillEquipRequirementAuditEvent) || !strings.Contains(log, "verdict=no_worn_rows") {
		t.Fatalf("a character without worn rows must still produce the audit line; log=\n%s", log)
	}
}

// 真档 PVF 回归（`DNFBRIDGE_REAL_PVF_SMOKE` 指向线上同源 Script.pvf 时才跑）：
// 把操作者 A/B 的物品级差别钉死 —— 光剑点名 33（光剑掌握），钝器不点名；
// 并且 33 就是 `[name] 光剑掌握`，在 grow_type 分支 1 上的可学上限是 10
// （角色 6 的 grow_type=0x21 ⇒ 分支 1 ⇒ 上限 10，库里正好 10）。
func TestRealPVFBeamswordNamesLightSwordMasteryAndTheClubNamesNothing(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	entries := map[string]dnfrepo.EquipmentEntry{
		"12": {SlotIndex: 12, ItemID: auditBeamswordID, Extra: map[string]string{"pvf_path": auditBeamswordPath}},
		"11": {SlotIndex: 11, ItemID: auditClubID, Extra: map[string]string{"pvf_path": auditClubPath}},
	}
	rows, unreadable := wornEquipmentRequiredSkillRows(archive, entries)
	if unreadable != 0 {
		t.Fatalf("unreadable rows=%d, want 0", unreadable)
	}
	if len(rows) != 1 || rows[0].Slot != 12 || rows[0].ItemID != auditBeamswordID {
		t.Fatalf("rows=%+v, want only the beamsword on slot 12", rows)
	}
	if len(rows[0].RequiredIDs) == 0 || rows[0].RequiredIDs[0] != auditLightSwordID {
		t.Fatalf("beamsword required ids=%v, want 33 first", rows[0].RequiredIDs)
	}
	t.Logf("beamsword %d [%s]=%v", auditBeamswordID, skillEquipRequirementSection, rows[0].RequiredIDs)

	mastery, _, err := readInitialPVFText(archive, "skill/Swordman/EquipLightSword.skl")
	if err != nil {
		t.Fatalf("read EquipLightSword.skl: %v", err)
	}
	if !strings.Contains(mastery, "`光剑掌握`") {
		t.Fatalf("skill/Swordman/EquipLightSword.skl is not 光剑掌握:\n%s", firstLines(mastery, 4))
	}
	// `[growtype maximum level] 0 10 0 0 0 10` ⇒ 分支 1 的上限是 10。
	if !strings.Contains(mastery, "[growtype maximum level]\n0 10 0 0 0 10") {
		t.Fatalf("光剑掌握 growtype cap changed:\n%s", firstLines(mastery, 30))
	}
	t.Logf("光剑掌握 = skill/Swordman/EquipLightSword.skl (id %d), growtype cap `0 10 0 0 0 10`", auditLightSwordID)
}

func TestRealPVFBeamswordAuditLogNamesTheSkillAndTheServedLevel(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	t.Setenv(skillEquipRequirementAuditEnv, "")

	// 走**真实登录边界**（`sendCurrentSceneSkillInfo` → build → audit），而不是直接调审计：
	// 这样把 `scene_skill_info.go` 里的调用点用 -overlay 抹掉时，本用例必须变红。
	run := func(t *testing.T, skills map[int64]dnfrepo.SkillState, layout dnfrepo.SkillLayout, wantMissing bool) string {
		t.Helper()
		logger, logPath := newPeerEndpointTestLogger(t)
		repositories := dnfrepomemory.NewMemoryGroup()
		if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
			CharacterID: "6",
			Entries: map[string]dnfrepo.EquipmentEntry{
				"12": {SlotIndex: 12, ItemID: auditBeamswordID, Extra: map[string]string{"pvf_path": auditBeamswordPath}},
				"11": {SlotIndex: 11, ItemID: auditClubID, Extra: map[string]string{"pvf_path": auditClubPath}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Skill.Save(context.Background(), dnfrepo.SkillRecord{
			CharacterID: "6",
			Skills:      skills,
			Layouts:     map[int]dnfrepo.SkillLayout{currentSkillInfoTreeIndex: layout},
		}); err != nil {
			t.Fatal(err)
		}
		catalog, err := buildSkillCatalogFromSource(context.Background(), archive)
		if err != nil {
			t.Fatal(err)
		}
		service := &Service{
			packetLog:               logger,
			initialEquipmentArchive: archive,
			skillCatalog:            catalog,
			repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
		}
		session := &gameSession{connID: "audit-real", conn: &bufferConn{}}
		character := dnfrepo.CharacterRecord{
			CharacterID: "6",
			Job:         "0",
			Level:       90,
			Stats:       map[string]int64{"grow_type": 0x21},
		}
		if err := service.sendCurrentSceneSkillInfo(session, context.Background(), character, "test_audit_real_pvf"); err != nil {
			t.Fatal(err)
		}
		log := readPeerEndpointTestLog(t, logger, logPath)
		if !strings.Contains(log, skillEquipRequirementAuditEvent) {
			t.Fatalf("audit line missing; log=\n%s", log)
		}
		if got := strings.Contains(log, skillEquipRequirementMissingEvent); got != wantMissing {
			t.Fatalf("missing event present=%t want=%t; log=\n%s", got, wantMissing, log)
		}
		return log
	}

	// 现场形状：33（光剑掌握）Lv10 + 27（钝器掌握）Lv27，两者都在 layout 里有槽位。
	log := run(t,
		map[int64]dnfrepo.SkillState{
			auditLightSwordID:   {Level: 10, Enabled: true},
			auditBluntMasteryID: {Level: 27, Enabled: true},
		},
		dnfrepo.SkillLayout{54: auditLightSwordID, 57: auditBluntMasteryID},
		false,
	)
	// 真档光剑的要求表是 `[swordman] 33 [demonic swordman] 33 [at swordman] 95`：
	// 本角色（job 0）只可能有 33，所以 `served[33:10]` 就够了；95 是另一个职业那一份，
	// 它出现在 missing[] 里是**信息**而不是判负。
	if !strings.Contains(log, "rows=12:101040493:req[33,95]:served[33:10]:missing[95]:served") ||
		!strings.Contains(log, "verdict=all_requirements_served") {
		t.Fatalf("the beamsword row must name skill 33 and its served level; log=\n%s", log)
	}
	if !strings.Contains(log, "served_skill_levels=") || !strings.Contains(log, "33:10") {
		t.Fatalf("the audit must print the served level vector; log=\n%s", log)
	}
	if !strings.Contains(log, "kind=game-main-skill-info-send") {
		t.Fatalf("the audit must stay attached to the skill page send; log=\n%s", log)
	}
	missingLog := run(t,
		map[int64]dnfrepo.SkillState{auditBluntMasteryID: {Level: 27, Enabled: true}},
		dnfrepo.SkillLayout{57: auditBluntMasteryID},
		true,
	)
	if !strings.Contains(missingLog, "rows=12:101040493:req[33,95]:served[-]:missing[33,95]:missing") ||
		!strings.Contains(missingLog, "required_skill_ids=[33 95]") ||
		!strings.Contains(missingLog, "verdict=worn_item_requirement_not_in_served_skill_page") {
		t.Fatalf("missing event must name the required ids and the verdict; log=\n%s", missingLog)
	}
}

func firstLines(text string, count int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, "\n") + " …"
}

// 该文件同时是"下个实测判读串"的钉子：事件名与字段名必须保持可 grep。
func TestSkillEquipRequirementAuditEventNamesStayGreppable(t *testing.T) {
	if !strings.Contains(skillEquipRequirementAuditEvent, "game-login-equip-requirement") ||
		!strings.Contains(skillEquipRequirementMissingEvent, "missing") ||
		!strings.Contains(skillEquipRequirementSkippedEvent, "skipped") {
		t.Fatalf("event names drifted: %q %q %q",
			skillEquipRequirementAuditEvent, skillEquipRequirementMissingEvent, skillEquipRequirementSkippedEvent)
	}
	if filepath.Ext(skillEquipRequirementAuditEnv) != "" || !strings.HasPrefix(skillEquipRequirementAuditEnv, "DNFBRIDGE_") {
		t.Fatalf("env name drifted: %q", skillEquipRequirementAuditEnv)
	}
}
