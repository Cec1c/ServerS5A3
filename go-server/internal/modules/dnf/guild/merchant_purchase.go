package guild

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const (
	MerchantShopMinID         = uint32(201)
	MerchantShopMaxID         = uint32(220)
	merchantPurchaseKeyPrefix = "merchant_purchase:"
)

var (
	ErrMerchantShop             = errors.New("guild merchant shop was not found")
	ErrMerchantProduct          = errors.New("guild merchant product was not found")
	ErrMerchantStaleShop        = errors.New("guild merchant shop level is stale")
	ErrMerchantProductLimit     = errors.New("guild merchant product quantity exceeds its stack limit")
	ErrMerchantCharacterLevel   = errors.New("character level is below the guild merchant product requirement")
	ErrMerchantAP               = errors.New("insufficient guild merchant action points")
	ErrMerchantCostOverflow     = errors.New("guild merchant purchase cost exceeds uint32")
	ErrMerchantInventory        = errors.New("guild merchant inventory is unavailable")
	ErrMerchantInventoryFull    = errors.New("guild merchant inventory has no compatible slot")
	ErrMerchantGrant            = errors.New("guild merchant item grant failed")
	ErrMerchantPurchaseIdentity = errors.New("guild merchant purchase receipt identity does not match")
)

type MerchantProduct struct {
	ItemID       uint32 `json:"item_id"`
	AP           uint32 `json:"ap"`
	StackLimit   uint32 `json:"stack_limit"`
	MinimumLevel int    `json:"minimum_level"`
}

type MerchantShop struct {
	Products  map[uint32]MerchantProduct `json:"products,omitempty"`
	ID        uint32                     `json:"id"`
	NPCID     uint32                     `json:"npc_id"`
	BaseNPCID uint32                     `json:"base_npc_id"`
	Index     uint8                      `json:"index"`
	Level     uint8                      `json:"level"`
}

type MerchantPurchaseCatalog struct {
	Shops map[uint32]MerchantShop `json:"shops,omitempty"`
}

func (c *MerchantPurchaseCatalog) Validate() error {
	if c == nil || len(c.Shops) == 0 {
		return ErrMerchantCatalog
	}
	for key, shop := range c.Shops {
		index, baseNPC, level, ok := merchantPurchaseShopSpec(key)
		if !ok || shop.ID != key || shop.Index != index || shop.BaseNPCID != baseNPC ||
			shop.Level != level || shop.NPCID != merchantPurchaseNPC(index, level) ||
			len(shop.Products) == 0 {
			return ErrMerchantCatalog
		}
		for itemID, product := range shop.Products {
			if itemID == 0 || product.ItemID != itemID || product.AP == 0 ||
				product.StackLimit == 0 || product.MinimumLevel < 0 {
				return ErrMerchantCatalog
			}
		}
	}
	return nil
}

func (c *MerchantPurchaseCatalog) Shop(id uint32) (MerchantShop, bool) {
	if c == nil {
		return MerchantShop{}, false
	}
	shop, ok := c.Shops[id]
	return shop, ok
}

func (s MerchantShop) Product(itemID uint32) (MerchantProduct, bool) {
	product, ok := s.Products[itemID]
	return product, ok
}

// 201..210 are defence shops and 211..220 are mercenary shops. The product
// NPC is distinct from the merchant upgrade NPC (390/389).
func merchantPurchaseShopSpec(shopID uint32) (index uint8, baseNPC uint32, level uint8, ok bool) {
	if shopID < MerchantShopMinID || shopID > MerchantShopMaxID {
		return 0, 0, 0, false
	}
	if shopID < 211 {
		return 0, 390, uint8(shopID-201) + 1, true
	}
	return 1, 389, uint8(shopID-211) + 1, true
}

func merchantPurchaseNPC(index uint8, level uint8) uint32 {
	if level < MerchantMinLevel || level > MerchantMaxLevel {
		return 0
	}
	if index == 0 {
		return 420 + uint32(level)
	}
	if index == 1 {
		return 410 + uint32(level)
	}
	return 0
}

type MerchantPurchaseRequest struct {
	Owner       r.GuildOwner
	RequestID   string
	ItemID      uint32
	Quantity    uint32
	ShopID      uint32
	NPCID       uint32
	GuildID     uint32
	Revalidate  func() bool
	CanInteract func(r.CharacterRecord, *Guild, bool) bool
}

type MerchantPurchaseGrant func(context.Context, r.Group, *r.CharacterRecord, *r.InventoryRecord, MerchantProduct, uint32, time.Time) ([]string, error)

