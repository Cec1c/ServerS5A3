package gmtool

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// P4b：面板「删除物品」UI 接线到 `POST …/items/delete` 的静态检查。
//
// 本包没有 JS 运行时（浏览器行为由 P4b 的交付报告与人工验证负责），但下面三类
// 回归必须能在 `go test ./internal/services/gmtool` 里被钉死：
//
//	1. 前端又打回那两个**从未注册**的旧路径（`items/delete-at` / `items/batch-delete`）；
//	2. 前端偷偷把失败吞掉报成功（清空分类必须是逐格真调用 + 如实回报失败数）；
//	3. 前端出现一条 `gmtool.go` 路由注册表里**根本不存在**的 `/api/...` 路径
//	   （开关关闭档下这就是"点了没反应/文案对不上"的根因）。
//
// 强度与既有同类用例一致：`TestP3FrontendAssetsAreEmbedded`
// （api_account_cubes_test.go:676）、`TestP1FrontendAssetsAreEmbedded`
// （p1_route_surface_test.go:357）。
// ---------------------------------------------------------------------------

const (
	// p4bScriptAsset 是 P4b 唯一改动的前端资产（经 `//go:embed wwwroot` 随二进制发布）。
	p4bScriptAsset = "wwwroot/js/inventory.js"
	// p4bRouteSource 是路由注册表的唯一来源，按**文本**读出来做静态交叉检查
	// （不走 mux：P4 那两条是 `if s.cfg.RemoveInventory` 条件注册的，默认档下
	// 运行时根本不存在，但"注册行是否在源码里"始终可查）。
	p4bRouteSource = "gmtool.go"
)

// p4bNewDeleteRoutePattern / p4bNewClearRoutePattern 是 P4（J6）交付的两条新路由，
// 注册在 gmtool.go 的 `if s.cfg.RemoveInventory` 块里（gmtool.go:355-358）。
const (
	p4bNewDeleteRoutePattern = "/api/characters/*/items/delete"
	p4bNewClearRoutePattern  = "/api/characters/*/items/clear"
)

var (
	// `mux.HandleFunc("POST /api/…", …)` —— 只收带方法的 /api/ 注册行。
	p4bRouteRegistration = regexp.MustCompile(`mux\.HandleFunc\("(GET|POST|DELETE|PUT|PATCH) (/api/[^"]*)"`)
	// JS 里的 API 路径（本文件全部用反引号模板串；顺带兼容单双引号）。
	p4bScriptAPIPath = regexp.MustCompile("[`'\"](/api/[^`'\"[:space:]]*)")
	// 路由模式里的路径变量 `{id}` / `{templateId}` 与 JS 模板串里的 `${…}` 都归一成 `*`。
	p4bRoutePlaceholder  = regexp.MustCompile(`\{[^}]*\}`)
	p4bScriptPlaceholder = regexp.MustCompile(`\$\{[^}]*\}`)
)

// p4bLegacyUnregisteredScriptPaths 曾是 inventory.js 里**P1 时代就在、当时仍落 `/api/` 兜底**
// 的两条调用（配置面板的能力探测与写回）。本批已把这两条真正接线
// （`GET /api/characters/{id}/items/config-options`、`POST /api/characters/{id}/items/configure`，
// 见 api_items_configure.go），它们因此**不再需要豁免** —— 留空表，让下面那条
// "脚本里的每条路径都必须命中注册表"的正向断言继续生效。
var p4bLegacyUnregisteredScriptPaths = map[string]string{}

// p4bReadScript 读内嵌的前端资产：漏进 `//go:embed wwwroot` 就会在客户机上 404。
func p4bReadScript(t *testing.T) string {
	t.Helper()
	raw, err := embeddedWWW.ReadFile(p4bScriptAsset)
	if err != nil {
		t.Fatalf("embedded asset %s: %v", p4bScriptAsset, err)
	}
	if len(raw) == 0 {
		t.Fatalf("embedded asset %s 是空的", p4bScriptAsset)
	}
	return string(raw)
}

