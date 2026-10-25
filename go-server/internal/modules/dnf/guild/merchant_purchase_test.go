package guild

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const (
	merchantPurchaseDefenceItem  = uint32(10156091)
	merchantPurchaseDefenceAlt   = uint32(10156092)
	merchantPurchaseMercenary    = uint32(10156642)
	merchantPurchaseMercenaryAlt = uint32(10156643)
)

func merchantPurchaseCatalogFixture() *MerchantPurchaseCatalog {
	return &MerchantPurchaseCatalog{Shops: map[uint32]MerchantShop{
		201: {
			ID: 201, NPCID: 421, BaseNPCID: 390, Index: 0, Level: 1,
			Products: map[uint32]MerchantProduct{
				merchantPurchaseDefenceItem: {ItemID: merchantPurchaseDefenceItem, AP: 2, StackLimit: 10, MinimumLevel: 1},
				merchantPurchaseDefenceAlt:  {ItemID: merchantPurchaseDefenceAlt, AP: 3, StackLimit: 20, MinimumLevel: 50},
			},
		},
		211: {
			ID: 211, NPCID: 411, BaseNPCID: 389, Index: 1, Level: 1,
			Products: map[uint32]MerchantProduct{
				merchantPurchaseMercenary:    {ItemID: merchantPurchaseMercenary, AP: 7, StackLimit: 10, MinimumLevel: 1},
				merchantPurchaseMercenaryAlt: {ItemID: merchantPurchaseMercenaryAlt, AP: 17, StackLimit: 5, MinimumLevel: 80},
			},
		},
	}}
}

func merchantPurchaseSuccessfulGrant(_ context.Context, _ r.Group, actor *r.CharacterRecord, inventory *r.InventoryRecord, product MerchantProduct, quantity uint32, expires time.Time) ([]string, error) {
	key := "0:50"
	stack, found := inventory.Slots[key]
	for slot := 51; found && (stack.ItemID != int64(product.ItemID) || !stack.ExpireAt.Equal(expires)); slot++ {
		if slot > 80 {
			return nil, ErrMerchantInventoryFull
		}
		key = fmt.Sprintf("0:%d", slot)
		stack, found = inventory.Slots[key]
	}
	if !found {
		stack = r.ItemStack{ItemID: int64(product.ItemID)}
	}
	stack.Count += int64(quantity)
	stack.ExpireAt = expires
	inventory.Slots[key] = stack
	if actor.Stats == nil {
		actor.Stats = make(map[string]int64)
	}
	actor.Stats["merchant_grant_marker"] = int64(quantity)
	return []string{key}, nil
}

