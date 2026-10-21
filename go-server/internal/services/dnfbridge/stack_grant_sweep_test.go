package dnfbridge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是"发放路径不许再漏合并"的**全服清扫**门禁（操作者 2026-09-28 原话：
// 「你排查一下还有没有类似道具会这样，要彻底解决堆叠问题，一个个人工排查太费时」）。
//
// 它不跑某个具体用例，而是把"能往背包/宠物背包/账号共享仓/仓库里放东西"的每一条
// 发放路径逐个点名，并要求该函数体里出现合并判据（或该路径被显式登记为例外 + 理由）。
// 新增一条不合并的发放路径 ⇒ 这里必须有名字，否则测试失败——这就是"不用再一个个
// 人工排查"的保证。
//
// 判据口径（与现场一致）：
//   - 通用发放：`dnfrepo.CanMergeItemStack`（身份，缺省即兼容）+ 目标行的
//     PVF `[stack limit]` 封顶；
//   - 同页段首选：`grantCurrentCeraShopProduct` / `addCurrentDungeonPickupToInventory`
//     这两个共用落位器本身已经合并，调用它们的路径只需出现调用；
//   - 手动拖拽合并的严格判据：`dnfrepo.StackContract`（item_kind/pvf_path）。
//
// 例外必须写理由：装备/时装/宠物（实例身份，永不合并）、固定仓位（账号晶块/灵魂仓，
// 一格一物）、就地模板替换（结果直接替换被消耗的那一格）。

type stackGrantRoute struct {
	name string
	file string // 相对 go-server 根
	fn   string // 必须存在的函数名
	// token 必须出现在该函数体内。空 token = 登记为例外，此时 reason 必填。
	token  string
	reason string
}

