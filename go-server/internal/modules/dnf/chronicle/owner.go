package chronicle

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	"longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrOwner        = errors.New("精炼资产归属无效")
	ErrRules        = errors.New("精炼PVF规则不可用")
	ErrTarget       = errors.New("精炼装备或选项无效")
	ErrMaterial     = errors.New("精炼气息无效")
	ErrMismatch     = errors.New("精炼模板与槽位不符")
	ErrDurability   = errors.New("精炼装备耐久未满")
	ErrLocked       = errors.New("精炼物品已锁定")
	ErrUnidentified = errors.New("精炼装备未识别")
)

type Request struct {
	MaterialSlot, TargetSlot int16
	MaterialID, TargetID     int64
	Option                   byte
}
type Option struct {
	AuraID                           uint32
	Job, Grow, EquipmentType, Number byte
}
type State struct {
	Durability           uint16
	Reinforce            byte
	Locked, Unidentified bool
	Options              []Option
}
type Reward struct{ ItemID, Count uint32 }
type RewardSlot struct {
	Slot          int16
	ItemID, Count uint32
}
type Result struct {
	Request           Request
	Succeeded         bool
	MaterialRemaining int64
	Options           int
	Probability, Roll int
	Rewards           []RewardSlot
	ChangedSlots      []int16
	Inventory         repository.InventoryRecord
	AccountInventory  repository.AccountInventoryRecord
}
type Command struct {
	AccountID, CharacterID string
	Request                Request
	UpdatedAt              time.Time
	Catalog                *Catalog
	Identity               func(repository.CharacterRecord) (byte, byte, error)
	State                  func(repository.ItemStack) (State, error)
	SetOptions             func(repository.ItemStack, []Option) (repository.ItemStack, error)
	MaterialPolicy         itemplacement.SlotPolicy
	FailureRewards         func(Equipment, State, Material) ([]Reward, error)
	GrantRewards           func(*repository.InventoryRecord, *repository.AccountInventoryRecord, []Reward) ([]RewardSlot, bool, error)
	Encode                 func(int16, repository.ItemStack) repository.ItemStack
	Random                 func(int) int
}
type Owner struct {
	assets   repository.AccountCharacterAssetUnitOfWork
	settings repository.SettingsRepository
}

func NewOwner(g repository.Group) (*Owner, error) {
	if g.AccountAssets == nil {
		return nil, ErrOwner
	}
	return &Owner{g.AccountAssets, g.Settings}, nil
}

