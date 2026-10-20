package memory

import (
	"context"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

// accountListFixture 建三个账号，UpdatedAt 刻意造出"同一时间戳"与"不同时间戳"两种情形，
// 用来分别钉住排序的两个键。
func accountListFixture(t *testing.T) repository.AccountRepository {
	t.Helper()
	group := NewMemoryGroup()
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	records := []repository.AccountRecord{
		{AccountID: "dnf:10001", RepresentAccountName: "Alice", UpdatedAt: base},
		{AccountID: "dnf:10006", RepresentAccountName: "Bob", UpdatedAt: base.Add(time.Hour)},
		{AccountID: "dnf:10011", RepresentAccountName: "Carol", UpdatedAt: base},
	}
	for _, record := range records {
		if err := group.Account.Save(context.Background(), record); err != nil {
			t.Fatalf("save %s: %v", record.AccountID, err)
		}
	}
	return group.Account
}

func accountIDs(records []repository.AccountRecord) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.AccountID)
	}
	return out
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// 排序契约：UpdatedAt 倒序优先，同一时间戳再按 AccountID 升序。
// fixture 里 dnf:10006 最新（单独在前），10001/10011 同一时间戳 ⇒ 必须按 id 升序。
func TestMemoryListAccountsOrdersByUpdatedThenID(t *testing.T) {
	account := accountListFixture(t)
	records, err := repository.ListAccounts(context.Background(), account, "", 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"dnf:10006", "dnf:10001", "dnf:10011"}
	if got := accountIDs(records); !equalStrings(got, want) {
		t.Fatalf("order=%v want %v", got, want)
	}
}

// 检索同时匹配 account_id 与代表角色名，且大小写不敏感。
func TestMemoryListAccountsSearchesIDAndRepresentName(t *testing.T) {
	account := accountListFixture(t)
	cases := []struct {
		query string
		want  []string
	}{
		{query: "10011", want: []string{"dnf:10011"}},
		{query: "1000", want: []string{"dnf:10006", "dnf:10001"}},
		{query: "alice", want: []string{"dnf:10001"}},
		{query: "ALICE", want: []string{"dnf:10001"}},
		{query: "  bob  ", want: []string{"dnf:10006"}},
		{query: "zzz", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			records, err := repository.ListAccounts(context.Background(), account, tc.query, 50)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got := accountIDs(records); !equalStrings(got, tc.want) {
				t.Fatalf("query %q -> %v want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestMemoryListAccountsHonoursLimit(t *testing.T) {
	account := accountListFixture(t)
	records, err := repository.ListAccounts(context.Background(), account, "", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := accountIDs(records); !equalStrings(got, []string{"dnf:10006", "dnf:10001"}) {
		t.Fatalf("limit=2 -> %v want the two newest by the same ordering", got)
	}
}

// 返回的必须是副本：改结果里的字段不能污染仓储。
func TestMemoryListAccountsReturnsCopies(t *testing.T) {
	account := accountListFixture(t)
	records, err := repository.ListAccounts(context.Background(), account, "10001", 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(records))
	}
	records[0].RepresentAccountName = "Mutated"
	records[0].Metadata = map[string]string{"polluted": "1"}

	again, err := repository.ListAccounts(context.Background(), account, "10001", 10)
	if err != nil || len(again) != 1 {
		t.Fatalf("relist: err=%v len=%d", err, len(again))
	}
	if again[0].RepresentAccountName != "Alice" {
		t.Fatalf("store was mutated through the result: name=%q", again[0].RepresentAccountName)
	}
	if _, polluted := again[0].Metadata["polluted"]; polluted {
		t.Fatal("store metadata was mutated through the result")
	}
}

// 已取消的 ctx 必须直接失败，且不返回任何记录。
func TestMemoryListAccountsRejectsCancelledContext(t *testing.T) {
	account := accountListFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.ListAccounts(ctx, account, "", 10); err == nil {
		t.Fatal("cancelled ctx must not return a successful listing")
	}
}
