package inventory

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

var (
	ErrWeaponSkinWeapon = errors.New("幻化武器已变化或不符合材料条件")
	ErrWeaponSkinMold   = errors.New("当前配方的林纳斯模具不足")
	ErrWeaponSkinSpace  = errors.New("背包没有可放置武器外观道具的位置")
	ErrWeaponSkinItem   = errors.New("当前槽位没有可登记的武器外观道具")
)

// WeaponSkinActor 绑定服务端从本次请求解析的角色、账号和场景。
type WeaponSkinActor struct {
	CharacterID string
	AccountID   string
	Now         time.Time
	Current     func() bool
}

type BeiyunForgeRequest struct {
	WeaponSkinActor
	WeaponSlot     int16
	MoldSlot       int16
	ExpectedWeapon dnfrepo.ItemStack // 服务端打开确认前取得的物品快照
	AllowCrossJob  bool              // 由服务端保存的目录选材会话决定，旧别云剑入口继续校验职业。
}

// BeiyunForgePlacement 由已核对的运行时 PVF 道具定义提供。
type BeiyunForgePlacement struct {
	Recipe     weaponskin.BeiyunRecipe
	Appearance weaponskin.Appearance
	Weapon     itemplacement.SlotPolicy
	Mold       itemplacement.SlotPolicy
	Product    itemplacement.SlotPolicy
}

func (p BeiyunForgePlacement) Target() weaponskin.Appearance {
	if p.Appearance.ID != 0 {
		return p.Appearance
	}
	a, _ := weaponskin.FromAppearance(weaponskin.SampleID)
	return a
}

type WeaponSkinInventoryChange struct {
	ChangedSlots []int16
	ProductSlot  int16
}

// AllowsWeapon 是背包选材与扣料共用的规则，实际槽位由当前背包容量决定。
func (p BeiyunForgePlacement) AllowsWeapon(bag *dnfrepo.InventoryRecord, list byte, slot int16, item dnfrepo.ItemStack, now time.Time) bool {
	return item.ItemID == int64(p.Target().SourceID) && item.Count == 1 && item.ExpireAt.IsZero() &&
		!itemlock.IsLockedAt(item, now) && p.Weapon.ForInventory(bag).Contains(list, slot)
}

// ForgeBeiyun 消耗武器与模具，发放需要单独使用的外观道具。
func (o *Owner) ForgeBeiyun(ctx context.Context, req BeiyunForgeRequest, placement BeiyunForgePlacement) (WeaponSkinInventoryChange, error) {
	if req.WeaponSlot == req.MoldSlot || placement.Weapon.ListType != listTypeMain ||
		placement.Mold.ListType != listTypeMain || placement.Product.ListType != listTypeMain {
		return WeaponSkinInventoryChange{}, ErrWeaponSkinWeapon
	}
	var result WeaponSkinInventoryChange
	appearance := placement.Target()
	items := appearance.Items(placement.Recipe)
	err := o.withWeaponSkinAssets(ctx, req.WeaponSkinActor, func(tx *Owner, character dnfrepo.CharacterRecord, bag dnfrepo.InventoryRecord) error {
		if !req.AllowCrossJob && !appearance.Allows(character.Job) {
			return weaponskin.ErrCharacter
		}
		if weaponskin.Registered(character, appearance) {
			return weaponskin.ErrAlreadyRegistered
		}
		weaponKey, moldKey := slotKey(listTypeMain, req.WeaponSlot), slotKey(listTypeMain, req.MoldSlot)
		weapon, mold := bag.Slots[weaponKey], bag.Slots[moldKey]
		if !placement.AllowsWeapon(&bag, listTypeMain, req.WeaponSlot, weapon, req.Now) ||
			!reflect.DeepEqual(weapon, req.ExpectedWeapon) {
			return ErrWeaponSkinWeapon
		}
		if mold.ItemID != int64(items.MoldID) || !weaponSkinStackUsable(mold, req.Now) ||
			!placement.Mold.ForInventory(&bag).Contains(listTypeMain, req.MoldSlot) {
			return ErrWeaponSkinMold
		}

		delete(bag.Slots, weaponKey)
		consumeWeaponSkinStack(bag.Slots, moldKey, mold)
		productSlot := int16(-1)
		for _, slot := range placement.Product.ForGrant().ForInventory(&bag).EmptySlots() {
			if _, occupied := bag.Slots[slotKey(listTypeMain, slot)]; !occupied {
				productSlot = slot
				break
			}
		}
		if productSlot < 0 {
			return ErrWeaponSkinSpace
		}
		productKey := slotKey(listTypeMain, productSlot)
		bag.Slots[productKey] = dnfrepo.ItemStack{
			ItemID: int64(items.ProductID), Count: 1, Bind: placement.Recipe != weaponskin.SecretRecipe,
			Extra: map[string]string{
				"item_kind": "stackable", "pvf_path": items.ProductPath,
				"stackable_type": "[waste]", weaponskin.ProductSkinKey: strconv.FormatUint(uint64(appearance.ID), 10),
			},
		}
		if !req.Current() {
			return weaponskin.ErrCharacter
		}
		if err := saveWeaponSkinBag(ctx, tx.repo, bag, req.Now, weaponKey, moldKey, productKey); err != nil {
			return err
		}
		result = WeaponSkinInventoryChange{ChangedSlots: []int16{req.WeaponSlot, req.MoldSlot}, ProductSlot: productSlot}
		if productSlot != req.WeaponSlot && productSlot != req.MoldSlot {
			result.ChangedSlots = append(result.ChangedSlots, productSlot)
		}
		return nil
	})
	if err != nil {
		return WeaponSkinInventoryChange{}, err
	}
	return result, nil
}