func merchantPurchaseFixture(t *testing.T) (r.Group, MerchantPurchaseRequest, *MerchantPurchaseCatalog, time.Time) {
	t.Helper()
	repos := guildTestGroup(t, 3)
	reg, g, _, _ := textRegistry(t)
	g.Essence = EssenceBalance{AP: 1000, GP: 20000}
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	if err := Save(context.Background(), repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if err := repos.Inventory.Save(context.Background(), r.InventoryRecord{
			CharacterID: id,
			Slots:       map[string]r.ItemStack{"0:1": {ItemID: 777, Count: 1}},
			Warehouse:   map[string]r.ItemStack{"0:0": {ItemID: 888, Count: 2}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	q := MerchantPurchaseRequest{
		Owner:   r.GuildOwner{CharacterID: "1", AccountID: "a1"},
		GuildID: g.ID, RequestID: fmt.Sprintf("%064x", 1),
		ItemID: merchantPurchaseDefenceItem, Quantity: 3, ShopID: 201, NPCID: 421,
		Revalidate:  func() bool { return true },
		CanInteract: func(r.CharacterRecord, *Guild, bool) bool { return true },
	}
	return repos, q, merchantPurchaseCatalogFixture(), now
}

func TestMerchantPurchaseCatalogValidatesRealShopLayoutAndProducts(t *testing.T) {
	catalog := merchantPurchaseCatalogFixture()
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	for shopID := MerchantShopMinID; shopID <= MerchantShopMaxID; shopID++ {
		index, base, level, ok := merchantPurchaseShopSpec(shopID)
		if !ok || index > 1 || level < 1 || level > 10 ||
			base != map[uint8]uint32{0: 390, 1: 389}[index] ||
			merchantPurchaseNPC(index, level) != map[uint8]uint32{0: 420, 1: 410}[index]+uint32(level) {
			t.Fatalf("shop mapping %d = index=%d base=%d level=%d ok=%v", shopID, index, base, level, ok)
		}
	}
	if _, _, _, ok := merchantPurchaseShopSpec(200); ok {
		t.Fatal("shop 200 accepted")
	}
	if _, _, _, ok := merchantPurchaseShopSpec(221); ok {
		t.Fatal("shop 221 accepted")
	}

	for name, mutate := range map[string]func(*MerchantPurchaseCatalog){
		"empty": func(c *MerchantPurchaseCatalog) { c.Shops = nil },
		"wrong_id": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[201]
			s.ID = 202
			c.Shops[201] = s
		},
		"wrong_index": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[201]
			s.Index = 1
			c.Shops[201] = s
		},
		"wrong_base": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[211]
			s.BaseNPCID = 390
			c.Shops[211] = s
		},
		"wrong_level": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[211]
			s.Level = 2
			c.Shops[211] = s
		},
		"wrong_npc": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[201]
			s.NPCID = 430
			c.Shops[201] = s
		},
		"no_products": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[201]
			s.Products = nil
			c.Shops[201] = s
		},
		"item_key_mismatch": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[201]
			p := s.Products[merchantPurchaseDefenceItem]
			p.ItemID = merchantPurchaseDefenceAlt
			s.Products[merchantPurchaseDefenceItem] = p
			c.Shops[201] = s
		},
		"zero_ap": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[201]
			p := s.Products[merchantPurchaseDefenceItem]
			p.AP = 0
			s.Products[merchantPurchaseDefenceItem] = p
			c.Shops[201] = s
		},
		"zero_stack": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[201]
			p := s.Products[merchantPurchaseDefenceItem]
			p.StackLimit = 0
			s.Products[merchantPurchaseDefenceItem] = p
			c.Shops[201] = s
		},
		"negative_minimum": func(c *MerchantPurchaseCatalog) {
			s := c.Shops[201]
			p := s.Products[merchantPurchaseDefenceItem]
			p.MinimumLevel = -1
			s.Products[merchantPurchaseDefenceItem] = p
			c.Shops[201] = s
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := merchantPurchaseCatalogFixture()
			mutate(c)
			if err := c.Validate(); !errors.Is(err, ErrMerchantCatalog) {
				t.Fatalf("invalid catalog accepted: %v", err)
			}
		})
	}
	var nilCatalog *MerchantPurchaseCatalog
	if err := nilCatalog.Validate(); !errors.Is(err, ErrMerchantCatalog) {
		t.Fatal(err)
	}
}

func TestMerchantPurchaseCommitsBothMerchantsActualAPQuantityExpiryInventoryAndReceipt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		item     uint32
		quantity uint32
		shop     uint32
		npc      uint32
		index    uint8
		cost     uint32
	}{
		{"defence", merchantPurchaseDefenceItem, 3, 201, 421, 0, 6},
		{"mercenary", merchantPurchaseMercenary, 2, 211, 411, 1, 14},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repos, q, catalog, now := merchantPurchaseFixture(t)
			q.ItemID, q.Quantity, q.ShopID, q.NPCID = tc.item, tc.quantity, tc.shop, tc.npc
			beforeInventory, found, err := repos.Inventory.Load(context.Background(), q.Owner.CharacterID)
			if err != nil || !found {
				t.Fatal(err)
			}
			beforeInventory = r.CloneInventory(beforeInventory)
			got, err := ApplyMerchantPurchase(context.Background(), repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, now)
			if err != nil {
				t.Fatal(err)
			}
			expires := itemexpiration.NextDailyDeleteAt(now)
			if got.Owner != q.Owner || got.GuildID != q.GuildID || got.ItemID != tc.item ||
				got.Quantity != tc.quantity || got.ShopID != tc.shop || got.NPCID != tc.npc ||
				got.MerchantIndex != tc.index || got.MerchantLevel != 1 ||
				got.CostAP != tc.cost || got.BalanceAfter != 1000-tc.cost ||
				!got.ExpireAt.Equal(expires) || !got.At.Equal(now) ||
				!reflect.DeepEqual(got.Slots, []string{"0:50"}) {
				t.Fatalf("receipt=%+v expires=%s", got, expires)
			}

			inventory, found, err := repos.Inventory.Load(context.Background(), q.Owner.CharacterID)
			if err != nil || !found {
				t.Fatal(err)
			}
			if inventory.Slots["0:50"].ItemID != int64(tc.item) ||
				inventory.Slots["0:50"].Count != int64(tc.quantity) ||
				!inventory.Slots["0:50"].ExpireAt.Equal(expires) ||
				!reflect.DeepEqual(inventory.Slots["0:1"], beforeInventory.Slots["0:1"]) ||
				!reflect.DeepEqual(inventory.Warehouse, beforeInventory.Warehouse) {
				t.Fatalf("inventory=%+v", inventory)
			}
			actor, _, err := repos.Character.Load(context.Background(), q.Owner.CharacterID)
			if err != nil || actor.Stats["merchant_grant_marker"] != int64(tc.quantity) {
				t.Fatalf("actor=%+v err=%v", actor, err)
			}
			reg, err := Load(context.Background(), repos.Settings)
			if err != nil || reg.Guilds[q.GuildID].Essence.AP != 1000-tc.cost ||
				reg.Guilds[q.GuildID].Essence.GP != 20000 ||
				!reflect.DeepEqual(reg.MerchantPurchaseReceipts[merchantPurchaseKeyPrefix+q.RequestID], got) {
				t.Fatalf("registry=%+v err=%v", reg, err)
			}
		})
	}
}

