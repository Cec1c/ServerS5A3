package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/db"
)

// Compile-time proof that the transaction scoped account wrappers expose the
// single-key metadata writer instead of forcing the full-record fallback.
var (
	_ dnfrepo.AccountMetadataEntryWriter = (*rentalScopedAccountRepository)(nil)
	_ dnfrepo.AccountMetadataEntryWriter = (*characterSettlementAccountRepository)(nil)
)

type metadataWrite struct {
	AccountID string
	Key       string
	Value     string
	At        time.Time
}

type recordingMetadataAccount struct {
	record    dnfrepo.AccountRecord
	fullSaves int
	writes    []metadataWrite
	writeErr  error
}

var (
	_ dnfrepo.AccountRepository          = (*recordingMetadataAccount)(nil)
	_ dnfrepo.AccountMetadataEntryWriter = (*recordingMetadataAccount)(nil)
)

func (r *recordingMetadataAccount) Load(_ context.Context, accountID string) (dnfrepo.AccountRecord, bool, error) {
	if strings.TrimSpace(accountID) != r.record.AccountID {
		return dnfrepo.AccountRecord{}, false, nil
	}
	return dnfrepo.CloneAccount(r.record), true, nil
}

func (r *recordingMetadataAccount) Save(_ context.Context, record dnfrepo.AccountRecord) error {
	r.fullSaves++
	r.record = dnfrepo.CloneAccount(record)
	return nil
}

func (r *recordingMetadataAccount) SaveMetadataEntry(_ context.Context, accountID, key, value string, at time.Time) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	r.writes = append(r.writes, metadataWrite{AccountID: accountID, Key: key, Value: value, At: at})
	if r.record.Metadata == nil {
		r.record.Metadata = map[string]string{}
	}
	r.record.Metadata[key] = value
	return nil
}

type missingWriterAccount struct{ record dnfrepo.AccountRecord }

var _ dnfrepo.AccountRepository = (*missingWriterAccount)(nil)

func (r *missingWriterAccount) Load(context.Context, string) (dnfrepo.AccountRecord, bool, error) {
	return dnfrepo.CloneAccount(r.record), true, nil
}

func (r *missingWriterAccount) Save(context.Context, dnfrepo.AccountRecord) error { return nil }

func metadataFixture(count int) map[string]string {
	metadata := make(map[string]string, count+1)
	for i := 0; i < count; i++ {
		metadata[fmt.Sprintf("unrelated:%03d", i)] = fmt.Sprintf("keep-%03d", i)
	}
	return metadata
}

// newRecordingMetadataAccount stores a clone so the fixture map cannot alias
// the recorded store; otherwise a narrow write would silently update both.
func newRecordingMetadataAccount(accountID string, metadata map[string]string) *recordingMetadataAccount {
	record := dnfrepo.AccountRecord{AccountID: accountID, Metadata: metadata}
	return &recordingMetadataAccount{record: dnfrepo.CloneAccount(record)}
}

func TestMySQLRentalScopedAccountMetadataEntryWritesOnlyTargetKey(t *testing.T) {
	unrelated := metadataFixture(128)
	base := newRecordingMetadataAccount("dnf:1", unrelated)
	scoped := &rentalScopedAccountRepository{accountID: "dnf:1", base: base}
	ctx := context.Background()

	account, found, err := scoped.Load(ctx, "dnf:1")
	if err != nil || !found {
		t.Fatalf("load found=%t err=%v", found, err)
	}
	now := time.Unix(2_000_000_100, 0).UTC()
	if err := dnfrepo.SaveAccountMetadataEntry(ctx, scoped, account, "target_key", "target_value", now); err != nil {
		t.Fatal(err)
	}
	if base.fullSaves != 0 {
		t.Fatalf("full account saves=%d, want 0", base.fullSaves)
	}
	if len(base.writes) != 1 {
		t.Fatalf("metadata writes=%+v, want one", base.writes)
	}
	write := base.writes[0]
	if write.AccountID != "dnf:1" || write.Key != "target_key" || write.Value != "target_value" || !write.At.Equal(now) {
		t.Fatalf("metadata write=%+v", write)
	}
	if got := len(base.record.Metadata); got != len(unrelated)+1 {
		t.Fatalf("metadata rows=%d, want %d", got, len(unrelated)+1)
	}
	for key, value := range unrelated {
		if base.record.Metadata[key] != value {
			t.Fatalf("unrelated key %q changed: %q", key, base.record.Metadata[key])
		}
	}
	if base.record.Metadata["target_key"] != "target_value" {
		t.Fatalf("target key=%q", base.record.Metadata["target_key"])
	}
}

