package dnfbridge

// equipment_recipe_catalog.go 只做一件事：把真实 Script.pvf 里「装备设计图」
// （[stackable type] 归一化为 [recipe] 的 stackable 条目）的**已证段结构**解析成
// 只读、fail-closed 的目录。它不选择奖励、不扣材料、不判断装备是否原地替换——那些是
// 运行时库存事务的职责。
//
// 证据来源（本轮真实档，只读）：
//
//	Q = 续修-D21恢复-20261011/D29-owned-portable-MariaDB-assets-once01/
//	    future-owned-instance/runtime/data/dnf/Script.pvf
//	sha256 = 369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06
//
// 取证结论（全部来自上面这一份真实档，不靠猜段名）：
//
//   - 枚举口径用 stackable/stackable.lst（id → 相对路径），不按文件后缀枚举：
//     设计图既有 .stk，也有 stackable/event/**/chn_recipe/*.equ。
//     实测该 lst 有 57,710 个 id→路径对，互不重复；其中 id<=0 仅 1 条
//     （0 → gold.stk）。loadEquipmentRecipeStackableList 过滤 id<=0，故
//     coverage.StackableEntries = 57,709 = 57,710 - 1（**不是**去重）。
//   - [stackable type] 的段值是 [recipe]，后面可跟一个整型子类选择值（实测 0/1/2/5）；
//     段值不是 [recipe] 的 stackable 直接不是设计图。
//   - [string data] 是制作专业名（实测 craftmanship / weaving / machinary /
//     chemistry / enchant）。
//   - 材料：[input item] 的 (id,count) 对里 **id>0** 才是材料；**id==0 是金币**
//     （与服务端参考实现 A21 InventoryCompoundItemRecipeService 的
//     ParseGoldCostFromInputItem 同义）。[need material] 是额外材料。
//   - 产出：[output item]；失败产出：[fail output]。
//   - 原地升级：[action type] 的 [infinite upgrade recipe] + [need material]，
//     没有 [output item]（实测 10090321..10090324）。
//   - [int data] 是另一种编码，实测至少两种形状：严格的「材料数+对、产出数+对、
//     可选单个 0 结尾」与「拍平转储」。本目录只在**严格形状**成立时解码，
//     其它形状一律 unsupported，绝不臆造材料/产出。
//   - Kind 判定优先级（本目录**有意**比服务端参考实现更保守）：先认显式
//     [input item]+[output item] 的 material-output，再认 [action type] 原地升级，
//     最后才试 [int data]。参考实现 A21 TryParseCompoundRecipe 反过来先试 int data。
//     更严边界：① int data 尾部除空或单个 0 之外还有值即拒绝（参考实现忽略尾部）；
//     ② int data 材料块内出现 id==0 的 (0,amount) 对即拒绝（参考实现按 id<=0 丢弃后接受）。
//     因此真实档里一部分事件设计图在本目录是 unsupported 而参考实现可用；未证即拒绝。
//   - [success rate] / [creation rate] / [broadcast type] / [maintain ability] /
//     [maintain socket stat] / [monster card id] / [bead item] / [item category]
//     原样保留，不解释语义。
//   - [price] / [value] 是普通 PVF 字段，**不是**制作金币费用；金币只认
//     [input item] 里 id==0 的那一对。
//
// 消费约定：只有 Kind 为 material-output / in-place-upgrade / int-data-encoded 的条目
// 才带可用的材料/产出；Kind==unsupported 的条目一律拒绝（fail closed），
// 不得用其中任何字段去扣材料或发产出。
// 为让该契约不可误用，解析时对 Kind==unsupported 的条目**清空全部交易字段**
// （材料/产出 Materials/ExtraMaterials/Outputs/FailOutputs/BeadItems 与金币
// GoldCost/GoldCostFound），只保留原文审计字段
// IntData/IntDataRemainder/ZeroCountPairs 与 UnsupportedReason。
// 契约测试见 equipment_recipe_catalog_test.go 的
// TestEquipmentRecipeCatalogUnsupportedEntriesExposeNoTransactionalPayload。

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const (
	equipmentRecipeStackableListPath = "stackable/stackable.lst"
	equipmentRecipeStackableRoot     = "stackable"
	// equipmentRecipeStackableTagRecipe 是设计图在 [stackable type] 里的段值。
	equipmentRecipeStackableTagRecipe = "[recipe]"
)

