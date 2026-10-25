package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	repository "longheng.io/server/internal/modules/dnf/repository"
	"strings"
	"testing"
	"time"
)

type accountCharacterPetSQLState struct {
	begins     int
	commits    int
	rollbacks  int
	queries    []string
	writes     []string
	petFailure bool
}

type accountCharacterPetSQLConnector struct {
	state *accountCharacterPetSQLState
}

func (c accountCharacterPetSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return accountCharacterPetSQLConn{state: c.state}, nil
}

func (c accountCharacterPetSQLConnector) Driver() driver.Driver {
	return accountCharacterPetSQLDriver{state: c.state}
}

type accountCharacterPetSQLDriver struct {
	state *accountCharacterPetSQLState
}

func (d accountCharacterPetSQLDriver) Open(string) (driver.Conn, error) {
	return accountCharacterPetSQLConn{state: d.state}, nil
}

type accountCharacterPetSQLConn struct {
	state *accountCharacterPetSQLState
}

func (c accountCharacterPetSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}

func (c accountCharacterPetSQLConn) Close() error {
	return nil
}

func (c accountCharacterPetSQLConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c accountCharacterPetSQLConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.state.begins++
	return accountCharacterPetSQLTx{state: c.state}, nil
}

func (c accountCharacterPetSQLConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.state.begins != 1 || c.state.commits != 0 || c.state.rollbacks != 0 {
		return nil, errors.New("query outside the active transaction")
	}
	c.state.queries = append(c.state.queries, query)
	return accountCharacterPetEmptyRows{}, nil
}

func (c accountCharacterPetSQLConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if c.state.begins != 1 || c.state.commits != 0 || c.state.rollbacks != 0 {
		return nil, errors.New("write outside the active transaction")
	}
	c.state.writes = append(c.state.writes, query)
	if c.state.petFailure && strings.Contains(query, "`dnf_pets`") {
		return nil, errors.New("pet write failed")
	}
	return driver.RowsAffected(1), nil
}

type accountCharacterPetSQLTx struct {
	state *accountCharacterPetSQLState
}

func (t accountCharacterPetSQLTx) Commit() error {
	t.state.commits++
	return nil
}

func (t accountCharacterPetSQLTx) Rollback() error {
	t.state.rollbacks++
	return nil
}

type accountCharacterPetEmptyRows struct{}

func (accountCharacterPetEmptyRows) Columns() []string { return []string{"unused"} }
func (accountCharacterPetEmptyRows) Close() error      { return nil }
func (accountCharacterPetEmptyRows) Next([]driver.Value) error {
	return io.EOF
}

func newAccountCharacterPetUnitOfWork(state *accountCharacterPetSQLState) (*mysqlAccountCharacterAssetUnitOfWork, func()) {
	db := sql.OpenDB(accountCharacterPetSQLConnector{state: state})
	u := &mysqlAccountCharacterAssetUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: db},
		readDBs:     []string{"readonly"},
		writeDBs:    []string{"shard_a", "shard_b"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}
	return u, func() { _ = db.Close() }
}

