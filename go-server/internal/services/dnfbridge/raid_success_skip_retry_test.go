package dnfbridge

import (
	"testing"
	"time"
)

// A successful socket write is not a receipt from the native movie window.
// If that window was absent on the first prompt, later input must recover it.
func TestRaidSuccessfulMovieSkipRetriesUntilActualCompletion(t *testing.T) {
	for _, phase := range []string{"anton_phase0", "anton_phase1", "luke_phase0", "luke_phase1", "fiendwar"} {
		t.Run(phase, func(t *testing.T) {
			f, sessions := r195MovieStageFixture(t, phase)
			p := sessions[0]
			b := f.state.Battle
			q := newFakeCurrentDungeonDeathTimerQueue()
			at := time.Now()
			q.now = at
			f.service.gameplayTimers = q
			for i, delta := range []time.Duration{0, 60 * time.Millisecond, 999 * time.Millisecond, time.Second, 1050 * time.Millisecond, 2 * time.Second} {
				q.now = at.Add(delta)
				f.conn.write.Reset()
				dispatchAntonCardRequest(t, f, p, 659, []byte{0})
				r195MovieWantPromptWire(t, f.conn.write.Bytes(), i == 0 || i == 3 || i == 5)
				if p.raidProjection.movieCompleted || b.RewardCardReady[b.Phase][p.selectedCharacterID] || len(b.RewardCards[b.Phase]) != 0 {
					t.Fatal("skip input advanced the card stage without actual movie completion")
				}
			}
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 659, []byte{1})
			if !p.raidProjection.movieCompleted || !b.RewardCardReady[b.Phase][p.selectedCharacterID] || len(antonNativeCardBodies(t, f)) != 2 {
				t.Fatal("actual completion did not open both native party masks")
			}
			q.now = at.Add(3 * time.Second)
			f.conn.write.Reset()
			dispatchAntonCardRequest(t, f, p, 659, []byte{0})
			r195MovieWantPromptWire(t, f.conn.write.Bytes(), false)
			if len(b.RewardCards[b.Phase]) != 0 {
				t.Fatal("prompt recovery minted a reward")
			}
		})
	}
}
