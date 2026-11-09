package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func TestWeddingMovieEndUsesEachDeliveredDeadlineAndRejectsNewScene(t *testing.T) {
	f := newWeddingChurchLoadingFixture(t)
	for _, p := range []*gameSession{f.first, f.second} {
		f.enter(t, p)
		weddingChurchLoadingFinish(t, f.s, p)
	}
	key, _ := r.WeddingPairScope([]string{"29", "30"})
	ceremony := f.s.wedding.ceremonies[key]
	ceremony.timer.Stop()
	f.now = time.Unix(f.at, 0)
	f.s.runCurrentWeddingCeremony(key, ceremony)
	ceremony.timer.Stop()
	first := f.s.wedding.movieEnds[29]
	second := f.s.wedding.movieEnds[30]
	if first == nil || second == nil {
		t.Fatal("real admission did not own both movies")
	}
	first.timer.Stop()
	second.timer.Stop()
	// A later successful delivery gets its own full interval.
	f.now = time.Unix(f.at+20, 0)
	f.s.wedding.mu.Lock()
	err := f.s.sendCurrentWeddingMovieBeginLocked(second.owner, second.token, second.route, second.kind, second.first, second.second)
	f.s.wedding.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	second = f.s.wedding.movieEnds[30]
	second.timer.Stop()
	weddingChurchLoadingFrames(t, f.first)
	weddingChurchLoadingFrames(t, f.second)
	f.now = time.Unix(f.at+60, 0)
	f.s.runCurrentWeddingCeremony(key, ceremony)
	weddingChurchLoadingFrames(t, f.first)
	weddingChurchLoadingFrames(t, f.second)
	f.s.runCurrentWeddingMovieEnd(first)
	frames := weddingChurchLoadingFrames(t, f.first)
	if len(frames) != 1 || frames[0].op != 999 || frames[0].body[0] != 1 {
		t.Fatalf("first native cleanup %v", frames)
	}
	f.s.runCurrentWeddingMovieEnd(second)
	second.timer.Stop()
	if frames = weddingChurchLoadingFrames(t, f.second); len(frames) != 0 {
		t.Fatal("late recipient was cut off by hosts' timer")
	}
	f.now = time.Unix(f.at+80, 0)
	f.s.runCurrentWeddingMovieEnd(second)
	frames = weddingChurchLoadingFrames(t, f.second)
	if len(frames) != 1 || frames[0].op != 999 || frames[0].body[0] != 1 {
		t.Fatal("late recipient did not receive own cleanup")
	}
	// An old timer must never exit a replacement scene, even for the same ID.
	f.s.wedding.mu.Lock()
	err = f.s.sendCurrentWeddingMovieBeginLocked(first.owner, first.token, first.route, first.kind, first.first, first.second)
	f.s.wedding.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	stale := f.s.wedding.movieEnds[29]
	stale.timer.Stop()
	weddingChurchLoadingFrames(t, f.first)
	presence, _ := f.s.onlinePlayers.PlayerForCharacter(29)
	f.s.onlinePlayers.EnterArea(&presence)
	f.now = time.Unix(stale.deadline, 0)
	f.s.runCurrentWeddingMovieEnd(stale)
	if len(weddingChurchLoadingFrames(t, f.first)) != 0 || f.s.wedding.movieEnds[29] != nil {
		t.Fatal("stale movie timer affected a new scene")
	}
}

func TestWeddingStartupRecoveryContinuesAfterBadReferenceAndMigratesBeforeInfo(t *testing.T) {
	f := newWeddingChurchLoadingFixture(t)
	f.s.wedding.guestRules = &wedding.GuestRules{Capacity: map[byte]int{0: 10}}
	if err := f.s.sendCurrentInitialWeddingState(f.first); err != nil {
		t.Fatal(err)
	}
	var token uint32
	for _, frame := range weddingChurchLoadingFrames(t, f.first) {
		if frame.op == 1000 {
			token = binary.LittleEndian.Uint32(frame.body)
		}
	}
	owner, _ := f.s.currentWeddingOwner()
	ledger, found, err := owner.ReadGuestLedger(context.Background(), []string{"29", "30"}, f.at)
	if err != nil || !found || token == 0 || ledger.Key != token || ledger.ReservationPayment.PayerID != "" || len(ledger.Invitations) != 0 {
		t.Fatalf("initial token/metadata not synchronized: %d %+v %v", token, ledger, err)
	}
	record, _, _ := f.repos.Settings.Load(context.Background(), r.WeddingGuestIndexScope)
	var index wedding.GuestIndex
	if err = json.Unmarshal([]byte(record.Values["index"]), &index); err != nil {
		t.Fatal(err)
	}
	// Put the bad reference first by sorted key; the valid run still schedules.
	bad := index.Runs[token]
	delete(index.Runs, token)
	index.Runs[token+1] = bad
	index.Runs[token] = wedding.GuestRunReference{Hosts: [2]string{"65000", "65001"}, At: f.at}
	raw, _ := json.Marshal(index)
	record.Values["index"] = string(raw)
	if err = f.repos.Settings.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	f.s.stopCurrentWeddingCeremonyTimers()
	f.s.wedding.mu.Lock()
	f.s.wedding.ceremoniesStopped = false
	f.s.wedding.ceremonies = nil
	f.s.wedding.mu.Unlock()
	f.s.recoverCurrentWeddingRuns()
	key, _ := r.WeddingPairScope([]string{"29", "30"})
	if f.s.wedding.ceremonies[key] == nil || f.s.wedding.ceremonies[key].timer == nil {
		t.Fatal("bad reference starved a valid offline run")
	}
	if f.s.wedding.recoveryTimer == nil {
		t.Fatal("bad reference did not retain a bounded retry")
	}
}

func TestWeddingRoomTreeProjectionNormalizesDayAndExpiryWithoutWritingRoom(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-service-day", true: "expired"}[expired], func(t *testing.T) {
			f := newWeddingChurchLoadingFixture(t)
			f.s.wedding.extensionRules = &wedding.ExtensionRules{Trees: map[int64]wedding.TreeRule{1: {Index: 1, DailyWater: 2, MaximumGrowth: 30}}}
			tree := wedding.TreeState{Index: 1, Expires: f.now.Unix() + 3600, Growth: 20, WaterDay: currentWeddingDay(f.now) - 1, WaterCount: 2}
			if expired {
				tree.Expires = f.now.Unix()
			}
			room := wedding.Room{Version: 1, Tree: tree}
			if err := f.s.writeCurrentWeddingRoom(f.first, room); err != nil {
				t.Fatal(err)
			}
			body := guildReply(t, f.first, 1008)
			if len(body) != 17 || body[16] != 0 {
				t.Fatalf("old service-day water count remained visible: %x", body)
			}
			index := binary.LittleEndian.Uint32(body[4:])
			if expired && index != 0 || !expired && index != 1 {
				t.Fatalf("tree expiry projection %x", body)
			}
			if room.Tree != tree {
				t.Fatal("projection mutated caller's durable room")
			}
		})
	}
}
