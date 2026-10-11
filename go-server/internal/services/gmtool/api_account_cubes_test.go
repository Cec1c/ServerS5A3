package gmtool

import (
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// P3 / J7：账号晶块 6 格读/写（GET/POST /api/characters/{id}/account-cubes）
//
// 六个槽位的物品（selection 0..5 = 黑/白/红/蓝/无色/金，真档文档名分别是
// 黑色/白色/红色/蓝色/无色/金色小晶块）与 `[stackable type]` 原文都来自真档取证；
// `TestRealPVFCubeFixtureMatchesTheDump` 会在真档上逐条重验，
// 这里的小夹具只是同一份形状的测试替身（不发明运行时数据）。
// ---------------------------------------------------------------------------

const (
	p3CubeBlack = 3033
	p3CubeWhite = 3034
	p3CubeRed   = 3035
	p3CubeBlue  = 3036
	p3CubeClear = 3037
	p3CubeGold  = 3262
	// p3WireRowSize 是当前客户端的物品行长度，与 dnfbridge 的
	// currentItemListEntryWireSize 同值（0x77）。
	p3WireRowSize = 0x77
)

// p3CubeBlackDocument 逐字复制真档 `stackable/material/cubepiece_black.stk`
// （`pvfdump -path stackable/material/cubepiece_black.stk`，464 字节）。
const p3CubeBlackDocument = "[name]\n`黑色小晶块` \n" +
	"[explain]\n`完成任务所需的材料， 同时可以用于晶之契约。 (分解装备时可以获得该道具)` \n" +
	"[grade]\n5 \n[rarity]\n1 \n[usable job]\n`[all]` \n[/usable job]\n\n" +
	"[attach type]\n`[free]` \n[minimum level]\n1 \n[value]\n40 \n" +
	"[icon]\n`Item/IconMaterial.img` 9 \n[field image]\n`Item/FieldImage.img` 30 \n" +
	"[stackable type]\n`[material]` 4 \n[item group name]\n`cube stuff` \n" +
	"[move wav]\n`CUBE_TOUCH` \n[need material]\n3037 5 \n[/need material]\n"

func p3CubeIDs() [6]int64 {
	return [6]int64{p3CubeBlack, p3CubeWhite, p3CubeRed, p3CubeBlue, p3CubeClear, p3CubeGold}
}

func p3CubePaths() [6]string {
	return [6]string{
		"material/cubepiece_black.stk",
		"material/cubepiece_white.stk",
		"material/cubepiece_red.stk",
		"material/cubepiece_blue.stk",
		"material/cubepiece_clear.stk",
		"material/cubepiece_gold.stk",
	}
}

func p3CubeNames() [6]string {
	// 逐字来自真档六个文档的 `[name]`（pvfdump）：
	// black 黑色小晶块 / white 白色小晶块 / red 红色小晶块 /
	// blue 蓝色小晶块 / clear 无色小晶块 / gold 金色小晶块。
	return [6]string{"黑色小晶块", "白色小晶块", "红色小晶块", "蓝色小晶块", "无色小晶块", "金色小晶块"}
}

// p3CubeSource 是六个晶块文档的测试替身：只保留物品索引与 Extra 真正用到的段
// （`[name]`、`[stackable type]`）；`[stack limit]` 由
// p3CubeSourceWithStackLimit 单独补，用来验证"仅 >0 时写"的分支。
func p3CubeSource() readonlyTestSource {
	source := make(readonlyTestSource, 8)
	paths, names := p3CubePaths(), p3CubeNames()
	for index := range paths {
		document := "[name]\n`" + names[index] + "` \n[stackable type]\n`[material]` 4 \n"
		if index == 0 {
			document = p3CubeBlackDocument
		}
		source["stackable/"+paths[index]] = document
	}
	return source
}

// p3CubeSourceWithStackLimit 给指定格子的文档补一段 `[stack limit] 999`。
func p3CubeSourceWithStackLimit(selection int) readonlyTestSource {
	source := p3CubeSource()
	key := "stackable/" + p3CubePaths()[selection]
	source[key] = source[key] + "[stack limit]\n999 \n"
	return source
}

func p3CubeEntries(source readonlyTestSource) []itemEntry {
	ids, paths := p3CubeIDs(), p3CubePaths()
	entries := make([]itemEntry, 0, len(paths))
	for index, path := range paths {
		entry := itemEntry{ID: uint32(ids[index]), Kind: "stackable", PVFPath: path}
		// 与生产索引同一条解析路径（items_index.parseItemEntry）。
		parseItemEntry(source, &entry)
		entries = append(entries, entry)
	}
	return entries
}

// p3CubeFixture 是 J7 测试台的输入。
type p3CubeFixture struct {
	// pvf 非空时覆盖 source（真档用例）。
	pvf dnfpvf.Source
	// source 是测试替身 PVF（默认 p3CubeSource()）。
	source readonlyTestSource
	// entries 覆盖物品索引（默认由 source 解析六个晶块）。
	entries []itemEntry
	// slots 是账号共享背包的预置内容（key 形如 "0:354"）。
	slots map[string]dnfrepo.ItemStack
	// ids 覆盖商品表；idsFunc 覆盖整个 provider（nil provider = 未注入）。
	ids     [6]int64
	idsFunc func() ([6]int64, error)
	// encoder 覆盖 Deps.EncodeStackRow（nil = 未注入，走 D4 降级分支）。
	encoder func(listType byte, slot int16, stack dnfrepo.ItemStack) []byte
	// account 覆盖账号记录。
	account *dnfrepo.AccountRecord
	// dropAccountItems 为真时把 group.AccountItems 置空（模拟事务能力缺失）。
	dropAccountItems bool
}

func newP3CubeHarness(t *testing.T, fixture p3CubeFixture) *gmtoolWriteHarness {
	t.Helper()
	harness := newGmtoolWriteHarness(t, nil)

	// 注意：不能写 `var source dnfpvf.Source = fixture.source` —— 一个 nil 的
	// readonlyTestSource（map）装进接口后**不是 nil 接口**，会静默变成"空 PVF"。
	var source dnfpvf.Source
	switch {
	case fixture.pvf != nil:
		source = fixture.pvf
	case fixture.source != nil:
		source = fixture.source
	default:
		source = p3CubeSource()
	}
	harness.server.deps.PVF = func() (dnfpvf.Source, error) { return source, nil }
	harness.source = source

	entries := fixture.entries
	if entries == nil {
		if testSource, ok := source.(readonlyTestSource); ok && testSource != nil {
			entries = p3CubeEntries(testSource)
		} else {
			// 真档：显式给六个条目，避免遍历 5.4 万条 stackable.lst。
			ids, paths := p3CubeIDs(), p3CubePaths()
			for index, path := range paths {
				entry := itemEntry{ID: uint32(ids[index]), Kind: "stackable", PVFPath: path}
				parseItemEntry(source, &entry)
				entries = append(entries, entry)
			}
		}
	}
	index := newItemIndex("", harness.server, harness.server.cfg.Logger)
	index.mu.Lock()
	index.setEntriesLocked(entries)
	index.mu.Unlock()
	harness.server.items = index

	switch {
	case fixture.idsFunc != nil:
		harness.server.deps.CrystalCubeItemIDs = fixture.idsFunc
	default:
		ids := fixture.ids
		if ids == ([6]int64{}) {
			ids = p3CubeIDs()
		}
		harness.server.deps.CrystalCubeItemIDs = func() ([6]int64, error) { return ids, nil }
	}
	harness.server.deps.EncodeStackRow = fixture.encoder

	account := dnfrepo.AccountRecord{AccountID: "acc-1", Metadata: map[string]string{}}
	if fixture.account != nil {
		account = *fixture.account
	}
	if err := harness.group.Account.Save(context.Background(), account); err != nil {
		t.Fatalf("save account: %v", err)
	}
	slots := fixture.slots
	if slots == nil {
		slots = map[string]dnfrepo.ItemStack{}
	}
	if err := harness.group.AccountInventory.Save(context.Background(), dnfrepo.AccountInventoryRecord{
		AccountID: "acc-1", Slots: slots,
	}); err != nil {
		t.Fatalf("save account inventory: %v", err)
	}
	if fixture.dropAccountItems {
		group := harness.group
		group.AccountItems = nil
		harness.server.deps.Repos = func() (dnfrepo.Group, bool) { return group, true }
	}
	return harness
}

func (h *gmtoolWriteHarness) setCube(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/1/account-cubes", map[string]string{"id": "1"}, body, h.server.handleSetAccountCube)
}

func (h *gmtoolWriteHarness) readCubes(t *testing.T) (int, map[string]any) {
	t.Helper()
	return h.get(t, "/api/characters/1/account-cubes", map[string]string{"id": "1"}, h.server.handleAccountCubes)
}

func (h *gmtoolWriteHarness) accountInventory(t *testing.T) dnfrepo.AccountInventoryRecord {
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

func (h *gmtoolWriteHarness) requireAccountInventoryUnchanged(t *testing.T, before dnfrepo.AccountInventoryRecord) {
	t.Helper()
	after := h.accountInventory(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("账号共享背包被改动了（本批应当零写入）:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func cubeSlotKey(selection int) string {
	return dnfrepo.AccountSharedInventorySlotKey(dnfrepo.CrystalWarehouseFirstSlot + int16(selection))
}

func cubeRows(t *testing.T, response map[string]any) []map[string]any {
	t.Helper()
	raw := responseList(t, response, "cubes")
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("cube row is not an object: %+v", item)
		}
		rows = append(rows, row)
	}
	return rows
}

// (计划书测试 4) 永远输出 6 行，含空格；"栈内是别的物品"算 0 而不是删掉。
func TestCubesAlwaysSixRows(t *testing.T) {
	ids := p3CubeIDs()
	harness := newP3CubeHarness(t, p3CubeFixture{
		slots: map[string]dnfrepo.ItemStack{
			cubeSlotKey(1): {ItemID: ids[1], Count: 42},
			cubeSlotKey(2): {ItemID: 999999, Count: 7}, // 别的物品占着这一格
		},
	})
	status, response := harness.readCubes(t)
	requireOK(t, status, response)
	rows := cubeRows(t, response)
	if len(rows) != 6 {
		t.Fatalf("cubes 行数 = %d, want 6", len(rows))
	}
	for selection, row := range rows {
		if row["selection"] != float64(selection) {
			t.Fatalf("第 %d 行的 selection = %v", selection, row["selection"])
		}
		wantSlot := float64(dnfrepo.CrystalWarehouseFirstSlot + int16(selection))
		if row["slot"] != wantSlot {
			t.Fatalf("第 %d 行的 slot = %v, want %v", selection, row["slot"], wantSlot)
		}
		if row["itemId"] != float64(ids[selection]) {
			t.Fatalf("第 %d 行的 itemId = %v, want %d", selection, row["itemId"], ids[selection])
		}
		wantCount := float64(0)
		if selection == 1 {
			wantCount = 42
		}
		if row["count"] != wantCount {
			t.Fatalf("第 %d 行的 count = %v, want %v", selection, row["count"], wantCount)
		}
	}
	if rows[1]["name"] != "白色小晶块" {
		t.Fatalf("第 1 行的 name = %v（物品索引必须给出真名）", rows[1]["name"])
	}
	if response["accountId"] != "acc-1" || response["characterId"] != float64(1) {
		t.Fatalf("响应缺少账号/角色标识: %+v", response)
	}
}

// 计划书 §5.3 的断言：商品表缺项 -> 明确失败，**绝不填 0**。
func TestCubesRejectCatalogGapsInsteadOfZero(t *testing.T) {
	harness := newP3CubeHarness(t, p3CubeFixture{
		idsFunc: func() ([6]int64, error) {
			ids := p3CubeIDs()
			ids[4] = 0 // 无色晶块在商品表里缺项
			return ids, nil
		},
	})
	status, response := harness.readCubes(t)
	if message := requireFailure(t, status, response); !strings.Contains(message, "第 4 格") {
		t.Fatalf("读接口的拒绝文案 = %q, want 点名第 4 格", message)
	}
	if _, hasCubes := response["cubes"]; hasCubes {
		t.Fatalf("缺项时不该返回一份填了 0 的快照: %+v", response)
	}

	before := harness.accountInventory(t)
	status, response = harness.setCube(t, map[string]any{"selection": 4, "count": 5})
	if message := requireFailure(t, status, response); !strings.Contains(message, "第 4 格") {
		t.Fatalf("写接口的拒绝文案 = %q", message)
	}
	harness.requireAccountInventoryUnchanged(t, before)

	// provider 未注入 = 能力缺失，同样明确失败。
	missing := newP3CubeHarness(t, p3CubeFixture{})
	missing.server.deps.CrystalCubeItemIDs = nil
	status, response = missing.readCubes(t)
	if message := requireFailure(t, status, response); !strings.Contains(message, "晶块商品表") {
		t.Fatalf("未注入 provider 的拒绝文案 = %q", message)
	}
}

// (计划书测试 5) count=0 -> 该格整栈删除（含 Extra）。
func TestCubeCountZeroDeletesStack(t *testing.T) {
	ids := p3CubeIDs()
	slots := map[string]dnfrepo.ItemStack{
		cubeSlotKey(0): {ItemID: ids[0], Count: 9, Extra: map[string]string{"item_kind": "stackable"}},
		cubeSlotKey(1): {ItemID: ids[1], Count: 4},
	}
	harness := newP3CubeHarness(t, p3CubeFixture{slots: slots})

	status, response := harness.setCube(t, map[string]any{"selection": 0, "count": 0})
	requireOK(t, status, response)
	record := harness.accountInventory(t)
	if _, exists := record.Slots[cubeSlotKey(0)]; exists {
		t.Fatalf("第 0 格应当被整栈删除: %+v", record.Slots)
	}
	if record.Slots[cubeSlotKey(1)].Count != 4 {
		t.Fatalf("没被点到的第 1 格不该被动: %+v", record.Slots)
	}
	rows := cubeRows(t, response)
	if rows[0]["count"] != float64(0) {
		t.Fatalf("删除后第 0 行的 count = %v, want 0", rows[0]["count"])
	}
	fields := harness.eventFields(t, "gm-account-cube-set")
	if fields["selection"] != 0 || fields["count"] != int64(0) || fields["old_count"] != int64(9) ||
		fields["old_item_id"] != int64(ids[0]) || fields["item_id"] != int64(ids[0]) ||
		fields["account_id"] != "acc-1" || fields["slot"] != int(dnfrepo.CrystalWarehouseFirstSlot) {
		t.Fatalf("审计事件字段不完整: %+v", fields)
	}
}

// (计划书测试 6) 栈内是别的物品 -> 整栈替换、旧 Extra 丢弃，写入完整身份证据。
func TestCubeDifferentItemReplacesStack(t *testing.T) {
	ids := p3CubeIDs()
	harness := newP3CubeHarness(t, p3CubeFixture{
		source: p3CubeSourceWithStackLimit(3),
		slots: map[string]dnfrepo.ItemStack{
			cubeSlotKey(3): {
				ItemID: 424242, Count: 3,
				Extra:    map[string]string{"legacy_key": "keep-me-not", "item_kind": "equipment"},
				RawEntry: []byte{0xDE, 0xAD},
			},
		},
	})
	status, response := harness.setCube(t, map[string]any{"selection": 3, "count": 12})
	requireOK(t, status, response)

	stack := harness.accountInventory(t).Slots[cubeSlotKey(3)]
	if stack.ItemID != ids[3] || stack.Count != 12 {
		t.Fatalf("栈 = %+v, want item=%d count=12", stack, ids[3])
	}
	if _, kept := stack.Extra["legacy_key"]; kept {
		t.Fatalf("换物品必须整栈替换、旧 Extra 全丢: %+v", stack.Extra)
	}
	wantExtra := map[string]string{
		"item_kind":      "stackable",
		"pvf_path":       "material/cubepiece_blue.stk",
		"stackable_type": "[material]",
		"stack_limit":    "999",
	}
	for key, value := range wantExtra {
		if stack.Extra[key] != value {
			t.Fatalf("Extra[%s] = %q, want %q（完整 %+v）", key, stack.Extra[key], value, stack.Extra)
		}
	}
	if len(stack.RawEntry) != 0 {
		t.Fatalf("未注入编码器时不该凭空造 RawEntry: %x", stack.RawEntry)
	}
}

// 真档 `material/cubepiece_black.stk` **没有** `[stack limit]` 段：那就不能写这个键。
func TestCubeOmitsStackLimitWhenPVFHasNone(t *testing.T) {
	harness := newP3CubeHarness(t, p3CubeFixture{})
	status, response := harness.setCube(t, map[string]any{"selection": 0, "count": 5})
	requireOK(t, status, response)
	stack := harness.accountInventory(t).Slots[cubeSlotKey(0)]
	if _, present := stack.Extra["stack_limit"]; present {
		t.Fatalf("真档没有 [stack limit]，不该补一个: %+v", stack.Extra)
	}
	if stack.Extra["stackable_type"] != "[material]" || stack.Extra["pvf_path"] != "material/cubepiece_black.stk" {
		t.Fatalf("Extra = %+v", stack.Extra)
	}
	rows := cubeRows(t, response)
	if rows[0]["name"] != "黑色小晶块" {
		t.Fatalf("第 0 行 name = %v, want 黑色小晶块（逐字来自真档文档）", rows[0]["name"])
	}
}

// 同物品续写：保留既有 Extra 元数据，但用本进程的编码器重写 0x77 行（上游 :494-495）。
func TestCubeSameItemKeepsMetadataAndRewritesRawEntry(t *testing.T) {
	ids := p3CubeIDs()
	type encodedCall struct {
		listType byte
		slot     int16
		itemID   int64
		count    int64
	}
	var calls []encodedCall
	fakeRow := make([]byte, p3WireRowSize)
	binary.LittleEndian.PutUint16(fakeRow[0:2], 0x77)

	harness := newP3CubeHarness(t, p3CubeFixture{
		slots: map[string]dnfrepo.ItemStack{
			cubeSlotKey(5): {
				ItemID: ids[5], Count: 3,
				Extra:    map[string]string{"custom_meta": "keep-me", "item_kind": "stackable"},
				RawEntry: []byte{1, 2, 3},
			},
		},
		encoder: func(listType byte, slot int16, stack dnfrepo.ItemStack) []byte {
			calls = append(calls, encodedCall{listType, slot, stack.ItemID, stack.Count})
			return fakeRow
		},
	})
	status, response := harness.setCube(t, map[string]any{"selection": 5, "count": 21})
	requireOK(t, status, response)

	stack := harness.accountInventory(t).Slots[cubeSlotKey(5)]
	if stack.ItemID != ids[5] || stack.Count != 21 {
		t.Fatalf("栈 = %+v", stack)
	}
	if stack.Extra["custom_meta"] != "keep-me" {
		t.Fatalf("同物品续写必须保留既有元数据: %+v", stack.Extra)
	}
	if len(calls) != 1 {
		t.Fatalf("编码器调用次数 = %d, want 1", len(calls))
	}
	if calls[0].listType != dnfrepo.MainInventoryListType || calls[0].slot != dnfrepo.CrystalWarehouseFirstSlot+5 ||
		calls[0].itemID != ids[5] || calls[0].count != 21 {
		t.Fatalf("编码器入参 = %+v（必须是 list0 + 该槽位 + 写入后的栈）", calls[0])
	}
	if !reflect.DeepEqual(stack.RawEntry, fakeRow) {
		t.Fatalf("RawEntry = %x, want 编码器给出的 %x", stack.RawEntry, fakeRow)
	}
	if fields := harness.eventFields(t, "gm-account-cube-set"); fields["raw_entry_encoded"] != true {
		t.Fatalf("事件应当标注已重编码: %+v", fields)
	}
}

// D4：没有编码器时照写 ItemID/Count/Extra，但绝不造 RawEntry，并在事件里如实标注。
func TestCubeWithoutEncoderStillWritesDurableFields(t *testing.T) {
	ids := p3CubeIDs()
	harness := newP3CubeHarness(t, p3CubeFixture{encoder: nil})
	status, response := harness.setCube(t, map[string]any{"selection": 2, "count": 8})
	requireOK(t, status, response)
	stack := harness.accountInventory(t).Slots[cubeSlotKey(2)]
	if stack.ItemID != ids[2] || stack.Count != 8 || stack.Extra["item_kind"] != "stackable" {
		t.Fatalf("栈 = %+v", stack)
	}
	if len(stack.RawEntry) != 0 {
		t.Fatalf("没有编码器时 RawEntry 必须留空: %x", stack.RawEntry)
	}
	if fields := harness.eventFields(t, "gm-account-cube-set"); fields["raw_entry_encoded"] != false {
		t.Fatalf("事件应当标注未重编码: %+v", fields)
	}
}

// (计划书测试 7 + D3) 越界与缺字段一律拒绝且零写入。
func TestCubeRejectsOutOfRangeSelectionAndCount(t *testing.T) {
	ids := p3CubeIDs()
	harness := newP3CubeHarness(t, p3CubeFixture{
		slots: map[string]dnfrepo.ItemStack{cubeSlotKey(0): {ItemID: ids[0], Count: 4}},
	})
	before := harness.accountInventory(t)
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"selection 负", map[string]any{"selection": -1, "count": 1}, "selection 必须在 0..5"},
		{"selection 越界", map[string]any{"selection": 6, "count": 1}, "selection 必须在 0..5"},
		{"count 负", map[string]any{"selection": 0, "count": -1}, "count 必须在 0.."},
		{"count 超 u32", map[string]any{"selection": 0, "count": int64(1) << 33}, "count 必须在 0.."},
		{"缺 selection", map[string]any{"count": 1}, "缺少 selection"},
		{"缺 count", map[string]any{"selection": 0}, "缺少 count"},
	}
	for _, testCase := range cases {
		status, response := harness.setCube(t, testCase.body)
		if message := requireFailure(t, status, response); !strings.Contains(message, testCase.want) {
			t.Fatalf("%s: 拒绝文案 = %q, want 含 %q", testCase.name, message, testCase.want)
		}
	}
	harness.requireAccountInventoryUnchanged(t, before)
	if !harness.hasEvent("gm-account-cube-rejected") {
		t.Fatal("越界拒绝必须记具名事件")
	}
}

