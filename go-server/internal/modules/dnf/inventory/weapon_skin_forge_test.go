package inventory

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

func TestBeiyunForgeRegistersOnlyTheUsingCharacter(t *testing.T) {
	ctx := context.Background()
	repos, actor, request, placement := seedBeiyunForge(t, ctx)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ForgeBeiyun(ctx, request, placement)
	if err != nil || result.ProductSlot != 65 || !reflect.DeepEqual(result.ChangedSlots, []int16{9, 121, 65}) {
		t.Fatalf("制作结果 %+v，错误 %v", result, err)
	}
	bag := loadTestInventory(t, ctx, repos, actor.CharacterID)
	product := bag.Slots[slotKey(listTypeMain, 65)]
	if bag.Slots[slotKey(listTypeMain, 9)].ItemID != 0 || bag.Slots[slotKey(listTypeMain, 121)].Count != 1 ||
		product.ItemID != weaponskin.BeiyunProductID || product.Count != 1 || !product.Bind {
		t.Fatalf("制作后背包 %+v", bag.Slots)
	}
	if _, err := owner.ForgeBeiyun(ctx, request, placement); !errors.Is(err, ErrWeaponSkinWeapon) {
		t.Fatalf("重复提交制作，错误 %v", err)
	}
	list := weaponskin.Command{CharacterID: actor.CharacterID, AccountID: actor.AccountID, Now: actor.Now, Current: actor.Current}
	before, err := weaponskin.Update(ctx, repos.CharacterAssets, list)
	if err != nil || before.Owned {
		t.Fatalf("使用前仓库 %+v，错误 %v", before, err)
	}
	if err := owner.UseBeiyunAppearance(ctx, actor, 65, placement); err != nil {
		t.Fatal(err)
	}
	after, err := weaponskin.Update(ctx, repos.CharacterAssets, list)
	if err != nil || !after.Owned || after.Selected != 0 || after.Revision != before.Revision+1 {
		t.Fatalf("使用后仓库 %+v，错误 %v", after, err)
	}
	if _, exists := loadTestInventory(t, ctx, repos, actor.CharacterID).Slots[slotKey(listTypeMain, 65)]; exists {
		t.Fatal("成功登记后仍有成品")
	}
	list.CharacterID = "78"
	other, err := weaponskin.Update(ctx, repos.CharacterAssets, list)
	if err != nil || other.Owned {
		t.Fatalf("同账号另一角色仓库 %+v，错误 %v", other, err)
	}
}

func TestBeiyunForgeRejectionsKeepMaterials(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*BeiyunForgeRequest, *dnfrepo.InventoryRecord)
		want   error
	}{
		{"背包已满", func(_ *BeiyunForgeRequest, bag *dnfrepo.InventoryRecord) {
			bag.Slots[slotKey(listTypeMain, 65)] = dnfrepo.ItemStack{ItemID: 1, Count: 1}
		}, ErrWeaponSkinSpace},
		{"确认期间换武器", func(_ *BeiyunForgeRequest, bag *dnfrepo.InventoryRecord) {
			key := slotKey(listTypeMain, 9)
			weapon := bag.Slots[key]
			weapon.Extra = map[string]string{"reinforce": "10"}
			bag.Slots[key] = weapon
		}, ErrWeaponSkinWeapon},
		{"模具已过期", func(req *BeiyunForgeRequest, bag *dnfrepo.InventoryRecord) {
			key := slotKey(listTypeMain, 121)
			mold := bag.Slots[key]
			mold.ExpireAt = req.Now
			bag.Slots[key] = mold
		}, ErrWeaponSkinMold},
		{"账号不匹配", func(req *BeiyunForgeRequest, _ *dnfrepo.InventoryRecord) {
			req.AccountID = "91"
		}, weaponskin.ErrCharacter},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos, actor, request, placement := seedBeiyunForge(t, ctx)
			bag := loadTestInventory(t, ctx, repos, actor.CharacterID)
			test.change(&request, &bag)
			saveTestInventory(t, ctx, repos, bag)
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ForgeBeiyun(ctx, request, placement); !errors.Is(err, test.want) {
				t.Fatalf("错误 %v，预期 %v", err, test.want)
			}
			if actual := loadTestInventory(t, ctx, repos, actor.CharacterID); !reflect.DeepEqual(bag, actual) {
				t.Fatalf("拒绝制作后背包发生变化：%+v", actual.Slots)
			}
		})
	}
}

