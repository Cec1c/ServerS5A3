package gmtool

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// D：角色改名（POST /api/characters/{id}/rename）
//
// 测试台沿用 api_mailbox_delete_test.go 的 gmtoolWriteHarness（里面已经有一个
// 角色 CharacterID "1" / AccountID "acc-1" / Name "tester" / slot 0 与日志收集器），
// 与 api_character_delete_test.go 对删除角色的用法完全同形。
//
// 三条断言口径（本文件的核心）：
//
//	1. 库里那一行**真的改了**（读回来比对，不看响应自述）；
//	2. 拒绝路径**零写入**：连写事务都不开（renameSettlementCounter.calls == 0）；
//	3. 幂等重试**不再写第二次**（事务计数不增、UpdatedAt 不变）。
// ---------------------------------------------------------------------------

// renameSettlementCounter 包住 Group.CharacterSettlement，只数"开了几次写事务"。
//
// 这是"零写入 / 只写一次"最直接的可观测证据：本面板所有单事务写路由都必须先经过
// 这个边界，进不去就代表一个字节都没写。
type renameSettlementCounter struct {
	inner dnfrepo.CharacterSettlementUnitOfWork
	calls int32
}

func (c *renameSettlementCounter) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	atomic.AddInt32(&c.calls, 1)
	return c.inner.WithinCharacterSettlement(ctx, characterID, apply)
}

func (c *renameSettlementCounter) count() int32 { return atomic.LoadInt32(&c.calls) }

// installRenameCounters 把计数事务边界装进测试台（Deps.Repos 返回的组）。
func installRenameCounters(t *testing.T, harness *gmtoolWriteHarness) *renameSettlementCounter {
	t.Helper()
	group, ok := harness.server.deps.Repos()
	if !ok {
		t.Fatal("repositories are not ready")
	}
	if group.CharacterSettlement == nil {
		t.Fatal("内存仓储组没有 CharacterSettlement：本用例依赖单事务边界")
	}
	counter := &renameSettlementCounter{inner: group.CharacterSettlement}
	group.CharacterSettlement = counter
	harness.server.deps.Repos = func() (dnfrepo.Group, bool) { return group, true }
	return counter
}

// renameCharacter 打一次改名请求（直接调 handler，与 deleteCharacter 同一手法）。
func (h *gmtoolWriteHarness) renameCharacter(t *testing.T, panelID string, body any) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/"+panelID+"/rename", map[string]string{"id": panelID},
		body, h.server.handleCharacterRename)
}

// renameBody 拼一个默认合法的请求体（characterOffline=true）。
func renameBody(newName, expectedName string) map[string]any {
	return map[string]any{
		"newName":          newName,
		"expectedName":     expectedName,
		"characterOffline": true,
	}
}

// seedCharacter 往测试台里再塞一个角色（用于跨账号/同账号重名与软删用例）。
func seedCharacter(t *testing.T, harness *gmtoolWriteHarness, record dnfrepo.CharacterRecord) {
	t.Helper()
	group, ok := harness.server.deps.Repos()
	if !ok {
		t.Fatal("repositories are not ready")
	}
	if record.Stats == nil {
		record.Stats = map[string]int64{}
	}
	if err := group.Character.Save(context.Background(), record); err != nil {
		t.Fatalf("seed character %s: %v", record.CharacterID, err)
	}
}

func requireRenameFailure(t *testing.T, status int, response map[string]any, wantContains string) string {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200（本仓业务失败恒 HTTP 200）", status)
	}
	if response["success"] != false {
		t.Fatalf("response = %+v, want success:false", response)
	}
	message, _ := response["error"].(string)
	if !strings.Contains(message, wantContains) {
		t.Fatalf("error = %q, want 包含 %q", message, wantContains)
	}
	return message
}

// ---------------------------------------------------------------------------
// 开关
// ---------------------------------------------------------------------------

