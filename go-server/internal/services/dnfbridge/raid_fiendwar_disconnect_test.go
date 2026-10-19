package dnfbridge

import (
	"testing"
	"time"
)

func TestRealPVFCurrentFiendWarOneDisconnectKeepsTheOtherParticipant(t *testing.T) {
	f, p := actualCurrentFiendWar(t, 2)
	f.state.Started = true
	b := f.state.Battle
	now := time.Now()
	b.EnterAfter = now.Add(-time.Minute)
	b.Deadline = now.Add(time.Hour)
	area := 0
	for id, a := range b.FiendWar.Config.Areas {
		if a.Dungeon > 0 && b.FiendWar.AreaOpen(id) {
			area = id
			break
		}
	}
	if area == 0 {
		t.Fatal("没有实际遭遇战")
	}
	for _, peer := range p {
		b.FiendWar.MemberAreas[peer.selectedCharacterID] = area
		setCurrentFiendWarTestOrigin(peer, area)
	}
	dungeon := uint32(b.FiendWar.Config.Areas[area].Dungeon)
	r0, r1 := raidCompletedRuntimeFixture(t, p[0], dungeon), raidCompletedRuntimeFixture(t, p[1], dungeon)
	if err := f.service.freezeCurrentRaidDungeonGroupEntry(p[0], r0, []*runtimePartyPreparedDungeonEntry{{session: p[1], runtime: r1}}); err != nil {
		t.Fatal(err)
	}
	run := r0.raidEntry.RunID
	b.Participation = map[uint16]currentRaidParticipation{19: {RunID: run, Runtime: r0, Session: p[0]}, 20: {RunID: run, Runtime: r1, Session: p[1]}}
	// A stale detach of another runtime cannot retire the adopted owner's run.
	stale := *r0
	f.service.releaseCurrentRaidParticipation(&stale, 19)
	if b.RunDepartures[run][19] {
		t.Fatal("旧运行时退役了当前参与者")
	}
	f.service.releaseCurrentRaidParticipation(r0, 19)
	if b.FiendWarEncounters[area].RunID != run || b.FiendWar.Areas[area].Group == 0 {
		t.Fatal("一人断线强制退出了其他参战者")
	}
	b.FiendWar.MemberAreas[19] = b.FiendWar.Config.WaitingArea // 重连回到等待室。
	if err := validateCurrentFiendWarDungeon(b, 20, r1); err != nil {
		t.Fatal("已退出的同伴阻止其他角色继续", err)
	}
	if _, changed, err := f.service.reduceCurrentRaidDungeonClearLocked(p[1], r1, now); err != nil || !changed {
		t.Fatal("留下的参战者不能通关", changed, err)
	}
}
