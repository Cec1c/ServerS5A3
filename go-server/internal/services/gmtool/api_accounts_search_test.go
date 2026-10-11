package gmtool

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// P7 / J14（剩余部分）：GET /api/accounts 的 q/limit 与跨账号检索
//
// 计划书 §4.7 测试清单第 3 条（默认档逐字节相同）落在这里，外加本批自己的三条：
//
//	TestP7AccountsDefaultQueryByteIdentical   默认档（与 on 档无参数）逐字节 == 改动前的实现
//	TestP7AccountsSearchUsesBoundedLister    on 档：q/limit 原样进 dnfrepo.ListAccounts（钳制归仓储）
//	TestP7AccountsSearchFailsLoudly         非 lister / 仓储故障 -> 明确失败 + 结构化 reason + 零写入
//	TestP7AccountsSearchDoesNotWrite        成功路径也是只读（账号/角色记录逐字段不变）
//
// 本包的测试机里没有 MySQL，而默认档读的就是启动器账号表（deps.go:290-319 webAccounts
// 的那条 SELECT）。这里用一个 40 行的 database/sql 驱动替身代替 sqlmock：本仓 go.mod
// 没有引入 sqlmock，也不该为一次测试引入依赖；替身完全离线、确定性，
// 而且走的是**真的** database/sql 扫描路径（NullTime/NullString 都过一遍）。
// ---------------------------------------------------------------------------

type p7AccountRow struct {
	PanelID     int64
	Account     string
	AccountID   string
	CreatedAt   time.Time
	LastLoginAt time.Time
	LastLoginIP string
}

type p7AccountConnector struct{ rows []p7AccountRow }

func (c p7AccountConnector) Connect(context.Context) (driver.Conn, error) {
	return p7AccountConn{rows: c.rows}, nil
}

func (c p7AccountConnector) Driver() driver.Driver { return p7AccountDriver{} }

type p7AccountDriver struct{}

func (p7AccountDriver) Open(string) (driver.Conn, error) { return p7AccountConn{}, nil }

type p7AccountConn struct{ rows []p7AccountRow }

func (c p7AccountConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("p7 test driver: prepare is not supported")
}

func (c p7AccountConn) Close() error { return nil }

func (c p7AccountConn) Begin() (driver.Tx, error) {
	return nil, errors.New("p7 test driver: transactions are not supported")
}

func (c p7AccountConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "ORDER BY id") {
		return nil, fmt.Errorf("p7 test driver: unexpected query %q", query)
	}
	return &p7AccountRows{rows: c.rows}, nil
}

type p7AccountRows struct {
	rows  []p7AccountRow
	index int
}

func (r *p7AccountRows) Columns() []string {
	return []string{"id", "account", "account_id", "created_at", "last_login_at", "last_login_ip"}
}

func (r *p7AccountRows) Close() error { return nil }

func (r *p7AccountRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.index]
	r.index++
	dest[0] = row.PanelID
	dest[1] = row.Account
	dest[2] = row.AccountID
	dest[3] = row.CreatedAt
	if row.LastLoginAt.IsZero() {
		dest[4] = nil
	} else {
		dest[4] = row.LastLoginAt
	}
	if strings.TrimSpace(row.LastLoginIP) == "" {
		dest[5] = nil
	} else {
		dest[5] = row.LastLoginIP
	}
	return nil
}

// ---------------------------------------------------------------------------
// 检索仓储替身
// ---------------------------------------------------------------------------

type p7ListCall struct {
	query string
	limit int
}

// p7CountingAccountStore 包住内存账号仓储并记录 ListAccounts 的入参。
// 嵌入的是 AccountRepository **接口**，所以包装器的方法集就是那个接口 —— 只有显式实现的
// ListAccounts 才会让类型断言成功。
type p7CountingAccountStore struct {
	dnfrepo.AccountRepository

	mu    sync.Mutex
	calls []p7ListCall
	err   error
}

func (s *p7CountingAccountStore) ListAccounts(ctx context.Context, query string, limit int) ([]dnfrepo.AccountRecord, error) {
	s.mu.Lock()
	s.calls = append(s.calls, p7ListCall{query: query, limit: limit})
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if lister, ok := s.AccountRepository.(dnfrepo.AccountLister); ok {
		return lister.ListAccounts(ctx, query, limit)
	}
	return nil, dnfrepo.ErrAccountListUnavailable
}