var (
	errEquipmentRecipePVFSourceRequired = errors.New("equipment recipe PVF source is required")
	errEquipmentRecipePVFItemUnresolved = errors.New("equipment recipe item is not a stackable list entry")
	errEquipmentRecipePVFDocumentShape  = errors.New("equipment recipe PVF document shape is invalid")
)

// equipmentRecipeKind 是一条设计图**已证**的操作种类。
type equipmentRecipeKind string

const (
	// equipmentRecipeKindMaterialOutput：.stk/.equ 显式给出 [input item] 材料与
	// [output item] 产出（金币可出现在 [input item] 的 id==0 对上）。
	equipmentRecipeKindMaterialOutput equipmentRecipeKind = "material-output"
	// equipmentRecipeKindInPlaceUpgrade：.stk/.equ 给出 [action type] 升级配方，
	// 只列 [need material]，没有独立 [output item]——目标装备原地升级。
	equipmentRecipeKindInPlaceUpgrade equipmentRecipeKind = "in-place-upgrade"
	// equipmentRecipeKindIntDataEncoded：没有显式 [input item]/[output item]，
	// 配方编码在 [int data] 且**严格形状**成立，材料/产出由此解出。
	equipmentRecipeKindIntDataEncoded equipmentRecipeKind = "int-data-encoded"
	// equipmentRecipeKindUnsupported：段结构不构成已证的材料/产出或原地升级形状。
	// 调用方必须拒绝，绝不能用其中任何字段扣材料或发产出。
	equipmentRecipeKindUnsupported equipmentRecipeKind = "unsupported"
)

// equipmentRecipeItemStack 保留一段配方里原样的 (物品 id, 数量) 对。
// 只有 [input item] 允许 ItemID==0（= 金币，见文件头）。
type equipmentRecipeItemStack struct {
	ItemID int64
	Count  int64
}

// equipmentRecipeEntry 是一条设计图的只读投影。除 Kind 之外的字段都是 PVF 原文，
// 不做语义推断；未被证明的段留零值并在 UnsupportedReason 里说明。
type equipmentRecipeEntry struct {
	ItemID  int64
	PVFPath string
	Kind    equipmentRecipeKind

	// Name / Grade / Rarity / AttachType / Price / Value 只作审计用，不参与交易。
	Name       string
	Grade      int64
	Rarity     int64
	AttachType string
	Price      int64
	Value      int64
	ValueFound bool

	// StackableTag 是 [stackable type] 归一化后的段值（"[recipe]"），
	// StackableKind 是段值后面那个子类选择整数。
	StackableTag       string
	StackableKind      int64
	StackableKindFound bool

	// Specialties 是 [string data] 的原文（实测 "[craftmanship]" 等）。
	Specialties []string

	// Materials 是 [input item] 中 id>0 的材料；GoldCost 是同一段里 id==0 的那一对。
	// Kind==unsupported 时 Materials/ExtraMaterials/Outputs/FailOutputs/BeadItems 与
	// 金币 GoldCost/GoldCostFound 一律为零值（见文件头消费约定），
	// 审计原文改看 IntData/IntDataRemainder/ZeroCountPairs。
	Materials     []equipmentRecipeItemStack
	GoldCost      int64
	GoldCostFound bool
	// ZeroCountPairs 保存 (id,0) 这种零数量对。真实档里出现过（nrcp_2654516 的
	// "3174 0"）——服务端参考实现 A21 直接跳过它们，本目录同样不计入材料/产出，
	// 但原样留证，不静默丢弃。
	ZeroCountPairs   []equipmentRecipeItemStack
	ExtraMaterials   []equipmentRecipeItemStack
	Outputs          []equipmentRecipeItemStack
	FailOutputs      []equipmentRecipeItemStack
	BeadItems        []equipmentRecipeItemStack
	NeedSkillInts    []int64
	ItemCategories   []string
	ImpossibleItems  []string
	IntData          []int64
	IntDataRemainder []int64

	MinimumItemmakingField string
	MinimumItemmakingLevel int64
	MinimumItemmakingFound bool

	SuccessRate       int64
	SuccessRateFound  bool
	CreationRate      int64
	CreationRateFound bool
	BroadcastType     int64
	BroadcastFound    bool
	MonsterCardID     int64
	MonsterCardFound  bool
	SubType           int64
	SubTypeFound      bool
	DailyLimit        int64
	DailyLimitFound   bool

	MaintainAbility     string
	MaintainSocketStat  bool
	ActionType          string
	CustomUpgradeType   string
	ExpertJobOnly       bool
	NpcGiftDisallowance bool

	UnsupportedReason string
}

