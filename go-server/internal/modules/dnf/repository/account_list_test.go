package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// stubAccountLister 只记录被调用时收到的 query/limit，用来钉死 ListAccounts 的
// 归一化契约（我们不关心它返回什么，只关心"传下去的是什么"）。
type stubAccountLister struct {
	AccountRepository
	calls  int
	query  string
	limit  int
	result []AccountRecord
	err    error
}

func (s *stubAccountLister) ListAccounts(_ context.Context, query string, limit int) ([]AccountRecord, error) {
	s.calls++
	s.query = query
	s.limit = limit
	return s.result, s.err
}

// 没接能力的仓储必须**明确失败**，绝不能退化成空列表 —— 否则 GM 面板会把
// "这个能力没实现"显示成"没有这个账号"。
func TestListAccountsUnavailableWithoutLister(t *testing.T) {
	if _, err := ListAccounts(context.Background(), nil, "", 10); !errors.Is(err, ErrAccountListUnavailable) {
		t.Fatalf("nil repo err=%v want ErrAccountListUnavailable", err)
	}
	plain := &stubAccountRepoOnly{}
	if _, err := ListAccounts(context.Background(), plain, "", 10); !errors.Is(err, ErrAccountListUnavailable) {
		t.Fatalf("non-lister repo err=%v want ErrAccountListUnavailable", err)
	}
}

// stubAccountRepoOnly 只满足 AccountRepository，刻意**不**实现 AccountLister。
type stubAccountRepoOnly struct{ AccountRepository }

func TestListAccountsNormalizesQueryAndLimit(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		limit     int
		wantQuery string
		wantLimit int
	}{
		{name: "zero limit defaults to 50", query: "  dnf:1  ", limit: 0, wantQuery: "dnf:1", wantLimit: 50},
		{name: "negative limit defaults to 50", query: "", limit: -7, wantQuery: "", wantLimit: 50},
		{name: "limit above 200 clamps", query: "ab", limit: 5000, wantQuery: "ab", wantLimit: 200},
		{name: "limit exactly 200 stays", query: "ab", limit: 200, wantQuery: "ab", wantLimit: 200},
		{name: "limit 1 stays", query: "ab", limit: 1, wantQuery: "ab", wantLimit: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubAccountLister{}
			if _, err := ListAccounts(context.Background(), stub, tc.query, tc.limit); err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if stub.calls != 1 {
				t.Fatalf("lister calls=%d want 1", stub.calls)
			}
			if stub.query != tc.wantQuery {
				t.Fatalf("query=%q want %q", stub.query, tc.wantQuery)
			}
			if stub.limit != tc.wantLimit {
				t.Fatalf("limit=%d want %d", stub.limit, tc.wantLimit)
			}
		})
	}
}

func TestListAccountsPassesThroughResultAndError(t *testing.T) {
	want := []AccountRecord{{AccountID: "dnf:10001"}, {AccountID: "dnf:10006"}}
	stub := &stubAccountLister{result: want}
	got, err := ListAccounts(context.Background(), stub, "", 10)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records=%+v want %+v", got, want)
	}

	sentinel := errors.New("boom")
	stub = &stubAccountLister{err: sentinel}
	if _, err := ListAccounts(context.Background(), stub, "", 10); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v want the lister's own error", err)
	}
}

// nil ctx 要按 context.Background() 走（不是 panic），而**已取消**的 ctx 必须
// 直接返回 ctx 的错误、一次都不打扰底层仓储。
func TestListAccountsContextHandling(t *testing.T) {
	stub := &stubAccountLister{}
	if _, err := ListAccounts(nil, stub, "x", 10); err != nil {
		t.Fatalf("nil ctx err=%v want nil", err)
	}
	if stub.calls != 1 {
		t.Fatalf("lister calls=%d want 1 for nil ctx", stub.calls)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	stub = &stubAccountLister{}
	if _, err := ListAccounts(cancelled, stub, "x", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx err=%v want context.Canceled", err)
	}
	if stub.calls != 0 {
		t.Fatalf("lister calls=%d want 0 for a cancelled ctx", stub.calls)
	}
}
