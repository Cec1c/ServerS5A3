package dnfbridge

import (
	"context"
	"time"

	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) commitCurrentDungeonExperiencePlanTimed(ctx context.Context, session *gameSession, repos dnfrepo.Group,
	plan progression.CommitPlan, maximumGains []uint32,
) (progression.CommitResult, error) {
	ctx, timing := dnfrepo.WithTransactionTiming(ctx)
	result, err := s.commitCurrentDungeonExperiencePlan(ctx, repos, plan, maximumGains)
	s.logCurrentDungeonTransactionTiming(session, "经验事务内部耗时", timing, err)
	return result, err
}

func (s *Service) logCurrentDungeonTransactionTiming(session *gameSession, event string, timing *dnfrepo.TransactionTiming, err error, extra ...any) {
	value := timing.Snapshot()
	// 两个宠物事务可能各自不足100毫秒，合计却已明显，单笔从50毫秒保留。
	if value.Total < 50*time.Millisecond || value.Calls[dnfrepo.TransactionBegin] == 0 {
		return
	}
	var measured time.Duration
	for _, duration := range value.Durations {
		measured += duration
	}
	other := value.Total - measured
	if other < 0 {
		other = 0
	}
	fields := []any{"总微秒", value.Total.Microseconds(),
		"开启事务微秒", value.Durations[dnfrepo.TransactionBegin].Microseconds(),
		"锁定读取微秒", value.Durations[dnfrepo.TransactionLockRead].Microseconds(),
		"普通读取微秒", value.Durations[dnfrepo.TransactionRead].Microseconds(),
		"写入微秒", value.Durations[dnfrepo.TransactionWrite].Microseconds(),
		"提交微秒", value.Durations[dnfrepo.TransactionCommit].Microseconds(),
		"回滚微秒", value.Durations[dnfrepo.TransactionRollback].Microseconds(),
		"其他处理微秒", other.Microseconds(),
		"事务调用数", value.Calls[dnfrepo.TransactionBegin],
		"锁读次数", value.Calls[dnfrepo.TransactionLockRead],
		"普通读取次数", value.Calls[dnfrepo.TransactionRead],
		"写入次数", value.Calls[dnfrepo.TransactionWrite],
		"提交调用数", value.Calls[dnfrepo.TransactionCommit], "失败", err != nil}
	s.logGameEvent(session, event, append(fields, extra...)...)
}
