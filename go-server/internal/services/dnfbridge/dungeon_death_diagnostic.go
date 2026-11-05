package dnfbridge

import "time"

const (
	dungeonDeathTimingEventKind     = "怪物死亡处理分段耗时"
	dungeonDeathTimingDiagnosticMsg = "dnfbridge dungeon death timing diagnostic"
)

// The source event is emitted only after the total death handling time reaches
// 100ms. Preserve its fixed timings in the ordinary JSON log when raw packet
// logging is disabled, without forwarding session identity or arbitrary fields.
func (s *Service) logDungeonDeathTimingDiagnostic(kind string, fields ...any) {
	if kind != dungeonDeathTimingEventKind {
		return
	}
	// Slow combat may affect many sessions at once. Share the existing bounded
	// slow-total sample slot so this fallback cannot amplify an incident into
	// a log storm; no extra per-player state or timer is allocated.
	if s.performance == nil || !s.performance.admitSlowSample(perfMonsterTotal, 300*time.Millisecond, time.Now()) {
		return
	}
	args := []any{"kind", kind}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			continue
		}
		switch key {
		case "对象":
			if value, ok := fields[i+1].(uint32); ok {
				args = append(args, key, value)
			}
		case "总毫秒", "副本锁等待毫秒", "前置校验毫秒", "掉落规划毫秒",
			"死亡通知毫秒", "经验处理毫秒", "任务处理毫秒", "收尾毫秒",
			"任务资格查询毫秒", "杀敌任务毫秒", "任务掉落毫秒", "杀怪任务毫秒",
			"房间缓存毫秒", "清房任务毫秒", "宠物清房毫秒":
			if value, ok := fields[i+1].(int64); ok {
				args = append(args, key, value)
			}
		}
	}
	s.logInfo(dungeonDeathTimingDiagnosticMsg, args...)
}