func saveAllAccountCharacterPetAssets(
	ctx context.Context,
	accounts repository.AccountInventoryRepository,
	characters repository.CharacterRepository,
	inventory repository.InventoryRepository,
	equipment repository.EquipmentRepository,
	pets repository.PetRepository,
	accountID, characterID string,
) error {
	if _, _, err := accounts.Load(ctx, accountID); err != nil {
		return err
	}
	if _, _, err := characters.Load(ctx, characterID); err != nil {
		return err
	}
	if _, _, err := inventory.Load(ctx, characterID); err != nil {
		return err
	}
	if _, _, err := equipment.Load(ctx, characterID); err != nil {
		return err
	}
	if _, _, err := pets.Load(ctx, characterID); err != nil {
		return err
	}

	if err := accounts.Save(ctx, repository.AccountInventoryRecord{
		AccountID: accountID,
		Slots: map[string]repository.ItemStack{
			"account-slot": {ItemID: 1001, Count: 2},
		},
	}); err != nil {
		return err
	}
	if err := repository.SaveCharacterFields(ctx, characters, repository.CharacterRecord{
		CharacterID: characterID,
		AccountID:   accountID,
		Name:        "pet-owner",
	}, repository.CharacterFieldBase); err != nil {
		return err
	}
	if err := repository.SaveInventoryFields(ctx, inventory, repository.InventoryRecord{
		CharacterID: characterID,
		Slots: map[string]repository.ItemStack{
			"character-slot": {ItemID: 1002, Count: 1},
		},
	}, repository.InventoryFieldSlots); err != nil {
		return err
	}
	if err := repository.SaveEquipmentFields(ctx, equipment, repository.EquipmentRecord{
		CharacterID: characterID,
		Entries: map[string]repository.EquipmentEntry{
			"11": {SlotIndex: 11, ItemID: 1003},
		},
	}, repository.EquipmentFieldEntries); err != nil {
		return err
	}
	return repository.SavePetFields(ctx, pets, repository.PetRecord{
		CharacterID: characterID,
		Entries: map[string]repository.PetEntry{
			"pet-1": {PetKey: "pet-1", ItemID: 1004},
		},
		EquippedKey: "pet-1",
	}, repository.PetFieldEntries, repository.PetFieldEquipped)
}

func TestMySQLAccountCharacterPetAssetsCommitOneLockedTransaction(t *testing.T) {
	state := &accountCharacterPetSQLState{}
	u, closeDB := newAccountCharacterPetUnitOfWork(state)
	defer closeDB()

	err := u.WithinAccountCharacterPetAssets(context.Background(), "account-1", "character-1", func(
		accounts repository.AccountInventoryRepository,
		characters repository.CharacterRepository,
		inventory repository.InventoryRepository,
		equipment repository.EquipmentRepository,
		pets repository.PetRepository,
	) error {
		return saveAllAccountCharacterPetAssets(context.Background(), accounts, characters, inventory, equipment, pets, "account-1", "character-1")
	})
	if err != nil {
		t.Fatalf("WithinAccountCharacterPetAssets() error = %v", err)
	}
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transaction state = %+v, want one nested-free commit", state)
	}
	if len(state.queries) != 5 {
		t.Fatalf("locked load queries = %d, want 5: %v", len(state.queries), state.queries)
	}
	for _, query := range state.queries {
		if strings.Contains(query, "readonly") || !strings.HasSuffix(query, " FOR UPDATE") {
			t.Fatalf("query escaped the locked write transaction: %s", query)
		}
	}
	joined := strings.Join(state.writes, "\n")
	for _, table := range []string{
		"`dnf_account_inventories`",
		"`dnf_characters`",
		"`dnf_inventories`",
		"`dnf_equipments`",
		"`dnf_pets`",
	} {
		if !strings.Contains(joined, table) {
			t.Fatalf("committed writes missing %s: %v", table, state.writes)
		}
	}
}

func TestMySQLAccountCharacterPetAssetsPetFailureRollsBackEveryAsset(t *testing.T) {
	state := &accountCharacterPetSQLState{petFailure: true}
	u, closeDB := newAccountCharacterPetUnitOfWork(state)
	defer closeDB()

	err := u.WithinAccountCharacterPetAssets(context.Background(), "account-1", "character-1", func(
		accounts repository.AccountInventoryRepository,
		characters repository.CharacterRepository,
		inventory repository.InventoryRepository,
		equipment repository.EquipmentRepository,
		pets repository.PetRepository,
	) error {
		return saveAllAccountCharacterPetAssets(context.Background(), accounts, characters, inventory, equipment, pets, "account-1", "character-1")
	})
	if err == nil || !strings.Contains(err.Error(), "pet write failed") {
		t.Fatalf("WithinAccountCharacterPetAssets() error = %v, want pet write failure", err)
	}
	if state.begins != 1 || state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("transaction state = %+v, want one rollback", state)
	}
	joined := strings.Join(state.writes, "\n")
	for _, table := range []string{
		"`dnf_account_inventories`",
		"`dnf_characters`",
		"`dnf_inventories`",
		"`dnf_equipments`",
		"`dnf_pets`",
	} {
		if !strings.Contains(joined, table) {
			t.Fatalf("rollback test did not reach %s: %v", table, state.writes)
		}
	}
}

