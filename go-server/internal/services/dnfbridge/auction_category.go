package dnfbridge

import (
	"encoding/binary"
	"sort"
	"strings"
)

// The current client's auction window builds a fixed category tree and sends the
// selected node's code in op187 body[5:7] (u16 LE); it never puts item ids there.
// Until this pass the server dropped that operand and answered every category with
// the same whole-market snapshot, so a bracelet listed under 武器 showed up in
// every tab.
//
// Evidence for the wire layout, the 172 node codes and their labels:
//
//	拍卖行分类搜索_根因与分类码表_20260927.md
//	tools/_auction_codes_full.txt
//
// currentAuctionClientCategoryTree (a generated test fixture) is the client's own
// tree; TestCurrentAuctionCategoryTreeMatchesCurrentClient pins this table to it
// node for node.
//
// What the client does not say is which item belongs to which node, so every node
// carries a rule over the server's PVF item catalog:
//
//   - equipment nodes key off [equipment type] plus the PVF path segments
//     (character/<job>/weapon/<type>/, character/common/<slot>/<material>/),
//   - stackable nodes key off [stackable type] and the item's PVF path,
//   - 全部 (40119) and any code this build has never seen keep the historical
//     "no category filter" behaviour, so an unmapped code cannot empty the market,
//   - the last child of a tab (其他) is "the tab's scope minus its siblings" and
//     the top-level 其他 is "everything eligible minus the other tabs", so an
//     imperfect rule can never make an auctionable item unreachable,
//   - 职业细分类读取[usable job]；设计图读取[output item]；副职业材料读取配方及提取目录。

// currentAuctionCategoryAllCode is the tree root, drawn as 全部.
const currentAuctionCategoryAllCode = uint16(40119)

type currentAuctionCategoryNodeKind uint8

const (
	// currentAuctionCategoryItems matches items by its own rule.
	currentAuctionCategoryItems currentAuctionCategoryNodeKind = iota + 1
	// currentAuctionCategoryChildren is a parent: its own scope rule (optional)
	// plus every child.
	currentAuctionCategoryChildren
	// currentAuctionCategoryRemainder is a tab's 其他: the tab scope minus the
	// other children of that tab.
	currentAuctionCategoryRemainder
	// currentAuctionCategoryGlobal is the top-level 其他: every eligible item no
	// other top-level node claims.
	currentAuctionCategoryGlobal
	// currentAuctionCategoryAll is 全部: no filter at all.
	currentAuctionCategoryAll
	// currentAuctionCategoryUnpinned is a real client node whose membership is not
	// proven yet; it answers nothing.
	currentAuctionCategoryUnpinned
	currentAuctionCategoryMetadata
)

// currentAuctionCategoryRule is one membership test over a PVF item definition.
// Empty fields are ignored; every populated field must hold.
type currentAuctionCategoryRule struct {
	kinds          []dungeonDropItemKind
	equipmentTypes []string
	stackableTypes []string
	// stackableNone excludes stackable types (the 消耗品 tab owns everything that
	// is neither a 设计图 nor a 副职业材料).
	stackableNone []string
	// pathAll entries must all appear in the item's PVF path, pathNone entries must
	// not. Paths are compared lower-cased with forward slashes.
	pathAll  []string
	pathNone []string
}