// 物品索引里没有这个物品 -> 明确失败（不写一个只有 ItemID 的栈）。
func TestCubeWriteFailsWhenItemDefinitionIsMissing(t *testing.T) {
	harness := newP3CubeHarness(t, p3CubeFixture{
		entries: []itemEntry{{ID: 12345, Kind: "stackable", PVFPath: "material/other.stk", Name: "别的物品"}},
	})
	before := harness.accountInventory(t)
	status, response := harness.setCube(t, map[string]any{"selection": 0, "count": 1})
	if message := requireFailure(t, status, response); !strings.Contains(message, "物品索引") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireAccountInventoryUnchanged(t, before)
	if fields := harness.eventFields(t, "gm-account-cube-rejected"); fields["reason"] != "item_definition_unavailable" {
		t.Fatalf("事件字段 = %+v", fields)
	}
}

// 事务能力缺失 -> 明确失败（上游 :431-433 的 ErrGMUnavailable）。
func TestCubeWriteFailsWithoutAccountItemsTransaction(t *testing.T) {
	harness := newP3CubeHarness(t, p3CubeFixture{dropAccountItems: true})
	before := harness.accountInventory(t)
	status, response := harness.setCube(t, map[string]any{"selection": 0, "count": 1})
	if message := requireFailure(t, status, response); !strings.Contains(message, "事务能力未就绪") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireAccountInventoryUnchanged(t, before)
}

