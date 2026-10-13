package dnfbridge

import (
	cryptorand "crypto/rand"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// boosterDuplicateSelectionCategoryEnv 是"同一个 `[booster select category]` id
// 出现多次"这一真档形状的读取档位开关。
//
//	未设置 / merge / 1 / true / on  = 合并（默认；第二个块的行追加进同一 category）
//	reject / 0 / false / off / legacy = 一键回退到 2026-09-28 之前：整份文档判非法
//	                                   （错误串 `duplicate booster selection category=<id>`）
//
// 只影响重复 category id 的读法，不改变任何其它 PVF 形状的接受与否。
const (
	boosterDuplicateSelectionCategoryEnv    = "DNFBRIDGE_BOOSTER_SELECT_CATEGORY_DUPLICATE"
	boosterDuplicateSelectionCategoryReject = "reject"
)

func boosterDuplicateSelectionCategoryMergeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(envString(boosterDuplicateSelectionCategoryEnv, ""))) {
	case boosterDuplicateSelectionCategoryReject, "0", "false", "off", "no", "legacy":
		return false
	default:
		return true
	}
}

func resolveCurrentBoosterDefinition(
	catalog *pvfDungeonDropCatalog,
	sourceItemID uint32,
	requestKind currentBoosterRequestKind,
) (currentBoosterDefinition, error) {
	if catalog == nil || catalog.source == nil || sourceItemID == 0 {
		return currentBoosterDefinition{}, errCurrentBoosterPVFInvalid
	}
	sourceDefinition, err := catalog.ResolveItem(sourceItemID)
	if err != nil {
		return currentBoosterDefinition{}, fmt.Errorf("%w: resolve source item=%d: %v", errCurrentBoosterPVFInvalid, sourceItemID, err)
	}
	if sourceDefinition.Kind != dungeonDropItemStackable {
		return currentBoosterDefinition{}, fmt.Errorf("%w: source item=%d is not stackable", errCurrentBoosterPVFInvalid, sourceItemID)
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, sourceDefinition.PVFPath)
	if err != nil {
		return currentBoosterDefinition{}, fmt.Errorf("%w: read source item=%d: %v", errCurrentBoosterPVFInvalid, sourceItemID, err)
	}
	stackableType := normalizeDungeonDropStackableType(magicBoxDocumentText(document, "stackable type"))
	definition := currentBoosterDefinition{Source: sourceDefinition, StackableType: stackableType}
	switch requestKind {
	case currentBoosterRequestSelection:
		if stackableType != "[booster selection]" {
			return currentBoosterDefinition{}, fmt.Errorf("%w: source item=%d type=%q is not selectable", errCurrentBoosterPVFInvalid, sourceItemID, stackableType)
		}
		requiredValues := document.Ints("booster selection num")
		if len(requiredValues) == 0 || requiredValues[0] < 0 || requiredValues[0] > currentBoosterMaxRewardRows {
			return currentBoosterDefinition{}, fmt.Errorf("%w: source item=%d selection_num=%v", errCurrentBoosterPVFInvalid, sourceItemID, requiredValues)
		}
		definition.SelectionRequired = int(requiredValues[0])
		definition.Selection, definition.SelectionCategory, err = parseCurrentBoosterSelectionCatalog(document)
		if err != nil {
			return currentBoosterDefinition{}, fmt.Errorf("%w: source item=%d: %v", errCurrentBoosterPVFInvalid, sourceItemID, err)
		}
		if len(definition.Selection) == 0 || len(definition.SelectionCategory) == 0 {
			return currentBoosterDefinition{}, fmt.Errorf(
				"%w: source item=%d selection_rows=%d selection_categories=%d",
				errCurrentBoosterPVFInvalid,
				sourceItemID,
				len(definition.Selection),
				len(definition.SelectionCategory),
			)
		}
	case currentBoosterRequestRandom:
		if stackableType != "[booster]" && stackableType != "[cera booster]" && stackableType != "[booster random]" && stackableType != "[random upgradable legacy]" && stackableType != "[upgradable legacy]" {
			return currentBoosterDefinition{}, fmt.Errorf("%w: source item=%d type=%q is not random", errCurrentBoosterPVFInvalid, sourceItemID, stackableType)
		}
		if stackableType == "[upgradable legacy]" {
			levels := document.Ints("minimum level")
			if len(levels) > 1 || (len(levels) == 1 && (levels[0] < 0 || levels[0] > math.MaxUint8)) {
				return currentBoosterDefinition{}, errCurrentBoosterPVFInvalid
			}
			if len(levels) == 1 {
				definition.GoldMinimumLevel = levels[0]
			}
		}
		definition.Random, err = resolveCurrentMagicBox(catalog, catalog.source, int64(sourceItemID))
		if err != nil {
			return currentBoosterDefinition{}, err
		}
		if definition.Random.Kind != "random" || len(definition.Random.Groups) == 0 {
			return currentBoosterDefinition{}, fmt.Errorf("%w: source item=%d has no random reward groups", errCurrentBoosterPVFInvalid, sourceItemID)
		}
	default:
		return currentBoosterDefinition{}, errCurrentBoosterRequestMalformed
	}
	if materialValues := document.Ints("need material"); len(materialValues) > 0 {
		if len(materialValues) < 2 ||
			materialValues[0] <= 0 ||
			materialValues[1] <= 0 ||
			materialValues[1] > currentBoosterMaxRewardUnits {
			return currentBoosterDefinition{}, fmt.Errorf("%w: source item=%d need_material=%v", errCurrentBoosterPVFInvalid, sourceItemID, materialValues)
		}
		definition.MaterialItemID = materialValues[0]
		definition.MaterialCount = materialValues[1]
	}
	return definition, nil
}