func TestMySQLAccountCharacterPetAssetsRejectsEveryCrossCharacterContainer(t *testing.T) {
	state := &accountCharacterPetSQLState{}
	u, closeDB := newAccountCharacterPetUnitOfWork(state)
	defer closeDB()

	err := u.WithinAccountCharacterPetAssets(context.Background(), "account-1", "character-1", func(
		accounts repository.AccountInventoryRepository,
		characters repository.CharacterRepository,
		inventory repository.InventoryRepository,
		equipment repository.EquipmentRepository,
		pets repository.PetRepository,
	) error {
		ctx := context.Background()
		errorsFound := make([]error, 0, 10)
		if _, _, err := accounts.Load(ctx, "account-2"); err == nil {
			errorsFound = append(errorsFound, errors.New("account inventory cross-account load accepted"))
		}
		if err := accounts.Save(ctx, repository.AccountInventoryRecord{AccountID: "account-2"}); err == nil {
			errorsFound = append(errorsFound, errors.New("account inventory cross-account save accepted"))
		}
		if _, _, err := characters.Load(ctx, "character-2"); err == nil {
			errorsFound = append(errorsFound, errors.New("character cross-character load accepted"))
		}
		if err := repository.SaveCharacterFields(ctx, characters, repository.CharacterRecord{CharacterID: "character-2"}, repository.CharacterFieldBase); err == nil {
			errorsFound = append(errorsFound, errors.New("character cross-character save accepted"))
		}
		if _, _, err := inventory.Load(ctx, "character-2"); err == nil {
			errorsFound = append(errorsFound, errors.New("inventory cross-character load accepted"))
		}
		if err := repository.SaveInventoryFields(ctx, inventory, repository.InventoryRecord{CharacterID: "character-2"}, repository.InventoryFieldSlots); err == nil {
			errorsFound = append(errorsFound, errors.New("inventory cross-character save accepted"))
		}
		if _, _, err := equipment.Load(ctx, "character-2"); err == nil {
			errorsFound = append(errorsFound, errors.New("equipment cross-character load accepted"))
		}
		if err := repository.SaveEquipmentFields(ctx, equipment, repository.EquipmentRecord{CharacterID: "character-2"}, repository.EquipmentFieldEntries); err == nil {
			errorsFound = append(errorsFound, errors.New("equipment cross-character save accepted"))
		}
		if _, _, err := pets.Load(ctx, "character-2"); err == nil {
			errorsFound = append(errorsFound, errors.New("pet cross-character load accepted"))
		}
		if err := repository.SavePetFields(ctx, pets, repository.PetRecord{CharacterID: "character-2"}, repository.PetFieldEquipped); err == nil {
			errorsFound = append(errorsFound, errors.New("pet cross-character save accepted"))
		}
		if len(errorsFound) > 0 {
			t.Fatal(errors.Join(errorsFound...))
		}
		return errors.New("end scope probe without committing")
	})
	if err == nil {
		t.Fatal("cross-character asset access was accepted")
	}
	if state.begins != 1 || state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("transaction state = %+v, want one rollback", state)
	}
	if len(state.queries) != 0 || len(state.writes) != 0 {
		t.Fatalf("cross-character checks reached SQL: queries=%v writes=%v", state.queries, state.writes)
	}
}