// 默认档：开关未设时，账号里有人在线也照样写，且**一次探针都不打**（今天的字节）。
func TestCubeWriteDefaultProfileDoesNotProbe(t *testing.T) {
	probeCalls := 0
	harness := newP3CubeHarness(t, p3CubeFixture{})
	harness.server.cfg.RequireOffline = false
	harness.server.deps.Online = func(uint16) bool { probeCalls++; return true }

	status, response := harness.setCube(t, map[string]any{"selection": 0, "count": 2})
	requireOK(t, status, response)
	if probeCalls != 0 {
		t.Fatalf("默认档连探针都不该碰，实际 %d 次", probeCalls)
	}
	if len(harness.kindsWithPrefix("gm-online-guard")) != 0 {
		t.Fatalf("默认档不该产生守卫日志: %v", harness.kindsWithPrefix("gm-online-guard"))
	}
	if harness.accountInventory(t).Slots[cubeSlotKey(0)].Count != 2 {
		t.Fatal("默认档必须照常写入")
	}
}

// 开关 on：目标角色在线 -> 拒绝；账号内**别的**角色在线 -> 也拒绝（上游 :427 / :441 双层）。
func TestCubeWriteRequiresOfflineWhenEnabled(t *testing.T) {
	ids := p3CubeIDs()
	// 目标角色 = 1；账号里还有角色 2。
	harness := newP3CubeHarness(t, p3CubeFixture{})
	sibling := dnfrepo.CharacterRecord{
		CharacterID: "2", AccountID: "acc-1", Name: "sibling", Job: "11", Level: 90, Stats: map[string]int64{},
	}
	if err := harness.group.Character.Save(context.Background(), sibling); err != nil {
		t.Fatalf("save sibling: %v", err)
	}
	harness.server.cfg.RequireOffline = true
	before := harness.accountInventory(t)

	// 只有"别的角色"在线：角色级守卫放行，账号级守卫必须拦住。
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 2 }
	status, response := harness.setCube(t, map[string]any{"selection": 0, "count": 3})
	if message := requireFailure(t, status, response); !strings.Contains(message, "仍有角色在线") {
		t.Fatalf("账号级守卫拒绝文案 = %q", message)
	}
	harness.requireAccountInventoryUnchanged(t, before)
	if fields := harness.eventFields(t, "gm-online-guard-rejected"); fields["reason"] != "account_character_online" ||
		fields["action"] != "account-cube-set" {
		t.Fatalf("账号级守卫事件 = %+v", fields)
	}

	// 目标角色自己在线：角色级守卫先拦。
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 1 }
	status, response = harness.setCube(t, map[string]any{"selection": 0, "count": 3})
	if message := requireFailure(t, status, response); !strings.Contains(message, "正在游戏内在线") {
		t.Fatalf("角色级守卫拒绝文案 = %q", message)
	}
	harness.requireAccountInventoryUnchanged(t, before)

	// 全离线：放行。
	harness.server.deps.Online = func(uint16) bool { return false }
	status, response = harness.setCube(t, map[string]any{"selection": 0, "count": 3})
	requireOK(t, status, response)
	if harness.accountInventory(t).Slots[cubeSlotKey(0)].ItemID != ids[0] {
		t.Fatal("全离线时应当写入成功")
	}
}

