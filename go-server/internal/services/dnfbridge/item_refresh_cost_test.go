package dnfbridge

// 本文件量化"开盒子 / 穿时装"这类高频物品操作在服务端一侧的仓储成本。
//
// 为什么需要它：操作者报告的两个症状（开盒子卡顿、穿时装卡顿）都体现为
// "一次操作 → 一次或多次容器全量重建"。要判断这是不是服务端缺陷，必须先知道
// **每一次容器重建到底打了几次仓储、开了几个事务**，而不是靠感觉。
//
// 这里的计数器包住仓储接口，统计 Load / Save / 事务边界调用次数；不改变被测
// 代码的任何行为（只读代理）。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// repoCallCounter 统计一次操作里的仓储调用。scope 用 "接口.方法" 命名。
type repoCallCounter struct {
	mu    sync.Mutex
	calls map[string]int
}

func newRepoCallCounter() *repoCallCounter {
	return &repoCallCounter{calls: map[string]int{}}
}

func (c *repoCallCounter) add(scope string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.calls[scope]++
	c.mu.Unlock()
}

func (c *repoCallCounter) total(prefix string) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	sum := 0
	for scope, count := range c.calls {
		if strings.HasPrefix(scope, prefix) {
			sum += count
		}
	}
	return sum
}

func (c *repoCallCounter) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.calls = map[string]int{}
	c.mu.Unlock()
}

func (c *repoCallCounter) summary() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	scopes := make([]string, 0, len(c.calls))
	for scope := range c.calls {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	parts := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		parts = append(parts, fmt.Sprintf("%s=%d", scope, c.calls[scope]))
	}
	return strings.Join(parts, " ")
}

type countingInventoryRepository struct {
	dnfrepo.InventoryRepository
	c     *repoCallCounter
	scope string
}

func (r countingInventoryRepository) Load(ctx context.Context, key string) (dnfrepo.InventoryRecord, bool, error) {
	r.c.add(r.scope + ".Load")
	return r.InventoryRepository.Load(ctx, key)
}

func (r countingInventoryRepository) Save(ctx context.Context, record dnfrepo.InventoryRecord) error {
	r.c.add(r.scope + ".Save")
	return r.InventoryRepository.Save(ctx, record)
}

type countingAccountInventoryRepository struct {
	dnfrepo.AccountInventoryRepository
	c     *repoCallCounter
	scope string
}

func (r countingAccountInventoryRepository) Load(ctx context.Context, key string) (dnfrepo.AccountInventoryRecord, bool, error) {
	r.c.add(r.scope + ".Load")
	return r.AccountInventoryRepository.Load(ctx, key)
}

func (r countingAccountInventoryRepository) Save(ctx context.Context, record dnfrepo.AccountInventoryRecord) error {
	r.c.add(r.scope + ".Save")
	return r.AccountInventoryRepository.Save(ctx, record)
}

type countingCharacterRepository struct {
	dnfrepo.CharacterRepository
	c     *repoCallCounter
	scope string
}

func (r countingCharacterRepository) Load(ctx context.Context, key string) (dnfrepo.CharacterRecord, bool, error) {
	r.c.add(r.scope + ".Load")
	return r.CharacterRepository.Load(ctx, key)
}

func (r countingCharacterRepository) Save(ctx context.Context, record dnfrepo.CharacterRecord) error {
	r.c.add(r.scope + ".Save")
	return r.CharacterRepository.Save(ctx, record)
}

type countingEquipmentRepository struct {
	dnfrepo.EquipmentRepository
	c     *repoCallCounter
	scope string
}

func (r countingEquipmentRepository) Load(ctx context.Context, key string) (dnfrepo.EquipmentRecord, bool, error) {
	r.c.add(r.scope + ".Load")
	return r.EquipmentRepository.Load(ctx, key)
}

