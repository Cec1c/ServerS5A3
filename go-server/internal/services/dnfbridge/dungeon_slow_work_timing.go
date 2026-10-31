package dnfbridge

import "time"

// 分段计时只在总耗时达到100毫秒时输出，不改变锁、事务或通知顺序。
type currentDungeonSlowWorkTiming struct {
	started, last time.Time
	parts         [4]time.Duration
	phase         int
}

func newCurrentDungeonSlowWorkTiming() currentDungeonSlowWorkTiming {
	now := time.Now()
	return currentDungeonSlowWorkTiming{started: now, last: now}
}

func (d *currentDungeonSlowWorkTiming) next() {
	now := time.Now()
	d.parts[d.phase] += now.Sub(d.last)
	d.last = now
	d.phase++
}

func (d *currentDungeonSlowWorkTiming) finish(s *Service, session *gameSession, event string, labels [4]string) {
	d.next()
	if d.last.Sub(d.started) < 100*time.Millisecond {
		return
	}
	s.logGameEvent(session, event, "总毫秒", d.last.Sub(d.started).Milliseconds(),
		labels[0], d.parts[0].Milliseconds(), labels[1], d.parts[1].Milliseconds(),
		labels[2], d.parts[2].Milliseconds(), labels[3], d.parts[3].Milliseconds())
}
