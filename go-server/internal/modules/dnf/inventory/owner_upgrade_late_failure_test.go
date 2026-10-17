package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// op50（强化 / 增幅）"晚期失败"原子性回归：钱包与账号共享材料已经写下去之后，
// **背包保存失败**必须让整次 op50 一个字节都不落库，并且禁止回成功包。
//
// 为什么现在要钉这一条：owner.Upgrade 在一次事务里按
// "目标校验 → 材料归属 → 角色金币 → 掷点 → 扣材料 → 存金币 → 存背包（最后）"
// 的顺序落库（owner.go:1414-1714，金币在 :1704、背包在 :1712、账号共享材料在 :1690），
// 而强化费用与"额外材料"的接入（见 jonathan-evidence/transaction-audit.md）会在这个
// 事务里新增更多"先写 A 再写 B"的组合。现有 op50 测试只覆盖正常档与业务拒绝档，
// 没有一条在"钱包/账号材料已写"之后让背包保存失败。
//
// 注入方式沿用本包既有惯例（owner_separate_upgrade_test.go:705 的
// forgingFailingEquipmentSaveUOW、damage_font_test.go:146 的 failingDamageFontAssetUOW）：
// 用**真实**的 memory UoW，只把它交给回调的那一个仓库换成"背包保存必失败"的包装，
// 其余仓储（角色、账号共享、装备）原样透传，事务与回滚都还是 memory 真实现。
//
// 精度说明（不要把它读成"SQL 已提交再回滚"的证据）：memory UoW 的回调拿到的是事务
// 对象（memory.*Transaction），真正写 store 发生在回调返回 nil 之后的提交阶段
// （memory_transaction.go:823-840 依次 角色 → 背包 → 装备，任一失败即还原）。
// 所以这里"已写"的精确含义是"owner.Upgrade 里排在背包保存**之前**的那些写入已经落在
// 本次事务的记录里"，逐条按代码位置对齐：
//   - 角色钱包：SaveCharacterFields（owner.go:1704）⇒ 背包保存之前已经写入（两条用例都断言）；
//   - 账号共享材料：回调内的 accountRepo.Save（owner.go:1690）⇒ 背包保存之前已经写入；
//   - 主背包行材料与装备等级：由**失败的那一次 SaveInventoryFields（owner.go:1712）本身**
//     承载（owner.go 只改内存 record，最后一次性 Save）⇒ 失败时自然还没写。
// 随后背包保存失败会把整笔事务连同上面已写的部分一起丢弃。提交阶段自身的
// "角色已提交、背包提交失败即还原"由 repository/memory/transaction_asset_test.go:71
// （TestMemoryCharacterAssetUnitOfWorkRestoresEarlierCommits）覆盖；真库档另有
// 可丢弃 DSN 的 MySQL 回归（如 dungeon/lucky_star_atomic_mysql_test.go）。

const (
	upgradeLateFailureCharacterID = "77"
	upgradeLateFailureAccountID   = "acc"
	upgradeLateFailureTargetSlot  = 9
	upgradeLateFailureTargetItem  = 700
	// upgradeLateFailureMaterialSlot 是主背包里的材料槽（非账号共享槽位）。
	upgradeLateFailureMaterialSlot = 10
	// upgradeLateFailureMainMaterial 是普通材料（3242 矛盾的结晶体），
	// upgradeAccountSharedMaterialSlot 不认识它 ⇒ 走角色资产事务。
	upgradeLateFailureMainMaterial = 3242
	// upgradeLateFailureSharedMaterial 是账号共享材料（3037 → 固定槽 358），
	// 走账号+角色资产事务，且在同一事务里先于背包写入。
	upgradeLateFailureSharedMaterial = 3037
	upgradeLateFailureSharedSlot     = 358
	upgradeLateFailureStartGold      = 12345
	// upgradeLateFailureGoldFee 写进目标装备的 Extra（upgradeGoldCostOf 的键之一），
	// 让这次 op50 真的走一遍"扣金币 + SaveCharacterFields"。
	upgradeLateFailureGoldFee      = 500
	upgradeLateFailureMaterialNeed = 10
	upgradeLateFailureMaterialHave = 11
	upgradeLateFailureSharedHave   = 12
)

