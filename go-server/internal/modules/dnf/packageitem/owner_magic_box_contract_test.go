package packageitem

import (
	"context"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场报障回归（2026-09-25，香港 packet_log + dnf_mails 取证）：
// 「赛丽亚的幸运里拿到的晶之/达人之/成长之契约，物品栏满时会进邮件，不直接与已有
// 同名契约叠加时长」。
//
// 真因：契约奖励原先和普通可堆叠道具走同一条发放路径（grantMagicBoxReward →
// overflow → AppendSystemMail「背包已满：礼盒奖励」），而契约的"叠加时长"语义是被
// 分流成账号契约时长（不占格）。参考实现 A21 ServerS4A21
// InventoryRewardGrantService.TryPlanPremium 也是在槽位规划之前就分流。
//
// 契约身份只看运行时 PVF etc/premiumlist_new.etc（[type]/[item]/[term]），由
// dnfbridge 在解析奖励模板时钉进 alignedcmd.MagicBoxRewardItem.PremiumType，
// 因此这里用显式契约模板复现。

const magicBoxContractTestAccountID = "dnf:10005"

// magicBoxContractRewardItem：item 30 = 霸王之契约(1天)，PVF premium type 22 / 86400s。
func magicBoxContractRewardItem() alignedcmd.MagicBoxRewardItem {
	return alignedcmd.MagicBoxRewardItem{
		ItemID:                 30,
		Kind:                   "stackable",
		SlotStart:              65,
		SlotEnd:                120,
		PVFPath:                "stackable/cash/contract_monarch1.stk",
		PremiumType:            premium.TypeOverEquip,
		PremiumDurationSeconds: 86400,
	}
}

func magicBoxContractTestResolution(rewardID int64, material bool) alignedcmd.MagicBoxResolution {
	resolution := alignedcmd.MagicBoxResolution{
		Kind: "random",
		Groups: []alignedcmd.MagicBoxRewardGroup{{
			DrawCount: 1,
			Entries:   []alignedcmd.MagicBoxRewardEntry{{ItemID: rewardID, Weight: 100, Count: 1}},
		}},
	}
	if material {
		resolution.MaterialItemID = 10007367
		resolution.MaterialCountPerUse = 1
	}
	return resolution
}

func magicBoxContractTestCommand() MagicBoxCommand {
	command := magicBoxTestCommand(10, 121)
	command.AccountID = magicBoxContractTestAccountID
	return command
}

// saveMagicBoxFullMainSegment 把主背包 65..120 全部占满，制造"物品栏满"。
func saveMagicBoxFullMainSegment(t *testing.T, ctx context.Context, repos dnfrepo.Group) {
	t.Helper()
	slots := magicBoxBaseSlots()
	for slot := int16(65); slot <= 120; slot++ {
		slots[slotKey(listTypeMain, slot)] = dnfrepo.ItemStack{
			ItemID: 900000 + int64(slot),
			Count:  1,
			Extra:  map[string]string{"item_kind": "stackable"},
		}
	}
	saveMagicBoxFixture(t, ctx, repos, slots)
}

func saveMagicBoxContractAccount(t *testing.T, ctx context.Context, repos dnfrepo.Group, expireAt int64) {
	t.Helper()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: magicBoxContractTestAccountID,
		State:     "active",
		Metadata:  map[string]string{premium.MetadataKey(premium.TypeOverEquip): strconv.FormatInt(expireAt, 10)},
	}); err != nil {
		t.Fatalf("save account: %v", err)
	}
}

