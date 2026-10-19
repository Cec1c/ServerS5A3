package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"sync"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildPresenceFixture(t *testing.T) (*Service, []*gameSession, dnfrepo.Group, time.Time) {
	t.Helper()
	s, peers, repos := guildTextFixture(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	r, _ := dnfguild.Load(context.Background(), repos.Settings)
	for _, g := range r.Guilds {
		for id, m := range g.Members {
			m.JoinedAt = now.Add(-30 * 24 * time.Hour)
			m.LastOnlineAt = now.Add(-24 * time.Hour)
			g.Members[id] = m
		}
	}
	if err := dnfguild.Save(context.Background(), repos.Settings, r, now); err != nil {
		t.Fatal(err)
	}
	return s, peers, repos, now
}

func TestGuildPresenceAuxiliaryAndReplacementStayOnline(t *testing.T) {
	for _, mode := range []string{"auxiliary_close", "auxiliary_select", "handover", "replaced_close"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, now := guildPresenceFixture(t)
			old := peers[1]
			old.connID = "guild-old"
			other := &gameSession{conn: &bufferConn{}, connID: "guild-new", accountID: old.accountID, selectedCharacterID: 2, characterGeneration: 99}
			s.registerLiveGameSession(old)
			s.registerLiveGameSession(other)
			switch mode {
			case "auxiliary_close":
				s.unbindGameSession(other)
			case "auxiliary_select":
				s.resetGameSessionForCharacterSelect(other)
			case "handover":
				s.unbindGameSession(old)
			case "replaced_close":
				s.promoteResidentGameSession(other, 2)
				s.unbindGameSession(old)
			}
			if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(now.Add(-24 * time.Hour)) {
				t.Fatal("auxiliary close changed last-seen")
			}
			r, _ := dnfguild.Load(context.Background(), repos.Settings)
			if _, online := s.currentGuildOnlineChannels(r.Guilds[1], peers[0])["2"]; !online {
				t.Fatal("surviving canonical member marked offline")
			}
			canonical, _ := s.onlineGameSession(2)
			s.unbindGameSession(canonical)
			if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(now) {
				t.Fatal("final connection close not recorded")
			}
		})
	}
}

func TestGuildPresenceOwnerFailureAndNoReadWrites(t *testing.T) {
	for _, mode := range []string{"wrong_account", "stale_member_owner", "save_failure", "unjoined", "repeat_bind", "read_only"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, _ := guildPresenceFixture(t)
			ctx := context.Background()
			switch mode {
			case "wrong_account":
				peers[1].accountID = "wrong"
			case "stale_member_owner", "unjoined":
				r, _ := dnfguild.Load(ctx, repos.Settings)
				m := r.Guilds[1].Members["2"]
				m.AccountID = "previous"
				r.Guilds[1].Members["2"] = m
				if mode == "unjoined" {
					delete(r.Guilds[1].Members, "2")
				}
				if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "save_failure":
				broken := repos
				broken.Guild = guildTextFailUoW{repos.Guild}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			character, _, _ := repos.Character.Load(ctx, "2")
			switch mode {
			case "read_only":
				guildDispatch(t, s, peers[0], 140, nil)
			case "repeat_bind":
				s.bindGameSessionCharacter(peers[1], 2)
			default:
				s.unbindGameSession(peers[1])
				if _, online := s.onlineGameSession(2); online {
					t.Fatal("failed save prevented disconnect")
				}
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			current, _, _ := repos.Character.Load(ctx, "2")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(character, current) {
				t.Fatal("non-observation changed persistent data")
			}
		})
	}
}

// Parse the callback's fixed row fields independently of the production writer.
func guildPresenceWireRow(t *testing.T, body []byte, target uint32) (byte, uint32) {
	t.Helper()
	at := 5 // dispatcher success and guild id
	_, at, ok := readCurrentRawDstr(body, at)
	if !ok {
		t.Fatal("guild DSTR")
	}
	at += 2
	count := int(binary.LittleEndian.Uint16(body[at:]))
	at += 2
	for i := 0; i < count; i++ {
		id := binary.LittleEndian.Uint32(body[at:])
		at += 4
		for j := 0; j < 2; j++ {
			_, at, ok = readCurrentRawDstr(body, at)
			if !ok {
				t.Fatal("member DSTR")
			}
		}
		channel := body[at+6]
		elapsed := binary.LittleEndian.Uint32(body[at+10:])
		at += 19
		_, at, ok = readCurrentRawDstr(body, at)
		if !ok {
			t.Fatal("auxiliary DSTR")
		}
		at += 4
		if id == target {
			return channel, elapsed
		}
	}
	t.Fatal("target missing from roster")
	return 0, 0
}

