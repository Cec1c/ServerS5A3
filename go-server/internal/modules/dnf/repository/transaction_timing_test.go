package repository

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestTransactionTimingContextIsolationAndCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	first, a := WithTransactionTiming(parent)
	second, b := WithTransactionTiming(parent)
	if TransactionTimingFromContext(first) != a || TransactionTimingFromContext(second) != b || a == b || TransactionTimingFromContext(parent) != nil || TransactionTimingFromContext(nil) != nil {
		t.Fatal("计时不得泄漏到其他请求")
	}
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() { defer group.Done(); a.Record(TransactionRead, time.Microsecond, 1) }()
	}
	group.Wait()
	value := a.Snapshot()
	if value.Calls[TransactionRead] != 10 || value.Durations[TransactionRead] != 10*time.Microsecond || b.Snapshot().Calls[TransactionRead] != 0 {
		t.Fatal("并发记录或请求隔离失败", value)
	}
	cancel()
	if first.Err() != context.Canceled || second.Err() != context.Canceled {
		t.Fatal("派生上下文必须保留原取消语义")
	}
}
