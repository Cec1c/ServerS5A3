package gmtool

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// P3 / J8：SP/TP 绝对值设置（POST /api/characters/{id}/sp/set）
//
// 夹具说明（**没有发明数据**）：
//   - 小数字夹具 `p3SmallProgressionSource` 的三份文件格式与真档逐段一致
//     （真档 dump：`Etc/spTable.etc` 是 `[sp table]`/`[tp table]` 的 "等级 值" 对，
//     `character/ExpTable.tbl` 是一串递增整数，`n_quest/questParameter.etc` 见
//     progression 包既有夹具）——数值取小是为了让断言可读；
//   - 真值由 `TestRealPVFSpAbsoluteMatchesTheDumpedSkillPointTable` 在真档上钉死
//     （Lv90 → 累计 SP/TP），那条用例需要 DNFBRIDGE_REAL_PVF_SMOKE。
// ---------------------------------------------------------------------------

// p3SmallProgressionSource：Lv5 的累计 SP = 10+30+30+40+50 = 160，累计 TP = 2+1+1 = 4。
func p3SmallProgressionSource() readonlyTestSource {
	return readonlyTestSource{
		progression.ExperienceTablePath: "100 250 500 900 1500\n",
		progression.SkillPointTablePath: "[sp table]\n1 10\n2 30\n3 30\n4 40\n5 50\n[/sp table]\n" +
			"[tp table]\n3 2\n4 1\n5 1\n[/tp table]\n",
		progression.QuestParameterPath: "[difficulty]\n`N` 100\n[/difficulty]\n" +
			"[exp reward table]\n100 -1\n[green level penalty]\n80\n[grey level penalty]\n30\n",
	}
}

// newP3SPHarness 建一个「角色 + 技能账 + 成长表」都在位的写路由测试台。
// cfg.RequireOffline 保持零值 = 默认档（不检查在线）。
func newP3SPHarness(t *testing.T, source dnfpvf.Source, character dnfrepo.CharacterRecord, skill *dnfrepo.SkillRecord) *gmtoolWriteHarness {
	t.Helper()
	harness := newGmtoolWriteHarness(t, nil)
	harness.server.deps.PVF = func() (dnfpvf.Source, error) { return source, nil }
	harness.source = source
	character.CharacterID = "1"
	if err := harness.group.Character.Save(context.Background(), character); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if skill != nil {
		record := *skill
		record.CharacterID = "1"
		if err := harness.group.Skill.Save(context.Background(), record); err != nil {
			t.Fatalf("save skill: %v", err)
		}
	}
	return harness
}

func p3Character(level int, bonusSP, bonusTP int64) dnfrepo.CharacterRecord {
	return dnfrepo.CharacterRecord{
		AccountID: "acc-1", Name: "tester", Job: "11", Level: level,
		Stats: map[string]int64{"grow_type": 0, "bonus_sp": bonusSP, "bonus_tp": bonusTP},
	}
}

func (h *gmtoolWriteHarness) setSpAbsolute(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/1/sp/set", map[string]string{"id": "1"}, body, h.server.handleSetSpAbsolute)
}

func (h *gmtoolWriteHarness) skill(t *testing.T) (dnfrepo.SkillRecord, bool) {
	t.Helper()
	return mustLoadSkill(t, h.group, "1")
}

func mustLoadSkill(t *testing.T, group dnfrepo.Group, characterID string) (dnfrepo.SkillRecord, bool) {
	t.Helper()
	record, found, err := group.Skill.Load(context.Background(), characterID)
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	return record, found
}

func (h *gmtoolWriteHarness) characterRecord(t *testing.T) dnfrepo.CharacterRecord {
	t.Helper()
	record, found, err := h.group.Character.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	return record
}

