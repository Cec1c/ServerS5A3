package gmtool

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// B：物品配置两条路由（GET …/items/config-options、POST …/items/configure）
//
// 测试台与 B 之外的用例共用 api_item_detail_test.go 的 readonlyHarness（内存仓储 +
// 假 PVF + 预置物品索引）与 api_mailbox_delete_test.go 的 gmtoolWriteHarness（日志收集 +
// post 辅助）。这里只补三样本批特有的东西：物品索引条目、背包记录、以及一份
// etc/amplifyitem.etc 的测试档。
// ---------------------------------------------------------------------------

// itemcfgTestAmplifyItemText 是一份形状与真档一致（见 cmd/scratch/pvfdump 的
// etc/amplifyitem.etc 实dump）、但数值刻意取成可区分的最小档：
// 四个红字类型的基础值分别是 10/20/30/40，epic（稀有度 4）权重 2.0
// ⇒ 初值必须是 20/40/60/80（公式来自 dnfbridge/amplify_item_pvf.go:240-253）。
const itemcfgTestAmplifyItemText = "[option mapping table]\n" +
	"`[NONE]` 0 `[physical defense]` 1 `[magical defense]` 2 `[physical attack]` 3 `[magical attack]` 4 `[all]` 5\n" +
	"[/option mapping table]\n" +
	"[option data]\n" +
	"`[physical defense]` 2500 10 `[magical defense]` 2500 20 `[physical attack]` 2500 30 `[magical attack]` 2500 40\n" +
	"[/option data]\n" +
	"[rarity weight]\n" +
	"`common` 1.00 `uncommon` 1.00 `rare` 1.00 `unique` 1.50 `epic` 2.00 `chronicle` 1.00 `legendary` 1.00\n" +
	"[/rarity weight]\n" +
	"[equip level const]\n" +
	"55\n"

// itemcfgTestSeparateUpgradeText 是 etc/upgrade_separate.etc 的测试档。
// 形状与真档一致（[table] 4 个数一档、[separate upgrade max] 取第 1 个数、
// [item weights by grade] 3 个数一组），但上限**故意取 7 而不是真档的 8**：
// 任何"写死 8"的实现都会在这里露馅。
const itemcfgTestSeparateUpgradeText = "[table]\n" +
	"2 1 10000 1 3 2 5000 1.11 4 3 5000 1.21 6 4 3000 1.32 8 5 3000 1.42 13 9 1500 1.53 18 12 1500 1.63\n" +
	"[/table]\n" +
	"[separate upgrade max]\n" +
	"7\n" +
	"[level]\n" +
	"7\n" +
	"[item weights by grade]\n" +
	"1 3326 1 2 3326 1 3 3326 2\n" +
	"[/item weights by grade]\n"

const (
	itemcfgTestWeaponID  = 7001
	itemcfgTestPotionID  = 7002
	itemcfgTestAvatarID  = 7003
	itemcfgTestPetEquip  = 7004
	itemcfgTestCoatID    = 7005
	itemcfgTestWeaponKey = "0:9"
	itemcfgTestPotionKey = "0:20"
	itemcfgTestAvatarKey = "0:30"
	itemcfgTestPetKey    = "0:40"
	itemcfgTestCoatKey   = "0:41"
	itemcfgTestWareKey   = "2:5"
)

func itemcfgTestEntries() []itemEntry {
	return []itemEntry{
		{ID: itemcfgTestWeaponID, Name: "测试短剑", Kind: "equipment", Tag: "weapon", Rarity: 4, MinLevel: 90,
			PVFPath: "weapon/test_sword.equ"},
		{ID: itemcfgTestPotionID, Name: "测试药水", Kind: "stackable", Segment: "消耗品",
			UsablePeriodDays: 30, PVFPath: "consumption/test_potion.stk"},
		{ID: itemcfgTestAvatarID, Name: "测试上衣", Kind: "equipment", Tag: "coat avatar", Rarity: 3, MinLevel: 1,
			PVFPath: "avatar/test_coat.equ"},
		{ID: itemcfgTestPetEquip, Name: "测试宠物装备", Kind: "equipment", Tag: "creature", Rarity: 2, MinLevel: 1,
			PVFPath: "creature/test_pet.equ"},
		// 可配置装备但**不是武器**：用来钉死"只有武器能锻造"这条门禁。
		{ID: itemcfgTestCoatID, Name: "测试上衣装备", Kind: "equipment", Tag: "coat", Rarity: 4, MinLevel: 90,
			PVFPath: "coat/test_coat.equ"},
	}
}

type itemcfgHarness struct {
	*gmtoolWriteHarness
}

// newItemcfgHarness 建一个"物品索引就绪 + 背包里放着这批测试物品"的测试台。
func newItemcfgHarness(t *testing.T, slots map[string]dnfrepo.ItemStack) *itemcfgHarness {
	t.Helper()
	character := dnfrepo.CharacterRecord{
		AccountID: "acc-1", Name: "tester", Job: "11", Level: 90, Stats: map[string]int64{},
	}
	base := newReadonlyHarness(t, readonlyFixture{
		source: readonlyTestSource{
			itemcfgAmplifyItemPVFPath:     itemcfgTestAmplifyItemText,
			itemcfgSeparateUpgradePVFPath: itemcfgTestSeparateUpgradeText,
		},
		character: character,
		entries:   itemcfgTestEntries(),
	})
	write := &gmtoolWriteHarness{readonlyHarness: base}
	base.server.cfg.Logger = write.record
	base.server.cfg.RequireOffline = false

	if slots != nil {
		record := dnfrepo.InventoryRecord{CharacterID: "1", Slots: map[string]dnfrepo.ItemStack{}}
		for key, stack := range slots {
			if strings.HasPrefix(key, "2:") {
				if record.Warehouse == nil {
					record.Warehouse = map[string]dnfrepo.ItemStack{}
				}
				record.Warehouse[key] = stack
				continue
			}
			record.Slots[key] = stack
		}
		if err := base.group.Inventory.Save(context.Background(), record); err != nil {
			t.Fatalf("save inventory: %v", err)
		}
	}
	return &itemcfgHarness{gmtoolWriteHarness: write}
}