func TestCharacterRenameFromEnvDefaultsToEnabled(t *testing.T) {
	// 未设置/空/未知值（含笔误）= 默认档启用。
	for _, value := range []string{"", " ", "on", "ON", "true", "1", "yes", "enable", "enabled", "hard", "rename", "offf"} {
		if !CharacterRenameFromEnv(value) {
			t.Fatalf("CharacterRenameFromEnv(%q) = false, want true（新路由默认可用）", value)
		}
	}
	// 回退必须写成显式字面量（与 QuestMutateFromEnv / chardelHardModeFromEnv 同约定）。
	for _, value := range []string{"off", "OFF", " off ", "0", "false", "FALSE", "no", "disable", "disabled", "none", "legacy"} {
		if CharacterRenameFromEnv(value) {
			t.Fatalf("CharacterRenameFromEnv(%q) = true, want false（显式回退档）", value)
		}
	}
}

func TestCharacterRenameFromEnvironmentReadsProcessEnv(t *testing.T) {
	if !CharacterRenameFromEnvironment() {
		t.Fatalf("%s 未设置时必须默认启用", renameModeEnv)
	}
	t.Setenv(renameModeEnv, "off")
	if CharacterRenameFromEnvironment() {
		t.Fatalf("%s=off 时必须关闭", renameModeEnv)
	}
	t.Setenv(renameModeEnv, "legacy")
	if CharacterRenameFromEnvironment() {
		t.Fatalf("%s=legacy 时必须关闭", renameModeEnv)
	}
	t.Setenv(renameModeEnv, "")
	if !CharacterRenameFromEnvironment() {
		t.Fatalf("%s 为空时必须取默认档启用", renameModeEnv)
	}
}

// 关闭档：路由**根本不注册**，请求落 /api/ 兜底 = 与加这条路由之前逐字节相同。
func TestRenameRouteIsNotRegisteredWhenDisabled(t *testing.T) {
	t.Setenv(renameModeEnv, "off")
	harness := newGmtoolWriteHarness(t, nil)
	handler := harness.server.routes()
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	recorder := httpDoJSON(t, handler, token, http.MethodPost, "/api/characters/1/rename",
		renameBody("test88", "tester"))
	if !strings.Contains(recorder.Body.String(), unportedFeatureMessage) {
		t.Fatalf("关闭档必须落 /api/ 兜底，实际: %s", recorder.Body.String())
	}
}

// 默认档：路由注册、要登录门禁、命中自己的 handler（不是兜底）。
func TestRenameRouteIsRegisteredByDefaultAndRequiresSession(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	handler := harness.server.routes()

	recorder := httpDoJSON(t, handler, "", http.MethodPost, "/api/characters/1/rename",
		renameBody("test88", "tester"))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status = %d, want 401（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"loginRequired":true`) {
		t.Fatalf("未登录 body = %s, want loginRequired:true", recorder.Body.String())
	}

	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	recorder = httpDoJSON(t, handler, token, http.MethodPost, "/api/characters/1/rename",
		renameBody("test88", "tester"))
	if strings.Contains(recorder.Body.String(), unportedFeatureMessage) {
		t.Fatalf("默认档不该落兜底: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"success":true`) {
		t.Fatalf("默认档改名应成功: %s", recorder.Body.String())
	}
}

func TestRenameSwitchIsReportedInEnvironment(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	found := false
	for _, entry := range harness.server.envSwitchReport() {
		if entry.Name != renameModeEnv {
			continue
		}
		found = true
		if !entry.Enabled {
			t.Fatalf("%s 默认档必须在环境报告里显示为启用", renameModeEnv)
		}
		if entry.DefaultWhenUnset != "on" {
			t.Fatalf("DefaultWhenUnset = %q, want on", entry.DefaultWhenUnset)
		}
	}
	if !found {
		t.Fatalf("环境报告缺少 %s", renameModeEnv)
	}
}

// ---------------------------------------------------------------------------
// 名字规则（逐条对齐上游 GmService.CharacterClone.cs:924-936）
// ---------------------------------------------------------------------------