// errUpgradeLateBagSave 是注入的背包保存失败（op50 事务里的最后一次写入）。
var errUpgradeLateBagSave = errors.New("injected op50 bag save failure")

// upgradeLateFailureProbe 记录"失败之前事务里已经写成了什么"以及背包保存被调了几次。
// 观察点在回调返回之后、UoW 丢弃事务之前，读的是事务记录，不是 store。
type upgradeLateFailureProbe struct {
	saveErr error

	bagSaveAttempts int

	goldSeen       bool
	goldAfterApply int64

	targetLevelSeen       bool
	targetLevelAfterApply byte

	mainMaterialSeen       bool
	mainMaterialAfterApply int64

	sharedMaterialSeen       bool
	sharedMaterialAfterApply int64
}

func (p *upgradeLateFailureProbe) observeCharacterRecord(ctx context.Context, characters dnfrepo.CharacterRepository) {
	if p == nil {
		return
	}
	character, ok, err := characters.Load(ctx, upgradeLateFailureCharacterID)
	if err != nil || !ok {
		return
	}
	p.goldAfterApply, p.goldSeen = character.Stats["gold"], true
}

func (p *upgradeLateFailureProbe) observeInventoryRecord(ctx context.Context, inventory dnfrepo.InventoryRepository) {
	if p == nil {
		return
	}
	record, ok, err := inventory.Load(ctx, upgradeLateFailureCharacterID)
	if err != nil || !ok {
		return
	}
	if stack, found := record.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)]; found {
		p.targetLevelAfterApply, p.targetLevelSeen = upgradeLevelOf(stack), true
	}
	if stack, found := record.Slots[slotKey(listTypeMain, upgradeLateFailureMaterialSlot)]; found {
		p.mainMaterialAfterApply, p.mainMaterialSeen = stack.Count, true
	}
}

// upgradeLateFailureInventory 只劫持"背包保存"这一个动作：Save / SaveFields 都失败。
// Load 及其它方法由被包裹的真实事务对象提供。saveErr 为 nil 时逐字透传，
// 便于同一套夹具同时跑对照档（对照档不使用本包装，见各测试）。
type upgradeLateFailureInventory struct {
	dnfrepo.InventoryRepository
	probe *upgradeLateFailureProbe
}

func (r upgradeLateFailureInventory) Save(ctx context.Context, record dnfrepo.InventoryRecord) error {
	if r.probe.saveErr == nil {
		return r.InventoryRepository.Save(ctx, record)
	}
	r.probe.bagSaveAttempts++
	return r.probe.saveErr
}

func (r upgradeLateFailureInventory) SaveFields(ctx context.Context, record dnfrepo.InventoryRecord, fields ...dnfrepo.InventoryField) error {
	if r.probe.saveErr == nil {
		return dnfrepo.SaveInventoryFields(ctx, r.InventoryRepository, record, fields...)
	}
	r.probe.bagSaveAttempts++
	return r.probe.saveErr
}

// upgradeLateFailureAssets 是真实 memory 角色资产事务 + 失败的背包保存。
type upgradeLateFailureAssets struct {
	base  dnfrepo.CharacterAssetUnitOfWork
	probe *upgradeLateFailureProbe
}

func (u upgradeLateFailureAssets) WithinCharacterAssets(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	return u.base.WithinCharacterAssets(ctx, characterID, func(
		characters dnfrepo.CharacterRepository,
		inventory dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		err := apply(characters, upgradeLateFailureInventory{InventoryRepository: inventory, probe: u.probe}, equipment)
		u.probe.observeCharacterRecord(ctx, characters)
		u.probe.observeInventoryRecord(ctx, inventory)
		return err
	})
}