// p4bNormalizeAPIPath 归一化一条路径：去掉 query/fragment，把路径变量换成 `*`。
func p4bNormalizeAPIPath(path string) string {
	if cut := strings.IndexAny(path, "?#"); cut >= 0 {
		path = path[:cut]
	}
	// 顺序要紧：JS 的 `${x}` 必须先处理，否则会被 `\{[^}]*\}` 先吃成 `$*`。
	path = p4bScriptPlaceholder.ReplaceAllString(path, "*")
	path = p4bRoutePlaceholder.ReplaceAllString(path, "*")
	return strings.TrimSpace(path)
}

// p4bScriptCall 是脚本里的一次 API 调用：方法 + 归一化路径 + 路径在原文里的下标。
type p4bScriptCall struct {
	method string
	path   string
	at     int
}

// p4bScriptCalls 抽出脚本里每一次 `/api/...` 调用。方法按调用名判定：
// `post(…)` = POST，其余（`api(…)`，默认 GET）算 GET。
func p4bScriptCalls(script string) []p4bScriptCall {
	matches := p4bScriptAPIPath.FindAllStringSubmatchIndex(script, -1)
	calls := make([]p4bScriptCall, 0, len(matches))
	for _, match := range matches {
		start, end := match[2], match[3]
		prefixFrom := start - 48
		if prefixFrom < 0 {
			prefixFrom = 0
		}
		prefix := script[prefixFrom:start]
		method := "GET"
		if at := strings.LastIndex(prefix, "post("); at >= 0 {
			if api := strings.LastIndex(prefix, "api("); api < at {
				method = "POST"
			}
		}
		calls = append(calls, p4bScriptCall{
			method: method,
			path:   p4bNormalizeAPIPath(script[start:end]),
			at:     start,
		})
	}
	return calls
}

// p4bEnclosingCall 返回包含下标 at 的那次调用的原文（从最近的 `(` 到配对的 `)`），
// 用来断言"调用时确实带了 container/count/listType/slot"。
func p4bEnclosingCall(script string, at int) string {
	open := strings.LastIndex(script[:at], "(")
	if open < 0 {
		return ""
	}
	depth := 0
	for i := open; i < len(script); i++ {
		switch script[i] {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
			if depth == 0 {
				return script[open : i+1]
			}
		}
	}
	return script[open:]
}

// p4bFunctionBody 返回 `function <name>(` 之后那段花括号配对的主体（含花括号）。
// 中文字符是多字节但都 ≥ 0x80，不会与 `{`/`}` 的字节混淆，所以按字节扫是安全的。
func p4bFunctionBody(script, name string) string {
	signature := strings.Index(script, "function "+name+"(")
	if signature < 0 {
		return ""
	}
	open := strings.Index(script[signature:], "{")
	if open < 0 {
		return ""
	}
	open += signature
	depth := 0
	for i := open; i < len(script); i++ {
		switch script[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return script[open : i+1]
			}
		}
	}
	return script[open:]
}

// p4bRegisteredRoutes 把 gmtool.go 当文本读出来，抽出全部 `"<METHOD> /api/…"` 注册行。
func p4bRegisteredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(p4bRouteSource)
	if err != nil {
		t.Fatalf("读路由注册表 %s: %v", p4bRouteSource, err)
	}
	routes := map[string]bool{}
	for _, match := range p4bRouteRegistration.FindAllStringSubmatch(string(raw), -1) {
		routes[match[1]+" "+p4bNormalizeAPIPath(match[2])] = true
	}
	if len(routes) == 0 {
		t.Fatalf("%s 里一条 /api/ 路由都没抽到（提取正则与注册写法失配）", p4bRouteSource)
	}
	return routes
}

// TestP4bInventoryScriptDropsLegacyDeletePaths 钉死"不再打旧路径"。
func TestP4bInventoryScriptDropsLegacyDeletePaths(t *testing.T) {
	script := p4bReadScript(t)

	for _, legacy := range []string{"items/delete-at", "items/batch-delete"} {
		if strings.Contains(script, legacy) {
			t.Fatalf("%s 仍然出现旧路径 %q：那两条路由从未注册，开关打开后 UI 也依然用不了",
				p4bScriptAsset, legacy)
		}
	}
	// 旧批量契约的响应字段也不该再被读（新的单格 delete 回的是 deleted/remaining/entry）。
	if strings.Contains(script, "failedCount") {
		t.Fatalf("%s 仍在读旧批量契约的 failedCount", p4bScriptAsset)
	}
	// 新路径 + 固定 container（依据写在 INVENTORY_CONTAINER 的注释里）必须都在。
	if !strings.Contains(script, "/api/characters/${currentChar.characterId}/items/delete") {
		t.Fatalf("%s 没有调用新的 POST …/items/delete", p4bScriptAsset)
	}
	if !strings.Contains(script, "const INVENTORY_CONTAINER = 'inventory';") {
		t.Fatalf("%s 没有把 container 固定成 'inventory'（api_items_delete.go:102 的 itemContainerInventory）", p4bScriptAsset)
	}
}

