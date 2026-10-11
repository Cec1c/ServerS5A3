package dnfbridge

import (
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrevivecoin "longheng.io/server/internal/modules/dnf/revivecoin"
)

func currentCeraShopProductDefinitionForGrantAt(
	definition dungeonDropItemDefinition,
	product currentCeraShopProduct,
	now time.Time,
) (dungeonDropItemDefinition, error) {
	resolved, err := currentPVFItemDefinitionForGrantAt(definition, now)
	if err != nil {
		return dungeonDropItemDefinition{}, err
	}
	if product.DurationDays == 0 {
		return resolved, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowUnix := now.Unix()
	durationSeconds := uint64(product.DurationDays) * uint64((24*time.Hour)/time.Second)
	if nowUnix <= 0 || nowUnix > math.MaxUint32 || durationSeconds > uint64(math.MaxUint32)-uint64(nowUnix) {
		return dungeonDropItemDefinition{}, fmt.Errorf(
			"%w: commodity=%d duration_days=%d now=%d",
			errCurrentCeraShopProductUnavailable,
			product.CommodityID,
			product.DurationDays,
			nowUnix,
		)
	}
	resolved.ExpirationDate = time.Unix(nowUnix+int64(durationSeconds), 0).UTC()
	return resolved, nil
}

func consumeCurrentCeraShopAvatarCoupon(inventory *dnfrepo.InventoryRecord, couponItemID int64) (currentItemListEntry, error) {
	if inventory == nil || couponItemID <= 0 {
		return currentItemListEntry{}, errCurrentCeraShopOwnerUnavailable
	}
	bestKey := ""
	bestSlot := int16(math.MaxInt16)
	var bestStack dnfrepo.ItemStack
	for key, stack := range inventory.Slots {
		listType, slot, ok := parseSceneInventorySlotKey(key)
		if !ok || listType != dnfrepo.MainInventoryListType || slot < 0 || dnfrepo.IsAccountSharedInventorySlot(listType, slot) {
			continue
		}
		if stack.ItemID != couponItemID || stack.Count <= 0 {
			continue
		}
		if bestKey == "" || slot < bestSlot {
			bestKey = key
			bestSlot = slot
			bestStack = stack
		}
	}
	if bestKey == "" {
		return currentItemListEntry{}, fmt.Errorf("%w: avatar coupon item=%d", errCurrentCeraShopCeraInsufficient, couponItemID)
	}
	bestStack.Count--
	if bestStack.Count <= 0 {
		delete(inventory.Slots, bestKey)
		var removed currentItemListEntry
		removed.patchCore(bestSlot, math.MaxUint32, 0)
		return removed, nil
	}
	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, bestSlot, bestStack)
	bestStack.RawEntry = append([]byte(nil), entry.data[:]...)
	inventory.Slots[bestKey] = bestStack
	return entry, nil
}

// currentCeraShopMainInventoryUpgradeStage recognizes the three main-bag
// expansion products through their runtime PVF paths. The returned value is
// the Cera-shop stage (1..3); the persisted op13 list-0 expansion is stage*8.
func currentCeraShopMainInventoryUpgradeStage(definition dungeonDropItemDefinition) (uint16, bool) {
	if definition.Kind != dungeonDropItemStackable {
		return 0, false
	}
	cleanPath := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(definition.PVFPath, "\\", "/")))
	cleanPath = strings.TrimPrefix(cleanPath, "stackable/")
	switch cleanPath {
	case "cash/inven_upgradekit1.stk":
		return 1, true
	case "cash/inven_upgradekit2.stk":
		return 2, true
	case "cash/chn_20140617_new_sales_item/chn_inventory_3rd_expansion_2683675.stk":
		return 3, true
	default:
		return 0, false
	}
}

// currentCeraShopAvatarInventoryUpgradeStage recognizes the avatar-slot expansion
// tickets through their runtime PVF paths.  The live catalog names them
// 装扮栏扩展券 (N阶段) with the explain text "装扮栏的空间增加7格。可以扩展到N阶段",
// so the persisted op13 list-1 param16 is stage*7 exactly the way the main bag
// persists stage*8.  Items 2683901..2683908 are the eight proven stages; coupons
// 2683909..2683915 exist in the PVF but are outside the current EXE table
// (0..56), so they are recognized and rejected at checkout instead of charging
// Cera for nothing.
func currentCeraShopAvatarInventoryUpgradeStage(definition dungeonDropItemDefinition) (uint16, bool) {
	if definition.Kind != dungeonDropItemStackable {
		return 0, false
	}
	cleanPath := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(definition.PVFPath, "\\", "/")))
	cleanPath = strings.TrimPrefix(cleanPath, "stackable/")
	const prefix = "cash/chn_20140819_avatar_inven/avatar_inven_"
	if !strings.HasPrefix(cleanPath, prefix) || path.Ext(cleanPath) != ".stk" {
		return 0, false
	}
	itemText := strings.TrimSuffix(strings.TrimPrefix(cleanPath, prefix), ".stk")
	itemID, err := strconv.ParseUint(itemText, 10, 32)
	if err != nil {
		return 0, false
	}
	const (
		firstAvatarUpgradeItemID = 2683901
		lastAvatarUpgradeItemID  = 2683908
	)
	if itemID < firstAvatarUpgradeItemID || itemID > lastAvatarUpgradeItemID {
		// The directory is positively the avatar-slot expansion family, but this
		// member is not one of the eight proven stages.
		return 0, true
	}
	return uint16(itemID-firstAvatarUpgradeItemID) + 1, true
}