func (s *p7CountingAccountStore) recorded() []p7ListCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]p7ListCall(nil), s.calls...)
}

// p7NonListerAccountStore 只暴露 AccountRepository 的方法集：ListAccounts 的类型断言必须失败。
type p7NonListerAccountStore struct{ dnfrepo.AccountRepository }

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

var p7AccountBase = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

// p7LauncherRows 是启动器账号表（dnf90_web_accounts）的三行 + 一行"只在启动器里存在"。
func p7LauncherRows() []p7AccountRow {
	return []p7AccountRow{
		{PanelID: 10001, Account: "alice", AccountID: "dnf:10001",
			CreatedAt: p7AccountBase, LastLoginAt: p7AccountBase.Add(time.Hour), LastLoginIP: "127.0.0.1"},
		{PanelID: 10006, Account: "bob", AccountID: "dnf:10006",
			CreatedAt: p7AccountBase.Add(time.Minute)},
		{PanelID: 10011, Account: "carol", AccountID: "dnf:10011",
			CreatedAt: p7AccountBase.Add(2 * time.Minute)},
		// 这份 DNF 账号表里没有 dnf:10099（检索档查不到它，全量档能）——
		// 这正是交付报告 §5 记录的那条检索词表限制。
		{PanelID: 10099, Account: "orphan", AccountID: "dnf:10099",
			CreatedAt: p7AccountBase.Add(3 * time.Minute)},
	}
}

// p7LauncherWebAccounts 是上面那张表经 webAccounts 之后的形态（ORDER BY id）。
func p7LauncherWebAccounts() []webAccount {
	rows := p7LauncherRows()
	accounts := make([]webAccount, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, webAccount{
			PanelID:     row.PanelID,
			Account:     row.Account,
			AccountID:   row.AccountID,
			CreatedAt:   row.CreatedAt,
			LastLoginAt: row.LastLoginAt,
			LastLoginIP: row.LastLoginIP,
		})
	}
	return accounts
}

// p7DNFAccounts 是 DNF 账号表（UpdatedAt 决定检索排序：10006 最新，然后 10001/10011 同刻按 id 升序）。
func p7DNFAccounts() []dnfrepo.AccountRecord {
	return []dnfrepo.AccountRecord{
		{AccountID: "dnf:10001", RepresentAccountName: "Alice", UpdatedAt: p7AccountBase,
			Metadata: map[string]string{"account_cera": "1234"}},
		{AccountID: "dnf:10006", RepresentAccountName: "Bob", UpdatedAt: p7AccountBase.Add(time.Hour)},
		{AccountID: "dnf:10011", RepresentAccountName: "Carol", UpdatedAt: p7AccountBase},
	}
}

func p7Characters() []dnfrepo.CharacterRecord {
	return []dnfrepo.CharacterRecord{
		{CharacterID: "11", AccountID: "dnf:10001", Name: "AliceMain", Job: "11", Level: 90, Stats: map[string]int64{}},
		{CharacterID: "12", AccountID: "dnf:10006", Name: "BobMain", Job: "12", Level: 60, Stats: map[string]int64{}},
	}
}

// p7AccountsHarness 造一个带"启动器账号表"（替身驱动）+ 内存 DNF 仓储的写测试台。
func p7AccountsHarness(t *testing.T) (*gmtoolWriteHarness, *p7CountingAccountStore) {
	t.Helper()
	harness := newGmtoolWriteHarness(t, nil)
	harness.server.db = sql.OpenDB(p7AccountConnector{rows: p7LauncherRows()})
	ctx := context.Background()
	for _, record := range p7DNFAccounts() {
		if err := harness.group.Account.Save(ctx, record); err != nil {
			t.Fatalf("save dnf account %s: %v", record.AccountID, err)
		}
	}
	for _, record := range p7Characters() {
		if err := harness.group.Character.Save(ctx, record); err != nil {
			t.Fatalf("save character %s: %v", record.CharacterID, err)
		}
	}
	store := &p7CountingAccountStore{AccountRepository: harness.group.Account}
	p7UseAccountRepository(harness, store)
	return harness, store
}

// p7UseAccountRepository 把仓储组里的账号仓储换成指定实现（并对 deps 重新注入整个组）。
func p7UseAccountRepository(harness *gmtoolWriteHarness, repo dnfrepo.AccountRepository) {
	group := harness.group
	group.Account = repo
	harness.server.deps.Repos = func() (dnfrepo.Group, bool) { return group, true }
}