func (r countingEquipmentRepository) Save(ctx context.Context, record dnfrepo.EquipmentRecord) error {
	r.c.add(r.scope + ".Save")
	return r.EquipmentRepository.Save(ctx, record)
}

type countingSettingsRepository struct {
	dnfrepo.SettingsRepository
	c     *repoCallCounter
	scope string
}

func (r countingSettingsRepository) Load(ctx context.Context, key string) (dnfrepo.SettingsRecord, bool, error) {
	r.c.add(r.scope + ".Load")
	return r.SettingsRepository.Load(ctx, key)
}

func (r countingSettingsRepository) Save(ctx context.Context, record dnfrepo.SettingsRecord) error {
	r.c.add(r.scope + ".Save")
	return r.SettingsRepository.Save(ctx, record)
}

type countingAccountRepository struct {
	dnfrepo.AccountRepository
	c     *repoCallCounter
	scope string
}

func (r countingAccountRepository) Load(ctx context.Context, key string) (dnfrepo.AccountRecord, bool, error) {
	r.c.add(r.scope + ".Load")
	return r.AccountRepository.Load(ctx, key)
}

func (r countingAccountRepository) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	r.c.add(r.scope + ".Save")
	return r.AccountRepository.Save(ctx, record)
}

// countingAccountAssets 统计 AccountCharacterAssetUnitOfWork 事务边界的进入次数，
// 以及事务内部真正打出去的 Load / Save。
type countingAccountAssets struct {
	inner dnfrepo.AccountCharacterAssetUnitOfWork
	c     *repoCallCounter
}

func (u countingAccountAssets) WithinAccountCharacterAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	u.c.add("AccountAssets.Begin")
	return u.inner.WithinAccountCharacterAssets(ctx, accountID, characterID, func(
		account dnfrepo.AccountInventoryRepository,
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		return apply(
			countingAccountInventoryRepository{AccountInventoryRepository: account, c: u.c, scope: "AccountAssets.accountInventory"},
			countingCharacterRepository{CharacterRepository: characters, c: u.c, scope: "AccountAssets.character"},
			countingInventoryRepository{InventoryRepository: inventories, c: u.c, scope: "AccountAssets.inventory"},
			countingEquipmentRepository{EquipmentRepository: equipment, c: u.c, scope: "AccountAssets.equipment"},
		)
	})
}

// countingRentalAssets 同上是 RentalAssetUnitOfWork 的计数代理。
type countingRentalAssets struct {
	inner dnfrepo.RentalAssetUnitOfWork
	c     *repoCallCounter
}

func (u countingRentalAssets) WithinRentalAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(dnfrepo.AccountRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	u.c.add("RentalAssets.Begin")
	return u.inner.WithinRentalAssets(ctx, accountID, characterID, func(
		account dnfrepo.AccountRepository,
		characters dnfrepo.CharacterRepository,
		inventories dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		return apply(
			countingAccountRepository{AccountRepository: account, c: u.c, scope: "RentalAssets.account"},
			countingCharacterRepository{CharacterRepository: characters, c: u.c, scope: "RentalAssets.character"},
			countingInventoryRepository{InventoryRepository: inventories, c: u.c, scope: "RentalAssets.inventory"},
			countingEquipmentRepository{EquipmentRepository: equipment, c: u.c, scope: "RentalAssets.equipment"},
		)
	})
}

// countingCharacterItems 统计穿脱/背包移动用到的角色物品事务。
type countingCharacterItems struct {
	inner dnfrepo.CharacterItemUnitOfWork
	c     *repoCallCounter
}

func (u countingCharacterItems) WithinCharacterItems(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	u.c.add("CharacterItems.Begin")
	return u.inner.WithinCharacterItems(ctx, characterID, func(inventories dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		return apply(
			countingInventoryRepository{InventoryRepository: inventories, c: u.c, scope: "CharacterItems.inventory"},
			countingEquipmentRepository{EquipmentRepository: equipment, c: u.c, scope: "CharacterItems.equipment"},
		)
	})
}

