package gmtool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// P2 写路由测试台（J0 在线守卫 + J5 邮件删除共用；对应 4.2 的测试清单）
// ---------------------------------------------------------------------------

// gmtoolLoggedEvent 是一条具名审计事件（cfg.Logger 收到的原始 key/value 序列）。
type gmtoolLoggedEvent struct {
	kind   string
	fields []any
}

func (e gmtoolLoggedEvent) fieldMap() map[string]any {
	out := make(map[string]any, len(e.fields)/2)
	for i := 0; i+1 < len(e.fields); i += 2 {
		out[fmt.Sprint(e.fields[i])] = e.fields[i+1]
	}
	return out
}

type gmtoolWriteHarness struct {
	*readonlyHarness

	mu     sync.Mutex
	events []gmtoolLoggedEvent
}

// newGmtoolWriteHarness 在 P1 只读测试台上加两样东西：可断言的日志收集器，
// 以及一个可写的邮箱 fixture。cfg.RequireOffline 保持零值 = 默认档。
func newGmtoolWriteHarness(t *testing.T, mails map[string]dnfrepo.MailRecord) *gmtoolWriteHarness {
	t.Helper()
	harness := newReadonlyHarness(t, readonlyFixture{
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1", Name: "tester", Job: "11", Level: 90, Stats: map[string]int64{},
		},
		mailbox:   &dnfrepo.MailboxRecord{Mails: mails},
		skipItems: true,
	})
	write := &gmtoolWriteHarness{readonlyHarness: harness}
	harness.server.cfg.Logger = write.record
	return write
}

func (h *gmtoolWriteHarness) record(kind string, fields ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, gmtoolLoggedEvent{kind: kind, fields: append([]any(nil), fields...)})
}

func (h *gmtoolWriteHarness) eventKinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	kinds := make([]string, 0, len(h.events))
	for _, event := range h.events {
		kinds = append(kinds, event.kind)
	}
	return kinds
}

func (h *gmtoolWriteHarness) kindsWithPrefix(prefix string) []string {
	kinds := make([]string, 0, 4)
	for _, kind := range h.eventKinds() {
		if strings.HasPrefix(kind, prefix) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func (h *gmtoolWriteHarness) hasEvent(kind string) bool {
	for _, seen := range h.eventKinds() {
		if seen == kind {
			return true
		}
	}
	return false
}

// eventFields 取最后一条同 kind 事件的 key/value 字段表。
func (h *gmtoolWriteHarness) eventFields(t *testing.T, kind string) map[string]any {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.events) - 1; i >= 0; i-- {
		if h.events[i].kind == kind {
			return h.events[i].fieldMap()
		}
	}
	t.Fatalf("没有找到具名事件 %q（已记录：%v）", kind, h.eventKinds())
	return nil
}

func (h *gmtoolWriteHarness) post(t *testing.T, target string, pathValues map[string]string, body any, handler http.HandlerFunc) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	for key, value := range pathValues {
		request.SetPathValue(key, value)
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, response
}

func (h *gmtoolWriteHarness) deleteMails(t *testing.T, mailIDs []string, force bool) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/1/mailbox/delete", map[string]string{"id": "1"},
		map[string]any{"mailIds": mailIDs, "force": force}, h.server.handleMailboxDelete)
}

func (h *gmtoolWriteHarness) mailbox(t *testing.T) dnfrepo.MailboxRecord {
	t.Helper()
	record, found, err := h.group.Mailbox.Load(context.Background(), "1")
	if err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	if !found {
		return dnfrepo.MailboxRecord{CharacterID: "1"}
	}
	return record
}

func (h *gmtoolWriteHarness) mailboxMails(t *testing.T) map[string]dnfrepo.MailRecord {
	t.Helper()
	return h.mailbox(t).Mails
}

