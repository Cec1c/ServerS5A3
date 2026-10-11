package gmtool

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// P4 / J6：背包单格删除 + 整容器清空（POST …/items/delete、…/items/clear）
//
// 测试重点按任务书的硬性要求排序：
//  1. **默认档零变化**（(a)）：两条路由不存在、落 /api/ 兜底、探针 0 次、
//     守卫日志 0 条、仓储 0 写入；
//  2. **开关只认字面 `on`**（(b)）；
//  3. 上游 gm_extended.go:507-657 / :978-990 的逐条语义（c1..c8）；
//  4. 计划书红线 C11 的有意偏离（(d)：账号记录缺失必须报错）；
//  5. 两个开关的正交性（(e)）。
// ---------------------------------------------------------------------------

const (
	p4SwordID = 1001
	p4Potion  = 1002
	p4Cube    = 1003
	p4Coin    = 1004
)

// p4Source 是四个物品的 PVF 测试替身（只保留物品索引真正读的段）。
func p4Source() readonlyTestSource {
	return readonlyTestSource{
		"stackable/material/potion.stk": "[name]\n`红色药水` \n[stackable type]\n`[consumable]` 4 \n",
		"stackable/material/cube.stk":   "[name]\n`黑色小晶块` \n[stackable type]\n`[material]` 4 \n",
		"stackable/material/coin.stk":   "[name]\n`复活币` \n[stackable type]\n`[material]` 4 \n",
		"equipment/weapon/sword.equ":    "[name]\n`测试短剑` \n[equipment type]\n`[weapon]` \n",
	}
}

func p4Entries(source readonlyTestSource) []itemEntry {
	raw := []itemEntry{
		{ID: p4SwordID, Kind: "equipment", PVFPath: "weapon/sword.equ"},
		{ID: p4Potion, Kind: "stackable", PVFPath: "material/potion.stk"},
		{ID: p4Cube, Kind: "stackable", PVFPath: "material/cube.stk"},
		{ID: p4Coin, Kind: "stackable", PVFPath: "material/coin.stk"},
	}
	entries := make([]itemEntry, 0, len(raw))
	for _, entry := range raw {
		item := entry
		parseItemEntry(source, &item)
		entries = append(entries, item)
	}
	return entries
}

// p4Fixture 是 J6 测试台的输入。
type p4Fixture struct {
	// remove 决定 cfg.RemoveInventory —— 也就是 DNFBRIDGE_GM_ITEM_REMOVE=on 与否。
	remove bool
	// slots / warehouse 是角色两个容器的预置内容。
	slots     map[string]dnfrepo.ItemStack
	warehouse map[string]dnfrepo.ItemStack
	// accountSlots 是账号共享背包（key 形如 "0:354"）。
	accountSlots map[string]dnfrepo.ItemStack
	// dropInventoryRecord 为真时连 Inventory 记录都不写（模拟"角色没有背包记录"）。
	dropInventoryRecord bool
	// dropAccountRecord 为真时不写账号共享背包记录（C11 场景）。
	dropAccountRecord bool
	// dropCharacterItems / dropAccountItems 置空对应的 UoW（模拟事务能力缺失）。
	dropCharacterItems bool
	dropAccountItems   bool
	// accountID 覆盖角色归属账号（空串 = 角色没有归属账号）。
	accountID *string
	// source 覆盖 PVF 替身。
	source readonlyTestSource
}

// p4AccountSharedSlotFixture 预置三个账号共享槽（晶块 0:354、灵魂 0:360），
// 用来验证 D4：清空 inventory 不许碰它们。
func p4AccountSharedSlotFixture() map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot): {
			ItemID: p4Cube, Count: 30,
			Extra: map[string]string{"item_kind": "stackable"},
		},
		dnfrepo.AccountSharedInventorySlotKey(dnfrepo.SoulWarehouseFirstSlot): {
			ItemID: p4Cube, Count: 7,
		},
	}
}

func newP4Harness(t *testing.T, fixture p4Fixture) *gmtoolWriteHarness {
	t.Helper()
	accountID := "acc-1"
	if fixture.accountID != nil {
		accountID = *fixture.accountID
	}
	harness := newGmtoolWriteHarness(t, nil)

	source := fixture.source
	if source == nil {
		source = p4Source()
	}
	harness.server.deps.PVF = func() (dnfpvf.Source, error) { return source, nil }
	harness.source = source

	index := newItemIndex("", harness.server, harness.server.cfg.Logger)
	index.mu.Lock()
	index.setEntriesLocked(p4Entries(source))
	index.mu.Unlock()
	harness.server.items = index

	// 角色归属账号：newGmtoolWriteHarness 落的是 "acc-1"，这里按夹具覆盖，
	// 好让"角色没有归属账号"这一条也能被测到（P3 的账号晶块测试同样这么做）。
	if accountID != "acc-1" || fixture.accountID != nil {
		record, found, err := harness.group.Character.Load(context.Background(), "1")
		if err != nil || !found {
			t.Fatalf("load character: found=%t err=%v", found, err)
		}
		record.AccountID = accountID
		if err := harness.group.Character.Save(context.Background(), record); err != nil {
			t.Fatalf("save character account: %v", err)
		}
	}

	if !fixture.dropInventoryRecord {
		slots := fixture.slots
		if slots == nil {
			slots = map[string]dnfrepo.ItemStack{}
		}
		warehouse := fixture.warehouse
		if warehouse == nil {
			warehouse = map[string]dnfrepo.ItemStack{}
		}
		if err := harness.group.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: "1", Slots: slots, Warehouse: warehouse,
		}); err != nil {
			t.Fatalf("save inventory: %v", err)
		}
	}
	if !fixture.dropAccountRecord {
		accountSlots := fixture.accountSlots
		if accountSlots == nil {
			accountSlots = map[string]dnfrepo.ItemStack{}
		}
		if err := harness.group.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
			AccountID: accountID, Slots: accountSlots,
		}); err != nil {
			t.Fatalf("save account inventory: %v", err)
		}
	}

	harness.server.cfg.RemoveInventory = fixture.remove
	if fixture.dropCharacterItems || fixture.dropAccountItems {
		group := harness.group
		if fixture.dropCharacterItems {
			group.CharacterItems = nil
		}
		if fixture.dropAccountItems {
			group.AccountItems = nil
		}
		harness.server.deps.Repos = func() (dnfrepo.Group, bool) { return group, true }
	}
	return harness
}