// countingRepositoryGroup 把整组仓储换成计数代理。store 上的 Load/Save 也被计入，
// 这样"事务外读"和"事务内读"可以分开看。
func countingRepositoryGroup(group dnfrepo.Group, counter *repoCallCounter) dnfrepo.Group {
	counted := group
	counted.Account = countingAccountRepository{AccountRepository: group.Account, c: counter, scope: "Account"}
	counted.AccountInventory = countingAccountInventoryRepository{AccountInventoryRepository: group.AccountInventory, c: counter, scope: "AccountInventory"}
	counted.Character = countingCharacterRepository{CharacterRepository: group.Character, c: counter, scope: "Character"}
	counted.Inventory = countingInventoryRepository{InventoryRepository: group.Inventory, c: counter, scope: "Inventory"}
	counted.Equipment = countingEquipmentRepository{EquipmentRepository: group.Equipment, c: counter, scope: "Equipment"}
	counted.Settings = countingSettingsRepository{SettingsRepository: group.Settings, c: counter, scope: "Settings"}
	if group.AccountAssets != nil {
		counted.AccountAssets = countingAccountAssets{inner: group.AccountAssets, c: counter}
	}
	if group.RentalAssets != nil {
		counted.RentalAssets = countingRentalAssets{inner: group.RentalAssets, c: counter}
	}
	if group.CharacterItems != nil {
		counted.CharacterItems = countingCharacterItems{inner: group.CharacterItems, c: counter}
	}
	return counted
}

// refreshCostFixture 是"一个真实体量的 90 级角色"的容器数据：主背包 45 行、
// 装扮栏 32 行、仓库 12 行、账号金库 3 行、已穿戴 10 件。数字取自 2026-09-25
// 香港现场抓包（op13 list0 entry_count=45 / list1 entry_count=32）。
type refreshCostFixture struct {
	group   dnfrepo.Group
	service *Service
	session *gameSession
	buffer  *bufferConn
	counter *repoCallCounter
}

func newRefreshCostFixture(t testing.TB) *refreshCostFixture {
	return newRefreshCostFixtureWithContracts(t, map[int64]currentPremiumContractInfo{
		490701999: {ItemID: 490701999, PremiumType: 22, DurationSeconds: 259200},
	})
}