// requireUnchanged 断言邮箱一个字节都没变（内存仓储 Load/Save 都深拷贝，
// 所以 DeepEqual 等价于"没有发生任何写入"）。
func (h *gmtoolWriteHarness) requireUnchanged(t *testing.T, before dnfrepo.MailboxRecord) {
	t.Helper()
	after := h.mailbox(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("邮箱被改动了（本批应当零写入）:\nbefore=%+v\nafter=%+v", before, after)
	}
}

// mailboxDeleteFixture 是三封典型邮件：未领取附件 / 未领取金币 / 干净邮件。
func mailboxDeleteFixture() map[string]dnfrepo.MailRecord {
	base := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)
	return map[string]dnfrepo.MailRecord{
		"1": {
			MailID:      "1",
			Title:       "带附件未领",
			Attachments: []dnfrepo.MailAttachment{{ItemID: 7, Count: 2, Bind: true}},
			CreatedAt:   base,
		},
		"2": {
			MailID:    "2",
			Title:     "带金币未领",
			Gold:      5000,
			CreatedAt: base.Add(time.Minute),
		},
		"3": {
			MailID:    "3",
			Title:     "干净邮件",
			CreatedAt: base.Add(2 * time.Minute),
		},
	}
}

// (a) 默认档：开关未设（cfg.RequireOffline=false）时，**在线角色照样能删** —— 今天的行为。
func TestMailboxDeleteKeepsWorkingWhenGuardIsOffEvenIfCharacterOnline(t *testing.T) {
	probeCalls := 0
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	harness.server.cfg.RequireOffline = false // DNFBRIDGE_GM_REQUIRE_OFFLINE 未设置
	harness.server.deps.Online = func(uint16) bool { probeCalls++; return true }

	status, response := harness.deleteMails(t, []string{"1", "3"}, true)
	requireOK(t, status, response)
	if response["deleted"] != float64(2) {
		t.Fatalf("deleted = %v, want 2: %+v", response["deleted"], response)
	}
	if probeCalls != 0 {
		t.Fatalf("默认档连在线探针都不该碰，实际调用 %d 次", probeCalls)
	}
	if kinds := harness.kindsWithPrefix("gm-online-guard"); len(kinds) != 0 {
		t.Fatalf("默认档不该产生任何在线守卫日志：%v", kinds)
	}
	mails := harness.mailboxMails(t)
	if !mails["1"].Deleted || len(mails["1"].Attachments) != 0 || mails["1"].Gold != 0 {
		t.Fatalf("选中的邮件没有被删除: %+v", mails["1"])
	}
	if !mails["3"].Deleted {
		t.Fatalf("选中的干净邮件没有被删除: %+v", mails["3"])
	}
	if mails["2"].Deleted {
		t.Fatalf("没选中的邮件被改动了: %+v", mails["2"])
	}
	// 审计事件里有角色号/邮件号/force/结果计数。
	fields := harness.eventFields(t, "gm-mailbox-delete")
	if fields["character_id"] != "1" || fields["mail_ids"] != "1,3" ||
		fields["force"] != true || fields["deleted"] != 2 || fields["remaining"] != 1 {
		t.Fatalf("审计事件字段不全: %+v", fields)
	}
}

// (b) 显式开启后在线角色被拒，且**零写入**（HTTP 200 + success:false，绝不 409）。
func TestMailboxDeleteRejectsOnlineCharacterWithZeroWrites(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 1 }
	before := harness.mailbox(t)

	status, response := harness.deleteMails(t, []string{"1", "3"}, true)
	message := requireFailure(t, status, response)
	if !strings.Contains(message, "在线") {
		t.Fatalf("失败文案没有说明是在线原因: %q", message)
	}
	harness.requireUnchanged(t, before)

	fields := harness.eventFields(t, "gm-online-guard-rejected")
	if fields["character_id"] != uint16(1) || fields["action"] != "mailbox-delete" ||
		fields["reason"] != "character_online" || fields["panel_id"] != 1 {
		t.Fatalf("守卫拒绝事件字段不全: %+v", fields)
	}
}

