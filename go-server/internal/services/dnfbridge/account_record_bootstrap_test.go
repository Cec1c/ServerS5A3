package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 回归背景（现场逐条见 account_record_bootstrap.go 头部注释）：
//
//	2026-09-25 香港 conn_id=game-000014
//	16:37:44.733 RECV msg_id=4 ENUM_CMDPACKET_SELECT_CHARACTER（选角）
//	16:37:45.042 WARN  handle game packet failed
//	    error="initialize selected town actor before transition:
//	           load crystal contract state: crystal contract account was not found"
//	    ← 同一毫秒 game-close（服务端关连接）⇒ 客户端"一选角色就网络连接中断"
//
// 缺的不是角色数据，而是目标账号的账号级行（角色迁移只改了
// dnf_characters.account_id）。下面第一个用例把"缺账号行 = 断线点"逐字钉住，
// 第二个用例钉住"修好之后选角 + 进场景不再中断"。

const accountRecordBootstrapTestAccountID = "dnf:10013"

type failingAccountStore struct {
	dnfrepo.AccountRepository
	err error
}

func (s failingAccountStore) Load(context.Context, string) (dnfrepo.AccountRecord, bool, error) {
	return dnfrepo.AccountRecord{}, false, s.err
}

func (s failingAccountStore) Save(context.Context, dnfrepo.AccountRecord) error {
	return s.err
}

type racingBootstrapAccountStore struct {
	dnfrepo.AccountRepository
	loadCalls     int
	createCalls   int
	fullSaveCalls int
	forceMissing  bool
}

// Load models the stale pre-check that lost the race: another writer has
// already committed, while this callback still observes found=false.
func (s *racingBootstrapAccountStore) Load(ctx context.Context, accountID string) (dnfrepo.AccountRecord, bool, error) {
	s.loadCalls++
	if s.forceMissing {
		return dnfrepo.AccountRecord{}, false, nil
	}
	return s.AccountRepository.Load(ctx, accountID)
}

func (s *racingBootstrapAccountStore) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	s.fullSaveCalls++
	return s.AccountRepository.Save(ctx, record)
}

func (s *racingBootstrapAccountStore) CreateIfAbsent(ctx context.Context, record dnfrepo.AccountRecord) (bool, error) {
	s.createCalls++
	_, found, err := s.AccountRepository.Load(ctx, record.AccountID)
	if err != nil || found {
		return false, err
	}
	return true, s.AccountRepository.Save(ctx, record)
}

type racingBootstrapAccountInventoryStore struct {
	dnfrepo.AccountInventoryRepository
	loadCalls int
	saveCalls int
}

func (s *racingBootstrapAccountInventoryStore) Load(context.Context, string) (dnfrepo.AccountInventoryRecord, bool, error) {
	s.loadCalls++
	return dnfrepo.AccountInventoryRecord{}, false, nil
}

func (s *racingBootstrapAccountInventoryStore) Save(ctx context.Context, record dnfrepo.AccountInventoryRecord) error {
	s.saveCalls++
	return s.AccountInventoryRepository.Save(ctx, record)
}

