package accountnotice

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"sync"
	"testing"
	"time"
)

func TestAccountNoticeConcurrentMonotoneAndIsolation(t *testing.T) {
	ctx := context.Background()
	accounts := memory.NewMemoryGroup().Account
	for _, id := range []string{"a", "b"} {
		if err := accounts.Save(ctx, repo.AccountRecord{AccountID: id, Metadata: map[string]string{"wallet": "keep", "notice_2nd_viewed_200": "1", "notice_2nd_viewed_3": "broken"}}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(id byte) {
			defer wg.Done()
			for j := 0; j < 2; j++ {
				if err := MarkViewed(ctx, accounts, "a", id); err != nil {
					t.Error(err)
				}
			}
		}(byte(i))
	}
	wg.Wait()
	ids, err := Load(ctx, accounts, "a")
	if err != nil || len(ids) != 128 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	for i, id := range ids {
		if int(id) != i {
			t.Fatal(ids)
		}
	}
	fresh, err := Load(ctx, accounts, "b")
	if err != nil || len(fresh) != 0 {
		t.Fatalf("other account=%v err=%v", fresh, err)
	}
	a, _, _ := accounts.Load(ctx, "a")
	if a.Metadata["wallet"] != "keep" {
		t.Fatal(a)
	}
	if err := MarkViewed(ctx, accounts, "a", 128); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := MarkViewed(ctx, accounts, "missing", 42); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := MarkViewed(ctx, accounts, "", 42); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := MarkViewed(cancelled, accounts, "a", 42); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type failingAccount struct {
	repo.AccountRepository
	fail error
}

func (f failingAccount) SaveMetadataEntry(context.Context, string, string, string, time.Time) error {
	return f.fail
}
func TestAccountNoticeSaveFailureDoesNotMarkViewed(t *testing.T) {
	ctx := context.Background()
	accounts := memory.NewMemoryGroup().Account
	if err := accounts.Save(ctx, repo.AccountRecord{AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("storage failed")
	if err := MarkViewed(ctx, failingAccount{accounts, failure}, "a", 42); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	ids, err := Load(ctx, accounts, "a")
	if err != nil || len(ids) != 0 {
		t.Fatalf("%v %v", ids, err)
	}
}