func (h *gmtoolWriteHarness) p7Accounts(t *testing.T, target string) *bytes.Buffer {
	t.Helper()
	recorder := h.getRaw(t, target, nil, h.server.handleAccountList)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200（业务失败也是 200 + success:false）", target, recorder.Code)
	}
	return recorder.Body
}

func p7DecodeAccounts(t *testing.T, body *bytes.Buffer) []any {
	t.Helper()
	payload := map[string]any{}
	if err := json.Unmarshal(body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %q: %v", body.String(), err)
	}
	rows, ok := payload["accounts"].([]any)
	if !ok {
		t.Fatalf("响应里没有 accounts 数组：%s", body.String())
	}
	return rows
}

// p7LegacyAccountListBody 是 api_accounts.go 改动前 handleAccountList 的原文（逐字复制：
// 同一条 SELECT 结果 -> 同一个循环 -> 同一个 map 字面量 -> 同一个 json.Encoder），
// 作为"默认档逐字节不变"的参照实现。
func p7LegacyAccountListBody(t *testing.T, s *Server, group dnfrepo.Group, accounts []webAccount) string {
	t.Helper()
	ctx := context.Background()
	payload := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		characters, charErr := s.charactersForAccount(ctx, group, account.AccountID)
		if charErr != nil {
			characters = nil
		}
		names := make([]string, 0, len(characters))
		for _, character := range characters {
			names = append(names, character.Name)
		}
		cera := s.accountCera(ctx, group, account.AccountID)
		payload = append(payload, map[string]any{
			"accountId":      account.PanelID,
			"name":           account.Account,
			"cera":           cera,
			"tokenCera":      0,
			"luckyStar":      0,
			"characterCount": len(characters),
			"characterNames": names,
		})
	}
	var buffer bytes.Buffer
	if err := json.NewEncoder(&buffer).Encode(map[string]any{"accounts": payload}); err != nil {
		t.Fatalf("encode legacy payload: %v", err)
	}
	return buffer.String()
}

// ---------------------------------------------------------------------------
// 开关
// ---------------------------------------------------------------------------