// upgradeLateFailureAccountAssets 是真实 memory 账号+角色资产事务 + 失败的背包保存。
// 账号共享材料的 Save 在回调内先发生（owner.go:1690），背包保存在最后（:1712）。
type upgradeLateFailureAccountAssets struct {
	base  dnfrepo.AccountCharacterAssetUnitOfWork
	probe *upgradeLateFailureProbe
}

func (u upgradeLateFailureAccountAssets) WithinAccountCharacterAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	return u.base.WithinAccountCharacterAssets(ctx, accountID, characterID, func(
		accountInventory dnfrepo.AccountInventoryRepository,
		characters dnfrepo.CharacterRepository,
		inventory dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		err := apply(accountInventory, characters, upgradeLateFailureInventory{InventoryRepository: inventory, probe: u.probe}, equipment)
		u.probe.observeCharacterRecord(ctx, characters)
		u.probe.observeInventoryRecord(ctx, inventory)
		if record, ok, loadErr := accountInventory.Load(ctx, upgradeLateFailureAccountID); loadErr == nil && ok {
			if stack, found := record.Slots[dnfrepo.AccountSharedInventorySlotKey(upgradeLateFailureSharedSlot)]; found {
				u.probe.sharedMaterialAfterApply, u.probe.sharedMaterialSeen = stack.Count, true
			}
		}
		return err
	})
}

func upgradeLateFailureTarget(extra map[string]string) dnfrepo.ItemStack {
	merged := map[string]string{
		"item_kind":      "equipment",
		"durability":     "45",
		"max_durability": "45",
	}
	for key, value := range extra {
		merged[key] = value
	}
	return dnfrepo.ItemStack{ItemID: upgradeLateFailureTargetItem, Count: 1, Extra: merged}
}

// upgradeLateFailureBody 组一条最小合法 op50 请求体：
// u16 mode, i16 目标槽, i32 目标道具, i16 材料槽, i16 券槽, i32 名称长度 + 名称。
func upgradeLateFailureBody(materialSlot int16) []byte {
	body := make([]byte, 16)
	binary.LittleEndian.PutUint16(body[0:2], 0)
	binary.LittleEndian.PutUint16(body[2:4], uint16(upgradeLateFailureTargetSlot))
	binary.LittleEndian.PutUint32(body[4:8], upgradeLateFailureTargetItem)
	binary.LittleEndian.PutUint16(body[8:10], uint16(materialSlot))
	binary.LittleEndian.PutUint16(body[10:12], 0xFFFF)
	return body
}

func runUpgradeLateFailureRequest(t *testing.T, repos dnfrepo.Group, materialSlot int16, materialItemID int) alignedcmd.Result {
	t.Helper()
	got, err := NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketUpgradeItem),
		Body:                upgradeLateFailureBody(materialSlot),
		AccountID:           upgradeLateFailureAccountID,
		SelectedCharacterID: 77,
		Repositories:        repos,
		UpgradePolicyResolver: func(mode string, currentLevel int) (alignedcmd.UpgradePolicyResolution, error) {
			if mode != "reinforce" || currentLevel != 0 {
				t.Fatalf("policy args mode=%s level=%d, want reinforce/0", mode, currentLevel)
			}
			return alignedcmd.UpgradePolicyResolution{
				SuccessWeight:  currentUpgradeSuccessBase, // 必定成功
				MaterialItemID: materialItemID,
				MaterialCount:  upgradeLateFailureMaterialNeed,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	return got
}

// 主背包材料 + 金币费用档。
func seedUpgradeLateFailureWalletFixture(t *testing.T, ctx context.Context, repos dnfrepo.Group) {
	t.Helper()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: upgradeLateFailureCharacterID,
		AccountID:   upgradeLateFailureAccountID,
		Stats:       map[string]int64{"gold": upgradeLateFailureStartGold},
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: upgradeLateFailureCharacterID,
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, upgradeLateFailureTargetSlot): upgradeLateFailureTarget(map[string]string{
				"upgrade_gold": strconv.Itoa(upgradeLateFailureGoldFee),
			}),
			slotKey(listTypeMain, upgradeLateFailureMaterialSlot): {
				ItemID: upgradeLateFailureMainMaterial,
				Count:  upgradeLateFailureMaterialHave,
			},
		},
	})
}