func TestMerchantPurchaseReplayPrecedesCurrentShopLevelAndDoesNotRedeliverOrExtend(t *testing.T) {
	repos, q, catalog, now := merchantPurchaseFixture(t)
	ctx := context.Background()
	first, err := ApplyMerchantPurchase(ctx, repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, now)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	reg.Guilds[q.GuildID].Merchants = &MerchantState{Levels: [2]uint8{2, 2}}
	if err := Save(ctx, repos.Settings, reg, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	beforeSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
	beforeInventory, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
	beforeCharacter, _, _ := repos.Character.Load(ctx, q.Owner.CharacterID)

	callbackReplays := 0
	q.CanInteract = func(_ r.CharacterRecord, _ *Guild, replay bool) bool {
		if replay {
			callbackReplays++
		}
		return true
	}
	refuses := 0
	replay, err := ApplyMerchantPurchase(ctx, repos.Guild, q, catalog, func(context.Context, r.Group, *r.CharacterRecord, *r.InventoryRecord, MerchantProduct, uint32, time.Time) ([]string, error) {
		refuses++
		return nil, errors.New("replay granted a second item")
	}, now.Add(12*time.Hour))
	afterSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
	afterInventory, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
	afterCharacter, _, _ := repos.Character.Load(ctx, q.Owner.CharacterID)
	if err != nil || !reflect.DeepEqual(replay, first) || callbackReplays != 1 || refuses != 0 {
		t.Fatalf("replay=%+v callback=%d grant=%d err=%v", replay, callbackReplays, refuses, err)
	}
	if !reflect.DeepEqual(beforeSettings, afterSettings) ||
		!reflect.DeepEqual(beforeInventory, afterInventory) ||
		!reflect.DeepEqual(beforeCharacter, afterCharacter) {
		t.Fatal("replay rewrote assets or moved expiration")
	}

	for name, mutate := range map[string]func(*MerchantPurchaseRequest){
		"item":     func(q *MerchantPurchaseRequest) { q.ItemID = merchantPurchaseDefenceAlt },
		"quantity": func(q *MerchantPurchaseRequest) { q.Quantity++ },
		"shop":     func(q *MerchantPurchaseRequest) { q.ShopID = 211 },
		"npc":      func(q *MerchantPurchaseRequest) { q.NPCID = 411 },
	} {
		t.Run(name, func(t *testing.T) {
			conflict := q
			mutate(&conflict)
			if _, err := ApplyMerchantPurchase(ctx, repos.Guild, conflict, catalog, merchantPurchaseSuccessfulGrant, now); !errors.Is(err, ErrMerchantPurchaseIdentity) {
				t.Fatalf("changed body reused identity: %v", err)
			}
		})
	}
	again, err := ApplyMerchantPurchase(ctx, repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, now.Add(24*time.Hour))
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatalf("original receipt after conflicts=%+v err=%v", again, err)
	}
}