type equipmentRecipeCatalogCoverage struct {
	StackableEntries int
	ReadEntries      int
	RecipeEntries    int
	Failed           int
	Unsupported      int
	ByKind           map[equipmentRecipeKind]int
}

type equipmentRecipeCatalogSnapshot struct {
	StackableEntries int `json:"stackable_entries"`
	RecipeEntries    int `json:"recipe_entries"`
}

// pvfEquipmentRecipeCatalog 只持有栈表与解析缓存。构造很便宜（只读一份
// stackable.lst）；Recipe 按需解析单条；Scan 才做整档遍历。
type pvfEquipmentRecipeCatalog struct {
	mu sync.Mutex

	source   dnfpvf.Source
	listPath string
	paths    map[int64]string
	order    []int64

	entries map[int64]equipmentRecipeEntry
	misses  map[int64]struct{}
}

func newPVFEquipmentRecipeCatalog(source dnfpvf.Source) (*pvfEquipmentRecipeCatalog, error) {
	if source == nil {
		return nil, errEquipmentRecipePVFSourceRequired
	}
	paths, order, err := loadEquipmentRecipeStackableList(source, equipmentRecipeStackableListPath)
	if err != nil {
		return nil, err
	}
	return &pvfEquipmentRecipeCatalog{
		source:   source,
		listPath: equipmentRecipeStackableListPath,
		paths:    paths,
		order:    order,
		entries:  make(map[int64]equipmentRecipeEntry, 64),
		misses:   make(map[int64]struct{}, 64),
	}, nil
}

func (c *pvfEquipmentRecipeCatalog) Snapshot() equipmentRecipeCatalogSnapshot {
	if c == nil {
		return equipmentRecipeCatalogSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return equipmentRecipeCatalogSnapshot{
		StackableEntries: len(c.order),
		RecipeEntries:    len(c.entries),
	}
}

// Recipe 返回一条设计图。found=false 表示该 id 不是 stackable 条目，或它不是
// [recipe] 设计图。段结构损坏时返回 error（fail closed）。
func (c *pvfEquipmentRecipeCatalog) Recipe(itemID int64) (equipmentRecipeEntry, bool, error) {
	if c == nil || c.source == nil {
		return equipmentRecipeEntry{}, false, errEquipmentRecipePVFSourceRequired
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, miss := c.misses[itemID]; miss {
		return equipmentRecipeEntry{}, false, nil
	}
	if entry, ok := c.entries[itemID]; ok {
		return entry, true, nil
	}
	result := c.loadEntry(itemID)
	if result.err != nil {
		return equipmentRecipeEntry{}, false, result.err
	}
	if !result.recipe {
		c.misses[itemID] = struct{}{}
		return equipmentRecipeEntry{}, false, nil
	}
	c.entries[itemID] = result.entry
	return result.entry, true, nil
}

// Scan 遍历整份 stackable.lst，返回全部设计图与覆盖统计。任何一条读/解析失败都会
// 终止并返回该错误（连同已得结果），因此调用方看到的永远是完整或明确失败的集合。
func (c *pvfEquipmentRecipeCatalog) Scan() ([]equipmentRecipeEntry, equipmentRecipeCatalogCoverage, error) {
	if c == nil || c.source == nil {
		return nil, equipmentRecipeCatalogCoverage{}, errEquipmentRecipePVFSourceRequired
	}
	ids := append([]int64(nil), c.order...)
	results := make([]equipmentRecipeScanResult, len(ids))
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	if len(ids) > 0 && workers > len(ids) {
		workers = len(ids)
	}
	next := make(chan int)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range next {
				results[index] = c.loadEntry(ids[index])
			}
		}()
	}
	for index := range ids {
		next <- index
	}
	close(next)
	wait.Wait()

	coverage := equipmentRecipeCatalogCoverage{
		StackableEntries: len(ids),
		ByKind:           make(map[equipmentRecipeKind]int),
	}
	entries := make([]equipmentRecipeEntry, 0, 128)
	var firstErr error
	for _, result := range results {
		switch {
		case result.err != nil:
			coverage.Failed++
			if firstErr == nil {
				firstErr = result.err
			}
		case !result.recipe:
			coverage.ReadEntries++
		default:
			coverage.ReadEntries++
			coverage.RecipeEntries++
			coverage.ByKind[result.entry.Kind]++
			if result.entry.Kind == equipmentRecipeKindUnsupported {
				coverage.Unsupported++
			}
			entries = append(entries, result.entry)
		}
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].ItemID < entries[right].ItemID })
	if firstErr != nil {
		return entries, coverage, firstErr
	}

	c.mu.Lock()
	for _, entry := range entries {
		c.entries[entry.ItemID] = entry
	}
	c.mu.Unlock()
	return entries, coverage, nil
}