func (r currentAuctionCategoryRule) matches(definition dungeonDropItemDefinition) bool {
	if len(r.kinds) != 0 {
		found := false
		for _, kind := range r.kinds {
			if definition.Kind == kind {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(r.equipmentTypes) != 0 && !currentAuctionCategoryStringIn(definition.EquipmentType, r.equipmentTypes) {
		return false
	}
	stackableType := normalizeDungeonDropStackableType(definition.StackableType)
	if len(r.stackableTypes) != 0 && !currentAuctionCategoryStringIn(stackableType, r.stackableTypes) {
		return false
	}
	if len(r.stackableNone) != 0 && currentAuctionCategoryStringIn(stackableType, r.stackableNone) {
		return false
	}
	if len(r.pathAll) != 0 || len(r.pathNone) != 0 {
		pvfPath := currentAuctionCategoryPath(definition.PVFPath)
		for _, want := range r.pathAll {
			if !strings.Contains(pvfPath, want) {
				return false
			}
		}
		for _, unwanted := range r.pathNone {
			if strings.Contains(pvfPath, unwanted) {
				return false
			}
		}
	}
	return true
}

func currentAuctionCategoryStringIn(value string, wanted []string) bool {
	if value == "" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range wanted {
		if normalized == candidate {
			return true
		}
	}
	return false
}

func currentAuctionCategoryPath(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
}

// currentAuctionCategoryNode is one node of the client's tree plus the server's
// membership rule for it.
type currentAuctionCategoryNode struct {
	code     uint16
	label    string
	parent   uint16
	children []uint16
	kind     currentAuctionCategoryNodeKind
	rule     currentAuctionCategoryRule
}

type currentAuctionCategoryTree struct {
	nodes    map[uint16]currentAuctionCategoryNode
	topLevel []uint16
	unpinned []uint16
	allCodes []uint16
}

var currentAuctionCategory = buildCurrentAuctionCategoryTree()

// currentAuctionCategoryNodeFor returns the client node for one code.
func currentAuctionCategoryNodeFor(code uint16) (currentAuctionCategoryNode, bool) {
	node, found := currentAuctionCategory.nodes[code]
	return node, found
}

// currentAuctionCategoryIsUnpinned reports whether a code is a known client node
// whose membership rule is still unproven.
func currentAuctionCategoryIsUnpinned(code uint16) bool {
	node, found := currentAuctionCategory.nodes[code]
	return found && node.kind == currentAuctionCategoryUnpinned
}

// currentAuctionCategoryItemMatches evaluates one node against one already
// resolved item definition.
func currentAuctionCategoryItemMatches(code uint16, definition dungeonDropItemDefinition) bool {
	node, found := currentAuctionCategory.nodes[code]
	if !found {
		return false
	}
	switch node.kind {
	case currentAuctionCategoryMetadata:
		return currentAuctionMetadataItemMatches(code, definition)
	case currentAuctionCategoryAll:
		return true
	case currentAuctionCategoryItems:
		return node.rule.matches(definition)
	case currentAuctionCategoryChildren:
		if node.rule.matches(definition) {
			return true
		}
		for _, child := range node.children {
			if currentAuctionCategoryItemMatches(child, definition) {
				return true
			}
		}
		return false
	case currentAuctionCategoryRemainder:
		tab, found := currentAuctionCategory.nodes[node.parent]
		if !found || !tab.rule.matches(definition) {
			return false
		}
		for _, sibling := range tab.children {
			if sibling == code {
				continue
			}
			if currentAuctionCategoryItemMatches(sibling, definition) {
				return false
			}
		}
		return true
	case currentAuctionCategoryGlobal:
		for _, top := range currentAuctionCategory.topLevel {
			if top == code {
				continue
			}
			if currentAuctionCategoryItemMatches(top, definition) {
				return false
			}
		}
		return true
	default:
		// currentAuctionCategoryUnpinned answers nothing.
		return false
	}
}

// currentAuctionCategoryEligible reports whether one listing's item belongs to the
// category the client asked for.
//
// An unknown code and 全部 keep the historical "no filter" behaviour: a code this
// build has never seen must not empty the market. A code that is known but still
// unpinned answers false, which is the honest empty page for it.
func (s *Service) currentAuctionCategoryEligible(code uint16, itemID uint32) bool {
	node, found := currentAuctionCategory.nodes[code]
	if !found || node.kind == currentAuctionCategoryAll {
		return true
	}
	// The node's own kind decides before any catalogue or test seam is consulted:
	// an unpinned node answers nothing whatever the item is.
	if node.kind == currentAuctionCategoryUnpinned {
		return false
	}
	if s != nil && s.auctionCategoryItem != nil {
		return s.auctionCategoryItem(code, itemID)
	}
	if s == nil || itemID == 0 {
		return false
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		return false
	}
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		return false
	}
	definition, err = s.enrichCurrentAuctionCategoryItem(code, catalog, definition)
	if err != nil {
		return false
	}
	return currentAuctionCategoryItemMatches(code, definition)
}

// currentAuctionCategoryRequestCode reads the u16 category the client wrote at
// op187 body[5:7]. A short body answers 0, which no node uses, so it falls back to
// the unfiltered behaviour.
func currentAuctionCategoryRequestCode(body []byte) uint16 {
	if len(body) < 7 {
		return 0
	}
	return binary.LittleEndian.Uint16(body[5:7])
}

type currentAuctionCategorySpec struct {
	code   uint16
	parent uint16
	label  string
	kind   currentAuctionCategoryNodeKind
	rule   currentAuctionCategoryRule
}

// currentAuctionCategoryWeaponJobs is 武器's job level. dir is the PVF directory
// under equipment/character/, types are that job's weapon directories in the
// order the client draws them.
var currentAuctionCategoryWeaponJobs = []struct {
	code  uint16
	label string
	dir   string
	types []struct {
		code  uint16
		label string
		dir   string
	}
}{
	{10101, "鬼剑士/黑暗武士/守护者", "swordman", []struct {
		code  uint16
		label string
		dir   string
	}{{10102, "短剑", "ssword"}, {10103, "太刀", "katana"}, {10104, "钝器", "club"}, {10105, "巨剑", "hsword"}, {10106, "光剑", "beamsword"}}},
	// Two fighter/mage pairs are cross-wired in the PVF itself: the `knuckle`
	// directory holds items named 「手套」 and `boxglove` holds 「拳套」, `pole` holds
	// 「长棍/棍棒」 and `rod` holds 「魔杖」. The tab is what the player reads, so the
	// directory is chosen by the names the items actually carry (see
	// TestCurrentAuctionCategoryRealPVFNamesMatchTheirLabels).
	{10201, "格斗家", "fighter", []struct {
		code  uint16
		label string
		dir   string
	}{{10202, "手套", "knuckle"}, {10203, "臂铠", "gauntlet"}, {10204, "爪", "claw"}, {10205, "拳套", "boxglove"}, {10206, "东方棍", "tonfa"}}},
	{10301, "神枪手", "gunner", []struct {
		code  uint16
		label string
		dir   string
	}{{10302, "左轮枪", "revolver"}, {10303, "自动手枪", "automatic"}, {10304, "步枪", "musket"}, {10305, "手炮", "hcannon"}, {10306, "手弩", "bowgun"}}},
	{10401, "魔法师/缔造者", "mage", []struct {
		code  uint16
		label string
		dir   string
	}{{10402, "矛", "spear"}, {10403, "棍棒", "pole"}, {10404, "魔杖", "rod"}, {10405, "法杖", "staff"}, {10406, "扫把", "broom"}}},
	{10501, "圣职者", "priest", []struct {
		code  uint16
		label string
		dir   string
	}{{10502, "十字架", "cross"}, {10503, "念珠", "rosary"}, {10504, "图腾", "totem"}, {10505, "镰刀", "scythe"}, {10506, "战斧", "axe"}}},
	{10601, "暗夜使者", "thief", []struct {
		code  uint16
		label string
		dir   string
	}{{10602, "匕首", "dagger"}, {10603, "双剑", "twinsword"}, {10604, "手杖", "wand"}, {10605, "苦无", "chakraweapon"}}},
	{10801, "魔枪士", "demoniclancer", []struct {
		code  uint16
		label string
		dir   string
	}{{10802, "长枪", "pike"}, {10803, "战戟", "halberd"}, {10804, "光枪", "beamspear"}, {10805, "暗矛", "javelin"}}},
	{10901, "枪剑士", "gunblader", []struct {
		code  uint16
		label string
		dir   string
	}{{10902, "长刀", "lblade"}, {10903, "小太刀", "sblade"}, {10904, "重剑", "mswd"}, {10905, "源力剑", "coreswd"}}},
}

// currentAuctionCategoryArmorMaterials is 防具's material level; each material's
// children are the same five parts in the same order (base+1 .. base+5).
var currentAuctionCategoryArmorMaterials = []struct {
	code  uint16
	label string
	dir   string
}{
	{11001, "布甲", "cloth"},
	{11100, "皮甲", "leather"},
	{11200, "轻甲", "larmor"},
	{11300, "重甲", "harmor"},
	{11400, "板甲", "plate"},
}

// currentAuctionCategoryArmorParts is the client's fixed part order inside one
// armor material: 上衣 / 头肩 / 下装 / 鞋 / 腰带.
var currentAuctionCategoryArmorParts = []struct {
	label string
	slot  string
	types []string
}{
	{"上衣", "jacket", []string{"[coat]"}},
	{"头肩", "shoulder", []string{"[shoulder]"}},
	{"下装", "pants", []string{"[pants]"}},
	{"鞋", "shoes", []string{"[shoes]"}},
	{"腰带", "belt", []string{"[waist]"}},
}

var currentAuctionCategoryArmorTypes = []string{"[coat]", "[pants]", "[shoulder]", "[shoes]", "[waist]"}

func currentAuctionCategorySpecs() []currentAuctionCategorySpec {
	specs := []currentAuctionCategorySpec{
		{code: currentAuctionCategoryAllCode, parent: 0, label: "全部", kind: currentAuctionCategoryAll},
	}

	// 武器 > <职业> > <武器类型>
	specs = append(specs, currentAuctionCategorySpec{
		code: 10000, parent: currentAuctionCategoryAllCode, label: "武器", kind: currentAuctionCategoryChildren,
		rule: currentAuctionCategoryRule{
			kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
			equipmentTypes: []string{"[weapon]"},
		},
	})
	for _, job := range currentAuctionCategoryWeaponJobs {
		specs = append(specs, currentAuctionCategorySpec{
			code: job.code, parent: 10000, label: job.label, kind: currentAuctionCategoryItems,
			rule: currentAuctionCategoryRule{
				kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
				equipmentTypes: []string{"[weapon]"},
				pathAll:        []string{"/character/" + job.dir + "/weapon/"},
			},
		})
		for _, weaponType := range job.types {
			specs = append(specs, currentAuctionCategorySpec{
				code: weaponType.code, parent: job.code, label: weaponType.label, kind: currentAuctionCategoryItems,
				rule: currentAuctionCategoryRule{
					kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
					equipmentTypes: []string{"[weapon]"},
					pathAll:        []string{"/character/" + job.dir + "/weapon/" + weaponType.dir + "/"},
				},
			})
		}
	}

	// 防具 > <护甲> > <部位>
	specs = append(specs, currentAuctionCategorySpec{
		code: 11000, parent: currentAuctionCategoryAllCode, label: "防具", kind: currentAuctionCategoryChildren,
		rule: currentAuctionCategoryRule{
			kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
			equipmentTypes: currentAuctionCategoryArmorTypes,
		},
	})
	for _, material := range currentAuctionCategoryArmorMaterials {
		specs = append(specs, currentAuctionCategorySpec{
			code: material.code, parent: 11000, label: material.label, kind: currentAuctionCategoryItems,
			rule: currentAuctionCategoryRule{
				kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
				equipmentTypes: currentAuctionCategoryArmorTypes,
				pathAll:        []string{"/" + material.dir + "/"},
			},
		})
		for offset, part := range currentAuctionCategoryArmorParts {
			specs = append(specs, currentAuctionCategorySpec{
				code: material.code + uint16(offset) + 1, parent: material.code, label: part.label,
				kind: currentAuctionCategoryItems,
				rule: currentAuctionCategoryRule{
					kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
					equipmentTypes: part.types,
					pathAll:        []string{"/" + part.slot + "/" + material.dir + "/"},
				},
			})
		}
	}

	// 首饰 > 项链 / 戒指 / 手镯 / 称号
	specs = append(specs, currentAuctionCategorySpec{
		code: 12000, parent: currentAuctionCategoryAllCode, label: "首饰", kind: currentAuctionCategoryChildren,
		rule: currentAuctionCategoryRule{
			kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
			equipmentTypes: []string{"[amulet]", "[ring]", "[wrist]", "[title name]"},
		},
	})
	for _, node := range []struct {
		code  uint16
		label string
		types []string
	}{
		{12001, "项链", []string{"[amulet]"}},
		{12002, "戒指", []string{"[ring]"}},
		{12003, "手镯", []string{"[wrist]"}},
		{12004, "称号", []string{"[title name]"}},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: 12000, label: node.label, kind: currentAuctionCategoryItems,
			rule: currentAuctionCategoryRule{
				kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
				equipmentTypes: node.types,
			},
		})
	}
	// 称号按现用客户端14个职业页及PVF的[usable job]筛选。
	for _, node := range []struct {
		code  uint16
		label string
	}{
		{12005, "鬼剑士/黑暗武士"}, {12006, "鬼剑士(女)"}, {12007, "格斗家(女)"}, {12008, "格斗家(男)"},
		{12009, "神枪手(男)"}, {12010, "神枪手(女)"}, {12011, "魔法师(女)/缔造者"}, {12012, "魔法师(男)"},
		{12013, "圣职者 (男)"}, {12017, "圣职者 (女)"}, {12014, "暗夜使者"}, {12015, "守护者"},
		{12016, "魔枪士"}, {12018, "枪剑士"},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: 12004, label: node.label, kind: currentAuctionCategoryMetadata,
		})
	}

	// 特殊装备 > 辅助装备 / 魔法石 / 耳环
	specs = append(specs, currentAuctionCategorySpec{
		code: 32000, parent: currentAuctionCategoryAllCode, label: "特殊装备", kind: currentAuctionCategoryChildren,
		rule: currentAuctionCategoryRule{
			kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
			equipmentTypes: []string{"[support]", "[magic stone]", "[earring]"},
		},
	})
	for _, node := range []struct {
		code  uint16
		label string
		types []string
	}{
		{32001, "辅助装备", []string{"[support]"}},
		{32100, "魔法石", []string{"[magic stone]"}},
		{32200, "耳环", []string{"[earring]"}},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: 32000, label: node.label, kind: currentAuctionCategoryItems,
			rule: currentAuctionCategoryRule{
				kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
				equipmentTypes: node.types,
			},
		})
	}
	// 辅助装备和魔法石分别按现用客户端16个职业页筛选。
	for _, node := range []struct {
		code  uint16
		label string
	}{
		{32002, "鬼剑士 (男)"}, {32003, "鬼剑士 (女)"}, {32004, "黑暗武士"}, {32013, "格斗家(男)"},
		{32005, "格斗家(女)"}, {32006, "神枪手(男)"}, {32011, "神枪手(女)"}, {32009, "魔法师(男)"},
		{32007, "魔法师(女)"}, {32008, "缔造者"}, {32010, "圣职者 (男)"}, {32016, "圣职者 (女)"},
		{32012, "暗夜使者"}, {32014, "守护者"}, {32015, "魔枪士"}, {32017, "枪剑士"},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: 32001, label: node.label, kind: currentAuctionCategoryMetadata,
		})
	}
	for _, node := range []struct {
		code  uint16
		label string
	}{
		{32101, "鬼剑士 (男)"}, {32102, "鬼剑士 (女)"}, {32103, "黑暗武士"}, {32112, "格斗家(男)"},
		{32104, "格斗家(女)"}, {32105, "神枪手(男)"}, {32110, "神枪手(女)"}, {32108, "魔法师(男)"},
		{32106, "魔法师(女)"}, {32107, "缔造者"}, {32109, "圣职者 (男)"}, {32115, "圣职者 (女)"},
		{32111, "暗夜使者"}, {32113, "守护者"}, {32114, "魔枪士"}, {32116, "枪剑士"},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: 32100, label: node.label, kind: currentAuctionCategoryMetadata,
		})
	}

	// 设计图: [recipe]. Its four children split by the recipe's output item, which
	// would mean parsing every recipe's [output item] inside the auction lock; that
	// is deliberately left out of this pass, so they stay unpinned.
	specs = append(specs, currentAuctionCategorySpec{
		code: 31000, parent: currentAuctionCategoryAllCode, label: "设计图", kind: currentAuctionCategoryChildren,
		rule: currentAuctionCategoryStackableRule("[recipe]"),
	})
	for _, node := range []struct {
		code   uint16
		parent uint16
		label  string
	}{
		{31001, 31000, "武器"},
		{31101, 31000, "防具"},
		{31201, 31000, "首饰"},
		{31301, 31000, "特殊装备"},
		{31302, 31301, "辅助装备"},
		{31303, 31301, "魔法石"},
		// The job level under 设计图>武器/防具/首饰 mirrors the item tree's own job
		// order; it needs the recipe output classification above, so it is unpinned
		// for the same reason. Labels are the client's.
		{31002, 31001, "鬼剑士/黑暗武士/守护者"},
		{31003, 31001, "格斗家"},
		{31004, 31001, "神枪手"},
		{31005, 31001, "魔法师/缔造者"},
		{31006, 31001, "圣职者"},
		{31007, 31001, "暗夜使者"},
		{31010, 31001, "魔枪士"},
		{31011, 31001, "枪剑士"},
		{31102, 31101, "布甲"},
		{31103, 31101, "皮甲"},
		{31104, 31101, "轻甲"},
		{31105, 31101, "重甲"},
		{31106, 31101, "板甲"},
		{31202, 31201, "项链"},
		{31203, 31201, "戒指"},
		{31204, 31201, "手镯"},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: node.parent, label: node.label, kind: currentAuctionCategoryMetadata,
		})
	}

	// 消耗品: every eligible stackable that is not a 设计图 or a 副职业材料.
	specs = append(specs, currentAuctionCategorySpec{
		code: 13000, parent: currentAuctionCategoryAllCode, label: "消耗品", kind: currentAuctionCategoryChildren,
		rule: currentAuctionCategoryRule{
			kinds:         []dungeonDropItemKind{dungeonDropItemStackable},
			stackableNone: []string{"[recipe]", "[material expert job]"},
		},
	})
	for _, node := range []struct {
		code  uint16
		label string
		kind  currentAuctionCategoryNodeKind
		rule  currentAuctionCategoryRule
	}{
		{13001, "消耗品", currentAuctionCategoryItems, currentAuctionCategoryConsumableRule()},
		{13002, "材料", currentAuctionCategoryItems, currentAuctionCategoryStackableRule("[material]")},
		{13003, "投掷/设置", currentAuctionCategoryItems, currentAuctionCategoryStackableRule("[throw]")},
		{13004, "抽取", currentAuctionCategoryMetadata, currentAuctionCategoryRule{}},
		{13005, "任务", currentAuctionCategoryItems, currentAuctionCategoryStackableRule("[quest]", "[quest receive]")},
		{13006, "其他", currentAuctionCategoryRemainder, currentAuctionCategoryRule{}},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: 13000, label: node.label, kind: node.kind, rule: node.rule,
		})
	}

	// 宠物: the pets themselves plus the three pet-equipment families.
	specs = append(specs, currentAuctionCategorySpec{
		code: 14000, parent: currentAuctionCategoryAllCode, label: "宠物", kind: currentAuctionCategoryChildren,
		rule: currentAuctionCategoryRule{
			kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
			equipmentTypes: []string{"[creature]", "[artifact red]", "[artifact blue]", "[artifact green]"},
		},
	})
	for _, node := range []struct {
		code  uint16
		label string
		types []string
	}{
		{14001, "红色宠物装备", []string{"[artifact red]"}},
		{14002, "蓝色宠物装备", []string{"[artifact blue]"}},
		{14003, "绿色宠物装备", []string{"[artifact green]"}},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: 14000, label: node.label, kind: currentAuctionCategoryItems,
			rule: currentAuctionCategoryRule{
				kinds:          []dungeonDropItemKind{dungeonDropItemEquipment},
				equipmentTypes: node.types,
			},
		})
	}

	// 副职业材料按真实配方、提取产物和附魔目录确定用途；通用材料页仍保留。
	specs = append(specs, currentAuctionCategorySpec{
		code: 33000, parent: currentAuctionCategoryAllCode, label: "副职业", kind: currentAuctionCategoryChildren,
		rule: currentAuctionCategoryStackableRule("[material expert job]"),
	})
	for _, node := range []struct {
		code  uint16
		label string
		kind  currentAuctionCategoryNodeKind
		rule  currentAuctionCategoryRule
	}{
		{33001, "炼金术师", currentAuctionCategoryMetadata, currentAuctionCategoryRule{}},
		{33002, "控偶师", currentAuctionCategoryMetadata, currentAuctionCategoryRule{}},
		{33003, "附魔师", currentAuctionCategoryMetadata, currentAuctionCategoryRule{}},
		{33004, "副职材料", currentAuctionCategoryItems, currentAuctionCategoryStackableRule("[material expert job]")},
	} {
		specs = append(specs, currentAuctionCategorySpec{
			code: node.code, parent: 33000, label: node.label, kind: node.kind, rule: node.rule,
		})
	}

	// 其他 is the top-level catch-all.
	specs = append(specs, currentAuctionCategorySpec{
		code: 34000, parent: currentAuctionCategoryAllCode, label: "其他", kind: currentAuctionCategoryGlobal,
	})

	return specs
}