// (计划书测试 1) 绝对值设置保留"已花点数"：spent = Total - Remaining 不变。
func TestSpSetAbsolutePreservesSpentPoints(t *testing.T) {
	// PVF：Lv5 总量 SP=160 / TP=4。旧账本：bonus 20 → Total 180、Remaining 60（已花 120）；
	// bonus_tp 1 → Total 5、Remaining 5（已花 0）。
	harness := newP3SPHarness(t, p3SmallProgressionSource(), p3Character(5, 20, 1), &dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{TotalSP: 180, RemainingSP: 60, TotalTP: 5, RemainingTP: 5, SyncedLevel: 5},
	})

	status, response := harness.setSpAbsolute(t, map[string]any{"bonus_sp": 40, "bonus_tp": 3})
	requireOK(t, status, response)

	// 160 + 40 = 200，已花 120 → 剩余 80；4 + 3 = 7，已花 0 → 剩余 7。
	want := map[string]float64{
		"totalSp": 200, "remainingSp": 80, "bonusSp": 40,
		"totalTp": 7, "remainingTp": 7, "bonusTp": 3,
	}
	for key, value := range want {
		if response[key] != value {
			t.Fatalf("%s = %v, want %v (完整响应 %+v)", key, response[key], value, response)
		}
	}
	record, found := harness.skill(t)
	if !found {
		t.Fatal("技能账在写入后应当仍然存在")
	}
	if record.Points.TotalSP-record.Points.RemainingSP != 120 {
		t.Fatalf("已花 SP = %d, want 120（绝对值设置必须保留已花点数）",
			record.Points.TotalSP-record.Points.RemainingSP)
	}
	if statInt(harness.characterRecord(t), "bonus_sp") != 40 {
		t.Fatalf("库里的 bonus_sp = %d, want 40", statInt(harness.characterRecord(t), "bonus_sp"))
	}
	fields := harness.eventFields(t, "gm-sp-set")
	if fields["result"] != "applied" || fields["old_bonus_sp"] != int64(20) || fields["bonus_sp"] != int64(40) ||
		fields["account_id"] != "acc-1" || fields["character_id"] != "1" {
		t.Fatalf("审计事件字段不完整: %+v", fields)
	}
}

// (计划书测试 2) 不变量门：会让剩余点数为负的设置被拒绝，且零写入。
func TestSpSetRejectsNegativeRemainingWithZeroWrites(t *testing.T) {
	character := p3Character(5, 20, 1)
	skill := dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{TotalSP: 180, RemainingSP: 10, TotalTP: 5, RemainingTP: 5, SyncedLevel: 5},
	}
	harness := newP3SPHarness(t, p3SmallProgressionSource(), character, &skill)
	beforeCharacter := harness.characterRecord(t)
	beforeSkill, _ := harness.skill(t)

	// 已花 170，而 PVF+0 = 160 → 剩余 -10。
	status, response := harness.setSpAbsolute(t, map[string]any{"bonus_sp": 0, "bonus_tp": 1})
	if message := requireFailure(t, status, response); !strings.Contains(message, "剩余点数") {
		t.Fatalf("拒绝文案 = %q, want 提到剩余点数", message)
	}
	requireRecordUnchanged(t, harness, beforeCharacter, beforeSkill)
	fields := harness.eventFields(t, "gm-sp-set-rejected")
	if fields["reason"] != "ledger_recompute" || fields["result"] != "rejected" {
		t.Fatalf("事件字段 = %+v, want reason=ledger_recompute", fields)
	}
	if fields["old_bonus_sp"] != int64(20) || fields["bonus_sp"] != int64(0) {
		t.Fatalf("事件必须带旧值/新值: %+v", fields)
	}
}

// (计划书测试 3) 技能账缺失 → 拒绝（不像只读详情那样合成空账本）。
func TestSpSetMissingLedgerRejected(t *testing.T) {
	harness := newP3SPHarness(t, p3SmallProgressionSource(), p3Character(5, 0, 0), nil)
	before := harness.characterRecord(t)

	status, response := harness.setSpAbsolute(t, map[string]any{"bonus_sp": 5, "bonus_tp": 0})
	if message := requireFailure(t, status, response); !strings.Contains(message, "技能点账本不存在") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	if _, found := harness.skill(t); found {
		t.Fatal("拒绝路径不该创建技能账")
	}
	if after := harness.characterRecord(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("角色被改动了（应当零写入）:\nbefore=%+v\nafter=%+v", before, after)
	}
	if fields := harness.eventFields(t, "gm-sp-set-rejected"); fields["reason"] != "skill_ledger_missing" {
		t.Fatalf("事件字段 = %+v", fields)
	}
}

// D1（见 api_sp_absolute.go 文件头）：绝对值模式必须同时给出两个值。
func TestSpSetRequiresBothBonusValuesAndAcceptsCamelCase(t *testing.T) {
	harness := newP3SPHarness(t, p3SmallProgressionSource(), p3Character(5, 0, 0), &dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{TotalSP: 160, RemainingSP: 160, TotalTP: 4, RemainingTP: 4, SyncedLevel: 5},
	})
	for _, testCase := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"两个都缺", map[string]any{}, "缺少 bonus_sp 与 bonus_tp"},
		{"只给 SP", map[string]any{"bonus_sp": 5}, "缺少 bonus_tp"},
		{"只给 TP", map[string]any{"bonus_tp": 5}, "缺少 bonus_sp"},
		{"下划线只给一个", map[string]any{"bonusSp": 5}, "缺少 bonus_tp"},
	} {
		status, response := harness.setSpAbsolute(t, testCase.body)
		if message := requireFailure(t, status, response); !strings.Contains(message, testCase.want) {
			t.Fatalf("%s: 文案 = %q, want 含 %q", testCase.name, message, testCase.want)
		}
	}
	// camelCase 两个都给 = 正常通过（前端既有写法）。
	status, response := harness.setSpAbsolute(t, map[string]any{"bonusSp": 7, "bonusTp": 2})
	requireOK(t, status, response)
	if response["bonusSp"] != float64(7) || response["bonusTp"] != float64(2) {
		t.Fatalf("camelCase 未被接受: %+v", response)
	}
}