func (h *gmtoolWriteHarness) deleteItem(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/1/items/delete", map[string]string{"id": "1"},
		body, h.server.handleItemRemove)
}

func (h *gmtoolWriteHarness) clearItems(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/1/items/clear", map[string]string{"id": "1"},
		body, h.server.handleItemClear)
}

func (h *gmtoolWriteHarness) inventory(t *testing.T) dnfrepo.InventoryRecord {
	t.Helper()
	record, found, err := h.group.Inventory.Load(context.Background(), "1")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if !found {
		return dnfrepo.InventoryRecord{CharacterID: "1"}
	}
	return record
}

func (h *gmtoolWriteHarness) p4AccountInventory(t *testing.T) dnfrepo.AccountInventoryRecord {
	t.Helper()
	record, found, err := h.group.AccountInventory.Load(context.Background(), "acc-1")
	if err != nil {
		t.Fatalf("load account inventory: %v", err)
	}
	if !found {
		return dnfrepo.AccountInventoryRecord{AccountID: "acc-1"}
	}
	return record
}

// inventoryFingerprint 是"库内容哈希"：只用暴露的结构字段算，不用
// reflect.DeepEqual（后者的结果会被 time.Time 的单调时钟读数影响 ——
// 一次合法写入把 UpdatedAt 设成 time.Now().UTC()，DeepEqual 比较会看到
// wall clock+monotonic，用它做"变没变"的判据会产生假阳性）。
func inventoryFingerprint(record dnfrepo.InventoryRecord) string {
	return stackMapFingerprint(record.Slots) + "#" + stackMapFingerprint(record.Warehouse)
}

func stackMapFingerprint(entries map[string]dnfrepo.ItemStack) string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		stack := entries[key]
		fmt.Fprintf(&builder, "%s=%d/%d/%t/%d/%s/%s|",
			key, stack.ItemID, stack.Count, stack.Bind, stack.ExpireAt.UTC().Unix(),
			hex.EncodeToString(stack.RawEntry), stackExtraFingerprint(stack.Extra))
	}
	return builder.String()
}

func stackExtraFingerprint(extra map[string]string) string {
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(extra[key])
		builder.WriteByte(';')
	}
	return builder.String()
}

// requireInventoryUnchanged 断言角色背包内容与仓库内容**一个字段都没变**。
func (h *gmtoolWriteHarness) requireInventoryUnchanged(t *testing.T, before string) {
	t.Helper()
	if after := inventoryFingerprint(h.inventory(t)); after != before {
		t.Fatalf("角色背包被改动了（本用例应当零写入）:\nbefore=%s\nafter =%s", before, after)
	}
}

func responseEntry(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	return responseMap(t, response, "entry")
}

func responseRemoved(t *testing.T, response map[string]any) []map[string]any {
	t.Helper()
	raw := responseList(t, response, "removed")
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("removed 元素不是对象: %+v", item)
		}
		rows = append(rows, row)
	}
	return rows
}

// ---------------------------------------------------------------------------
// (b) 开关解析：只认一个字面量 `on`
// ---------------------------------------------------------------------------

func TestItemRemoveFromEnvOnlyLiteralOn(t *testing.T) {
	// 两端裁剪后逐字节等于 `on`（大小写不敏感）都算开启 —— 与
	// RequireOfflineFromEnv（online_guard.go:61-63）逐字同一条规则。
	enabled := []string{"on", "ON", "On", "oN", " on ", "\ton\n", "on\t"}
	for _, value := range enabled {
		if !RequireItemRemoveFromEnv(value) {
			t.Fatalf("RequireItemRemoveFromEnv(%q) = false, want true", value)
		}
	}
	disabled := []string{
		"", " ", "\t", "\n", "off", "OFF", "Off", " off ",
		"true", "TRUE", "True", "1", "0", "yes", "no", "y", "n",
		"enable", "enabled", "disable", "disabled", "onn", "no", "on,off",
		"oon", "open", "ONN", "tru", "2", "-1", "on;off", "on off", "on/off",
		"只有 on", "on!", "「on」",
	}
	for _, value := range disabled {
		if RequireItemRemoveFromEnv(value) {
			t.Fatalf("RequireItemRemoveFromEnv(%q) = true, want false（本仓开关只认字面 on）", value)
		}
	}
}

func TestItemRemoveFromEnvironmentReadsProcessEnv(t *testing.T) {
	t.Setenv(itemRemoveEnv, "on")
	if !RequireItemRemoveFromEnvironment() {
		t.Fatal("DNFBRIDGE_GM_ITEM_REMOVE=on 必须启用")
	}
	t.Setenv(itemRemoveEnv, "true")
	if RequireItemRemoveFromEnvironment() {
		t.Fatal("DNFBRIDGE_GM_ITEM_REMOVE=true 必须仍然是关闭（只认字面 on）")
	}
	t.Setenv(itemRemoveEnv, "")
	if RequireItemRemoveFromEnvironment() {
		t.Fatal("DNFBRIDGE_GM_ITEM_REMOVE 空值必须关闭")
	}
}

// 开关名与计划书 §4.4 的建议必须逐字相同（写错一个字符就等于开关永远关）。
func TestItemRemoveEnvNameMatchesThePlan(t *testing.T) {
	if itemRemoveEnv != "DNFBRIDGE_GM_ITEM_REMOVE" {
		t.Fatalf("开关名 = %q, want DNFBRIDGE_GM_ITEM_REMOVE", itemRemoveEnv)
	}
}

// ---------------------------------------------------------------------------
// (a) 默认档零变化：路由不存在、探针 0 次、守卫日志 0 条、零写入
// ---------------------------------------------------------------------------

// 默认档（cfg.RemoveInventory == false，即 DNFBRIDGE_GM_ITEM_REMOVE 未设）
// 下两条路由**一行都没注册**：请求必须继续落 /api/ 兜底，且带会话时也一样。
func TestItemRemoveDefaultOffRoutesAreAbsent(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		slots:     map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
		warehouse: map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}},
	})
	handler := harness.server.routes() // 注册冲突会在这里 panic
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	probes := []struct {
		name   string
		target string
		body   string
	}{
		{"items/delete", "/api/characters/1/items/delete", `{"container":"inventory","key":"0:12","count":0}`},
		{"items/clear", "/api/characters/1/items/clear", `{"container":"inventory"}`},
		{"items/batch-delete（前端同族路径，故意保持兜底）", "/api/characters/1/items/batch-delete", `{"items":[]}`},
		{"items/delete-at（前端同族路径，故意保持兜底）", "/api/characters/1/items/delete-at", `{"listType":0,"slot":12,"count":0}`},
	}
	for _, probe := range probes {
		// 未登录：门禁仍然是 401（兜底在门禁之后）。
		request := httptest.NewRequest(http.MethodPost, probe.target, strings.NewReader(probe.body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未登录 status = %d, want 401", probe.name, recorder.Code)
		}
		// 带会话：必须命中兜底文案。
		request = httptest.NewRequest(http.MethodPost, probe.target, strings.NewReader(probe.body))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", probe.name, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s 默认档没有落 /api/ 兜底（这就是「默认档不是今天的字节」）: %s",
				probe.name, recorder.Body.String())
		}
	}
}