// newRefreshCostFixtureWithContracts 允许指定运行时 PVF 契约表。默认那份故意
// 不与背包任何物品匹配（生产的常态：契约表非空、背包里没有契约道具）。
func newRefreshCostFixtureWithContracts(t testing.TB, contracts map[int64]currentPremiumContractInfo) *refreshCostFixture {
	t.Helper()
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()

	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":                       "",
		"stackable/stackable.lst":                   "3227 `material/solvent_magicpower.stk`\n",
		"equipment/equipment.lst":                   "9001 `weapon/test_sword.equ`\n",
		"stackable/material/solvent_magicpower.stk": "[name] `Magic Solvent`\n[stackable type] `[material]`\n[stack limit] 999\n",
		"equipment/weapon/test_sword.equ":           "[name] `Test Sword`\n[durability] 57\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := group.Account.Save(ctx, dnfrepo.AccountRecord{
		AccountID: "dnf:1",
		Metadata:  map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "dnf:1",
		Name:        "Actor19",
		Job:         "15",
		Level:       90,
		Stats:       map[string]int64{"gold": 123456},
	}); err != nil {
		t.Fatal(err)
	}
	slots := map[string]dnfrepo.ItemStack{}
	for slot := 1; slot <= 44; slot++ {
		slots[fmt.Sprintf("0:%d", slot)] = dnfrepo.ItemStack{ItemID: 3227, Count: int64(slot + 1)}
	}
	for slot := 121; slot <= 152; slot++ {
		slots[fmt.Sprintf("1:%d", slot)] = dnfrepo.ItemStack{ItemID: 3227, Count: 1}
	}
	warehouse := map[string]dnfrepo.ItemStack{}
	for slot := 1; slot <= 12; slot++ {
		warehouse[fmt.Sprintf("2:%d", slot)] = dnfrepo.ItemStack{ItemID: 3227, Count: 3}
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: slots, Warehouse: warehouse}); err != nil {
		t.Fatal(err)
	}
	if err := group.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: "dnf:1",
		Slots: map[string]dnfrepo.ItemStack{
			"0:354": {ItemID: 3033, Count: 13},
			"0:365": {ItemID: 10158124, Count: 14},
			"0:355": {ItemID: 3034, Count: 2},
		},
	}); err != nil {
		t.Fatal(err)
	}
	entries := map[string]dnfrepo.EquipmentEntry{}
	for slot := 0; slot < 10; slot++ {
		entries[fmt.Sprintf("%d", slot)] = dnfrepo.EquipmentEntry{SlotIndex: int16(slot), ItemID: 9001}
	}
	if err := group.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "19", Entries: entries}); err != nil {
		t.Fatal(err)
	}

	counter := newRepoCallCounter()
	counted := countingRepositoryGroup(group, counter)
	buffer := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "dnf:1",
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return counted, true },
		pvfItemCatalog:     catalog,
		// 生产环境 etc/premiumlist_new.etc 一定解析出非空的契约表，于是主背包
		// 快照本来每次都会进一次 RentalAssets 事务做"契约道具自动激活"检查。
		// 这里注入的契约 ID 由调用方给定：默认那份不与背包任何物品匹配
		// （=生产的常态：表非空、背包里没有契约道具）。
		premiumCatalog: &currentPremiumCatalog{contractsByItem: contracts},
	}
	session := &gameSession{
		conn:                            buffer,
		connID:                          "item-refresh-cost",
		channel:                         channelcatalog.Channel{ID: 19},
		selectedCharacterID:             19,
		initialTownRouteCharacterID:     19,
		initialTownRouteStage:           currentInitialTownRoutePlayerStateSent,
		townSceneReadyCharacterID:       19,
		connectionTownActorOwnerChannel: 19,
		townActorOwnerChannel:           19,
	}
	return &refreshCostFixture{group: counted, service: service, session: session, buffer: buffer, counter: counter}
}

// warmUp 跑一次同样的路径，把"每会话一次"的迁移/首次建缓存排除在测量之外，
// 这样后面测到的就是**稳态下每一次操作**的成本（高频操作关心的正是这个）。
func (f *refreshCostFixture) warmUp(t *testing.T) {
	t.Helper()
	if _, _, _, ok := f.service.buildCurrentItemListBodyForSession(context.Background(), f.session, dnfrepo.MainInventoryListType); !ok {
		t.Fatal("warm-up main-inventory build failed")
	}
	if err := f.service.sendSelectedCurrentContainerListsWithRefresh(f.session, "warm_up", true); err != nil {
		t.Fatal(err)
	}
	f.counter.reset()
	f.buffer.write.Reset()
}