// 真档复核：六个 cubepiece 的 id 与名字必须与真档 stackable.lst + 文档逐字一致。
// 这条用例同时是上面所有夹具常量的来源证明（需要 DNFBRIDGE_REAL_PVF_SMOKE）。
func TestRealPVFCubeFixtureMatchesTheDump(t *testing.T) {
	source := loadRealPVFSource(t)
	entries := p3CubeEntriesFromRealPVF(t, source)
	ids, names := p3CubeIDs(), p3CubeNames()
	for index, entry := range entries {
		if entry.ID != uint32(ids[index]) {
			t.Fatalf("%s 的 id = %d, want %d", p3CubePaths()[index], entry.ID, ids[index])
		}
		if strings.TrimSpace(entry.Name) != names[index] {
			t.Fatalf("%s 的 name = %q, want %q", p3CubePaths()[index], entry.Name, names[index])
		}
		if entry.Tag != "material" {
			t.Fatalf("%s 的 [stackable type] 首个 tag = %q, want material", p3CubePaths()[index], entry.Tag)
		}
	}
}

// 真档端到端：写一格晶块，Extra 必须逐字来自真档文档（含"真档没有 [stack limit]"）。
func TestRealPVFCubeWriteUsesTheDumpedDocument(t *testing.T) {
	source := loadRealPVFSource(t)
	harness := newP3CubeHarness(t, p3CubeFixture{pvf: source})
	status, response := harness.setCube(t, map[string]any{"selection": 0, "count": 7})
	requireOK(t, status, response)

	stack := harness.accountInventory(t).Slots[cubeSlotKey(0)]
	if stack.ItemID != p3CubeBlack || stack.Count != 7 {
		t.Fatalf("栈 = %+v", stack)
	}
	if stack.Extra["item_kind"] != "stackable" ||
		stack.Extra["pvf_path"] != "material/cubepiece_black.stk" ||
		stack.Extra["stackable_type"] != "[material]" {
		t.Fatalf("真档 Extra = %+v", stack.Extra)
	}
	if _, present := stack.Extra["stack_limit"]; present {
		t.Fatalf("真档 cubepiece_black.stk 没有 [stack limit]，不该写该键: %+v", stack.Extra)
	}
	rows := cubeRows(t, response)
	if rows[0]["name"] != "黑色小晶块" {
		t.Fatalf("name = %v, want 黑色小晶块", rows[0]["name"])
	}
}

