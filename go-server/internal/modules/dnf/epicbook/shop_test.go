package epicbook

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

const (
	shopTestAccountID   = "shop-account"
	shopTestCharacterID = "shop-character"
	shopTestTargetID    = uint32(105000001)
)

var (
	shopTestMaterials = [5]uint32{103000001, 103000002, 103000003, 103000004, 103000005}
	shopTestNow       = time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC)
)

type shopConfigSource map[string]string

func (s shopConfigSource) ReadText(path string) (string, error) {
	text, ok := s[path]
	if !ok {
		return "", fmt.Errorf("missing %s", path)
	}
	return text, nil
}

var shopNativeConfigValues = map[string]string{
	"npcIndex":                       "1130",
	"epic piece provide":             "5",
	"epic piece received":            "10",
	"gabriel appearance probability": "1",
	"list change chance":             "5",
	"list change cost":               "100000",
	"kind of epic piece":             "5",
	"same type":                      "0",
	"normal type":                    "5",
}

func shopConfigDocument(mutate func(map[string]string)) string {
	values := make(map[string]string, len(shopNativeConfigValues))
	for key, value := range shopNativeConfigValues {
		values[key] = value
	}
	if mutate != nil {
		mutate(values)
	}
	order := []string{
		"npcIndex",
		"epic piece provide",
		"epic piece received",
		"gabriel appearance probability",
		"list change chance",
		"list change cost",
		"kind of epic piece",
		"same type",
		"normal type",
	}
	var out strings.Builder
	for _, key := range order {
		value, ok := values[key]
		if !ok {
			continue
		}
		fmt.Fprintf(&out, "[%s]\n%s\n", key, value)
	}
	return out.String()
}