func TestMySQLRentalScopedAccountMetadataEntryRejectsForeignAccount(t *testing.T) {
	base := newRecordingMetadataAccount("dnf:1", metadataFixture(8))
	scoped := &rentalScopedAccountRepository{accountID: "dnf:1", base: base}
	err := dnfrepo.SaveAccountMetadataEntry(
		context.Background(),
		scoped,
		dnfrepo.AccountRecord{AccountID: "dnf:2", Metadata: map[string]string{"target_key": "other"}},
		"target_key",
		"value",
		time.Now(),
	)
	if !errors.Is(err, db.ErrRecordKeyRequired) {
		t.Fatalf("err=%v, want record key rejection", err)
	}
	if base.fullSaves != 0 || len(base.writes) != 0 {
		t.Fatalf("foreign account wrote: saves=%d writes=%+v", base.fullSaves, base.writes)
	}
}

func TestMySQLRentalScopedAccountMetadataEntryRequiresWriter(t *testing.T) {
	scoped := &rentalScopedAccountRepository{
		accountID: "dnf:1",
		base:      &missingWriterAccount{record: dnfrepo.AccountRecord{AccountID: "dnf:1"}},
	}
	err := dnfrepo.SaveAccountMetadataEntry(
		context.Background(),
		scoped,
		dnfrepo.AccountRecord{AccountID: "dnf:1"},
		"target_key",
		"value",
		time.Now(),
	)
	if !errors.Is(err, dnfrepo.ErrAccountMetadataUnavailable) {
		t.Fatalf("err=%v, want metadata unavailable", err)
	}
}

type metadataCharacterRepository struct {
	record dnfrepo.CharacterRecord
	found  bool
	err    error
	loads  int
}

var _ dnfrepo.CharacterRepository = (*metadataCharacterRepository)(nil)

func (r *metadataCharacterRepository) Load(context.Context, string) (dnfrepo.CharacterRecord, bool, error) {
	r.loads++
	return dnfrepo.CloneCharacter(r.record), r.found, r.err
}

func (r *metadataCharacterRepository) Save(context.Context, dnfrepo.CharacterRecord) error {
	return nil
}

func (r *metadataCharacterRepository) ListByAccount(context.Context, string, int) ([]dnfrepo.CharacterRecord, error) {
	return nil, nil
}