// 默认档直接把 handler 调起来（探针与日志的行为在这个层面最清楚）：
// 不允许任何写入、任何探针调用、任何守卫日志。
func TestItemRemoveDefaultOffHandlerWritesNothing(t *testing.T) {
	for _, build := range []struct {
		name    string
		fixture p4Fixture
	}{
		{"delete", p4Fixture{
			slots:     map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
			warehouse: map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}},
		}},
		{"clear", p4Fixture{
			slots:     map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
			warehouse: map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}},
		}},
	} {
		t.Run(build.name, func(t *testing.T) {
			probeCalls := 0
			harness := newP4Harness(t, build.fixture)
			// 连守卫开关也一并打开 —— 依然不许有任何探针调用，因为默认档下
			// 这两条路由的存在性本身被开关关掉了（前端不会走到 handler）。
			harness.server.deps.Online = func(uint16) bool { probeCalls++; return true }
			before := inventoryFingerprint(harness.inventory(t))

			var status int
			var response map[string]any
			if build.name == "delete" {
				status, response = harness.deleteItem(t, map[string]any{
					"container": "inventory", "key": "0:12", "count": 0,
				})
			} else {
				status, response = harness.clearItems(t, map[string]any{"container": "inventory"})
			}
			// 第二层守卫：就算绕过路由直接调 handler，也必须逐字返回兜底文案。
			if message := requireFailure(t, status, response); message != unportedFeatureMessage {
				t.Fatalf("关闭档的 handler 文案 = %q, want 与 /api/ 兜底逐字相同 %q",
					message, unportedFeatureMessage)
			}
			harness.requireInventoryUnchanged(t, before)
			if probeCalls != 0 {
				t.Fatalf("默认档不许碰在线探针，实际调用 %d 次", probeCalls)
			}
			if kinds := harness.kindsWithPrefix("gm-online-guard"); len(kinds) != 0 {
				t.Fatalf("默认档不许产生在线守卫日志: %v", kinds)
			}
			if kinds := harness.kindsWithPrefix("gm-item-remove"); len(kinds) != 0 {
				t.Fatalf("默认档不许产生物品移除日志: %v", kinds)
			}
		})
	}
}

// 开关**必须**由操作者显式打开：cfg.RemoveInventory=false 时哪怕环境变量是
// `on` 也不注册（配置是唯一真源，环境只由装配点读取）。
func TestItemRemoveRoutesOnlyExistWhenConfigIsOn(t *testing.T) {
	t.Setenv(itemRemoveEnv, "on")
	harness := newP4Harness(t, p4Fixture{
		slots: map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	if harness.server.cfg.RemoveInventory {
		t.Fatal("测试台默认必须是关闭档")
	}
	handler := harness.server.routes()
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/characters/1/items/delete",
		strings.NewReader(`{"container":"inventory","key":"0:12","count":0}`))
	request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
		t.Fatalf("环境变量不该单独注册路由（cfg 才是真源）: %s", recorder.Body.String())
	}
}

// 开启档：两条路由必须命中自己的 handler（不是兜底、不是 404），未登录仍 401。
func TestItemRemoveRoutesRequireSessionWhenEnabled(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	handler := harness.server.routes()
	probes := []struct {
		name   string
		target string
		body   string
	}{
		{"items/delete", "/api/characters/1/items/delete", `{"container":"inventory","key":"0:12","count":1}`},
		{"items/clear", "/api/characters/1/items/clear", `{"container":"warehouse"}`},
	}
	for _, probe := range probes {
		request := httptest.NewRequest(http.MethodPost, probe.target, strings.NewReader(probe.body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", probe.name, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), `"loginRequired":true`) {
			t.Fatalf("%s: body = %s", probe.name, recorder.Body.String())
		}
	}
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	for _, probe := range probes {
		request := httptest.NewRequest(http.MethodPost, probe.target, strings.NewReader(probe.body))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s", probe.name, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s: 落到了 /api/ 兜底: %s", probe.name, recorder.Body.String())
		}
	}
}

// 新路由不许把既有的同名前缀路由遮住（尤其是 /items 与 /items/{templateId}/grant-options）。
func TestItemRemoveRoutesDoNotShadowExistingItemRoutes(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	handler := harness.server.routes()
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	probes := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/characters/1/items"},
		{http.MethodGet, "/api/characters/1/items/1002/grant-options"},
		{http.MethodGet, "/api/items/categories"},
		{http.MethodGet, "/api/items/browse?limit=1"},
	}
	for _, probe := range probes {
		request := httptest.NewRequest(probe.method, probe.target, nil)
		request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s -> 404，既有路由被新路由遮住了", probe.target)
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s 落到了兜底: %s", probe.target, recorder.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// (c) 删除语义（上游 gm_extended.go:507-582 + gmRemoveStack :978-990）
// ---------------------------------------------------------------------------

// c1：count=0 -> 整栈删除，连同 Extra 一起丢。
func TestItemRemoveCountZeroDeletesWholeStackWithExtra(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots: map[string]dnfrepo.ItemStack{
			"0:12": {
				ItemID: p4SwordID, Count: 1, Bind: true,
				Extra: map[string]string{"durability": "57", "pvf_path": "weapon/sword.equ"},
			},
			"0:13": {ItemID: p4Potion, Count: 9},
		},
	})
	status, response := harness.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:12", "count": 0,
	})
	requireOK(t, status, response)
	if response["deleted"] != float64(1) || response["remaining"] != float64(1) {
		t.Fatalf("deleted/remaining = %v/%v, want 1/1", response["deleted"], response["remaining"])
	}
	entry := responseEntry(t, response)
	if entry["itemId"] != float64(p4SwordID) || entry["name"] != "测试短剑" ||
		entry["count"] != float64(1) || entry["wholeStack"] != true || entry["extraDropped"] != true {
		t.Fatalf("entry = %+v", entry)
	}
	slots := harness.inventory(t).Slots
	if _, exists := slots["0:12"]; exists {
		t.Fatalf("整栈删除后 0:12 还在: %+v", slots["0:12"])
	}
	if slots["0:13"].Count != 9 {
		t.Fatalf("没选中的槽位被改动了: %+v", slots["0:13"])
	}
	fields := harness.eventFields(t, "gm-item-remove")
	if fields["character_id"] != "1" || fields["container"] != "inventory" ||
		fields["key"] != "0:12" || fields["item_id"] != int64(p4SwordID) ||
		fields["count"] != int64(1) || fields["result"] != "applied" ||
		fields["whole_stack"] != true || fields["extra_dropped"] != true ||
		fields["slot"] != 12 || fields["list_type"] != 0 {
		t.Fatalf("审计事件字段不全: %+v", fields)
	}
}