func TestSpSetRejectsOutOfRangeBonusWithZeroWrites(t *testing.T) {
	character := p3Character(5, 0, 0)
	skill := dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{TotalSP: 160, RemainingSP: 160, TotalTP: 4, RemainingTP: 4, SyncedLevel: 5},
	}
	harness := newP3SPHarness(t, p3SmallProgressionSource(), character, &skill)
	beforeCharacter := harness.characterRecord(t)
	beforeSkill, _ := harness.skill(t)

	for _, body := range []map[string]any{
		{"bonus_sp": -1, "bonus_tp": 0},
		{"bonus_sp": 0, "bonus_tp": -1},
		{"bonus_sp": 65536, "bonus_tp": 0},
		{"bonus_sp": 0, "bonus_tp": 1 << 20},
	} {
		status, response := harness.setSpAbsolute(t, body)
		if message := requireFailure(t, status, response); !strings.Contains(message, "必须在 0..65535") {
			t.Fatalf("body=%+v 的拒绝文案 = %q", body, message)
		}
	}
	requireRecordUnchanged(t, harness, beforeCharacter, beforeSkill)
}

// 上限门（对应上游 gm_extended.go:297-298 的 Total<=MaxUint16）：bonus 本身合法，
// 但 PVF 总量 + bonus 超过 u16 线格式 → 拒绝且零写入。
func TestSpSetRejectsLedgerOverflowBeyondUint16(t *testing.T) {
	character := p3Character(5, 0, 0)
	skill := dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{TotalSP: 160, RemainingSP: 160, TotalTP: 4, RemainingTP: 4, SyncedLevel: 5},
	}
	harness := newP3SPHarness(t, p3SmallProgressionSource(), character, &skill)
	beforeCharacter := harness.characterRecord(t)
	beforeSkill, _ := harness.skill(t)

	status, response := harness.setSpAbsolute(t, map[string]any{"bonus_sp": 65535, "bonus_tp": 0})
	if message := requireFailure(t, status, response); !strings.Contains(message, "65535") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	requireRecordUnchanged(t, harness, beforeCharacter, beforeSkill)
	if fields := harness.eventFields(t, "gm-sp-set-rejected"); fields["reason"] != "ledger_overflow" {
		t.Fatalf("事件字段 = %+v", fields)
	}
}

// 幂等：两个 bonus 与账本都没变 -> 零写入（不刷新 UpdatedAt）。
func TestSpSetIsIdempotentWhenNothingChanges(t *testing.T) {
	character := p3Character(5, 20, 1)
	skill := dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{TotalSP: 180, RemainingSP: 60, TotalTP: 5, RemainingTP: 5, SyncedLevel: 5},
	}
	harness := newP3SPHarness(t, p3SmallProgressionSource(), character, &skill)
	beforeCharacter := harness.characterRecord(t)
	beforeSkill, _ := harness.skill(t)

	status, response := harness.setSpAbsolute(t, map[string]any{"bonus_sp": 20, "bonus_tp": 1})
	requireOK(t, status, response)
	if response["totalSp"] != float64(180) || response["remainingSp"] != float64(60) {
		t.Fatalf("幂等响应 = %+v", response)
	}
	requireRecordUnchanged(t, harness, beforeCharacter, beforeSkill)
	if fields := harness.eventFields(t, "gm-sp-set"); fields["result"] != "noop" {
		t.Fatalf("事件字段 = %+v, want result=noop", fields)
	}
}