func (r *metadataCharacterRepository) FindIDByName(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (r *metadataCharacterRepository) NextNumericID(context.Context) (int, error) { return 0, nil }

func TestMySQLCharacterSettlementAccountMetadataEntryVerifiesOwnerBeforeWriting(t *testing.T) {
	base := newRecordingMetadataAccount("dnf:1", metadataFixture(128))
	characters := &metadataCharacterRepository{
		record: dnfrepo.CharacterRecord{CharacterID: "42", AccountID: "dnf:1"},
		found:  true,
	}
	scoped := &characterSettlementAccountRepository{characterID: "42", characters: characters, accounts: base}
	account, _, err := scoped.Load(context.Background(), "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := dnfrepo.SaveAccountMetadataEntry(context.Background(), scoped, account, "target_key", "target_value", time.Now()); err != nil {
		t.Fatal(err)
	}
	if characters.loads != 2 {
		t.Fatalf("character ownership loads=%d, want 2 (load plus write verification)", characters.loads)
	}
	if base.fullSaves != 0 || len(base.writes) != 1 || base.writes[0].Key != "target_key" {
		t.Fatalf("saves=%d writes=%+v", base.fullSaves, base.writes)
	}
	if len(base.record.Metadata) != 129 {
		t.Fatalf("metadata rows=%d, want 129", len(base.record.Metadata))
	}
}

func TestMySQLCharacterSettlementAccountMetadataEntryRejectsForeignAccount(t *testing.T) {
	base := &recordingMetadataAccount{record: dnfrepo.AccountRecord{AccountID: "dnf:1"}}
	characters := &metadataCharacterRepository{
		record: dnfrepo.CharacterRecord{CharacterID: "42", AccountID: "dnf:1"},
		found:  true,
	}
	scoped := &characterSettlementAccountRepository{characterID: "42", characters: characters, accounts: base}
	err := dnfrepo.SaveAccountMetadataEntry(
		context.Background(),
		scoped,
		dnfrepo.AccountRecord{AccountID: "dnf:2"},
		"target_key",
		"value",
		time.Now(),
	)
	if !errors.Is(err, db.ErrRecordKeyRequired) {
		t.Fatalf("err=%v, want record key rejection", err)
	}
	if len(base.writes) != 0 || base.fullSaves != 0 {
		t.Fatalf("foreign account wrote: saves=%d writes=%+v", base.fullSaves, base.writes)
	}
}

func TestMySQLCharacterSettlementAccountMetadataEntryRequiresOwnedCharacter(t *testing.T) {
	base := &recordingMetadataAccount{record: dnfrepo.AccountRecord{AccountID: "dnf:1"}}
	characters := &metadataCharacterRepository{record: dnfrepo.CharacterRecord{CharacterID: "42", AccountID: "dnf:1"}}
	scoped := &characterSettlementAccountRepository{characterID: "42", characters: characters, accounts: base}
	err := dnfrepo.SaveAccountMetadataEntry(
		context.Background(),
		scoped,
		dnfrepo.AccountRecord{AccountID: "dnf:1"},
		"target_key",
		"value",
		time.Now(),
	)
	if !errors.Is(err, dnfrepo.ErrCharacterSettlementTransactionUnavailable) {
		t.Fatalf("err=%v, want settlement unavailable", err)
	}
	if len(base.writes) != 0 || base.fullSaves != 0 {
		t.Fatalf("unowned character wrote: saves=%d writes=%+v", base.fullSaves, base.writes)
	}
}

func metadataWritesIn(queries []string) []string {
	var result []string
	for _, query := range queries {
		if strings.Contains(query, "`dnf_"+mysqlAccountMetadataTable+"`") {
			result = append(result, query)
		}
	}
	return result
}

func assertNarrowAccountUpdate(t *testing.T, queries []string) {
	t.Helper()
	found := false
	for _, query := range queries {
		if !strings.Contains(query, "`dnf_"+mysqlAccountTable+"`") {
			continue
		}
		found = true
		if !strings.Contains(query, "SET updated_at = ?") || strings.Contains(query, "honor_exp") || strings.Contains(query, "state =") {
			t.Fatalf("account update is not the narrow timestamp write: %s", query)
		}
	}
	if !found {
		t.Fatalf("no account timestamp update in %v", queries)
	}
}

func TestMySQLCharacterSettlementMetadataEntryUsesNarrowWriteInOneTransaction(t *testing.T) {
	state := &raidLedgerSQLState{characterAccount: "a"}
	database := sql.OpenDB(raidLedgerSQLConnector{state: state})
	defer database.Close()
	settlement := &mysqlCharacterSettlementUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"readonly"},
		writeDBs:    []string{"shard_a"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}
	now := time.Unix(2_000_000_200, 0).UTC()
	err := settlement.WithinCharacterSettlement(context.Background(), "19", func(tx dnfrepo.Group) error {
		account, found, err := tx.Account.Load(context.Background(), "a")
		if err != nil {
			return err
		}
		if !found {
			return errors.New("account a not found")
		}
		return dnfrepo.SaveAccountMetadataEntry(context.Background(), tx.Account, account, "target_key", "target_value", now)
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 || state.outsideTxOperations != 0 {
		t.Fatalf("transaction state=%+v", state)
	}
	writes := metadataWritesIn(state.writeSQL)
	if len(writes) != 1 {
		t.Fatalf("metadata writes=%d queries=%v", len(writes), state.writeSQL)
	}
	if !strings.Contains(writes[0], "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("metadata write is not a single-row upsert: %s", writes[0])
	}
	for _, query := range state.writeSQL {
		if strings.Contains(query, "DELETE FROM") {
			t.Fatalf("metadata replacement leaked into the scoped write: %s", query)
		}
	}
	assertNarrowAccountUpdate(t, state.writeSQL)
}

var errInjectedMetadataWrite = errors.New("injected account metadata write failure")

type metadataFaultConnector struct {
	state  *raidLedgerSQLState
	failOn func(string) bool
}

func (c metadataFaultConnector) Connect(context.Context) (driver.Conn, error) {
	return &metadataFaultConn{raidLedgerSQLConn: c.state.connection(), failOn: c.failOn}, nil
}

func (c metadataFaultConnector) Driver() driver.Driver { return raidLedgerSQLDriver{state: c.state} }

type metadataFaultConn struct {
	*raidLedgerSQLConn
	failOn func(string) bool
}

func (c *metadataFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.failOn(query) {
		c.state.writeSQL = append(c.state.writeSQL, query)
		return nil, errInjectedMetadataWrite
	}
	return c.raidLedgerSQLConn.ExecContext(ctx, query, args)
}

func TestMySQLCharacterSettlementMetadataEntryFailureRollsBack(t *testing.T) {
	state := &raidLedgerSQLState{characterAccount: "a"}
	connector := metadataFaultConnector{
		state: state,
		failOn: func(query string) bool {
			return strings.HasPrefix(query, "INSERT INTO") && strings.Contains(query, mysqlAccountMetadataTable)
		},
	}
	database := sql.OpenDB(connector)
	defer database.Close()
	settlement := &mysqlCharacterSettlementUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"readonly"},
		writeDBs:    []string{"shard_a"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}
	err := settlement.WithinCharacterSettlement(context.Background(), "19", func(tx dnfrepo.Group) error {
		account, found, err := tx.Account.Load(context.Background(), "a")
		if err != nil {
			return err
		}
		if !found {
			return errors.New("account a not found")
		}
		return dnfrepo.SaveAccountMetadataEntry(context.Background(), tx.Account, account, "target_key", "target_value", time.Now())
	})
	if !errors.Is(err, errInjectedMetadataWrite) {
		t.Fatalf("err=%v, want injected metadata failure", err)
	}
	if state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("failure transaction state=%+v", state)
	}
	for _, query := range state.writeSQL {
		if strings.Contains(query, "`dnf_"+mysqlAccountMetadataTable+"`") && strings.Contains(query, "DELETE FROM") {
			t.Fatalf("metadata replacement used on failure path: %s", query)
		}
	}
	assertNarrowAccountUpdate(t, state.writeSQL)
}