func TestRenameValidateCharacterNameMatchesUpstream(t *testing.T) {
	valid := []string{"ab", "test88", "test78_copy", "中文", "中文名字测试", "abc中文"}
	for _, name := range valid {
		if message := renameValidateCharacterName(name); message != "" {
			t.Fatalf("renameValidateCharacterName(%q) = %q, want 合法", name, message)
		}
	}
	cases := []struct {
		name    string
		message string
	}{
		{"", renameErrNameEmpty},
		{"   ", renameErrNameEmpty},
		{"a", renameErrNameLength},             // 1 字节 < 2
		{"abcdefghijklm", renameErrNameLength}, // 13 字节 > 12
		{"中文名字测试测", renameErrNameLength},       // 7 个中文 = 14 字节 > 12
		{"😀", renameErrNameNotGBK},             // GBK(936) 表示不了
		{"abc😀", renameErrNameNotGBK},          // 混在合法名里也不行
		{"a\x00b", renameErrNameNotGBK},        // NUL（上游 :929 单列一条）
		{"a\x01b", renameErrNameControl},       // 控制字符（GBK 能往返，靠第三条拦）
		{"\u3000", renameErrNameNotGBK},        // 全角空格：TrimSpace 不裁 => 2 字节合法？见下
	}
	for _, testCase := range cases {
		got := renameValidateCharacterName(testCase.name)
		if testCase.name == "\u3000" {
			// U+3000 是 GBK 里的全角空格（2 字节），既能表示也满足 2 字节下限：
			// 上游 .NET Trim() 会把它裁掉（IsWhiteSpace=true）→ 空 → 拒绝；
			// 本仓 TrimSpace 同样把 U+3000 当空白裁掉，所以合法判断在 handler 那一层
			// （见 TestRenameRejectsBlankNameAfterTrim）。这里只钉住"不 panic、有结论"。
			if got != "" && got != renameErrNameEmpty {
				t.Fatalf("U+3000 -> %q, want 空串或空名拒绝", got)
			}
			continue
		}
		if got != testCase.message {
			t.Fatalf("renameValidateCharacterName(%q) = %q, want %q", testCase.name, got, testCase.message)
		}
	}
}

func TestRenameRejectsBlankNameAfterTrim(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)
	status, response := harness.renameCharacter(t, "1", renameBody("\u3000", "tester"))
	requireRenameFailure(t, status, response, renameErrNameEmpty)
	if counter.count() != 0 {
		t.Fatalf("空名被拒时不能开事务（calls=%d）", counter.count())
	}
}

// ---------------------------------------------------------------------------
// 成功路径
// ---------------------------------------------------------------------------

func TestRenameRenamesTheCharacterRow(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)

	status, response := harness.renameCharacter(t, "1", renameBody("test88", "tester"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200（body=%+v）", status, response)
	}
	if response["success"] != true {
		t.Fatalf("response = %+v, want success:true", response)
	}
	for field, want := range map[string]any{
		"name":         "test88",
		"previousName": "tester",
		"applied":      true,
		"idempotent":   false,
		"verified":     true,
		"renameMode":   "enabled",
		"accountName":  "acc-1",
	} {
		if response[field] != want {
			t.Fatalf("%s = %v, want %v（response=%+v）", field, response[field], want, response)
		}
	}

	// 库里那一行真的改了（不看响应自述）。
	record, found := harness.character(t, "1")
	if !found {
		t.Fatal("角色 1 应该还在（改名不是删除）")
	}
	if record.Name != "test88" {
		t.Fatalf("dnf_characters.name = %q, want test88", record.Name)
	}
	if record.CharacterID != "1" || record.AccountID != "acc-1" || record.Slot != 0 {
		t.Fatalf("改名不该动角色号/账号/槽位: %+v", record)
	}
	if counter.count() != 1 {
		t.Fatalf("成功路径必须正好开 1 个写事务（calls=%d）", counter.count())
	}
	fields := harness.eventFields(t, "gm-character-rename")
	if fields["result"] != "applied" || fields["name"] != "test88" || fields["previous_name"] != "tester" {
		t.Fatalf("审计事件字段 = %+v", fields)
	}
}

// 中文（GBK 多字节）也要能改。
func TestRenameAcceptsChineseName(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	status, response := harness.renameCharacter(t, "1", renameBody("中文名字测试", "tester"))
	if response["success"] != true {
		t.Fatalf("status=%d response=%+v, want success", status, response)
	}
	record, _ := harness.character(t, "1")
	if record.Name != "中文名字测试" {
		t.Fatalf("name = %q, want 中文名字测试", record.Name)
	}
}

// ---------------------------------------------------------------------------
// 拒绝路径（全部零写入）
// ---------------------------------------------------------------------------

