package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestRealPVFCurrentRaidDisconnectPublishesOfflineRetainedSeat(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"etc/raid/anton.etc", "etc/raid/luke/luke.etc"} {
		text, err := archive.ReadText(p)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := pvf.Parse(p, text)
		if err != nil {
			t.Fatal(err)
		}
		seconds, ok := doc.Int("DISCONN MEMBER WAIT TIME")
		if !ok || seconds != 300 {
			t.Fatal("actual disconnect grace", p, seconds)
		}
	}
	s, _, member, lc, _, state := prepareApprovedCurrentRaid(t)
	s.initialEquipmentArchive = archive
	s.shutdownGameSessionEvents(member, false)
	if _, bound := s.boundGameSessionCharacterSnapshot(member); bound {
		t.Fatal("fixture did not finish canonical shutdown")
	}
	after, retained := s.runtimeRaidForMember(member.selectedCharacterID)
	if !retained {
		t.Fatal("disconnect dropped retained seat before actual grace")
	}
	idx := runtimeRaidMemberIndex(after, member.selectedCharacterID)
	if idx < 0 || after.Members[idx].UserState != 0 {
		t.Fatalf("disconnected retained member still online: key=%x member=%d state=%d", state.RaidKey, member.selectedCharacterID, after.Members[idx].UserState)
	}
	if lc.write.Len() == 0 {
		t.Fatal("offline presence never projected to peer")
	}
}