func TestGuildPresenceRosterRefreshAndRestart(t *testing.T) {
	s, peers, repos, now := guildPresenceFixture(t)
	peers[1].channel.ID = 12
	guildDispatch(t, s, peers[0], 140, nil)
	if channel, elapsed := guildPresenceWireRow(t, guildReply(t, peers[0], 140), 2); channel != 12 || elapsed != 0 {
		t.Fatalf("online channel=%d elapsed=%d", channel, elapsed)
	}
	for _, peer := range peers {
		peer.conn.(*bufferConn).write.Reset()
	}
	s.unbindGameSession(peers[1])
	if channel, elapsed := guildPresenceWireRow(t, guildReply(t, peers[0], 140), 2); channel != 255 || elapsed != 0 {
		t.Fatalf("offline channel=%d elapsed=%d", channel, elapsed)
	}
	if peers[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("presence crossed guilds")
	}
	fresh := &Service{options: s.options, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }, gameSessions: map[uint16]*gameSession{1: peers[0]}, gameplayTimers: &processGameplayTimeQueue{now: func() time.Time { return now.Add(2 * time.Hour) }}}
	peers[0].conn.(*bufferConn).write.Reset()
	guildDispatch(t, fresh, peers[0], 140, nil)
	if channel, elapsed := guildPresenceWireRow(t, guildReply(t, peers[0], 140), 2); channel != 255 || elapsed != 7200 {
		t.Fatalf("restart channel=%d elapsed=%d", channel, elapsed)
	}
}

func TestGuildPresenceDelayedObservationsAndOverflow(t *testing.T) {
	s, peers, repos, now := guildPresenceFixture(t)
	// Keep peer writes out of the concurrent persistence test.
	s.gameSessions = map[uint16]*gameSession{}
	var wg sync.WaitGroup
	for _, delay := range []time.Duration{0, -time.Hour, -2 * time.Hour, -time.Minute} {
		wg.Add(1)
		go func(delay time.Duration) {
			defer wg.Done()
			s.persistCurrentGuildPresence(currentGuildPresenceObservation{owner: dnfrepo.GuildOwner{CharacterID: "2", AccountID: "a2"}, at: now.Add(delay)})
		}(delay)
	}
	wg.Wait()
	if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(now) {
		t.Fatal("late observation regressed time")
	}
	r, _ := dnfguild.Load(context.Background(), repos.Settings)
	m := r.Guilds[1].Members["2"]
	m.CharacterID = "65538"
	r.Guilds[1].Members[m.CharacterID] = m
	s.gameSessions[2] = peers[1]
	if _, ok := s.currentGuildOnlineChannels(r.Guilds[1], peers[0])["65538"]; ok {
		t.Fatal("overflow member aliased live character 2")
	}
}

func guildPresenceLastSeen(t *testing.T, repos dnfrepo.Group, id string) time.Time {
	t.Helper()
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	_, m, found := r.GuildForCharacter(id)
	if !found {
		t.Fatal("member missing")
	}
	return m.LastOnlineAt
}

func TestGuildPresenceLifecycleLastSeen(t *testing.T) {
	for _, mode := range []string{"disconnect", "return_select", "direct_switch", "login"} {
		t.Run(mode, func(t *testing.T) {
			s, peers, repos, now := guildPresenceFixture(t)
			switch mode {
			case "disconnect":
				s.unbindGameSession(peers[1])
			case "return_select":
				s.resetGameSessionForCharacterSelect(peers[1])
			case "direct_switch":
				s.bindGameSessionCharacter(peers[1], 3)
			case "login":
				delete(s.gameSessions, 2)
				peers[1].selectedCharacterID = 0
				s.bindGameSessionCharacter(peers[1], 2)
			}
			if got := guildPresenceLastSeen(t, repos, "2"); !got.Equal(now) {
				t.Fatalf("%s last seen=%s want=%s", mode, got, now)
			}
			if got := guildPresenceLastSeen(t, repos, "3"); !got.Equal(now.Add(-24 * time.Hour)) {
				t.Fatal("changed another account's member")
			}
		})
	}
}