func itemcfgDefaultSlots(t *testing.T) map[string]dnfrepo.ItemStack {
	t.Helper()
	return map[string]dnfrepo.ItemStack{
		// 0xE0 是高 3 位已占用的打包字节：写强化等级时必须保留它们。
		itemcfgTestWeaponKey: {ItemID: itemcfgTestWeaponID, Count: 1, Extra: map[string]string{"ext_data0": "224"}},
		itemcfgTestPotionKey: {ItemID: itemcfgTestPotionID, Count: 5, ExpireAt: time.Now().UTC().Add(10 * 24 * time.Hour)},
		itemcfgTestAvatarKey: {ItemID: itemcfgTestAvatarID, Count: 1},
		itemcfgTestPetKey:    {ItemID: itemcfgTestPetEquip, Count: 1},
		itemcfgTestCoatKey:   {ItemID: itemcfgTestCoatID, Count: 1},
		itemcfgTestWareKey:   {ItemID: itemcfgTestPotionID, Count: 3, ExpireAt: time.Now().UTC().Add(5 * 24 * time.Hour)},
		"0:5":                {ItemID: 424242, Count: 1},
	}
}

func (h *itemcfgHarness) inventory(t *testing.T) dnfrepo.InventoryRecord {
	t.Helper()
	group, ok := h.server.deps.Repos()
	if !ok {
		t.Fatal("repositories are not ready")
	}
	record, found, err := group.Inventory.Load(context.Background(), "1")
	if err != nil || !found {
		t.Fatalf("load inventory: found=%t err=%v", found, err)
	}
	return record
}

func (h *itemcfgHarness) stackAt(t *testing.T, key string) (dnfrepo.ItemStack, bool) {
	t.Helper()
	record := h.inventory(t)
	if strings.HasPrefix(key, "2:") {
		stack, ok := record.Warehouse[key]
		return stack, ok
	}
	stack, ok := record.Slots[key]
	return stack, ok
}

func (h *itemcfgHarness) configOptions(t *testing.T, listType, slot int) (int, map[string]any) {
	t.Helper()
	target := "/api/characters/1/items/config-options?listType=" + strconv.Itoa(listType) +
		"&slot=" + strconv.Itoa(slot)
	return h.get(t, target, map[string]string{"id": "1"}, h.server.handleItemConfigOptions)
}

func (h *itemcfgHarness) configure(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	return h.post(t, "/api/characters/1/items/configure", map[string]string{"id": "1"},
		body, h.server.handleItemConfigure)
}

func (h *itemcfgHarness) requireInventoryUnchanged(t *testing.T, before dnfrepo.InventoryRecord) {
	t.Helper()
	if after := h.inventory(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("拒绝路径必须零写入:\nbefore=%+v\nafter =%+v", before, after)
	}
}

// ---------------------------------------------------------------------------
// config-options
// ---------------------------------------------------------------------------

// 装备能力块：字段名与前端读的那几个必须逐个对上。
func TestItemcfgConfigOptionsEquipmentCapability(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	status, response := harness.configOptions(t, 0, 9)
	requireOK(t, status, response)

	if response["type"] != "equipment" || response["itemTemplateId"] != float64(itemcfgTestWeaponID) {
		t.Fatalf("响应 = %+v", response)
	}
	equipment := responseMap(t, response, "equipment")
	if equipment["canUpgrade"] != true || equipment["canAmplify"] != true || equipment["canForge"] != true {
		t.Fatalf("装备布尔位 = %+v（武器可强化/增幅/锻造）", equipment)
	}
	if equipment["maxUpgradeLevel"] != float64(itemcfgMaxUpgradeLevel) {
		t.Fatalf("强化上界 = %+v", equipment)
	}
	// 锻造上限必须来自 PVF（测试档故意写 7，真档是 8）——写死 8 就会在这里失败。
	if equipment["maxForgingLevel"] != float64(7) ||
		equipment["forgingPVFPath"] != itemcfgSeparateUpgradePVFPath {
		t.Fatalf("锻造上限/来源 = %+v（必须来自 %s 的 [separate upgrade max]）",
			equipment, itemcfgSeparateUpgradePVFPath)
	}
	// 高 3 位 0xE0 已占用，低 5 位是 0 ⇒ 当前强化等级读出来必须是 0。
	if equipment["currentUpgradeLevel"] != float64(0) || equipment["currentQualityMode"] != float64(itemcfgQualityRandom) {
		t.Fatalf("当前值 = %+v", equipment)
	}
	if equipment["minimumLevel"] != float64(90) || equipment["rarity"] != float64(4) || equipment["type"] != "weapon" {
		t.Fatalf("PVF 证据 = %+v", equipment)
	}
	options, _ := equipment["qualityOptions"].([]any)
	if len(options) != 2 {
		t.Fatalf("qualityOptions = %+v, want 2 项（0/1）", equipment["qualityOptions"])
	}
	amplify, _ := equipment["amplifyTypes"].([]any)
	if len(amplify) != 5 {
		t.Fatalf("amplifyTypes = %+v, want 5 项（0..4）", equipment["amplifyTypes"])
	}
	if response["expiration"] != nil {
		t.Fatalf("没有期限的物品不该给期限能力: %+v", response["expiration"])
	}
}

// 已到最上级 + 已有红字 + 已有强化：读回来必须与写入器/游戏读器同口径。
func TestItemcfgConfigOptionsReadsExistingState(t *testing.T) {
	slots := itemcfgDefaultSlots(t)
	slots[itemcfgTestWeaponKey] = dnfrepo.ItemStack{
		ItemID: itemcfgTestWeaponID, Count: 1,
		Extra: map[string]string{
			"quality_seed": strconv.FormatUint(uint64(itemquality.TopSeed), 10),
			"reinforce":    "13",
			"amplify_type": "3",
		},
	}
	harness := newItemcfgHarness(t, slots)
	_, response := harness.configOptions(t, 0, 9)
	requireOK(t, 200, response)
	equipment := responseMap(t, response, "equipment")
	if equipment["currentQualityMode"] != float64(itemcfgQualityTop) ||
		equipment["currentQualitySeed"] != float64(itemquality.TopSeed) {
		t.Fatalf("品级读取 = %+v", equipment)
	}
	if equipment["currentUpgradeLevel"] != float64(13) || equipment["currentAmplifyType"] != float64(3) {
		t.Fatalf("强化/红字读取 = %+v", equipment)
	}
}