func TestRenameRejectsWrongExpectedName(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)

	status, response := harness.renameCharacter(t, "1", renameBody("test88", "test78_copy"))
	requireRenameFailure(t, status, response, renameErrExpected)
	if counter.count() != 0 {
		t.Fatalf("乐观并发拒绝必须零写入（calls=%d）", counter.count())
	}
	record, _ := harness.character(t, "1")
	if record.Name != "tester" {
		t.Fatalf("被拒后 name = %q, want tester（不改）", record.Name)
	}
	fields := harness.eventFields(t, "gm-character-rename-rejected")
	if fields["reason"] != "expected_name_changed" {
		t.Fatalf("事件 reason = %v, want expected_name_changed", fields["reason"])
	}
}

func TestRenameRejectsMissingExpectedName(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)
	status, response := harness.renameCharacter(t, "1", map[string]any{
		"newName":          "test88",
		"characterOffline": true,
	})
	requireRenameFailure(t, status, response, "缺少 expectedName")
	if counter.count() != 0 {
		t.Fatalf("缺字段必须零写入（calls=%d）", counter.count())
	}
}

// 重名拒绝：两个作用域都钉住（上游 CharacterNameExists 是全表口径 ⇒ 全服唯一）。
func TestRenameRejectsDuplicateNameAcrossAccounts(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)
	seedCharacter(t, harness, dnfrepo.CharacterRecord{
		CharacterID: "2", AccountID: "acc-2", Slot: 0, Name: "taken",
	})

	status, response := harness.renameCharacter(t, "1", renameBody("taken", "tester"))
	requireRenameFailure(t, status, response, renameErrNameTakenPrefix+"taken")
	if counter.count() != 0 {
		t.Fatalf("重名拒绝必须零写入（calls=%d）", counter.count())
	}
	record, _ := harness.character(t, "1")
	if record.Name != "tester" {
		t.Fatalf("被拒后 name = %q, want tester", record.Name)
	}
	other, found := harness.character(t, "2")
	if !found || other.Name != "taken" {
		t.Fatalf("另一个角色的名字不该被动过: %+v found=%t", other, found)
	}
}

func TestRenameRejectsDuplicateNameInTheSameAccount(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)
	seedCharacter(t, harness, dnfrepo.CharacterRecord{
		CharacterID: "3", AccountID: "acc-1", Slot: 1, Name: "samenam",
	})

	status, response := harness.renameCharacter(t, "1", renameBody("samenam", "tester"))
	requireRenameFailure(t, status, response, renameErrNameTakenPrefix+"samenam")
	if counter.count() != 0 {
		t.Fatalf("同账号重名拒绝必须零写入（calls=%d）", counter.count())
	}
}

// 软删（delete_flag != 0）的角色与上游 `WHERE delete_flag=0` 同义：不存在。
func TestRenameRejectsSoftDeletedCharacter(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)
	group, _ := harness.server.deps.Repos()
	record, found, err := group.Character.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("load character: found=%t err=%v", found, err)
	}
	if record.Stats == nil {
		record.Stats = map[string]int64{}
	}
	record.Stats["delete_flag"] = 1
	if err := group.Character.Save(context.Background(), record); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}

	status, response := harness.renameCharacter(t, "1", renameBody("test88", "tester"))
	requireRenameFailure(t, status, response, renameErrGone)
	if counter.count() != 0 {
		t.Fatalf("软删角色必须零写入（calls=%d）", counter.count())
	}
}

func TestRenameRejectsInvalidNames(t *testing.T) {
	cases := []struct {
		name    string
		message string
	}{
		{"a", renameErrNameLength},
		{"abcdefghijklm", renameErrNameLength},
		{"中文名字测试测", renameErrNameLength},
		{"😀", renameErrNameNotGBK},
		{"a\x01b", renameErrNameControl},
	}
	for _, testCase := range cases {
		harness := newGmtoolWriteHarness(t, nil)
		counter := installRenameCounters(t, harness)
		status, response := harness.renameCharacter(t, "1", renameBody(testCase.name, "tester"))
		requireRenameFailure(t, status, response, testCase.message)
		if counter.count() != 0 {
			t.Fatalf("非法名 %q 必须零写入（calls=%d）", testCase.name, counter.count())
		}
		record, _ := harness.character(t, "1")
		if record.Name != "tester" {
			t.Fatalf("非法名 %q 被拒后 name = %q, want tester", testCase.name, record.Name)
		}
	}
}