// TestP4bDeleteCallsCarryContainerAndCount 钉死每一次删除调用都带齐新契约的四个字段。
//
// 契约（api_items_delete.go 的 D1/D5）：`count` 缺失会被直接拒绝（指针区分"没给"与
// "给了 0"），`container` 与 `listType` 必须自洽 —— 少任何一个都是"点了必失败"。
func TestP4bDeleteCallsCarryContainerAndCount(t *testing.T) {
	script := p4bReadScript(t)

	var deleteCalls []p4bScriptCall
	for _, call := range p4bScriptCalls(script) {
		if call.path == p4bNewDeleteRoutePattern {
			deleteCalls = append(deleteCalls, call)
		}
	}
	if len(deleteCalls) < 2 {
		t.Fatalf("%s 里 items/delete 的调用点只有 %d 个，期望 ≥2（单行删除 + 清空分类逐格删除）",
			p4bScriptAsset, len(deleteCalls))
	}
	for _, call := range deleteCalls {
		if call.method != "POST" {
			t.Fatalf("items/delete 的调用点用了 %s，必须是 POST", call.method)
		}
		source := p4bEnclosingCall(script, call.at)
		for _, marker := range []string{"container: INVENTORY_CONTAINER", "listType: item.listType", "slot: item.slot", "count: 0"} {
			if !strings.Contains(source, marker) {
				t.Fatalf("items/delete 的调用缺 %q：%s", marker, strings.TrimSpace(source))
			}
		}
	}
}

// TestP4bClearCategoryDeletesOneByOneAndReportsFailures 钉死清空分类的逐格语义。
//
// 服务端没有批量端点，所以"清空当前分类"必须是**逐格** POST …/items/delete，
// 并且：中途某个请求抛错 → 立即停止 + 原样显示服务端文案（不编"已清空"）；
// 聚合出来的失败数必须进 toast 并让 toast 变成错误态（绝不吞掉失败报成功）。
func TestP4bClearCategoryDeletesOneByOneAndReportsFailures(t *testing.T) {
	script := p4bReadScript(t)
	body := p4bFunctionBody(script, "clearCurrentCategory")
	if body == "" {
		t.Fatalf("在 %s 里找不到 clearCurrentCategory 的函数体", p4bScriptAsset)
	}
	for _, marker := range []string{
		"for (const item of targets)",    // 逐格，不是一次批量
		"items/delete",                   // 打新路由
		"container: INVENTORY_CONTAINER", // 带 container
		"count: 0",                       // 0 = 整栈（缺字段会被服务端拒绝）
		"toast(e.message, true)",         // 失败原样透出服务端文案
		"if (deleted > 0) loadItems();",  // 半途停止后把真实现状刷回来
		"已清空 ${deleted} 件",               // 沿用既有文案形状
		"失败 ${failed}",                   // 失败数如实显示
		"failed > 0",                     // 有失败才用错误态
		"loadItems();",                   // 收尾照样刷新
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("clearCurrentCategory 缺少 %q", marker)
		}
	}
	// 失败数只能来自"服务端说成功却没删掉"的如实判定，不能凭空写死。
	if !strings.Contains(body, "Number(result?.deleted) > 0") {
		t.Fatal("clearCurrentCategory 没有按服务端回报的 deleted 判定成功/失败")
	}
	// 单行删除按钮的行为必须与今天一致：toast('已删除') → 清配置 → 刷新。
	table := p4bFunctionBody(script, "renderItemTable")
	if table == "" {
		t.Fatalf("在 %s 里找不到 renderItemTable 的函数体", p4bScriptAsset)
	}
	for _, marker := range []string{
		"items/delete",
		"container: INVENTORY_CONTAINER",
		"count: 0",
		"toast('已删除')",
		"clearInventoryConfiguration();",
		"loadItems();",
		"toast(e.message, true)",
	} {
		if !strings.Contains(table, marker) {
			t.Fatalf("renderItemTable 的删除按钮缺少 %q", marker)
		}
	}
}

