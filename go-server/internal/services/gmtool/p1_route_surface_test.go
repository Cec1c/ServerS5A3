package gmtool

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// 「既有路由零变化」的可复跑证明
// ---------------------------------------------------------------------------
//
// P1 只改了既有代码的三个地方，全部与既有请求的可观察行为无关：
//
//	1. items_index.go：把 parseItemEntry 内部的路径拼接抽成 itemEntry.documentPath()
//	   （纯重构，见 TestItemEntryDocumentPathIsPureRefactor），并给 5 个只读方法加
//	   nil 守卫（Server 在测试里没装配物品索引时返回"查不到"而不是 panic）。
//	2. items_index.go：itemSearchQuery 新增 Group 字段 + matchItemEntry 新增一条
//	   `query.Group != ""` 的短路分支。Group 为空时该分支不参与判断，
//	   见 TestItemBrowseFilterIsUnchangedWhenGroupIsEmpty 的逐组合差分。
//	3. api_items.go：把 `group` 查询参数读进 search.Group。前端没有任何调用带这个
//	   参数（grep wwwroot 的 `group=` / `group:` 零命中）。
//
// 下面用两个可复跑的方式把它钉死：谓词差分 + 真实 mux 的「不被兜底吞掉」探针。

// legacyMatchItemEntry 是 P1 改动**之前**的 matchItemEntry 原文（逐字复制，
// 只去掉新增的 Group 分支）。它就是「既有调用者看到的行为」的定义。
func legacyMatchItemEntry(entry itemEntry, query itemSearchQuery, now int64) bool {
	if query.Kind != "" && entry.Kind != query.Kind {
		return false
	}
	if len(query.Tags) > 0 && !containsFold(query.Tags, entry.Tag) {
		return false
	}
	if len(query.Segments) > 0 && !containsFold(query.Segments, entry.Segment) {
		return false
	}
	if query.Special != "" && !strings.EqualFold(entry.Special, query.Special) {
		return false
	}
	if query.Rarity >= 0 && entry.Rarity != query.Rarity {
		return false
	}
	if query.MinLevel > 0 && entry.MinLevel < query.MinLevel {
		return false
	}
	if query.MaxLevel > 0 && entry.MinLevel > query.MaxLevel {
		return false
	}
	if query.Q != "" {
		if numeric, err := parseUint32ForTest(query.Q); err == nil {
			if numeric != entry.ID {
				return false
			}
		} else if !strings.Contains(strings.ToLower(entry.Name), strings.ToLower(query.Q)) {
			return false
		}
	}
	if query.UsableJob != -1 && entry.Kind == "equipment" {
		switch {
		case query.UsableJob == -2:
			if !isUnrestrictedUsableJob(entry.UsableJob) {
				return false
			}
		case query.UsableJob >= 0:
			if !usableByJob(entry.UsableJob, query.UsableJob) {
				return false
			}
		}
	}
	if !matchItemExpiration(entry, query.Expiration, now) {
		return false
	}
	return true
}