// TestMainInventorySnapshotRepositoryCostPerOperation 钉住"重建一次主背包快照"
// 的仓储成本。这里的数字就是操作者感受到的卡顿来源：每一次开盒子/穿时装都要
// 重建主背包，而重建本身在稳态下仍在跑修复性事务。
func TestMainInventorySnapshotRepositoryCostPerOperation(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)

	body, source, count, ok := fixture.service.buildCurrentItemListBodyForSession(context.Background(), fixture.session, dnfrepo.MainInventoryListType)
	if !ok {
		t.Fatal("main-inventory build failed")
	}
	t.Logf("main-inventory snapshot: rows=%d body_len=%d source=%s", count, len(body), source)
	t.Logf("repository calls per main-inventory snapshot: %s", fixture.counter.summary())
	t.Logf("totals: loads=%d saves=%d transactions=%d",
		fixture.counter.total("Inventory.Load")+
			fixture.counter.total("AccountInventory.Load")+
			fixture.counter.total("Character.Load")+
			fixture.counter.total("Equipment.Load")+
			fixture.counter.total("Settings.Load")+
			fixture.counter.total("Account.Load")+
			fixture.counter.total("AccountAssets.")+
			fixture.counter.total("RentalAssets."),
		fixture.counter.total(".Save"),
		fixture.counter.total("AccountAssets.Begin")+fixture.counter.total("RentalAssets.Begin")+fixture.counter.total("CharacterItems.Begin"),
	)
	// 空集门：背包里没有契约道具时，RentalAssets 事务必须不再被打开。
	if got := fixture.counter.total("RentalAssets.Begin"); got != 0 {
		t.Fatalf("RentalAssets transactions without a contract stack in the bag = %d, want 0", got)
	}
	// 修复性 AccountAssets 事务仍在（本批未改，见测试注释与 changelog）。
	if got := fixture.counter.total("AccountAssets.Begin"); got != 1 {
		t.Fatalf("AccountAssets repair transactions = %d, want 1 (unchanged by the premium gate)", got)
	}
}

// TestMainInventorySnapshotStillActivatesContractStackInBag 是空集门的反方向：
// 背包里真有契约堆叠时，事务必须照旧进入，并把契约折算成账号时长、删掉背包行。
func TestMainInventorySnapshotStillActivatesContractStackInBag(t *testing.T) {
	// 道具 3227 是这份夹具背包里的实际物品（主背包 44 行都用它）。
	fixture := newRefreshCostFixtureWithContracts(t, map[int64]currentPremiumContractInfo{
		3227: {ItemID: 3227, PremiumType: 22, DurationSeconds: 3600},
	})
	if _, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(context.Background(), fixture.session, dnfrepo.MainInventoryListType); !ok {
		t.Fatal("first main-inventory build failed")
	}
	inventory, found, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	for slot := 1; slot <= 44; slot++ {
		if _, exists := inventory.Slots[fmt.Sprintf("0:%d", slot)]; exists {
			t.Fatalf("contract stack 0:%d survived activation", slot)
		}
	}
	account, found, err := fixture.group.Account.Load(context.Background(), "dnf:1")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if got := fixture.counter.total("RentalAssets.Begin"); got != 1 {
		t.Fatalf("RentalAssets transactions with a contract stack in the bag = %d, want 1", got)
	}
	if expire := premium.ExpireAt(account, 22); expire <= 0 {
		t.Fatal("contract stack was removed from the bag without extending the account entitlement")
	}
}

// BenchmarkMainInventorySnapshotRebuild 是"一次开盒子/穿时装触发的容器重建"的
// 纯 Go 成本（不含 MySQL 往返）：内存仓储、48 行主背包、10 件装备、12 行仓库。
func BenchmarkMainInventorySnapshotRebuild(b *testing.B) {
	fixture := newRefreshCostFixture(b)
	if _, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(context.Background(), fixture.session, dnfrepo.MainInventoryListType); !ok {
		b.Fatal("warm-up build failed")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, ok := fixture.service.buildCurrentItemListBodyForSession(context.Background(), fixture.session, dnfrepo.MainInventoryListType); !ok {
			b.Fatal("build failed")
		}
	}
}

