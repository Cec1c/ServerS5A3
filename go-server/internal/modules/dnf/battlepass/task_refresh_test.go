package battlepass

import (
	"encoding/json"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"testing"
	"time"
)

func TestBattlePassTaskRefreshPreservesSeasonAndAssets(t *testing.T) {
	g, c, p, cmd := fixture(t)
	cmd.Now = cmd.Now.Add(24 * time.Hour)
	today := repo.ActivityDay(cmd.Now)
	for id, m := range c.Missions {
		m.Target = 100
		c.Missions[id] = m
	}
	m := c.Missions[1]
	m.Target = 1
	c.Missions[1] = m
	cmd.Counters = []Counter{{Day: today, Kind: 17, Param: -1, Amount: 25}, {Day: repo.ActivityDay(p.Start), Kind: 17, Param: -1, Amount: 10}}
	initial := query(t, g, c, p, cmd)
	s := initial.State
	s.Level, s.XP, s.Premium = 2, 200, true
	s.DailyCompleted, s.WeeklyCompleted = 3, 1
	s.RewardClaimed[0][0], s.RewardClaimed[1][0] = 2, 3
	s.TaskRefresh = &TaskRefresh{Day: DayNumber(cmd.Now), Week: WeekNumber(cmd.Now), Daily: map[uint32]int64{}, Weekly: map[uint32]int64{}}
	for _, id := range c.Daily[0] {
		s.TaskRefresh.Daily[id] = 25
	}
	for _, id := range c.Weekly[0] {
		s.TaskRefresh.Weekly[id] = 35
	}
	a, _, _ := g.Account.Load(nil, cmd.AccountID)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	a.Metadata[MetadataKey] = string(raw)
	if err = g.Account.Save(nil, a); err != nil {
		t.Fatal(err)
	}
	v := query(t, g, c, p, cmd)
	if v.Missions[0].Progress != 0 || v.Missions[4].Progress != 0 || v.Missions[8].Progress != 35 {
		t.Fatal("日周刷新影响赛季或未扣原始未截断计数", v.Missions)
	}
	if v.State.Level != 2 || v.State.XP != 200 || !v.State.Premium || !Claimed(v.State, 0, 2) || !Claimed(v.State, 1, 1) || v.State.DailyCompleted != 3 || v.State.WeeklyCompleted != 1 {
		t.Fatal("刷新丢失权益或赛季累计", v.State)
	}
	cmd = ready(cmd, p)
	cmd.Action, cmd.Target = 1, 1
	if _, err = Execute(nil, g, c, p, cmd); !errors.Is(err, ErrClaim) {
		t.Fatal("旧行为刷新后仍可领取", err)
	}
	cmd.Counters[0].Amount++
	v = query(t, g, c, p, cmd)
	if v.State.XP != 300 || v.State.DailyCompleted != 4 || v.Missions[4].Progress != 1 || v.Missions[8].Progress != 36 {
		t.Fatal("新增行为、经验或赛季进度错误", v.State, v.Missions)
	}
	cmd.Action, cmd.Target = 0, 0
	cmd.CharacterID = "2"
	v = query(t, g, c, p, cmd)
	if v.Missions[0].Flags&2 == 0 || v.State.XP != 300 {
		t.Fatal("刷新后账号共享失效")
	}
	cmd.Now = cmd.Now.Add(24 * time.Hour)
	cmd.Counters = append(cmd.Counters, Counter{Day: repo.ActivityDay(cmd.Now), Kind: 17, Param: -1, Amount: 3})
	v = query(t, g, c, p, cmd)
	if v.Missions[1].Progress != 3 || v.Missions[4].Progress != 4 || v.Missions[8].Progress != 39 || v.Missions[0].Flags&2 != 0 {
		t.Fatal("跨日基线失效或周累计错误", v.Missions)
	}
	cmd.Now = p.Start.AddDate(0, 0, 4).Add(time.Hour)
	cmd.Counters = append(cmd.Counters, Counter{Day: repo.ActivityDay(cmd.Now), Kind: 17, Param: -1, Amount: 1})
	v = query(t, g, c, p, cmd)
	if v.Missions[4].Progress != 1 || v.Missions[8].Progress != 40 || v.State.XP != 300 {
		t.Fatal("跨周基线未失效或赛季权益丢失", v.State, v.Missions)
	}
}

func TestBattlePassTaskRefreshSaturatesAndRejectsNegativeBaseline(t *testing.T) {
	g, c, p, cmd := fixture(t)
	cmd.Counters[0].Amount = math.MaxInt64
	cmd.Counters = append(cmd.Counters, cmd.Counters[0])
	v := query(t, g, c, p, cmd)
	if v.Missions[0].Progress != 1 || v.Missions[8].Progress != 1 {
		t.Fatal("大计数累加溢出")
	}
	s := v.State
	s.TaskRefresh = &TaskRefresh{Day: DayNumber(cmd.Now), Daily: map[uint32]int64{1: -100}}
	cmd.Counters = nil
	v = stateView(c, p, s, cmd)
	if v.Missions[0].Progress != 0 {
		t.Fatal("负基线增加虚假进度")
	}
}