// 有限期的堆叠物：只给期限能力（type=expiration），不给装备字段。
func TestItemcfgConfigOptionsExpirationOnlyForLimitedStackable(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	status, response := harness.configOptions(t, 0, 20)
	requireOK(t, status, response)
	if response["type"] != "expiration" || response["equipment"] != nil {
		t.Fatalf("响应 = %+v", response)
	}
	expiration := responseMap(t, response, "expiration")
	if expiration["canOverride"] != true || expiration["maxDays"] != float64(itemcfgMaxExpireDays) {
		t.Fatalf("期限能力 = %+v", expiration)
	}
	if expiration["durations"] != nil {
		t.Fatalf("本仓没有档位表，durations 必须是 null: %+v", expiration["durations"])
	}
	remaining, _ := expiration["currentRemainingDays"].(float64)
	if remaining < 9 || remaining > 11 {
		t.Fatalf("currentRemainingDays = %v, want ~10", expiration["currentRemainingDays"])
	}
}

// 装扮：不发明选项（D4），没有期限时明确失败。
func TestItemcfgConfigOptionsAvatarIsHonest(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	status, response := harness.configOptions(t, 0, 30)
	if message := requireFailure(t, status, response); !strings.Contains(message, "avatar select ability") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	if fields := harness.eventFields(t, "gm-item-configure-rejected"); fields["reason"] != "avatar_options_unavailable" {
		t.Fatalf("审计字段 = %+v", fields)
	}

	// 同一件装扮加上实例期限后：退化成"只读期限"（vendor :129-147 的同一分支）。
	slots := itemcfgDefaultSlots(t)
	slots[itemcfgTestAvatarKey] = dnfrepo.ItemStack{
		ItemID: itemcfgTestAvatarID, Count: 1,
		ExpireAt: time.Now().UTC().Add(3 * 24 * time.Hour),
	}
	harness = newItemcfgHarness(t, slots)
	status, response = harness.configOptions(t, 0, 30)
	requireOK(t, status, response)
	if response["type"] != "expiration" || response["avatar"] != nil {
		t.Fatalf("响应 = %+v", response)
	}
}

// 非可配置装备 + 无期限 ⇒ 明确失败（不静默给一个"没东西可配"的空壳）。
func TestItemcfgConfigOptionsUnsupportedEquipmentFails(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	status, response := harness.configOptions(t, 0, 40)
	if message := requireFailure(t, status, response); !strings.Contains(message, "没有可配置项") {
		t.Fatalf("拒绝文案 = %q", message)
	}
}

// 空槽位 / 账号共享槽 / 钱包行 / 不支持的容器 都必须具名拒绝。
func TestItemcfgConfigOptionsRejections(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	for _, testCase := range []struct {
		name     string
		listType int
		slot     int
		wantSub  string
	}{
		{name: "空槽位", listType: 0, slot: 77, wantSub: "没有可配置物品"},
		{name: "账号共享槽", listType: 0, slot: 354, wantSub: "账号共享槽"},
		{name: "钱包行", listType: 0, slot: 1, wantSub: "钱包行"},
		{name: "穿戴容器", listType: 3, slot: 12, wantSub: "装备聚合"},
		{name: "账号金库", listType: 12, slot: 0, wantSub: "账号金库"},
		{name: "未知容器", listType: 99, slot: 0, wantSub: "不支持的容器"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			status, response := harness.configOptions(t, testCase.listType, testCase.slot)
			if message := requireFailure(t, status, response); !strings.Contains(message, testCase.wantSub) {
				t.Fatalf("拒绝文案 = %q, want 含 %q", message, testCase.wantSub)
			}
		})
	}
	// 缺查询参数
	status, response := harness.get(t, "/api/characters/1/items/config-options",
		map[string]string{"id": "1"}, harness.server.handleItemConfigOptions)
	if message := requireFailure(t, status, response); !strings.Contains(message, "listType") {
		t.Fatalf("缺参拒绝文案 = %q", message)
	}
}

// ---------------------------------------------------------------------------
// configure：装备字段的落库形态
// ---------------------------------------------------------------------------

func TestItemcfgConfigureEquipmentWritesGameModelFields(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	status, response := harness.configure(t, map[string]any{
		"listType": 0,
		"slot":     9,
		"options": map[string]any{
			"qualityMode":  itemcfgQualityTop,
			"upgradeLevel": 17,
			"amplifyType":  3,
			"forgingLevel": 0,
		},
	})
	requireOK(t, status, response)
	// 前端在装备分支会把四个字段一起发过来（不可锻造时 forgingLevel 恒为 0），
	// 所以这里也顺带钉死"值没变就不记成一次改动"：applied 里不该出现 forgingLevel。
	if response["type"] != "equipment" || response["qualitySeed"] != float64(itemquality.TopSeed) ||
		response["upgradeLevel"] != float64(17) || response["amplifyType"] != float64(3) ||
		response["amplifyValue"] != float64(60) || response["canForge"] != true ||
		response["forgingLevel"] != float64(0) {
		t.Fatalf("响应 = %+v（红字初值必须是 epic 权重 2.0 × 基础值 30 = 60）", response)
	}
	if applied, _ := response["applied"].([]any); len(applied) != 3 {
		t.Fatalf("applied = %v（forgingLevel 与当前值相同，不该被记成改动）", response["applied"])
	}
	stack, found := harness.stackAt(t, itemcfgTestWeaponKey)
	if !found {
		t.Fatal("目标物品消失了")
	}
	if stack.Extra["quality_seed"] != strconv.FormatUint(uint64(itemquality.TopSeed), 10) {
		t.Fatalf("quality_seed = %q", stack.Extra["quality_seed"])
	}
	if stack.Extra["reinforce"] != "17" || stack.Extra["upgrade_level"] != "17" {
		t.Fatalf("强化 Extra = %+v", stack.Extra)
	}
	// 打包字节：高 3 位（0xE0）必须保留，低 5 位是等级。
	if stack.Extra["ext_data0"] != "241" || stack.Extra["packed_flag_byte"] != "241" {
		t.Fatalf("ext_data0 = %q, want 241（0xE0|17）", stack.Extra["ext_data0"])
	}
	if stack.Extra["amplify_type"] != "3" || stack.Extra["byte_13"] != "3" ||
		stack.Extra["amplify_value"] != "60" || stack.Extra["marker_16"] != "60" {
		t.Fatalf("红字 Extra = %+v", stack.Extra)
	}
	if _, ok := stack.Extra["last_gm_configure"]; !ok {
		t.Fatalf("缺少审计键: %+v", stack.Extra)
	}
	if stack.Count != 1 || stack.ItemID != itemcfgTestWeaponID {
		t.Fatalf("物品身份被改了: %+v", stack)
	}
	fields := harness.eventFields(t, "gm-item-configure")
	if fields["result"] != "applied" || fields["type"] != "equipment" {
		t.Fatalf("审计字段 = %+v", fields)
	}
	if applied, _ := fields["applied"].(string); !strings.Contains(applied, "qualityMode") {
		t.Fatalf("applied = %v", fields["applied"])
	}
}