func currentAuctionCategoryStackableRule(types ...string) currentAuctionCategoryRule {
	return currentAuctionCategoryRule{
		kinds:          []dungeonDropItemKind{dungeonDropItemStackable},
		stackableTypes: types,
	}
}

// currentAuctionCategoryConsumableRule is the 消耗品>消耗品 leaf: the usable /
// booster / package [stackable type] families. Everything else inside the tab
// lands in 消耗品>其他, so an incomplete list here cannot hide an item.
func currentAuctionCategoryConsumableRule() currentAuctionCategoryRule {
	return currentAuctionCategoryStackableRule(
		"[town and dungeon]",
		"[unlimited town and dungeon]",
		"[expert town potion]",
		"[teleport potion]",
		"[npc teleport potion]",
		"[dye]",
		"[booster]",
		"[booster selection]",
		"[booster random]",
		"[cera booster]",
		"[usable cera package]",
		"[cera package]",
		"[auto enchant]",
		"[auto use special buff]",
		"[disguise]",
		"[random disguise]",
		"[coupon]",
		"[contract]",
		"[avatar gift]",
		"[feed]",
		"[creature expitem]",
		"[quiz item]",
		"[legacy]",
		"[upgradable legacy]",
		"[random upgradable legacy]",
		"[multi upgradable legacy]",
		"[multi upgradable legacy bonus cera]",
		"[stackable legacy]",
		"[apc legacy]",
		"[upgrade limit cube]",
		"[gold kaleido]",
		"[set]",
		"[item upgrad buff usual upgrade]",
		"[item upgrad buff amplify upgrade]",
		"[scale effect]",
		"[only effect]",
		"[global effect]",
		"[roulette buff]",
		"[equipment effect]",
		"[virtual]",
		"[material instant item]",
		"[waste instant item]",
	)
}

