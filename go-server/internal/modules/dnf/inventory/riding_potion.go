package inventory

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrRidingPotion = errors.New("变身药水的道具、场景、等级、冷却或材料条件不满足")

// RidingPotionDefinition 只接受运行时PVF解析结果；客户端只提交槽位和动作99。
type RidingPotionDefinition struct {
	ItemID, FormID, DurationMS, MinimumLevel, MaximumLevel, CoolTimeMS int64
	PVFPath                                                            string
	DungeonIDs                                                         []int64
	Materials                                                          map[int64]int64
	DisallowHardcore                                                   bool
}

type RidingPotionRequest struct {
	CharacterID, AccountID string
	SlotIndex              int16
	DungeonID              int64
	Definition             RidingPotionDefinition
	Revalidate             func() bool
}

type RidingPotionResult struct {
	ItemID, Remaining int64
	ChangedSlots      []int16
}

// UseRidingPotion 将扣药、材料和PVF冷却放在同一角色资产事务内。
// op515没有使用nonce，每次接受是一笔使用；失败及不确定提交均不自动重试。
func (o *Owner) UseRidingPotion(ctx context.Context, req RidingPotionRequest, now time.Time) (RidingPotionResult, error) {
	d := req.Definition
	if o == nil || o.repo == nil || o.characters == nil || req.Revalidate == nil || req.CharacterID == "" || req.AccountID == "" || req.SlotIndex < 0 || req.DungeonID <= 0 || now.IsZero() ||
		d.ItemID <= 0 || d.ItemID > math.MaxUint32 || d.FormID <= 0 || d.FormID > math.MaxUint32 || d.DurationMS <= 0 || d.DurationMS > math.MaxUint32 || d.CoolTimeMS < 0 || d.CoolTimeMS > math.MaxUint32 ||
		d.MinimumLevel < 0 || d.MaximumLevel < 0 || (d.MaximumLevel != 0 && d.MaximumLevel < d.MinimumLevel) || strings.TrimSpace(d.PVFPath) == "" {
		return RidingPotionResult{}, ErrRidingPotion
	}
	allowed := false
	for _, id := range d.DungeonIDs {
		if id == req.DungeonID {
			allowed = true
		}
	}
	if !allowed {
		return RidingPotionResult{}, ErrRidingPotion
	}
	for id, count := range d.Materials {
		if id <= 0 || id > math.MaxUint32 || id == d.ItemID || count <= 0 || count > math.MaxInt32 {
			return RidingPotionResult{}, ErrRidingPotion
		}
	}
	if !o.inItemTransaction {
		var result RidingPotionResult
		err := o.withinCharacterAssetTransaction(ctx, req.CharacterID, func(tx *Owner) error { var err error; result, err = tx.UseRidingPotion(ctx, req, now); return err })
		if err != nil {
			return RidingPotionResult{}, err
		}
		return result, nil
	}
	if !req.Revalidate() {
		return RidingPotionResult{}, ErrRidingPotion
	}
	c, found, err := o.characters.Load(ctx, req.CharacterID)
	if err != nil {
		return RidingPotionResult{}, err
	}
	if !found || c.AccountID != req.AccountID || c.Stats["delete_flag"] != 0 || int64(c.Level) < d.MinimumLevel || (d.MaximumLevel != 0 && int64(c.Level) > d.MaximumLevel) ||
		(d.DisallowHardcore && c.Stats["is_hardcore_mode"] != 0) {
		return RidingPotionResult{}, ErrRidingPotion
	}
	c = repo.CloneCharacter(c)
	if c.Stats == nil {
		c.Stats = map[string]int64{}
	}
	cooldownKey := "riding_potion_next_use_ms_" + strconv.FormatInt(d.ItemID, 10)
	if c.Stats[cooldownKey] < 0 || c.Stats[cooldownKey] > now.UnixMilli() {
		return RidingPotionResult{}, ErrRidingPotion
	}
	bag, found, err := o.repo.Load(ctx, req.CharacterID)
	if err != nil {
		return RidingPotionResult{}, err
	}
	if !found {
		return RidingPotionResult{}, ErrInventoryNotFound
	}
	bag = repo.CloneInventory(bag)
	key := slotKey(0, req.SlotIndex)
	stack, found := bag.Slots[key]
	if !found || stack.ItemID != d.ItemID || !ridingPotionStackUsable(stack, now) {
		return RidingPotionResult{}, ErrRidingPotion
	}
	if p := strings.TrimSpace(stack.Extra["pvf_path"]); p != "" && !strings.EqualFold(strings.ReplaceAll(p, "\\", "/"), d.PVFPath) {
		return RidingPotionResult{}, ErrRidingPotion
	}
	changed := map[int16]bool{}
	// 只消耗本角色主背包的有效材料，按槽位排序，材料不足时整个事务回滚。
	indices := []int16{}
	for k := range bag.Slots {
		list, index, ok := parseSlotKey(k)
		if ok && list == 0 && index >= 0 {
			indices = append(indices, index)
		}
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	for id, need := range d.Materials {
		for _, index := range indices {
			k := slotKey(0, index)
			material, ok := bag.Slots[k]
			if !ok || material.ItemID != id || !ridingPotionStackUsable(material, now) {
				continue
			}
			amount := min(material.Count, need)
			material.Count -= amount
			need -= amount
			if material.Count == 0 {
				delete(bag.Slots, k)
			} else {
				updateStackRawAmount(&material)
				bag.Slots[k] = material
			}
			changed[index] = true
			if need == 0 {
				break
			}
		}
		if need != 0 {
			return RidingPotionResult{}, ErrRidingPotion
		}
	}
	stack.Count--
	if stack.Count == 0 {
		delete(bag.Slots, key)
	} else {
		updateStackRawAmount(&stack)
		bag.Slots[key] = stack
	}
	changed[req.SlotIndex] = true
	bag.UpdatedAt = now
	if err := repo.SaveInventoryFields(ctx, o.repo, bag, repo.InventoryFieldSlots); err != nil {
		return RidingPotionResult{}, err
	}
	if d.CoolTimeMS > 0 {
		c.Stats[cooldownKey] = now.UnixMilli() + d.CoolTimeMS
		c.UpdatedAt = now
		if err := repo.SaveCharacterFields(ctx, o.characters, c, repo.CharacterFieldStats); err != nil {
			return RidingPotionResult{}, err
		}
	}
	if !req.Revalidate() {
		return RidingPotionResult{}, ErrRidingPotion
	}
	result := RidingPotionResult{ItemID: d.ItemID, Remaining: stack.Count}
	for index := range changed {
		result.ChangedSlots = append(result.ChangedSlots, index)
	}
	sort.Slice(result.ChangedSlots, func(i, j int) bool { return result.ChangedSlots[i] < result.ChangedSlots[j] })
	return result, nil
}

func ridingPotionStackUsable(stack repo.ItemStack, now time.Time) bool {
	if stack.Count <= 0 || stack.Count > math.MaxInt32 || (!stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt)) {
		return false
	}
	kind := strings.TrimSpace(stack.Extra["item_kind"])
	return kind == "" || strings.EqualFold(kind, "stackable")
}