// 0x77 行存在时，行内偏移必须与 Extra 一起改（免得缓存行与权威 Extra 打架）。
func TestItemcfgConfigurePatchesTheCachedWireRow(t *testing.T) {
	slots := itemcfgDefaultSlots(t)
	raw := make([]byte, itemcfgItemListEntrySize)
	slots[itemcfgTestWeaponKey] = dnfrepo.ItemStack{
		ItemID: itemcfgTestWeaponID, Count: 1, RawEntry: raw,
	}
	harness := newItemcfgHarness(t, slots)
	status, response := harness.configure(t, map[string]any{
		"listType": 0, "slot": 9,
		"options": map[string]any{"qualityMode": itemcfgQualityRandom, "upgradeLevel": 7, "amplifyType": 2},
	})
	requireOK(t, status, response)
	stack, _ := harness.stackAt(t, itemcfgTestWeaponKey)
	if len(stack.RawEntry) != itemcfgItemListEntrySize {
		t.Fatalf("行宽 = %d, want %d", len(stack.RawEntry), itemcfgItemListEntrySize)
	}
	if stack.RawEntry[0x0A] != 7 {
		t.Fatalf("行 0x0A = %d, want 7", stack.RawEntry[0x0A])
	}
	if stack.RawEntry[0x13] != 2 {
		t.Fatalf("行 0x13 = %d, want 2", stack.RawEntry[0x13])
	}
	// 品级种子：行 0x06..0x09（小端）必须与 Extra 一致。
	seed, err := strconv.ParseUint(stack.Extra["quality_seed"], 10, 32)
	if err != nil || uint32(seed) != uint32(stack.RawEntry[0x06])|uint32(stack.RawEntry[0x07])<<8|
		uint32(stack.RawEntry[0x08])<<16|uint32(stack.RawEntry[0x09])<<24 {
		t.Fatalf("行内品质种子与 Extra 不一致: extra=%q raw=%x", stack.Extra["quality_seed"], stack.RawEntry[:10])
	}
}

// 只改期限：走既有的 GM 期限覆写实现（expire_time/expire_unix/expiration_source）。
func TestItemcfgConfigureExpirationWritesTheGMOverrideKeys(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	before := time.Now().UTC()
	status, response := harness.configure(t, map[string]any{
		"listType": 0, "slot": 20,
		"options": map[string]any{"expirationDays": 7},
	})
	requireOK(t, status, response)
	if response["type"] != "expiration" {
		t.Fatalf("响应 = %+v", response)
	}
	stack, _ := harness.stackAt(t, itemcfgTestPotionKey)
	if stack.ExpireAt.Before(before.Add(6*24*time.Hour)) || stack.ExpireAt.After(before.Add(8*24*time.Hour)) {
		t.Fatalf("到期时间 = %v, want ≈ now+7d", stack.ExpireAt)
	}
	if stack.Extra["expiration_source"] != gmExpirationSource || stack.Extra["gm_expire_days"] != "7" {
		t.Fatalf("期限 Extra = %+v", stack.Extra)
	}
	if stack.Extra["expire_unix"] != strconv.FormatInt(stack.ExpireAt.Unix(), 10) {
		t.Fatalf("expire_unix = %q, want %d", stack.Extra["expire_unix"], stack.ExpireAt.Unix())
	}
	if stack.Count != 5 {
		t.Fatalf("数量被改了: %d", stack.Count)
	}
}

// 仓库（listType 2）走 Warehouse 字段，不能污染 Slots。
func TestItemcfgConfigureWarehouseSlotUsesWarehouseField(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	status, response := harness.configure(t, map[string]any{
		"listType": 2, "slot": 5,
		"options": map[string]any{"expirationDays": 30},
	})
	requireOK(t, status, response)
	record := harness.inventory(t)
	if _, ok := record.Slots[itemcfgTestWareKey]; ok {
		t.Fatalf("仓库行被写进了 Slots: %+v", record.Slots)
	}
	stack, ok := record.Warehouse[itemcfgTestWareKey]
	if !ok || stack.Extra["gm_expire_days"] != "30" {
		t.Fatalf("仓库行没写对: %+v", record.Warehouse)
	}
	if _, ok := record.Slots[itemcfgTestPotionKey]; !ok {
		t.Fatalf("主背包被动了: %+v", record.Slots)
	}
}

// ---------------------------------------------------------------------------
// configure：拒绝路径必须零写入
// ---------------------------------------------------------------------------