// c2：0 < count < Count -> 原地递减，Extra/RawEntry/ExpireAt/Bind 全部保留。
func TestItemRemovePartialKeepsExtraAndMetadata(t *testing.T) {
	expire := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots: map[string]dnfrepo.ItemStack{
			"0:12": {
				ItemID: p4Potion, Count: 9, Bind: true, ExpireAt: expire,
				RawEntry: []byte{0x77, 0x01, 0x02},
				Extra:    map[string]string{"durability": "57", "item_kind": "stackable"},
			},
		},
	})
	status, response := harness.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:12", "count": 4,
	})
	requireOK(t, status, response)
	if response["deleted"] != float64(4) || response["remaining"] != float64(1) {
		t.Fatalf("deleted/remaining = %v/%v, want 4/1", response["deleted"], response["remaining"])
	}
	entry := responseEntry(t, response)
	if entry["wholeStack"] != false || entry["extraDropped"] != false || entry["count"] != float64(4) {
		t.Fatalf("部分删除的 entry = %+v", entry)
	}
	stack := harness.inventory(t).Slots["0:12"]
	if stack.Count != 5 {
		t.Fatalf("递减后 count = %d, want 5", stack.Count)
	}
	if !stack.Bind || !stack.ExpireAt.Equal(expire) || len(stack.RawEntry) != 3 ||
		stack.Extra["durability"] != "57" || stack.Extra["item_kind"] != "stackable" {
		t.Fatalf("部分删除必须保留 Extra/RawEntry/ExpireAt/Bind: %+v", stack)
	}
}

// c2'：count >= Count 也是整栈删除（上游 :983 的 `count >= stack.Count`）。
func TestItemRemoveCountAtOrAboveStackDeletesWholeStack(t *testing.T) {
	for _, count := range []int{7, 8, 100} {
		harness := newP4Harness(t, p4Fixture{
			remove: true,
			slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 7}},
		})
		status, response := harness.deleteItem(t, map[string]any{
			"container": "inventory", "key": "0:12", "count": count,
		})
		requireOK(t, status, response)
		if response["deleted"] != float64(7) {
			t.Fatalf("count=%d: deleted = %v, want 7", count, response["deleted"])
		}
		if _, exists := harness.inventory(t).Slots["0:12"]; exists {
			t.Fatalf("count=%d: 整栈删除后 0:12 还在", count)
		}
	}
}

// c3：仓库走 Warehouse map（上游 :542-544），且 key 必须是 2:<slot>。
func TestItemRemoveWarehouseUsesWarehouseMapOnly(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove:    true,
		slots:     map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
		warehouse: map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}},
	})
	status, response := harness.deleteItem(t, map[string]any{
		"container": "warehouse", "key": "2:5", "count": 0,
	})
	requireOK(t, status, response)
	if response["remaining"] != float64(0) {
		t.Fatalf("remaining = %v, want 0（只剩仓库这一条）", response["remaining"])
	}
	record := harness.inventory(t)
	if _, exists := record.Warehouse["2:5"]; exists {
		t.Fatal("仓库物品没有被删除")
	}
	if record.Slots["0:12"].Count != 5 {
		t.Fatalf("角色背包被顺手改动了: %+v", record.Slots["0:12"])
	}
}

// c4：list_type 与 container 不一致必须被点名拒绝（本仓的 D5），且**零写入**。
func TestItemRemoveRejectsContainerAndListTypeMismatch(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove:    true,
		slots:     map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
		warehouse: map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}},
	})
	before := inventoryFingerprint(harness.inventory(t))
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"warehouse 里找 0:12", map[string]any{"container": "warehouse", "key": "0:12", "count": 0}, "list_type=2"},
		{"inventory 里找 2:5", map[string]any{"container": "inventory", "key": "2:5", "count": 0}, "list_type=0"},
		{"account 里找 0:12", map[string]any{"container": "account", "key": "0:12", "count": 0}, "0:354"},
		{"inventory 里找账号共享槽 0:354", map[string]any{"container": "inventory", "key": "0:354", "count": 0}, "container=account"},
	}
	for _, testCase := range cases {
		status, response := harness.deleteItem(t, testCase.body)
		message := requireFailure(t, status, response)
		if !strings.Contains(message, testCase.want) {
			t.Fatalf("%s: 拒绝文案 = %q, want 含 %q", testCase.name, message, testCase.want)
		}
	}
	harness.requireInventoryUnchanged(t, before)
	fields := harness.eventFields(t, "gm-item-remove-rejected")
	if fields["reason"] != "slot_mismatch" && fields["reason"] != "slot_not_in_container" {
		t.Fatalf("拒绝事件 reason = %v", fields["reason"])
	}
	if fields["result"] != "rejected" || fields["container"] == "" {
		t.Fatalf("拒绝事件字段不全: %+v", fields)
	}
}

// c5：equipment / 未知容器 -> 拒绝（上游 :575-576），零写入。
func TestItemRemoveRejectsEquipmentAndUnknownContainers(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	before := inventoryFingerprint(harness.inventory(t))
	for _, container := range []string{"equipment", "cube", "pet", "avatar", "guild", "nope", "  "} {
		status, response := harness.deleteItem(t, map[string]any{
			"container": container, "key": "0:12", "count": 0,
		})
		message := requireFailure(t, status, response)
		if !strings.Contains(message, "只读或不支持") {
			t.Fatalf("container=%q 的拒绝文案 = %q", container, message)
		}
	}
	harness.requireInventoryUnchanged(t, before)
	fields := harness.eventFields(t, "gm-item-remove-rejected")
	if fields["reason"] != "container_unsupported" && fields["reason"] != "slot_mismatch" {
		t.Fatalf("拒绝事件 reason = %v", fields["reason"])
	}
}