func parseCurrentBoosterSelectionCandidates(document *dnfpvf.Document) ([]currentBoosterSelectionCandidate, error) {
	candidates, _, err := parseCurrentBoosterSelectionCatalog(document)
	return candidates, err
}

func parseCurrentBoosterSelectionCatalog(
	document *dnfpvf.Document,
) ([]currentBoosterSelectionCandidate, map[uint16][]currentBoosterSelectionCandidate, error) {
	if document == nil {
		return nil, nil, errCurrentBoosterPVFInvalid
	}
	inCategory := false
	// categoryReopened 只在"同一个 category id 的第二段块"内为真：真档里
	// `[booster select category] 7 4` 会在同一份 .stk 里出现两次（第二次是
	// `[stackable]` 空占位块），合并后块内出现与第一段完全相同的行时按重复
	// 占位丢弃，而不是把整只礼盒判成非法 PVF（旧行为 ⇒ 客户端弹通用失败码 4）。
	categoryReopened := false
	var categoryID uint16
	result := make([]currentBoosterSelectionCandidate, 0, 8)
	seen := make(map[uint32]currentBoosterSelectionCandidate)
	categorySeen := make(map[uint16]map[uint32]struct{})
	categories := make(map[uint16][]currentBoosterSelectionCandidate)
	for _, section := range document.Sections {
		name := normalizeMagicBoxSectionName(section.Name)
		switch name {
		case "booster select category":
			if inCategory {
				return nil, nil, fmt.Errorf("nested booster selection category")
			}
			header := magicBoxSectionInts(document, section)
			if len(header) == 0 ||
				header[0] < 0 ||
				header[0] > math.MaxUint8 ||
				(len(header) > 1 && (header[1] < 0 || header[1] > math.MaxUint8)) {
				return nil, nil, fmt.Errorf("invalid booster selection category header=%v", header)
			}
			// Current NoPack writes the two category header bytes separately
			// immediately after source_slot. Keep the same little-endian key
			// instead of discarding the grow-type/subcategory byte.
			categoryID = uint16(header[0])
			if len(header) > 1 {
				categoryID |= uint16(header[1]) << 8
			}
			categoryReopened = false
			if _, duplicate := categories[categoryID]; duplicate {
				// 真档证据（`runtime/data/dnf/Script.pvf` 的
				// `stackable/cash/chn_490700910.stk`，道具 490700910 = 灿烂徽章礼盒）：
				// 61 个 `[booster select category]` 块只有 60 个不同 id ——
				// `7 4`（id 1031）出现两次，第二次是 `[stackable]` 空占位块。
				// 线上同一时刻的拒绝理由逐字为
				// `duplicate booster selection category=1031`，op160 失败体恒为
				// `00 04`，客户端把 body[1]=4 渲染成「物品栏空间不足」
				// （厂商 Network/Builders/CeraShop/CeraShopPurchaseAckBuilder.cs:8-13）。
				// 空占位块不携带任何选项，因此合并（第二个块的行追加进同一 category）
				// 才是不丢数据的读法；`DNFBRIDGE_BOOSTER_SELECT_CATEGORY_DUPLICATE=reject`
				// 一键回到 2026-09-28 之前的"整档判非法"。
				if !boosterDuplicateSelectionCategoryMergeEnabled() {
					return nil, nil, fmt.Errorf("duplicate booster selection category=%d", categoryID)
				}
				categoryReopened = true
				inCategory = true
				continue
			}
			inCategory = true
			categories[categoryID] = make([]currentBoosterSelectionCandidate, 0, 8)
			categorySeen[categoryID] = make(map[uint32]struct{})
			continue
		case "/booster select category":
			if !inCategory {
				return nil, nil, fmt.Errorf("booster selection category close without open")
			}
			inCategory = false
			continue
		}
		if !inCategory || strings.HasPrefix(name, "/") {
			continue
		}
		values := magicBoxSectionInts(document, section)
		if len(values) == 0 {
			continue
		}
		stride := 0
		switch name {
		case "avatar":
			// Avatar choices carry item/count plus the client attribute
			// category and option written in the 10-byte op160 request.
			stride = 4
		case "stackable", "creature", "equipment", "etc":
			// Ordinary items, pets and enchant orbs are simple item/count
			// pairs. Treating two adjacent pairs as one avatar quad made the
			// second item look like an option byte and rejected valid choices.
			stride = 2
		default:
			return nil, nil, fmt.Errorf("selection section=%q with %d integer values has unsupported runtime-PVF shape", section.Name, len(values))
		}
		if len(values)%stride != 0 {
			return nil, nil, fmt.Errorf("selection section=%q values=%d is not divisible by stride=%d", section.Name, len(values), stride)
		}
		for offset := 0; offset < len(values); offset += stride {
			itemID, count := values[offset], values[offset+1]
			categoryKind, option := int64(0), int64(0)
			if stride == 4 {
				categoryKind, option = values[offset+2], values[offset+3]
			}
			if itemID <= 0 || itemID > math.MaxUint32 || count <= 0 || count > currentBoosterMaxRewardUnits || option < 0 || option > math.MaxUint8 {
				return nil, nil, fmt.Errorf("selection section=%q invalid row=%v", section.Name, values[offset:offset+stride])
			}
			candidate := currentBoosterSelectionCandidate{
				ItemID:       uint32(itemID),
				Count:        uint32(count),
				CategoryKind: categoryKind,
				Option:       byte(option),
			}
			if previous, duplicate := seen[candidate.ItemID]; duplicate {
				if previous != candidate {
					return nil, nil, fmt.Errorf("selection item=%d has conflicting rows", candidate.ItemID)
				}
			} else {
				seen[candidate.ItemID] = candidate
				result = append(result, candidate)
			}
			if _, duplicate := categorySeen[categoryID][candidate.ItemID]; duplicate {
				if categoryReopened {
					// 合并进来的第二段块重复了第一段已有的行：它是同一份数据的
					// 重复占位，丢掉这一行即可（真档 490700910 的第二段是空块，
					// 这条只是对"非空重复块"的保守兜底）。
					continue
				}
				return nil, nil, fmt.Errorf("selection category=%d has duplicate item=%d", categoryID, candidate.ItemID)
			}
			categorySeen[categoryID][candidate.ItemID] = struct{}{}
			categories[categoryID] = append(categories[categoryID], candidate)
		}
	}
	if inCategory {
		return nil, nil, fmt.Errorf("booster selection category is not closed")
	}
	for id, candidates := range categories {
		// Job/grow-type selection documents retain empty category placeholders
		// for combinations the client does not offer (for example a job without
		// a fifth advancement).  They do not invalidate populated categories.
		// currentBoosterSelectedCandidates still rejects an empty category if a
		// forged request explicitly selects one.
		if len(candidates) > currentBoosterMaxRewardRows {
			return nil, nil, fmt.Errorf("booster selection category=%d rows=%d", id, len(candidates))
		}
	}
	return result, categories, nil
}

