package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/weaponskin"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestWeaponSkinForgeSelectionRejectsLegacyRegistration(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	actor := dnfinventory.WeaponSkinActor{CharacterID: "1", AccountID: "dnf:1", Now: time.Now(), Current: func() bool { return true }}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: actor.CharacterID, AccountID: actor.AccountID, Job: "0", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: actor.CharacterID, Slots: map[string]dnfrepo.ItemStack{
		"1:28": {ItemID: weaponskin.SampleID, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	state, err := weaponskin.Update(ctx, repos.CharacterAssets, weaponskin.Command{
		CharacterID: actor.CharacterID, AccountID: actor.AccountID, Now: actor.Now, Current: actor.Current, Action: weaponskin.Apply,
	})
	if err != nil || !state.Owned {
		t.Fatalf("旧版外观应用失败：%+v，错误 %v", state, err)
	}
	selection, err := prepareBeiyunForgeSelection(ctx, repos, actor, dnfinventory.BeiyunForgePlacement{}, forgeNoSlot, false)
	if !errors.Is(err, weaponskin.ErrAlreadyRegistered) || weaponSkinForgeStatus(err) != 7 || selection.token != 0 {
		t.Fatalf("重复制作仍可进入确认，选择 %+v，错误 %v", selection, err)
	}
}

func TestWeaponSkinForgeExactSelectionResolvesOnlyRequestedWeapon(t *testing.T) {
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	actor := dnfinventory.WeaponSkinActor{CharacterID: "1", AccountID: "dnf:1", Now: time.Now(), Current: func() bool { return true }}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: actor.CharacterID, AccountID: actor.AccountID, Job: "0"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: actor.CharacterID, Slots: map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 27000, Count: 1}, "0:10": {ItemID: 27001, Count: 1},
		"0:121": {ItemID: weaponskin.MagicMoldID, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	appearance, ok := weaponskin.FromSource(27000)
	if !ok {
		t.Fatal("测试武器缺少目录条目")
	}
	placement := dnfinventory.BeiyunForgePlacement{
		Appearance: appearance,
		Weapon:     itemplacement.SlotPolicy{Start: 9, End: 64},
		Mold:       itemplacement.SlotPolicy{Start: 121, End: 176},
	}
	var resolved []int64
	backpack, err := inspectWeaponSkinForgeBackpack(ctx, repos, actor, placement, true, 9, func(itemID int64) (dnfinventory.BeiyunForgePlacement, bool) {
		resolved = append(resolved, itemID)
		return placement, itemID == 27000
	})
	if err != nil || len(resolved) != 1 || resolved[0] != 27000 {
		t.Fatalf("精确选材应只解析所点武器：%v，错误 %v", resolved, err)
	}
	selected, err := pickWeaponSkinForgeSlot(backpack, weaponskin.MagicRecipe, 9, true)
	if err != nil || selected.weaponSlot != 9 || selected.moldSlot != 121 {
		t.Fatalf("精确选材未保留正确的武器与模具槽：%+v，错误 %v", selected, err)
	}
}