// c6：count 缺失 / 负数 / key 不存在 / 坏 key -> 拒绝且零写入（上游 :509、:980-982）。
func TestItemRemoveRejectsBadRequestsWithZeroWrites(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	before := inventoryFingerprint(harness.inventory(t))
	cases := []struct {
		name string
		body any
		want string
	}{
		{"缺 count（上游零值 = 整栈删除，最危险的缺省）", map[string]any{"container": "inventory", "key": "0:12"}, "缺少 count"},
		{"count 为负", map[string]any{"container": "inventory", "key": "0:12", "count": -1}, "不能为负"},
		{"key 不存在", map[string]any{"container": "inventory", "key": "0:99", "count": 0}, "不存在"},
		{"坏 key（没有冒号）", map[string]any{"container": "inventory", "key": "12", "count": 0}, "key 格式不正确"},
		{"坏 key（list_type 不是数字）", map[string]any{"container": "inventory", "key": "x:12", "count": 0}, "key 格式不正确"},
		{"坏 key（slot 越界）", map[string]any{"container": "inventory", "key": "0:99999", "count": 0}, "key 格式不正确"},
		{"key 与 listType 互相矛盾", map[string]any{"container": "inventory", "key": "0:12", "listType": 1, "slot": 12, "count": 0}, "listType"},
		{"既没有 key 也没有 listType/slot", map[string]any{"container": "inventory", "count": 0}, "key"},
		{"listType 越界", map[string]any{"container": "inventory", "listType": 999, "slot": 12, "count": 0}, "0..255"},
		{"slot 越界", map[string]any{"container": "inventory", "listType": 0, "slot": 99999, "count": 0}, "slot"},
	}
	for _, testCase := range cases {
		status, response := harness.deleteItem(t, testCase.body)
		message := requireFailure(t, status, response)
		if !strings.Contains(message, testCase.want) {
			t.Fatalf("%s: 拒绝文案 = %q, want 含 %q", testCase.name, message, testCase.want)
		}
	}
	harness.requireInventoryUnchanged(t, before)
}

// 未知 JSON 字段必须报错，不能静默当成 key=""（本仓的 D1）。
func TestItemRemoveRejectsUnknownBodyFields(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	before := inventoryFingerprint(harness.inventory(t))
	status, response := harness.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:12", "count": 0, "deleteEverything": true,
	})
	message := requireFailure(t, status, response)
	if !strings.Contains(message, "未知字段") {
		t.Fatalf("未知字段的拒绝文案 = %q", message)
	}
	harness.requireInventoryUnchanged(t, before)
}

// c7：角色没有背包记录 / 事务能力缺失 -> 明确失败（上游 :537-539、:529-531）。
func TestItemRemoveRejectsMissingInventoryRecordAndTransaction(t *testing.T) {
	missing := newP4Harness(t, p4Fixture{remove: true, dropInventoryRecord: true})
	status, response := missing.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:12", "count": 0,
	})
	if message := requireFailure(t, status, response); !strings.Contains(message, "背包记录") {
		t.Fatalf("缺记录时的拒绝文案 = %q", message)
	}
	fields := missing.eventFields(t, "gm-item-remove-rejected")
	if fields["reason"] != "inventory_record_missing" {
		t.Fatalf("reason = %v, want inventory_record_missing", fields["reason"])
	}

	noTx := newP4Harness(t, p4Fixture{
		remove: true, dropCharacterItems: true,
		slots: map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	before := inventoryFingerprint(noTx.inventory(t))
	status, response = noTx.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:12", "count": 0,
	})
	if message := requireFailure(t, status, response); !strings.Contains(message, "事务能力未就绪") {
		t.Fatalf("缺事务能力时的拒绝文案 = %q", message)
	}
	noTx.requireInventoryUnchanged(t, before)
}

// c8：账号容器 —— 走 AccountInventory（上游 :551-574），成功时返回账号侧剩余条数。
func TestItemRemoveAccountContainerDeletesAccountSharedSlot(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove:       true,
		accountSlots: p4AccountSharedSlotFixture(),
		slots:        map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	crystalKey := dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot)
	status, response := harness.deleteItem(t, map[string]any{
		"container": "account", "key": crystalKey, "count": 0,
	})
	requireOK(t, status, response)
	if response["remaining"] != float64(1) {
		t.Fatalf("账号共享背包 remaining = %v, want 1", response["remaining"])
	}
	entry := responseEntry(t, response)
	if entry["itemId"] != float64(p4Cube) || entry["name"] != "黑色小晶块" || entry["count"] != float64(30) {
		t.Fatalf("entry = %+v", entry)
	}
	account := harness.p4AccountInventory(t)
	if _, exists := account.Slots[crystalKey]; exists {
		t.Fatal("账号共享槽没有被删除")
	}
	if account.Slots[dnfrepo.AccountSharedInventorySlotKey(dnfrepo.SoulWarehouseFirstSlot)].Count != 7 {
		t.Fatal("没选中的账号槽被改动了")
	}
	// 角色侧副本一个字节都不许动。
	if harness.inventory(t).Slots["0:12"].Count != 5 {
		t.Fatal("账号容器的删除动了角色背包")
	}
}

// c8'：账号容器 —— 账号记录缺失 -> **明确失败**（上游 :565-567 的删除分支口径）。
func TestItemRemoveAccountContainerRejectsMissingAccountRecord(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{remove: true, dropAccountRecord: true})
	crystalKey := dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot)
	status, response := harness.deleteItem(t, map[string]any{
		"container": "account", "key": crystalKey, "count": 0,
	})
	if message := requireFailure(t, status, response); !strings.Contains(message, "共享背包记录") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	fields := harness.eventFields(t, "gm-item-remove-rejected")
	if fields["reason"] != "account_inventory_missing" {
		t.Fatalf("reason = %v", fields["reason"])
	}
}

// c8”：角色没有归属账号 -> 明确失败（不猜一个账号出来）。
func TestItemRemoveAccountContainerRejectsCharacterWithoutAccount(t *testing.T) {
	empty := ""
	harness := newP4Harness(t, p4Fixture{
		remove: true, accountID: &empty, dropAccountRecord: true,
	})
	crystalKey := dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot)
	status, response := harness.deleteItem(t, map[string]any{
		"container": "account", "key": crystalKey, "count": 0,
	})
	if message := requireFailure(t, status, response); !strings.Contains(message, "归属账号") {
		t.Fatalf("拒绝文案 = %q", message)
	}
}