func currentBoosterSelectedCandidates(
	definition currentBoosterDefinition,
	request currentBoosterOpenRequest,
) ([]currentBoosterSelectionCandidate, error) {
	if len(request.Selections) == 0 {
		return nil, errCurrentBoosterSelectionInvalid
	}
	category, found := definition.SelectionCategory[request.SelectionContext]
	if !found || len(category) == 0 {
		return nil, fmt.Errorf("%w: category=%d is not in source PVF", errCurrentBoosterSelectionInvalid, request.SelectionContext)
	}
	required := definition.SelectionRequired
	if required == 0 {
		required = len(category)
	}
	if required <= 0 || len(request.Selections) != required {
		return nil, fmt.Errorf(
			"%w: category=%d selected=%d want=%d",
			errCurrentBoosterSelectionInvalid,
			request.SelectionContext,
			len(request.Selections),
			required,
		)
	}
	byItem := make(map[uint32]currentBoosterSelectionCandidate, len(category))
	for _, candidate := range category {
		byItem[candidate.ItemID] = candidate
	}
	selected := make([]currentBoosterSelectionCandidate, 0, len(request.Selections))
	seen := make(map[uint32]struct{}, len(request.Selections))
	for _, choice := range request.Selections {
		if choice.ItemID == 0 {
			return nil, errCurrentBoosterSelectionInvalid
		}
		if _, duplicate := seen[choice.ItemID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate item=%d", errCurrentBoosterSelectionInvalid, choice.ItemID)
		}
		seen[choice.ItemID] = struct{}{}
		candidate, found := byItem[choice.ItemID]
		if !found {
			return nil, fmt.Errorf(
				"%w: item=%d is not in source PVF category=%d",
				errCurrentBoosterSelectionInvalid,
				choice.ItemID,
				request.SelectionContext,
			)
		}
		if candidate.CategoryKind != 0 {
			// Avatar category rows carry the client-selected attribute byte.
			// The fourth PVF integer is not that runtime choice: the live
			// eight-piece writer sends different values for hat/hair/top/etc.
			candidate.Option = choice.Option
		} else if candidate.Option != choice.Option {
			return nil, fmt.Errorf("%w: item=%d option=%d want=%d", errCurrentBoosterSelectionInvalid, choice.ItemID, choice.Option, candidate.Option)
		}
		selected = append(selected, candidate)
	}
	return selected, nil
}