type MerchantPurchaseReceipt struct {
	Owner         r.GuildOwner `json:"owner"`
	GuildID       uint32       `json:"guild_id"`
	ItemID        uint32       `json:"item_id"`
	Quantity      uint32       `json:"quantity"`
	ShopID        uint32       `json:"shop_id"`
	NPCID         uint32       `json:"npc_id"`
	MerchantIndex uint8        `json:"merchant_index"`
	MerchantLevel uint8        `json:"merchant_level"`
	CostAP        uint32       `json:"cost_ap"`
	BalanceAfter  uint32       `json:"balance_after"`
	ExpireAt      time.Time    `json:"expire_at"`
	Slots         []string     `json:"slots"`
	At            time.Time    `json:"at"`
}

func validateMerchantPurchaseReceipts(reg Registry) error {
	for key, receipt := range reg.MerchantPurchaseReceipts {
		requestID, ok := strings.CutPrefix(key, merchantPurchaseKeyPrefix)
		if !ok || !validPurchaseID(requestID) ||
			receipt.Owner.AccountID == "" || receipt.Owner.CharacterID == "" ||
			receipt.GuildID == 0 || receipt.ItemID == 0 || receipt.Quantity == 0 ||
			receipt.ShopID < MerchantShopMinID || receipt.ShopID > MerchantShopMaxID ||
			receipt.CostAP == 0 || receipt.At.IsZero() ||
			!receipt.ExpireAt.Equal(itemexpiration.NextDailyDeleteAt(receipt.At)) ||
			!validMerchantPurchaseSlots(receipt.Slots) {
			return ErrInvalidRegistry
		}
		index, _, level, valid := merchantPurchaseShopSpec(receipt.ShopID)
		if !valid || receipt.MerchantIndex != index || receipt.MerchantLevel != level ||
			receipt.NPCID != merchantPurchaseNPC(index, level) {
			return ErrInvalidRegistry
		}
	}
	return nil
}

func validMerchantPurchaseSlots(slots []string) bool {
	if len(slots) == 0 {
		return false
	}
	seen := make(map[string]bool, len(slots))
	for _, slot := range slots {
		if slot == "" || strings.TrimSpace(slot) != slot || seen[slot] {
			return false
		}
		seen[slot] = true
	}
	return true
}

func validMerchantPurchaseGrant(
	before, after r.InventoryRecord,
	slots []string,
	product MerchantProduct,
	quantity uint32,
	expires time.Time,
) bool {
	if !reflect.DeepEqual(before.Warehouse, after.Warehouse) {
		return false
	}
	returned := make(map[string]bool, len(slots))
	for _, slot := range slots {
		returned[slot] = true
	}
	for key, old := range before.Slots {
		if returned[key] {
			continue
		}
		current, found := after.Slots[key]
		if !found || !reflect.DeepEqual(old, current) {
			return false
		}
	}
	for key := range after.Slots {
		if returned[key] {
			continue
		}
		if _, existed := before.Slots[key]; !existed {
			return false
		}
	}
	var added uint64
	for _, key := range slots {
		old, existed := before.Slots[key]
		current, found := after.Slots[key]
		if !found || current.ItemID != int64(product.ItemID) ||
			current.Count <= 0 || !current.ExpireAt.Equal(expires) {
			return false
		}
		base := int64(0)
		if existed {
			if old.ItemID != current.ItemID || old.Count <= 0 || current.Count <= old.Count {
				return false
			}
			base = old.Count
		}
		delta := uint64(current.Count - base)
		if delta > uint64(quantity)-added {
			return false
		}
		added += delta
	}
	return added == uint64(quantity)
}