// (c) Deps.Online == nil（宿主没注入探针）时必须安全降级：放行 + 一条具名日志。
func TestMailboxDeleteWithoutOnlineProbeDegradesSafely(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = nil

	status, response := harness.deleteMails(t, []string{"3"}, false)
	requireOK(t, status, response)
	if response["deleted"] != float64(1) {
		t.Fatalf("没有探针时必须放行（视为未知/不在线）: %+v", response)
	}
	fields := harness.eventFields(t, "gm-online-guard-probe-missing")
	if fields["character_id"] != uint16(1) || fields["reason"] != "probe_not_injected" ||
		fields["action"] != "mailbox-delete" {
		t.Fatalf("探针缺失事件字段不全: %+v", fields)
	}
}

// (d) 不带 force：未领取资产的邮件必须被拒，整批零写入，且如实回报是哪几封。
func TestMailboxDeleteWithoutForceProtectsUnclaimedAssets(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	before := harness.mailbox(t)

	status, response := harness.deleteMails(t, []string{"1", "2", "3"}, false)
	message := requireFailure(t, status, response)
	for _, mailID := range []string{"1", "2"} {
		if !strings.Contains(message, mailID) {
			t.Fatalf("失败文案没有列出受保护的邮件 %s: %q", mailID, message)
		}
	}
	protected := responseList(t, response, "protected")
	if len(protected) != 2 || protected[0] != "1" || protected[1] != "2" {
		t.Fatalf("protected = %v, want [1 2]", protected)
	}
	harness.requireUnchanged(t, before)
	fields := harness.eventFields(t, "gm-mailbox-delete-rejected")
	if fields["reason"] != "unclaimed_assets" || fields["mail_ids"] != "1,2" || fields["force"] != false {
		t.Fatalf("拒绝事件字段不全: %+v", fields)
	}
}

// (e) 带 force：连附件与金币一起删，附件清空（对应上游 gm.go:574-582）。
func TestMailboxDeleteForceClearsAttachmentsAndGold(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())

	status, response := harness.deleteMails(t, []string{"1", "2"}, true)
	requireOK(t, status, response)
	if response["deleted"] != float64(2) || response["remaining"] != float64(1) {
		t.Fatalf("deleted/remaining = %v/%v, want 2/1", response["deleted"], response["remaining"])
	}
	mails := harness.mailboxMails(t)
	first := mails["1"]
	if !first.Deleted || !first.Read || first.Gold != 0 || len(first.Attachments) != 0 {
		t.Fatalf("附件邮件没有被清成删除态: %+v", first)
	}
	second := mails["2"]
	if !second.Deleted || second.Gold != 0 || len(second.Attachments) != 0 {
		t.Fatalf("金币邮件没有被清成删除态: %+v", second)
	}
	if mails["3"].Deleted {
		t.Fatalf("没选中的邮件被改动了: %+v", mails["3"])
	}
}

// (f-1) 边界：0 个 / 101 个 / 含空字符串 -> 拒绝，且零写入（上游 gm.go:521-523,531-533）。
func TestMailboxDeleteRejectsEmptyOversizedAndBlankBatch(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	before := harness.mailbox(t)

	if _, response := harness.deleteMails(t, nil, false); !strings.Contains(
		requireFailure(t, http.StatusOK, response), "1..100") {
		t.Fatalf("空列表必须报 1..100: %+v", response)
	}
	tooMany := make([]string, 0, 101)
	for i := 1; i <= 101; i++ {
		tooMany = append(tooMany, strconv.Itoa(i))
	}
	if _, response := harness.deleteMails(t, tooMany, false); !strings.Contains(
		requireFailure(t, http.StatusOK, response), "101") {
		t.Fatalf("101 个必须被拒: %+v", response)
	}
	if _, response := harness.deleteMails(t, []string{"3", "  "}, false); !strings.Contains(
		requireFailure(t, http.StatusOK, response), "空") {
		t.Fatalf("空邮件号必须被拒: %+v", response)
	}
	harness.requireUnchanged(t, before)
	if !harness.hasEvent("gm-mailbox-delete-rejected") {
		t.Fatalf("拒绝也必须留下具名事件: %v", harness.eventKinds())
	}
}

