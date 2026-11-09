package dnfbridge

import (
	"fmt"
	"testing"
	"time"
)

func TestRealPVFFailureSkipRetriesUnacknowledgedNativePrompt(t *testing.T) {
	for _, phase := range []int{0, 1} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			f, p, at := realAntonProjectionFixture(t, phase)
			b := f.state.Battle
			b.TimedOut = true
			b.Deadline = b.EnterAfter.Add(time.Duration(b.Configuration.PhaseLimitSeconds[phase]) * time.Second)
			at = b.Deadline
			b.AntonFlowConfig.MovieEndSeconds = [4]uint32{20, 20, 20, 20}
			q := newFakeCurrentDungeonDeathTimerQueue()
			q.now = at
			f.service.gameplayTimers = q
			if err := f.service.sendCurrentAntonTerminalResult(p, cloneRuntimeRaidState(*f.state)); err != nil {
				t.Fatal(err)
			}
			// The native NOTI599 leaf can ignore the first prompt while its
			// movie window is absent. Further key repeats must stay bounded,
			// but a later input must be able to deliver that prompt again.
			for i, delta := range []time.Duration{0, 60 * time.Millisecond, 999 * time.Millisecond, time.Second, 1050 * time.Millisecond, 2 * time.Second} {
				q.now = at.Add(delta)
				f.conn.write.Reset()
				dispatchAntonCardRequest(t, f, p, 659, []byte{0})
				r195MovieWantPromptWire(t, f.conn.write.Bytes(), i == 0 || i == 3 || i == 5)
				if f.service.currentRaidFailurePresentationReady(p, *f.state) || len(b.RewardCards[phase]) != 0 || len(b.RewardCardReady[phase]) != 0 || b.PhaseCompleted {
					t.Fatal("skip input completed the movie or created reward eligibility")
				}
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 659, []byte{1})
			r195MovieWantPromptWire(t, f.conn.write.Bytes(), false)
			if !f.service.currentRaidFailurePresentationReady(p, *f.state) {
				t.Fatal("actual completion did not release failure return")
			}
			q.now = at.Add(3 * time.Second)
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 659, []byte{0})
			r195MovieWantPromptWire(t, f.conn.write.Bytes(), false)
			if len(b.RewardCards[phase]) != 0 || len(b.RewardCardReady[phase]) != 0 || len(f.service.raidWeeklyIntents) != 0 {
				t.Fatal("completed failure reopened rewards")
			}
		})
	}
}