// p3CubeEntriesFromRealPVF 按 stackable.lst 的路径取六个 cubepiece 的条目
// （与 dnfbridge/premium_pvf.go:126-153 同一口径：路径 -> id）。
func p3CubeEntriesFromRealPVF(t *testing.T, source dnfpvf.Source) []itemEntry {
	t.Helper()
	text, err := source.ReadText(itemListStackable)
	if err != nil {
		t.Fatalf("read %s: %v", itemListStackable, err)
	}
	document, err := dnfpvf.Parse(itemListStackable, text)
	if err != nil {
		t.Fatalf("parse %s: %v", itemListStackable, err)
	}
	byPath := make(map[string]uint32, 4096)
	for _, entry := range dnfpvf.ParseList(document) {
		path := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(entry.Path, "\\", "/")))
		if path != "" && entry.ID > 0 {
			byPath[path] = uint32(entry.ID)
		}
	}
	entries := make([]itemEntry, 0, 6)
	for _, path := range p3CubePaths() {
		id, ok := byPath[path]
		if !ok {
			t.Fatalf("真档 stackable/stackable.lst 里没有 %s", path)
		}
		entry := itemEntry{ID: id, Kind: "stackable", PVFPath: path}
		parseItemEntry(source, &entry)
		entries = append(entries, entry)
	}
	return entries
}