// TestP4bScriptDoesNotProbeTheServerSwitch 钉死"开关关闭档不许有前端特判"。
//
// 两条写路由是 `if s.cfg.RemoveInventory` 条件注册的（gmtool.go:355-358）。前端一旦
// 去探测开关，就会出现"开关关着时按钮被隐藏/被禁用"这种**默认档可观察变化**；
// 新路径落 `/api/` 兜底 + 原样弹服务端文案才是要求的行为。删除按钮的存在条件
// 仍然只由数据里的 `deletable` 决定（api_items.go:582）。
func TestP4bScriptDoesNotProbeTheServerSwitch(t *testing.T) {
	script := p4bReadScript(t)
	for _, forbidden := range []string{"DNFBRIDGE_GM_ITEM_REMOVE", "RemoveInventory", "requireItemRemove"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("%s 出现了 %q：前端不得探测服务端开关（关着时行为必须与今天一致）",
				p4bScriptAsset, forbidden)
		}
	}
	cell := p4bFunctionBody(script, "renderInventoryActionCell")
	if cell == "" {
		t.Fatalf("在 %s 里找不到 renderInventoryActionCell 的函数体", p4bScriptAsset)
	}
	for _, marker := range []string{`if (item.deletable)`, `data-act="delete"`} {
		if !strings.Contains(cell, marker) {
			t.Fatalf("删除按钮的渲染条件变了（缺 %q）：按钮不许因开关状态被隐藏", marker)
		}
	}
}

// TestP4bScriptAPIPathsAreRegistered 钉死"脚本打的每一条 /api/ 路径都有注册"。
//
// 这就是本批要修的 bug 的一般形式：`items/delete-at` 与 `items/batch-delete` 从来没被
// 注册过（见 api_items_delete_test.go 的兜底用例），于是按钮永远只回一句"未移植"。
// 两条新路由（items/delete、items/clear）必须能从 gmtool.go 的注册行里匹配到。
func TestP4bScriptAPIPathsAreRegistered(t *testing.T) {
	routes := p4bRegisteredRoutes(t)
	for _, want := range []string{
		"POST " + p4bNewDeleteRoutePattern,
		"POST " + p4bNewClearRoutePattern,
	} {
		if !routes[want] {
			t.Fatalf("%s 里没有注册 %q（P4 的两条写路由）", p4bRouteSource, want)
		}
	}

	script := p4bReadScript(t)
	calls := p4bScriptCalls(script)
	if len(calls) == 0 {
		t.Fatalf("%s 里一条 /api/ 调用都没抽到（提取正则与前端写法失配）", p4bScriptAsset)
	}
	seenLegacy := map[string]bool{}
	for _, call := range calls {
		key := call.method + " " + call.path
		if routes[key] {
			continue
		}
		reason, allowed := p4bLegacyUnregisteredScriptPaths[call.path]
		if allowed {
			seenLegacy[call.path] = true
			t.Logf("已记账的遗留未注册调用（%s）: %s %s", reason, call.method, call.path)
			continue
		}
		t.Errorf("%s 调用了未注册的路径 %s（gmtool.go 的路由表里没有）", p4bScriptAsset, key)
	}
	// 豁免表不能腐化：里面每一条都必须仍然真的出现在脚本里。
	for path, reason := range p4bLegacyUnregisteredScriptPaths {
		if !seenLegacy[path] {
			t.Errorf("豁免表里的 %s（%s）在 %s 里已经不存在了，请删掉这条豁免", path, reason, p4bScriptAsset)
		}
	}
	// 删除路径必须**精确**落在新路由上（不是打了某个前缀相似的路由）。
	deleteSeen := false
	for _, call := range calls {
		if call.path == p4bNewDeleteRoutePattern {
			deleteSeen = true
		}
	}
	if !deleteSeen {
		t.Fatalf("%s 没有调用 %s", p4bScriptAsset, p4bNewDeleteRoutePattern)
	}
}
