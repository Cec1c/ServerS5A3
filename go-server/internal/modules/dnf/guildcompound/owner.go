package guildcompound

import (
	"context"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件实现公会守护珠/勋章合成的资产事务。成功率和配方只由 catalog 决定；
// 物品定义、候选池、原始行编码、发放落位和期限规则由 bridge 通过 Rules 注入。
// 域层不引用 bridge。

var (
	// ErrInvalid 表示请求、材料快照、配方、候选或随机值无法形成一次合法合成。
	ErrInvalid = errors.New("guild compound request is invalid")
	// ErrNoSpace 表示产物在固定 list38 分页中无法落位。
	ErrNoSpace = errors.New("guild compound result has no free list38 slot")

	errOwnerUnavailable = errors.New("guild compound owner is unavailable")
)

// MaterialDefinition 是合成材料在 PVF 中的家族和品级。
type MaterialDefinition struct {
	Family MaterialFamily
	Rarity uint32
}

// Rules 是 owner 需要的只读规则与桥接能力。候选和 Grant 由 dnfbridge 提供。
type Rules struct {
	Catalog     CompoundCatalog
	Resolve     func(uint32) (MaterialDefinition, error)
	Candidates  func(CompoundKind, uint32) ([]uint32, error)
	UpdateStack func(repo.ItemStack, int16) (repo.ItemStack, error)
	Grant       func(*repo.InventoryRecord, uint32) (int16, error)
	Expired     func(repo.ItemStack, time.Time) bool
	Roll        func(uint32) uint32
}

// Command 是一次公会合成请求。Request.Grade 与每件材料的真实品级必须完全一致。
type Command struct {
	AccountID   string
	CharacterID string
	Kind        CompoundKind
	Request     Request
	Rules       Rules
	Now         time.Time
}

// Result 是已提交的合成结果。Changed 是变更后的槽快照；Count 为 0 的项表示
// 该槽已被消耗并删除。失败合成也返回已扣除材料的 Changed。
type Result struct {
	Success     bool
	Roll        uint32
	SuccessRate uint32
	ResultItem  uint32
	NextRarity  uint32
	ResultSlot  int16
	Changed     map[int16]repo.ItemStack
}

// Owner 只提交角色背包的 Slots 字段。
type Owner struct {
	characterAssets repo.CharacterAssetUnitOfWork
	accountAssets   repo.AccountCharacterAssetUnitOfWork
}

// NewOwner 优先使用角色资产事务；仅在缺失时回退到账号+角色资产事务。
func NewOwner(group repo.Group) (*Owner, error) {
	if group.CharacterAssets != nil {
		return &Owner{characterAssets: group.CharacterAssets}, nil
	}
	if group.AccountAssets != nil {
		return &Owner{accountAssets: group.AccountAssets}, nil
	}
	return nil, errOwnerUnavailable
}

// Apply 在一笔事务内校验角色归属和全部材料，扣除每一条引用，掷骰并保存产物。
// 掷骰失败仍提交扣料；Grant、UpdateStack 或 Save 失败时返回空 Result 并回滚。
func (o *Owner) Apply(ctx context.Context, cmd Command) (Result, error) {
	if o == nil || (o.characterAssets == nil && o.accountAssets == nil) {
		return Result{}, errOwnerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	accountID := strings.TrimSpace(cmd.AccountID)
	characterID := strings.TrimSpace(cmd.CharacterID)
	if accountID == "" || characterID == "" {
		return Result{}, fmt.Errorf("%w: account and character are required", ErrInvalid)
	}
	family, ok := compoundFamily(cmd.Kind)
	if !ok {
		return Result{}, fmt.Errorf("%w: kind=%d", ErrInvalid, cmd.Kind)
	}
	if err := validateRules(cmd.Rules); err != nil {
		return Result{}, err
	}

	ladder := cmd.Rules.Catalog.Ladder(cmd.Kind)
	recipe, found := ladder.Recipe(cmd.Request.Grade)
	if !found || !recipe.Compoundable() {
		return Result{}, fmt.Errorf("%w: kind=%s grade=%d has no compoundable recipe",
			ErrInvalid, cmd.Kind, cmd.Request.Grade)
	}
	count := cmd.Request.Count
	if !recipe.AcceptsMaterialCount(count) {
		return Result{}, fmt.Errorf("%w: kind=%s grade=%d count=%d outside 2..%d",
			ErrInvalid, cmd.Kind, cmd.Request.Grade, count, recipe.MaxMaterialCount)
	}
	nextRarity, hasNext := ladder.NextRarity(cmd.Request.Grade)
	if !hasNext {
		return Result{}, fmt.Errorf("%w: kind=%s grade=%d has no next grade",
			ErrInvalid, cmd.Kind, cmd.Request.Grade)
	}
	successRate := recipe.SuccessRateFor(count)
	if successRate == 0 {
		return Result{}, fmt.Errorf("%w: kind=%s grade=%d has zero success rate",
			ErrInvalid, cmd.Kind, cmd.Request.Grade)
	}

	candidates, err := cmd.Rules.Candidates(cmd.Kind, nextRarity)
	if err != nil {
		return Result{}, fmt.Errorf("%w: candidates kind=%s grade=%d: %v",
			ErrInvalid, cmd.Kind, nextRarity, err)
	}
	pool, err := validateCandidatePool(cmd.Kind, family, nextRarity, candidates, cmd.Rules.Resolve)
	if err != nil {
		return Result{}, err
	}
	poolSet := make(map[uint32]struct{}, len(pool))
	for _, itemID := range pool {
		poolSet[itemID] = struct{}{}
	}

	now := cmd.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var result Result
	apply := func(
		characters repo.CharacterRepository,
		inventories repo.InventoryRepository,
	) error {
		if characters == nil || inventories == nil {
			return errOwnerUnavailable
		}
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return fmt.Errorf("load guild compound character: %w", err)
		}
		if !found || strings.TrimSpace(character.CharacterID) != characterID ||
			strings.TrimSpace(character.AccountID) != accountID {
			return fmt.Errorf("%w: character %s does not belong to account %s",
				ErrInvalid, characterID, accountID)
		}

		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return fmt.Errorf("load guild compound inventory: %w", err)
		}
		if !found || strings.TrimSpace(inventory.CharacterID) != characterID {
			return fmt.Errorf("%w: inventory for character %s is missing", ErrInvalid, characterID)
		}
		inventory = repo.CloneInventory(inventory)
		if inventory.Slots == nil {
			inventory.Slots = make(map[string]repo.ItemStack)
		}

		pending, err := applyInventory(ctx, cmd, now, family, nextRarity, successRate, pool, poolSet, &inventory)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		inventory.UpdatedAt = now
		if err := repo.SaveInventoryFields(ctx, inventories, inventory, repo.InventoryFieldSlots); err != nil {
			return fmt.Errorf("save guild compound inventory: %w", err)
		}
		result = pending
		return nil
	}

	if o.characterAssets != nil {
		err = o.characterAssets.WithinCharacterAssets(ctx, characterID,
			func(characters repo.CharacterRepository, inventories repo.InventoryRepository, _ repo.EquipmentRepository) error {
				return apply(characters, inventories)
			})
	} else {
		err = o.accountAssets.WithinAccountCharacterAssets(ctx, accountID, characterID,
			func(_ repo.AccountInventoryRepository, characters repo.CharacterRepository, inventories repo.InventoryRepository, _ repo.EquipmentRepository) error {
				return apply(characters, inventories)
			})
	}
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func applyInventory(
	ctx context.Context,
	cmd Command,
	now time.Time,
	family MaterialFamily,
	nextRarity uint32,
	successRate uint32,
	pool []uint32,
	poolSet map[uint32]struct{},
	inventory *repo.InventoryRecord,
) (Result, error) {
	materials, need, err := validateMaterials(cmd, now, family, inventory)
	if err != nil {
		return Result{}, err
	}
	specialItem, hasSpecial, err := gemFamilySuccessor(cmd.Kind, materials, family, nextRarity, poolSet, cmd.Rules.Resolve)
	if err != nil {
		return Result{}, err
	}

	// 到这里所有材料、品级、分页、数量、锁定、期限和特殊产物都已验证；下面才改克隆。
	changed := make(map[int16]repo.ItemStack, len(need)+1)
	slots := make([]int16, 0, len(need))
	for slot := range need {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	for _, slot := range slots {
		key := list38SlotKey(slot)
		stack := inventory.Slots[key]
		stack.Count -= int64(need[slot])
		if stack.Count <= 0 {
			delete(inventory.Slots, key)
			changed[slot] = repo.ItemStack{}
			continue
		}
		updated, err := cmd.Rules.UpdateStack(stack, slot)
		if err != nil {
			return Result{}, fmt.Errorf("%w: update material slot %d: %v", ErrInvalid, slot, err)
		}
		if updated.ItemID != stack.ItemID || updated.Count != stack.Count ||
			updated.Bind != stack.Bind || !updated.ExpireAt.Equal(stack.ExpireAt) {
			return Result{}, fmt.Errorf("%w: update material slot %d changed the stack identity", ErrInvalid, slot)
		}
		inventory.Slots[key] = updated
		changed[slot] = updated
	}

	pending := Result{
		NextRarity:  nextRarity,
		SuccessRate: successRate,
		ResultSlot:  -1,
		Changed:     changed,
	}
	roll, err := rollBelow(cmd.Rules.Roll, CompoundRateDenominator)
	if err != nil {
		return Result{}, err
	}
	pending.Roll = roll
	pending.Success = roll < successRate
	if !pending.Success {
		// 用户确认的旧规则：失败也扣料，扣料已写入 changed 并随事务提交。
		return pending, nil
	}

	resultItem := specialItem
	if !hasSpecial {
		if len(pool) == 0 || uint64(len(pool)) > uint64(math.MaxUint32) {
			return Result{}, fmt.Errorf("%w: kind=%s has no result candidate", ErrInvalid, cmd.Kind)
		}
		index, err := rollBelow(cmd.Rules.Roll, uint32(len(pool)))
		if err != nil {
			return Result{}, err
		}
		resultItem = pool[index]
	}

	definition, err := cmd.Rules.Resolve(resultItem)
	if err != nil {
		return Result{}, fmt.Errorf("%w: result item %d: %v", ErrInvalid, resultItem, err)
	}
	if definition.Family != family || definition.Rarity != nextRarity {
		return Result{}, fmt.Errorf("%w: result item %d family=%d rarity=%d want family=%d rarity=%d",
			ErrInvalid, resultItem, definition.Family, definition.Rarity, family, nextRarity)
	}

	first, last, _ := family.page()
	beforeResultCounts := make(map[int16]int64, int(last-first)+1)
	for slot := first; slot <= last; slot++ {
		if stack, found := inventory.Slots[list38SlotKey(slot)]; found {
			beforeResultCounts[slot] = stack.Count
		}
	}
	resultSlot, err := cmd.Rules.Grant(inventory, resultItem)
	if err != nil {
		if errors.Is(err, ErrNoSpace) {
			return Result{}, ErrNoSpace
		}
		return Result{}, err
	}
	if resultSlot < first || resultSlot > last {
		return Result{}, fmt.Errorf("%w: result item %d landed outside list38 page %d..%d at slot %d",
			ErrInvalid, resultItem, first, last, resultSlot)
	}
	resultStack, found := inventory.Slots[list38SlotKey(resultSlot)]
	if !found || resultStack.ItemID != int64(resultItem) ||
		resultStack.Count != beforeResultCounts[resultSlot]+1 {
		return Result{}, fmt.Errorf("%w: grant result item %d did not land at slot %d",
			ErrInvalid, resultItem, resultSlot)
	}
	if family == MaterialFamilyGuildMedal && resultStack.Count != 1 {
		return Result{}, fmt.Errorf("%w: medal result item %d landed with count %d, want 1",
			ErrInvalid, resultItem, resultStack.Count)
	}
	pending.ResultItem = resultItem
	pending.ResultSlot = resultSlot
	pending.Changed[resultSlot] = resultStack
	return pending, nil
}

// validateMaterials 在任何扣料之前完成整组校验，并返回每条引用的 ItemID 与聚合数量。
func validateMaterials(
	cmd Command,
	now time.Time,
	family MaterialFamily,
	inventory *repo.InventoryRecord,
) ([]uint32, map[int16]uint32, error) {
	count := cmd.Request.Count
	if count == 0 || count > MaxMaterialCount {
		return nil, nil, fmt.Errorf("%w: material count=%d", ErrInvalid, count)
	}

	// 快照按固定 list38 坐标构造；只读取请求引用到的槽，不把别名或别页当材料。
	snapshot := make([]List38Slot, 0, count)
	seenSnapshot := make(map[int16]struct{}, count)
	for index := uint32(0); index < count; index++ {
		slot, ok := materialSlot(cmd.Request.MaterialRefs[index], family)
		if !ok {
			return nil, nil, fmt.Errorf("%w: material ref[%d]=%d outside list38 page",
				ErrInvalid, index, cmd.Request.MaterialRefs[index])
		}
		if _, seen := seenSnapshot[slot]; seen {
			continue
		}
		stack, found := inventory.Slots[list38SlotKey(slot)]
		if !found {
			return nil, nil, fmt.Errorf("%w: material slot %d is missing", ErrInvalid, slot)
		}
		itemID, ok := uint32ItemID(stack.ItemID)
		if !ok || itemID == 0 {
			return nil, nil, fmt.Errorf("%w: material slot %d has invalid item id %d",
				ErrInvalid, slot, stack.ItemID)
		}
		quantity, ok := uint32Quantity(stack.Count)
		if !ok || quantity == 0 {
			return nil, nil, fmt.Errorf("%w: material slot %d has invalid quantity %d",
				ErrInvalid, slot, stack.Count)
		}
		seenSnapshot[slot] = struct{}{}
		snapshot = append(snapshot, List38Slot{Slot: slot, ItemID: itemID, Quantity: quantity})
	}

	resolution, err := ResolveMaterialRefs(family, count, cmd.Request.MaterialRefs, snapshot)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	need := make(map[int16]uint32, len(resolution.Materials))
	for _, material := range resolution.Materials {
		// ResolveMaterialRefs 只在 835 上校验可用量；864 也必须在这里逐槽聚合。
		if material.Requested == 0 || material.Available < material.Requested {
			return nil, nil, fmt.Errorf("%w: material slot %d need=%d available=%d",
				ErrInvalid, material.Slot, material.Requested, material.Available)
		}
		stack := inventory.Slots[list38SlotKey(material.Slot)]
		if family == MaterialFamilyGuildMedal && (stack.Count != 1 || material.Requested != 1) {
			return nil, nil, fmt.Errorf("%w: medal slot %d is not one distinct equipment instance", ErrInvalid, material.Slot)
		}
		if stack.ItemID != int64(material.ItemID) {
			return nil, nil, fmt.Errorf("%w: material slot %d item changed during validation",
				ErrInvalid, material.Slot)
		}
		if materialExpired(stack, now, cmd.Rules.Expired) {
			return nil, nil, fmt.Errorf("%w: material slot %d is expired", ErrInvalid, material.Slot)
		}
		if materialLocked(stack, now) {
			return nil, nil, fmt.Errorf("%w: material slot %d is locked", ErrInvalid, material.Slot)
		}
		definition, err := cmd.Rules.Resolve(material.ItemID)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: material slot %d item %d: %v",
				ErrInvalid, material.Slot, material.ItemID, err)
		}
		if definition.Family != family || definition.Rarity != cmd.Request.Grade {
			return nil, nil, fmt.Errorf("%w: material slot %d item %d family=%d rarity=%d want family=%d rarity=%d",
				ErrInvalid, material.Slot, material.ItemID,
				definition.Family, definition.Rarity, family, cmd.Request.Grade)
		}
		need[material.Slot] = material.Requested
	}

	materials := make([]uint32, 0, count)
	for index := uint32(0); index < count; index++ {
		slot, _ := materialSlot(cmd.Request.MaterialRefs[index], family)
		stack := inventory.Slots[list38SlotKey(slot)]
		itemID, ok := uint32ItemID(stack.ItemID)
		if !ok {
			return nil, nil, fmt.Errorf("%w: material slot %d lost its item id", ErrInvalid, slot)
		}
		materials = append(materials, itemID)
	}
	return materials, need, nil
}