// TestItemBrowseFilterIsUnchangedWhenGroupIsEmpty 穷举「既有过滤参数 × 条目」的组合，
// 断言新谓词与改动前的谓词在 Group 为空时**逐组合同结果**。既有前端从不发送
// `group`，所以既有调用者看到的搜索结果集必然逐条相同。
func TestItemBrowseFilterIsUnchangedWhenGroupIsEmpty(t *testing.T) {
	entries := []itemEntry{
		{ID: 1, Name: "金币", Kind: "stackable", Tag: "material", Segment: "材料", Rarity: 0, MinLevel: 1, UsableJob: "`[all]`"},
		{ID: 2, Name: "HP 药水", Kind: "stackable", Tag: "consumable", Segment: "消耗品", Rarity: 1, MinLevel: 5, UsablePeriodDays: 7},
		{ID: 3, Name: "稀有剑", Kind: "equipment", Tag: "weapon", Rarity: 3, MinLevel: 50, UsableJob: "`[swordman]`", Special: "boss"},
		{ID: 4, Name: "稀有装扮帽", Kind: "equipment", Tag: "hat avatar", Rarity: 2, MinLevel: 1, UsableJob: "`[all]`"},
		{ID: 5, Name: "宠物蛋", Kind: "equipment", Tag: "creature", Rarity: 4, MinLevel: 1, UsableJob: ""},
		{ID: 6, Name: "称号", Kind: "equipment", Tag: "title name", Rarity: -1, MinLevel: 1, UsableJob: "`[any]`"},
		{ID: 7, Name: "奇怪的东西", Kind: "equipment", Tag: "", Rarity: -1},
		{ID: 8, Name: "过期券", Kind: "stackable", Tag: "quest", Segment: "任务品", Rarity: 0, AbsoluteExpireUnix: 1000},
	}
	queries := []itemSearchQuery{
		{},
		{Kind: "stackable"},
		{Kind: "equipment"},
		{Tags: []string{"weapon"}},
		{Tags: []string{"WEAPON", "creature"}},
		{Segments: []string{"材料"}},
		{Special: "boss"},
		{Special: "legacy"},
		{Rarity: -1},
		{Rarity: 0},
		{Rarity: 3},
		{MinLevel: 50},
		{MaxLevel: 1},
		{MinLevel: 10, MaxLevel: 60},
		{Q: "金币"},
		{Q: "3"},
		{Q: "99"},
		{Q: "稀有"},
		{UsableJob: -1},
		{UsableJob: -2},
		{UsableJob: 0},
		{UsableJob: 30},
		{Expiration: "limited"},
		{Expiration: "none"},
		{Expiration: "relative"},
		{Expiration: "absolute"},
		{Expiration: "expired"},
		{Expiration: "all"},
		{Kind: "stackable", Rarity: 0, MinLevel: 1, Expiration: "none", Q: "币"},
		{Kind: "equipment", UsableJob: -2, Tags: []string{"hat avatar"}, Rarity: 2},
	}
	const now = int64(2_000_000_000)
	compared := 0
	for _, query := range queries {
		if query.Group != "" {
			t.Fatalf("this differential only covers the legacy shape (Group empty): %+v", query)
		}
		for _, entry := range entries {
			compared++
			got := matchItemEntry(entry, query, now)
			want := legacyMatchItemEntry(entry, query, now)
			if got != want {
				t.Fatalf("item %d query %+v: new=%t legacy=%t", entry.ID, query, got, want)
			}
		}
	}
	if compared != len(queries)*len(entries) {
		t.Fatalf("compared %d combinations, want %d", compared, len(queries)*len(entries))
	}
}

func parseUint32ForTest(value string) (uint32, error) {
	var out uint32
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, &readonlyTestMissingPath{path: value}
		}
		out = out*10 + uint32(ch-'0')
	}
	return out, nil
}

// TestItemEntryDocumentPathIsPureRefactor 钉死抽取出来的路径拼接与改动前逐字等价。
func TestItemEntryDocumentPathIsPureRefactor(t *testing.T) {
	cases := []struct {
		kind, path, want string
	}{
		{"stackable", "coin.stk", "stackable/coin.stk"},
		{"stackable", "cash/river.stk", "stackable/cash/river.stk"},
		{"stackable", "stackable/coin.stk", "stackable/coin.stk"},
		{"stackable", "Stackable/Coin.stk", "Stackable/Coin.stk"},
		{"stackable", "/coin.stk", "stackable/coin.stk"},
		{"equipment", "sword.equ", "equipment/sword.equ"},
		{"equipment", "weapon/sword.equ", "equipment/weapon/sword.equ"},
		{"equipment", "equipment/weapon/sword.equ", "equipment/weapon/sword.equ"},
	}
	for _, testCase := range cases {
		entry := itemEntry{Kind: testCase.kind, PVFPath: testCase.path}
		// 改动前的公式。
		legacy := testCase.path
		if !strings.HasPrefix(strings.ToLower(legacy), strings.ToLower(testCase.kind)+"/") {
			legacy = testCase.kind + "/" + strings.TrimPrefix(legacy, "/")
		}
		if got := entry.documentPath(); got != legacy {
			t.Fatalf("kind=%s path=%s: documentPath=%q legacy=%q", testCase.kind, testCase.path, got, legacy)
		}
		if got := entry.documentPath(); got != testCase.want {
			t.Fatalf("kind=%s path=%s: documentPath=%q want %q", testCase.kind, testCase.path, got, testCase.want)
		}
	}
}