func assertUpgradeLateFailureStoresUntouched(t *testing.T, ctx context.Context, repos dnfrepo.Group) {
	t.Helper()
	character, ok, err := repos.Character.Load(ctx, upgradeLateFailureCharacterID)
	if err != nil || !ok {
		t.Fatalf("character load ok=%t err=%v", ok, err)
	}
	if gold := character.Stats["gold"]; gold != upgradeLateFailureStartGold {
		t.Fatalf("gold=%d, want the pre-request %d (a failed op50 must not charge)", gold, upgradeLateFailureStartGold)
	}
	loaded := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
	target, found := loaded.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)]
	if !found {
		t.Fatalf("target slot %d disappeared", upgradeLateFailureTargetSlot)
	}
	if target.ItemID != upgradeLateFailureTargetItem || target.Count != 1 {
		t.Fatalf("target stack = %+v", target)
	}
	if level := upgradeLevelOf(target); level != 0 {
		t.Fatalf("target upgrade level = %d, want the pre-request 0", level)
	}
	material, found := loaded.Slots[slotKey(listTypeMain, upgradeLateFailureMaterialSlot)]
	if !found || material.Count != upgradeLateFailureMaterialHave {
		t.Fatalf("main material = %+v found=%t, want count %d", material, found, upgradeLateFailureMaterialHave)
	}
}