// currentCeraShopPersonalCargoUpgradeTarget recognizes the original personal
// cargo tools from their real PVF paths. The no-suffix tool is the first tier
// (8 -> 24); numeric suffixes identify later 16-slot tiers.  The later live
// catalog uses a separate numeric directory, so catalog.personalCargoUpgradeTarget
// augments this path-only recognition with the item's own PVF name/explain
// fields rather than with a copied commodity-ID table.
func currentCeraShopPersonalCargoUpgradeTarget(definition dungeonDropItemDefinition) (uint16, bool) {
	if definition.Kind != dungeonDropItemStackable {
		return 0, false
	}
	cleanPath := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(definition.PVFPath, "\\", "/")))
	if cleanPath == "" || strings.Contains(cleanPath, "cash/chn_account_cargo/") {
		return 0, false
	}
	base := path.Base(cleanPath)
	if path.Ext(base) != ".stk" {
		return 0, false
	}
	stem := strings.TrimSuffix(base, ".stk")
	const prefix = "safe_upgradekit"
	if !strings.HasPrefix(stem, prefix) {
		return 0, false
	}
	tierText := strings.TrimPrefix(stem, prefix)
	if tierText != "" {
		for _, digit := range tierText {
			if digit < '0' || digit > '9' {
				return 0, false
			}
		}
	}
	tier := 0
	if tierText != "" {
		parsed, err := strconv.ParseUint(tierText, 10, 16)
		if err != nil {
			return 0, true
		}
		tier = int(parsed)
	}
	const (
		firstPersonalCargoUpgradeSlots = 24
		personalCargoUpgradeStep       = 16
		maximumPersonalCargoUpgrade    = 200
	)
	target := firstPersonalCargoUpgradeSlots + tier*personalCargoUpgradeStep
	if target > maximumPersonalCargoUpgrade {
		return 0, true
	}
	return uint16(target), true
}

// isAccountCargoUpgradeTool recognizes the account-cargo tool through the
// active PVF path, not through a copied commodity or item-template ID.  The
// ordinary personal-cargo recognizer explicitly excludes this directory.
func (c *pvfCeraShopCatalog) isAccountCargoUpgradeTool(definition dungeonDropItemDefinition) bool {
	if c == nil || definition.Kind != dungeonDropItemStackable {
		return false
	}
	cleanPath := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(definition.PVFPath, "\\", "/")))
	return path.Ext(cleanPath) == ".stk" && strings.Contains(cleanPath, "cash/chn_account_cargo/account_cargo")
}

// personalCargoUpgradeTarget recognizes every real personal-cargo tool in
// the active PVF.  The post-152 products are not named safe_upgradekit*.stk:
// their stable contract is the stackable item's exact name plus an explicit
// target capacity in [explain].  Both values are read from the PVF that also
// supplied the requested Cera product; neither a client field nor a static
// commodity-ID map can turn an ordinary item into a warehouse upgrade.
func (c *pvfCeraShopCatalog) personalCargoUpgradeTarget(definition dungeonDropItemDefinition) (uint16, bool) {
	if target, isUpgrade := currentCeraShopPersonalCargoUpgradeTarget(definition); isUpgrade {
		return target, true
	}
	if c == nil || c.source == nil || definition.Kind != dungeonDropItemStackable {
		return 0, false
	}
	cleanPath := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(definition.PVFPath, "\\", "/")))
	if cleanPath == "" || strings.Contains(cleanPath, "cash/chn_account_cargo/") || path.Ext(cleanPath) != ".stk" {
		return 0, false
	}
	document, err := parseDungeonCardPVFDocument(c.source, definition.PVFPath)
	if err != nil {
		return 0, false
	}
	name, nameFound := document.Text("name")
	if !nameFound || strings.TrimSpace(name) != "金库升级工具" {
		return 0, false
	}
	explain, explainFound := document.Text("explain")
	target, valid := currentCeraShopPersonalCargoTargetFromExplain(explain)
	if !explainFound || !valid {
		// This is positively a warehouse tool but its target is not a current
		// EXE-valid capacity. Reject it at checkout instead of silently placing
		// an unusable tool in the bag.
		return 0, true
	}
	return target, true
}

func currentCeraShopPersonalCargoTargetFromExplain(explain string) (uint16, bool) {
	const (
		prefix = "可以使金库的空间增加到"
		suffix = "格"
		max    = uint64(200)
	)
	clean := strings.TrimSpace(explain)
	if !strings.HasPrefix(clean, prefix) {
		return 0, false
	}
	remaining := strings.TrimPrefix(clean, prefix)
	end := strings.Index(remaining, suffix)
	if end <= 0 {
		return 0, false
	}
	target, err := strconv.ParseUint(strings.TrimSpace(remaining[:end]), 10, 16)
	if err != nil || target > max || target < uint64(currentExeInitialPersonalCargoSlotCount) {
		return 0, false
	}
	value := uint16(target)
	if (value-uint16(currentExeInitialPersonalCargoSlotCount))%16 != 0 {
		return 0, false
	}
	return value, true
}

// currentCeraShopContainerUpgradeResult 是本次结账实际推进的容器档位，
// 供成功回包决定要刷新哪些容器列表（list 0 / list 1 / list 2）。
type currentCeraShopContainerUpgradeResult struct {
	MainInventoryExpansion   uint16
	PersonalCargoSlotCount   uint16
	AvatarInventoryExpansion uint16
}