// 前端形状（listType/slot）必须能直接工作 —— 这是"以后接前端"的前提。
func TestItemRemoveAcceptsFrontendCamelCaseShape(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	status, response := harness.deleteItem(t, map[string]any{"listType": 0, "slot": 12, "count": 0})
	requireOK(t, status, response)
	if response["key"] != "0:12" {
		t.Fatalf("归一化后的 key = %v, want 0:12", response["key"])
	}
	if _, exists := harness.inventory(t).Slots["0:12"]; exists {
		t.Fatal("前端形状没有被删除")
	}

	warehouse := newP4Harness(t, p4Fixture{
		remove:    true,
		warehouse: map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}},
	})
	status, response = warehouse.deleteItem(t, map[string]any{"listType": 2, "slot": 5, "count": 1})
	requireOK(t, status, response)
	if response["container"] != "warehouse" || response["deleted"] != float64(1) {
		t.Fatalf("listType=2 的归一化结果 = %+v", response)
	}
	if warehouse.inventory(t).Warehouse["2:5"].Count != 2 {
		t.Fatal("listType=2 递减失败")
	}
}

// ---------------------------------------------------------------------------
// (c) 整容器清空（上游 gm_extended.go:584-657）
// ---------------------------------------------------------------------------

// 清空 inventory：只删真正属于本角色的槽，账号共享槽必须留下并如实回报 skipped。
func TestItemClearInventoryKeepsAccountSharedSlotsAndReportsSkipped(t *testing.T) {
	accountSlots := p4AccountSharedSlotFixture()
	slots := map[string]dnfrepo.ItemStack{
		"0:12": {ItemID: p4Potion, Count: 5, Extra: map[string]string{"item_kind": "stackable"}},
		"0:13": {ItemID: p4Coin, Count: 2},
	}
	for key, stack := range accountSlots {
		slots[key] = stack
	}
	harness := newP4Harness(t, p4Fixture{
		remove:       true,
		slots:        slots,
		warehouse:    map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}},
		accountSlots: accountSlots,
	})
	status, response := harness.clearItems(t, map[string]any{"container": "inventory"})
	requireOK(t, status, response)
	if response["deleted"] != float64(2) {
		t.Fatalf("deleted = %v, want 2", response["deleted"])
	}
	if response["skipped"] != float64(2) {
		t.Fatalf("skipped = %v, want 2（两个账号共享槽）", response["skipped"])
	}
	if response["remaining"] != float64(2) {
		t.Fatalf("remaining = %v, want 2（跳过的条数）", response["remaining"])
	}
	removed := responseRemoved(t, response)
	if len(removed) != 2 || removed[0]["key"] != "0:12" || removed[1]["key"] != "0:13" {
		t.Fatalf("removed = %+v", removed)
	}
	if removed[0]["extraDropped"] != true || removed[0]["wholeStack"] != true {
		t.Fatalf("整容器清空必须如实标注 extra 被销毁: %+v", removed[0])
	}

	record := harness.inventory(t)
	if len(record.Slots) != 2 {
		t.Fatalf("清空后 Slots 条数 = %d, want 2（只剩账号共享槽）: %+v", len(record.Slots), record.Slots)
	}
	for key := range accountSlots {
		if _, exists := record.Slots[key]; !exists {
			t.Fatalf("账号共享槽 %s 被角色容器清空顺手删掉了（见 D4）", key)
		}
	}
	if len(record.Warehouse) != 1 {
		t.Fatalf("清空 inventory 不许碰 Warehouse: %+v", record.Warehouse)
	}
	fields := harness.eventFields(t, "gm-item-remove")
	if fields["operation"] != "clear" || fields["container"] != "inventory" ||
		fields["deleted"] != 2 || fields["skipped"] != int64(2) || fields["result"] != "applied" {
		t.Fatalf("审计事件字段不全: %+v", fields)
	}
}

// 清空 warehouse：整张 Warehouse 清空，Slots 不动。
func TestItemClearWarehouseEmptiesTheWarehouseMap(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove:    true,
		slots:     map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
		warehouse: map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}, "2:6": {ItemID: p4Coin, Count: 1}},
	})
	status, response := harness.clearItems(t, map[string]any{"container": "warehouse"})
	requireOK(t, status, response)
	if response["deleted"] != float64(2) || response["skipped"] != float64(0) || response["remaining"] != float64(0) {
		t.Fatalf("清空仓库的计数 = %+v", response)
	}
	record := harness.inventory(t)
	if len(record.Warehouse) != 0 {
		t.Fatalf("仓库没清空: %+v", record.Warehouse)
	}
	if record.Slots["0:12"].Count != 5 {
		t.Fatal("清空仓库动了角色背包")
	}
}

// (d) **计划书 §5.2 红线 C11 / D3**：清空 account 容器且账号记录缺失时，
// 上游 gm_extended.go:642-644 会静默成功；本仓**明确失败**。
func TestItemClearMissingAccountRecordFailsInsteadOfPretendingSuccess(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{remove: true, dropAccountRecord: true})

	status, response := harness.clearItems(t, map[string]any{"container": "account"})
	message := requireFailure(t, status, response)
	if !strings.Contains(message, "共享背包记录") {
		t.Fatalf("拒绝文案 = %q, want 点名账号共享背包记录缺失", message)
	}
	if !strings.Contains(message, "未做任何写入") {
		t.Fatalf("拒绝文案必须声明零写入: %q", message)
	}
	if _, hasDeleted := response["deleted"]; hasDeleted {
		t.Fatalf("拒绝响应不许带 deleted（那会被读成「静默假成功」）: %+v", response)
	}
	fields := harness.eventFields(t, "gm-item-remove-rejected")
	if fields["reason"] != "account_inventory_missing" || fields["operation"] != "clear" {
		t.Fatalf("拒绝事件字段 = %+v", fields)
	}

	// 对照：账号记录**存在但为空**时是正常成功（deleted=0）—— 区分"缺失"与"空"。
	present := newP4Harness(t, p4Fixture{remove: true})
	status, response = present.clearItems(t, map[string]any{"container": "account"})
	requireOK(t, status, response)
	if response["deleted"] != float64(0) || response["skipped"] != float64(0) {
		t.Fatalf("空账号背包的清空结果 = %+v", response)
	}
}