func TestItemcfgConfigureRejectionsAreZeroWrite(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		body    map[string]any
		wantSub string
	}{
		{
			name: "锻造越界（超过 PVF 上限 7）",
			body: map[string]any{"listType": 0, "slot": 9,
				"options": map[string]any{"forgingLevel": 8}},
			wantSub: "forgingLevel=8",
		},
		{
			name: "非武器不能锻造",
			body: map[string]any{"listType": 0, "slot": 40,
				"options": map[string]any{"forgingLevel": 3}},
			wantSub: "不是可配置装备",
		},
		{
			name: "装扮属性",
			body: map[string]any{"listType": 0, "slot": 30,
				"options": map[string]any{"avatarOptionValue": 4}},
			wantSub: "装扮属性配置未移植",
		},
		{
			name: "强化越界",
			body: map[string]any{"listType": 0, "slot": 9,
				"options": map[string]any{"upgradeLevel": 32}},
			wantSub: "upgradeLevel=32",
		},
		{
			name: "品级模式非法",
			body: map[string]any{"listType": 0, "slot": 9,
				"options": map[string]any{"qualityMode": 7}},
			wantSub: "装备品级选项无效",
		},
		{
			name: "红字类型越界",
			body: map[string]any{"listType": 0, "slot": 9,
				"options": map[string]any{"amplifyType": 9}},
			wantSub: "amplifyType=9",
		},
		{
			name: "期限越界",
			body: map[string]any{"listType": 0, "slot": 20,
				"options": map[string]any{"expirationDays": 4000}},
			wantSub: "expirationDays=4000",
		},
		{
			name: "非装备写装备字段",
			body: map[string]any{"listType": 0, "slot": 40,
				"options": map[string]any{"upgradeLevel": 5}},
			wantSub: "不是可配置装备",
		},
		{
			name: "无期限物品写期限",
			body: map[string]any{"listType": 0, "slot": 9,
				"options": map[string]any{"expirationDays": 30}},
			wantSub: "不支持自定义期限",
		},
		{
			name: "空 options",
			body: map[string]any{"listType": 0, "slot": 9,
				"options": map[string]any{}},
			wantSub: "没有任何要改的字段",
		},
		{
			name: "未知 option 字段",
			body: map[string]any{"listType": 0, "slot": 9,
				"options": map[string]any{"enchantCardId": 12}},
			wantSub: "请求格式不正确",
		},
		{
			name:    "缺 options",
			body:    map[string]any{"listType": 0, "slot": 9},
			wantSub: "缺少 options",
		},
		{
			name: "槽位不存在",
			body: map[string]any{"listType": 0, "slot": 77,
				"options": map[string]any{"upgradeLevel": 3}},
			wantSub: "没有可配置物品",
		},
		{
			name: "物品不在 PVF 索引里",
			body: map[string]any{"listType": 0, "slot": 5,
				"options": map[string]any{"upgradeLevel": 3}},
			wantSub: "在 PVF 中不存在",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
			before := harness.inventory(t)
			status, response := harness.configure(t, testCase.body)
			if message := requireFailure(t, status, response); !strings.Contains(message, testCase.wantSub) {
				t.Fatalf("拒绝文案 = %q, want 含 %q", message, testCase.wantSub)
			}
			harness.requireInventoryUnchanged(t, before)
		})
	}
}

// 增幅门槛：等级/稀有度不够时 canAmplify=false，写入被具名拒绝。
func TestItemcfgConfigureRefusesAmplifyBelowTheGate(t *testing.T) {
	slots := itemcfgDefaultSlots(t)
	slots[itemcfgTestWeaponKey] = dnfrepo.ItemStack{ItemID: itemcfgTestWeaponID, Count: 1}
	harness := newItemcfgHarness(t, slots)
	// 把索引条目换成"低等级白装"：直接替换索引内容（与真档无关，只验证门槛判断）。
	lowLevel := itemEntry{ID: itemcfgTestWeaponID, Name: "低级短剑", Kind: "equipment", Tag: "weapon",
		Rarity: 1, MinLevel: 10, PVFPath: "weapon/low.equ"}
	harness.server.items.mu.Lock()
	harness.server.items.setEntriesLocked([]itemEntry{lowLevel})
	harness.server.items.mu.Unlock()

	status, response := harness.configOptions(t, 0, 9)
	requireOK(t, status, response)
	if equipment := responseMap(t, response, "equipment"); equipment["canAmplify"] != false || equipment["canUpgrade"] != true {
		t.Fatalf("能力块 = %+v", equipment)
	}
	before := harness.inventory(t)
	status, response = harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"amplifyType": 2}})
	if message := requireFailure(t, status, response); !strings.Contains(message, "不支持增幅") ||
		!strings.Contains(message, "55") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireInventoryUnchanged(t, before)
}

// 装备数量 != 1：单件实例属性无法表达，必须具名拒绝（ITEMCFG-D6）。
func TestItemcfgConfigureRefusesEquipmentStackOfMany(t *testing.T) {
	slots := itemcfgDefaultSlots(t)
	slots[itemcfgTestWeaponKey] = dnfrepo.ItemStack{ItemID: itemcfgTestWeaponID, Count: 3}
	harness := newItemcfgHarness(t, slots)
	before := harness.inventory(t)
	status, response := harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"upgradeLevel": 5}})
	if message := requireFailure(t, status, response); !strings.Contains(message, "数量不是 1") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireInventoryUnchanged(t, before)
}

// 红字类型没变时沿用已存数值（ITEMCFG-D2），换了类型才写 PVF 初值。
func TestItemcfgConfigureKeepsTheExistingAmplifyValue(t *testing.T) {
	slots := itemcfgDefaultSlots(t)
	slots[itemcfgTestWeaponKey] = dnfrepo.ItemStack{
		ItemID: itemcfgTestWeaponID, Count: 1,
		Extra: map[string]string{"amplify_type": "3", "amplify_value": "999"},
	}
	harness := newItemcfgHarness(t, slots)
	status, response := harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"amplifyType": 3}})
	requireOK(t, status, response)
	if response["amplifyValue"] != float64(999) {
		t.Fatalf("类型没变时不该重算红字数值: %+v", response)
	}
	// 换成类型 4：初值 = epic 2.0 × 40 = 80。
	status, response = harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"amplifyType": 4}})
	requireOK(t, status, response)
	if response["amplifyValue"] != float64(80) {
		t.Fatalf("换类型后应写 PVF 初值 80: %+v", response)
	}
	// 清零红字（0）⇒ 类型与数值都归零。
	status, response = harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"amplifyType": 0}})
	requireOK(t, status, response)
	stack, _ := harness.stackAt(t, itemcfgTestWeaponKey)
	if stack.Extra["amplify_type"] != "0" || stack.Extra["amplify_value"] != "0" {
		t.Fatalf("清零红字失败: %+v", stack.Extra)
	}
}