// 在线守卫：探针说在线就拒绝，**不看 cfg.RequireOffline**（默认档也是拒绝）。
func TestRenameRejectsOnlineCharacter(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)
	if harness.server.cfg.RequireOffline {
		t.Fatal("本用例的前提是 cfg.RequireOffline 取默认档 false")
	}
	asked := make([]uint16, 0, 1)
	harness.server.deps.Online = func(characterID uint16) bool {
		asked = append(asked, characterID)
		return true
	}

	status, response := harness.renameCharacter(t, "1", renameBody("test88", "tester"))
	requireRenameFailure(t, status, response, renameErrOnline)
	if len(asked) != 1 || asked[0] != 1 {
		t.Fatalf("探针被问的角色 = %v, want [1]", asked)
	}
	if counter.count() != 0 {
		t.Fatalf("在线角色必须零写入（calls=%d）", counter.count())
	}
	record, _ := harness.character(t, "1")
	if record.Name != "tester" {
		t.Fatalf("在线被拒后 name = %q, want tester", record.Name)
	}
	fields := harness.eventFields(t, "gm-character-rename-rejected")
	if fields["reason"] != "character_online" {
		t.Fatalf("事件 reason = %v, want character_online", fields["reason"])
	}
}

// 探针说不在线：放行（不能因为探针存在就把所有人都拦下）。
func TestRenameAllowsCharacterTheProbeSaysIsOffline(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	harness.server.deps.Online = func(uint16) bool { return false }
	status, response := harness.renameCharacter(t, "1", renameBody("test88", "tester"))
	if response["success"] != true {
		t.Fatalf("status=%d response=%+v, want success", status, response)
	}
}

// 上游 :18 的"操作者确认离线"：不给就不做（缺字段与显式 false 同一条拒绝）。
func TestRenameRejectsWithoutOfflineConfirmation(t *testing.T) {
	for _, body := range []any{
		map[string]any{"newName": "test88", "expectedName": "tester"},
		map[string]any{"newName": "test88", "expectedName": "tester", "characterOffline": false},
	} {
		harness := newGmtoolWriteHarness(t, nil)
		counter := installRenameCounters(t, harness)
		status, response := harness.renameCharacter(t, "1", body)
		requireRenameFailure(t, status, response, renameErrOffline)
		if counter.count() != 0 {
			t.Fatalf("缺离线确认必须零写入（calls=%d）", counter.count())
		}
		record, _ := harness.character(t, "1")
		if record.Name != "tester" {
			t.Fatalf("被拒后 name = %q, want tester", record.Name)
		}
	}
}

// 事务能力缺失：明确失败（绝不静默退化成"直接写一条"）。
func TestRenameFailsClearlyWithoutSettlementTransaction(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	group, _ := harness.server.deps.Repos()
	group.CharacterSettlement = nil
	harness.server.deps.Repos = func() (dnfrepo.Group, bool) { return group, true }

	status, response := harness.renameCharacter(t, "1", renameBody("test88", "tester"))
	requireRenameFailure(t, status, response, renameErrTxMissing)
	record, _ := harness.character(t, "1")
	if record.Name != "tester" {
		t.Fatalf("被拒后 name = %q, want tester", record.Name)
	}
}

// ---------------------------------------------------------------------------
// 幂等
// ---------------------------------------------------------------------------

func TestRenameIdempotentRetryDoesNotWriteAgain(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	counter := installRenameCounters(t, harness)

	if _, response := harness.renameCharacter(t, "1", renameBody("test88", "tester")); response["success"] != true {
		t.Fatalf("第一次改名必须成功: %+v", response)
	}
	first, found := harness.character(t, "1")
	if !found || first.Name != "test88" {
		t.Fatalf("第一次改名后 name = %q found=%t", first.Name, found)
	}
	if counter.count() != 1 {
		t.Fatalf("第一次改名后写事务数 = %d, want 1", counter.count())
	}

	// 重试：新名与库里已生效的新名相同（客户端/操作者重发同一次改名）。
	status, response := harness.renameCharacter(t, "1", renameBody("test88", "test88"))
	if status != http.StatusOK || response["success"] != true {
		t.Fatalf("幂等重试必须成功（status=%d response=%+v）", status, response)
	}
	if response["idempotent"] != true || response["applied"] != false {
		t.Fatalf("幂等重试应 idempotent=true applied=false，实际 %+v", response)
	}
	if response["name"] != "test88" || response["previousName"] != "test88" {
		t.Fatalf("幂等重试的 name/previousName = %v/%v", response["name"], response["previousName"])
	}
	if counter.count() != 1 {
		t.Fatalf("幂等重试**不许**再开写事务（calls=%d, want 1）", counter.count())
	}
	second, _ := harness.character(t, "1")
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("幂等重试不该刷新 UpdatedAt: %v -> %v", first.UpdatedAt, second.UpdatedAt)
	}
	if second.Name != "test88" {
		t.Fatalf("幂等重试后 name = %q, want test88", second.Name)
	}
}