// 清空 account：整张账号共享背包清空，角色侧副本不动。
func TestItemClearAccountEmptiesAccountSharedInventory(t *testing.T) {
	accountSlots := p4AccountSharedSlotFixture()
	slots := map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}}
	for key, stack := range accountSlots {
		slots[key] = stack
	}
	harness := newP4Harness(t, p4Fixture{
		remove: true, slots: slots, accountSlots: accountSlots,
	})
	status, response := harness.clearItems(t, map[string]any{"container": "account"})
	requireOK(t, status, response)
	if response["deleted"] != float64(2) {
		t.Fatalf("deleted = %v, want 2", response["deleted"])
	}
	if len(harness.p4AccountInventory(t).Slots) != 0 {
		t.Fatal("账号共享背包没清空")
	}
	// 陈旧的账号槽副本仍在角色侧（这是本仓 D4 的边界：请用 account 容器清它们）。
	if len(harness.inventory(t).Slots) != 3 {
		t.Fatalf("清空账号容器动了角色侧键: %+v", harness.inventory(t).Slots)
	}
}

// 清空 inventory / warehouse：角色背包记录缺失 -> 明确失败，零写入。
func TestItemClearRejectsMissingInventoryRecord(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{remove: true, dropInventoryRecord: true})
	for _, container := range []string{"inventory", "warehouse"} {
		status, response := harness.clearItems(t, map[string]any{"container": container})
		if message := requireFailure(t, status, response); !strings.Contains(message, "背包记录") {
			t.Fatalf("container=%s 的拒绝文案 = %q", container, message)
		}
	}
}

// 清空：空 container / 只读容器 -> 拒绝，零写入。
func TestItemClearRejectsEmptyAndReadOnlyContainers(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	before := inventoryFingerprint(harness.inventory(t))
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"空 container", map[string]any{}, "只读或不支持"},
		{"container=equipment", map[string]any{"container": "equipment"}, "只读或不支持"},
		{"container=cube", map[string]any{"container": "cube"}, "只读或不支持"},
		{"container=pet", map[string]any{"container": "pet"}, "只读或不支持"},
	}
	for _, testCase := range cases {
		status, response := harness.clearItems(t, testCase.body)
		message := requireFailure(t, status, response)
		if !strings.Contains(message, testCase.want) {
			t.Fatalf("%s: 拒绝文案 = %q", testCase.name, message)
		}
	}
	harness.requireInventoryUnchanged(t, before)

	// 未知字段同样拒绝（整容器清空是"不可分级"的操作，多给一个 count 是误解）。
	status, response := harness.clearItems(t, map[string]any{"container": "inventory", "count": 0})
	if message := requireFailure(t, status, response); !strings.Contains(message, "未知字段") {
		t.Fatalf("clear 带 count 的拒绝文案 = %q", message)
	}
	harness.requireInventoryUnchanged(t, before)
}

// 清空：空容器 -> 成功 + deleted=0（不假报条数）。
func TestItemClearEmptyContainerSucceedsWithZeroDeleted(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{remove: true})
	for _, container := range []string{"inventory", "warehouse", "account"} {
		status, response := harness.clearItems(t, map[string]any{"container": container})
		requireOK(t, status, response)
		if response["deleted"] != float64(0) {
			t.Fatalf("container=%s 的 deleted = %v, want 0", container, response["deleted"])
		}
	}
}

// ---------------------------------------------------------------------------
// (e) 两个开关正交：RequireOffline 只管守卫，绝不改变"路由在不在"
// ---------------------------------------------------------------------------

// 关闭档 + 守卫开关 on：路由仍然不存在（守卫开关不能把 P4 路由带出来）。
func TestItemRemoveRoutesStayAbsentEvenWhenGuardIsOn(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		slots: map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(uint16) bool { return false }
	handler := harness.server.routes()
	token, err := harness.server.startSession()
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/characters/1/items/delete",
		strings.NewReader(`{"container":"inventory","key":"0:12","count":0}`))
	request.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
		t.Fatalf("守卫开关把 P4 路由带出来了: %s", recorder.Body.String())
	}
	if kinds := harness.kindsWithPrefix("gm-online-guard"); len(kinds) != 0 {
		t.Fatalf("默认档不许产生守卫日志: %v", kinds)
	}
}

// 开启档 + 守卫 off（默认）→ 在线角色照样能删；**探针 0 次**、守卫日志 0 条。
func TestItemRemoveKeepsWorkingWhenGuardIsOffEvenIfCharacterOnline(t *testing.T) {
	probeCalls := 0
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	harness.server.cfg.RequireOffline = false
	harness.server.deps.Online = func(uint16) bool { probeCalls++; return true }

	status, response := harness.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:12", "count": 0,
	})
	requireOK(t, status, response)
	if _, exists := harness.inventory(t).Slots["0:12"]; exists {
		t.Fatal("守卫关闭时应当照常删除")
	}
	if probeCalls != 0 {
		t.Fatalf("守卫关闭时连探针都不该碰，实际 %d 次", probeCalls)
	}
	if kinds := harness.kindsWithPrefix("gm-online-guard"); len(kinds) != 0 {
		t.Fatalf("守卫关闭时不该有守卫日志: %v", kinds)
	}
}

// 开启档 + 守卫 on + 角色在线 → 拒绝且**零写入**（HTTP 200 + success:false）。
func TestItemRemoveRejectsOnlineCharacterWithZeroWrites(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 1 }
	before := inventoryFingerprint(harness.inventory(t))

	status, response := harness.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:12", "count": 0,
	})
	if message := requireFailure(t, status, response); !strings.Contains(message, "在线") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireInventoryUnchanged(t, before)
	fields := harness.eventFields(t, "gm-online-guard-rejected")
	if fields["character_id"] != uint16(1) || fields["action"] != "items-delete" {
		t.Fatalf("守卫事件字段 = %+v", fields)
	}
	if harness.hasEvent("gm-item-remove") {
		t.Fatalf("被守卫拦下时不该留下 gm-item-remove 成功事件: %v", harness.eventKinds())
	}
}