// ---------------------------------------------------------------------------
// 锻造（separate upgrade / GenuineUpgrade）
// ---------------------------------------------------------------------------

// 锻造落库形态：Extra 两个别名 + 0x77 行的 0x57；0x58/0x59（解封等级/交易限制）
// 与本行其它字节一个都不许动。
func TestItemcfgConfigureWritesForgingToRowByte57(t *testing.T) {
	slots := itemcfgDefaultSlots(t)
	raw := make([]byte, itemcfgItemListEntrySize)
	raw[itemcfgForgingRowOffset+1] = 0x11 // 0x58 = 解封装备等级：不许被碰
	raw[itemcfgForgingRowOffset+2] = 0x22 // 0x59 = 交易限制：不许被碰
	raw[0x47] = 0                         // 索引状态 count
	slots[itemcfgTestWeaponKey] = dnfrepo.ItemStack{
		ItemID: itemcfgTestWeaponID, Count: 1, RawEntry: raw,
	}
	harness := newItemcfgHarness(t, slots)

	status, response := harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"forgingLevel": 5}})
	requireOK(t, status, response)
	if response["forgingLevel"] != float64(5) || response["canForge"] != true ||
		response["forgingRowOffset"] != float64(itemcfgForgingRowOffset) {
		t.Fatalf("响应 = %+v（锻造必须写 0x%02X 并回读）", response, itemcfgForgingRowOffset)
	}
	stack, _ := harness.stackAt(t, itemcfgTestWeaponKey)
	if stack.Extra["byte_57"] != "5" || stack.Extra["value_57"] != "5" {
		t.Fatalf("锻造 Extra = %+v", stack.Extra)
	}
	if stack.RawEntry[itemcfgForgingRowOffset] != 5 {
		t.Fatalf("行 0x%02X = %d, want 5", itemcfgForgingRowOffset, stack.RawEntry[itemcfgForgingRowOffset])
	}
	if stack.RawEntry[itemcfgForgingRowOffset+1] != 0x11 || stack.RawEntry[itemcfgForgingRowOffset+2] != 0x22 {
		t.Fatalf("0x58/0x59 被改动了: %x", stack.RawEntry[itemcfgForgingRowOffset:])
	}
	if stack.RawEntry[0x47] != 0 || stack.RawEntry[0x48] != 0 || stack.RawEntry[0x4A] != 0 {
		t.Fatalf("索引状态区被写脏了（0x4A 是记录数组的第 3 字节，绝不能当锻造用）: %x", stack.RawEntry[0x47:0x57])
	}
	if fields := harness.eventFields(t, "gm-item-configure"); fields["forging_level"] != 5 ||
		fields["forging_row_offset"] != itemcfgForgingRowOffset {
		t.Fatalf("审计字段 = %+v", fields)
	}

	// 写回 0：如实清零（既清 Extra 也清行内字节）。
	status, response = harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"forgingLevel": 0}})
	requireOK(t, status, response)
	stack, _ = harness.stackAt(t, itemcfgTestWeaponKey)
	if stack.Extra["byte_57"] != "0" || stack.RawEntry[itemcfgForgingRowOffset] != 0 {
		t.Fatalf("锻造清零失败: extra=%+v raw=%x", stack.Extra, stack.RawEntry[itemcfgForgingRowOffset:])
	}
}

// 当前锻造等级的读取：Extra 优先；没有 Extra 时读行内 0x57。
func TestItemcfgConfigOptionsReadsCurrentForgingLevel(t *testing.T) {
	slots := itemcfgDefaultSlots(t)
	raw := make([]byte, itemcfgItemListEntrySize)
	raw[itemcfgForgingRowOffset] = 4
	slots[itemcfgTestWeaponKey] = dnfrepo.ItemStack{ItemID: itemcfgTestWeaponID, Count: 1, RawEntry: raw}
	harness := newItemcfgHarness(t, slots)
	_, response := harness.configOptions(t, 0, 9)
	requireOK(t, 200, response)
	if equipment := responseMap(t, response, "equipment"); equipment["currentForgingLevel"] != float64(4) {
		t.Fatalf("从行内读锻造 = %+v", equipment)
	}

	slots[itemcfgTestWeaponKey] = dnfrepo.ItemStack{ItemID: itemcfgTestWeaponID, Count: 1,
		Extra: map[string]string{"byte_57": "6"}}
	harness = newItemcfgHarness(t, slots)
	_, response = harness.configOptions(t, 0, 9)
	requireOK(t, 200, response)
	if equipment := responseMap(t, response, "equipment"); equipment["currentForgingLevel"] != float64(6) {
		t.Fatalf("从 Extra 读锻造 = %+v", equipment)
	}
}

// 不是武器的可配置装备：canForge=false、maxForgingLevel=0，写入被具名拒绝。
func TestItemcfgForgeOnlyForWeapons(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	_, response := harness.configOptions(t, 0, 41) // coat
	requireOK(t, 200, response)
	equipment := responseMap(t, response, "equipment")
	if equipment["canForge"] != false || equipment["maxForgingLevel"] != float64(0) ||
		equipment["canUpgrade"] != true {
		t.Fatalf("上衣的锻造能力 = %+v（只有武器可锻造）", equipment)
	}
	before := harness.inventory(t)
	status, response := harness.configure(t, map[string]any{"listType": 0, "slot": 41,
		"options": map[string]any{"forgingLevel": 3}})
	if message := requireFailure(t, status, response); !strings.Contains(message, "只有武器可以锻造") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireInventoryUnchanged(t, before)
	if fields := harness.eventFields(t, "gm-item-configure-rejected"); fields["reason"] != "forging_not_weapon" {
		t.Fatalf("审计字段 = %+v", fields)
	}
}

