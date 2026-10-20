package memory

import (
	"context"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestMemoryAccountStoryDigestViewedLevelIsMonotonicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	group := NewMemoryGroup()
	if err := group.Account.Save(ctx, repository.AccountRecord{
		AccountID: "dnf:10001",
		Metadata:  map[string]string{"selector_adventure_info_slot": "4"},
	}); err != nil {
		t.Fatal(err)
	}
	load := func(t *testing.T) repository.AccountRecord {
		t.Helper()
		record, found, err := group.Account.Load(ctx, "dnf:10001")
		if err != nil || !found {
			t.Fatalf("load account found=%t err=%v", found, err)
		}
		return record
	}

	if err := repository.AdvanceAccountStoryDigestViewedLevel(ctx, group.Account, "dnf:10001", 40); err != nil {
		t.Fatal(err)
	}
	if got := repository.AccountStoryDigestViewedLevel(load(t)); got != 40 {
		t.Fatalf("viewed level after first advance = %d, want 40", got)
	}
	// 幂等：重放同一个等级不改变结果，也不丢其它元数据键。
	if err := repository.AdvanceAccountStoryDigestViewedLevel(ctx, group.Account, "dnf:10001", 40); err != nil {
		t.Fatal(err)
	}
	record := load(t)
	if got := repository.AccountStoryDigestViewedLevel(record); got != 40 {
		t.Fatalf("viewed level after replay = %d, want 40", got)
	}
	if record.Metadata["selector_adventure_info_slot"] != "4" {
		t.Fatalf("unrelated metadata key lost: %#v", record.Metadata)
	}
	// 单调：更低的上报（跳过动画后的旧等级 / 换小号登录）不回退记录。
	if err := repository.AdvanceAccountStoryDigestViewedLevel(ctx, group.Account, "dnf:10001", 12); err != nil {
		t.Fatal(err)
	}
	if got := repository.AccountStoryDigestViewedLevel(load(t)); got != 40 {
		t.Fatalf("viewed level regressed to %d, want 40", got)
	}
	if err := repository.AdvanceAccountStoryDigestViewedLevel(ctx, group.Account, "dnf:10001", 90); err != nil {
		t.Fatal(err)
	}
	if got := repository.AccountStoryDigestViewedLevel(load(t)); got != 90 {
		t.Fatalf("viewed level after higher advance = %d, want 90", got)
	}

	if err := repository.AdvanceAccountStoryDigestViewedLevel(ctx, group.Account, "dnf:missing", 90); err != repository.ErrAccountStoryDigestAccountMissing {
		t.Fatalf("missing account error = %v, want %v", err, repository.ErrAccountStoryDigestAccountMissing)
	}
	if err := repository.AdvanceAccountStoryDigestViewedLevel(ctx, group.Account, "  ", 90); err != repository.ErrAccountStoryDigestAccountMissing {
		t.Fatalf("blank account error = %v, want %v", err, repository.ErrAccountStoryDigestAccountMissing)
	}
	if err := repository.AdvanceAccountStoryDigestViewedLevel(ctx, nil, "dnf:10001", 90); err != repository.ErrAccountStoryDigestAdvanceUnavailable {
		t.Fatalf("nil repository error = %v, want %v", err, repository.ErrAccountStoryDigestAdvanceUnavailable)
	}
}

func TestMemoryAccountStoryDigestViewedLevelConcurrentAdvancesKeepMaximum(t *testing.T) {
	ctx := context.Background()
	group := NewMemoryGroup()
	if err := group.Account.Save(ctx, repository.AccountRecord{AccountID: "dnf:10005"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for level := uint32(1); level <= 32; level++ {
		wg.Add(1)
		go func(level uint32) {
			defer wg.Done()
			if err := repository.AdvanceAccountStoryDigestViewedLevel(ctx, group.Account, "dnf:10005", level); err != nil {
				t.Errorf("advance %d: %v", level, err)
			}
		}(level)
	}
	wg.Wait()
	record, found, err := group.Account.Load(ctx, "dnf:10005")
	if err != nil || !found {
		t.Fatalf("load account found=%t err=%v", found, err)
	}
	if got := repository.AccountStoryDigestViewedLevel(record); got != 32 {
		t.Fatalf("concurrent viewed level = %d, want 32", got)
	}
}

func TestAccountStoryDigestViewedLevelParsingDefaultsToNeverWatched(t *testing.T) {
	for _, test := range []struct {
		value string
		want  uint32
	}{
		{value: "", want: 0},
		{value: "   ", want: 0},
		{value: "0", want: 0},
		{value: " 90 ", want: 90},
		{value: "not-a-level", want: 0},
		{value: "-1", want: 0},
	} {
		if got := repository.ParseAccountStoryDigestViewedLevel(test.value); got != test.want {
			t.Fatalf("parse %q = %d, want %d", test.value, got, test.want)
		}
	}
	if got := repository.FormatAccountStoryDigestViewedLevel(90); got != "90" {
		t.Fatalf("format 90 = %q", got)
	}
	if got := repository.AccountStoryDigestViewedLevel(repository.AccountRecord{}); got != 0 {
		t.Fatalf("nil metadata viewed level = %d, want 0", got)
	}
}