func TestMerchantPurchaseReplayStillChecksCurrentIdentityPermissionAndInteraction(t *testing.T) {
	repos, q, catalog, now := merchantPurchaseFixture(t)
	ctx := context.Background()
	first, err := ApplyMerchantPurchase(ctx, repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, now)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"permission", "deleted_character", "scene", "identity"} {
		t.Run(mode, func(t *testing.T) {
			request := q
			switch mode {
			case "permission":
				reg, _ := Load(ctx, repos.Settings)
				reg.Guilds[q.GuildID].GradePermissions = map[byte]uint32{1: 0, 4: 0}
				if err := Save(ctx, repos.Settings, reg, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			case "deleted_character":
				actor, _, _ := repos.Character.Load(ctx, q.Owner.CharacterID)
				actor.Stats["delete_flag"] = 1
				if err := repos.Character.Save(ctx, actor); err != nil {
					t.Fatal(err)
				}
			case "scene":
				request.CanInteract = func(r.CharacterRecord, *Guild, bool) bool { return false }
			case "identity":
				reg, _ := Load(ctx, repos.Settings)
				reg.Guilds[q.GuildID].GradePermissions = map[byte]uint32{1: PurchasePermission, 4: PurchasePermission}
				if err := Save(ctx, repos.Settings, reg, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				request.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
			}
			beforeSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
			beforeInventory, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
			got, err := ApplyMerchantPurchase(ctx, repos.Guild, request, catalog, merchantPurchaseSuccessfulGrant, now.Add(time.Hour))
			if err == nil || got.GuildID != 0 {
				t.Fatalf("replay bypassed current %s check: %+v %v", mode, got, err)
			}
			afterSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
			afterInventory, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
			if !reflect.DeepEqual(beforeSettings, afterSettings) {
				t.Fatalf("%s replay changed registry", mode)
			}
			if !reflect.DeepEqual(beforeInventory, afterInventory) {
				t.Fatalf("%s replay changed inventory", mode)
			}

			if mode == "permission" || mode == "identity" {
				reg, _ := Load(ctx, repos.Settings)
				reg.Guilds[q.GuildID].GradePermissions = map[byte]uint32{1: PurchasePermission, 4: PurchasePermission}
				if err := Save(ctx, repos.Settings, reg, now.Add(2*time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "deleted_character" {
				actor, _, _ := repos.Character.Load(ctx, q.Owner.CharacterID)
				actor.Stats["delete_flag"] = 0
				if err := repos.Character.Save(ctx, actor); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	beforeSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
	beforeInventory, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
	replayed, err := ApplyMerchantPurchase(ctx, repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, now.Add(2*time.Hour))
	afterSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
	afterInventory, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("authorized replay after rejection=%+v err=%v", replayed, err)
	}
	if !reflect.DeepEqual(beforeSettings, afterSettings) || !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatal("authorized replay changed receipt, inventory or AP")
	}
}

type merchantPurchaseFailRegistrySettings struct{ r.SettingsRepository }

func (merchantPurchaseFailRegistrySettings) Save(context.Context, r.SettingsRecord) error {
	return errors.New("merchant purchase registry save failure")
}

type merchantPurchaseInventoryFailUOW struct {
	base r.GuildUnitOfWork
}

func (u merchantPurchaseInventoryFailUOW) WithinGuild(ctx context.Context, owners []r.GuildOwner, apply func(r.Group) error) error {
	return u.base.WithinGuild(ctx, owners, apply)
}

func (u merchantPurchaseInventoryFailUOW) WithinGuildInventory(ctx context.Context, owner r.GuildOwner, apply func(r.Group) error) error {
	inventory, ok := u.base.(r.GuildInventoryUnitOfWork)
	if !ok {
		return r.ErrGuildTransaction
	}
	return inventory.WithinGuildInventory(ctx, owner, func(tx r.Group) error {
		tx.Settings = merchantPurchaseFailRegistrySettings{SettingsRepository: tx.Settings}
		return apply(tx)
	})
}

func TestMerchantPurchaseRejectsInvalidRulesAndRollsBackGrantAndRegistrySaveFailures(t *testing.T) {
	modes := []string{
		"nonexistent_item", "nonexistent_shop", "wrong_npc", "stale_level",
		"no_permission", "nonmember", "wrong_guild", "low_level", "over_quantity",
		"insufficient_ap", "cost_overflow", "full_bag", "grant_failure", "grant_invalid_slots",
		"grant_wrong_item", "grant_wrong_quantity", "grant_wrong_expiration",
		"grant_untracked_change",
		"deleted_character", "scene", "revalidate", "bad_request_id", "zero_item",
		"zero_quantity", "zero_time", "nil_uow", "nil_catalog", "nil_grant",
		"nil_revalidate", "nil_interact", "save_failure",
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			repos, q, catalog, now := merchantPurchaseFixture(t)
			ctx := context.Background()
			grant := MerchantPurchaseGrant(merchantPurchaseSuccessfulGrant)
			uow := repos.Guild
			reg, err := Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			g := reg.Guilds[q.GuildID]
			switch mode {
			case "nonexistent_item":
				q.ItemID = 999
			case "nonexistent_shop":
				q.ShopID, q.NPCID = 299, 499
			case "wrong_npc":
				q.NPCID = 411
			case "stale_level":
				g.Merchants = &MerchantState{Levels: [2]uint8{2, 1}}
			case "no_permission":
				g.GradePermissions = map[byte]uint32{1: 0, 4: 0}
			case "nonmember":
				q.Owner = r.GuildOwner{CharacterID: "2", AccountID: "a2"}
			case "wrong_guild":
				q.GuildID = 2
			case "low_level":
				actor, _, _ := repos.Character.Load(ctx, q.Owner.CharacterID)
				actor.Level = 0
				if err := repos.Character.Save(ctx, actor); err != nil {
					t.Fatal(err)
				}
			case "over_quantity":
				q.Quantity = 11
			case "insufficient_ap":
				g.Essence.AP = 1
			case "cost_overflow":
				g.Essence.AP = ^uint32(0)
				shop := catalog.Shops[201]
				product := shop.Products[merchantPurchaseDefenceItem]
				product.AP, product.StackLimit = ^uint32(0), 2
				shop.Products[merchantPurchaseDefenceItem] = product
				catalog.Shops[201] = shop
				q.Quantity = 2
			case "full_bag":
				grant = func(context.Context, r.Group, *r.CharacterRecord, *r.InventoryRecord, MerchantProduct, uint32, time.Time) ([]string, error) {
					return nil, ErrMerchantInventoryFull
				}
			case "grant_failure":
				grant = func(_ context.Context, _ r.Group, _ *r.CharacterRecord, inventory *r.InventoryRecord, product MerchantProduct, quantity uint32, expires time.Time) ([]string, error) {
					inventory.Slots["0:50"] = r.ItemStack{ItemID: int64(product.ItemID), Count: int64(quantity), ExpireAt: expires}
					return nil, errors.New("grant failed after mutating inventory")
				}
			case "grant_invalid_slots":
				grant = func(context.Context, r.Group, *r.CharacterRecord, *r.InventoryRecord, MerchantProduct, uint32, time.Time) ([]string, error) {
					return []string{"0:50", "0:50"}, nil
				}
			case "grant_wrong_item":
				grant = func(_ context.Context, _ r.Group, _ *r.CharacterRecord, inventory *r.InventoryRecord, _ MerchantProduct, quantity uint32, expires time.Time) ([]string, error) {
					inventory.Slots["0:50"] = r.ItemStack{ItemID: int64(merchantPurchaseDefenceAlt), Count: int64(quantity), ExpireAt: expires}
					return []string{"0:50"}, nil
				}
			case "grant_wrong_quantity":
				grant = func(_ context.Context, _ r.Group, _ *r.CharacterRecord, inventory *r.InventoryRecord, product MerchantProduct, quantity uint32, expires time.Time) ([]string, error) {
					inventory.Slots["0:50"] = r.ItemStack{ItemID: int64(product.ItemID), Count: int64(quantity + 1), ExpireAt: expires}
					return []string{"0:50"}, nil
				}
			case "grant_wrong_expiration":
				grant = func(_ context.Context, _ r.Group, _ *r.CharacterRecord, inventory *r.InventoryRecord, product MerchantProduct, quantity uint32, expires time.Time) ([]string, error) {
					inventory.Slots["0:50"] = r.ItemStack{ItemID: int64(product.ItemID), Count: int64(quantity), ExpireAt: expires.Add(time.Second)}
					return []string{"0:50"}, nil
				}
			case "grant_untracked_change":
				grant = func(_ context.Context, _ r.Group, _ *r.CharacterRecord, inventory *r.InventoryRecord, product MerchantProduct, quantity uint32, expires time.Time) ([]string, error) {
					inventory.Slots["0:50"] = r.ItemStack{ItemID: int64(product.ItemID), Count: int64(quantity), ExpireAt: expires}
					inventory.Slots["0:51"] = r.ItemStack{ItemID: int64(product.ItemID), Count: 1, ExpireAt: expires}
					return []string{"0:50"}, nil
				}
			case "deleted_character":
				actor, _, _ := repos.Character.Load(ctx, q.Owner.CharacterID)
				actor.Stats["delete_flag"] = 1
				if err := repos.Character.Save(ctx, actor); err != nil {
					t.Fatal(err)
				}
			case "scene":
				q.CanInteract = func(r.CharacterRecord, *Guild, bool) bool { return false }
			case "revalidate":
				q.Revalidate = func() bool { return false }
			case "bad_request_id":
				q.RequestID = "bad"
			case "zero_item":
				q.ItemID = 0
			case "zero_quantity":
				q.Quantity = 0
			case "zero_time":
				now = time.Time{}
			case "nil_uow":
				uow = nil
			case "nil_catalog":
				catalog = nil
			case "nil_grant":
				grant = nil
			case "nil_revalidate":
				q.Revalidate = nil
			case "nil_interact":
				q.CanInteract = nil
			case "save_failure":
				uow = merchantPurchaseInventoryFailUOW{base: uow}
			}
			if err := Save(ctx, repos.Settings, reg, now); err != nil && mode != "zero_time" {
				t.Fatal(err)
			}
			beforeSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
			beforeInventory, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
			beforeCharacter, _, _ := repos.Character.Load(ctx, q.Owner.CharacterID)
			got, err := ApplyMerchantPurchase(ctx, uow, q, catalog, grant, now)
			if err == nil || got.GuildID != 0 {
				t.Fatalf("accepted %s: %+v %v", mode, got, err)
			}
			afterSettings, _, _ := repos.Settings.Load(ctx, RegistryScope)
			afterInventory, _, _ := repos.Inventory.Load(ctx, q.Owner.CharacterID)
			afterCharacter, _, _ := repos.Character.Load(ctx, q.Owner.CharacterID)
			if !reflect.DeepEqual(beforeSettings, afterSettings) ||
				!reflect.DeepEqual(beforeInventory, afterInventory) ||
				!reflect.DeepEqual(beforeCharacter, afterCharacter) {
				t.Fatalf("%s failed request changed assets or receipt", mode)
			}
		})
	}
}

func TestMerchantPurchaseConcurrentLastBalanceAndSameRequestReplay(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_%v", same), func(t *testing.T) {
			repos, q, catalog, now := merchantPurchaseFixture(t)
			ctx := context.Background()
			reg, _ := Load(ctx, repos.Settings)
			reg.Guilds[q.GuildID].Essence.AP = 5
			reg.Guilds[q.GuildID].Merchants = &MerchantState{Levels: [2]uint8{1, 1}}
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			q.Quantity = 1
			var wg sync.WaitGroup
			results := make(chan error, 16)
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					request := q
					if !same {
						request.RequestID = fmt.Sprintf("%064x", i+1)
					}
					_, err := ApplyMerchantPurchase(ctx, repos.Guild, request, catalog, merchantPurchaseSuccessfulGrant, now)
					results <- err
				}(i)
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else if !errors.Is(err, ErrMerchantAP) {
					t.Fatalf("concurrent error=%v", err)
				}
			}
			wantSuccess, wantReceipts, wantCount, wantAP := 2, 2, int64(2), uint32(1)
			if same {
				wantSuccess, wantReceipts, wantCount, wantAP = 16, 1, 1, 3
			}
			reg, err := Load(ctx, repos.Settings)
			if err != nil || success != wantSuccess ||
				len(reg.MerchantPurchaseReceipts) != wantReceipts ||
				reg.Guilds[q.GuildID].Essence.AP != wantAP {
				t.Fatalf("success=%d registry=%+v err=%v", success, reg, err)
			}
			inventory, _, err := repos.Inventory.Load(ctx, q.Owner.CharacterID)
			if err != nil || inventory.Slots["0:50"].Count != wantCount {
				t.Fatalf("inventory=%+v err=%v", inventory, err)
			}
		})
	}
}