func TestWeaponSkinForgeDecode(t *testing.T) {
	body := make([]byte, 24)
	copy(body, "WSF1")
	binary.LittleEndian.PutUint32(body[4:8], 9)
	binary.LittleEndian.PutUint32(body[8:12], 11)
	body[12] = forgeConfirm
	binary.LittleEndian.PutUint16(body[14:16], 20)
	binary.LittleEndian.PutUint64(body[16:24], 0xaabbccdd)
	r, ok := decodeWeaponSkinForge(body)
	if !ok || r != (weaponSkinForgeRequest{id: 9, epoch: 11, action: forgeConfirm, slot: 20, token: 0xaabbccdd}) {
		t.Fatalf("请求解析错误：%+v, %t", r, ok)
	}
	for _, action := range []byte{forgeSelect, forgeRegister, 255} {
		body[12] = action
		if _, ok := decodeWeaponSkinForge(body); ok {
			t.Fatalf("错误接受带确认令牌的动作 %d", action)
		}
	}
	body[12] = forgeConfirm
	binary.LittleEndian.PutUint64(body[16:24], 0)
	if _, ok := decodeWeaponSkinForge(body); ok {
		t.Fatal("没有确认令牌的制作请求被接受")
	}
	body[12] = forgeSelect
	binary.LittleEndian.PutUint16(body[14:16], 0xffff)
	if r, ok := decodeWeaponSkinForge(body); !ok || r.slot != forgeNoSlot {
		t.Fatalf("首个背包选择请求无效：%+v", r)
	}
	body[12] = forgeRegister
	if _, ok := decodeWeaponSkinForge(body); ok {
		t.Fatal("武器外观登记缺少有效的成品槽位")
	}
	binary.LittleEndian.PutUint16(body[14:16], 65)
	if r, ok := decodeWeaponSkinForge(body); !ok || r != (weaponSkinForgeRequest{id: 9, epoch: 11, action: forgeRegister, slot: 65}) {
		t.Fatalf("武器外观登记解析错误：%+v, %t", r, ok)
	}
	body[12] = forgePickSlot
	if _, ok := decodeWeaponSkinForge(body); ok {
		t.Fatal("精确选材错误接受了消耗品格")
	}
	binary.LittleEndian.PutUint16(body[14:16], 64)
	if r, ok := decodeWeaponSkinForge(body); !ok || r.slot != 64 {
		t.Fatalf("精确选材拒绝了装备页末格：%+v, %t", r, ok)
	}
	body[12] = forgeInventory
	if _, ok := decodeWeaponSkinForge(body); ok {
		t.Fatal("背包灰显查询错误地接受了选中槽位")
	}
	binary.LittleEndian.PutUint16(body[14:16], 0xffff)
	if r, ok := decodeWeaponSkinForge(body); !ok || r.slot != forgeNoSlot {
		t.Fatalf("背包灰显查询解析错误：%+v, %t", r, ok)
	}
	body[13] = byte(weaponskin.SecretRecipe)
	if r, ok := decodeWeaponSkinForge(body); !ok || r.recipe != weaponskin.SecretRecipe {
		t.Fatalf("秘传模具查询解析错误：%+v, %t", r, ok)
	}
	body[13]++
	if _, ok := decodeWeaponSkinForge(body); ok {
		t.Fatal("接受了未知配方")
	}
}

