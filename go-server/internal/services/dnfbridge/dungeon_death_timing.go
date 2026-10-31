package dnfbridge

import "time"

// 只对慢请求记录分段；不改变请求处理、经验提交和通知顺序。
// 接收队列等待及客户端绘制仍不在此计时范围内。
type currentDungeonDeathTiming struct {
	started, last time.Time
	parts         [7]time.Duration
	details       [7]time.Duration
	next          int
}

func (d *currentDungeonDeathTiming) detail(index int, since time.Time) time.Time {
	now := time.Now()
	d.details[index] += now.Sub(since)
	return now
}

func newCurrentDungeonDeathTiming() currentDungeonDeathTiming {
	now := time.Now()
	return currentDungeonDeathTiming{started: now, last: now}
}

func (d *currentDungeonDeathTiming) mark(index int) {
	now := time.Now()
	d.parts[index] += now.Sub(d.last)
	d.last = now
	d.next = index + 1
}

func (d *currentDungeonDeathTiming) finish(s *Service, session *gameSession, objectKey uint32) {
	// 提前返回时把剩余时间记入尚未完成的阶段，避免将失败耗时误记为收尾。
	d.mark(d.next)
	if d.last.Sub(d.started) < 100*time.Millisecond {
		return
	}
	s.logGameEvent(session, "怪物死亡处理分段耗时", "对象", objectKey,
		"总毫秒", d.last.Sub(d.started).Milliseconds(), "副本锁等待毫秒", d.parts[0].Milliseconds(),
		"前置校验毫秒", d.parts[1].Milliseconds(), "掉落规划毫秒", d.parts[2].Milliseconds(),
		"死亡通知毫秒", d.parts[3].Milliseconds(), "经验处理毫秒", d.parts[4].Milliseconds(),
		"任务处理毫秒", d.parts[5].Milliseconds(), "收尾毫秒", d.parts[6].Milliseconds(),
		"任务资格查询毫秒", d.details[0].Milliseconds(), "杀敌任务毫秒", d.details[1].Milliseconds(),
		"任务掉落毫秒", d.details[2].Milliseconds(), "杀怪任务毫秒", d.details[3].Milliseconds(),
		"房间缓存毫秒", d.details[4].Milliseconds(), "清房任务毫秒", d.details[5].Milliseconds(),
		"宠物清房毫秒", d.details[6].Milliseconds())
}