// ---------------------------------------------------------------------------
// 前端接线（静态检查，与 p4b_frontend_test.go 同一强度）
//
// 面板前端是**内嵌静态资源**（`//go:embed wwwroot`，index.html + 裸 JS，无构建步骤），
// 所以本批直接加了改名控件。这里钉住三件事：控件在、绑上了、打的路径有注册
// （最后一条正是 P4b 那个 bug 的一般形式：按钮打了一条从未注册的路径）。
// ---------------------------------------------------------------------------

func TestRenameFrontendIsWired(t *testing.T) {
	rawIndex, err := embeddedWWW.ReadFile("wwwroot/index.html")
	if err != nil {
		t.Fatalf("embedded index.html: %v", err)
	}
	index := string(rawIndex)
	for _, id := range []string{"rename-character-name", "btn-rename-character", "rename-character-state"} {
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Fatalf("index.html 缺少 #%s（改名控件没接上）", id)
		}
	}

	rawScript, err := embeddedWWW.ReadFile("wwwroot/js/character.js")
	if err != nil {
		t.Fatalf("embedded character.js: %v", err)
	}
	script := string(rawScript)
	body := p4bFunctionBody(script, "renameCurrentCharacter")
	if body == "" {
		t.Fatal("character.js 里找不到 renameCurrentCharacter")
	}
	for _, marker := range []string{
		"/rename`",                     // 打的是本批新增的路由
		"expectedName: character.name", // 乐观并发：逐字给出当前名
		"characterOffline: true",       // 上游 :18 的操作者确认
		"newName,",                     // 新名
		"toast(e.message, true)",       // 失败原样透出服务端文案
		"r.verified === false",         // verified 字段被如实使用
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("renameCurrentCharacter 缺少 %q", marker)
		}
	}

	rawBindings, err := embeddedWWW.ReadFile("wwwroot/js/bindings.js")
	if err != nil {
		t.Fatalf("embedded bindings.js: %v", err)
	}
	if !strings.Contains(string(rawBindings), "$('#btn-rename-character').onclick = renameCurrentCharacter;") {
		t.Fatal("bindings.js 未绑定 #btn-rename-character")
	}

	// 脚本打的路径必须真的注册过（gmtool.go 的注册行是本包唯一真源）。
	if routes := p4bRegisteredRoutes(t); !routes["POST /api/characters/*/rename"] {
		t.Fatal("gmtool.go 里没有注册 POST /api/characters/{id}/rename")
	}
}

// 幂等的第三条路：ExpectedName 用改后的新名、新名也等于它 —— 两次调用之间名字
// 被别的路径改掉时，"已生效"也必须被识别成成功而不是"重名/冲突"。
func TestRenameIdempotentWhenNameAlreadyAppliedBySomeoneElse(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil)
	// 库里已经是 test88，但调用者上次拿到的 ExpectedName 也是 test88（说明上一次
	// 已经改成功，只是响应丢了）。
	group, _ := harness.server.deps.Repos()
	record, _, _ := group.Character.Load(context.Background(), "1")
	record.Name = "test88"
	if err := group.Character.Save(context.Background(), record); err != nil {
		t.Fatalf("seed renamed record: %v", err)
	}
	counter := installRenameCounters(t, harness)

	status, response := harness.renameCharacter(t, "1", renameBody("test88", "test88"))
	if response["success"] != true || response["idempotent"] != true {
		t.Fatalf("status=%d response=%+v, want 幂等成功", status, response)
	}
	if counter.count() != 0 {
		t.Fatalf("幂等路径不许开写事务（calls=%d）", counter.count())
	}
}