func TestWeaponSkinForgeRealPVF(t *testing.T) {
	filename := os.Getenv("LINUS_FORGE_TEST_PVF")
	if filename == "" {
		t.Skip("需要林纳斯配套候选 PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: filename})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	placement, ok := resolveBeiyunForgePlacement(catalog, weaponskin.MagicRecipe)
	if !ok {
		weapon, weaponErr := catalog.ResolveItem(weaponskin.BeiyunWeaponID)
		mold, moldErr := catalog.ResolveItem(weaponskin.MagicMoldID)
		product, productErr := catalog.ResolveItem(weaponskin.BeiyunProductID)
		t.Fatalf("配套无效，武器 %+v/%v，模具 %+v/%v，成品 %+v/%v", weapon, weaponErr, mold, moldErr, product, productErr)
	}
	if !placement.Weapon.Contains(dnfrepo.MainInventoryListType, 9) ||
		!placement.Mold.Contains(dnfrepo.MainInventoryListType, 121) ||
		!placement.Product.Contains(dnfrepo.MainInventoryListType, 65) {
		t.Fatalf("真实背包分类异常：%+v", placement)
	}
	npcs, err := currentNPCProductionList(archive, "npc/npc.lst")
	if err != nil {
		t.Fatal(err)
	}
	npc, err := currentNPCProductionDocument(archive, "npc/npc.lst", npcs[weaponSkinForgeNPC])
	if err != nil {
		t.Fatal(err)
	}
	role := npc.Texts("role")
	web := npc.Texts("web page")
	if len(role) != 2 || role[0] != "[item shop]" || role[1] != "[web page popup window]" ||
		len(web) != 3 || web[0] != "about:blank#dnf90-linus-weapon-skin" {
		t.Fatalf("林纳斯角色或入口异常：%q %q", role, web)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	actor := dnfinventory.WeaponSkinActor{CharacterID: "1", AccountID: "dnf:1", Now: time.Now(), Current: func() bool { return true }}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: actor.CharacterID, AccountID: actor.AccountID, Job: "0", Level: 90}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: actor.CharacterID, Slots: map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: weaponskin.BeiyunWeaponID, Count: 1}, "0:12": {ItemID: weaponskin.BeiyunWeaponID, Count: 1},
		"0:121": {ItemID: weaponskin.MagicMoldID, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	selected, err := prepareBeiyunForgeSelection(ctx, repos, actor, placement, 9, false)
	if err != nil || selected.weaponSlot != 12 || selected.weapon.ItemID != weaponskin.BeiyunWeaponID || selected.moldSlot != 121 {
		t.Fatalf("切换到下一把武器失败：%+v，错误 %v", selected, err)
	}
	exact, err := prepareBeiyunForgeSelection(ctx, repos, actor, placement, 9, true)
	if err != nil || exact.weaponSlot != 9 {
		t.Fatalf("没有按点击的真实槽位选择：%+v，错误 %v", exact, err)
	}
	if _, err := prepareBeiyunForgeSelection(ctx, repos, actor, placement, 10, true); !errors.Is(err, dnfinventory.ErrWeaponSkinWeapon) {
		t.Fatalf("空槽应被拒绝，实际错误 %v", err)
	}
	owner, err := dnfinventory.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ForgeBeiyun(ctx, dnfinventory.BeiyunForgeRequest{
		WeaponSkinActor: actor, WeaponSlot: selected.weaponSlot, MoldSlot: selected.moldSlot, ExpectedWeapon: selected.weapon,
	}, placement)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.UseBeiyunAppearance(ctx, actor, result.ProductSlot, placement); err != nil {
		t.Fatal(err)
	}
	state, err := weaponskin.Update(ctx, repos.CharacterAssets, weaponskin.Command{
		CharacterID: actor.CharacterID, AccountID: actor.AccountID, Now: actor.Now, Current: actor.Current,
	})
	if err != nil || !state.Owned || state.Selected != 0 {
		t.Fatalf("制作后登记的角色仓库状态：%+v，错误 %v", state, err)
	}
}

func TestWeaponSkinCatalogRealPVF(t *testing.T) {
	filename := os.Getenv("LINUS_CATALOG_TEST_PVF")
	if filename == "" {
		t.Skip("需要多武器配套候选 PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: filename})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	actor := dnfinventory.WeaponSkinActor{CharacterID: "1", AccountID: "dnf:1", Now: time.Now(), Current: func() bool { return true }}
	character := dnfrepo.CharacterRecord{CharacterID: actor.CharacterID, AccountID: actor.AccountID, Job: "0", Level: 90}
	character, err = weaponskin.RegisterBeiyun(character, actor.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	other := dnfrepo.CharacterRecord{CharacterID: "2", AccountID: actor.AccountID, Job: "0", Level: 90}
	if err := repos.Character.Save(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: actor.CharacterID, Slots: map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 27098, Count: 1}, "0:10": {ItemID: 101040146, Count: 1},
		"0:11": {ItemID: 102030192, Count: 1}, "0:12": {ItemID: weaponskin.BeiyunWeaponID, Count: 1},
		"0:121": {ItemID: weaponskin.MagicMoldID, Count: 1}, "0:122": {ItemID: weaponskin.SecretMoldID, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfinventory.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	var previous dnfinventory.BeiyunForgePlacement
	trialRows := weaponskin.Catalog()[:3]
	for i, a := range trialRows[1:] {
		recipe := weaponskin.BeiyunRecipe(i)
		base, ok := resolveBeiyunForgePlacement(catalog, recipe)
		if !ok {
			t.Fatal("现用别云剑配套失效")
		}
		resolve := func(id int64) (dnfinventory.BeiyunForgePlacement, bool) {
			look, ok := weaponskin.FromSource(uint32(id))
			if !ok {
				return dnfinventory.BeiyunForgePlacement{}, false
			}
			return resolveWeaponSkinForgePlacement(catalog, recipe, look)
		}
		pack, err := inspectWeaponSkinForgeBackpack(ctx, repos, actor, base, true, forgeNoSlot, resolve)
		if err != nil || pack.allowed != uint64((3-i)|4)<<8 || pack.registered != 1<<11 {
			t.Fatalf("配方 %d 装备页允许掩码=%#x，已登记掩码=%#x，错误 %v", recipe, pack.allowed, pack.registered, err)
		}
		if selection, err := pickWeaponSkinForgeSlot(pack, recipe, 11, true); err != nil || selection.weapon.ItemID != 102030192 {
			t.Fatalf("鬼剑士未能选中格斗家拳套：%+v，错误 %v", selection, err)
		}
		selection, err := pickWeaponSkinForgeSlot(pack, recipe, int16(9+i), true)
		if err != nil || selection.weapon.ItemID != int64(a.SourceID) {
			t.Fatalf("武器未按槽位选中：%+v / %v", selection, err)
		}
		placement, ok := resolve(selection.weapon.ItemID)
		if !ok {
			t.Fatalf("候选武器资源不完整：%+v", a)
		}
		created, err := owner.ForgeBeiyun(ctx, dnfinventory.BeiyunForgeRequest{
			WeaponSkinActor: actor, WeaponSlot: selection.weaponSlot, MoldSlot: selection.moldSlot, ExpectedWeapon: selection.weapon,
		}, placement)
		if err != nil {
			t.Fatal(err)
		}
		bag, _, err := repos.Inventory.Load(ctx, actor.CharacterID)
		if err != nil {
			t.Fatal(err)
		}
		made := bag.Slots[currentSocketInventoryKey(dnfrepo.MainInventoryListType, created.ProductSlot)]
		if made.ItemID != int64(a.Products[recipe]) || made.Bind != (recipe == weaponskin.MagicRecipe) {
			t.Fatalf("成品或绑定类型错误：%+v", made)
		}
		if i == 0 {
			previous = placement
		} else if err := owner.UseBeiyunAppearance(ctx, actor, created.ProductSlot, previous); !errors.Is(err, dnfinventory.ErrWeaponSkinItem) {
			t.Fatalf("另一外观成品错误登记：%v", err)
		}
		if err := owner.UseBeiyunAppearance(ctx, actor, created.ProductSlot, placement); err != nil {
			t.Fatal(err)
		}
	}
	cmd := weaponskin.Command{CharacterID: "1", AccountID: actor.AccountID, Now: actor.Now, Current: actor.Current,
		Available: func(row weaponskin.Appearance) bool { return weaponSkinAssetReady(catalog, row) }}
	state, err := weaponskin.Update(ctx, repos.CharacterAssets, cmd)
	flags := make([]byte, (len(weaponskin.Catalog())+7)/8)
	flags[0] = 7
	if err != nil || state.Unlocked != string(flags) || state.Selected != 0 {
		t.Fatalf("三种外观登记查询错误：%+v / %v", state, err)
	}
	for _, a := range trialRows {
		cmd.Action, cmd.Appearance, cmd.Revision = weaponskin.Apply, a.ID, state.Revision
		state, err = weaponskin.Update(ctx, repos.CharacterAssets, cmd)
		if err != nil || state.Unlocked != string(flags) || state.Selected != a.ID {
			t.Fatalf("切换武器外观失败：%+v / %v", state, err)
		}
	}
	cmd.CharacterID, cmd.Action, cmd.Appearance, cmd.Revision = "2", weaponskin.List, 0, 0
	state, err = weaponskin.Update(ctx, repos.CharacterAssets, cmd)
	flags[0] = 0
	if err != nil || state.Unlocked != string(flags) || state.Selected != 0 {
		t.Fatalf("同账号另一角色看到新外观：%+v / %v", state, err)
	}
}

func TestWeaponSkinForgeAllRaritiesRealPVF(t *testing.T) {
	path := os.Getenv("LINUS_CATALOG_TEST_PVF")
	if path == "" {
		t.Skip("需要本机全量幻化 PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	type grade struct {
		count, otherJob int
		sample          weaponskin.Appearance
		name            string
	}
	var grades [7]grade
	var rejected [7][2]int
	now := time.Now()
	for _, a := range weaponskin.Catalog() {
		weapon, err := catalog.ResolveItem(a.SourceID)
		if err != nil || weapon.Rarity < 0 || weapon.Rarity >= int64(len(grades)) {
			t.Fatalf("武器 %d 的品级定义无效：%d，错误 %v", a.SourceID, weapon.Rarity, err)
		}
		for recipe := weaponskin.MagicRecipe; recipe <= weaponskin.SecretRecipe; recipe++ {
			placement, ok := resolveWeaponSkinForgePlacement(catalog, recipe, a)
			if ok && placement.AllowsWeapon(nil, dnfrepo.MainInventoryListType, 9,
				dnfrepo.ItemStack{ItemID: int64(a.SourceID), Count: 1}, now) {
				continue
			}
			rejected[weapon.Rarity][recipe]++
			if rejected[weapon.Rarity][recipe] <= 5 {
				items := a.Items(recipe)
				product, productErr := catalog.ResolveItem(items.ProductID)
				t.Logf("品质 %d 武器 %d 配方 %d 不可选：源 %q（目录 %q），外观有效 %t，成品 %q（目录 %q） %s %s %s，成品读取错误 %v，配方已解析 %t",
					weapon.Rarity, a.SourceID, recipe, weapon.PVFPath, a.SourcePath, weaponSkinAssetReady(catalog, a),
					product.PVFPath, items.ProductPath, product.StackableType, product.ActionType, product.AttachType, productErr, ok)
			}
		}
		row := &grades[weapon.Rarity]
		row.count++
		if !a.Allows("0") {
			row.otherJob++
		}
		if row.sample.ID == 0 && a.OwnerJobs != 0xffff {
			row.sample, row.name = a, weapon.Name
		}
	}
	for rarity, row := range grades {
		if row.sample.ID == 0 {
			t.Fatalf("没有品质 %d 的跨职业实档样本", rarity)
		}
		if rejected[rarity] != [2]int{} {
			t.Errorf("[rarity] %d：目录 %d 件，神奇模具拒绝 %d 件，秘传模具拒绝 %d 件",
				rarity, row.count, rejected[rarity][weaponskin.MagicRecipe], rejected[rarity][weaponskin.SecretRecipe])
		}
		t.Logf("[rarity] %d：目录 %d 件，原职业条件会拦截男鬼剑士的其中 %d 件。样本 %s（%d）",
			rarity, row.count, row.otherJob, row.name, row.sample.SourceID)
		for recipe := weaponskin.MagicRecipe; recipe <= weaponskin.SecretRecipe; recipe++ {
			t.Run(fmt.Sprintf("品质%d-配方%d", rarity, recipe), func(t *testing.T) {
				verifyCrossJobForgeRecipe(t, catalog, row.sample, recipe)
			})
		}
	}
}

func verifyCrossJobForgeRecipe(t *testing.T, catalog *pvfDungeonDropCatalog, a weaponskin.Appearance, recipe weaponskin.BeiyunRecipe) {
	t.Helper()
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	actor := dnfinventory.WeaponSkinActor{CharacterID: "1", AccountID: "dnf:1", Now: time.Now(), Current: func() bool { return true }}
	job := "0"
	for n := 1; a.Allows(job); n++ {
		job = strconv.Itoa(n)
	}
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: actor.CharacterID, AccountID: actor.AccountID, Job: job, Level: 90}); err != nil {
		t.Fatal(err)
	}
	weapon := dnfrepo.ItemStack{ItemID: int64(a.SourceID), Count: 1}
	items := a.Items(recipe)
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: actor.CharacterID, Slots: map[string]dnfrepo.ItemStack{
		"0:9": weapon, "0:121": {ItemID: int64(items.MoldID), Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	placement, ok := resolveWeaponSkinForgePlacement(catalog, recipe, a)
	if !ok {
		t.Fatal("真实配方未解析")
	}
	backpack, err := inspectWeaponSkinForgeBackpack(ctx, repos, actor, placement, true, forgeNoSlot, func(id int64) (dnfinventory.BeiyunForgePlacement, bool) {
		return placement, id == weapon.ItemID
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := pickWeaponSkinForgeSlot(backpack, recipe, 9, true)
	if err != nil || selection.weapon.ItemID != weapon.ItemID || selection.moldSlot != 121 {
		t.Fatalf("职业 %s 未能选中武器 %d 或配方模具：%+v，%v", job, a.SourceID, selection, err)
	}
	owner, err := dnfinventory.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	req := dnfinventory.BeiyunForgeRequest{WeaponSkinActor: actor, WeaponSlot: selection.weaponSlot,
		MoldSlot: selection.moldSlot, ExpectedWeapon: selection.weapon}
	if _, err := owner.ForgeBeiyun(ctx, req, placement); !errors.Is(err, weaponskin.ErrCharacter) {
		t.Fatalf("旧入口未保留职业限制：%v", err)
	}
	req.AllowCrossJob = true
	created, err := owner.ForgeBeiyun(ctx, req, placement)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.UseBeiyunAppearance(ctx, actor, created.ProductSlot, placement); !errors.Is(err, weaponskin.ErrCharacter) {
		t.Fatalf("制作角色用不适配的外观入库：%v", err)
	}
	bag, found, err := repos.Inventory.Load(ctx, actor.CharacterID)
	product := bag.Slots[currentSocketInventoryKey(dnfrepo.MainInventoryListType, created.ProductSlot)]
	if err != nil || !found || bag.Slots["0:9"].Count != 0 || bag.Slots["0:121"].Count != 0 ||
		product.Count != 1 || product.ItemID != int64(items.ProductID) || product.Bind != (recipe == weaponskin.MagicRecipe) {
		t.Fatalf("跨职业制作、扣料或拒绝入库后成品不符：武器 %d，配方 %d，%+v，%v", a.SourceID, recipe, product, err)
	}
}

func TestWeaponSkinForgeSecretRealPVF(t *testing.T) {
	filename := os.Getenv("LINUS_SECRET_TEST_PVF")
	if filename == "" {
		t.Skip("需要秘传模具候选 PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: filename})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	secret, ok := resolveBeiyunForgePlacement(catalog, weaponskin.SecretRecipe)
	if !ok {
		t.Fatal("真实 PVF 缺少配套的秘传模具或账号绑定成品")
	}
	magic, ok := resolveBeiyunForgePlacement(catalog, weaponskin.MagicRecipe)
	if !ok {
		t.Fatal("原有神奇配方不能解析")
	}
	definition, err := catalog.ResolveItem(weaponskin.AccountProductID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	actor := dnfinventory.WeaponSkinActor{CharacterID: "21", AccountID: "dnf:1", Now: time.Now(), Current: func() bool { return true }}
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: actor.AccountID, Metadata: map[string]string{"account_cargo_level": "8"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"21", "22"} {
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: id, AccountID: actor.AccountID, Job: "0", Level: 90}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: id, Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
			t.Fatal(err)
		}
	}
	weapon := dnfrepo.ItemStack{ItemID: weaponskin.BeiyunWeaponID, Count: 1}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: actor.CharacterID, Slots: map[string]dnfrepo.ItemStack{
		"0:9": weapon, "0:121": {ItemID: weaponskin.SecretMoldID, Count: 2},
	}}); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfinventory.NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	req := dnfinventory.BeiyunForgeRequest{WeaponSkinActor: actor, WeaponSlot: 9, MoldSlot: 121, ExpectedWeapon: weapon}
	if _, err := owner.ForgeBeiyun(ctx, req, magic); !errors.Is(err, dnfinventory.ErrWeaponSkinMold) {
		t.Fatalf("神奇配方错用了秘传模具：%v", err)
	}
	result, err := owner.ForgeBeiyun(ctx, req, secret)
	if err != nil {
		t.Fatal(err)
	}
	bag, _, err := repos.Inventory.Load(ctx, actor.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	product := bag.Slots[currentSocketInventoryKey(dnfrepo.MainInventoryListType, result.ProductSlot)]
	if bag.Slots["0:9"].Count != 0 || bag.Slots["0:121"].Count != 1 ||
		product.ItemID != weaponskin.AccountProductID || product.Count != 1 || product.Bind {
		t.Fatalf("秘传配方发放或扣料异常：%+v", bag.Slots)
	}
	if err := validateCurrentCargoInsert(product, 12, definition, actor.Now); err != nil {
		t.Fatalf("秘传成品不能存入账号金库：%v", err)
	}
	for _, command := range []dnfinventory.Command{
		{AccountID: actor.AccountID, SelectedCharacterID: 21, SourceListType: 0,
			SourceSlotIndex: result.ProductSlot, DestinationListType: 12, DestinationSlotIndex: 0, MoveCount: 1},
		{AccountID: actor.AccountID, SelectedCharacterID: 22, SourceListType: 12,
			SourceSlotIndex: 0, DestinationListType: 0, DestinationSlotIndex: 65, MoveCount: 1},
	} {
		if move, err := owner.Move(ctx, command); err != nil || !move.Changed {
			t.Fatalf("账号金库转移失败：%+v，%v", move, err)
		}
	}
	actor.CharacterID = "22"
	if err := owner.UseBeiyunAppearance(ctx, actor, 65, secret); err != nil {
		t.Fatalf("同账号角色取出后登记失败：%v", err)
	}
	for _, id := range []string{"21", "22"} {
		ch, found, err := repos.Character.Load(ctx, id)
		if err != nil || !found || weaponskin.BeiyunRegistered(ch) != (id == "22") {
			t.Fatalf("角色 %s 登记状态异常：%+v，%t，%v", id, ch.Stats, found, err)
		}
	}
}

func TestWeaponSkinForgeRegistrationThroughGameplay(t *testing.T) {
	product := dnfrepo.ItemStack{
		ItemID: weaponskin.BeiyunProductID, Count: 1,
		Extra: map[string]string{weaponskin.ProductSkinKey: "101609901"},
	}
	service, session, conn := newEmptySlotRowTestServiceWithStack(t, "0:65", product)
	repos, _ := service.repositoryProvider()
	character := dnfrepo.CharacterRecord{CharacterID: "21", AccountID: session.accountID, Job: "0", Level: 90}
	other := character
	other.CharacterID = "22"
	for _, record := range []dnfrepo.CharacterRecord{character, other} {
		if err := repos.Character.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	session.ctx, session.accountFromToken = ctx, true
	service.gameSessions = map[uint16]*gameSession{21: session}
	if err := service.startGameSessionEvents(ctx, session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		stop, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = service.stopGameSessionEvents(stop, session)
	})
	service.options.currentClientVersion = "official-24.9"
	service.pvfItemCatalog = &pvfDungeonDropCatalog{
		source: bridgePVFSource{},
		itemCache: map[uint32]dungeonDropItemDefinition{
			weaponskin.BeiyunWeaponID: {Kind: dungeonDropItemEquipment, PVFPath: weaponskin.BeiyunWeaponPath, EquipmentType: "[weapon]"},
			weaponskin.SampleID:       {Kind: dungeonDropItemEquipment, PVFPath: weaponskin.SamplePath, EquipmentType: "[weapon avatar]"},
			weaponskin.MagicMoldID:    {Kind: dungeonDropItemStackable, PVFPath: weaponskin.MagicMoldPath, StackableType: "[material]"},
			weaponskin.BeiyunProductID: {Kind: dungeonDropItemStackable, PVFPath: weaponskin.BeiyunProductPath,
				StackableType: "[waste]"},
		},
	}
	handle := weaponSkinDemoGameplayModule().UpperHandlers[weaponSkinDemoOpcode]
	send := func(class, status byte, slot uint16, id uint32, expectSlotUpdate bool) {
		t.Helper()
		conn.write.Reset()
		session.weaponSkinForgeLastRequest = time.Time{}
		body := []byte("WSF1\x00\x00\x00\x00\x09\x00\x00\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
		binary.LittleEndian.PutUint32(body[4:8], id)
		binary.LittleEndian.PutUint16(body[14:16], slot)
		if err := handle(service, session, gameplayRequest{Classification: class, Body: body}); err != nil {
			t.Fatal(err)
		}
		packets := conn.write.Bytes()
		if class != 1 {
			if len(packets) != 0 {
				t.Fatal("非 class1 请求发送了回执")
			}
			return
		}
		if expectSlotUpdate {
			update, rest := splitLongHengGameServerUpperPacket(t, packets)
			if update.Header.MsgID != 14 || len(update.Body) < 9 || binary.LittleEndian.Uint16(update.Body[3:5]) != slot ||
				binary.LittleEndian.Uint32(update.Body[5:9]) != emptySlotItemID() {
				t.Fatalf("入库前置槽位刷新错误：%+v", update)
			}
			packets = rest
		}
		reply, rest := splitLongHengGameServerUpperPacket(t, packets)
		b := reply.Body
		if len(rest) != 0 || reply.Header.Classification != 1 || reply.Header.MsgID != weaponSkinDemoOpcode ||
			len(b) != 28 || string(b[:4]) != "WFR1" || binary.LittleEndian.Uint32(b[4:8]) != id ||
			binary.LittleEndian.Uint32(b[8:12]) != 9 || b[12] != status || b[13] != forgeRegister {
			t.Fatalf("入库回执不匹配：%+v", reply)
		}
	}
	send(0, 0, 65, 1, false)
	send(1, 6, 66, 2, false)
	send(1, 0, 65, 3, true)

	after, _, err := repos.Character.Load(ctx, character.CharacterID)
	if err != nil || !weaponskin.BeiyunRegistered(after) || weaponskin.Selected(after) != 0 {
		t.Fatalf("登记、应用状态异常：%+v，%v", after.Stats, err)
	}
	afterOther, _, err := repos.Character.Load(ctx, other.CharacterID)
	if err != nil || weaponskin.BeiyunRegistered(afterOther) {
		t.Fatalf("同账号其他角色被登记：%+v，%v", afterOther.Stats, err)
	}
	bag, _, err := repos.Inventory.Load(ctx, character.CharacterID)
	if err != nil || bag.Slots["0:65"].Count != 0 {
		t.Fatalf("成功入库未扣成品：%+v，%v", bag.Slots["0:65"], err)
	}
	bag.Slots = map[string]dnfrepo.ItemStack{"0:65": product}
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	send(1, 7, 65, 4, false)
	bag, _, err = repos.Inventory.Load(ctx, character.CharacterID)
	if err != nil || bag.Slots["0:65"].Count != 1 {
		t.Fatalf("重复登记扣了成品：%+v，%v", bag.Slots["0:65"], err)
	}
}