// stackGrantRoutes 是审计表本身。现场取证：全服 1892 行库存里"PVF 可堆叠却缺堆叠契约"
// 的只有强化/增幅摧毁补偿那一族（3243 角色 6/15/16、3244 角色 5/6）与一行历史更名卡；
// 它们由第 1 行（摧毁补偿）负责，其余每一条都必须已经在并。
var stackGrantRoutes = []stackGrantRoute{
	// —— 强化/增幅失败摧毁补偿（本轮修复的对象）——
	{name: "upgrade_destroy_bonus", file: "internal/modules/dnf/inventory/upgrade_destroy_bonus.go",
		fn: "placeUpgradeDestroyBonusMerging", token: "CanMergeItemStack"},

	// —— 副本/世界掉落与拾取 ——
	{name: "dungeon_pickup_range", file: "internal/modules/dnf/dungeon/pickup_owner.go",
		fn: "stackPickupInRange", token: "CanMergeItemStack"},
	{name: "dungeon_drop_pickup", file: "internal/services/dnfbridge/dungeon_drop_pickup.go",
		fn: "currentDungeonPickupPlacement", token: "MergeIdentity"},
	{name: "dungeon_quest_drop", file: "internal/services/dnfbridge/dungeon_quest_drop.go",
		fn: "currentQuestDropGrantStack", token: "CanMergeItemStack"},

	// —— 奖励：翻牌 / 任务 / 教程 / 随机盒 / 魔盒 ——
	{name: "dungeon_card_reward", file: "internal/modules/dnf/dungeon/owner.go",
		fn: "addCardItem", token: "CanMergeItemStack"},
	{name: "quest_finish_reward", file: "internal/services/dnfbridge/quest_finish.go",
		fn: "currentQuestFinishItemAllocator", token: "addCurrentDungeonPickupToInventory"},
	{name: "tutorial_reward", file: "internal/modules/dnf/dungeon/tutorial_reward_owner.go",
		fn: "canStackTutorialReward", token: "CanMergeItemStack"},
	{name: "random_reward_box", file: "internal/modules/dnf/inventory/owner.go",
		fn: "grantRandomRewardItem", token: "CanMergeItemStack"},
	{name: "magic_box_reward", file: "internal/modules/dnf/packageitem/owner.go",
		fn: "grantMagicBoxReward", token: "CanMergeItemStack"},
	{name: "online_event_spend_time", file: "internal/services/dnfbridge/online_event_spend_time.go",
		fn: "currentSpendTimeItemAllocator", token: "addCurrentDungeonPickupToInventory"},

	// —— 邮件（含 GM 面板发放）——
	{name: "mail_claim", file: "internal/modules/dnf/mail/owner.go",
		fn: "canMailStack", token: "CanMergeItemStack"},

	// —— 点券商城 / 礼包 / booster / NPC 商店 / 分解 / 史诗制作 / 副职业 / 竞猜 / 冒险团 ——
	{name: "cera_shop_product", file: "internal/services/dnfbridge/cera_shop_grant.go",
		fn: "grantCurrentCeraShopProduct", token: "grantCurrentCeraShopProductAt"},
	{name: "cera_shop_product_at", file: "internal/services/dnfbridge/cera_shop_grant.go",
		fn: "grantCurrentCeraShopProductAt", token: "CanMergeItemStack"},
	{name: "cera_shop_pet_consumable", file: "internal/services/dnfbridge/cera_shop_grant.go",
		fn: "grantCurrentCeraShopPetConsumable", token: "CanMergeItemStack"},
	{name: "cera_shop_guild_medal", file: "internal/services/dnfbridge/cera_shop_grant.go",
		fn: "grantCurrentGuildMedalPageItem", token: "CanMergeItemStack"},
	{name: "cera_shop_purchase", file: "internal/services/dnfbridge/cera_shop_purchase.go",
		fn: "commitCurrentCeraShopPurchase", token: "grantCurrentCeraShopProduct"},
	{name: "cera_package", file: "internal/services/dnfbridge/cera_package.go",
		fn: "commitCurrentCeraPackage", token: "grantCurrentCeraShopProduct"},
	{name: "booster_reward", file: "internal/services/dnfbridge/booster_projection.go",
		fn: "grantCurrentBoosterReward", token: "grantCurrentCeraShopProduct"},
	{name: "npc_shop_buy", file: "internal/services/dnfbridge/npc_shop_projection.go",
		fn: "commitCurrentNPCShopBuy", token: "grantCurrentNPCShopProduct"},
	{name: "npc_shop_material_exchange", file: "internal/services/dnfbridge/npc_shop_projection.go",
		fn: "commitCurrentNPCShopMaterialExchange", token: "grantCurrentNPCShopProduct"},
	{name: "npc_shop_main_bag", file: "internal/services/dnfbridge/npc_shop_projection.go",
		fn: "grantCurrentNPCShopProduct", token: "grantCurrentCeraShopProductAt"},
	{name: "npc_shop_medal_page", file: "internal/services/dnfbridge/npc_shop_projection.go",
		fn: "grantCurrentNPCShopProduct", token: "grantCurrentGuildMedalPageItem"},
	{name: "disjoint_rewards", file: "internal/services/dnfbridge/disjoint_compound_projection.go",
		fn: "currentGrantDisjointRewards", token: "grantCurrentCeraShopProduct"},
	{name: "epic_production_compound", file: "internal/services/dnfbridge/epic_production.go",
		fn: "commitCurrentEpicProductionCompound", token: "grantCurrentCeraShopProduct"},
	{name: "expert_job_compound", file: "internal/services/dnfbridge/gameplay_module_expert_job.go",
		fn: "commitCurrentExpertJobCompound", token: "grantCurrentCeraShopProduct"},
	{name: "expert_job_store", file: "internal/services/dnfbridge/expert_job_store.go",
		fn: "commitCurrentExpertJobStoreDisjointBatch", token: "currentGrantDisjointRewards"},
	{name: "joust_betting", file: "internal/services/dnfbridge/joust_betting.go",
		fn: "commitCurrentJoustBetting", token: "grantCurrentCeraShopProduct"},
	{name: "adventure_shop_purchase", file: "internal/services/dnfbridge/adventure_runtime.go",
		fn: "handleCurrentAdventureShopPurchase", token: "grantCurrentCeraShopProduct"},
	{name: "expert_job_store_disjoint", file: "internal/services/dnfbridge/expert_job_store.go",
		fn: "commitCurrentExpertJobStoreDisjointBatch", token: "currentGrantDisjointRewards"},

	// —— 玩家间交易（本轮修复的第二条非合并路径）——
	{name: "item_trade_received", file: "internal/modules/dnf/itemtrade/owner.go",
		fn: "mergeTradeStack", token: "CanMergeItemStack"},

	// —— 显式例外（有理由，不是漏网）——
	{name: "disjoint_fixed_warehouse_slot", file: "internal/services/dnfbridge/disjoint_compound_projection.go",
		fn: "grantCurrentDisjointWarehouseStack",
		reason: "账号晶块/灵魂仓是固定仓位（一格一物）：按 AccountSharedInventorySlotKey 并入同一固定格，" +
			"外来占用物不并也不驱逐；溢出回落背包走 grantCurrentCeraShopProduct"},
	{name: "achievement_title", file: "internal/modules/dnf/achievement/owner.go", fn: "grantTitle",
		reason: "称号书格子一物一格、Count 恒 1，函数内已按同 item 去重（不会拆成两行）"},
	{name: "limited_cube_use", file: "internal/modules/dnf/limitedcube/owner.go", fn: "Use",
		reason: "就地模板替换：结果直接替换被消耗的那一格（旧模板行不保留），没有第二行可产生"},
	{name: "pet_hatch", file: "internal/modules/dnf/pet/owner.go", fn: "Hatch",
		reason: "宠物是实例（creature_serial 身份）、Count 恒 1，蛋格被就地替换"},
	{name: "creature_reconcile", file: "internal/modules/dnf/creaturestate/inventory.go", fn: "ReconcileInventory",
		reason: "宠物容器按 creature_serial 归位（实例身份，永不堆叠）"},
	{name: "title_book_move", file: "internal/modules/dnf/titlebook/owner.go", fn: "Put",
		reason: "称号书格子一物一格、Count 恒 1"},
	{name: "equipment_effect_rune", file: "internal/modules/dnf/equipmenteffect/owner.go", fn: "Apply",
		reason: "只消耗符文并把效果写进目标装备，不产生新的可堆叠行"},
	{name: "item_grade_adjust", file: "internal/modules/dnf/itemgrade/owner.go", fn: "Adjust",
		reason: "只消耗调整箱并改写目标装备品质，不产生新的可堆叠行"},
	{name: "skill_book_use", file: "internal/modules/dnf/experienceitem/owner.go", fn: "UseSkillBook",
		reason: "只扣 1 本书并把 SP/TP 记进角色账本，不产生新的可堆叠行"},
	{name: "damage_font_unlock", file: "internal/modules/dnf/inventory/damage_font.go", fn: "UnlockDamageFont",
		reason: "只扣 1 张字体皮肤并把归属记进角色统计，不产生新的可堆叠行"},
	{name: "revive_coin_wallet", file: "internal/modules/dnf/revivecoin/wallet.go", fn: "Grant",
		reason: "复活币钱包是固定槽位（WalletKey），不是普通堆叠行"},
	{name: "epic_piece_ledger", file: "internal/services/dnfbridge/epic_piece_grant.go", fn: "epicPieceGrant",
		reason: "史诗碎片记账号账本（不发背包行）"},
	{name: "collectbox_registry", file: "internal/modules/dnf/collectbox/owner.go", fn: "Put",
		reason: "收集箱是登记表（物品被登记而非入包），原格被清空"},
	{name: "avatar_option_change", file: "internal/modules/dnf/avataroptionchange/owner.go", fn: "project",
		reason: "只改目标装扮的属性字节，源箱被扣 1，不产生新的可堆叠行"},
	{name: "socket_emblem_consume", file: "internal/services/dnfbridge/socket_emblem_projection.go",
		fn:     "consumeCurrentSocketStack",
		reason: "只扣徽章/材料并把结果写进目标装备的孔位，不产生新的可堆叠行"},
	{name: "rental_asset", file: "internal/services/dnfbridge/rental_asset_projection.go",
		fn:     "rentCurrentEquipment",
		reason: "租借装备是实例（租期身份），Count 恒 1"},
	{name: "guardian_gem_write", file: "internal/services/dnfbridge/guardian_gem_projection.go",
		fn:     "currentGuardianGemWriteTarget",
		reason: "守护珠写进 list 38 的固定分页格（49..97），一格一物"},
	{name: "premium_contract_activation", file: "internal/modules/dnf/premium/inventory_activation.go",
		fn:     "ActivateContractGrant",
		reason: "契约激活写的是账号契约账本与固定券行，不是普通可堆叠材料"},
	{name: "tournament_card_bundle", file: "internal/services/dnfbridge/dungeon_tournament_flow.go",
		fn:     "currentTournamentRewardGrantBundle",
		reason: "只构造 card reward bundle（身份键齐全），落行由 addCardItem 负责——那条已经在并"},
	{name: "avatar_compound_result", file: "internal/services/dnfbridge/gameplay_module_avatar_compound.go",
		fn:     "currentAvatarCompoundGrantStack",
		reason: "合成结果是装扮实例（item_kind=avatar、Count 恒 1），就地替换被消耗的格子"},
}