func TestRealPVFCurrentRaidPresenceSeatLifecycle(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("actual PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"etc/raid/anton.etc", "etc/raid/luke/luke.etc"} {
		config, err := raid.LoadBattleConfiguration(archive, p)
		if err != nil || config.DisconnectWaitSeconds != 300 {
			t.Fatal(p, config.DisconnectWaitSeconds, err)
		}
	}
	newFixture := func(t *testing.T) (*Service, *gameSession, *gameSession, *fakeCurrentDungeonDeathTimerQueue) {
		s, l, m, _, _, _ := prepareApprovedCurrentRaid(t)
		s.initialEquipmentArchive = archive
		q := newFakeCurrentDungeonDeathTimerQueue()
		s.gameplayTimers = q
		return s, l, m, q
	}
	freshOwner := func(s *Service, old *gameSession) *gameSession {
		fresh := &gameSession{conn: &bufferConn{}, accountID: old.accountID, selectedCharacterID: old.selectedCharacterID, channel: old.channel, loginPacketSeen: true, accountFromToken: true}
		s.bindGameSessionCharacter(fresh, fresh.selectedCharacterID)
		return fresh
	}
	t.Run("actual_luke_directory_uses_its_resource_wait", func(t *testing.T) {
		s, l, m, _, _ := prepareAccountFriendTest(t)
		l.channel = channelcatalog.Channel{ID: 32, Type: dnfenum.HiddenRaidType}
		m.channel = l.channel
		s.initialEquipmentArchive = archive
		if err := s.handleCurrentCreateRaid(l, 664, currentRaidRequestFixture(1, "luke-presence", 0, 0, 0)); err != nil {
			t.Fatal(err)
		}
		request := currentRaidApplyFixture(l.selectedCharacterID)
		request[0] = 32
		request[9] = byte(s.channelAdvertiseID())
		if err := s.handleGameCommand(m, 1, 820, request); err != nil {
			t.Fatal(err)
		}
		if err := s.handleGameCommand(l, 1, 11, currentRaidApprovalFixture(m.selectedCharacterID)); err != nil {
			t.Fatal(err)
		}
		state, ok := s.runtimeRaidForMember(m.selectedCharacterID)
		if !ok || state.RouteOrRaidType != 1 {
			t.Fatal("public Luke owner not created")
		}
		q := newFakeCurrentDungeonDeathTimerQueue()
		s.gameplayTimers = q
		s.shutdownGameSessionEvents(m, false)
		seat, ok := s.currentRaidDisconnects[m.selectedCharacterID]
		if !ok || !seat.ExpiresAt.Equal(q.Now().Add(300*time.Second)) {
			t.Fatal("Luke resource grace missing")
		}
		q.now = q.now.Add(time.Second)
		fresh := freshOwner(s, m)
		if _, ok := s.boundGameSessionCharacterSnapshot(fresh); !ok {
			t.Fatal("Luke canonical restore")
		}
		if _, ok := s.currentRaidDisconnects[m.selectedCharacterID]; ok {
			t.Fatal("Luke reservation not cancelled")
		}
	})
	t.Run("exact_deadline_not_early_and_idempotent", func(t *testing.T) {
		s, _, m, q := newFixture(t)
		s.shutdownGameSessionEvents(m, false)
		seat := s.currentRaidDisconnects[m.selectedCharacterID]
		if !seat.ExpiresAt.Equal(q.Now().Add(300 * time.Second)) {
			t.Fatal("resource deadline not used")
		}
		s.expireCurrentRaidDisconnectSeats(seat.ExpiresAt.Add(-time.Nanosecond))
		if _, ok := s.runtimeRaidForMember(m.selectedCharacterID); !ok {
			t.Fatal("early eviction")
		}
		s.expireCurrentRaidDisconnectSeats(seat.ExpiresAt)
		if _, ok := s.runtimeRaidForMember(m.selectedCharacterID); ok {
			t.Fatal("deadline did not evict")
		}
		s.expireCurrentRaidDisconnectSeats(seat.ExpiresAt.Add(time.Second))
		if len(s.currentRaidDisconnects) != 0 {
			t.Fatal("expiry not idempotent")
		}
	})
	t.Run("normal_rebind_cancels_expiry_without_self_roster", func(t *testing.T) {
		s, _, m, q := newFixture(t)
		s.shutdownGameSessionEvents(m, false)
		oldSeat := s.currentRaidDisconnects[m.selectedCharacterID]
		q.now = q.now.Add(299 * time.Second)
		fresh := freshOwner(s, m)
		if _, ok := s.currentRaidDisconnects[m.selectedCharacterID]; ok {
			t.Fatal("seat not cancelled")
		}
		state, ok := s.runtimeRaidForMember(m.selectedCharacterID)
		if !ok || state.Members[runtimeRaidMemberIndex(state, m.selectedCharacterID)].UserState != 1 {
			t.Fatal("owner not restored")
		}
		if fresh.conn.(*bufferConn).write.Len() != 0 {
			t.Fatal("roster sent before own raid object")
		}
		s.expireCurrentRaidDisconnectSeats(oldSeat.ExpiresAt)
		if _, ok := s.runtimeRaidForMember(m.selectedCharacterID); !ok {
			t.Fatal("cancelled expiry evicted fresh owner")
		}
	})
	for _, variant := range []string{"channel", "type", "account", "unauth", "expired", "clock_rollback"} {
		t.Run("rebind_"+variant+"_does_not_restore", func(t *testing.T) {
			s, _, m, q := newFixture(t)
			s.shutdownGameSessionEvents(m, false)
			seat := s.currentRaidDisconnects[m.selectedCharacterID]
			fresh := &gameSession{conn: &bufferConn{}, accountID: m.accountID, selectedCharacterID: m.selectedCharacterID, channel: m.channel, loginPacketSeen: true, accountFromToken: true}
			switch variant {
			case "channel":
				fresh.channel.ID++
			case "type":
				fresh.channel.Type = 0
			case "account":
				fresh.accountID = "foreign-account"
			case "unauth":
				fresh.accountFromToken = false
			case "expired":
				q.now = seat.ExpiresAt
			case "clock_rollback":
				q.now = seat.DisconnectedAt.Add(-time.Second)
			}
			s.bindGameSessionCharacter(fresh, fresh.selectedCharacterID)
			_, reserved := s.currentRaidDisconnects[m.selectedCharacterID]
			if variant == "expired" && reserved {
				t.Fatal("late canonical bind left an expired directory visible")
			}
			if variant != "expired" && !reserved {
				t.Fatal("foreign owner cancelled reservation")
			}
			s.expireCurrentRaidDisconnectSeats(seat.ExpiresAt)
			if _, ok := s.runtimeRaidForMember(m.selectedCharacterID); ok {
				t.Fatal("foreign/late owner rescued expired seat")
			}
		})
	}
	t.Run("late_old_shutdown_cannot_replace_new_disconnect", func(t *testing.T) {
		s, _, m, q := newFixture(t)
		oldIdentity, ok := s.boundGameSessionCharacterSnapshot(m)
		if !ok {
			t.Fatal("fixture")
		}
		s.shutdownGameSessionEvents(m, false)
		q.now = q.now.Add(100 * time.Second)
		fresh := freshOwner(s, m)
		s.shutdownGameSessionEvents(fresh, false)
		newSeat := s.currentRaidDisconnects[m.selectedCharacterID]
		if newSeat.Session != fresh {
			t.Fatal("new owner not recorded")
		}
		s.noteCurrentRaidDisconnect(oldIdentity)
		if !reflect.DeepEqual(newSeat, s.currentRaidDisconnects[m.selectedCharacterID]) {
			t.Fatal("late old cleanup changed newer reservation")
		}
		s.expireCurrentRaidDisconnectSeats(q.Now().Add(200 * time.Second))
		if _, ok := s.runtimeRaidForMember(m.selectedCharacterID); !ok {
			t.Fatal("old deadline evicted newer seat")
		}
	})
	t.Run("live_auxiliary_handover_never_marks_offline", func(t *testing.T) {
		s, _, m, _ := newFixture(t)
		m.connID = "presence-original"
		s.registerLiveGameSession(m)
		fresh := freshOwner(s, m)
		fresh.connID = "presence-auxiliary"
		s.registerLiveGameSession(fresh)
		// The town resident keeps original as canonical until its real shutdown.
		if canonical, ok := s.boundGameSessionCharacterSnapshot(m); !ok || canonical.session != m {
			t.Fatal("auxiliary stole resident directory")
		}
		s.shutdownGameSessionEvents(m, false)
		if _, ok := s.boundGameSessionCharacterSnapshot(fresh); !ok {
			t.Fatal("normal handover failed")
		}
		if _, ok := s.currentRaidDisconnects[m.selectedCharacterID]; ok {
			t.Fatal("handover created false offline seat")
		}
		state, ok := s.runtimeRaidForMember(m.selectedCharacterID)
		if !ok || state.Members[runtimeRaidMemberIndex(state, m.selectedCharacterID)].UserState != 1 {
			t.Fatal("handover marked live character offline")
		}
	})
	t.Run("delayed_central_callback_reads_current_clock", func(t *testing.T) {
		s, _, m, q := newFixture(t)
		s.shutdownGameSessionEvents(m, false)
		seat := s.currentRaidDisconnects[m.selectedCharacterID]
		if err := s.scheduleCurrentRaidClock(context.Background()); err != nil {
			t.Fatal(err)
		}
		if q.scheduleCount != 1 {
			t.Fatal("disconnect installed per-member timer", q.scheduleCount)
		}
		task := q.active[currentRaidClockName]
		if task == nil {
			t.Fatal("no process raid clock")
		}
		q.now = seat.ExpiresAt
		task.callback(seat.DisconnectedAt.Add(time.Second))
		if _, ok := s.runtimeRaidForMember(m.selectedCharacterID); ok {
			t.Fatal("delayed callback trusted stale due time")
		}
		if q.scheduleCount != 2 {
			t.Fatal("process clock stopped")
		}
	})
	t.Run("leader_expiry_promotes_remaining_online_member", func(t *testing.T) {
		s, l, m, _ := newFixture(t)
		s.shutdownGameSessionEvents(l, false)
		seat := s.currentRaidDisconnects[l.selectedCharacterID]
		s.expireCurrentRaidDisconnectSeats(seat.ExpiresAt)
		state, ok := s.runtimeRaidForMember(m.selectedCharacterID)
		if !ok || state.LeaderID != m.selectedCharacterID || len(state.Members) != 1 {
			t.Fatal("expired leader stranded remaining member")
		}
	})
	t.Run("old_aggregate_reservation_does_not_remove_replacement", func(t *testing.T) {
		s, _, m, _ := newFixture(t)
		s.shutdownGameSessionEvents(m, false)
		seat := s.currentRaidDisconnects[m.selectedCharacterID]
		// Defensive exact-pointer control, not claimed as a live key-reuse path.
		old := s.raids[seat.RaidKey]
		replacement := cloneRuntimeRaidState(*old)
		replacement.Members[runtimeRaidMemberIndex(replacement, m.selectedCharacterID)].UserState = 1
		s.raids[seat.RaidKey] = &replacement
		s.expireCurrentRaidDisconnectSeats(seat.ExpiresAt)
		state, ok := s.runtimeRaidForMember(m.selectedCharacterID)
		if !ok || state.Members[runtimeRaidMemberIndex(state, m.selectedCharacterID)].UserState != 1 {
			t.Fatal("stale aggregate cleanup changed replacement")
		}
	})
	t.Run("offline_retained_seat_cannot_be_frozen_into_paid_start", func(t *testing.T) {
		s, l, m, _ := newFixture(t)
		s.shutdownGameSessionEvents(m, false)
		state, ok, err := s.startRuntimeRaidWithEntryCost(l)
		if err != nil || ok || state.Started {
			t.Fatal("offline retained member admitted", ok, err)
		}
		after, _ := s.runtimeRaidForMember(l.selectedCharacterID)
		if after.Started || after.Battle != nil {
			t.Fatal("start installed battle before membership resolved")
		}
	})
}