func validateCurrentBoosterSourceExpiration(stack dnfrepo.ItemStack, definition dungeonDropItemDefinition, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if expire := currentItemListStackExpire(stack); expire != 0 {
		if uint64(expire) <= uint64(now.Unix()) {
			return errCurrentBoosterExpired
		}
		return nil
	}
	if definition.UsablePeriodDays > 0 && currentItemListStackExpire(stack) == 0 {
		return errCurrentBoosterExpired
	}
	if !definition.ExpirationDate.IsZero() && !now.Before(definition.ExpirationDate) {
		return errCurrentBoosterExpired
	}
	return nil
}
func resolveCurrentBoosterRewardAmounts(
	definition currentBoosterDefinition,
	request currentBoosterOpenRequest,
	roll func(int64) (int64, error),
) (map[uint32]uint32, map[uint32]byte, error) {
	amounts := make(map[uint32]uint32)
	options := make(map[uint32]byte)
	switch request.Kind {
	case currentBoosterRequestSelection:
		candidates, err := currentBoosterSelectedCandidates(definition, request)
		if err != nil {
			return nil, nil, err
		}
		for _, candidate := range candidates {
			current := uint64(amounts[candidate.ItemID])
			if current+uint64(candidate.Count) > currentBoosterMaxRewardUnits {
				return nil, nil, errCurrentBoosterPVFInvalid
			}
			amounts[candidate.ItemID] = uint32(current + uint64(candidate.Count))
			options[candidate.ItemID] = candidate.Option
		}
	case currentBoosterRequestRandom:
		if roll == nil {
			return nil, nil, errCurrentBoosterPVFInvalid
		}
		for _, group := range definition.Random.Groups {
			totalWeight := int64(0)
			for _, entry := range group.Entries {
				if entry.ItemID < 0 || (entry.ItemID == 0 && definition.StackableType != "[upgradable legacy]") || entry.ItemID > math.MaxUint32 || entry.Weight < 0 || entry.Count <= 0 || uint64(entry.Count) > currentBoosterRewardUnitLimit(uint32(entry.ItemID)) {
					return nil, nil, errCurrentBoosterPVFInvalid
				}
				if entry.Weight > math.MaxInt64-totalWeight {
					return nil, nil, errCurrentBoosterPVFInvalid
				}
				totalWeight += entry.Weight
			}
			if totalWeight <= 0 {
				return nil, nil, errCurrentBoosterPVFInvalid
			}
			drawCount := group.DrawCount
			if drawCount < 1 {
				drawCount = 1
			}
			if drawCount > currentBoosterMaxRewardRows {
				return nil, nil, errCurrentBoosterPVFInvalid
			}
			for draw := int64(0); draw < drawCount; draw++ {
				value, err := roll(totalWeight)
				if err != nil || value < 0 || value >= totalWeight {
					return nil, nil, fmt.Errorf("%w: random roll: %v", errCurrentBoosterPVFInvalid, err)
				}
				cumulative := int64(0)
				for _, entry := range group.Entries {
					cumulative += entry.Weight
					if value >= cumulative {
						continue
					}
					current := uint64(amounts[uint32(entry.ItemID)])
					if current+uint64(entry.Count) > currentBoosterRewardUnitLimit(uint32(entry.ItemID)) {
						return nil, nil, errCurrentBoosterPVFInvalid
					}
					amounts[uint32(entry.ItemID)] = uint32(current + uint64(entry.Count))
					break
				}
			}
		}
	default:
		return nil, nil, errCurrentBoosterRequestMalformed
	}
	if len(amounts) == 0 || len(amounts) > currentBoosterMaxRewardRows {
		return nil, nil, errCurrentBoosterPVFInvalid
	}
	return amounts, options, nil
}