// (f-2) 边界：重复 id 去重；上限 100 恰好可用（上游 gm.go:527-539）。
func TestMailboxDeleteDeduplicatesAndAcceptsHundredMails(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	status, response := harness.deleteMails(t, []string{"3", "3", " 3 "}, false)
	requireOK(t, status, response)
	if response["requested"] != float64(1) || response["deleted"] != float64(1) {
		t.Fatalf("重复 id 必须去重: %+v", response)
	}

	// 上游风格的下划线别名也必须能读（mail_ids）。
	status, response = harness.post(t, "/api/characters/1/mailbox/delete", map[string]string{"id": "1"},
		map[string]any{"mail_ids": []string{"2"}, "force": false}, harness.server.handleMailboxDelete)
	requireFailure(t, status, response) // "2" 带未领金币，无 force -> 拒绝（说明别名被读到了）
	if protected := responseList(t, response, "protected"); len(protected) != 1 || protected[0] != "2" {
		t.Fatalf("mail_ids 别名没有被解析: %+v", response)
	}

	// 上限 100（含）必须可用。
	mails := make(map[string]dnfrepo.MailRecord, 100)
	ids := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		id := strconv.Itoa(i)
		mails[id] = dnfrepo.MailRecord{MailID: id, Title: "批量邮件"}
		ids = append(ids, id)
	}
	full := newGmtoolWriteHarness(t, mails)
	status, response = full.deleteMails(t, ids, false)
	requireOK(t, status, response)
	if response["deleted"] != float64(100) || response["remaining"] != float64(0) {
		t.Fatalf("100 封必须一次删完: %+v", response)
	}
}

// (f-3) 边界：不存在的邮件号按上游整批拒绝（gm.go:566-568），零写入。
func TestMailboxDeleteRejectsUnknownMailIDsWithZeroWrites(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	before := harness.mailbox(t)

	status, response := harness.deleteMails(t, []string{"3", "404"}, false)
	message := requireFailure(t, status, response)
	if !strings.Contains(message, "404") {
		t.Fatalf("失败文案没有列出不存在的邮件号: %q", message)
	}
	notFound := responseList(t, response, "notFound")
	if len(notFound) != 1 || notFound[0] != "404" {
		t.Fatalf("notFound = %v, want [404]", notFound)
	}
	harness.requireUnchanged(t, before) // 合法的 "3" 也不能被顺手删掉
}

// 幂等：删已经删过的邮件 = 无操作 + 如实回报（不报错、不二次写入）。
//
// 这是与上游 gm.go:566-568 唯一的有意差异：上游把"已删除"和"不存在"一视同仁地报错。
func TestMailboxDeleteIsIdempotentForAlreadyDeletedMail(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())

	status, response := harness.deleteMails(t, []string{"3"}, false)
	requireOK(t, status, response)
	if response["deleted"] != float64(1) || response["remaining"] != float64(2) {
		t.Fatalf("首次删除: %+v", response)
	}
	afterFirst := harness.mailbox(t)

	status, response = harness.deleteMails(t, []string{"3"}, false)
	requireOK(t, status, response)
	if response["deleted"] != float64(0) || response["remaining"] != float64(2) {
		t.Fatalf("重复删除必须是无操作: %+v", response)
	}
	already := responseList(t, response, "alreadyDeleted")
	if len(already) != 1 || already[0] != "3" {
		t.Fatalf("alreadyDeleted = %v, want [3]", already)
	}
	harness.requireUnchanged(t, afterFirst)
	fields := harness.eventFields(t, "gm-mailbox-delete")
	if fields["deleted"] != 0 || fields["already_deleted_count"] != 1 {
		t.Fatalf("幂等无操作也要留审计事件: %+v", fields)
	}
}