// TestWearMovePostActionRepositoryCost 是"穿/脱一件时装或装备"（class1/op19）
// 结算后的成本，与 inventory/handler.go 的 ownerAppliedEquipmentMoveResult /
// ownerAppliedPetEquipmentMoveResult 逐字节同形：ACK + 穿脱专用整容器动作 +
// 外观刷新。DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH 默认档必须一个整容器都不发。
func TestWearMovePostActionRepositoryCost(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)

	result := alignedcmd.Result{
		Operation: "move_itemspace",
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          19,
			Body:           []byte{1, 0, 9, 0, 1, 0, 0, 0, 3, 12, 0},
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{
			alignedcmd.PostActionRefreshSelectedWearContainers,
			alignedcmd.PostActionRefreshSelectedActorAppearance,
		},
	}
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatalf("wear post action error = %v", err)
	}
	containerPackets := 0
	appearancePackets := 0
	for stream := fixture.buffer.write.Bytes(); len(stream) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		switch {
		case packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) && packet.Header.Classification == 0:
			containerPackets++
		case packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && packet.Header.Classification == 0:
			appearancePackets++
		}
		stream = rest
	}
	t.Logf("wear move: container packets=%d appearance packets=%d total bytes=%d", containerPackets, appearancePackets, fixture.buffer.write.Len())
	t.Logf("repository calls per wear move: %s", fixture.counter.summary())
	t.Logf("totals: loads=%d saves=%d transactions=%d",
		fixture.counter.total(".Load"), fixture.counter.total(".Save"),
		fixture.counter.total("AccountAssets.Begin")+fixture.counter.total("RentalAssets.Begin")+fixture.counter.total("CharacterItems.Begin"))
	if containerPackets != 0 {
		t.Fatalf("wear move emitted %d full class0/op13 containers; the suppressed default must emit none", containerPackets)
	}
	if appearancePackets != 1 {
		t.Fatalf("wear move appearance packets = %d, want 1 mode0 self projection", appearancePackets)
	}
}

// BenchmarkMagicBoxPostActionRefresh 是"一次开盒子 = ACK + 5 个整容器"的完整
// 服务端发送成本（内存仓储）。
func BenchmarkMagicBoxPostActionRefresh(b *testing.B) {
	fixture := newRefreshCostFixture(b)
	if err := fixture.service.sendSelectedCurrentContainerListsWithRefresh(fixture.session, "warm_up", true); err != nil {
		b.Fatal(err)
	}
	result := alignedcmd.Result{
		Operation: "use_magic_box",
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          uint16(dnfenum.CmdPacketUseRandomboxItem),
			Body:           []byte{1, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedItemContainers},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fixture.buffer.write.Reset()
		if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
			b.Fatal(err)
		}
	}
}

// TestMagicBoxPostActionRepositoryCost 覆盖开盒子（class1/op208、op1128 礼盒）
// 结算后的那一个 PostActionRefreshSelectedItemContainers：它与
// packageitem/handler.go 的 ownerAppliedMagicBoxResult 逐字节同形。
func TestMagicBoxPostActionRepositoryCost(t *testing.T) {
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)

	result := alignedcmd.Result{
		Operation: "use_magic_box",
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          uint16(dnfenum.CmdPacketUseRandomboxItem),
			Body:           []byte{1, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedItemContainers},
	}
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatalf("magic-box post action error = %v", err)
	}
	containerPackets := 0
	for stream := fixture.buffer.write.Bytes(); len(stream) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketLeaveParty) && packet.Header.Classification == 0 {
			containerPackets++
		}
		stream = rest
	}
	t.Logf("magic-box open: container packets=%d bytes=%d", containerPackets, fixture.buffer.write.Len())
	t.Logf("repository calls per magic-box open: %s", fixture.counter.summary())
	t.Logf("totals: loads=%d saves=%d transactions=%d",
		fixture.counter.total(".Load"), fixture.counter.total(".Save"),
		fixture.counter.total("AccountAssets.Begin")+fixture.counter.total("RentalAssets.Begin")+fixture.counter.total("CharacterItems.Begin"))
	if containerPackets != len(currentSelectedItemListTypes) {
		t.Fatalf("magic-box container packets = %d, want %d (wire behaviour pinned)", containerPackets, len(currentSelectedItemListTypes))
	}
	if got := fixture.counter.total("RentalAssets.Begin"); got != 0 {
		t.Fatalf("RentalAssets transactions per box open without a contract stack = %d, want 0", got)
	}
}