func TestMerchantPurchaseSixAMDeadlineDoesNotMoveOnPostBoundaryReplay(t *testing.T) {
	repos, q, catalog, _ := merchantPurchaseFixture(t)
	loc := time.FixedZone("CST", 8*60*60)
	before := time.Date(2026, 10, 25, 5, 59, 59, 0, loc)
	first, err := ApplyMerchantPurchase(context.Background(), repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, before)
	if err != nil {
		t.Fatal(err)
	}
	wantBoundary := time.Date(2026, 10, 25, 6, 0, 0, 0, loc).UTC()
	if !first.ExpireAt.Equal(wantBoundary) {
		t.Fatalf("expiry=%s want=%s", first.ExpireAt, wantBoundary)
	}
	replayed, err := ApplyMerchantPurchase(context.Background(), repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, before.Add(2*time.Second))
	if err != nil || !replayed.ExpireAt.Equal(wantBoundary) || !replayed.At.Equal(first.At) {
		t.Fatalf("post-boundary replay=%+v err=%v", replayed, err)
	}
	reg, err := Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	receipt := reg.MerchantPurchaseReceipts[merchantPurchaseKeyPrefix+q.RequestID]
	if receipt.Owner != first.Owner || receipt.GuildID != first.GuildID ||
		receipt.ItemID != first.ItemID || receipt.Quantity != first.Quantity ||
		receipt.ShopID != first.ShopID || receipt.NPCID != first.NPCID ||
		receipt.MerchantIndex != first.MerchantIndex || receipt.MerchantLevel != first.MerchantLevel ||
		receipt.CostAP != first.CostAP || receipt.BalanceAfter != first.BalanceAfter ||
		!receipt.ExpireAt.Equal(first.ExpireAt) || !receipt.At.Equal(first.At) ||
		!reflect.DeepEqual(receipt.Slots, first.Slots) {
		t.Fatalf("reloaded receipt=%+v first=%+v", receipt, first)
	}
}