// ① 金币已写、背包保存失败：金币、材料、目标等级全部回到请求前，且不回成功包。
func TestUpgradeLateBagSaveFailureRollsBackWalletMaterialAndLevel(t *testing.T) {
	ctx := context.Background()

	t.Run("control charges the wallet, the material and the level", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		seedUpgradeLateFailureWalletFixture(t, ctx, repos)
		got := runUpgradeLateFailureRequest(t, repos, upgradeLateFailureMaterialSlot, upgradeLateFailureMainMaterial)
		if !got.ResponseAllowed || len(got.UpperResponses) == 0 {
			t.Fatalf("control must return the op50 success ACK: %+v", got)
		}
		character, ok, err := repos.Character.Load(ctx, upgradeLateFailureCharacterID)
		if err != nil || !ok {
			t.Fatalf("character load ok=%t err=%v", ok, err)
		}
		if gold := character.Stats["gold"]; gold != upgradeLateFailureStartGold-upgradeLateFailureGoldFee {
			t.Fatalf("gold=%d, want %d", gold, upgradeLateFailureStartGold-upgradeLateFailureGoldFee)
		}
		loaded := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
		if level := upgradeLevelOf(loaded.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)]); level != 1 {
			t.Fatalf("target level = %d, want 1", level)
		}
		if left := loaded.Slots[slotKey(listTypeMain, upgradeLateFailureMaterialSlot)].Count; left != upgradeLateFailureMaterialHave-upgradeLateFailureMaterialNeed {
			t.Fatalf("material left = %d, want %d", left, upgradeLateFailureMaterialHave-upgradeLateFailureMaterialNeed)
		}
	})

	t.Run("bag save failure rolls the whole request back", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		probe := &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}
		repos.CharacterAssets = upgradeLateFailureAssets{base: repos.CharacterAssets, probe: probe}
		seedUpgradeLateFailureWalletFixture(t, ctx, repos)

		got := runUpgradeLateFailureRequest(t, repos, upgradeLateFailureMaterialSlot, upgradeLateFailureMainMaterial)
		if got.ResponseAllowed || len(got.UpperResponses) != 0 {
			t.Fatalf("a failed bag save must not produce any ACK: %+v", got)
		}
		if !strings.Contains(got.Reason, errUpgradeLateBagSave.Error()) {
			t.Fatalf("reason = %q, want the injected bag save failure", got.Reason)
		}
		if probe.bagSaveAttempts != 1 {
			t.Fatalf("bag save attempts = %d, want 1", probe.bagSaveAttempts)
		}
		// "已写"：角色钱包由 SaveCharacterFields 承载，排在背包保存之前 ⇒ 此刻事务里已经是扣费后的余额。
		if !probe.goldSeen || probe.goldAfterApply != upgradeLateFailureStartGold-upgradeLateFailureGoldFee {
			t.Fatalf("wallet was not written before the bag save failed: seen=%t gold=%d", probe.goldSeen, probe.goldAfterApply)
		}
		// 主背包那一行材料与装备等级由**失败的那一次背包保存**承载（owner.go 只改内存 record，
		// 最后一次性 Save）⇒ 此刻事务里仍是请求前的值。这条断言把"哪些写入排在前面、
		// 哪些写入正是失败步骤本身"钉死，防止以后把材料改动挪到背包保存之前而没人发现。
		if !probe.mainMaterialSeen || probe.mainMaterialAfterApply != upgradeLateFailureMaterialHave {
			t.Fatalf("main material must still be the pre-request count at bag-save time: seen=%t count=%d; 若费用/额外材料接入有意把材料写入挪到背包保存之前，请同步更新本条期望值", probe.mainMaterialSeen, probe.mainMaterialAfterApply)
		}
		if !probe.targetLevelSeen || probe.targetLevelAfterApply != 0 {
			t.Fatalf("target level must still be the pre-request 0 at bag-save time: seen=%t level=%d; 同理，等级由背包保存承载，若有意改变顺序请同步更新", probe.targetLevelSeen, probe.targetLevelAfterApply)
		}
		// 回滚：store 必须与请求前逐项相同。
		assertUpgradeLateFailureStoresUntouched(t, ctx, repos)
	})
}