func gemFamilySuccessor(
	kind CompoundKind,
	materials []uint32,
	family MaterialFamily,
	nextRarity uint32,
	poolSet map[uint32]struct{},
	resolve func(uint32) (MaterialDefinition, error),
) (uint32, bool, error) {
	if kind != CompoundGem || len(materials) != 4 {
		return 0, false, nil
	}
	first := materials[0]
	for _, material := range materials[1:] {
		if material != first {
			return 0, false, nil
		}
	}
	if first == math.MaxUint32 {
		return 0, false, fmt.Errorf("%w: 4 same-id gem %d has no successor", ErrInvalid, first)
	}
	successor := first + 1
	if _, found := poolSet[successor]; !found {
		return 0, false, fmt.Errorf("%w: 4 same-id gem %d successor %d is absent from the next-grade pool",
			ErrInvalid, first, successor)
	}
	definition, err := resolve(successor)
	if err != nil {
		return 0, false, fmt.Errorf("%w: 4 same-id gem successor %d: %v", ErrInvalid, successor, err)
	}
	if definition.Family != family || definition.Rarity != nextRarity {
		return 0, false, fmt.Errorf("%w: 4 same-id gem successor %d family=%d rarity=%d want family=%d rarity=%d",
			ErrInvalid, successor, definition.Family, definition.Rarity, family, nextRarity)
	}
	return successor, true, nil
}