// Refine在同一账号/角色资产事务中校验、扣气息、保存属性或销毁并返材。
func (o *Owner) Refine(ctx context.Context, c Command) (Result, error) {
	if o == nil || o.assets == nil || c.Catalog == nil || c.Identity == nil || c.State == nil || c.SetOptions == nil || c.Encode == nil || strings.TrimSpace(c.AccountID) == "" || strings.TrimSpace(c.CharacterID) == "" {
		return Result{}, ErrOwner
	}
	r := c.Request
	if r.MaterialSlot < 3 || r.TargetSlot < 3 || r.MaterialSlot == r.TargetSlot || r.MaterialID <= 0 || r.TargetID <= 0 || r.Option > 31 {
		return Result{}, ErrMaterial
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := c.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var result Result
	err := o.assets.WithinAccountCharacterAssets(ctx, c.AccountID, c.CharacterID, func(accounts repository.AccountInventoryRepository, characters repository.CharacterRepository, inventories repository.InventoryRepository, _ repository.EquipmentRepository) error {
		character, found, err := characters.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != c.AccountID || character.CharacterID != c.CharacterID {
			return ErrOwner
		}
		job, grow, err := c.Identity(character)
		if err != nil {
			return err
		}
		inv, found, err := inventories.Load(ctx, c.CharacterID)
		if err != nil {
			return err
		}
		if !found || inv.CharacterID != c.CharacterID {
			return ErrOwner
		}
		if err = repository.BindInventoryContainerState(ctx, o.settings, &inv); err != nil {
			return err
		}
		inv = repository.CloneInventory(inv)
		acc, _, err := accounts.Load(ctx, c.AccountID)
		if err != nil {
			return err
		}
		acc = repository.CloneAccountInventory(acc)
		acc.AccountID = c.AccountID
		if acc.Slots == nil {
			acc.Slots = map[string]repository.ItemStack{}
		}
		mk, tk := slotKey(r.MaterialSlot), slotKey(r.TargetSlot)
		mat, mok := inv.Slots[mk]
		target, tok := inv.Slots[tk]
		if !mok || mat.Count <= 0 || !c.MaterialPolicy.ForInventory(&inv).Contains(0, r.MaterialSlot) || !usable(mat, now) {
			return ErrMaterial
		}
		equipmentPolicy := itemplacement.SlotPolicy{ListType: 0, Start: 9, End: 64, AllowQuickSlots: true}.ForInventory(&inv)
		if !tok || target.Count != 1 || !equipmentPolicy.Contains(0, r.TargetSlot) || !usable(target, now) {
			return ErrTarget
		}
		if mat.ItemID != r.MaterialID || target.ItemID != r.TargetID {
			return ErrMismatch
		}
		material, err := c.Catalog.Material(mat.ItemID)
		if err != nil {
			return err
		}
		equipment, err := c.Catalog.Equipment(target.ItemID)
		if err != nil {
			return err
		}
		if !material.Allows(job, grow, equipment, r.Option) {
			return ErrTarget
		}
		state, err := c.State(target)
		if err != nil {
			return err
		}
		matState, err := c.State(mat)
		if err != nil {
			return err
		}
		if state.Locked || matState.Locked {
			return ErrLocked
		}
		if state.Unidentified {
			return ErrUnidentified
		}
		if state.Durability != equipment.Durability {
			return ErrDurability
		}
		if len(state.Options) >= 2 {
			return ErrTarget
		}
		for _, opt := range state.Options {
			if opt.AuraID == 1254+uint32(material.AuraType) && opt.Number == r.Option {
				return ErrTarget
			}
		}
		random := c.Random
		if random == nil {
			random = rand.Intn
		}
		roll := random(101)
		if roll < 0 || roll > 100 {
			return ErrRules
		}
		probability := material.Probabilities[len(state.Options)]
		success := probability >= 100 || roll <= probability
		changed := map[int16]bool{r.MaterialSlot: true, r.TargetSlot: true}
		mat.Count--
		if mat.Count == 0 {
			delete(inv.Slots, mk)
		} else {
			inv.Slots[mk] = c.Encode(r.MaterialSlot, mat)
		}
		var rows []RewardSlot
		accountDirty := false
		if success {
			options := append(append([]Option(nil), state.Options...), Option{AuraID: 1254 + uint32(material.AuraType), Job: job, Grow: grow, EquipmentType: equipment.TypeID, Number: r.Option})
			target, err = c.SetOptions(target, options)
			if err != nil {
				return err
			}
			inv.Slots[tk] = c.Encode(r.TargetSlot, target)
		} else {
			if c.FailureRewards == nil || c.GrantRewards == nil {
				return ErrRules
			}
			rewards, err := c.FailureRewards(equipment, state, material)
			if err != nil {
				return err
			}
			delete(inv.Slots, tk)
			rows, accountDirty, err = c.GrantRewards(&inv, &acc, rewards)
			if err != nil {
				return err
			}
			if len(rows) == 0 || len(rows) > 255 {
				return ErrRules
			}
			for _, row := range rows {
				changed[row.Slot] = true
			}
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		inv.UpdatedAt = now
		if err = repository.SaveInventoryFields(ctx, inventories, inv, repository.InventoryFieldSlots); err != nil {
			return err
		}
		if accountDirty {
			acc.UpdatedAt = now
			if err = accounts.Save(ctx, acc); err != nil {
				return err
			}
		}
		slots := make([]int16, 0, len(changed))
		for slot := range changed {
			slots = append(slots, slot)
		}
		sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
		result = Result{Request: r, Succeeded: success, MaterialRemaining: mat.Count, Options: len(state.Options), Probability: probability, Roll: roll, Rewards: rows, ChangedSlots: slots, Inventory: inv, AccountInventory: acc}
		if success {
			result.Options++
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}
func slotKey(slot int16) string { return "0:" + strconv.Itoa(int(slot)) }
func usable(s repository.ItemStack, now time.Time) bool {
	return s.ExpireAt.IsZero() || now.Before(s.ExpireAt)
}
func FailureCode(err error) byte {
	switch {
	case errors.Is(err, ErrMismatch):
		return 0x17
	case errors.Is(err, ErrDurability):
		return 7
	case errors.Is(err, ErrLocked):
		return 0xd5
	case errors.Is(err, ErrUnidentified):
		return 0xae
	case errors.Is(err, ErrMaterial):
		return 4
	default:
		return 0x13
	}
}
func ValidateOptions(options []Option) error {
	if len(options) > 2 {
		return ErrTarget
	}
	for _, o := range options {
		if o.AuraID < 1254 || o.AuraID > 1256 || int(o.Job) >= len(jobNames) || o.Grow > 15 || o.Number > 31 {
			return fmt.Errorf("%w: 持久精炼属性无效", ErrTarget)
		}
	}
	return nil
}