func TestRealPVFCurrentRaidPresenceLastSeatPreservesAcceptedJournal(t *testing.T) {
	f, session, _, at := onlineAntonTerminalFixture(t)
	s := f.service
	s.options.currentClientVersion = "official-24.9"
	q := newFakeCurrentDungeonDeathTimerQueue()
	q.now = at
	s.gameplayTimers = q
	// The existing actual-final fixture uses a legacy directory layout.
	// Normalize only the directory flag and exercise existing removals so
	// the final retained seat is current while its accepted battle is untouched.
	f.state.CurrentLayout = true
	s.raidMu.Lock()
	s.removeRuntimeRaidMemberLocked(20)
	s.removeRuntimeRaidMemberLocked(21)
	s.raidMu.Unlock()
	before := cloneRuntimeRaidState(*f.state)
	if before.Battle.Configuration.DisconnectWaitSeconds != 300 || !before.Battle.PhaseCompleted {
		t.Fatal("not actual completed fixture")
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		t.Fatal("not owned fixture")
	}
	// Observe physical in-memory assets, not only displayed counters.
	beforeAssets := map[string]any{}
	for _, id := range []int{19, 20, 21} {
		key := strconv.Itoa(id)
		c, _, err := f.repos.Character.Load(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := f.repos.Inventory.Load(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		beforeAssets[key+"c"] = c
		beforeAssets[key+"i"] = v
	}
	s.shutdownGameSessionEvents(session, false)
	seat, ok := s.currentRaidDisconnects[identity.character]
	if !ok {
		t.Fatal("completed seat not reserved")
	}
	s.expireCurrentRaidDisconnectSeats(seat.ExpiresAt)
	if _, ok := s.runtimeRaidForMember(identity.character); ok {
		t.Fatal("expired empty directory retained member")
	}
	after := s.raids[before.RaidKey]
	for _, id := range []int{19, 20, 21} {
		key := strconv.Itoa(id)
		c, _, err := f.repos.Character.Load(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := f.repos.Inventory.Load(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(beforeAssets[key+"c"], c) || !reflect.DeepEqual(beforeAssets[key+"i"], v) {
			t.Fatal("disconnect expiry mutated assets", id)
		}
	}
	if after == nil || len(after.Members) != 0 || after.LeaderID != 0 || !reflect.DeepEqual(before.Battle, cloneRuntimeRaidState(*after).Battle) {
		t.Fatal("expiry discarded or changed frozen completed journal")
	}
}
