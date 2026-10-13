package gmtool

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// This driver implements the actual native schema, not the incompatible six
// columns of the legacy web table. All writes and credential reads are rejected.
type nativeGMRow struct{ username, accountID string }
type nativeGMConnector struct{ rows []nativeGMRow }
type nativeGMDriver struct{}
type nativeGMConn struct{ rows []nativeGMRow }
type nativeGMRows struct {
	rows []nativeGMRow
	next int
}

func (c nativeGMConnector) Connect(context.Context) (driver.Conn, error) {
	return nativeGMConn{c.rows}, nil
}
func (nativeGMConnector) Driver() driver.Driver         { return nativeGMDriver{} }
func (nativeGMDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unexpected open") }
func (nativeGMConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (nativeGMConn) Close() error              { return nil }
func (nativeGMConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c nativeGMConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if q != "SELECT username, account_id, created_at, last_login_at FROM `dnf_login_accounts` ORDER BY account_id" {
		return nil, errors.New("query does not match native local account schema")
	}
	return &nativeGMRows{rows: c.rows}, nil
}
func (*nativeGMRows) Columns() []string {
	return []string{"username", "account_id", "created_at", "last_login_at"}
}
func (*nativeGMRows) Close() error { return nil }
func (r *nativeGMRows) Next(values []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.next]
	r.next++
	values[0] = row.username
	values[1] = row.accountID
	values[2] = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	values[3] = nil
	return nil
}

func TestNativeGMAccountsResolveExistingLocalCharacters(t *testing.T) {
	h := newGmtoolWriteHarness(t, nil)
	const aid = "dnf:ce80fb49d2712db0fc1297b6614b2a36"
	h.server.cfg.Table = "dnf_login_accounts"
	h.server.db = sql.OpenDB(nativeGMConnector{[]nativeGMRow{{"tester", aid}}})
	t.Cleanup(func() { _ = h.server.db.Close() })
	ctx := context.Background()
	account := dnfrepo.AccountRecord{AccountID: aid, Metadata: map[string]string{"account_cera": "123"}}
	character := dnfrepo.CharacterRecord{CharacterID: "2", AccountID: aid, Name: "LocalRole", Level: 1, Job: "11", Stats: map[string]int64{}}
	if err := h.group.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := h.group.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	beforeAccount, _, _ := h.group.Account.Load(ctx, aid)
	beforeRole, _, _ := h.group.Character.Load(ctx, "2")
	_, listed := h.get(t, "/api/accounts", nil, h.server.handleAccountList)
	rows, ok := listed["accounts"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("native account list unavailable: %v", listed)
	}
	row := rows[0].(map[string]any)
	pid := int64(row["accountId"].(float64))
	if pid <= 0 || pid > 9007199254740991 || row["name"] != "tester" || row["characterCount"] != float64(1) {
		t.Fatalf("incorrect account projection: %v", row)
	}
	_, listed = h.get(t, "/api/characters?accountId="+strconv.FormatInt(pid, 10), nil, h.server.handleCharacterList)
	roles, ok := listed["characters"].([]any)
	if !ok || len(roles) != 1 {
		t.Fatalf("local role lookup failed: %v", listed)
	}
	role := roles[0].(map[string]any)
	if role["name"] != "LocalRole" || int64(role["accountId"].(float64)) != pid {
		t.Fatalf("wrong ownership: %v", role)
	}
	_, detail := h.get(t, "/api/characters/2", map[string]string{"id": "2"}, h.server.handleCharacterDetail)
	if int64(detail["accountId"].(float64)) != pid {
		t.Fatalf("detail account ID differs: %v", detail)
	}
	_, wrong := h.get(t, "/api/characters?accountId="+strconv.FormatInt(pid+1, 10), nil, h.server.handleCharacterList)
	if wrong["success"] != false {
		t.Fatalf("unknown panel account accepted: %v", wrong)
	}
	stored, _, _ := h.group.Account.Load(ctx, aid)
	storedRole, _, _ := h.group.Character.Load(ctx, "2")
	if !reflect.DeepEqual(beforeAccount, stored) || !reflect.DeepEqual(beforeRole, storedRole) {
		t.Fatal("read APIs changed game records")
	}
	if strings.Contains(h.p7Accounts(t, "/api/accounts").String(), "password_hash") {
		t.Fatal("credentials included in account payload")
	}
}

func TestNativeGMSelectorsStableAcrossReorderAndNewAccounts(t *testing.T) {
	s := &Server{cfg: Config{Table: "dnf_login_accounts"}}
	rows := []nativeGMRow{{"alice", "dnf:alice-hash"}, {"bob", "dnf:bob-hash"}}
	read := func(rows []nativeGMRow) map[string]int64 {
		db := sql.OpenDB(nativeGMConnector{rows})
		defer db.Close()
		s.db = db
		accounts, err := s.webAccounts(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, a := range accounts {
			out[a.AccountID] = a.PanelID
		}
		return out
	}
	one := read(rows)
	two := read([]nativeGMRow{{"new", "dnf:new-hash"}, rows[1], rows[0]})
	for key, id := range one {
		if two[key] != id || int64(s.accountPanelID(key)) != id || id > 9007199254740991 {
			t.Fatal("native selector moved or lost precision")
		}
	}
	if one[rows[0].accountID] == one[rows[1].accountID] {
		t.Fatal("ambiguous fixture selectors")
	}
	if s.accountPanelID("") != 0 {
		t.Fatal("empty game account became a panel owner")
	}
	legacy := &Server{}
	if legacy.accountPanelID("dnf:10001") != 10001 {
		t.Fatal("legacy character projection changed")
	}
}

func TestNativeGMRejectsAmbiguousOrInvalidAccountRows(t *testing.T) {
	if err := validateLocalAccountSelectors([]webAccount{{PanelID: 7, AccountID: "dnf:a"}, {PanelID: 7, AccountID: "dnf:b"}}); err == nil {
		t.Fatal("ambiguous selector accepted")
	}
	for _, rows := range [][]nativeGMRow{
		{{"a", "dnf:same"}, {"b", "dnf:same"}},
		{{"", "dnf:valid"}},
		{{"a", "invalid owner with spaces"}},
	} {
		s := &Server{cfg: Config{Table: "dnf_login_accounts"}, db: sql.OpenDB(nativeGMConnector{rows})}
		_, err := s.webAccounts(context.Background())
		_ = s.db.Close()
		if err == nil {
			t.Fatal("invalid native account row accepted")
		}
	}
}

func TestNativeGMAccountCurrencyKeepsRealOwner(t *testing.T) {
	h := newGmtoolWriteHarness(t, nil)
	rows := []nativeGMRow{{"alice", "dnf:alice-hash"}, {"bob", "dnf:bob-hash"}}
	h.server.cfg.Table = "dnf_login_accounts"
	h.server.db = sql.OpenDB(nativeGMConnector{rows})
	defer h.server.db.Close()
	ctx := context.Background()
	for _, a := range rows {
		if err := h.group.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: a.accountID, Metadata: map[string]string{"account_cera": "5"}}); err != nil {
			t.Fatal(err)
		}
	}
	id := strconv.FormatInt(localAccountPanelID(rows[1].accountID), 10)
	_, response := h.post(t, "/api/accounts/"+id+"/currency", map[string]string{"id": id}, map[string]any{"type": "cera", "value": 17}, h.server.handleAccountCurrency)
	if response["success"] != true {
		t.Fatalf("native currency owner unresolved: %v", response)
	}
	if h.server.accountCera(ctx, h.group, rows[0].accountID) != 5 || h.server.accountCera(ctx, h.group, rows[1].accountID) != 17 {
		t.Fatal("wrong real account modified")
	}
	_, response = h.post(t, "/api/accounts/1/currency", map[string]string{"id": "1"}, map[string]any{"type": "cera", "value": 99}, h.server.handleAccountCurrency)
	if response["success"] != false || h.server.accountCera(ctx, h.group, rows[1].accountID) != 17 {
		t.Fatal("unknown selector wrote account data")
	}
	_, response = h.post(t, "/api/accounts/create-for-clone", nil, map[string]any{}, h.server.handleAccountCreateForClone)
	if response["success"] != false || !strings.Contains(response["error"].(string), "本地登录器") {
		t.Fatal("native profile entered legacy registration SQL")
	}
}