type equipmentRecipeScanResult struct {
	entry  equipmentRecipeEntry
	listed bool
	recipe bool
	err    error
}

func (c *pvfEquipmentRecipeCatalog) loadEntry(itemID int64) equipmentRecipeScanResult {
	listed, ok := c.paths[itemID]
	if !ok {
		return equipmentRecipeScanResult{}
	}
	documentPath, text, err := readDungeonDropText(c.source, equipmentRecipeStackableRoot, listed)
	if err != nil {
		return equipmentRecipeScanResult{listed: true, err: fmt.Errorf("%w: item_id=%d listed=%s: %v", errEquipmentRecipePVFItemUnresolved, itemID, listed, err)}
	}
	document, err := dnfpvf.Parse(documentPath, text)
	if err != nil {
		return equipmentRecipeScanResult{listed: true, err: fmt.Errorf("parse equipment recipe item=%d path=%s: %w", itemID, documentPath, err)}
	}
	entry, recipe, err := parseEquipmentRecipeDocument(itemID, documentPath, document)
	if err != nil {
		return equipmentRecipeScanResult{listed: true, err: err}
	}
	return equipmentRecipeScanResult{entry: entry, listed: true, recipe: recipe}
}

func loadEquipmentRecipeStackableList(source dnfpvf.Source, listPath string) (map[int64]string, []int64, error) {
	text, err := source.ReadText(listPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read equipment recipe stackable list %s: %w", listPath, err)
	}
	document, err := dnfpvf.Parse(listPath, text)
	if err != nil {
		return nil, nil, fmt.Errorf("parse equipment recipe stackable list %s: %w", listPath, err)
	}
	entries := dnfpvf.ParseList(document)
	// 过滤 id<=0 与空路径，并对同一 id 只保留首个。当前证据档的 lst 实测 57,710 对、
	// 无重复 id，仅 1 条 id<=0（0 → gold.stk），所以 57,710 - 1 = 57,709；
	// 这是 id<=0 过滤的结果，不是去重。
	paths := make(map[int64]string, len(entries))
	order := make([]int64, 0, len(entries))
	for _, entry := range entries {
		clean := cleanDungeonDropPath(entry.Path)
		if entry.ID <= 0 || clean == "" {
			continue
		}
		if _, exists := paths[entry.ID]; exists {
			continue
		}
		paths[entry.ID] = clean
		order = append(order, entry.ID)
	}
	if len(order) == 0 {
		return nil, nil, fmt.Errorf("%w: %s has no usable entries", errEquipmentRecipePVFDocumentShape, listPath)
	}
	return paths, order, nil
}