// 账号容器：账号内**其它**角色在线 → 拒绝（上游 :555-559，J0 的账号级用法）。
func TestItemRemoveAccountPathRejectsWhenAnotherAccountCharacterIsOnline(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove:       true,
		accountSlots: p4AccountSharedSlotFixture(),
	})
	// 给同一个账号再加一个角色（角色号 2）。
	second := dnfrepo.CharacterRecord{
		CharacterID: "2", AccountID: "acc-1", Name: "other", Job: "11", Level: 90,
		Stats: map[string]int64{},
	}
	if err := harness.group.Character.Save(context.Background(), second); err != nil {
		t.Fatalf("save second character: %v", err)
	}
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 2 }
	before := harness.p4AccountInventory(t)

	crystalKey := dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot)
	status, response := harness.deleteItem(t, map[string]any{
		"container": "account", "key": crystalKey, "count": 0,
	})
	if message := requireFailure(t, status, response); !strings.Contains(message, "在线") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireAccountInventoryUnchanged(t, before)
	fields := harness.eventFields(t, "gm-online-guard-rejected")
	if fields["reason"] != "account_character_online" || fields["account_id"] != "acc-1" {
		t.Fatalf("账号级守卫事件字段 = %+v", fields)
	}
}

// 守卫 on 但角色不在线 → 正常删除（守卫不是"一律拒绝"）。
func TestItemRemoveAllowsOfflineCharacterWhenGuardIsOn(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots:  map[string]dnfrepo.ItemStack{"0:12": {ItemID: p4Potion, Count: 5}},
	})
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(uint16) bool { return false }
	status, response := harness.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:12", "count": 0,
	})
	requireOK(t, status, response)
	if _, exists := harness.inventory(t).Slots["0:12"]; exists {
		t.Fatal("离线角色的删除应当放行")
	}
}

// 键的编解码与反射基线一致，避免两套 key 方言。
func TestItemSlotKeyMatchesRepositoryHelpers(t *testing.T) {
	if got := itemSlotKey(0, 354); got != dnfrepo.AccountSharedInventorySlotKey(354) {
		t.Fatalf("itemSlotKey(0,354) = %q, want %q", got, dnfrepo.AccountSharedInventorySlotKey(354))
	}
	if got := itemSlotKey(2, 5); got != "2:5" {
		t.Fatalf("itemSlotKey(2,5) = %q, want 2:5", got)
	}
	listType, slot, err := parseItemSlotKey("0:354")
	if err != nil || listType != 0 || slot != 354 {
		t.Fatalf("parseItemSlotKey(0:354) = %d,%d,%v", listType, slot, err)
	}
	if _, _, err := parseItemSlotKey("0"); err == nil {
		t.Fatal("没有冒号的 key 必须报错")
	}
}

// 清空的"删哪些键"判据必须与仓库层的不变量一致（item_normalization_audit.go:163-175）。
func TestItemContainerClearRemovesMatchesRepositoryInvariants(t *testing.T) {
	cases := []struct {
		container string
		field     dnfrepo.InventoryField
		key       string
		want      bool
	}{
		{"inventory", dnfrepo.InventoryFieldSlots, "0:12", true},
		{"inventory", dnfrepo.InventoryFieldSlots, "0:354", false}, // 账号共享槽（晶块）
		{"inventory", dnfrepo.InventoryFieldSlots, "0:360", false}, // 账号共享槽（灵魂）
		{"inventory", dnfrepo.InventoryFieldSlots, "0:365", false},
		{"inventory", dnfrepo.InventoryFieldSlots, "0:366", true}, // 边界之外仍是角色侧
		{"inventory", dnfrepo.InventoryFieldSlots, "1:3", false},  // 装扮不属于本容器
		{"inventory", dnfrepo.InventoryFieldSlots, "7:1", false},  // 宠物不属于本容器
		{"inventory", dnfrepo.InventoryFieldSlots, "38:1", false}, // 勋章不属于本容器
		{"inventory", dnfrepo.InventoryFieldSlots, "坏键", false},   // 看不懂就不删
		{"warehouse", dnfrepo.InventoryFieldWarehouse, "2:5", true},
		{"warehouse", dnfrepo.InventoryFieldWarehouse, "0:12", true}, // 该 map 只承载 list 2，整体清空
		{"account", dnfrepo.InventoryFieldSlots, "0:354", true},
	}
	for _, testCase := range cases {
		got := itemContainerClearRemoves(testCase.container, testCase.field, testCase.key)
		if got != testCase.want {
			t.Fatalf("container=%s key=%s: %t, want %t", testCase.container, testCase.key, got, testCase.want)
		}
	}
}

// 排序稳定：removed 按 (listType, slot) 升序，不随 Go map 随机序抖动。
func TestItemClearRemovedEntriesAreSorted(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{}
	for _, key := range []string{"0:30", "0:5", "0:12", "0:7", "0:99"} {
		slots[key] = dnfrepo.ItemStack{ItemID: p4Potion, Count: 1}
	}
	harness := newP4Harness(t, p4Fixture{remove: true, slots: slots})
	for attempt := 0; attempt < 4; attempt++ {
		fresh := newP4Harness(t, p4Fixture{remove: true, slots: slots})
		status, response := fresh.clearItems(t, map[string]any{"container": "inventory"})
		requireOK(t, status, response)
		removed := responseRemoved(t, response)
		slotsSeen := make([]int, 0, len(removed))
		for _, row := range removed {
			slotsSeen = append(slotsSeen, int(row["slot"].(float64)))
		}
		if !sort.IntsAreSorted(slotsSeen) {
			t.Fatalf("removed 未按 slot 升序: %v", slotsSeen)
		}
		_ = harness
	}
}

// 零写入的最强证据形态：拒绝路径的 map 指纹与调用前**逐字段相同**
// （用 reflect.DeepEqual 兜一层，确保指纹函数本身没有漏字段）。
func TestItemRemoveRejectionLeavesMapsDeepEqual(t *testing.T) {
	harness := newP4Harness(t, p4Fixture{
		remove: true,
		slots: map[string]dnfrepo.ItemStack{
			"0:12": {ItemID: p4Potion, Count: 5, Bind: true, Extra: map[string]string{"k": "v"}},
		},
		warehouse: map[string]dnfrepo.ItemStack{"2:5": {ItemID: p4Cube, Count: 3}},
	})
	before := harness.inventory(t)
	status, response := harness.deleteItem(t, map[string]any{
		"container": "inventory", "key": "0:99", "count": 0,
	})
	requireFailure(t, status, response)
	after := harness.inventory(t)
	if !reflect.DeepEqual(before.Slots, after.Slots) || !reflect.DeepEqual(before.Warehouse, after.Warehouse) {
		t.Fatalf("拒绝路径改动了容器内容:\nbefore=%+v\nafter=%+v", before, after)
	}
}