// ApplyMerchantPurchase commits the actual inventory grant, shared guild AP
// debit and permanent receipt in one registry/character/inventory transaction.
// A duplicate request is identified before current shop level is consulted, so
// an upgrade cannot turn a durable retry into a second purchase or extend its
// original expiration.
func ApplyMerchantPurchase(
	ctx context.Context,
	uow r.GuildUnitOfWork,
	q MerchantPurchaseRequest,
	catalog *MerchantPurchaseCatalog,
	grant MerchantPurchaseGrant,
	now time.Time,
) (MerchantPurchaseReceipt, error) {
	var result MerchantPurchaseReceipt
	if uow == nil || q.Revalidate == nil || q.CanInteract == nil || grant == nil || now.IsZero() {
		return result, ErrRepositoryUnavailable
	}
	if !validPurchaseID(q.RequestID) || q.GuildID == 0 || q.ItemID == 0 ||
		q.Quantity == 0 || q.ShopID < MerchantShopMinID || q.ShopID > MerchantShopMaxID {
		return result, ErrMerchantPurchaseIdentity
	}
	if err := catalog.Validate(); err != nil {
		return result, err
	}
	inventoryUOW, ok := uow.(r.GuildInventoryUnitOfWork)
	if !ok || inventoryUOW == nil {
		return result, r.ErrGuildTransaction
	}

	key := merchantPurchaseKeyPrefix + q.RequestID
	err := inventoryUOW.WithinGuildInventory(ctx, q.Owner, func(tx r.Group) error {
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		actor, found, err := tx.Character.Load(ctx, q.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.CharacterID != q.Owner.CharacterID ||
			actor.AccountID != q.Owner.AccountID || actor.Stats["delete_flag"] != 0 {
			return ErrCharacterNotFound
		}
		actor = r.CloneCharacter(actor)

		reg, err := Load(ctx, tx.Settings)
		if err != nil {
			return err
		}
		g, member, found := reg.GuildForOwner(q.Owner)
		if !found || g.ID != q.GuildID {
			return ErrNotMember
		}
		if GradePermissionsFor(g, member.Grade)&PurchasePermission == 0 {
			return ErrPermissionDenied
		}

		old, replay := reg.MerchantPurchaseReceipts[key]
		if replay && (old.Owner != q.Owner || old.GuildID != q.GuildID ||
			old.ItemID != q.ItemID || old.Quantity != q.Quantity ||
			old.ShopID != q.ShopID || old.NPCID != q.NPCID) {
			return ErrMerchantPurchaseIdentity
		}
		if !q.CanInteract(actor, g, replay) {
			return ErrMerchantInteraction
		}
		if replay {
			if !q.Revalidate() {
				return ErrCharacterNotFound
			}
			result = old
			result.Slots = append([]string(nil), old.Slots...)
			return nil
		}

		shop, found := catalog.Shop(q.ShopID)
		if !found {
			return ErrMerchantShop
		}
		if shop.NPCID != q.NPCID {
			return ErrMerchantNPC
		}
		currentLevel, err := MerchantLevel(g, shop.Index)
		if err != nil {
			return err
		}
		if currentLevel != shop.Level {
			return ErrMerchantStaleShop
		}
		product, found := shop.Product(q.ItemID)
		if !found {
			return ErrMerchantProduct
		}
		if actor.Level < product.MinimumLevel {
			return ErrMerchantCharacterLevel
		}
		if q.Quantity > product.StackLimit {
			return ErrMerchantProductLimit
		}
		if uint64(q.Quantity) > math.MaxUint32/uint64(product.AP) {
			return ErrMerchantCostOverflow
		}
		costAP := uint64(product.AP) * uint64(q.Quantity)
		if uint64(g.Essence.AP) < costAP {
			return ErrMerchantAP
		}

		inventory, found, err := tx.Inventory.Load(ctx, q.Owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || inventory.CharacterID != q.Owner.CharacterID {
			return ErrMerchantInventory
		}
		inventory = r.CloneInventory(inventory)
		if inventory.Slots == nil {
			inventory.Slots = make(map[string]r.ItemStack)
		}
		beforeGrant := r.CloneInventory(inventory)
		expireAt := itemexpiration.NextDailyDeleteAt(now)
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		slots, err := grant(ctx, tx, &actor, &inventory, product, q.Quantity, expireAt)
		if err != nil {
			return err
		}
		if !validMerchantPurchaseSlots(slots) {
			return ErrMerchantGrant
		}
		if !validMerchantPurchaseGrant(beforeGrant, inventory, slots, product, q.Quantity, expireAt) {
			return ErrMerchantGrant
		}
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}

		g.Essence.AP = uint32(uint64(g.Essence.AP) - costAP)
		g.UpdatedAt = now
		actor.UpdatedAt = now
		inventory.UpdatedAt = now
		result = MerchantPurchaseReceipt{
			Owner: q.Owner, GuildID: g.ID, ItemID: q.ItemID, Quantity: q.Quantity,
			ShopID: q.ShopID, NPCID: q.NPCID, MerchantIndex: shop.Index,
			MerchantLevel: shop.Level, CostAP: uint32(costAP),
			BalanceAfter: g.Essence.AP, ExpireAt: expireAt,
			Slots: append([]string(nil), slots...), At: now,
		}
		if reg.MerchantPurchaseReceipts == nil {
			reg.MerchantPurchaseReceipts = make(map[string]MerchantPurchaseReceipt)
		}
		reg.MerchantPurchaseReceipts[key] = result
		if err := r.SaveCharacterFields(ctx, tx.Character, actor, r.CharacterFieldStats); err != nil {
			return err
		}
		if err := r.SaveInventoryFields(ctx, tx.Inventory, inventory, r.InventoryFieldSlots); err != nil {
			return err
		}
		if err := Save(ctx, tx.Settings, reg, now); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrCharacterNotFound
		}
		return nil
	})
	if err != nil {
		return MerchantPurchaseReceipt{}, err
	}
	return result, nil
}