// existingReadonlyProbes 是既有（P1 之前就注册的）只读路由里**不依赖 s.db** 的那些。
// 带会话 Cookie 打真实 mux：命中自己的 handler 就不会返回「该功能未移植」兜底。
var existingReadonlyProbes = []struct {
	name   string
	target string
	values map[string]string
}{
	{"角色详情", "/api/characters/1", map[string]string{"id": "1"}},
	{"角色属性", "/api/characters/1/stats", map[string]string{"id": "1"}},
	{"SP/TP", "/api/characters/1/sptp", map[string]string{"id": "1"}},
	{"转职选项", "/api/characters/1/growoptions", map[string]string{"id": "1"}},
	{"金币上限", "/api/characters/1/gold-limit", map[string]string{"id": "1"}},
	{"物品分类", "/api/items/categories", nil},
	{"物品检索", "/api/items/browse", nil},
	{"物品搜索", "/api/items/search?q=%E6%B5%8B%E8%AF%95", nil},
	{"角色背包", "/api/characters/1/items", map[string]string{"id": "1"}},
	{"发放能力块", "/api/characters/1/items/1/grant-options", map[string]string{"id": "1", "templateId": "1"}},
}

func TestExistingReadonlyRoutesStillResolveToTheirOwnHandlers(t *testing.T) {
	harness := questReadonlyHarness(t)
	handler := harness.server.routes() // 注册冲突会在这里 panic（Go 1.22 ServeMux 语义）
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	for _, probe := range existingReadonlyProbes {
		request := httptest.NewRequest(http.MethodGet, probe.target, nil)
		for key, value := range probe.values {
			request.SetPathValue(key, value)
		}
		request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s: %s -> 404，既有路由被新路由遮住了", probe.name, probe.target)
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s: %s 落到了 /api/ 兜底：%s", probe.name, probe.target, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "\"") {
			t.Fatalf("%s: %s 返回了非 JSON: %s", probe.name, probe.target, recorder.Body.String())
		}
	}
}

func TestP1AddedRoutesRequireSession(t *testing.T) {
	harness := questReadonlyHarness(t)
	handler := harness.server.routes()
	newRoutes := []struct {
		target string
		values map[string]string
	}{
		{"/api/items/1/detail", map[string]string{"itemId": "1"}},
		{"/api/characters/1/mailbox", map[string]string{"id": "1"}},
		{"/api/characters/1/quests", map[string]string{"id": "1"}},
		{"/api/characters/1/quests/search?q=1", map[string]string{"id": "1"}},
		{"/api/characters/1/quests/3971/detail", map[string]string{"id": "1", "questId": "3971"}},
	}
	for _, route := range newRoutes {
		request := httptest.NewRequest(http.MethodGet, route.target, nil)
		for key, value := range route.values {
			request.SetPathValue(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", route.target, recorder.Code)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "\"loginRequired\":true") {
			t.Fatalf("%s: body = %s, want loginRequired:true", route.target, body)
		}
	}
	// 带会话时同一条路由必须放行到自己的 handler（不是 401、也不是兜底）。
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	for _, route := range newRoutes {
		request := httptest.NewRequest(http.MethodGet, route.target, nil)
		for key, value := range route.values {
			request.SetPathValue(key, value)
		}
		request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s", route.target, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s: 落到了 /api/ 兜底: %s", route.target, recorder.Body.String())
		}
	}
}

func TestItemGroupBuckets(t *testing.T) {
	// 分桶只看装备类型的第一个 [tag]：上游 gmItemGroup 的同一张表。
	cases := map[string]string{
		"creature":       "pet",
		"artifact red":   "pet",
		"artifact blue":  "pet",
		"artifact green": "pet",
		"hat avatar":     "avatar",
		"weapon avatar":  "avatar",
		"shoes avatar":   "avatar",
		"weapon":         "equipment",
		"magic stone":    "equipment",
		"support weapon": "equipment",
		"title name":     "equipment",
		"name tag":       "equipment",
		"":               "other",
		"unknown tag":    "other",
		// 消耗品 tag 长在装备 kind 上是异常形态，落 other，不硬塞进 stackable。
		"consumable": "other",
	}
	for tag, want := range cases {
		if got := itemGroup(itemEntry{Kind: "equipment", Tag: tag}); got != want {
			t.Fatalf("equipment tag %q -> %q, want %q", tag, got, want)
		}
	}
	// stackable 一律进 stackable 桶，与 tag 无关（上游 gmItemGroup 同口径）。
	if got := itemGroup(itemEntry{Kind: "stackable", Tag: "weapon"}); got != "stackable" {
		t.Fatalf("stackable kind -> %q, want stackable", got)
	}
	if got := itemGroup(itemEntry{Kind: "stackable", Tag: ""}); got != "stackable" {
		t.Fatalf("tagless stackable kind -> %q, want stackable", got)
	}
}