func newAccountRecordBootstrapFixture(t *testing.T) (*Service, dnfrepo.Group) {
	t.Helper()
	repositories := testRepositoryGroup()
	// 迁移后的实际形态：角色已经挂在目标启动器账号上（character_id 全库唯一，
	// 只改 account_id），目标账号在 dnf_accounts / dnf_account_inventories
	// 里没有任何行。
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "10",
		AccountID:   accountRecordBootstrapTestAccountID,
		Slot:        0,
		Name:        "sample-account-a",
		Job:         "3",
		Level:       16,
		Stats: map[string]int64{
			"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234,
			"direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "10",
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options: options{accountPrefix: defaultAccountPrefix},
		premiumCatalog: &currentPremiumCatalog{
			contractsByItem: make(map[int64]currentPremiumContractInfo),
			devilSlots:      make(map[uint32]currentPremiumDevilSlotInfo),
			crystalCubeIDs:  [6]int64{3033, 3034, 3035, 3036, 3037, 3262},
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	return service, repositories
}

func newAccountRecordBootstrapSession() *gameSession {
	channel := channelcatalog.Channel{ServerID: 1, ID: 252, Type: 40, Name: "ch.252", Port: 10252}
	return &gameSession{
		conn:            &bufferConn{},
		connID:          "account-record-bootstrap",
		channel:         channel,
		residentChannel: channel,
		accountID:       accountRecordBootstrapTestAccountID,
	}
}

// selectFirstSlotRequest 复现现场那一条选角请求：wire body 2 字节 = 0（槽 0），
// 日志里就是 request_slot=0 selected_by=slot。
func selectFirstSlotRequest() []byte {
	request := make([]byte, 2)
	binary.LittleEndian.PutUint16(request, 0)
	return request
}

func TestMissingAccountLevelRowIsTheSelectedCharacterEnterAbortPoint(t *testing.T) {
	// 这一半是"修复前"的现场：账号行缺失时，进场景链路上的第一步强依赖
	// （晶体契约 op898 状态）就会返回错误。该错误在 game.go:379-384 被
	// read loop 当作致命错误直接关连接 —— 这就是玩家的"网络连接中断"。
	service, repositories := newAccountRecordBootstrapFixture(t)
	session := newAccountRecordBootstrapSession()
	session.selectedCharacterID = 10

	err := service.sendCurrentCrystalContractStateOnce(session, "reproduce_missing_account_row")
	if err == nil {
		t.Fatal("missing account row no longer aborts the enter path; this test pinned the 2026-09-25 failure")
	}
	if !strings.Contains(err.Error(), "crystal contract account was not found") {
		t.Fatalf("unexpected abort reason: %v", err)
	}
	if _, found, loadErr := repositories.Account.Load(context.Background(), accountRecordBootstrapTestAccountID); loadErr != nil || found {
		t.Fatalf("the failing step must not create the account row by itself: found=%t err=%v", found, loadErr)
	}
}

func TestSelectedCharacterEnterCreatesMissingAccountRecordsAndKeepsTheSessionAlive(t *testing.T) {
	service, repositories := newAccountRecordBootstrapFixture(t)
	session := newAccountRecordBootstrapSession()
	ctx := context.Background()

	// 选角：这是"本会话要用哪个账号的角色池"定案的时刻，账号级记录在这里补齐。
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("select character aborted the session: %v", err)
	}
	if session.selectedCharacterID != 10 {
		t.Fatalf("selected character = %d, want 10", session.selectedCharacterID)
	}

	account, found, err := repositories.Account.Load(ctx, accountRecordBootstrapTestAccountID)
	if err != nil || !found {
		t.Fatalf("account row was not created on demand: found=%t err=%v", found, err)
	}
	if account.State != accountRecordBootstrapState {
		t.Fatalf("created account state = %q, want %q", account.State, accountRecordBootstrapState)
	}
	// 唯一键 uk_dnf_accounts_represent_account_name：绝不能凭空写一个冒险团名字。
	if strings.TrimSpace(account.RepresentAccountName) != "" {
		t.Fatalf("created account invented a represent name: %q", account.RepresentAccountName)
	}
	if account.Metadata["source"] != "dnfbridge" {
		t.Fatalf("created account metadata source = %q, want dnfbridge", account.Metadata["source"])
	}
	if account.Metadata[accountRecordBootstrapMetadataKey] != "true" ||
		account.Metadata[accountRecordBootstrapSourceMetadataKey] != accountRecordBootstrapSelectSource {
		t.Fatalf("created account is not marked as a bootstrap row: %#v", account.Metadata)
	}
	if account.CreatedAt.IsZero() {
		t.Fatal("created account has no created_at")
	}
	// 账号容器完全惰性创建：缺失读取方按空容器处理，bootstrap 不写空壳，
	// 因而不会与并发的首条共享物品写入互相覆盖。
	if _, inventoryFound, inventoryErr := repositories.AccountInventory.Load(ctx, accountRecordBootstrapTestAccountID); inventoryErr != nil {
		t.Fatalf("load account inventory: %v", inventoryErr)
	} else if inventoryFound {
		t.Fatal("bootstrap must leave the account inventory absent until its first real writer")
	}

	// 现场断线那一步：账号行补齐之后必须成功，会话不再被关掉。
	if err := service.sendCurrentCrystalContractStateOnce(session, "after_select_before_scene_enter"); err != nil {
		t.Fatalf("crystal contract state still aborts the connection: %v", err)
	}
	if connection, ok := session.conn.(*bufferConn); !ok || connection.write.Len() == 0 {
		t.Fatal("no packet was written for the repaired enter path")
	}
}

func TestSelectedCharacterEnterNeverRewritesAnExistingAccountRecord(t *testing.T) {
	service, repositories := newAccountRecordBootstrapFixture(t)
	session := newAccountRecordBootstrapSession()
	ctx := context.Background()

	// 已有账号行（正常账号的真实档案）：必须一个字节都不动。
	original := dnfrepo.AccountRecord{
		AccountID:            "dnf:10010",
		State:                "active",
		HonorExp:             74479,
		RepresentAccountName: "浩成家族",
		Metadata:             map[string]string{"source": "ops-audit", "account_cargo_created": "true"},
	}
	if err := repositories.Account.Save(ctx, original); err != nil {
		t.Fatal(err)
	}
	session.accountID = "dnf:10010"
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "9",
		AccountID:   "dnf:10010",
		Slot:        0,
		Name:        "烧B",
		Job:         "14",
		Level:       90,
		Stats:       map[string]int64{"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "9",
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("select character aborted the session: %v", err)
	}

	after, found, err := repositories.Account.Load(ctx, "dnf:10010")
	if err != nil || !found {
		t.Fatalf("existing account row disappeared: found=%t err=%v", found, err)
	}
	if after.State != original.State || after.HonorExp != original.HonorExp ||
		after.RepresentAccountName != original.RepresentAccountName {
		t.Fatalf("existing account row was rewritten: %#v", after)
	}
	if after.Metadata["source"] != "ops-audit" || after.Metadata["account_cargo_created"] != "true" {
		t.Fatalf("existing account metadata was rewritten: %#v", after.Metadata)
	}
	if _, bootstrapped := after.Metadata[accountRecordBootstrapMetadataKey]; bootstrapped {
		t.Fatalf("existing account row was marked as bootstrapped: %#v", after.Metadata)
	}
}

func TestAccountRecordBootstrapCreateIfAbsentPreservesConcurrentWinnerMetadata(t *testing.T) {
	repositories := testRepositoryGroup()
	ctx := context.Background()
	existing := dnfrepo.AccountRecord{
		AccountID: accountRecordBootstrapTestAccountID,
		State:     "active",
		Metadata: map[string]string{
			"keep":                 "yes",
			"raid_fate_balance_v1": "777",
			"raid_fate_week_v1":    "2943",
			"raid_fate_earned_v1":  "345",
		},
	}
	if err := repositories.Account.Save(ctx, existing); err != nil {
		t.Fatal(err)
	}
	racing := &racingBootstrapAccountStore{AccountRepository: repositories.Account, forceMissing: true}
	repositories.Account = racing
	service := &Service{
		options:            options{accountPrefix: defaultAccountPrefix},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := newAccountRecordBootstrapSession()
	if !service.ensureAccountLevelRecords(ctx, repositories, session, accountRecordBootstrapTestAccountID, accountRecordBootstrapLoginSource) {
		t.Fatal("existing account was not recognized as available")
	}
	if racing.loadCalls != 1 || racing.createCalls != 1 || racing.fullSaveCalls != 0 {
		t.Fatalf("bootstrap calls load/create/save=%d/%d/%d", racing.loadCalls, racing.createCalls, racing.fullSaveCalls)
	}
	after, found, err := racing.AccountRepository.Load(ctx, accountRecordBootstrapTestAccountID)
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if after.Metadata["keep"] != "yes" || after.Metadata["raid_fate_balance_v1"] != "777" || after.Metadata["raid_fate_week_v1"] != "2943" || after.Metadata["raid_fate_earned_v1"] != "345" {
		t.Fatalf("concurrent account metadata was replaced: %#v", after.Metadata)
	}
}

func TestAccountRecordBootstrapExistingAccountStaysReadOnly(t *testing.T) {
	repositories := testRepositoryGroup()
	ctx := context.Background()
	existing := dnfrepo.AccountRecord{
		AccountID: accountRecordBootstrapTestAccountID,
		State:     "active",
		Metadata:  map[string]string{"keep": "yes", "raid_fate_balance_v1": "777"},
	}
	if err := repositories.Account.Save(ctx, existing); err != nil {
		t.Fatal(err)
	}
	counting := &racingBootstrapAccountStore{AccountRepository: repositories.Account}
	repositories.Account = counting
	service := &Service{
		options:            options{accountPrefix: defaultAccountPrefix},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	if !service.ensureAccountLevelRecords(ctx, repositories, newAccountRecordBootstrapSession(), accountRecordBootstrapTestAccountID, accountRecordBootstrapLoginSource) {
		t.Fatal("existing account was not recognized")
	}
	if counting.loadCalls != 1 || counting.createCalls != 0 || counting.fullSaveCalls != 0 {
		t.Fatalf("existing-account bootstrap calls load/create/save=%d/%d/%d", counting.loadCalls, counting.createCalls, counting.fullSaveCalls)
	}
	after, found, err := counting.AccountRepository.Load(ctx, accountRecordBootstrapTestAccountID)
	if err != nil || !found || after.Metadata["keep"] != "yes" || after.Metadata["raid_fate_balance_v1"] != "777" {
		t.Fatalf("existing account changed: found=%t account=%+v err=%v", found, after, err)
	}
}

func TestAccountRecordBootstrapUsesCreateIfAbsentForMissingAccount(t *testing.T) {
	repositories := testRepositoryGroup()
	ctx := context.Background()
	if err := repositories.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
		AccountID: accountRecordBootstrapTestAccountID,
		Slots: map[string]dnfrepo.ItemStack{
			"0:1": {ItemID: 1001, Count: 7},
		},
	}); err != nil {
		t.Fatal(err)
	}
	inventory := &racingBootstrapAccountInventoryStore{AccountInventoryRepository: repositories.AccountInventory}
	repositories.AccountInventory = inventory
	racing := &racingBootstrapAccountStore{AccountRepository: repositories.Account}
	repositories.Account = racing
	service := &Service{
		options:            options{accountPrefix: defaultAccountPrefix},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := newAccountRecordBootstrapSession()
	if !service.ensureAccountLevelRecords(ctx, repositories, session, accountRecordBootstrapTestAccountID, accountRecordBootstrapLoginSource) {
		t.Fatal("missing account was not created")
	}
	if racing.loadCalls != 1 || racing.createCalls != 1 || racing.fullSaveCalls != 0 {
		t.Fatalf("bootstrap calls load/create/save=%d/%d/%d", racing.loadCalls, racing.createCalls, racing.fullSaveCalls)
	}
	after, found, err := racing.AccountRepository.Load(ctx, accountRecordBootstrapTestAccountID)
	if err != nil || !found {
		t.Fatalf("load created account found=%t err=%v", found, err)
	}
	if after.Metadata[accountRecordBootstrapMetadataKey] != "true" || after.Metadata[accountRecordBootstrapSourceMetadataKey] != accountRecordBootstrapLoginSource {
		t.Fatalf("created account metadata=%#v", after.Metadata)
	}
	if inventory.loadCalls != 0 || inventory.saveCalls != 0 {
		t.Fatalf("bootstrap touched lazy account inventory load/save=%d/%d", inventory.loadCalls, inventory.saveCalls)
	}
	stored, found, err := inventory.AccountInventoryRepository.Load(ctx, accountRecordBootstrapTestAccountID)
	if err != nil || !found || stored.Slots["0:1"].ItemID != 1001 || stored.Slots["0:1"].Count != 7 {
		t.Fatalf("concurrent account inventory was not preserved: found=%t record=%+v err=%v", found, stored, err)
	}
}

func TestAccountRecordBootstrapFailsOpenWhenTheRepositoryIsUnavailable(t *testing.T) {
	service, repositories := newAccountRecordBootstrapFixture(t)
	session := newAccountRecordBootstrapSession()

	injected := errors.New("injected account repository failure")
	repositories.Account = failingAccountStore{AccountRepository: repositories.Account, err: injected}
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return repositories, true }

	// 补齐失败绝不能被升级成会话级错误：选角照常往下走（后续那一步自己会失败，
	// 那是数据库整体不可用的另一类故障，不在这里兜）。
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("account repository failure aborted the select path: %v", err)
	}
	if session.selectedCharacterID != 10 {
		t.Fatalf("selected character = %d, want 10", session.selectedCharacterID)
	}
}