func validateCandidatePool(
	kind CompoundKind,
	family MaterialFamily,
	nextRarity uint32,
	candidates []uint32,
	resolve func(uint32) (MaterialDefinition, error),
) ([]uint32, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: kind=%s has no next-grade candidates", ErrInvalid, kind)
	}
	seen := make(map[uint32]struct{}, len(candidates))
	for _, itemID := range candidates {
		if itemID == 0 {
			return nil, fmt.Errorf("%w: kind=%s candidate id 0", ErrInvalid, kind)
		}
		if _, duplicate := seen[itemID]; duplicate {
			return nil, fmt.Errorf("%w: kind=%s candidate %d is duplicated", ErrInvalid, kind, itemID)
		}
		seen[itemID] = struct{}{}
		definition, err := resolve(itemID)
		if err != nil {
			return nil, fmt.Errorf("%w: kind=%s candidate %d: %v", ErrInvalid, kind, itemID, err)
		}
		if definition.Family != family || definition.Rarity != nextRarity {
			return nil, fmt.Errorf("%w: kind=%s candidate %d family=%d rarity=%d want family=%d rarity=%d",
				ErrInvalid, kind, itemID, definition.Family, definition.Rarity, family, nextRarity)
		}
	}
	return candidates, nil
}

func validateRules(rules Rules) error {
	if rules.Resolve == nil || rules.Candidates == nil || rules.UpdateStack == nil || rules.Grant == nil {
		return fmt.Errorf("%w: rules callbacks are incomplete", ErrInvalid)
	}
	return nil
}

