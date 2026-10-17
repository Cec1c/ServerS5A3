package equipmentcompound

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/repository"
)

type Request struct {
	SourceSlot int16
	Mode, Part uint16
}
type Command struct {
	AccountID, CharacterID string
	Request                Request
	UpdatedAt              time.Time
	Catalog                *Catalog
	EquipmentPolicy        itemplacement.SlotPolicy
	MaterialPolicies       map[int64]itemplacement.SlotPolicy
	// 当前90的实际封装状态由桥接解读持久化行，在事务内对刚加载的来源核对。
	SourceSealed func(repository.ItemStack) bool
	// 随机及当前版本行编码只由服务器注入，客户端不能提供。
	Random      func(int64) int64
	BuildStack  func(int16, Definition, repository.ItemStack) (repository.ItemStack, error)
	EncodeStack func(int16, repository.ItemStack) repository.ItemStack
}
type Result struct {
	SourceSlot, ResultSlot                        int16
	Source                                        Definition
	Output                                        Definition
	SelectedWeight, LevelWeight, TotalLevelWeight int64
	ChangedSlots                                  []int16
	Inventory                                     repository.InventoryRecord
	AccountInventory                              repository.AccountInventoryRecord
}
type Owner struct {
	assets   repository.AccountCharacterAssetUnitOfWork
	settings repository.SettingsRepository
}

func NewOwner(group repository.Group) (*Owner, error) {
	if group.AccountAssets == nil {
		return nil, ErrOwner
	}
	return &Owner{group.AccountAssets, group.Settings}, nil
}