func TestBeiyunForgeRegistersCatalogMailProducts(t *testing.T) {
	const magicProduct = 10309604
	appearance, ok := weaponskin.FromProduct(magicProduct, weaponskin.MagicRecipe)
	if !ok {
		t.Fatal("缺少现场测试成品")
	}
	for _, tc := range []struct {
		name   string
		recipe weaponskin.BeiyunRecipe
		legacy bool
		edit   func(*dnfrepo.ItemStack)
		want   error
	}{
		{name: "邮件神奇成品", recipe: weaponskin.MagicRecipe},
		{name: "邮件秘传成品", recipe: weaponskin.SecretRecipe},
		{name: "制作标记正确", edit: func(item *dnfrepo.ItemStack) {
			item.Extra[weaponskin.ProductSkinKey] = "101690604"
		}},
		{name: "明确的标记冲突", edit: func(item *dnfrepo.ItemStack) {
			item.Extra[weaponskin.ProductSkinKey] = "101690603"
		}, want: ErrWeaponSkinItem},
		{name: "路径不符", edit: func(item *dnfrepo.ItemStack) {
			item.Extra["pvf_path"] = "stackable/weaponskinforge/10309603.stk"
		}, want: ErrWeaponSkinItem},
		{name: "物品类型不符", edit: func(item *dnfrepo.ItemStack) {
			item.Extra["stackable_type"] = "[etc]"
		}, want: ErrWeaponSkinItem},
		{name: "缺少服务端身份", edit: func(item *dnfrepo.ItemStack) {
			item.Extra = nil
		}, want: ErrWeaponSkinItem},
		{name: "背包编号不符", edit: func(item *dnfrepo.ItemStack) {
			item.ItemID--
		}, want: ErrWeaponSkinItem},
		{name: "旧式成品仍需标记", legacy: true, want: ErrWeaponSkinItem},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos, actor, _, placement := seedBeiyunForge(t, ctx)
			if !tc.legacy {
				placement.Appearance = appearance
			}
			placement.Recipe = tc.recipe
			items := placement.Target().Items(tc.recipe)
			product := dnfrepo.ItemStack{
				ItemID: int64(items.ProductID), Count: 1,
				Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[waste]", "pvf_path": items.ProductPath},
			}
			if tc.edit != nil {
				tc.edit(&product)
			}
			key := slotKey(listTypeMain, 65)
			before := loadTestInventory(t, ctx, repos, actor.CharacterID)
			before.Slots[key] = product
			saveTestInventory(t, ctx, repos, before)
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.UseBeiyunAppearance(ctx, actor, 65, placement); !errors.Is(err, tc.want) {
				t.Fatalf("登记错误 %v，预期 %v", err, tc.want)
			}
			after := loadTestInventory(t, ctx, repos, actor.CharacterID)
			character, found, err := repos.Character.Load(ctx, actor.CharacterID)
			if err != nil || !found {
				t.Fatalf("读取登记状态失败：%v", err)
			}
			if tc.want != nil {
				if !reflect.DeepEqual(before, after) || weaponskin.Registered(character, placement.Target()) {
					t.Fatal("拒绝登记后发生资产变化")
				}
			} else if _, remains := after.Slots[key]; remains || !weaponskin.Registered(character, placement.Target()) {
				t.Fatal("合法成品没有消费并登记")
			}
		})
	}
}

func TestBeiyunForgeRegisteredBeforeConfirmationKeepsMaterials(t *testing.T) {
	ctx := context.Background()
	repos, actor, request, placement := seedBeiyunForge(t, ctx)
	bag := loadTestInventory(t, ctx, repos, actor.CharacterID)
	character, found, err := repos.Character.Load(ctx, actor.CharacterID)
	if err != nil || !found {
		t.Fatalf("读取角色失败，存在 %t，错误 %v", found, err)
	}
	character, err = weaponskin.RegisterBeiyun(character, actor.Now)
	if err != nil {
		t.Fatal(err)
	}
	saveTestCharacter(t, ctx, repos, character)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ForgeBeiyun(ctx, request, placement); !errors.Is(err, weaponskin.ErrAlreadyRegistered) {
		t.Fatalf("已登记角色继续制作，错误 %v", err)
	}
	if actual := loadTestInventory(t, ctx, repos, actor.CharacterID); !reflect.DeepEqual(bag, actual) {
		t.Fatalf("重复制作检查改变了背包：%+v", actual.Slots)
	}
}

func seedBeiyunForge(t *testing.T, ctx context.Context) (dnfrepo.Group, WeaponSkinActor, BeiyunForgeRequest, BeiyunForgePlacement) {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	actor := WeaponSkinActor{CharacterID: "77", AccountID: "90", Now: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), Current: func() bool { return true }}
	for _, id := range []string{actor.CharacterID, "78"} {
		saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: id, AccountID: actor.AccountID, Job: "0", Level: 90})
	}
	weapon := dnfrepo.ItemStack{ItemID: weaponskin.BeiyunWeaponID, Count: 1}
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: actor.CharacterID, Slots: map[string]dnfrepo.ItemStack{
		slotKey(listTypeMain, 9):   weapon,
		slotKey(listTypeMain, 121): {ItemID: weaponskin.MagicMoldID, Count: 2},
	}})
	request := BeiyunForgeRequest{WeaponSkinActor: actor, WeaponSlot: 9, MoldSlot: 121, ExpectedWeapon: weapon}
	placement := BeiyunForgePlacement{
		Weapon:  itemplacement.SlotPolicy{ListType: listTypeMain, Start: 9, End: 9},
		Mold:    itemplacement.SlotPolicy{ListType: listTypeMain, Start: 121, End: 121},
		Product: itemplacement.SlotPolicy{ListType: listTypeMain, Start: 65, End: 65},
	}
	return repos, actor, request, placement
}