func compoundFamily(kind CompoundKind) (MaterialFamily, bool) {
	switch kind {
	case CompoundGem:
		return MaterialFamilyGuardianGem, true
	case CompoundFlag:
		return MaterialFamilyGuildMedal, true
	default:
		return MaterialFamilyUnspecified, false
	}
}

func materialSlot(ref uint32, family MaterialFamily) (int16, bool) {
	if ref == AbsentMaterialRef || ref >= List38SlotCount {
		return 0, false
	}
	slot := int16(ref)
	first, last, ok := family.page()
	if !ok || slot < first || slot > last || repo.IsAccountSharedInventorySlot(38, slot) {
		return 0, false
	}
	return slot, true
}

func list38SlotKey(slot int16) string {
	return "38:" + strconv.FormatInt(int64(slot), 10)
}

func uint32ItemID(value int64) (uint32, bool) {
	if value <= 0 || value > int64(math.MaxUint32) {
		return 0, false
	}
	return uint32(value), true
}

func uint32Quantity(value int64) (uint32, bool) {
	if value <= 0 || value > int64(math.MaxUint32) {
		return 0, false
	}
	return uint32(value), true
}

func materialExpired(stack repo.ItemStack, now time.Time, expired func(repo.ItemStack, time.Time) bool) bool {
	if !stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt) {
		return true
	}
	return expired != nil && expired(stack, now)
}

func materialLocked(stack repo.ItemStack, now time.Time) bool {
	if stack.Extra == nil {
		return false
	}
	if strings.TrimSpace(stack.Extra[repo.SortLockExtraKey]) == "1" {
		return true
	}
	return itemlock.IsLockedAt(stack, now)
}

func rollBelow(roll func(uint32) uint32, upper uint32) (uint32, error) {
	if upper == 0 {
		return 0, fmt.Errorf("%w: roll upper bound is zero", ErrInvalid)
	}
	value := uint32(mathrand.IntN(int(upper)))
	if roll != nil {
		value = roll(upper)
	}
	if value >= upper {
		return 0, fmt.Errorf("%w: roll %d is outside [0,%d)", ErrInvalid, value, upper)
	}
	return value, nil
}