func TestMySQLMailboxScopedAccountMetadataEntryUsesNarrowWrite(t *testing.T) {
	state := &raidLedgerSQLState{characterAccount: "a"}
	database := sql.OpenDB(raidLedgerSQLConnector{state: state})
	defer database.Close()
	u := &mysqlMailboxAssetUnitOfWork{router: mysqlRouter{
		db:          SQLDBAdapter{DB: database},
		readDBs:     []string{"readonly"},
		writeDBs:    []string{"shard_a"},
		tablePrefix: "dnf",
		now:         time.Now,
	}}
	err := u.WithinAccountMailbox(context.Background(), "a", "19", func(accounts dnfrepo.AccountRepository, _ dnfrepo.MailboxRepository) error {
		account, found, err := accounts.Load(context.Background(), "a")
		if err != nil {
			return err
		}
		if !found {
			return errors.New("account a not found")
		}
		return dnfrepo.SaveAccountMetadataEntry(context.Background(), accounts, account, "attendance", "claimed", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("mailbox transaction state=%+v", state)
	}
	writes := metadataWritesIn(state.writeSQL)
	if len(writes) != 1 || !strings.Contains(writes[0], "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("metadata writes=%v", writes)
	}
	for _, query := range state.writeSQL {
		if strings.Contains(query, "DELETE FROM") {
			t.Fatalf("metadata replacement leaked into mailbox write: %s", query)
		}
	}
	assertNarrowAccountUpdate(t, state.writeSQL)
}
