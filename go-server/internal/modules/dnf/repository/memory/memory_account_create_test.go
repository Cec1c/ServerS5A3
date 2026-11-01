package memory

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestAccountCreateIfAbsentConcurrentWinnerAndFullRecordPreserved(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAccountStore()
	var wg sync.WaitGroup
	winners := make(chan string, 32)
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := strconv.Itoa(i)
			created, err := repository.CreateAccountIfAbsent(ctx, store, repository.AccountRecord{AccountID: "a", State: "active", Metadata: map[string]string{"winner": id}})
			errs <- err
			if created {
				winners <- id
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("首次创建者必须唯一，实际%d", len(winners))
	}
	winner := <-winners
	a, _, _ := store.Load(ctx, "a")
	if a.Metadata["winner"] != winner {
		t.Fatal("失败竞争者覆盖了首次元数据", a)
	}
	a.HonorExp, a.RepresentAccountName = 88, "冒险团"
	a.CreatedAt, a.UpdatedAt = time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC()
	a.Metadata = map[string]string{"event_seria_returnee_v1": "claimed", "account_cera": "123", "seria_newbie_eligibility_v1": "started"}
	if err := store.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	created, err := repository.CreateAccountIfAbsent(ctx, store, repository.AccountRecord{AccountID: "a", Metadata: map[string]string{"seria_newbie_eligibility_v1": "pending"}})
	got, _, _ := store.Load(ctx, "a")
	if err != nil || created || !reflect.DeepEqual(a, got) {
		t.Fatalf("已有资格、余额、冒险团及时间必须全部保留：created=%t err=%v got=%+v", created, err, got)
	}
}

func TestAccountCreateIfAbsentCancellationAndNameConflictLeaveNoRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newMemoryAccountStore()
	if created, err := repository.CreateAccountIfAbsent(ctx, store, repository.AccountRecord{AccountID: "a"}); created || !errors.Is(err, context.Canceled) {
		t.Fatal(created, err)
	}
	ctx = context.Background()
	if _, found, _ := store.Load(ctx, "a"); found {
		t.Fatal("取消请求仍创建了账号")
	}
	if err := store.Save(ctx, repository.AccountRecord{AccountID: "b", RepresentAccountName: "冒险团"}); err != nil {
		t.Fatal(err)
	}
	if created, err := repository.CreateAccountIfAbsent(ctx, store, repository.AccountRecord{AccountID: "a", RepresentAccountName: "冒险团"}); created || !errors.Is(err, repository.ErrRepresentAccountNameExists) {
		t.Fatal(created, err)
	}
	if _, found, _ := store.Load(ctx, "a"); found {
		t.Fatal("唯一名称冲突仍留下账号")
	}
}
