package repository

import (
	"context"
	"sync"
	"time"
)

// TransactionTiming 仅由指定请求启用，不持有SQL、参数或玩家数据。
// 锁定读取包含数据库锁等待及读取，不能作为纯锁等待时间。
type TransactionTiming struct {
	mu      sync.Mutex
	started time.Time
	value   TransactionTimingSnapshot
}

type TransactionTimingPhase uint8

const (
	TransactionBegin TransactionTimingPhase = iota
	TransactionLockRead
	TransactionRead
	TransactionWrite
	TransactionCommit
	TransactionRollback
	transactionTimingPhases
)

type TransactionTimingSnapshot struct {
	Total     time.Duration
	Durations [transactionTimingPhases]time.Duration
	Calls     [transactionTimingPhases]int
}

type transactionTimingContextKey struct{}

func WithTransactionTiming(ctx context.Context) (context.Context, *TransactionTiming) {
	if ctx == nil {
		ctx = context.Background()
	}
	timing := &TransactionTiming{started: time.Now()}
	return context.WithValue(ctx, transactionTimingContextKey{}, timing), timing
}

func TransactionTimingFromContext(ctx context.Context) *TransactionTiming {
	if ctx == nil {
		return nil
	}
	timing, _ := ctx.Value(transactionTimingContextKey{}).(*TransactionTiming)
	return timing
}

func (t *TransactionTiming) Record(phase TransactionTimingPhase, elapsed time.Duration, calls int) {
	if t == nil || phase >= transactionTimingPhases {
		return
	}
	t.mu.Lock()
	t.value.Durations[phase] += elapsed
	t.value.Calls[phase] += calls
	t.mu.Unlock()
}

func (t *TransactionTiming) Snapshot() TransactionTimingSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	value := t.value
	value.Total = time.Since(t.started)
	return value
}