// repoRootFromTest 从测试进程的工作目录往上找到含 go.mod 的目录（= go-server 根）。
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// stackGrantRouteFunctionBody 返回命名函数的源码正文（含注释），找不到就失败。
func stackGrantRouteFunctionBody(t *testing.T, root string, route stackGrantRoute) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(route.file))
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("route %s: read %s: %v", route.name, route.file, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("route %s: parse %s: %v", route.name, route.file, err)
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != route.fn || fn.Body == nil {
			continue
		}
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		return string(src[start:end])
	}
	t.Fatalf("route %s: function %s not found in %s (renamed? then update this audit table)", route.name, route.fn, route.file)
	return ""
}

// TestEveryStackableGrantRouteMergesOrIsAnExplicitException 是"全服发放路径清扫"门禁。
func TestEveryStackableGrantRouteMergesOrIsAnExplicitException(t *testing.T) {
	root := repoRootFromTest(t)
	if len(stackGrantRoutes) < 30 {
		t.Fatalf("audit table shrank to %d routes; every stackable grant route must stay named here", len(stackGrantRoutes))
	}
	mergeRoutes := 0
	exceptions := 0
	for _, route := range stackGrantRoutes {
		body := stackGrantRouteFunctionBody(t, root, route)
		if route.token == "" {
			if strings.TrimSpace(route.reason) == "" {
				t.Fatalf("route %s: exception without a reason", route.name)
			}
			exceptions++
			continue
		}
		if strings.TrimSpace(route.reason) != "" {
			t.Fatalf("route %s: a merging route must not carry an exception reason", route.name)
		}
		if !strings.Contains(body, route.token) {
			t.Fatalf("route %s (%s:%s) does not call %s: a stackable grant path without a merge "+
				"judgement splits rows (2026-09-28 现场: 3243/3244 每次摧毁多一行)",
				route.name, route.file, route.fn, route.token)
		}
		mergeRoutes++
	}
	// 表里必须有真门禁：合并路径与例外都要有，且合并路径占多数。
	if mergeRoutes < 20 || exceptions < 10 {
		t.Fatalf("audit table looks wrong: merging=%d exceptions=%d", mergeRoutes, exceptions)
	}
	t.Logf("stackable grant routes audited: %d merging, %d explicit exceptions", mergeRoutes, exceptions)
}

// TestStackContractJudgeIsOwnedByTheRepositoryLayer 钉死"严格堆叠判据只有一份实现"：
// inventory（op19 三态）与 itemtrade（交易收下）都必须走 dnfrepo.StackContract，
// 不允许各自再写一套 item_kind/pvf_path 字符串判断。
func TestStackContractJudgeIsOwnedByTheRepositoryLayer(t *testing.T) {
	root := repoRootFromTest(t)
	for _, site := range []struct {
		file string
		fn   string
	}{
		{file: "internal/modules/dnf/inventory/owner.go", fn: "pvfStackContract"},
		{file: "internal/modules/dnf/itemtrade/owner.go", fn: "mergeTradeStack"},
	} {
		body := stackGrantRouteFunctionBody(t, root, stackGrantRoute{name: site.fn, file: site.file, fn: site.fn})
		if !strings.Contains(body, "StackContract") {
			t.Fatalf("%s:%s must delegate to dnfrepo.StackContract (single implementation)", site.file, site.fn)
		}
		if strings.Contains(body, `Extra["item_kind"]`) {
			t.Fatalf("%s:%s re-implements the stack contract instead of delegating", site.file, site.fn)
		}
	}
}