// 锻造表读不到：能力块如实报 canForge=false / maxForgingLevel=0（不是写死 8），
// 写入被具名拒绝且零写入。
func TestItemcfgForgingTableMissingIsReportedHonestly(t *testing.T) {
	character := dnfrepo.CharacterRecord{AccountID: "acc-1", Name: "tester", Job: "11", Level: 90, Stats: map[string]int64{}}
	base := newReadonlyHarness(t, readonlyFixture{
		source: readonlyTestSource{
			itemcfgAmplifyItemPVFPath: itemcfgTestAmplifyItemText, // 只有红字表，没有锻造表
		},
		character: character,
		entries:   itemcfgTestEntries(),
	})
	write := &gmtoolWriteHarness{readonlyHarness: base}
	base.server.cfg.Logger = write.record
	harness := &itemcfgHarness{gmtoolWriteHarness: write}
	if err := base.group.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "1",
		Slots:       map[string]dnfrepo.ItemStack{itemcfgTestWeaponKey: {ItemID: itemcfgTestWeaponID, Count: 1}},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	_, response := harness.configOptions(t, 0, 9)
	requireOK(t, 200, response)
	equipment := responseMap(t, response, "equipment")
	if equipment["canForge"] != false || equipment["maxForgingLevel"] != float64(0) ||
		equipment["canAmplify"] != true {
		t.Fatalf("锻造表缺失时的能力块 = %+v", equipment)
	}
	if !harness.hasEvent("gm-item-configure-capability-degraded") {
		t.Fatalf("降级必须留具名日志: %v", harness.eventKinds())
	}
	before := harness.inventory(t)
	status, response := harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"forgingLevel": 3}})
	if message := requireFailure(t, status, response); !strings.Contains(message, "锻造表") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireInventoryUnchanged(t, before)
}

// 红字数值表读不到时必须明确失败（不写一个读不回来的红字）。
func TestItemcfgConfigureRefusesWhenAmplifyTableIsMissing(t *testing.T) {
	character := dnfrepo.CharacterRecord{AccountID: "acc-1", Name: "tester", Job: "11", Level: 90, Stats: map[string]int64{}}
	base := newReadonlyHarness(t, readonlyFixture{
		source:    readonlyTestSource{}, // 没有 etc/amplifyitem.etc
		character: character,
		entries:   itemcfgTestEntries(),
	})
	write := &gmtoolWriteHarness{readonlyHarness: base}
	base.server.cfg.Logger = write.record
	harness := &itemcfgHarness{gmtoolWriteHarness: write}
	if err := base.group.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "1",
		Slots:       map[string]dnfrepo.ItemStack{itemcfgTestWeaponKey: {ItemID: itemcfgTestWeaponID, Count: 1}},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	status, response := harness.configOptions(t, 0, 9)
	requireOK(t, status, response)
	equipment := responseMap(t, response, "equipment")
	if equipment["canUpgrade"] != true || equipment["canAmplify"] != false {
		t.Fatalf("读不到红字表时必须如实报 canAmplify=false: %+v", equipment)
	}
	if !harness.hasEvent("gm-item-configure-capability-degraded") {
		t.Fatalf("降级必须留下具名日志: %v", harness.eventKinds())
	}
	before := harness.inventory(t)
	status, response = harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"amplifyType": 2}})
	if message := requireFailure(t, status, response); !strings.Contains(message, "不支持增幅") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	harness.requireInventoryUnchanged(t, before)
}

// 物品索引还在构建中 ⇒ 明确失败（不是"物品不存在"）。
func TestItemcfgConfigOptionsWaitsForTheItemIndex(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	harness.server.items.mu.Lock()
	harness.server.items.ready = false
	harness.server.items.mu.Unlock()
	status, response := harness.configOptions(t, 0, 9)
	if message := requireFailure(t, status, response); !strings.Contains(message, "索引还在构建中") {
		t.Fatalf("拒绝文案 = %q", message)
	}
}

// 在线守卫：开关打开且角色在线 ⇒ 拒绝且零写入；默认档不探针。
func TestItemcfgConfigureOnlineGuard(t *testing.T) {
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))
	probes := 0
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(uint16) bool { probes++; return true }
	before := harness.inventory(t)
	status, response := harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"upgradeLevel": 3}})
	if message := requireFailure(t, status, response); !strings.Contains(message, "正在游戏内在线") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	if probes == 0 {
		t.Fatal("开关打开时必须真的探针")
	}
	harness.requireInventoryUnchanged(t, before)

	harness.server.cfg.RequireOffline = false
	probes = 0
	status, response = harness.configure(t, map[string]any{"listType": 0, "slot": 9,
		"options": map[string]any{"upgradeLevel": 3}})
	requireOK(t, status, response)
	if probes != 0 {
		t.Fatalf("默认档不该探针，实际 %d 次", probes)
	}
}

// 真档取证（需要 DNFBRIDGE_REAL_PVF_SMOKE 指向 Script.pvf）：
// etc/upgrade_separate.etc 的 [separate upgrade max] 就是锻造上限（真档 8）。
//
// 取证命令：
//
//	go run ./cmd/scratch/pvfdump -pvf <Script.pvf> -path 'etc/upgrade_separate.etc'
//	  → [table] 40 个数（10 档，4 个一组）、[separate upgrade max] 8、[level] 8
func TestItemcfgForgingMaxFromRealPVF(t *testing.T) {
	source := loadRealPVFSource(t)
	server := &Server{deps: Deps{PVF: func() (dnfpvf.Source, error) { return source, nil }}}
	table, err := server.itemcfgSeparateUpgradeTable()
	if err != nil {
		t.Fatalf("读真档 %s: %v", itemcfgSeparateUpgradePVFPath, err)
	}
	if table.maxLevel != 8 {
		t.Fatalf("[separate upgrade max] = %d, want 8", table.maxLevel)
	}
	if table.levelCount != 10 {
		t.Fatalf("[table] 档数 = %d, want 10（4 个数一档）", table.levelCount)
	}
}