func TestP7AccountSearchSwitchOnlyAcceptsExplicitOn(t *testing.T) {
	for _, value := range []string{"on", "ON", " on "} {
		if !GMAccountSearchFromEnv(value) {
			t.Fatalf("GMAccountSearchFromEnv(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", " ", "true", "1", "yes", "off"} {
		if GMAccountSearchFromEnv(value) {
			t.Fatalf("GMAccountSearchFromEnv(%q) = true, want false（本仓开关只认字面 on）", value)
		}
	}
	t.Setenv(gmAccountSearchEnv, "")
	if GMAccountSearchFromEnvironment() {
		t.Fatal("空值必须是 off")
	}
	t.Setenv(gmAccountSearchEnv, "on")
	if !GMAccountSearchFromEnvironment() {
		t.Fatal("显式 on 必须生效")
	}
}

// ---------------------------------------------------------------------------
// 默认档逐字节不变（计划书 §4.7 测试清单第 3 条）
// ---------------------------------------------------------------------------

func TestP7AccountsDefaultQueryByteIdentical(t *testing.T) {
	harness, store := p7AccountsHarness(t)
	want := p7LegacyAccountListBody(t, harness.server, harness.group, p7LauncherWebAccounts())
	if len(p7LauncherWebAccounts()) != 4 {
		t.Fatal("夹具变了，逐字节断言的覆盖面也要跟着改")
	}

	cases := []struct {
		name   string
		env    string
		target string
	}{
		{"关闭档 无参数", "", "/api/accounts"},
		{"关闭档 带 q/limit", "", "/api/accounts?q=alice&limit=2"},
		{"关闭档 带非法 limit", "", "/api/accounts?limit=abc&q=%20"},
		{"开启档 无参数", "on", "/api/accounts"},
		{"开启档 只带无关参数", "on", "/api/accounts?foo=1"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(gmAccountSearchEnv, testCase.env)
			body := harness.p7Accounts(t, testCase.target)
			if got := body.String(); got != want {
				t.Fatalf("%s 的响应变了：\ngot =%s\nwant=%s", testCase.target, got, want)
			}
		})
	}
	if calls := store.recorded(); len(calls) != 0 {
		t.Fatalf("默认档与无参数档都不该碰跨账号检索，实际调用：%+v", calls)
	}
}

// ---------------------------------------------------------------------------
// on 档：q/limit 原样交给 dnfrepo.ListAccounts（钳制只有仓储一处实现）
// ---------------------------------------------------------------------------

func TestP7AccountsSearchUsesBoundedLister(t *testing.T) {
	t.Setenv(gmAccountSearchEnv, "on")
	harness, store := p7AccountsHarness(t)
	defaultBody := p7LegacyAccountListBody(t, harness.server, harness.group, p7LauncherWebAccounts())
	defaultRows := p7DecodeAccounts(t, bytes.NewBufferString(defaultBody))

	// limit=1：仓储按 updated_at DESC 取第一条 -> dnf:10006（Bob）。
	rows := p7DecodeAccounts(t, harness.p7Accounts(t, "/api/accounts?q=1000&limit=1"))
	if len(rows) != 1 {
		t.Fatalf("limit=1 应当只回一行，实际 %d 行：%+v", len(rows), rows)
	}
	if got := rows[0].(map[string]any)["accountId"]; got != float64(10006) {
		t.Fatalf("limit=1 命中的是 %v, want 10006（dnf:10006 的 updated_at 最新）", got)
	}
	// 检索档的一行必须与全量档里的同一行逐字段相同（名字/点券/角色名都来自同一条链路）。
	var wantRow any
	for _, row := range defaultRows {
		if row.(map[string]any)["accountId"] == float64(10006) {
			wantRow = row
		}
	}
	if wantRow == nil {
		t.Fatal("全量档里找不到 10006 这一行")
	}
	if !p7EqualJSON(rows[0], wantRow) {
		t.Fatalf("检索档的行与全量档不同：\nsearch=%+v\ndefault=%+v", rows[0], wantRow)
	}

	calls := store.recorded()
	if len(calls) != 1 || calls[0].query != "1000" || calls[0].limit != 1 {
		t.Fatalf("ListAccounts 入参 = %+v, want [{1000 1}]", calls)
	}

	// q 由仓储侧 TrimSpace/大小写不敏感；代表名匹配同样生效。
	rows = p7DecodeAccounts(t, harness.p7Accounts(t, "/api/accounts?q=%20BOB%20&limit=10"))
	if len(rows) != 1 || rows[0].(map[string]any)["accountId"] != float64(10006) {
		t.Fatalf("q=BOB -> %+v, want 只有 10006", rows)
	}
	if calls = store.recorded(); calls[len(calls)-1].query != "BOB" {
		t.Fatalf("query 应当被仓储侧 TrimSpace，实际 %q", calls[len(calls)-1].query)
	}

	// 钳制规则（<=0 -> 50、>200 -> 200）只有 dnfrepo.ListAccounts 一处实现，
	// 这里断言启动器这一层是**原样透传**，不自己再算一遍。
	for _, testCase := range []struct {
		target    string
		wantLimit int
	}{
		{"/api/accounts?limit=0", 50},
		{"/api/accounts?limit=-4", 50},
		{"/api/accounts?limit=1000", 200},
		{"/api/accounts?q=dnf", 50},
	} {
		harness.p7Accounts(t, testCase.target)
		calls = store.recorded()
		if got := calls[len(calls)-1].limit; got != testCase.wantLimit {
			t.Fatalf("%s -> ListAccounts limit = %d, want %d（钳制归仓储）",
				testCase.target, got, testCase.wantLimit)
		}
	}

	// q 命中了 DNF 账号表里没有的启动器行 -> 检索档看不到它（已知限制，写在交付报告 §5）。
	rows = p7DecodeAccounts(t, harness.p7Accounts(t, "/api/accounts?q=orphan"))
	if len(rows) != 0 {
		t.Fatalf("检索档不该返回 DNF 账号表里不存在的账号：%+v", rows)
	}
	if len(defaultRows) != 4 {
		t.Fatalf("全量档应当仍然看得到 4 行：%+v", defaultRows)
	}
}

func p7EqualJSON(left any, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftRaw) == string(rightRaw)
}

// ---------------------------------------------------------------------------
// 拿不到检索能力 / 仓储故障 -> 明确失败（绝不退化成空列表）
// ---------------------------------------------------------------------------