// currentCeraShopPrepareContainerUpgrades validates and projects the persisted
// current EXE list-0/list-1/list-2 headers inside the Cera-shop asset transaction.
func currentCeraShopPrepareContainerUpgrades(
	record *dnfrepo.SettingsRecord,
	found bool,
	characterID string,
	purchases []currentCeraShopResolvedPurchase,
	now time.Time,
) (dnfrepo.SettingsRecord, currentCeraShopContainerUpgradeResult, bool, error) {
	hasUpgrade := false
	for _, purchase := range purchases {
		if purchase.MainInventoryUpgradeStage != 0 ||
			purchase.PersonalCargoUpgradeTarget != 0 ||
			purchase.AvatarInventoryUpgradeStage != 0 {
			hasUpgrade = true
			break
		}
	}
	if !hasUpgrade {
		return dnfrepo.SettingsRecord{}, currentCeraShopContainerUpgradeResult{}, false, nil
	}
	if record == nil || strings.TrimSpace(characterID) == "" {
		return dnfrepo.SettingsRecord{}, currentCeraShopContainerUpgradeResult{}, false, errCurrentCeraShopOwnerUnavailable
	}

	before := dnfrepo.CloneSettings(*record)
	currentMainExpansion := uint16(currentExeInitialMainSlotCount)
	currentSlots := uint16(currentExeInitialPersonalCargoSlotCount)
	currentAvatarExpansion := uint16(currentExeInitialAvatarExpansion)
	if !found {
		before = newCharacterContainerStateSettings(characterID, now)
	} else {
		state, stateErr := dnfrepo.CharacterContainerStateFromSettings(before, characterID)
		if stateErr != nil {
			return dnfrepo.SettingsRecord{}, currentCeraShopContainerUpgradeResult{}, false, stateErr
		}
		currentMainExpansion = state.MainSlotCount
		currentSlots = state.PersonalCargoSlotCount
		currentAvatarExpansion = state.AvatarExpansion
	}

	targetMainExpansion := currentMainExpansion
	targetSlots := currentSlots
	targetAvatarExpansion := currentAvatarExpansion
	lastMainCommodityID := uint32(0)
	lastPersonalCargoCommodityID := uint32(0)
	lastAvatarCommodityID := uint32(0)
	for _, purchase := range purchases {
		if purchase.MainInventoryUpgradeStage != 0 {
			targetExpansion := purchase.MainInventoryUpgradeStage * 8
			if purchase.MainInventoryUpgradeStage > 3 || targetExpansion != targetMainExpansion+8 || targetExpansion > 24 {
				return dnfrepo.SettingsRecord{}, currentCeraShopContainerUpgradeResult{}, false, fmt.Errorf("%w: commodity=%d target_main_expansion=%d current_main_expansion=%d", errCurrentCeraShopProductUnavailable, purchase.Product.CommodityID, targetExpansion, targetMainExpansion)
			}
			targetMainExpansion = targetExpansion
			lastMainCommodityID = purchase.Product.CommodityID
		}
		if purchase.PersonalCargoUpgradeTarget != 0 {
			if purchase.PersonalCargoUpgradeTarget <= targetSlots {
				return dnfrepo.SettingsRecord{}, currentCeraShopContainerUpgradeResult{}, false, fmt.Errorf("%w: commodity=%d target_slots=%d current_slots=%d", errCurrentCeraShopProductUnavailable, purchase.Product.CommodityID, purchase.PersonalCargoUpgradeTarget, targetSlots)
			}
			targetSlots = purchase.PersonalCargoUpgradeTarget
			lastPersonalCargoCommodityID = purchase.Product.CommodityID
		}
		if purchase.AvatarInventoryUpgradeStage != 0 {
			// 装扮栏只能逐级 +7（合法值 0,7..56）。跳级或超出客户端档位表都在结账时
			// 拒绝，避免扣了点券却没有效果。
			targetExpansion := purchase.AvatarInventoryUpgradeStage * currentExeAvatarExpansionStep
			if purchase.AvatarInventoryUpgradeStage > currentExeAvatarExpansionMaxStage ||
				targetExpansion != targetAvatarExpansion+currentExeAvatarExpansionStep {
				return dnfrepo.SettingsRecord{}, currentCeraShopContainerUpgradeResult{}, false, fmt.Errorf("%w: commodity=%d target_avatar_expansion=%d current_avatar_expansion=%d", errCurrentCeraShopProductUnavailable, purchase.Product.CommodityID, targetExpansion, targetAvatarExpansion)
			}
			targetAvatarExpansion = targetExpansion
			lastAvatarCommodityID = purchase.Product.CommodityID
		}
	}

	after := dnfrepo.CloneSettings(before)
	if after.Values == nil {
		after.Values = make(map[string]string)
	}
	result := currentCeraShopContainerUpgradeResult{}
	if targetMainExpansion != currentMainExpansion {
		after.Values["main_list_param16"] = strconv.Itoa(int(targetMainExpansion))
		after.Values["main_inventory_last_upgrade_commodity"] = strconv.FormatUint(uint64(lastMainCommodityID), 10)
		after.Values["main_inventory_last_upgrade_from"] = strconv.Itoa(int(currentMainExpansion))
		after.Values["main_inventory_last_upgrade_to"] = strconv.Itoa(int(targetMainExpansion))
		result.MainInventoryExpansion = targetMainExpansion
	}
	if targetSlots != currentSlots {
		after.Values["personal_cargo_list_param16"] = strconv.Itoa(int(targetSlots))
		after.Values["personal_cargo_last_upgrade_commodity"] = strconv.FormatUint(uint64(lastPersonalCargoCommodityID), 10)
		after.Values["personal_cargo_last_upgrade_from"] = strconv.Itoa(int(currentSlots))
		after.Values["personal_cargo_last_upgrade_to"] = strconv.Itoa(int(targetSlots))
		result.PersonalCargoSlotCount = targetSlots
	}
	if targetAvatarExpansion != currentAvatarExpansion {
		after.Values["avatar_list_param16"] = strconv.Itoa(int(targetAvatarExpansion))
		after.Values["avatar_inventory_last_upgrade_commodity"] = strconv.FormatUint(uint64(lastAvatarCommodityID), 10)
		after.Values["avatar_inventory_last_upgrade_from"] = strconv.Itoa(int(currentAvatarExpansion))
		after.Values["avatar_inventory_last_upgrade_to"] = strconv.Itoa(int(targetAvatarExpansion))
		result.AvatarInventoryExpansion = targetAvatarExpansion
	}
	upgraded := 0
	source := ""
	if result.MainInventoryExpansion != 0 {
		upgraded++
		source = "current_exe_cera_shop_main_inventory_upgrade"
	}
	if result.PersonalCargoSlotCount != 0 {
		upgraded++
		source = "current_exe_cera_shop_personal_cargo_upgrade"
	}
	if result.AvatarInventoryExpansion != 0 {
		upgraded++
		source = "current_exe_cera_shop_avatar_inventory_upgrade"
	}
	if upgraded > 1 {
		source = "current_exe_cera_shop_container_upgrade"
	}
	after.Values["source"] = source
	after.UpdatedAt = now.UTC()
	return after, result, true, nil
}