// ② 账号共享材料已写、背包保存失败：共享槽、金币、目标等级全部回到请求前。
func TestUpgradeLateBagSaveFailureRollsBackAccountSharedMaterial(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, repos dnfrepo.Group) {
		t.Helper()
		saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
			CharacterID: upgradeLateFailureCharacterID,
			AccountID:   upgradeLateFailureAccountID,
			Stats:       map[string]int64{"gold": upgradeLateFailureStartGold},
		})
		if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
			AccountID: upgradeLateFailureAccountID,
			Slots: map[string]dnfrepo.ItemStack{
				dnfrepo.AccountSharedInventorySlotKey(upgradeLateFailureSharedSlot): {
					ItemID: upgradeLateFailureSharedMaterial,
					Count:  upgradeLateFailureSharedHave,
				},
			},
		}); err != nil {
			t.Fatalf("Save account inventory error = %v", err)
		}
		saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
			CharacterID: upgradeLateFailureCharacterID,
			Slots: map[string]dnfrepo.ItemStack{
				slotKey(listTypeMain, upgradeLateFailureTargetSlot): upgradeLateFailureTarget(nil),
			},
		})
	}
	sharedCount := func(t *testing.T, repos dnfrepo.Group) int64 {
		t.Helper()
		record, ok, err := repos.AccountInventory.Load(ctx, upgradeLateFailureAccountID)
		if err != nil || !ok {
			t.Fatalf("account inventory load ok=%t err=%v", ok, err)
		}
		return record.Slots[dnfrepo.AccountSharedInventorySlotKey(upgradeLateFailureSharedSlot)].Count
	}

	t.Run("control consumes the account shared row", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		seed(t, repos)
		got := runUpgradeLateFailureRequest(t, repos, upgradeLateFailureSharedSlot, upgradeLateFailureSharedMaterial)
		if !got.ResponseAllowed || len(got.UpperResponses) == 0 {
			t.Fatalf("control must return the op50 success ACK: %+v", got)
		}
		if left := sharedCount(t, repos); left != upgradeLateFailureSharedHave-upgradeLateFailureMaterialNeed {
			t.Fatalf("account shared count = %d, want %d", left, upgradeLateFailureSharedHave-upgradeLateFailureMaterialNeed)
		}
		loaded := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
		if level := upgradeLevelOf(loaded.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)]); level != 1 {
			t.Fatalf("target level = %d, want 1", level)
		}
	})

	t.Run("bag save failure restores the account shared row", func(t *testing.T) {
		repos := dnfrepomemory.NewMemoryGroup()
		probe := &upgradeLateFailureProbe{saveErr: errUpgradeLateBagSave}
		repos.AccountAssets = upgradeLateFailureAccountAssets{base: repos.AccountAssets, probe: probe}
		seed(t, repos)

		got := runUpgradeLateFailureRequest(t, repos, upgradeLateFailureSharedSlot, upgradeLateFailureSharedMaterial)
		if got.ResponseAllowed || len(got.UpperResponses) != 0 {
			t.Fatalf("a failed bag save must not produce any ACK: %+v", got)
		}
		if !strings.Contains(got.Reason, errUpgradeLateBagSave.Error()) {
			t.Fatalf("reason = %q, want the injected bag save failure", got.Reason)
		}
		if probe.bagSaveAttempts != 1 {
			t.Fatalf("bag save attempts = %d, want 1", probe.bagSaveAttempts)
		}
		// "已写"：账号共享材料由回调内的 accountRepo.Save 承载（owner.go:1690），
		// 排在背包保存之前 ⇒ 此刻事务里已经是扣减后的数量。
		if !probe.sharedMaterialSeen || probe.sharedMaterialAfterApply != upgradeLateFailureSharedHave-upgradeLateFailureMaterialNeed {
			t.Fatalf("account shared material was not written before the bag save failed: seen=%t count=%d", probe.sharedMaterialSeen, probe.sharedMaterialAfterApply)
		}
		// 目标等级同样由失败的背包保存承载 ⇒ 此刻仍是请求前的 0。
		if !probe.targetLevelSeen || probe.targetLevelAfterApply != 0 {
			t.Fatalf("target level must still be the pre-request 0 at bag-save time: seen=%t level=%d; 同理，等级由背包保存承载，若有意改变顺序请同步更新", probe.targetLevelSeen, probe.targetLevelAfterApply)
		}
		if !probe.goldSeen || probe.goldAfterApply != upgradeLateFailureStartGold {
			t.Fatalf("this scene has no gold fee, wallet must stay %d: seen=%t gold=%d", upgradeLateFailureStartGold, probe.goldSeen, probe.goldAfterApply)
		}
		// 回滚：共享槽与角色资产都必须与请求前相同。
		if left := sharedCount(t, repos); left != upgradeLateFailureSharedHave {
			t.Fatalf("account shared count = %d, want the pre-request %d", left, upgradeLateFailureSharedHave)
		}
		character, ok, err := repos.Character.Load(ctx, upgradeLateFailureCharacterID)
		if err != nil || !ok {
			t.Fatalf("character load ok=%t err=%v", ok, err)
		}
		if gold := character.Stats["gold"]; gold != upgradeLateFailureStartGold {
			t.Fatalf("gold=%d, want %d", gold, upgradeLateFailureStartGold)
		}
		loaded := loadTestInventory(t, ctx, repos, upgradeLateFailureCharacterID)
		if level := upgradeLevelOf(loaded.Slots[slotKey(listTypeMain, upgradeLateFailureTargetSlot)]); level != 0 {
			t.Fatalf("target level = %d, want the pre-request 0", level)
		}
	})
}