func TestOwnerApplyMagicBoxContractRewardStacksDurationWhenInventoryIsFull(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveMagicBoxFullMainSegment(t, ctx, repos)
	// 已有同名契约未到期：叠加必须从原到期时间继续，而不是 replace/重新起算。
	const existingExpire = int64(4_100_000_000)
	saveMagicBoxContractAccount(t, ctx, repos, existingExpire)

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	rewards := map[int64]alignedcmd.MagicBoxRewardItem{30: magicBoxContractRewardItem()}
	result, err := owner.ApplyMagicBox(
		ctx,
		magicBoxContractTestCommand(),
		staticMagicBoxResolver(magicBoxContractTestResolution(30, true)),
		staticMagicBoxRewardResolver(rewards),
	)
	if err != nil {
		t.Fatalf("ApplyMagicBox: %v", err)
	}
	if !result.Success || !result.Changed {
		t.Fatalf("result = %+v", result)
	}
	// 1) 绝不进邮件
	if result.OverflowMailID != "" {
		t.Fatalf("contract reward was mailed: overflow_mail_id=%s", result.OverflowMailID)
	}
	mailbox, found, err := repos.Mailbox.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	if found && len(mailbox.Mails) != 0 {
		t.Fatalf("mailbox must stay empty for a contract grant: %+v", mailbox.Mails)
	}
	// 2) 时长按 PVF 时长叠加到账号
	if len(result.ContractActivations) != 1 {
		t.Fatalf("contract activations = %+v", result.ContractActivations)
	}
	activation := result.ContractActivations[0]
	if activation.PremiumType != premium.TypeOverEquip || activation.Units != 1 ||
		activation.DurationSeconds != 86400 || activation.ExpireAt != existingExpire+86400 {
		t.Fatalf("activation = %+v, want type=22 units=1 duration=86400 expire=%d",
			activation, existingExpire+86400)
	}
	account, found, err := repos.Account.Load(ctx, magicBoxContractTestAccountID)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if got := premium.ExpireAt(account, premium.TypeOverEquip); got != existingExpire+86400 {
		t.Fatalf("account expire = %d, want %d", got, existingExpire+86400)
	}
	// 3) 不占格：背包里既没有契约行，也没有任何一行被改动
	loaded, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	for key, stack := range loaded.Slots {
		if stack.ItemID == 30 {
			t.Fatalf("contract item row %s was created: %+v", key, stack)
		}
	}
	if loaded.Slots["0:121"].Count != 2 {
		t.Fatalf("material was not consumed exactly once: %+v", loaded.Slots["0:121"])
	}
	if _, found := loaded.Slots["0:10"]; found {
		t.Fatalf("box slot must be consumed")
	}
	// 4) 契约没有背包行 → 结果行里不能出现（当前 EXE 的结果行需要真实槽位）
	if len(result.Rewards) != 0 || len(result.DisplayRewards) != 0 {
		t.Fatalf("contract reward leaked into the result rows: rewards=%+v display=%+v",
			result.Rewards, result.DisplayRewards)
	}
}

func TestOwnerApplyMagicBoxContractRewardDoesNotOccupyASlotWithRoomLeft(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveMagicBoxFixture(t, ctx, repos, magicBoxBaseSlots())
	saveMagicBoxContractAccount(t, ctx, repos, 0)

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	resolution := alignedcmd.MagicBoxResolution{
		Kind: "random",
		Groups: []alignedcmd.MagicBoxRewardGroup{
			{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: 30, Weight: 100, Count: 1}}},
			{DrawCount: 1, Entries: []alignedcmd.MagicBoxRewardEntry{{ItemID: 2600014, Weight: 100, Count: 2}}},
		},
		MaterialItemID:      10007367,
		MaterialCountPerUse: 1,
	}
	rewards := map[int64]alignedcmd.MagicBoxRewardItem{
		30:      magicBoxContractRewardItem(),
		2600014: {ItemID: 2600014, Kind: "stackable", SlotStart: 65, SlotEnd: 120, PVFPath: "stackable/professional/potion/ptn_instantmovement.stk"},
	}
	result, err := owner.ApplyMagicBox(ctx, magicBoxContractTestCommand(), staticMagicBoxResolver(resolution), staticMagicBoxRewardResolver(rewards))
	if err != nil {
		t.Fatalf("ApplyMagicBox: %v", err)
	}
	if !result.Success || result.OverflowMailID != "" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.ContractActivations) != 1 || len(result.Rewards) != 1 || result.Rewards[0].ItemID != 2600014 {
		t.Fatalf("contract must not take a slot while ordinary rewards still grant: %+v", result)
	}
	loaded, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	for key, stack := range loaded.Slots {
		if stack.ItemID == 30 {
			t.Fatalf("contract took slot %s even though the bag had room: %+v", key, stack)
		}
	}
	if potion := loaded.Slots["0:65"]; potion.ItemID != 2600014 || potion.Count != 2 {
		t.Fatalf("ordinary reward missing: %+v", loaded.Slots)
	}
	account, _, err := repos.Account.Load(ctx, magicBoxContractTestAccountID)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if got := premium.ExpireAt(account, premium.TypeOverEquip); got == 0 {
		t.Fatalf("account contract was not extended: %+v", account.Metadata)
	}
}

// 账号仓储不可用时必须退回旧行为（照常入包），绝不因为分流不可用而丢奖励。
func TestOwnerApplyMagicBoxContractRewardFallsBackToInventoryWithoutAccountRepository(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveMagicBoxFixture(t, ctx, repos, magicBoxBaseSlots())
	repos.Account = nil

	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	rewards := map[int64]alignedcmd.MagicBoxRewardItem{30: magicBoxContractRewardItem()}
	result, err := owner.ApplyMagicBox(
		ctx,
		magicBoxContractTestCommand(),
		staticMagicBoxResolver(magicBoxContractTestResolution(30, true)),
		staticMagicBoxRewardResolver(rewards),
	)
	if err != nil {
		t.Fatalf("ApplyMagicBox: %v", err)
	}
	if len(result.ContractActivations) != 0 {
		t.Fatalf("no account repository must not produce activations: %+v", result.ContractActivations)
	}
	loaded, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	if stack := loaded.Slots["0:65"]; stack.ItemID != 30 || stack.Count != 1 {
		t.Fatalf("contract reward must fall back to the inventory: %+v", loaded.Slots)
	}
}