func TestMerchantPurchaseReceiptValidationRejectsCorruptExpirationAndIdentity(t *testing.T) {
	repos, q, catalog, now := merchantPurchaseFixture(t)
	ctx := context.Background()
	if _, err := ApplyMerchantPurchase(ctx, repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, now); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	receipt := reg.MerchantPurchaseReceipts[merchantPurchaseKeyPrefix+q.RequestID]
	receipt.ExpireAt = receipt.ExpireAt.Add(time.Second)
	reg.MerchantPurchaseReceipts[merchantPurchaseKeyPrefix+q.RequestID] = receipt
	if err := Save(ctx, repos.Settings, reg, now.Add(time.Minute)); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("corrupt expiration accepted: %v", err)
	}
	reg.MerchantPurchaseReceipts[merchantPurchaseKeyPrefix+q.RequestID] = firstMerchantPurchaseReceipt(t, repos)
	receipt = reg.MerchantPurchaseReceipts[merchantPurchaseKeyPrefix+q.RequestID]
	receipt.NPCID = 411
	reg.MerchantPurchaseReceipts[merchantPurchaseKeyPrefix+q.RequestID] = receipt
	if err := Save(ctx, repos.Settings, reg, now.Add(2*time.Minute)); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("corrupt merchant identity accepted: %v", err)
	}
}