// parseEquipmentRecipeDocument 只认 [recipe] 设计图；返回 recipe=false 表示该
// stackable 不是设计图（不是错误）。
func parseEquipmentRecipeDocument(itemID int64, documentPath string, document *dnfpvf.Document) (equipmentRecipeEntry, bool, error) {
	tagKind, tagKindFound, isRecipe, err := equipmentRecipeTag(document)
	if err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if !isRecipe {
		return equipmentRecipeEntry{}, false, nil
	}

	entry := equipmentRecipeEntry{
		ItemID:       itemID,
		PVFPath:      documentPath,
		Kind:         equipmentRecipeKindUnsupported,
		StackableTag: equipmentRecipeStackableTagRecipe,
	}
	if tagKindFound {
		entry.StackableKind = tagKind
		entry.StackableKindFound = true
	}

	if entry.Name, _, err = equipmentRecipeFirstText(document, "name"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.AttachType, _, err = equipmentRecipeFirstText(document, "attach type"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.Grade, _, err = equipmentRecipeIntValue(document, "grade"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.Rarity, _, err = equipmentRecipeIntValue(document, "rarity"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.Price, _, err = equipmentRecipeIntValue(document, "price"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.Value, entry.ValueFound, err = equipmentRecipeIntValue(document, "value"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}

	if entry.Specialties, _, err = equipmentRecipeSectionTexts(document, "string data"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}

	inputPairs, zeroCount, err := equipmentRecipePayloadStacks(document, "input item", true)
	if err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	entry.ZeroCountPairs = append(entry.ZeroCountPairs, zeroCount...)
	for _, pair := range inputPairs {
		if pair.ItemID == 0 {
			if entry.GoldCostFound && entry.GoldCost != pair.Count {
				return equipmentRecipeEntry{}, false, fmt.Errorf("%w: section=input item conflicting gold pairs", errEquipmentRecipePVFDocumentShape)
			}
			entry.GoldCost = pair.Count
			entry.GoldCostFound = true
			continue
		}
		entry.Materials = append(entry.Materials, pair)
	}
	if entry.ExtraMaterials, zeroCount, err = equipmentRecipePayloadStacks(document, "need material", false); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	entry.ZeroCountPairs = append(entry.ZeroCountPairs, zeroCount...)
	if entry.Outputs, zeroCount, err = equipmentRecipePayloadStacks(document, "output item", false); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	entry.ZeroCountPairs = append(entry.ZeroCountPairs, zeroCount...)
	if entry.FailOutputs, zeroCount, err = equipmentRecipePayloadStacks(document, "fail output", false); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	entry.ZeroCountPairs = append(entry.ZeroCountPairs, zeroCount...)
	if entry.BeadItems, zeroCount, err = equipmentRecipePayloadStacks(document, "bead item", false); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	entry.ZeroCountPairs = append(entry.ZeroCountPairs, zeroCount...)
	if entry.NeedSkillInts, _, err = equipmentRecipeSectionInts(document, "need skill"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.ItemCategories, _, err = equipmentRecipeSectionTexts(document, "item category"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.ImpossibleItems, _, err = equipmentRecipeSectionTexts(document, "impossible contents"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.IntData, _, err = equipmentRecipePayloadInts(document, "int data"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.MaintainAbility, _, err = equipmentRecipeFirstText(document, "maintain ability"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.ActionType, _, err = equipmentRecipeFirstText(document, "action type"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.CustomUpgradeType, _, err = equipmentRecipeFirstText(document, "custom upgrade type"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	makingTexts, _, err := equipmentRecipeSectionTexts(document, "minimum itemmaking level")
	if err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if len(makingTexts) > 0 {
		entry.MinimumItemmakingField = makingTexts[0]
	}
	makingLevel, makingFound, err := equipmentRecipeFirstInt(document, "minimum itemmaking level")
	if err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if makingFound {
		entry.MinimumItemmakingLevel = makingLevel
		entry.MinimumItemmakingFound = true
	}
	if entry.SuccessRate, entry.SuccessRateFound, err = equipmentRecipeIntValue(document, "success rate"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.CreationRate, entry.CreationRateFound, err = equipmentRecipeIntValue(document, "creation rate"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.BroadcastType, entry.BroadcastFound, err = equipmentRecipeIntValue(document, "broadcast type"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.MonsterCardID, entry.MonsterCardFound, err = equipmentRecipeIntValue(document, "monster card id"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.SubType, entry.SubTypeFound, err = equipmentRecipeIntValue(document, "sub type"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.DailyLimit, entry.DailyLimitFound, err = equipmentRecipeIntValue(document, "daily purchase limit"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.MaintainSocketStat, err = equipmentRecipeSectionPresent(document, "maintain socket stat"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.ExpertJobOnly, err = equipmentRecipeSectionPresent(document, "expertjob only"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}
	if entry.NpcGiftDisallowance, err = equipmentRecipeSectionPresent(document, "npc gift disallowance"); err != nil {
		return equipmentRecipeEntry{}, false, err
	}

	switch {
	case len(entry.Outputs) > 0 && len(entry.Materials) > 0:
		entry.Kind = equipmentRecipeKindMaterialOutput
	case len(entry.Outputs) == 0 && entry.ActionType != "" && (len(entry.Materials) > 0 || len(entry.ExtraMaterials) > 0):
		entry.Kind = equipmentRecipeKindInPlaceUpgrade
	case len(entry.Outputs) == 0 && len(entry.IntData) > 0:
		materials, outputs, remainder, ok := decodeEquipmentRecipeIntData(entry.IntData)
		if ok {
			entry.Kind = equipmentRecipeKindIntDataEncoded
			entry.Materials = materials
			entry.Outputs = outputs
			break
		}
		entry.UnsupportedReason = "recipe payload is [int data] encoded with an unproven shape"
		if len(remainder) > 0 {
			entry.IntDataRemainder = remainder
		}
	default:
		entry.UnsupportedReason = "recipe sections are neither an explicit material/output pair nor a proven in-place upgrade"
	}
	if entry.Kind == equipmentRecipeKindUnsupported {
		if entry.UnsupportedReason == "" {
			entry.UnsupportedReason = "recipe sections are not proven"
		}
		// F1 契约：unsupported 条目不得携带任何可消费载荷，避免消费者漏判 Kind 时
		// 误扣材料、误扣金币或误发产出。金币 GoldCost/GoldCostFound 同样是交易字段。
		// IntData / IntDataRemainder / ZeroCountPairs / UnsupportedReason 作为原文审计证据保留。
		entry.Materials = nil
		entry.ExtraMaterials = nil
		entry.Outputs = nil
		entry.FailOutputs = nil
		entry.BeadItems = nil
		entry.GoldCost = 0
		entry.GoldCostFound = false
	}
	return entry, true, nil
}

// decodeEquipmentRecipeIntData 只认严格的「材料数 + 材料对 + 产出数 + 产出对（+ 单个 0 结尾）」。
// 任何多余值都原样放进 remainder 并让调用方按 unsupported 处理。
// 边界判断一律用无乘法形式，避免计数为 MaxInt64 一类极端值时 int64 乘法回绕绕过 fail closed。
func decodeEquipmentRecipeIntData(values []int64) ([]equipmentRecipeItemStack, []equipmentRecipeItemStack, []int64, bool) {
	if len(values) == 0 {
		return nil, nil, nil, false
	}
	position := 0
	materialCount := values[position]
	position++
	if materialCount <= 0 || materialCount > int64(len(values)-position)/2 {
		// 无乘法边界：要求剩余值至少能放下 materialCount 对（2*materialCount 个）。
		return nil, nil, values[position:], false
	}
	materials := make([]equipmentRecipeItemStack, 0, materialCount)
	for index := int64(0); index < materialCount; index++ {
		pair, ok := equipmentRecipeIntDataPair(values[position], values[position+1])
		if !ok {
			return nil, nil, values[position:], false
		}
		materials = append(materials, pair)
		position += 2
	}
	if position == len(values) {
		return nil, nil, nil, false
	}
	outputCount := values[position]
	position++
	if outputCount <= 0 || outputCount > int64(len(values)-position)/2 {
		// 同上，无乘法边界。
		return nil, nil, values[position:], false
	}
	outputs := make([]equipmentRecipeItemStack, 0, outputCount)
	for index := int64(0); index < outputCount; index++ {
		pair, ok := equipmentRecipeIntDataPair(values[position], values[position+1])
		if !ok {
			return nil, nil, values[position:], false
		}
		outputs = append(outputs, pair)
		position += 2
	}
	remainder := append([]int64(nil), values[position:]...)
	if len(remainder) > 1 || (len(remainder) == 1 && remainder[0] != 0) {
		return nil, nil, remainder, false
	}
	return materials, outputs, nil, true
}

func equipmentRecipeIntDataPair(itemID, count int64) (equipmentRecipeItemStack, bool) {
	if itemID <= 0 || count <= 0 {
		return equipmentRecipeItemStack{}, false
	}
	return equipmentRecipeItemStack{ItemID: itemID, Count: count}, true
}

func equipmentRecipeSectionTokens(document *dnfpvf.Document, section string) ([]dnfpvf.Token, bool, error) {
	sets, err := equipmentRecipeTokenSets(document, section)
	if err != nil {
		return nil, false, err
	}
	if len(sets) == 0 {
		return nil, false, nil
	}
	return sets[0], true, nil
}

// equipmentRecipePayloadTokens 只给会改变材料/产出的段用：这些段重复出现就是形状不可证，必须 fail closed。
func equipmentRecipePayloadTokens(document *dnfpvf.Document, section string) ([]dnfpvf.Token, bool, error) {
	sets, err := equipmentRecipeTokenSets(document, section)
	if err != nil {
		return nil, false, err
	}
	if len(sets) == 0 {
		return nil, false, nil
	}
	if len(sets) > 1 {
		return nil, false, fmt.Errorf("%w: section=%s appears %d times", errEquipmentRecipePVFDocumentShape, section, len(sets))
	}
	return sets[0], true, nil
}

// equipmentRecipeTokenSets 返回同名段的全部出现。真实档里确有整块重复的文档
// （例如 10099183、2660474 把整份物品块写了两遍），所以不能一律按重复报错。
func equipmentRecipeTokenSets(document *dnfpvf.Document, section string) ([][]dnfpvf.Token, error) {
	if document == nil {
		return nil, fmt.Errorf("%w: section=%s document is nil", errEquipmentRecipePVFDocumentShape, section)
	}
	sets := make([][]dnfpvf.Token, 0, 1)
	for _, candidate := range document.Sections {
		if !strings.EqualFold(strings.TrimSpace(candidate.Name), section) {
			continue
		}
		if candidate.Start < 0 || candidate.End > len(document.Tokens) || candidate.Start > candidate.End {
			return nil, fmt.Errorf("%w: section=%s token range", errEquipmentRecipePVFDocumentShape, section)
		}
		sets = append(sets, document.Tokens[candidate.Start:candidate.End])
	}
	return sets, nil
}

// equipmentRecipeTag 判定一条 stackable 是否为 [recipe] 设计图，并给出段值后的子类选择整数。
// 同一文档里 [recipe] 出现多次且选择值不一致时 fail closed。
func equipmentRecipeTag(document *dnfpvf.Document) (int64, bool, bool, error) {
	sets, err := equipmentRecipeTokenSets(document, "stackable type")
	if err != nil {
		return 0, false, false, err
	}
	var kind int64
	kindFound := false
	seen := false
	for _, tokens := range sets {
		texts, err := equipmentRecipeTextsFromTokens(tokens)
		if err != nil {
			return 0, false, false, err
		}
		if len(texts) == 0 || !strings.EqualFold(texts[0], equipmentRecipeStackableTagRecipe) {
			continue
		}
		current, currentFound, err := equipmentRecipeFirstIntFromTokens(tokens)
		if err != nil {
			return 0, false, false, err
		}
		if seen && kindFound && currentFound && current != kind {
			return 0, false, false, fmt.Errorf("%w: section=stackable type conflicting recipe kinds", errEquipmentRecipePVFDocumentShape)
		}
		if !seen {
			kind, kindFound = current, currentFound
		}
		seen = true
	}
	return kind, kindFound, seen, nil
}

func equipmentRecipeSectionInts(document *dnfpvf.Document, section string) ([]int64, bool, error) {
	tokens, found, err := equipmentRecipeSectionTokens(document, section)
	if err != nil || !found {
		return nil, found, err
	}
	values, err := equipmentRecipeIntsFromTokens(tokens, section)
	if err != nil {
		return nil, true, err
	}
	return values, true, nil
}

// equipmentRecipePayloadInts 用于 [int data] 这类会改变配方内容的段（重复即 fail closed）。
func equipmentRecipePayloadInts(document *dnfpvf.Document, section string) ([]int64, bool, error) {
	tokens, found, err := equipmentRecipePayloadTokens(document, section)
	if err != nil || !found {
		return nil, found, err
	}
	values, err := equipmentRecipeIntsFromTokens(tokens, section)
	if err != nil {
		return nil, true, err
	}
	return values, true, nil
}

func equipmentRecipeIntsFromTokens(tokens []dnfpvf.Token, section string) ([]int64, error) {
	values := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		switch token.Kind {
		case dnfpvf.TokenInt:
			values = append(values, token.Int)
		case dnfpvf.TokenSymbol:
			// 结构符号（大括号/逗号等）不携带数值。
		default:
			return nil, fmt.Errorf("%w: section=%s has non-integer token %q", errEquipmentRecipePVFDocumentShape, section, token.Raw)
		}
	}
	return values, nil
}

// equipmentRecipeFirstInt 返回段里第一个整数。用于同时含文本与数字的段：
// [stackable type] 的 "[recipe] 1"、[minimum itemmaking level] 的 "weapon 8"。
func equipmentRecipeFirstInt(document *dnfpvf.Document, section string) (int64, bool, error) {
	tokens, found, err := equipmentRecipeSectionTokens(document, section)
	if err != nil || !found {
		return 0, false, err
	}
	return equipmentRecipeFirstIntFromTokens(tokens)
}

func equipmentRecipeFirstIntFromTokens(tokens []dnfpvf.Token) (int64, bool, error) {
	for _, token := range tokens {
		if token.Kind == dnfpvf.TokenInt {
			return token.Int, true, nil
		}
	}
	return 0, false, nil
}

func equipmentRecipeSectionTexts(document *dnfpvf.Document, section string) ([]string, bool, error) {
	tokens, found, err := equipmentRecipeSectionTokens(document, section)
	if err != nil || !found {
		return nil, found, err
	}
	values, err := equipmentRecipeTextsFromTokens(tokens)
	if err != nil {
		return nil, true, err
	}
	return values, true, nil
}

func equipmentRecipeTextsFromTokens(tokens []dnfpvf.Token) ([]string, error) {
	values := make([]string, 0, len(tokens))
	for _, token := range tokens {
		switch token.Kind {
		case dnfpvf.TokenString, dnfpvf.TokenIdent:
			if value := strings.TrimSpace(token.Value); value != "" {
				values = append(values, value)
			}
		}
	}
	return values, nil
}

func equipmentRecipeFirstText(document *dnfpvf.Document, section string) (string, bool, error) {
	values, found, err := equipmentRecipeSectionTexts(document, section)
	if err != nil || !found || len(values) == 0 {
		return "", false, err
	}
	return values[0], true, nil
}

func equipmentRecipeIntValue(document *dnfpvf.Document, section string) (int64, bool, error) {
	values, found, err := equipmentRecipeSectionInts(document, section)
	if err != nil || !found || len(values) == 0 {
		return 0, false, err
	}
	if len(values) != 1 {
		return 0, false, fmt.Errorf("%w: section=%s values=%d want=1", errEquipmentRecipePVFDocumentShape, section, len(values))
	}
	return values[0], true, nil
}

func equipmentRecipeSectionPresent(document *dnfpvf.Document, section string) (bool, error) {
	_, found, err := equipmentRecipeSectionTokens(document, section)
	return found, err
}

// equipmentRecipePayloadStacks 解析 (id,count) 对段。真实档出现过数量为 0 的对
// （nrcp_2654516 的 "3174 0"）；服务端参考实现 A21 直接跳过它们，本目录同样跳过，
// 但把跳过的对原样交给调用方留证，不静默丢弃。
func equipmentRecipePayloadStacks(document *dnfpvf.Document, section string, allowZeroItemID bool) ([]equipmentRecipeItemStack, []equipmentRecipeItemStack, error) {
	tokens, found, err := equipmentRecipePayloadTokens(document, section)
	if err != nil || !found {
		return nil, nil, err
	}
	values, err := equipmentRecipeIntsFromTokens(tokens, section)
	if err != nil {
		return nil, nil, err
	}
	if len(values)%2 != 0 {
		return nil, nil, fmt.Errorf("%w: section=%s values=%d row_width=2", errEquipmentRecipePVFDocumentShape, section, len(values))
	}
	pairs := make([]equipmentRecipeItemStack, 0, len(values)/2)
	zeroCount := make([]equipmentRecipeItemStack, 0, 1)
	for offset := 0; offset < len(values); offset += 2 {
		itemID, count := values[offset], values[offset+1]
		if itemID < 0 || count < 0 || (!allowZeroItemID && itemID == 0) {
			return nil, nil, fmt.Errorf("%w: section=%s pair=(%d,%d)", errEquipmentRecipePVFDocumentShape, section, itemID, count)
		}
		if count == 0 {
			zeroCount = append(zeroCount, equipmentRecipeItemStack{ItemID: itemID, Count: count})
			continue
		}
		pairs = append(pairs, equipmentRecipeItemStack{ItemID: itemID, Count: count})
	}
	return pairs, zeroCount, nil
}