// UseBeiyunAppearance 将背包里的成品登记给当前角色，应用仍由仓库处理。
func (o *Owner) UseBeiyunAppearance(ctx context.Context, actor WeaponSkinActor, slot int16, placement BeiyunForgePlacement) error {
	appearance := placement.Target()
	items := appearance.Items(placement.Recipe)
	return o.withWeaponSkinAssets(ctx, actor, func(tx *Owner, character dnfrepo.CharacterRecord, bag dnfrepo.InventoryRecord) error {
		key := slotKey(listTypeMain, slot)
		product := bag.Slots[key]
		if !matchesWeaponSkinProduct(product, appearance, items) ||
			!weaponSkinStackUsable(product, actor.Now) || !placement.Product.ForInventory(&bag).Contains(listTypeMain, slot) {
			return ErrWeaponSkinItem
		}
		registered, err := weaponskin.Register(character, appearance, actor.Now)
		if err != nil {
			return err
		}
		consumeWeaponSkinStack(bag.Slots, key, product)
		if !actor.Current() {
			return weaponskin.ErrCharacter
		}
		if err := saveWeaponSkinBag(ctx, tx.repo, bag, actor.Now, key); err != nil {
			return err
		}
		return dnfrepo.SaveCharacterFields(ctx, tx.characters, registered, dnfrepo.CharacterFieldStats)
	})
}

func matchesWeaponSkinProduct(product dnfrepo.ItemStack, appearance weaponskin.Appearance, items weaponskin.BeiyunRecipeItems) bool {
	if items.ProductID == 0 || product.ItemID != int64(items.ProductID) {
		return false
	}
	if marker, present := product.Extra[weaponskin.ProductSkinKey]; present {
		return marker == strconv.FormatUint(uint64(appearance.ID), 10)
	}
	// 目录成品的编号专属于该外观与配方。邮件领取按运行 PVF 先保存这三项身份。
	// 旧别云剑试验成品的编号沿用既有道具，仍需制作标记。
	return appearance.ID != weaponskin.SampleID &&
		product.Extra["item_kind"] == "stackable" &&
		product.Extra["stackable_type"] == "[waste]" &&
		product.Extra["pvf_path"] == items.ProductPath
}

func (o *Owner) withWeaponSkinAssets(ctx context.Context, actor WeaponSkinActor, apply func(*Owner, dnfrepo.CharacterRecord, dnfrepo.InventoryRecord) error) error {
	if o == nil || o.assets == nil || actor.CharacterID == "" || actor.AccountID == "" || actor.Current == nil || actor.Now.IsZero() {
		return weaponskin.ErrUnavailable
	}
	return o.withinCharacterAssetTransaction(ctx, actor.CharacterID, func(tx *Owner) error {
		if !actor.Current() || tx.characters == nil || tx.repo == nil {
			return weaponskin.ErrCharacter
		}
		character, found, err := tx.characters.Load(ctx, actor.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != actor.CharacterID || character.AccountID != actor.AccountID {
			return weaponskin.ErrCharacter
		}
		bag, found, err := tx.repo.Load(ctx, actor.CharacterID)
		if err != nil {
			return err
		}
		if !found || bag.CharacterID != actor.CharacterID {
			return ErrInventoryNotFound
		}
		if err := dnfrepo.BindInventoryContainerState(ctx, tx.settings, &bag); err != nil {
			return err
		}
		return apply(tx, character, dnfrepo.CloneInventory(bag))
	})
}

func weaponSkinStackUsable(stack dnfrepo.ItemStack, now time.Time) bool {
	return stack.Count > 0 && !isEquipmentLocked(stack) && (stack.ExpireAt.IsZero() || stack.ExpireAt.After(now))
}

func consumeWeaponSkinStack(items map[string]dnfrepo.ItemStack, key string, stack dnfrepo.ItemStack) {
	if stack.Count == 1 {
		delete(items, key)
		return
	}
	stack.Count--
	updateStackRawAmount(&stack)
	items[key] = stack
}

func saveWeaponSkinBag(ctx context.Context, repo dnfrepo.InventoryRepository, bag dnfrepo.InventoryRecord, now time.Time, keys ...string) error {
	bag.UpdatedAt = now.UTC()
	fields := []dnfrepo.InventoryField{dnfrepo.InventoryFieldSlots}
	return dnfrepo.SaveInventoryFieldsScoped(ctx, repo, bag, fields, dnfrepo.InventorySlotDeltaKeysForFields(fields, keys))
}