func grantCurrentCeraShopProduct(
	inventory *dnfrepo.InventoryRecord,
	definition dungeonDropItemDefinition,
	count uint32,
) ([]uint16, error) {
	return grantCurrentCeraShopProductAt(inventory, definition, count, time.Now().UTC())
}

func grantCurrentCeraShopProductAt(inventory *dnfrepo.InventoryRecord, definition dungeonDropItemDefinition, count uint32, now time.Time) ([]uint16, error) {
	if definition.DailyDeleteItem {
		var err error
		definition, err = currentPVFItemDefinitionForGrantAt(definition, now)
		if err != nil {
			return nil, err
		}
	}
	if inventory == nil || count == 0 {
		return nil, errCurrentCeraShopProductUnavailable
	}
	// Current runtime PVF and the 86JP domain reference agree that item 1 is
	// the fixed slot-1 revive-coin wallet, while item 42
	// (cash/coin_general.stk) grants one wallet unit. Neither reward may be
	// spilled into an ordinary backpack slot.
	if dnfrevivecoin.IsReward(int64(definition.ItemID)) {
		if _, err := dnfrevivecoin.Grant(
			inventory,
			int64(count),
			"current_cera_reward_revive_coin_wallet",
		); err != nil {
			return nil, fmt.Errorf("%w: revive coin reward item=%d: %v", errCurrentCeraShopProductUnavailable, definition.ItemID, err)
		}
		return []uint16{uint16(dnfrevivecoin.WalletSlot)}, nil
	}
	remaining := count
	slots := make([]uint16, 0, 1)
	touched := make(map[uint16]struct{})
	appendTouched := func(slot uint16) {
		if _, duplicate := touched[slot]; duplicate {
			return
		}
		touched[slot] = struct{}{}
		slots = append(slots, slot)
	}
	// Package and booster rewards can be a full PVF stack while the player
	// already owns a partial stack. Fill every existing compatible stack
	// first, then spill only the remainder into new slots. Passing the full
	// reward directly to addCurrentDungeonPickupToInventory required the whole
	// chunk to fit and therefore left the partial row untouched.
	fillRange := func(start, end int16) {
		if definition.Kind != dungeonDropItemStackable || remaining == 0 {
			return
		}
		incoming := currentPVFGrantIdentity(definition)
		for slot := start; slot <= end && remaining > 0; slot++ {
			key := currentDungeonPickupMainSlotKey(slot)
			stack, exists := inventory.Slots[key]
			if !exists ||
				stack.ItemID != int64(definition.ItemID) ||
				stack.Bind ||
				stack.Count <= 0 ||
				!currentItemStackExpirationMatches(stack, definition.ExpirationDate) {
				continue
			}
			// 身份判据（dnfrepo.CanMergeItemStack 是仓库层唯一实现）：pvf_path /
			// item_kind / stackable_type 或实例到期时间冲突的行不得并。旧代码只看
			// item_id + 未绑定 + 到期时间字符串相等，于是"同 item 的另一个实例"
			// （例如点券商品与活动奖励发的同名道具）会被并成一行。
			if !dnfrepo.CanMergeItemStack(stack, incoming) {
				continue
			}
			available := int64(math.MaxInt32) - stack.Count
			if definition.StackLimit > 0 {
				if stack.Count >= definition.StackLimit {
					continue
				}
				available = definition.StackLimit - stack.Count
			}
			if available <= 0 {
				continue
			}
			added := int64(remaining)
			if added > available {
				added = available
			}
			stack.Count += added
			if !definition.ExpirationDate.IsZero() {
				stack, _ = applyCurrentPVFItemExpiration(stack, definition)
			}
			entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			inventory.Slots[key] = stack
			remaining -= uint32(added)
			appendTouched(uint16(slot))
		}
	}
	if definition.Kind == dungeonDropItemStackable &&
		dungeonDropStackablePrefersItemQuickSlots(definition.StackableType) {
		fillRange(currentDungeonPickupQuickSlotStart, currentDungeonPickupQuickSlotEnd)
	}
	fillRange(definition.SlotStart, definition.SlotEnd)
	for remaining > 0 {
		chunk := remaining
		switch definition.Kind {
		case dungeonDropItemEquipment:
			chunk = 1
		case dungeonDropItemStackable:
			if definition.StackLimit > 0 && int64(chunk) > definition.StackLimit {
				chunk = uint32(definition.StackLimit)
			}
		default:
			return nil, errCurrentCeraShopProductUnavailable
		}
		slot, err := addCurrentDungeonPickupToInventory(inventory, definition, chunk)
		if err != nil {
			return nil, err
		}
		appendTouched(slot)
		remaining -= chunk
	}
	return slots, nil
}