func TestItemBrowseGroupFilterNarrowsResults(t *testing.T) {
	harness := newReadonlyHarness(t, readonlyFixture{
		source: readonlyItemSource(readonlyTestWeapon, nil),
		character: dnfrepo.CharacterRecord{
			AccountID: "acc-1", Name: "tester", Job: "11", Level: 90, Stats: map[string]int64{},
		},
	})
	all := browseIDs(t, harness, "")
	if len(all) != 2 {
		t.Fatalf("unfiltered browse = %v, want both fixture items", all)
	}
	if ids := browseIDs(t, harness, "&group=stackable"); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("group=stackable -> %v, want [1]", ids)
	}
	if ids := browseIDs(t, harness, "&group=equipment"); len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("group=equipment -> %v, want [2]", ids)
	}
	if ids := browseIDs(t, harness, "&group=other"); len(ids) != 0 {
		t.Fatalf("group=other -> %v, want empty", ids)
	}
	// 上游的显式逃生口。
	if ids := browseIDs(t, harness, "&group=all"); len(ids) != len(all) {
		t.Fatalf("group=all -> %v, want the unfiltered set %v", ids, all)
	}
}

func browseIDs(t *testing.T, harness *readonlyHarness, suffix string) []uint32 {
	t.Helper()
	_, response := harness.get(t, "/api/items/browse?limit=50"+suffix, nil, harness.server.handleItemBrowse)
	requireOK(t, http.StatusOK, response)
	results := responseList(t, response, "results")
	ids := make([]uint32, 0, len(results))
	for _, entry := range results {
		ids = append(ids, uint32(entry.(map[string]any)["itemId"].(float64)))
	}
	return ids
}

// TestP1FrontendAssetsAreEmbedded 保证新只读页随二进制发布：cfg.WWWRoot 为空时
// 前端从 //go:embed wwwroot 读取，漏掉一个文件就会在客户机上 404。
func TestP1FrontendAssetsAreEmbedded(t *testing.T) {
	for _, asset := range []string{"wwwroot/js/item-detail.js", "wwwroot/js/mailbox.js", "wwwroot/js/quests.js"} {
		if _, err := embeddedWWW.ReadFile(asset); err != nil {
			t.Fatalf("embedded asset %s: %v", asset, err)
		}
	}
	raw, err := embeddedWWW.ReadFile("wwwroot/index.html")
	if err != nil {
		t.Fatalf("embedded index.html: %v", err)
	}
	index := string(raw)
	for _, marker := range []string{
		`data-tab="itemlookup"`, `data-tab="mailbox"`, `data-tab="questlog"`,
		`id="tab-itemlookup"`, `id="tab-mailbox"`, `id="tab-questlog"`,
		`src="js/item-detail.js"`, `src="js/mailbox.js"`, `src="js/quests.js"`,
	} {
		if !strings.Contains(index, marker) {
			t.Fatalf("index.html is missing %s", marker)
		}
	}
	// bindings.js 引用的每个新元素都必须在 index.html 里真实存在，
	// 否则又是一次「悬空绑定杀死其后全部绑定」的事故（见 bindings.js 顶部注释）。
	bindings, err := embeddedWWW.ReadFile("wwwroot/js/bindings.js")
	if err != nil {
		t.Fatalf("embedded bindings.js: %v", err)
	}
	for _, id := range []string{
		"item-detail-query", "btn-item-detail-query", "item-detail-candidates", "item-detail-body",
		"btn-refresh-mailbox", "mailbox-body",
		"btn-refresh-questlog", "questlog-query", "btn-questlog-search",
		"questlog-search-results", "questlog-body", "questlog-detail",
	} {
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Fatalf("bindings.js references #%s but index.html has no such element", id)
		}
	}
	if !strings.Contains(string(bindings), "loadItemLookupTab") ||
		!strings.Contains(string(bindings), "loadMailboxTab") ||
		!strings.Contains(string(bindings), "loadQuestLogTab") {
		t.Fatal("bindings.js does not wire the three P1 read-only tabs")
	}
}