// 真档取证：同一份 PVF 里 etc/amplifyitem.etc 的解析必须与游戏自己那份解析一致。
//
// 期望值来自对同一份 PVF 的 dump（cmd/scratch/pvfdump -path etc/amplifyitem.etc）：
//
//	[option mapping table] `[NONE]` 0 `[physical defense]` 1 … `[all]` 5
//	[option data]          四项基础值全是 5
//	[rarity weight]        common 1.00 … unique 1.30 epic 1.50 chronicle 1.10 legendary 1.40
//	[equip level const]    55
//
// ⇒ epic（稀有度 4）的红字初值 = int64(1.50 × 5) = 7（四个类型同值，因为基础值相同）。
func TestItemcfgAmplifyTableFromRealPVF(t *testing.T) {
	source := loadRealPVFSource(t)
	server := &Server{deps: Deps{PVF: func() (dnfpvf.Source, error) { return source, nil }}}
	table, err := server.itemcfgAmplifyTable()
	if err != nil {
		t.Fatalf("读真档 %s: %v", itemcfgAmplifyItemPVFPath, err)
	}
	if table.equipLevelConst != 55 {
		t.Fatalf("[equip level const] = %d, want 55", table.equipLevelConst)
	}
	for amplifyType := int64(1); amplifyType <= 4; amplifyType++ {
		if table.optionBase[amplifyType] != 5 {
			t.Fatalf("optionBase[%d] = %v, want 5", amplifyType, table.optionBase[amplifyType])
		}
	}
	if table.rarityWeight[4] != 1.5 || table.rarityWeight[3] != 1.3 {
		t.Fatalf("rarityWeight = %+v, want epic 1.5 / unique 1.3", table.rarityWeight)
	}
	value, err := table.initialValue(4, 3)
	if err != nil || value != 7 {
		t.Fatalf("epic/类型3 初值 = %d (%v), want 7", value, err)
	}
	if !table.hasInitialValues(4) || table.hasInitialValues(7) {
		t.Fatalf("hasInitialValues 判定不对: epic=%t rarity7=%t",
			table.hasInitialValues(4), table.hasInitialValues(7))
	}
}

// 接线自检：把交付给 gmtool.go 的六条 HandleFunc 注册行放进一个真 ServeMux，
// 验证（1）模式串合法且与既有路由**不冲突**（Go 1.22 的 mux 遇到冲突会在这里 panic），
// （2）经 mux 分发时 r.PathValue 能取到 {id}（直接调 handler 的用例是用 SetPathValue
// 模拟的，这一条才是真分发）。
//
// 账号金库那三条只做注册（它们的账号解析要查启动器账号表 = s.db，单测里没有数据库）；
// 它们的业务分支由 api_account_cargo_test.go 直接打 cargoApplyXxx 覆盖。
// 删角色那一条走**软删除档**（默认的硬删除需要 s.db；硬删除路径由
// api_character_delete_test.go 的假事务用例覆盖），这里只验证模式串能分发到 handler。
func TestItemcfgRoutePatternsWireThroughTheMux(t *testing.T) {
	t.Setenv(chardelModeEnv, "soft")
	harness := newItemcfgHarness(t, itemcfgDefaultSlots(t))

	mux := http.NewServeMux()
	// 与这三条前缀重叠的既有注册行（模式串照抄 gmtool.go，只用于验证不冲突）。
	for _, pattern := range []string{
		"GET /api/accounts/{id}/detail",
		"POST /api/accounts/{id}/currency",
		"GET /api/accounts/{id}/honor-level/max",
		"GET /api/characters",
		"GET /api/characters/{id}",
		"GET /api/characters/{id}/items",
		"POST /api/characters/{id}/items",
		"GET /api/characters/{id}/items/{templateId}/grant-options",
		"POST /api/characters/{id}/items/delete",
		"POST /api/characters/{id}/items/clear",
		"POST /api/characters/{id}/personal-cargo/max",
		"POST /api/characters/{id}/mailbox/delete",
	} {
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	// 本批六条（必须与交付报告里给 gmtool.go 的注册行逐字相同）。
	mux.HandleFunc("POST /api/accounts/{id}/cargo/max", harness.server.handleAccountCargoMax)
	mux.HandleFunc("POST /api/accounts/{id}/cargo/clear", harness.server.handleAccountCargoClear)
	mux.HandleFunc("POST /api/accounts/{id}/cargo/delete", harness.server.handleAccountCargoDelete)
	mux.HandleFunc("GET /api/characters/{id}/items/config-options", harness.server.handleItemConfigOptions)
	mux.HandleFunc("POST /api/characters/{id}/items/configure", harness.server.handleItemConfigure)
	mux.HandleFunc("POST /api/characters/{id}/delete", harness.server.handleCharacterDelete)

	call := func(method, target string, body any) (int, map[string]any) {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		request := httptest.NewRequest(method, target, reader)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s %s -> 404（路由没命中）", method, target)
		}
		if strings.Contains(recorder.Body.String(), "该功能未移植到 90CN 面板") {
			t.Fatalf("%s %s 落到了 /api/ 兜底: %s", method, target, recorder.Body.String())
		}
		response := map[string]any{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %s %s: %v (%s)", method, target, err, recorder.Body.String())
		}
		return recorder.Code, response
	}

	status, response := call(http.MethodGet, "/api/characters/1/items/config-options?listType=0&slot=9", nil)
	requireOK(t, status, response)
	if response["type"] != "equipment" {
		t.Fatalf("经 mux 的 config-options = %+v", response)
	}
	status, response = call(http.MethodPost, "/api/characters/1/items/configure", map[string]any{
		"listType": 0, "slot": 9, "options": map[string]any{"upgradeLevel": 4},
	})
	requireOK(t, status, response)
	if response["upgradeLevel"] != float64(4) {
		t.Fatalf("经 mux 的 configure = %+v", response)
	}
	// 删角色放最后（它会把角色 1 隐藏掉）。
	status, response = call(http.MethodPost, "/api/characters/1/delete", map[string]any{"confirmText": chardelConfirmText})
	requireOK(t, status, response)
	if response["deleteMode"] != "soft" {
		t.Fatalf("经 mux 的 delete = %+v", response)
	}
}