func loadShopTestConfig(t *testing.T) ShopConfig {
	t.Helper()
	config, err := LoadShopConfig(shopConfigSource{ShopConfigPath: shopConfigDocument(nil)})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestLoadShopConfigAcceptsNativeFiveSlotContract(t *testing.T) {
	config, err := LoadShopConfig(shopConfigSource{ShopConfigPath: shopConfigDocument(nil)})
	if err != nil {
		t.Fatal(err)
	}
	want := ShopConfig{
		NPC:               1130,
		Provide:           5,
		Received:          10,
		AppearancePercent: 1,
		RefreshLimit:      5,
		RefreshGold:       100000,
	}
	if config != want {
		t.Fatalf("config=%+v, want %+v", config, want)
	}
	if _, err := LoadShopConfig(nil); !errors.Is(err, ErrShop) {
		t.Fatalf("nil source error=%v, want %v", err, ErrShop)
	}
}

func TestLoadShopConfigRejectsMissingOrIncompatibleValues(t *testing.T) {
	for _, tag := range []string{
		"npcIndex",
		"epic piece provide",
		"epic piece received",
		"gabriel appearance probability",
		"list change chance",
		"list change cost",
		"kind of epic piece",
		"same type",
		"normal type",
	} {
		t.Run("missing/"+tag, func(t *testing.T) {
			doc := shopConfigDocument(func(values map[string]string) { delete(values, tag) })
			if _, err := LoadShopConfig(shopConfigSource{ShopConfigPath: doc}); !errors.Is(err, ErrShop) {
				t.Fatalf("missing %q error=%v, want %v", tag, err, ErrShop)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		tag   string
		value string
	}{
		{name: "negative_npc", tag: "npcIndex", value: "-1"},
		{name: "negative_provide", tag: "epic piece provide", value: "-1"},
		{name: "negative_received", tag: "epic piece received", value: "-10"},
		{name: "negative_appearance", tag: "gabriel appearance probability", value: "-1"},
		{name: "appearance_over_100", tag: "gabriel appearance probability", value: "101"},
		{name: "negative_refresh_chance", tag: "list change chance", value: "-1"},
		{name: "refresh_chance_over_100", tag: "list change chance", value: "101"},
		{name: "negative_refresh_cost", tag: "list change cost", value: "-1"},
		{name: "client_four_slots", tag: "kind of epic piece", value: "4"},
		{name: "client_six_slots", tag: "kind of epic piece", value: "6"},
		{name: "same_type_nonzero", tag: "same type", value: "1"},
		{name: "normal_type_not_five", tag: "normal type", value: "4"},
		{name: "client_receives_nine", tag: "epic piece received", value: "9"},
		{name: "client_receives_eleven", tag: "epic piece received", value: "11"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := shopConfigDocument(func(values map[string]string) { values[tc.tag] = tc.value })
			if _, err := LoadShopConfig(shopConfigSource{ShopConfigPath: doc}); !errors.Is(err, ErrShop) {
				t.Fatalf("%s=%s error=%v, want %v", tc.tag, tc.value, err, ErrShop)
			}
		})
	}
}

func newShopTestRepositories(t *testing.T) repo.Group {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	metadata := map[string]string{
		"unrelated": "keep-me",
		"nickname":  "epic-collector",
	}
	for _, id := range shopTestMaterials {
		metadata[repo.AccountEpicPieceMetadataKey(id)] = "10"
	}
	if err := repositories.Account.Save(ctx, repo.AccountRecord{
		AccountID: shopTestAccountID,
		Metadata:  metadata,
		UpdatedAt: shopTestNow,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, repo.CharacterRecord{
		CharacterID: shopTestCharacterID,
		AccountID:   shopTestAccountID,
		Stats: map[string]int64{
			"gold":  250000,
			"power": 321,
		},
		UpdatedAt: shopTestNow,
	}); err != nil {
		t.Fatal(err)
	}
	return repositories
}

func loadShopTestAccount(t *testing.T, repositories repo.Group) repo.AccountRecord {
	t.Helper()
	account, found, err := repositories.Account.Load(context.Background(), shopTestAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("shop account missing")
	}
	return account
}

func loadShopTestCharacter(t *testing.T, repositories repo.Group) repo.CharacterRecord {
	t.Helper()
	character, found, err := repositories.Character.Load(context.Background(), shopTestCharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("shop character missing")
	}
	return character
}

func setShopTestPieceBalance(t *testing.T, repositories repo.Group, pieceID uint32, balance uint32) {
	t.Helper()
	ctx := context.Background()
	account := loadShopTestAccount(t, repositories)
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	account.Metadata[repo.AccountEpicPieceMetadataKey(pieceID)] = repo.FormatAccountEpicPieceBalance(balance)
	if err := repositories.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
}

func shopTestExchangeCommand(config ShopConfig, encounter string) ShopCommand {
	return ShopCommand{
		AccountID:   shopTestAccountID,
		CharacterID: shopTestCharacterID,
		Encounter:   encounter,
		Config:      config,
		Target:      shopTestTargetID,
		Materials:   shopTestMaterials,
		Now:         shopTestNow,
	}
}

func assertShopTestSnapshotUnchanged(t *testing.T, repositories repo.Group, account repo.AccountRecord, character repo.CharacterRecord) {
	t.Helper()
	if got := loadShopTestAccount(t, repositories); !reflect.DeepEqual(got, account) {
		t.Fatalf("account changed after rejection: got=%+v want=%+v", got, account)
	}
	if got := loadShopTestCharacter(t, repositories); !reflect.DeepEqual(got, character) {
		t.Fatalf("character changed after rejection: got=%+v want=%+v", got, character)
	}
}

func TestApplyShopRejectsInvalidFiveMaterialRequest(t *testing.T) {
	config := loadShopTestConfig(t)
	cases := []struct {
		name   string
		target uint32
		change func(*[5]uint32)
	}{
		{name: "zero_target"},
		{name: "zero_material", target: shopTestTargetID, change: func(materials *[5]uint32) { materials[2] = 0 }},
		{name: "material_equals_target", target: shopTestTargetID, change: func(materials *[5]uint32) { materials[3] = shopTestTargetID }},
		{name: "duplicate_material", target: shopTestTargetID, change: func(materials *[5]uint32) { materials[4] = materials[1] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repositories := newShopTestRepositories(t)
			account := loadShopTestAccount(t, repositories)
			character := loadShopTestCharacter(t, repositories)
			command := shopTestExchangeCommand(config, "invalid-"+tc.name)
			command.Target = tc.target
			if tc.change != nil {
				tc.change(&command.Materials)
			}
			if _, err := ApplyShop(context.Background(), repositories, command); !errors.Is(err, ErrShop) {
				t.Fatalf("error=%v, want %v", err, ErrShop)
			}
			assertShopTestSnapshotUnchanged(t, repositories, account, character)
		})
	}
}

func TestApplyShopExchangesFiveKindsAndBytesZeroBalances(t *testing.T) {
	repositories := newShopTestRepositories(t)
	config := loadShopTestConfig(t)
	beforeCharacter := loadShopTestCharacter(t, repositories)
	command := shopTestExchangeCommand(config, "exchange-once")

	result, err := ApplyShop(context.Background(), repositories, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gold != 250000 {
		t.Fatalf("gold=%d, want exchange to leave 250000", result.Gold)
	}
	if len(result.Balances) != 6 {
		t.Fatalf("balances=%v, want five debits and one target", result.Balances)
	}
	for _, id := range shopTestMaterials {
		if got := result.Balances[id]; got != 0 {
			t.Fatalf("material %d balance=%d, want 0", id, got)
		}
	}
	if got := result.Balances[shopTestTargetID]; got != 5 {
		t.Fatalf("target balance=%d, want 5", got)
	}

	account := loadShopTestAccount(t, repositories)
	for _, id := range shopTestMaterials {
		value, present := account.Metadata[repo.AccountEpicPieceMetadataKey(id)]
		if !present || value != "0" {
			t.Fatalf("material %d metadata=%q present=%t, want retained zero", id, value, present)
		}
	}
	if got := account.Metadata[repo.AccountEpicPieceMetadataKey(shopTestTargetID)]; got != "5" {
		t.Fatalf("target metadata=%q, want 5", got)
	}
	if got := account.Metadata["unrelated"]; got != "keep-me" {
		t.Fatalf("unrelated account metadata=%q, want keep-me", got)
	}
	if got := account.Metadata["nickname"]; got != "epic-collector" {
		t.Fatalf("account nickname=%q, want epic-collector", got)
	}
	if got := account.Metadata["epic_shop_exchange:"+shopTestCharacterID]; got != command.Encounter {
		t.Fatalf("encounter receipt=%q, want %q", got, command.Encounter)
	}
	if !account.UpdatedAt.Equal(shopTestNow) {
		t.Fatalf("account updated_at=%s, want %s", account.UpdatedAt, shopTestNow)
	}
	if got := loadShopTestCharacter(t, repositories); !reflect.DeepEqual(got, beforeCharacter) {
		t.Fatalf("exchange changed character: got=%+v want=%+v", got, beforeCharacter)
	}
}

func TestApplyShopTargetCapRejectsWithoutDebit(t *testing.T) {
	repositories := newShopTestRepositories(t)
	setShopTestPieceBalance(t, repositories, shopTestTargetID, repo.AccountEpicPieceBalanceMax-4)
	account := loadShopTestAccount(t, repositories)
	character := loadShopTestCharacter(t, repositories)
	command := shopTestExchangeCommand(loadShopTestConfig(t), "target-cap")

	if _, err := ApplyShop(context.Background(), repositories, command); !errors.Is(err, ErrMaterial) {
		t.Fatalf("error=%v, want %v", err, ErrMaterial)
	}
	assertShopTestSnapshotUnchanged(t, repositories, account, character)
}

func TestApplyShopInsufficientMaterialRejectsWithoutPartialDebit(t *testing.T) {
	for index, materialID := range shopTestMaterials {
		t.Run(fmt.Sprintf("material_%d", index), func(t *testing.T) {
			repositories := newShopTestRepositories(t)
			setShopTestPieceBalance(t, repositories, materialID, 9)
			account := loadShopTestAccount(t, repositories)
			character := loadShopTestCharacter(t, repositories)
			command := shopTestExchangeCommand(loadShopTestConfig(t), fmt.Sprintf("insufficient-%d", index))
			if _, err := ApplyShop(context.Background(), repositories, command); !errors.Is(err, ErrMaterial) {
				t.Fatalf("error=%v, want %v", err, ErrMaterial)
			}
			assertShopTestSnapshotUnchanged(t, repositories, account, character)
		})
	}
}

func TestApplyShopRejectsRepeatedEncounter(t *testing.T) {
	repositories := newShopTestRepositories(t)
	command := shopTestExchangeCommand(loadShopTestConfig(t), "exchange-once")
	if _, err := ApplyShop(context.Background(), repositories, command); err != nil {
		t.Fatal(err)
	}
	account := loadShopTestAccount(t, repositories)
	character := loadShopTestCharacter(t, repositories)

	result, err := ApplyShop(context.Background(), repositories, command)
	if !errors.Is(err, ErrShopUsed) {
		t.Fatalf("error=%v, want %v", err, ErrShopUsed)
	}
	if result.Gold != 0 || result.Balances != nil {
		t.Fatalf("repeated encounter returned result=%+v", result)
	}
	assertShopTestSnapshotUnchanged(t, repositories, account, character)
}

func TestApplyShopRefreshChargesGoldWithoutDebitingPieces(t *testing.T) {
	repositories := newShopTestRepositories(t)
	beforeAccount := loadShopTestAccount(t, repositories)
	command := ShopCommand{
		AccountID:   shopTestAccountID,
		CharacterID: shopTestCharacterID,
		Encounter:   "refresh-once",
		Config:      loadShopTestConfig(t),
		Refresh:     true,
		Now:         shopTestNow,
	}

	result, err := ApplyShop(context.Background(), repositories, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gold != 150000 || result.Balances != nil {
		t.Fatalf("refresh result=%+v, want gold=150000 and no balances", result)
	}
	if got := loadShopTestAccount(t, repositories); !reflect.DeepEqual(got, beforeAccount) {
		t.Fatalf("refresh changed account fragments/metadata: got=%+v want=%+v", got, beforeAccount)
	}
	character := loadShopTestCharacter(t, repositories)
	if character.Stats["gold"] != 150000 || character.Stats["power"] != 321 {
		t.Fatalf("refresh stats=%v, want gold=150000 and power=321", character.Stats)
	}
	if !character.UpdatedAt.Equal(shopTestNow) {
		t.Fatalf("character updated_at=%s, want %s", character.UpdatedAt, shopTestNow)
	}
}

func TestApplyShopRejectsWrongAccountOwnership(t *testing.T) {
	repositories := newShopTestRepositories(t)
	const wrongAccountID = "foreign-shop-account"
	ctx := context.Background()
	if err := repositories.Account.Save(ctx, repo.AccountRecord{
		AccountID: wrongAccountID,
		Metadata:  map[string]string{"foreign": "keep"},
		UpdatedAt: shopTestNow,
	}); err != nil {
		t.Fatal(err)
	}
	primaryAccount := loadShopTestAccount(t, repositories)
	foreignAccount, found, err := repositories.Account.Load(ctx, wrongAccountID)
	if err != nil || !found {
		t.Fatalf("foreign account found=%t err=%v", found, err)
	}
	character := loadShopTestCharacter(t, repositories)
	command := shopTestExchangeCommand(loadShopTestConfig(t), "foreign-owner")
	command.AccountID = wrongAccountID

	if _, err := ApplyShop(ctx, repositories, command); !errors.Is(err, repo.ErrEpicBookOwnership) {
		t.Fatalf("error=%v, want %v", err, repo.ErrEpicBookOwnership)
	}
	assertShopTestSnapshotUnchanged(t, repositories, primaryAccount, character)
	gotForeign, found, err := repositories.Account.Load(ctx, wrongAccountID)
	if err != nil || !found || !reflect.DeepEqual(gotForeign, foreignAccount) {
		t.Fatalf("foreign account changed: found=%t err=%v got=%+v want=%+v", found, err, gotForeign, foreignAccount)
	}
}

// shopLedgerFault mirrors the existing transaction-boundary wrappers used by
// other domain tests: project the domain mutation first, then inject either a
// final scoped-repository save failure or a failure before the transaction can
// commit. The memory ledger must leave every durable preimage unchanged.
type shopLedgerFault struct {
	repo.AccountCharacterAssetUnitOfWork
	accountSaveErr   error
	characterSaveErr error
	boundaryErr      error
}

func (u shopLedgerFault) WithinAccountCharacterLedgerAssets(
	ctx context.Context,
	accountID string,
	characterID string,
	apply func(repo.AccountRepository, repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error,
) error {
	ledger, ok := u.AccountCharacterAssetUnitOfWork.(repo.AccountCharacterLedgerAssetUnitOfWork)
	if !ok {
		return repo.ErrAccountCharacterAssetTransactionUnavailable
	}
	return ledger.WithinAccountCharacterLedgerAssets(ctx, accountID, characterID, func(
		account repo.AccountRepository,
		accountInventory repo.AccountInventoryRepository,
		character repo.CharacterRepository,
		inventory repo.InventoryRepository,
		equipment repo.EquipmentRepository,
	) error {
		if u.accountSaveErr != nil {
			account = shopFailingAccountRepository{AccountRepository: account, err: u.accountSaveErr}
		}
		if u.characterSaveErr != nil {
			character = shopFailingCharacterRepository{CharacterRepository: character, err: u.characterSaveErr}
		}
		if err := apply(account, accountInventory, character, inventory, equipment); err != nil {
			return err
		}
		return u.boundaryErr
	})
}

type shopFailingAccountRepository struct {
	repo.AccountRepository
	err error
}

func (r shopFailingAccountRepository) Save(ctx context.Context, record repo.AccountRecord) error {
	if err := r.AccountRepository.Save(ctx, record); err != nil {
		return err
	}
	return r.err
}

type shopFailingCharacterRepository struct {
	repo.CharacterRepository
	err error
}

func (r shopFailingCharacterRepository) Save(ctx context.Context, record repo.CharacterRecord) error {
	if err := r.CharacterRepository.Save(ctx, record); err != nil {
		return err
	}
	return r.err
}

func TestApplyShopLateSaveAndCommitFailuresRollBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refresh bool
		wrap    func(repo.Group, error) *shopLedgerFault
	}{
		{
			name: "exchange_last_save",
			wrap: func(repositories repo.Group, fault error) *shopLedgerFault {
				return &shopLedgerFault{AccountCharacterAssetUnitOfWork: repositories.AccountAssets, accountSaveErr: fault}
			},
		},
		{
			name: "exchange_commit",
			wrap: func(repositories repo.Group, fault error) *shopLedgerFault {
				return &shopLedgerFault{AccountCharacterAssetUnitOfWork: repositories.AccountAssets, boundaryErr: fault}
			},
		},
		{
			name:    "refresh_last_save",
			refresh: true,
			wrap: func(repositories repo.Group, fault error) *shopLedgerFault {
				return &shopLedgerFault{AccountCharacterAssetUnitOfWork: repositories.AccountAssets, characterSaveErr: fault}
			},
		},
		{
			name:    "refresh_commit",
			refresh: true,
			wrap: func(repositories repo.Group, fault error) *shopLedgerFault {
				return &shopLedgerFault{AccountCharacterAssetUnitOfWork: repositories.AccountAssets, boundaryErr: fault}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repositories := newShopTestRepositories(t)
			account := loadShopTestAccount(t, repositories)
			character := loadShopTestCharacter(t, repositories)
			fault := errors.New("injected " + tc.name + " failure")
			repositories.AccountAssets = tc.wrap(repositories, fault)
			command := shopTestExchangeCommand(loadShopTestConfig(t), "rollback-"+tc.name)
			command.Refresh = tc.refresh
			if tc.refresh {
				command.Target = 0
				command.Materials = [5]uint32{}
			}
			if _, err := ApplyShop(context.Background(), repositories, command); !errors.Is(err, fault) {
				t.Fatalf("error=%v, want %v", err, fault)
			}
			assertShopTestSnapshotUnchanged(t, repositories, account, character)
		})
	}
}