// Compound 的材料、来源删除和新装备保存共享账号+角色资产事务。
// 角色统计、钱包、穿戴栏及来源的强化/附魔等实例数据不会被移到新模板。
func (o *Owner) Compound(ctx context.Context, cmd Command) (Result, error) {
	if o == nil || o.assets == nil || cmd.Catalog == nil || cmd.BuildStack == nil || strings.TrimSpace(cmd.AccountID) == "" || strings.TrimSpace(cmd.CharacterID) == "" {
		return Result{}, ErrOwner
	}
	if cmd.Request.Mode != 0 || cmd.Request.Part < 1 || cmd.Request.Part > 4 || cmd.Request.SourceSlot < 3 {
		return Result{}, ErrSource
	}
	if cmd.EquipmentPolicy.ListType != 0 || cmd.EquipmentPolicy.Start != 9 || cmd.EquipmentPolicy.End != 64 {
		return Result{}, ErrRules
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := cmd.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	accountID, characterID := strings.TrimSpace(cmd.AccountID), strings.TrimSpace(cmd.CharacterID)
	var result Result
	err := o.assets.WithinAccountCharacterAssets(ctx, accountID, characterID, func(accounts repository.AccountInventoryRepository, characters repository.CharacterRepository, inventories repository.InventoryRepository, _ repository.EquipmentRepository) error {
		if accounts == nil || characters == nil || inventories == nil {
			return ErrOwner
		}
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != accountID {
			return ErrOwner
		}
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || inventory.CharacterID != characterID || inventory.Slots == nil {
			return ErrOwner
		}
		account, _, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if err = repository.BindInventoryContainerState(ctx, o.settings, &inventory); err != nil {
			return err
		}
		inventory, account = repository.CloneInventory(inventory), repository.CloneAccountInventory(account)
		account.AccountID = accountID
		if account.Slots == nil {
			account.Slots = map[string]repository.ItemStack{}
		}
		policy := cmd.EquipmentPolicy.ForInventory(&inventory)
		key := slotKey(cmd.Request.SourceSlot)
		source, exists := inventory.Slots[key]
		if !exists || source.Count != 1 || !policy.Contains(0, cmd.Request.SourceSlot) || !usable(source, now) || cmd.SourceSealed == nil || !cmd.SourceSealed(source) {
			return ErrSource
		}
		definition, err := cmd.Catalog.Resolve(source.ItemID)
		if err != nil {
			return err
		}
		if kind := source.Extra["item_kind"]; kind != "" && kind != "equipment" {
			return ErrSource
		}
		if stored := source.Extra["pvf_path"]; stored != "" && !strings.EqualFold(strings.ReplaceAll(stored, "\\", "/"), definition.PVFPath) {
			return ErrSource
		}
		pools, err := cmd.Catalog.Pools(definition, cmd.Request.Part)
		if err != nil {
			return err
		}
		materials, err := cmd.Catalog.Rules.Requirements(definition.Rarity, definition.MinimumLevel)
		if err != nil {
			return err
		}
		changed := map[int16]bool{cmd.Request.SourceSlot: true}
		accountDirty, err := consumeMaterials(&inventory, &account, materials, cmd.MaterialPolicies, now, changed)
		if err != nil {
			return err
		}
		delete(inventory.Slots, key)
		resultSlot := int16(-1)
		for _, slot := range policy.EmptySlots() {
			if _, occupied := inventory.Slots[slotKey(slot)]; !occupied {
				resultSlot = slot
				break
			}
		}
		if resultSlot < 0 {
			return ErrCapacity
		}
		// 上面仅预演克隆资产，材料/空间/两级池全通过后才抽等级和物品。
		poolWeights := make([]int64, len(pools))
		for i, pool := range pools {
			poolWeights[i] = pool.Weight
		}
		poolIndex, totalLevelWeight, err := pick(poolWeights, cmd.Random)
		if err != nil {
			return err
		}
		pool := pools[poolIndex]
		weights := make([]int64, len(pool.Candidates))
		for i, candidate := range pool.Candidates {
			weights[i] = candidate.Weight
		}
		candidateIndex, _, err := pick(weights, cmd.Random)
		if err != nil {
			return err
		}
		candidate := pool.Candidates[candidateIndex]
		stack, err := cmd.BuildStack(resultSlot, candidate.Definition, source)
		if err != nil {
			return err
		}
		if stack.ItemID != candidate.Definition.ItemID || stack.Count != 1 || stack.Bind != source.Bind || !stack.ExpireAt.IsZero() {
			return ErrSource
		}
		inventory.Slots[slotKey(resultSlot)] = stack
		changed[resultSlot] = true
		slots := make([]int16, 0, len(changed))
		for slot := range changed {
			slots = append(slots, slot)
		}
		sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
		if cmd.EncodeStack != nil {
			for _, slot := range slots {
				items := inventory.Slots
				if repository.IsAccountSharedInventorySlot(0, slot) {
					items = account.Slots
				}
				if stack, exists := items[slotKey(slot)]; exists {
					items[slotKey(slot)] = cmd.EncodeStack(slot, stack)
				}
			}
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		inventory.UpdatedAt = now
		if err = repository.SaveInventoryFields(ctx, inventories, inventory, repository.InventoryFieldSlots); err != nil {
			return err
		}
		if accountDirty {
			account.UpdatedAt = now
			if err = accounts.Save(ctx, account); err != nil {
				return err
			}
		}
		result = Result{cmd.Request.SourceSlot, resultSlot, definition, candidate.Definition, candidate.Weight, pool.Weight, totalLevelWeight, slots, inventory, account}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func consumeMaterials(inventory *repository.InventoryRecord, account *repository.AccountInventoryRecord, requirements []Material, policies map[int64]itemplacement.SlotPolicy, now time.Time, changed map[int16]bool) (bool, error) {
	accountDirty := false
	for _, material := range requirements {
		if material.ItemID <= 0 || material.Count <= 0 || material.Count > math.MaxUint32 {
			return false, ErrRules
		}
		if slot, shared := repository.AccountWarehouseSlotForItem(material.ItemID); shared {
			// 只认可账号固定灵魂/晶块格；历史角色副本不能拿来代付。
			key := slotKey(slot)
			stack, exists := account.Slots[key]
			if !exists || stack.ItemID != material.ItemID || stack.Count < material.Count || stack.Count > math.MaxUint32 || !usable(stack, now) {
				return false, ErrMaterials
			}
			stack.Count -= material.Count
			if stack.Count == 0 {
				delete(account.Slots, key)
			} else {
				account.Slots[key] = stack
			}
			changed[slot], accountDirty = true, true
			continue
		}
		policy, valid := policies[material.ItemID]
		if !valid || policy.ListType != 0 {
			return false, ErrRules
		}
		remaining := material.Count
		for _, slot := range policy.ForInventory(inventory).MergeSlots() {
			key := slotKey(slot)
			stack, exists := inventory.Slots[key]
			if !exists || stack.ItemID != material.ItemID || stack.Count <= 0 || stack.Count > math.MaxUint32 || !usable(stack, now) {
				continue
			}
			if kind := stack.Extra["item_kind"]; kind != "" && kind != "stackable" {
				continue
			}
			amount := remaining
			if stack.Count < amount {
				amount = stack.Count
			}
			stack.Count -= amount
			if stack.Count == 0 {
				delete(inventory.Slots, key)
			} else {
				inventory.Slots[key] = stack
			}
			changed[slot] = true
			remaining -= amount
			if remaining == 0 {
				break
			}
		}
		if remaining != 0 {
			return false, ErrMaterials
		}
	}
	return accountDirty, nil
}

func pick(weights []int64, random func(int64) int64) (int, int64, error) {
	total := int64(0)
	for _, weight := range weights {
		if weight <= 0 || weight > math.MaxInt64-total {
			return 0, 0, ErrRules
		}
		total += weight
	}
	if total == 0 {
		return 0, 0, ErrCandidates
	}
	roll := int64(0)
	if random == nil {
		roll = rand.Int63n(total)
	} else {
		roll = random(total)
	}
	if roll < 0 || roll >= total {
		return 0, 0, fmt.Errorf("%w：随机抽样越界", ErrRules)
	}
	for i, weight := range weights {
		if roll < weight {
			return i, total, nil
		}
		roll -= weight
	}
	return 0, 0, ErrCandidates
}

func slotKey(slot int16) string { return "0:" + strconv.Itoa(int(slot)) }
func usable(stack repository.ItemStack, now time.Time) bool {
	if !stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(stack.Extra["equipment_lock_state"])) {
	case "1", "2", "active", "locked", "unlocking", "pending_unlock":
		return false
	}
	for _, field := range []string{"expire_time", "expire_unix"} {
		if raw := strings.TrimSpace(stack.Extra[field]); raw != "" {
			deadline, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || deadline < 0 || (deadline > 0 && now.Unix() >= deadline) {
				return false
			}
		}
	}
	return true
}
