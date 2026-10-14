package dnfbridge

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	// errMagicBoxBoosterInfoShape rejects a [booster info] child tag whose
	// integer vector does not fit the row width of its family. Guessing a
	// stride here is what turned 阐释者礼盒 (10007317) into 复活币 x100000:
	// its single [avatar] row "1 101600024 100000 1 3 0" was read as
	// (item 1, weight 101600024, count 100000) because the avatar row is five
	// integers wide, not three.
	errMagicBoxBoosterInfoShape = errors.New("magic box [booster info] row shape is invalid")
	// errMagicBoxBoosterInfoGated reports a [booster info] family this server
	// cannot project faithfully. [charactor] blocks carry a job label plus a
	// level window (86JP PvfLib keeps both as BoosterRewardEntry
	// CharacterJobLabel and applies them at use time), so granting the whole
	// table would hand out every job's rows. The open fails closed instead.
	errMagicBoxBoosterInfoGated = errors.New("magic box [booster info] family needs a job/level gate the server does not implement")
)

// alignedMagicBoxResolversForCommand keeps PVF loading request-driven. Only
// the single-open magic-box command needs these dependencies; other aligned
// commands do not open or scan the item catalog. The resolvers mirror the
// 86JP StackableItemFile contract: [RANDOMBOX] [int data] groups (skip the
// leading triple, then (item, weight, count, ignored) quads), the
// [booster info] child-tag groups (leading DrawCount, then the row width of
// that tag's family), and the explicit [sealing removal item] opening cost.
func (s *Service) alignedMagicBoxResolversForCommand(opcode dnfenum.CmdPacket) (alignedcmd.MagicBoxResolver, alignedcmd.MagicBoxRewardItemResolver, error) {
	if opcode != dnfenum.CmdPacketUseRandomboxItem && opcode != dnfenum.CmdPacketUseRandomboxItemExpand {
		return nil, nil, nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, nil, err
	}
	if catalog == nil {
		return nil, nil, errDungeonDropSourceRequired
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	// 契约物品（etc/premiumlist_new.etc 的 [type]/[item]/[term]）在**任何槽位规划
	// 之前**就必须被分流成账号契约时长，所以奖励模板解析期就把 (type, 时长) 钉在
	// 模板上，grant 侧不再自己查表（见 alignedcmd.MagicBoxRewardItem）。premiumlist
	// 读不出来时保持零值 = 旧行为（照常入包/走邮件），绝不因此让开箱失败。
	premiumCatalog, premiumErr := s.currentPremiumCatalog()
	if premiumErr != nil {
		premiumCatalog = nil
	}
	return func(boxItemID int64) (alignedcmd.MagicBoxResolution, error) {
			return resolveCurrentMagicBox(catalog, source, boxItemID)
		}, func(itemID int64) (alignedcmd.MagicBoxRewardItem, error) {
			item, err := resolveCurrentMagicBoxRewardItem(catalog, source, itemID)
			if err != nil {
				return item, err
			}
			item.PremiumType, item.PremiumDurationSeconds = currentMagicBoxRewardContract(premiumCatalog, itemID)
			return item, nil
		}, nil
}

// currentMagicBoxRewardContract reports the runtime-PVF contract mapping of one
// reward template: premium type plus duration in seconds, or (0, 0) for an
// ordinary item.
func currentMagicBoxRewardContract(catalog *currentPremiumCatalog, itemID int64) (int64, int64) {
	if catalog == nil || itemID <= 0 || itemID == premium.BlackDiamondThirtyDayItemID {
		return 0, 0
	}
	info, ok := catalog.contractsByItem[itemID]
	if !ok || info.PremiumType <= 0 || info.DurationSeconds <= 0 {
		return 0, 0
	}
	return info.PremiumType, info.DurationSeconds
}

// resolveCurrentMagicBox resolves one box template through the active runtime
// PVF. Unresolvable or non-stackable templates and unsupported box families
// resolve with an empty Kind; catalog/document read failures and [booster info]
// tables whose row shape or family this server cannot project faithfully
// surface as errors so the open fails closed instead of granting a
// plausible-looking item.
func resolveCurrentMagicBox(catalog *pvfDungeonDropCatalog, source dnfpvf.Source, boxItemID int64) (alignedcmd.MagicBoxResolution, error) {
	resolution := alignedcmd.MagicBoxResolution{}
	if catalog == nil || source == nil {
		return resolution, errDungeonDropSourceRequired
	}
	if boxItemID <= 0 || boxItemID > int64(^uint32(0)) {
		return resolution, nil
	}
	definition, err := catalog.ResolveItem(uint32(boxItemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			return resolution, nil
		}
		return alignedcmd.MagicBoxResolution{}, fmt.Errorf("resolve magic box item=%d: %w", boxItemID, err)
	}
	if definition.Kind != dungeonDropItemStackable {
		return resolution, nil
	}
	document, err := parseDungeonCardPVFDocument(source, definition.PVFPath)
	if err != nil {
		return alignedcmd.MagicBoxResolution{}, fmt.Errorf("parse magic box item=%d path=%s: %w", boxItemID, definition.PVFPath, err)
	}
	resolution.BoxPVFPath = definition.PVFPath

	switch normalizeMagicBoxPVFType(magicBoxDocumentText(document, "stackable type")) {
	case "upgradable legacy":
		// Lottery jars use a top-level item/weight/count table, without a
		// RANDOMBOX wrapper or a leading draw count (PvfLib's
		// ParseUpgradableLegacyRewards and the current Anton jar agree).
		values := document.Ints("int data")
		if len(values) == 0 || len(values)%3 != 0 {
			return alignedcmd.MagicBoxResolution{}, fmt.Errorf("legacy jar item=%d invalid reward triples", boxItemID)
		}
		group, err := weightedMagicBoxLotteryGroup(append([]int64{1}, values...), 3)
		if err != nil {
			return alignedcmd.MagicBoxResolution{}, err
		}
		resolution.Groups = []alignedcmd.MagicBoxRewardGroup{group}
	case "random upgradable legacy":
		resolution.Groups = parseMagicBoxRandomGroups(document)
	case "booster", "cera booster", "booster random":
		groups, err := parseMagicBoxBoosterGroups(document)
		if err != nil {
			return alignedcmd.MagicBoxResolution{}, fmt.Errorf(
				"magic box item=%d path=%s: %w",
				boxItemID,
				definition.PVFPath,
				err,
			)
		}
		resolution.Groups = groups
	default:
		return resolution, nil
	}
	if len(resolution.Groups) == 0 {
		return alignedcmd.MagicBoxResolution{}, nil
	}
	resolution.Kind = "random"
	resolution.MaterialItemID, resolution.MaterialCountPerUse = resolveMagicBoxNeedMaterial(document, boxItemID)
	return resolution, nil
}

// parseMagicBoxRandomGroups walks the [RANDOMBOX] child [int data] sections.
// Each section is one independent group with DrawCount 1; per the 86JP
// ParseRandomBoxRewards rule the leading triple is skipped and every later
// four ints form one (item, weight, count, ignored) entry. A section with no
// quad entries and at least two ints degrades to one default-weight entry.
func parseMagicBoxRandomGroups(document *dnfpvf.Document) []alignedcmd.MagicBoxRewardGroup {
	groups := make([]alignedcmd.MagicBoxRewardGroup, 0, 4)
	inRandombox := false
	for _, section := range document.Sections {
		name := normalizeMagicBoxSectionName(section.Name)
		switch name {
		case "randombox":
			inRandombox = true
			continue
		case "/randombox":
			inRandombox = false
		}
		if !inRandombox || name != "int data" {
			continue
		}
		ints := magicBoxSectionInts(document, section)
		group := alignedcmd.MagicBoxRewardGroup{DrawCount: 1}
		if len(ints) >= 7 {
			for index := 3; index+3 < len(ints); index += 4 {
				if ints[index] <= 0 {
					continue
				}
				group.Entries = append(group.Entries, alignedcmd.MagicBoxRewardEntry{
					ItemID: ints[index],
					Weight: maxMagicBoxInt(ints[index+1], 0),
					Count:  maxMagicBoxInt(ints[index+2], 1),
				})
			}
		}
		if len(group.Entries) == 0 && len(ints) >= 2 && ints[0] > 0 {
			group.Entries = append(group.Entries, alignedcmd.MagicBoxRewardEntry{
				ItemID: ints[0],
				Weight: 10000,
				Count:  maxMagicBoxInt(ints[1], 1),
			})
		}
		if len(group.Entries) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

// parseMagicBoxBoosterGroups walks the [booster info] child tags. Each direct
// child tag is one independent reward group; the leading integer is the group's
// DrawCount and the remaining integers are fixed-width reward rows
// (86JP PvfLib StackableItemFile.ParseBoosterInfo -> AddWeightedRewards):
//
//	[avatar] / [special avatar]  DrawCount then (item, weight, count, avatar
//	                             attribute category, avatar attribute option)
//	[broadcast]                  DrawCount then (item, weight, count, ignored)
//	[charactor]                  job label + level window, then DrawCount and
//	                             (item, weight, count) rows (job/level gated)
//	everything else              DrawCount then (item, weight, count)
//
// Every section must fit its family's row width. A section that does not is an
// error rather than a best-effort guess, so an unknown table can never be
// misread into a plausible-looking reward.
func parseMagicBoxBoosterGroups(document *dnfpvf.Document) ([]alignedcmd.MagicBoxRewardGroup, error) {
	groups := make([]alignedcmd.MagicBoxRewardGroup, 0, 4)
	inInfo := false
	for _, section := range document.Sections {
		name := normalizeMagicBoxSectionName(section.Name)
		switch name {
		case "booster info":
			inInfo = true
			continue
		case "/booster info":
			inInfo = false
		}
		if !inInfo || strings.HasPrefix(name, "/") {
			continue
		}
		values := magicBoxSectionValues(document, section)
		if len(values) == 0 {
			continue
		}
		group, err := parseMagicBoxBoosterGroup(name, values)
		if err != nil {
			if errors.Is(err, errMagicBoxBoosterInfoGated) {
				return nil, fmt.Errorf("section=%q values=%v: %w", section.Name, values, err)
			}
			return nil, fmt.Errorf("%w: section=%q values=%v", errMagicBoxBoosterInfoShape, section.Name, values)
		}
		if len(group.Entries) > 0 {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

// parseMagicBoxBoosterGroup decodes one direct [booster info] child tag.
func parseMagicBoxBoosterGroup(name string, values []int64) (alignedcmd.MagicBoxRewardGroup, error) {
	if len(values) == 1 {
		// A lone leading integer is an empty group placeholder, as the 90 PVF
		// writes for reward tables that exist but grant nothing ([equipment] 1,
		// [etc] 1, the empty [charactor] placeholders).
		return alignedcmd.MagicBoxRewardGroup{DrawCount: maxMagicBoxInt(values[0], 1)}, nil
	}
	switch name {
	case "charactor":
		return alignedcmd.MagicBoxRewardGroup{}, errMagicBoxBoosterInfoGated
	case "avatar", "special avatar":
		if len(values) < 6 || (len(values)-1)%5 != 0 {
			return alignedcmd.MagicBoxRewardGroup{}, fmt.Errorf("avatar row width=%d", len(values)-1)
		}
		return weightedMagicBoxBoosterGroup(values, 5)
	case "broadcast":
		if len(values) < 5 || (len(values)-1)%4 != 0 {
			return alignedcmd.MagicBoxRewardGroup{}, fmt.Errorf("broadcast row width=%d", len(values)-1)
		}
		return weightedMagicBoxBoosterGroup(values, 4)
	default:
		if (len(values)-1)%3 != 0 {
			return alignedcmd.MagicBoxRewardGroup{}, fmt.Errorf("reward row width=%d", len(values)-1)
		}
		return weightedMagicBoxBoosterGroup(values, 3)
	}
}

// weightedMagicBoxBoosterGroup reads the leading DrawCount and the
// (item, weight, count) head of every fixed-width row. A zero item id is the
// PVF's explicit "no item" placeholder in ordinary boosters. Legacy lottery
// jars instead use item zero for a weighted gold reward. A negative
// item id, a negative weight or a non-positive count is a malformed table and
// rejects the whole box instead of dropping value silently.
func weightedMagicBoxBoosterGroup(values []int64, stride int) (alignedcmd.MagicBoxRewardGroup, error) {
	return weightedMagicBoxGroup(values, stride, false)
}

func weightedMagicBoxLotteryGroup(values []int64, stride int) (alignedcmd.MagicBoxRewardGroup, error) {
	return weightedMagicBoxGroup(values, stride, true)
}

func weightedMagicBoxGroup(values []int64, stride int, allowGold bool) (alignedcmd.MagicBoxRewardGroup, error) {
	group := alignedcmd.MagicBoxRewardGroup{DrawCount: maxMagicBoxInt(values[0], 1)}
	for index := 1; index+2 < len(values); index += stride {
		itemID, weight, count := values[index], values[index+1], values[index+2]
		if itemID == 0 && !allowGold {
			continue
		}
		if itemID < 0 || weight < 0 || count < 1 {
			return alignedcmd.MagicBoxRewardGroup{}, fmt.Errorf(
				"row item=%d weight=%d count=%d",
				itemID,
				weight,
				count,
			)
		}
		group.Entries = append(group.Entries, alignedcmd.MagicBoxRewardEntry{
			ItemID: itemID,
			Weight: weight,
			Count:  count,
		})
	}
	return group, nil
}

// magicBoxSectionValues returns one section's numeric vector in token order.
// The 90 PVF writes a few booster weights as decimals (for example
// "108040274 8.20 1" inside 10162015); 86JP PvfLib scales decimal weights by
// the widest fraction in the block, so 8.20 becomes 820 and stays comparable
// with the integer weights of the same table (10000 = 100%).
func magicBoxSectionValues(document *dnfpvf.Document, section dnfpvf.Section) []int64 {
	if section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
		return nil
	}
	values := make([]int64, 0, section.End-section.Start)
	floats := make([]float64, 0, 4)
	isFloat := make([]bool, 0, section.End-section.Start)
	maxFraction := 0
	for _, token := range document.Tokens[section.Start:section.End] {
		switch token.Kind {
		case dnfpvf.TokenInt:
			values = append(values, token.Int)
			floats = append(floats, 0)
			isFloat = append(isFloat, false)
		case dnfpvf.TokenFloat:
			fraction := magicBoxFractionDigits(token.Raw)
			if fraction > maxFraction {
				maxFraction = fraction
			}
			values = append(values, 0)
			floats = append(floats, token.Float)
			isFloat = append(isFloat, true)
		}
	}
	if maxFraction == 0 {
		return values
	}
	scale := math.Pow10(maxFraction)
	for index := range values {
		if isFloat[index] {
			values[index] = int64(math.Round(floats[index] * scale))
		}
	}
	return values
}

// magicBoxFractionDigits counts the fractional digits of one PVF number token.
func magicBoxFractionDigits(raw string) int {
	dot := strings.IndexByte(raw, '.')
	if dot < 0 {
		return 0
	}
	digits := 0
	for _, char := range raw[dot+1:] {
		if char < '0' || char > '9' {
			break
		}
		digits++
	}
	return digits
}

// resolveMagicBoxNeedMaterial reads the per-open material requirement. The
// [sealing removal item] section is preferred: when its ints are
// 1+header*2 shaped the leading int is an entry count and skipped, the rest
// form (item, count) pairs; the first pair with a positive count that is not
// the box itself wins. For booster boxes top-level [need material] is the
// NPC exchange price, not a second opening cost. PvfLib parses it independently of
// RandomBoxRemovalItems; InventoryPackageRewardResolver.ResolveNeedMaterial
// uses only the latter. Charging it again blocked the real 490005029 revive
// coin box after purchase and incorrectly reported inventory-full.
func resolveMagicBoxNeedMaterial(document *dnfpvf.Document, boxItemID int64) (int64, int64) {
	ints := document.Ints("sealing removal item")
	if len(ints) > 0 && int64(len(ints)) == 1+ints[0]*2 {
		ints = ints[1:]
	}
	for index := 0; index+1 < len(ints); index += 2 {
		itemID, count := ints[index], ints[index+1]
		if itemID <= 0 || count <= 0 || itemID == boxItemID {
			continue
		}
		return itemID, count
	}
	// Keep the already validated lottery-jar policy separate from booster
	// acquisition prices. The runtime Anton upgradable-legacy definition and
	// its original transaction regressions use this top-level material vector;
	// changing a revival booster must not silently waive a working jar cost.
	if normalizeMagicBoxPVFType(magicBoxDocumentText(document, "stackable type")) == "upgradable legacy" {
		ints = document.Ints("lottery use need item")
		if len(ints) == 0 {
			ints = document.Ints("need material")
		}
		if len(ints) >= 2 && ints[0] > 0 && ints[1] > 0 && ints[0] != boxItemID {
			return ints[0], ints[1]
		}
	}
	return 0, 0
}

// resolveCurrentMagicBoxRewardItem resolves one reward template for the
// durable grant step: inventory kind, stack limit (0 = unlimited), PVF slot
// range, and the seal flag from its [attach type].
func resolveCurrentMagicBoxRewardItem(catalog *pvfDungeonDropCatalog, source dnfpvf.Source, itemID int64) (alignedcmd.MagicBoxRewardItem, error) {
	if catalog == nil || source == nil {
		return alignedcmd.MagicBoxRewardItem{}, errDungeonDropSourceRequired
	}
	if itemID <= 0 || itemID > int64(^uint32(0)) {
		return alignedcmd.MagicBoxRewardItem{}, nil
	}
	definition, err := catalog.ResolveItem(uint32(itemID))
	if err != nil {
		if errors.Is(err, errDungeonDropItemUnresolved) {
			return alignedcmd.MagicBoxRewardItem{}, nil
		}
		return alignedcmd.MagicBoxRewardItem{}, fmt.Errorf("resolve magic box reward item=%d: %w", itemID, err)
	}
	definition, err = currentPVFItemDefinitionForGrantAt(definition, time.Now().UTC())
	if err != nil {
		return alignedcmd.MagicBoxRewardItem{}, fmt.Errorf("resolve magic box reward item=%d expiration: %w", itemID, err)
	}
	item := alignedcmd.MagicBoxRewardItem{
		ItemID:           itemID,
		Kind:             string(definition.Kind),
		EquipmentType:    definition.EquipmentType,
		StackLimit:       definition.StackLimit,
		SlotStart:        definition.SlotStart,
		SlotEnd:          definition.SlotEnd,
		PVFPath:          definition.PVFPath,
		ExpireAt:         definition.ExpirationDate,
		UsablePeriodDays: definition.UsablePeriodDays,
	}
	// 容器/分段判定一律问 currentPVFItemTargetContainer（唯一真源）：
	// 宠物本体 → list 7 0..139、宠物消耗品（[feed]/[creature]，含更名卡 25）→
	// list 7 189..238、其余按家族落主背包（材料 121..176 等）。
	container := currentPVFItemTargetContainer(definition)
	item.SlotStart = container.SlotStart
	item.SlotEnd = container.SlotEnd
	policy := currentPVFItemPlacementPolicy(definition)
	item.AllowQuickSlots = policy.AllowQuickSlots
	item.PreferQuickSlots = policy.PreferQuickSlots
	if container.ListType != dnfrepo.MainInventoryListType {
		item.TargetListType = container.ListType
	}
	if container.PetBody {
		item.StackLimit = 1
	}
	document, err := parseDungeonCardPVFDocument(source, definition.PVFPath)
	if err != nil {
		return alignedcmd.MagicBoxRewardItem{}, fmt.Errorf("parse magic box reward item=%d path=%s: %w", itemID, definition.PVFPath, err)
	}
	attachType := normalizeMagicBoxPVFType(magicBoxDocumentText(document, "attach type"))
	item.Seal = attachType == "sealing"
	// 与 dungeon_drop_catalog.go:244 同口径的三键回退（提取为 firstPositivePVFInt）：
	// 这里要的是"新造实例的满耐久"，即 PVF 的最大耐久，而不是当前耐久 ——
	// 消费方 packageitem/owner.go:784 与 :865 把同一个值同时写进 Extra 的
	// durability 与 max_durability。只写 [maximum durability] / [max durability]
	// 的奖励装备过去在这里读不到值（item.Durability 保持 0），发出去的实例是
	// "0 耐久"且不可修，与修理定价是同一条缺陷族。
	// 上限仍按 uint16 字段宽度守住 <= 65535（与 dungeon_drop_catalog 的 clamp 同界）。
	if durability, found := firstPositivePVFInt(document, "durability", "maximum durability", "max durability"); found && durability <= 65535 {
		item.Durability = uint16(durability)
	}
	return item, nil
}

func magicBoxDocumentText(document *dnfpvf.Document, name string) string {
	values := document.Texts(name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func magicBoxSectionInts(document *dnfpvf.Document, section dnfpvf.Section) []int64 {
	if section.Start < 0 || section.End > len(document.Tokens) || section.Start > section.End {
		return nil
	}
	out := make([]int64, 0, section.End-section.Start)
	for _, token := range document.Tokens[section.Start:section.End] {
		if token.Kind == dnfpvf.TokenInt {
			out = append(out, token.Int)
		}
	}
	return out
}

func normalizeMagicBoxSectionName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func normalizeMagicBoxPVFType(value string) string {
	return strings.ToLower(strings.Trim(value, "` []"))
}

func maxMagicBoxInt(value int64, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}