func secureCurrentBoosterRoll(limit int64) (int64, error) {
	if limit <= 0 {
		return 0, errCurrentBoosterPVFInvalid
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(limit))
	if err != nil {
		return 0, err
	}
	return value.Int64(), nil
}

func resolveCurrentBoosterRewards(
	catalog *pvfDungeonDropCatalog,
	amounts map[uint32]uint32,
	options map[uint32]byte,
	sourceStack dnfrepo.ItemStack,
	now time.Time,
) ([]currentBoosterReward, error) {
	itemIDs := make([]uint32, 0, len(amounts))
	for itemID := range amounts {
		itemIDs = append(itemIDs, itemID)
	}
	sort.Slice(itemIDs, func(i, j int) bool { return itemIDs[i] < itemIDs[j] })
	rewards := make([]currentBoosterReward, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		count := amounts[itemID]
		definition, err := catalog.ResolveItem(itemID)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve reward item=%d: %v", errCurrentBoosterPVFInvalid, itemID, err)
		}
		definition, err = currentPVFItemDefinitionForNestedRewardGrantAt(definition, sourceStack, now)
		if err != nil {
			return nil, fmt.Errorf("%w: reward item=%d expiration: %v", errCurrentBoosterPVFInvalid, itemID, err)
		}
		document, err := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
		if err != nil {
			return nil, fmt.Errorf("%w: reward item=%d: %v", errCurrentBoosterPVFInvalid, itemID, err)
		}
		attachType := normalizeMagicBoxPVFType(magicBoxDocumentText(document, "attach type"))
		placement, err := currentBoosterPlacement(definition)
		if err != nil {
			return nil, err
		}
		rewards = append(rewards, currentBoosterReward{
			Definition: definition,
			Count:      count,
			Option:     options[itemID],
			Seal:       attachType == "sealing",
			Placement:  placement,
		})
	}
	return rewards, nil
}

// currentBoosterPlacement 决定礼盒奖励落到哪个容器。判定本体是
// currentPVFItemTargetContainer（唯一真源）：宠物消耗品家族 = `[feed]` + `[creature]`
// （更名卡 25 属于后者，旧代码只认 `[feed]`，于是礼盒开出的更名卡落在主背包消耗品页）。
func currentBoosterPlacement(definition dungeonDropItemDefinition) (currentBoosterRewardPlacement, error) {
	container := currentPVFItemTargetContainer(definition)
	switch definition.Kind {
	case dungeonDropItemStackable:
		if container.PetConsumable {
			return currentBoosterRewardPetConsumable, nil
		}
		if container.GuardianGem {
			// 公会守护珠（[stackable type] `[flag gem]`）属于 wire list 38 的
			// 守护珠页 49..97，不是主背包消耗品页。
			return currentBoosterRewardGuildMedal, nil
		}
		return currentBoosterRewardMain, nil
	case dungeonDropItemEquipment:
		switch {
		case container.PetBody:
			return currentBoosterRewardPetBody, nil
		case container.Avatar:
			return currentBoosterRewardAvatar, nil
		case container.PetArtifact:
			return currentBoosterRewardPetArtifact, nil
		case container.GuildMedal:
			// 公会勋章（[equipment type] `[flag]`）属于 wire list 38 的勋章页 0..48。
			return currentBoosterRewardGuildMedal, nil
		}
		return currentBoosterRewardMain, nil
	default:
		return currentBoosterRewardMain, errCurrentBoosterPVFInvalid
	}
}

func currentBoosterRewardUnitLimit(itemID uint32) uint64 {
	if itemID == 0 {
		return math.MaxInt32
	}
	return currentBoosterMaxRewardUnits
}
