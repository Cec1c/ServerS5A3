package dnfbridge

import (
	"context"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type selectAccountProjectionReadCounter struct {
	dnfrepo.AccountRepository
	loads int
}

func (r *selectAccountProjectionReadCounter) Load(ctx context.Context, accountID string) (dnfrepo.AccountRecord, bool, error) {
	r.loads++
	return r.AccountRepository.Load(ctx, accountID)
}

func TestP1SelectAccountProjectionReadsOneSnapshotAndPreservesDefaults(t *testing.T) {
	ctx := context.Background()
	repos := testRepositoryGroup()
	account := dnfrepo.AccountRecord{AccountID: "dnf:select-projection", Metadata: map[string]string{}}
	setCurrentAccountCera(&account, 17)
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}

	counter := &selectAccountProjectionReadCounter{AccountRepository: repos.Account}
	cera, premium := loadCurrentSelectAccountProjection(ctx, counter, account.AccountID, time.Now().UTC())
	if counter.loads != 1 || cera != 17 || len(premium) == 0 {
		t.Fatalf("loads=%d cera=%d premium=%x", counter.loads, cera, premium)
	}

	cera, premium = loadCurrentSelectAccountProjection(ctx, nil, account.AccountID, time.Now().UTC())
	if cera != 0 || len(premium) != 1 || premium[0] != 0 {
		t.Fatalf("nil repository defaults changed: cera=%d premium=%x", cera, premium)
	}
}