func TestMerchantPurchaseReceiptSurvivesGuildDisband(t *testing.T) {
	repos, q, catalog, now := merchantPurchaseFixture(t)
	ctx := context.Background()
	receipt, err := ApplyMerchantPurchase(ctx, repos.Guild, q, catalog, merchantPurchaseSuccessfulGrant, now)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.DepartMember(q.Owner, r.GuildOwner{CharacterID: "2", AccountID: "a2"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Disband(q.Owner, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, repos.Settings, reg, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	reg, err = Load(ctx, repos.Settings)
	if err != nil || len(reg.Guilds) != 0 ||
		!reflect.DeepEqual(reg.MerchantPurchaseReceipts[merchantPurchaseKeyPrefix+q.RequestID], receipt) {
		t.Fatalf("receipts after disband=%+v err=%v", reg.MerchantPurchaseReceipts, err)
	}
}

func firstMerchantPurchaseReceipt(t *testing.T, repos r.Group) MerchantPurchaseReceipt {
	t.Helper()
	reg, err := Load(context.Background(), repos.Settings)
	if err != nil || len(reg.MerchantPurchaseReceipts) != 1 {
		t.Fatalf("receipts=%+v err=%v", reg.MerchantPurchaseReceipts, err)
	}
	for _, receipt := range reg.MerchantPurchaseReceipts {
		return receipt
	}
	return MerchantPurchaseReceipt{}
}