func TestP7AccountsSearchFailsLoudly(t *testing.T) {
	t.Setenv(gmAccountSearchEnv, "on")

	t.Run("非 lister", func(t *testing.T) {
		harness, _ := p7AccountsHarness(t)
		p7UseAccountRepository(harness, p7NonListerAccountStore{AccountRepository: harness.group.Account})
		before := p7AccountSnapshot(t, harness)

		body := harness.p7Accounts(t, "/api/accounts?q=alice")
		response := map[string]any{}
		if err := json.Unmarshal(body.Bytes(), &response); err != nil {
			t.Fatalf("decode %q: %v", body.String(), err)
		}
		message := requireFailure(t, http.StatusOK, response)
		if !strings.Contains(message, dnfrepo.ErrAccountListUnavailable.Error()) {
			t.Fatalf("失败文案没有带上仓储的原文：%q", message)
		}
		if _, has := response["accounts"]; has {
			t.Fatalf("失败响应不许带 accounts（会被前端当成「查不到账号」）：%s", body.String())
		}
		fields := harness.eventFields(t, "gm-account-search-rejected")
		if fields["reason"] != "account_list_unavailable" || fields["result"] != "rejected" {
			t.Fatalf("结构化事件字段不对：%+v", fields)
		}
		p7RequireSameSnapshot(t, harness, before)
	})

	t.Run("仓储故障", func(t *testing.T) {
		harness, _ := p7AccountsHarness(t)
		p7UseAccountRepository(harness, &p7CountingAccountStore{
			AccountRepository: harness.group.Account,
			err:               errors.New("p7 account list boom"),
		})
		before := p7AccountSnapshot(t, harness)

		body := harness.p7Accounts(t, "/api/accounts?q=alice")
		response := map[string]any{}
		if err := json.Unmarshal(body.Bytes(), &response); err != nil {
			t.Fatalf("decode %q: %v", body.String(), err)
		}
		message := requireFailure(t, http.StatusOK, response)
		if !strings.Contains(message, "p7 account list boom") {
			t.Fatalf("失败文案没有带上传入的故障：%q", message)
		}
		if fields := harness.eventFields(t, "gm-account-search-rejected"); fields["reason"] != "account_list_failed" {
			t.Fatalf("结构化事件字段不对：%+v", fields)
		}
		p7RequireSameSnapshot(t, harness, before)
	})
}

// ---------------------------------------------------------------------------
// 成功路径也是只读
// ---------------------------------------------------------------------------

type p7Snapshot struct {
	accounts   []dnfrepo.AccountRecord
	account    dnfrepo.AccountRecord
	character  dnfrepo.CharacterRecord
	mailbox    dnfrepo.MailboxRecord
	mailboxOK  bool
	accountOK  bool
	charOK     bool
	wantStable string
}

func p7AccountSnapshot(t *testing.T, harness *gmtoolWriteHarness) p7Snapshot {
	t.Helper()
	ctx := context.Background()
	snapshot := p7Snapshot{}
	records, err := dnfrepo.ListAccounts(ctx, harness.group.Account, "", 200)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	snapshot.accounts = records
	snapshot.account, snapshot.accountOK, err = harness.group.Account.Load(ctx, "dnf:10001")
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	snapshot.character, snapshot.charOK, err = harness.group.Character.Load(ctx, "1")
	if err != nil {
		t.Fatalf("load character: %v", err)
	}
	snapshot.mailbox, snapshot.mailboxOK, err = harness.group.Mailbox.Load(ctx, "1")
	if err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	snapshot.wantStable = p7Marshal(t, snapshot)
	return snapshot
}

func p7RequireSameSnapshot(t *testing.T, harness *gmtoolWriteHarness, before p7Snapshot) {
	t.Helper()
	after := p7AccountSnapshot(t, harness)
	if p7Marshal(t, after) != before.wantStable {
		t.Fatalf("只读路由改动了库：\nbefore=%s\nafter =%s", before.wantStable, p7Marshal(t, after))
	}
}

func TestP7AccountsSearchDoesNotWrite(t *testing.T) {
	t.Setenv(gmAccountSearchEnv, "on")
	harness, _ := p7AccountsHarness(t)
	before := p7AccountSnapshot(t, harness)
	rows := p7DecodeAccounts(t, harness.p7Accounts(t, "/api/accounts?q=Alice&limit=5"))
	if len(rows) != 1 {
		t.Fatalf("q=Alice -> %+v, want 只有 10001", rows)
	}
	p7RequireSameSnapshot(t, harness, before)
	if fields := harness.eventFields(t, "gm-account-search"); fields["matched"] != 1 || fields["returned"] != 1 {
		t.Fatalf("检索审计事件字段不对：%+v", fields)
	}
}