func TestAccountRecordBootstrapUsesTheSharedFallbackAccountWhenTheSessionIsUnbound(t *testing.T) {
	service, repositories := newAccountRecordBootstrapFixture(t)
	session := newAccountRecordBootstrapSession()
	ctx := context.Background()

	// 未定案会话（旧启动器/没有令牌）：accountIDForSession 落到共享兜底账号。
	// 新装库上共享账号同样可能没有账号行，选角那一刻必须一并补齐，否则第一个
	// 玩家一样会一选角色就断线。
	session.accountID = ""
	session.selectedCharacterID = 0
	fallback := service.accountID()
	if fallback == "" {
		t.Fatal("shared fallback account id is empty")
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "11",
		AccountID:   fallback,
		Slot:        0,
		Name:        "fallback",
		Job:         "0",
		Level:       1,
		Stats:       map[string]int64{"town_id": 38, "area_id": 1, "pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "11",
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("select character aborted the session: %v", err)
	}
	account, found, err := repositories.Account.Load(ctx, fallback)
	if err != nil || !found {
		t.Fatalf("shared fallback account row was not created: found=%t err=%v", found, err)
	}
	if account.Metadata[accountRecordBootstrapSourceMetadataKey] != accountRecordBootstrapSelectSource {
		t.Fatalf("shared fallback bootstrap source = %q", account.Metadata[accountRecordBootstrapSourceMetadataKey])
	}
	if err := service.sendCurrentCrystalContractStateOnce(session, "after_select_before_scene_enter"); err != nil {
		t.Fatalf("crystal contract state aborts the shared-fallback enter path: %v", err)
	}
}

func TestAccountRecordBootstrapIsSharedByBothSelectEntryPoints(t *testing.T) {
	service, repositories := newAccountRecordBootstrapFixture(t)
	session := newAccountRecordBootstrapSession()
	ctx := context.Background()

	// 旧客户端走 legacy 选角入口（11 字节 body：u32 charID + u8 slot + u8 option
	// + 5 字节填充 + flag），同样必须先补齐账号级记录。
	body := make([]byte, 11)
	binary.LittleEndian.PutUint32(body[:4], 10)
	if err := service.sendSelectCharacterState(session, body); err != nil {
		t.Fatalf("legacy select character aborted the session: %v", err)
	}
	if _, found, err := repositories.Account.Load(ctx, accountRecordBootstrapTestAccountID); err != nil || !found {
		t.Fatalf("legacy select entry did not create the account row: found=%t err=%v", found, err)
	}
}