// 新路由必须：未登录 401；带会话时命中自己的 handler（不是 /api/ 兜底，也不是 404）。
func TestMailboxDeleteRouteRequiresSessionAndIsNotTheFallback(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	handler := harness.server.routes()

	request := httptest.NewRequest(http.MethodPost, "/api/characters/1/mailbox/delete",
		strings.NewReader(`{"mailIds":["3"]}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status = %d, want 401", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"loginRequired":true`) {
		t.Fatalf("未登录 body = %s", recorder.Body.String())
	}

	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/characters/1/mailbox/delete",
		strings.NewReader(`{"mailIds":["3"]}`))
	request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("带会话 status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
		t.Fatalf("新路由落到了 /api/ 兜底: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"success":true`) {
		t.Fatalf("带会话应当删成功: %s", recorder.Body.String())
	}
}

// 守卫绝不能泄漏到既有路由：即使开关为 on、角色在线，既有路由也必须走它们自己的
// 逻辑（例如 route 26 equipment-slots/unlock、既有的 mailbox/clear）—— 本仓今天
// 不检查在线，给既有路由加上守卫会改变既有行为（见 online_guard.go 的作用域红线）。
func TestOnlineGuardDoesNotLeakIntoExistingRoutes(t *testing.T) {
	harness := newGmtoolWriteHarness(t, mailboxDeleteFixture())
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(uint16) bool { return true }
	handler := harness.server.routes()
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	existingWrites := []string{
		"/api/characters/1/equipment-slots/unlock",
		"/api/characters/1/wallet",
		"/api/characters/1/mailbox/clear",
	}
	for _, route := range existingWrites {
		request := httptest.NewRequest(http.MethodPost, route, strings.NewReader(`{}`))
		request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", route, recorder.Code, body)
		}
		if strings.Contains(body, "正在游戏内在线") {
			t.Fatalf("%s 被在线守卫拦住了：守卫只作用于 P2 新增路由（既有行为必须逐字节不变）", route)
		}
		if strings.Contains(body, "该功能未移植到 90CN 面板") {
			t.Fatalf("%s 落到了 /api/ 兜底: %s", route, body)
		}
	}
}

// 前端资产必须随二进制发布，且 bindings.js 引用的两个新元素在 index.html 里真实存在
// （bindings.js 顶部注释点名过的事故：悬空绑定会杀死其后全部绑定）。
func TestMailboxDeleteFrontendAssetsAreEmbedded(t *testing.T) {
	mailbox, err := embeddedWWW.ReadFile("wwwroot/js/mailbox.js")
	if err != nil {
		t.Fatalf("embedded mailbox.js: %v", err)
	}
	for _, marker := range []string{"deleteSelectedMailboxMails", "mailbox-select", "mailbox-delete-force"} {
		if !strings.Contains(string(mailbox), marker) {
			t.Fatalf("mailbox.js is missing %s", marker)
		}
	}
	rawIndex, err := embeddedWWW.ReadFile("wwwroot/index.html")
	if err != nil {
		t.Fatalf("embedded index.html: %v", err)
	}
	index := string(rawIndex)
	for _, id := range []string{"btn-mailbox-delete", "mailbox-delete-force"} {
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Fatalf("index.html is missing #%s", id)
		}
	}
	bindings, err := embeddedWWW.ReadFile("wwwroot/js/bindings.js")
	if err != nil {
		t.Fatalf("embedded bindings.js: %v", err)
	}
	if !strings.Contains(string(bindings), `$('#btn-mailbox-delete').onclick = deleteSelectedMailboxMails;`) {
		t.Fatal("bindings.js does not wire the P2 mailbox delete button")
	}
}