// 既有路由 15 仍是**增量**语义：同一次改动里逐字对拍，证明 P3 没有回退它。
func TestExistingIncrementalSpRouteIsUnchanged(t *testing.T) {
	harness := newP3SPHarness(t, p3SmallProgressionSource(), p3Character(5, 20, 1), &dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{TotalSP: 180, RemainingSP: 60, TotalTP: 5, RemainingTP: 5, SyncedLevel: 5},
	})
	status, response := harness.post(t, "/api/characters/1/sp", map[string]string{"id": "1"},
		map[string]any{"sp": 10, "tp": 2}, harness.server.handleAdjustSpTp)
	requireOK(t, status, response)
	// 增量：20+10 = 30，1+2 = 3；总量 160+30 = 190，已花 120 → 剩余 70。
	if response["bonusSp"] != float64(30) || response["bonusTp"] != float64(3) ||
		response["totalSp"] != float64(190) || response["remainingSp"] != float64(70) {
		t.Fatalf("既有增量路由的读数变了: %+v", response)
	}
}

// 路由面：新路由要登录、命中自己的 handler；既有 /sp 不被它遮住。
func TestSpSetRouteSurface(t *testing.T) {
	harness := newP3SPHarness(t, p3SmallProgressionSource(), p3Character(5, 20, 1), &dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{TotalSP: 180, RemainingSP: 60, TotalTP: 5, RemainingTP: 5, SyncedLevel: 5},
	})
	handler := harness.server.routes() // 注册冲突会在这里 panic
	unauthorized := httpPostJSON(t, handler, "/api/characters/1/sp/set", map[string]any{"bonus_sp": 1, "bonus_tp": 1})
	if unauthorized.Code != http.StatusUnauthorized ||
		!strings.Contains(unauthorized.Body.String(), "\"loginRequired\":true") {
		t.Fatalf("未登录: status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	authorized := httpPostJSONWithSession(t, handler, token, "/api/characters/1/sp/set",
		map[string]any{"bonus_sp": 1, "bonus_tp": 1})
	if authorized.Code != http.StatusOK {
		t.Fatalf("带会话: status=%d body=%s", authorized.Code, authorized.Body.String())
	}
	if strings.Contains(authorized.Body.String(), "该功能未移植到 90CN 面板") {
		t.Fatalf("落到了 /api/ 兜底: %s", authorized.Body.String())
	}
	// 既有增量路由仍然命中自己（不是 401、也不是兜底）。
	legacy := httpPostJSONWithSession(t, handler, token, "/api/characters/1/sp", map[string]any{"sp": 1, "tp": 1})
	if legacy.Code != http.StatusOK || strings.Contains(legacy.Body.String(), "该功能未移植到 90CN 面板") {
		t.Fatalf("既有 /sp 路由被遮住: status=%d body=%s", legacy.Code, legacy.Body.String())
	}
}

// requireRecordUnchanged 断言角色 + 技能账一个字节都没变（内存仓储 Load/Save 都深拷贝）。
func requireRecordUnchanged(t *testing.T, harness *gmtoolWriteHarness, character dnfrepo.CharacterRecord, skill dnfrepo.SkillRecord) {
	t.Helper()
	if after := harness.characterRecord(t); !reflect.DeepEqual(character, after) {
		t.Fatalf("角色被改动了（本批应当零写入）:\nbefore=%+v\nafter=%+v", character, after)
	}
	after, found := harness.skill(t)
	if !found {
		t.Fatal("技能账在拒绝路径上应当保持原样")
	}
	if !reflect.DeepEqual(skill, after) {
		t.Fatalf("技能账被改动了（本批应当零写入）:\nbefore=%+v\nafter=%+v", skill, after)
	}
}

// 真档：端点必须按真档 Etc/spTable.etc 的累计值算总量。
// 真档 dump（`pvfdump -path Etc/spTable.etc`）：`[sp table] 1 0 2 30 … 90 240`、
// `[tp table] 50 1 … 100 1` → Lv90 累计 SP = 11970、TP = 41。
func TestRealPVFSpAbsoluteMatchesTheDumpedSkillPointTable(t *testing.T) {
	source := loadRealPVFSource(t)
	harness := newP3SPHarness(t, source, p3Character(90, 0, 0), &dnfrepo.SkillRecord{
		Points: dnfrepo.SkillPointState{SyncedLevel: 90},
	})
	status, response := harness.setSpAbsolute(t, map[string]any{"bonus_sp": 30, "bonus_tp": 9})
	requireOK(t, status, response)
	if response["totalSp"] != float64(11970+30) {
		t.Fatalf("totalSp = %v, want 12000（真档 Lv90 累计 SP 11970 + bonus 30）", response["totalSp"])
	}
	if response["totalTp"] != float64(41+9) {
		t.Fatalf("totalTp = %v, want 50（真档 Lv90 累计 TP 41 + bonus 9）", response["totalTp"])
	}
	if response["remainingSp"] != response["totalSp"] || response["remainingTp"] != response["totalTp"] {
		t.Fatalf("空账本下剩余应等于总量: %+v", response)
	}
}