// P3 前端资产：新 JS 必须随二进制发布，且 bindings.js 引用的每个 id 都要真实存在
// （否则又是一次"悬空绑定杀死其后全部绑定"的事故，见 bindings.js 顶部注释）。
func TestP3FrontendAssetsAreEmbedded(t *testing.T) {
	raw, err := embeddedWWW.ReadFile("wwwroot/js/p3-tools.js")
	if err != nil {
		t.Fatalf("embedded asset wwwroot/js/p3-tools.js: %v", err)
	}
	script := string(raw)
	for _, marker := range []string{"loadAccountCubes", "writeAccountCube", "setSpAbsoluteValues"} {
		if !strings.Contains(script, marker) {
			t.Fatalf("p3-tools.js 缺少 %s", marker)
		}
	}
	indexRaw, err := embeddedWWW.ReadFile("wwwroot/index.html")
	if err != nil {
		t.Fatalf("embedded index.html: %v", err)
	}
	index := string(indexRaw)
	for _, marker := range []string{
		`src="js/p3-tools.js"`,
		`id="account-cube-table"`, `id="btn-load-account-cubes"`, `id="account-cube-state"`,
		`id="sp-absolute-sp"`, `id="sp-absolute-tp"`, `id="btn-sp-absolute"`,
	} {
		if !strings.Contains(index, marker) {
			t.Fatalf("index.html is missing %s", marker)
		}
	}
	bindingsRaw, err := embeddedWWW.ReadFile("wwwroot/js/bindings.js")
	if err != nil {
		t.Fatalf("embedded bindings.js: %v", err)
	}
	bindings := string(bindingsRaw)
	for _, id := range []string{"btn-load-account-cubes", "btn-sp-absolute"} {
		if !strings.Contains(bindings, "'#"+id+"'") {
			t.Fatalf("bindings.js 没有绑定 #%s", id)
		}
		if !strings.Contains(index, `id="`+id+`"`) {
			t.Fatalf("bindings.js references #%s but index.html has no such element", id)
		}
	}
	// 既有绑定（P2 的）必须仍在，且新文件加载顺序在 gmtool-scope.js 之前
	// （否则会被那一批 noop 覆盖）。
	if !strings.Contains(bindings, "'#btn-mailbox-delete'") {
		t.Fatal("P2 的邮箱删除绑定被覆盖了")
	}
	if strings.Index(index, `src="js/p3-tools.js"`) > strings.Index(index, `src="js/gmtool-scope.js"`) {
		t.Fatal("p3-tools.js 必须在 gmtool-scope.js 之前加载")
	}
}