func grantCurrentCeraShopAvatar(
	inventory *dnfrepo.InventoryRecord,
	source dnfpvf.Source,
	definition dungeonDropItemDefinition,
	product currentCeraShopProduct,
	attributeValue byte,
) (int16, error) {
	// 是否"该进装扮栏"由**运行时 PVF 的装备部位规则**决定（下面紧跟的
	// currentEquipmentPlacementRuleForPVFType(equipmentType) == avatar 检查），
	// 不再要求调用方传进来的 product.Section == "avatar"：商城目录里同一件装扮
	// 可能挂在 [item]/[package]/[visual] 段下，按段分流会让它掉进主背包
	// （现场报障"商城购买装扮放在购物车无法批量购买，提示为物品栏空间不足"）。
	if inventory == nil || source == nil || definition.ItemID == 0 || definition.Kind != dungeonDropItemEquipment {
		return 0, errCurrentCeraShopProductUnavailable
	}
	document, err := parseDungeonCardPVFDocument(source, definition.PVFPath)
	if err != nil {
		return 0, fmt.Errorf("%w: avatar item=%d PVF=%s: %v", errCurrentCeraShopProductUnavailable, definition.ItemID, definition.PVFPath, err)
	}
	equipmentType, found := document.Text("equipment type")
	if !found {
		return 0, fmt.Errorf("%w: avatar item=%d missing equipment type", errCurrentCeraShopProductUnavailable, definition.ItemID)
	}
	rule, ok := currentEquipmentPlacementRuleForPVFType(equipmentType)
	if !ok || rule.class != currentEquipmentPlacementClassAvatar {
		return 0, fmt.Errorf("%w: avatar item=%d type=%q", errCurrentCeraShopProductUnavailable, definition.ItemID, equipmentType)
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	const avatarSlotStart int16 = 0
	const avatarSlotEnd int16 = 500
	var slot int16 = -1
	for candidate := avatarSlotStart; candidate <= avatarSlotEnd; candidate++ {
		if _, occupied := inventory.Slots[currentCeraShopInventorySlotKey(1, candidate)]; !occupied {
			slot = candidate
			break
		}
	}
	if slot < 0 {
		return 0, fmt.Errorf("%w: avatar item=%d range=%d..%d", errDungeonPickupInventoryFull, definition.ItemID, avatarSlotStart, avatarSlotEnd)
	}

	extra := map[string]string{
		"source":                         "cera_shop_avatar",
		"item_kind":                      "avatar",
		"pvf_path":                       definition.PVFPath,
		"equipment_type":                 rule.pvfType,
		"ext_data0":                      strconv.FormatUint(uint64(attributeValue), 10),
		"amount_or_count":                "0", // permanent avatars have no remaining-seconds countdown.
		"avatar_duration_days":           strconv.FormatUint(uint64(product.AvatarDurationDays), 10),
		"avatar_duration_selector_index": strconv.FormatUint(uint64(product.AvatarDurationIndex), 10),
	}
	// Logical count is 1 like every other equipment-kind stack; the wire
	// amount still renders as 0 through the amount_or_count override above
	// (permanent avatars have no remaining-seconds countdown). Consumers such
	// as avatar disjoint require Count == 1 for a single equipment instance.
	stack := dnfrepo.ItemStack{ItemID: int64(definition.ItemID), Count: 1, Extra: extra}
	now := time.Now().UTC()
	if product.AvatarDurationDays > 0 {
		stack.ExpireAt = now.Add(time.Duration(product.AvatarDurationDays) * 24 * time.Hour)
		expireUnix := strconv.FormatInt(stack.ExpireAt.Unix(), 10)
		stack.Extra["expire_time"] = expireUnix
		stack.Extra["expire_unix"] = expireUnix
	} else if !definition.ExpirationDate.IsZero() {
		stack, _ = applyCurrentPVFItemExpirationAt(stack, definition, now)
	}
	// Runtime PVF marks the National Day aura choices as [aurora avatar]
	// with two [emblem socket default] M sockets, and skins ([skin avatar])
	// are born socketed the same way.  Those are creation-time sockets:
	// project them when the avatar is granted, without consuming the manual
	// avatar socket-opening material.  Do not interpret [avatar type select]
	// as a default, and do not pre-open ordinary avatar parts.
	if socketTypes := currentAvatarCreationSocketTypes(source, definition.PVFPath, rule); len(socketTypes) > 0 {
		var socketData [currentAvatarSocketBytes]byte
		currentSetAvatarSocketTypes(&socketData, socketTypes)
		currentApplyAvatarSocketDataToStack(&stack, 1, slot, socketData)
	}
	entry := currentItemListEntryFromStack(1, slot, stack)
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
	inventory.Slots[currentCeraShopInventorySlotKey(1, slot)] = stack
	return slot, nil
}

func currentCeraShopInventorySlotKey(listType byte, slot int16) string {
	return strconv.Itoa(int(listType)) + ":" + strconv.FormatInt(int64(slot), 10)
}

// isCurrentCeraShopCreatureItem returns true when the item is a pet/creature
// egg that belongs in the Pet list (list 7) rather than the main backpack.
// PVF rule: equipment type contains [creature].
func isCurrentCeraShopCreatureItem(definition dungeonDropItemDefinition) bool {
	if definition.Kind != dungeonDropItemEquipment {
		return false
	}
	return strings.Contains(strings.ToLower(definition.EquipmentType), "creature")
}

// isCurrentCeraShopPetConsumable 是"宠物消耗品（list 7 189..238）家族"判定的旧名字，
// 判据本体在 currentPVFPetConsumableFamily（`[feed]` + `[creature]` 两类堆叠品）。
//
// **现场根因（2026-09-25）**：这里原来只认 `[feed]`，于是商城购买的宠物更名卡
// （item 25，`stackable/cash/creature/rename_card.stk`，家族 `[creature]`）掉进主背包
// 65..120（消耗品页），而邮件领取的同名卡在 list 7 189..238 —— 同一件道具两条路径两个段。
// 保留这个函数名是因为 cera_package.go 等调用方仍在使用；规则只有一份。
func isCurrentCeraShopPetConsumable(definition dungeonDropItemDefinition) bool {
	return definition.Kind == dungeonDropItemStackable &&
		currentPVFPetConsumableFamily(normalizeDungeonDropStackableType(definition.StackableType))
}

// grantCurrentCeraShopPetConsumable places a PVF [feed]/[creature] stack into list 7's
// current-EXE pet-consumable segment.  It deliberately cannot merge with an
// identically numbered row in list 0.
//
// 合并判据 = 运行时 PVF 的堆叠上限 + "只看身份"（dnfrepo.CanMergeItemStack）：
// 先填满 189..238 里已有的同身份行，装不下的部分才另开新格。
//
// **根因修复（2026-09-24 现场）**：旧实现把整段合并循环放在 `if definition.StackLimit > 0`
// 里面，而运行时 `stackable/cash/creature/creature_food.stk`（宠物饲料 item 24）与
// `rename_card.stk`（更名卡 item 25）**根本没有 [stack limit] 段**——PVF 解析结果
// StackLimit == 0 表示"无上限"，于是每次发放都跳过合并、直接在 189..238 里另开一格。
// 现场只读查库证实两行 Extra 逐键相同（都是 source=cera_package +
// cera_package_source_item=490701976 + 同一 pvf_path/stackable_type）：
// `7:189 = item 24 ×600` 与 `7:190 = item 24 ×300` 就是这么来的。
// 点券商城购买（cera_shop_purchase.go）、点券套餐（cera_package.go）与 booster 礼盒
// （booster_projection.go）三条路径都汇入本函数，所以一处修好三条都修好。
func grantCurrentCeraShopPetConsumable(
	inventory *dnfrepo.InventoryRecord,
	definition dungeonDropItemDefinition,
	count uint32,
) ([]int16, error) {
	if inventory == nil || count == 0 || definition.ItemID == 0 || !isCurrentCeraShopPetConsumable(definition) {
		return nil, errCurrentCeraShopProductUnavailable
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}

	remaining := count
	changedSlots := make([]int16, 0, 1)
	grantIdentity := currentPetConsumableGrantIdentity(definition)
	for remaining > 0 {
		// 第一步：并进已有同身份行。StackLimit == 0（无上限）时同样要合并——
		// 这正是上面那条根因。
		for slot := currentCeraShopPetConsumableSlotStart; slot <= currentCeraShopPetConsumableSlotEnd && remaining > 0; slot++ {
			key := currentCeraShopInventorySlotKey(currentPetInventoryListType, slot)
			stack, found := inventory.Slots[key]
			if !found || stack.Count < 0 || stack.ItemID != int64(definition.ItemID) {
				continue
			}
			// 实例到期时间用当前 EXE 的 0x77 行读取口径单独判一次：CanMergeItemStack
			// 只在"两边都写了到期时间"时比较，而"已有行没有到期时间、新发的有"（或反之）
			// 是两件不同的实例，绝不能合。
			if !currentItemStackExpirationMatches(stack, definition.ExpirationDate) {
				continue
			}
			if !dnfrepo.CanMergeItemStack(stack, grantIdentity) {
				continue
			}
			available := int64(math.MaxInt32) - stack.Count
			if definition.StackLimit > 0 {
				if stack.Count >= definition.StackLimit {
					continue
				}
				available = definition.StackLimit - stack.Count
			}
			if available <= 0 {
				continue
			}
			added := int64(remaining)
			if added > available {
				added = available
			}
			stack.Count += added
			if !definition.ExpirationDate.IsZero() {
				stack, _ = applyCurrentPVFItemExpiration(stack, definition)
			}
			entry := currentItemListEntryFromStack(currentPetInventoryListType, slot, stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			inventory.Slots[key] = stack
			changedSlots = append(changedSlots, slot)
			remaining -= uint32(added)
		}
		if remaining == 0 {
			break
		}

		// 第二步：只有"超上限 / 没有同身份行"的剩余部分才另开一格。
		chunk := remaining
		if definition.StackLimit > 0 && int64(chunk) > definition.StackLimit {
			chunk = uint32(definition.StackLimit)
		}
		var slot int16 = -1
		for candidate := currentCeraShopPetConsumableSlotStart; candidate <= currentCeraShopPetConsumableSlotEnd; candidate++ {
			if _, occupied := inventory.Slots[currentCeraShopInventorySlotKey(currentPetInventoryListType, candidate)]; !occupied {
				slot = candidate
				break
			}
		}
		if slot < 0 {
			return nil, fmt.Errorf(
				"%w: pet consumable item=%d range=%d..%d",
				errDungeonPickupInventoryFull,
				definition.ItemID,
				currentCeraShopPetConsumableSlotStart,
				currentCeraShopPetConsumableSlotEnd,
			)
		}

		extra := map[string]string{
			"source":         "cera_shop_pet_consumable",
			"item_kind":      string(definition.Kind),
			"pvf_path":       definition.PVFPath,
			"stackable_type": definition.StackableType,
		}
		if definition.StackLimit > 0 {
			extra["stack_limit"] = strconv.FormatInt(definition.StackLimit, 10)
		}
		stack := dnfrepo.ItemStack{ItemID: int64(definition.ItemID), Count: int64(chunk), Extra: extra}
		if !definition.ExpirationDate.IsZero() {
			stack, _ = applyCurrentPVFItemExpiration(stack, definition)
		}
		entry := currentItemListEntryFromStack(currentPetInventoryListType, slot, stack)
		stack.RawEntry = append([]byte(nil), entry.data[:]...)
		inventory.Slots[currentCeraShopInventorySlotKey(currentPetInventoryListType, slot)] = stack
		changedSlots = append(changedSlots, slot)
		remaining -= chunk
	}
	return changedSlots, nil
}

// currentPVFGrantIdentity 是"这一批 PVF 发放"的身份投影（item_kind / pvf_path /
// stackable_type / 实例到期时间），判据本体仍然是 dnfrepo.CanMergeItemStack。
//
// 点券商城商品、点券套餐、booster 礼盒、NPC 商店、任务奖励、在线活动奖励、
// 副本掉落拾取都汇到同一条落库路径（dnfdungeon.PlacePickupItem /
// currentDungeonPickupPlacement），所以共用这一个投影，不再各写一套。
func currentPVFGrantIdentity(definition dungeonDropItemDefinition) dnfrepo.ItemStack {
	return currentPetConsumableGrantIdentity(definition)
}

// grantCurrentGuildMedalPageItem places a guild-medal / guardian-gem reward into
// its **list 38** page (medals 0..48, guardian gems 49..97).
//
// 现场报障（2026-09-26）：「公会勋章获得时直接进入装备栏、无法被拖拽进入勋章栏-勋章」
// 「公会守护珠获得时直接进入消耗品栏且拖拽装备无反应」。两条都是**发放容器**判定
// 缺家族：勋章（[equipment type] `[flag]`）落到装备页 9..64，守护珠
// （[stackable type] `[flag gem]`）落到默认消耗品页 65..120。wire 容器是 38
// （与 `currentGuildMedalInventoryListType` / 勋章穿戴端点 list 38 → actor 槽 32 同一份证据）。
//
// 合并口径与 `grantCurrentCeraShopPetConsumable` 一致：先填满本页里已有的同身份行
// （dnfrepo.CanMergeItemStack），装不下的部分才另开新格；勋章是装备，StackLimit 强制 1。
func grantCurrentGuildMedalPageItem(
	inventory *dnfrepo.InventoryRecord,
	definition dungeonDropItemDefinition,
	container currentPVFItemContainer,
	count uint32,
) ([]int16, error) {
	if inventory == nil || count == 0 || definition.ItemID == 0 ||
		container.ListType != currentGuildMedalInventoryListType ||
		(!container.GuildMedal && !container.GuardianGem) {
		return nil, errCurrentCeraShopProductUnavailable
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	stackLimit := definition.StackLimit
	if container.GuildMedal {
		stackLimit = 1
	}
	remaining := count
	changedSlots := make([]int16, 0, 1)
	grantIdentity := currentPVFGrantIdentity(definition)
	for remaining > 0 {
		for slot := container.SlotStart; slot <= container.SlotEnd && remaining > 0; slot++ {
			key := currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, slot)
			stack, found := inventory.Slots[key]
			if !found || stack.Count < 0 || stack.ItemID != int64(definition.ItemID) {
				continue
			}
			if !currentItemStackExpirationMatches(stack, definition.ExpirationDate) {
				continue
			}
			if !dnfrepo.CanMergeItemStack(stack, grantIdentity) {
				continue
			}
			available := int64(math.MaxInt32) - stack.Count
			if stackLimit > 0 {
				if stack.Count >= stackLimit {
					continue
				}
				available = stackLimit - stack.Count
			}
			if available <= 0 {
				continue
			}
			added := int64(remaining)
			if added > available {
				added = available
			}
			stack.Count += added
			if !definition.ExpirationDate.IsZero() {
				stack, _ = applyCurrentPVFItemExpiration(stack, definition)
			}
			entry := currentItemListEntryFromStack(currentGuildMedalInventoryListType, slot, stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			inventory.Slots[key] = stack
			changedSlots = append(changedSlots, slot)
			remaining -= uint32(added)
		}
		if remaining == 0 {
			break
		}
		chunk := remaining
		if stackLimit > 0 && int64(chunk) > stackLimit {
			chunk = uint32(stackLimit)
		}
		var slot int16 = -1
		for candidate := container.SlotStart; candidate <= container.SlotEnd; candidate++ {
			if _, occupied := inventory.Slots[currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, candidate)]; !occupied {
				slot = candidate
				break
			}
		}
		if slot < 0 {
			return nil, fmt.Errorf(
				"%w: guild-medal-list item=%d list=%d range=%d..%d",
				errDungeonPickupInventoryFull,
				definition.ItemID,
				currentGuildMedalInventoryListType,
				container.SlotStart,
				container.SlotEnd,
			)
		}
		extra := map[string]string{
			"source":    "guild_medal_inventory_grant",
			"item_kind": string(definition.Kind),
			"pvf_path":  definition.PVFPath,
		}
		if definition.StackableType != "" {
			extra["stackable_type"] = definition.StackableType
		}
		if definition.EquipmentType != "" {
			extra["equipment_type"] = definition.EquipmentType
		}
		if stackLimit > 0 {
			extra["stack_limit"] = strconv.FormatInt(stackLimit, 10)
		}
		stack := dnfrepo.ItemStack{ItemID: int64(definition.ItemID), Count: int64(chunk), Extra: extra}
		if !definition.ExpirationDate.IsZero() {
			stack, _ = applyCurrentPVFItemExpiration(stack, definition)
		}
		entry := currentItemListEntryFromStack(currentGuildMedalInventoryListType, slot, stack)
		stack.RawEntry = append([]byte(nil), entry.data[:]...)
		inventory.Slots[currentCeraShopInventorySlotKey(currentGuildMedalInventoryListType, slot)] = stack
		changedSlots = append(changedSlots, slot)
		remaining -= chunk
	}
	return changedSlots, nil
}

// currentPetConsumableGrantIdentity 是"这一批发放"的身份投影：只有会改变道具语义的键
// （item_kind / pvf_path / stackable_type / expire_time），不带 source / stack_limit
// 这类记账与派生键，交给 dnfrepo.CanMergeItemStack 做"缺省即兼容"的比较。
func currentPetConsumableGrantIdentity(definition dungeonDropItemDefinition) dnfrepo.ItemStack {
	extra := make(map[string]string, 4)
	if kind := strings.TrimSpace(string(definition.Kind)); kind != "" {
		extra["item_kind"] = kind
	}
	if pvfPath := strings.TrimSpace(definition.PVFPath); pvfPath != "" {
		extra["pvf_path"] = pvfPath
	}
	if stackableType := strings.TrimSpace(definition.StackableType); stackableType != "" {
		extra["stackable_type"] = stackableType
	}
	if !definition.ExpirationDate.IsZero() {
		if expire := currentPVFExpirationUnix(definition.ExpirationDate); expire != 0 {
			value := strconv.FormatUint(uint64(expire), 10)
			extra["expire_time"] = value
			extra["expire_unix"] = value
			if definition.DailyDeleteItem {
				extra["daily_delete_at"] = value
			}
		}
	}
	return dnfrepo.ItemStack{
		ItemID:   int64(definition.ItemID),
		ExpireAt: definition.ExpirationDate,
		Extra:    extra,
	}
}

// grantCurrentCeraShopPet places a creature/pet egg into the Pet list (list 7)
// at the first available slot in the pet-body range (0-139).
func grantCurrentCeraShopPet(inventory *dnfrepo.InventoryRecord, definition dungeonDropItemDefinition) (int16, error) {
	if inventory == nil || definition.ItemID == 0 {
		return 0, errCurrentCeraShopProductUnavailable
	}
	if inventory.Slots == nil {
		inventory.Slots = make(map[string]dnfrepo.ItemStack)
	}
	const petBodySlotStart int16 = 0
	const petBodySlotEnd int16 = 139
	for slot := petBodySlotStart; slot <= petBodySlotEnd; slot++ {
		key := fmt.Sprintf("%d:%d", currentPetInventoryListType, slot)
		if _, occupied := inventory.Slots[key]; occupied {
			continue
		}
		stack := dnfrepo.ItemStack{
			ItemID: int64(definition.ItemID),
			Count:  1,
			Extra: map[string]string{
				"source":         "cera_shop_creature",
				"item_kind":      string(dungeonDropItemEquipment),
				"pvf_path":       definition.PVFPath,
				"equipment_type": definition.EquipmentType,
			},
		}
		if !definition.ExpirationDate.IsZero() {
			stack.ExpireAt = definition.ExpirationDate
			stack.Extra["expire_time"] = strconv.FormatInt(definition.ExpirationDate.Unix(), 10)
		}
		inventory.Slots[key] = stack
		return slot, nil
	}
	return 0, fmt.Errorf("%w: pet body slots 0-139 full", errCurrentCeraShopProductUnavailable)
}