func buildCurrentAuctionCategoryTree() currentAuctionCategoryTree {
	specs := currentAuctionCategorySpecs()
	tree := currentAuctionCategoryTree{nodes: make(map[uint16]currentAuctionCategoryNode, len(specs))}
	for _, spec := range specs {
		if _, exists := tree.nodes[spec.code]; exists {
			panic("dnfbridge: duplicate auction category code")
		}
		tree.nodes[spec.code] = currentAuctionCategoryNode{
			code: spec.code, label: spec.label, parent: spec.parent, kind: spec.kind, rule: spec.rule,
		}
		tree.allCodes = append(tree.allCodes, spec.code)
	}
	// Second pass: children are only known once every node exists, so the spec list
	// can stay parent-before-child free of ordering constraints.
	for _, spec := range specs {
		if spec.parent == 0 {
			continue
		}
		parent, found := tree.nodes[spec.parent]
		if !found {
			panic("dnfbridge: auction category spec references a missing parent")
		}
		parent.children = append(parent.children, spec.code)
		tree.nodes[spec.parent] = parent
	}
	for _, code := range tree.allCodes {
		node := tree.nodes[code]
		if node.parent == currentAuctionCategoryAllCode {
			tree.topLevel = append(tree.topLevel, code)
		}
		if node.kind == currentAuctionCategoryUnpinned {
			tree.unpinned = append(tree.unpinned, code)
		}
	}
	sort.Slice(tree.allCodes, func(i, j int) bool { return tree.allCodes[i] < tree.allCodes[j] })
	sort.Slice(tree.topLevel, func(i, j int) bool { return tree.topLevel[i] < tree.topLevel[j] })
	sort.Slice(tree.unpinned, func(i, j int) bool { return tree.unpinned[i] < tree.unpinned[j] })
	return tree
}
