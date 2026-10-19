package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func guildInvitationFixture(t *testing.T) (*Service, []*gameSession, dnfrepo.Group, *fakeCurrentDungeonDeathTimerQueue) {
	t.Helper()
	s, peers, repos := guildFoundationFixture(t)
	q := newFakeCurrentDungeonDeathTimerQueue()
	s.gameplayTimers = q
	r := dnfguild.NewRegistry()
	ctx := context.Background()
	master, _, _ := repos.Character.Load(ctx, "1")
	g, err := r.Create(master, "测试公会", q.Now())
	if err != nil {
		t.Fatal(err)
	}
	g.Promotion, g.Tags = "共同冒险", []byte{1, 3}
	if err := dnfguild.Save(ctx, repos.Settings, r, q.Now()); err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		p.channel.ID = 10
	}
	return s, peers, repos, q
}

func guildInvitationOffer(t *testing.T, s *Service, p []*gameSession) {
	t.Helper()
	guildDispatch(t, s, p[0], 151, append([]byte{byte(s.channelAdvertiseID())}, guildWireString("角色2")...))
	if got := guildReply(t, p[0], 151); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("offer=%x", got)
	}
}

func TestGuildInvitationRejectAndExpiry(t *testing.T) {
	for _, mode := range []string{"decline", "timer", "deadline", "stale_timer", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, q := guildInvitationFixture(t)
			before, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
			guildInvitationOffer(t, s, p)
			task := q.task(t, 0)
			if task.delay != 15*time.Second {
				t.Fatal("wrong native dialog lifetime")
			}
			p[1].conn.(*bufferConn).write.Reset()
			switch mode {
			case "timer":
				q.fire(task, false)
				guildDispatch(t, s, p[0], 151, append([]byte{1}, guildWireString("角色2")...))
				if !bytes.Equal(guildReply(t, p[0], 151), []byte{0, 23}) {
					t.Fatal("replaced undrained expired dialog")
				}
				guildDispatch(t, s, p[1], 152, []byte{1})
				if !bytes.Equal(guildReply(t, p[1], 152), []byte{0, 103}) {
					t.Fatal("expired invite accepted")
				}
			case "deadline":
				q.mu.Lock()
				q.now = q.now.Add(15 * time.Second)
				q.mu.Unlock()
				guildDispatch(t, s, p[1], 152, []byte{1})
				if !bytes.Equal(guildReply(t, p[1], 152), []byte{0, 103}) {
					t.Fatal("deadline accepted before timer callback")
				}
			case "malformed":
				guildDispatch(t, s, p[1], 152, []byte{2})
				if p[1].guild.incomingInvitation == nil {
					t.Fatal("malformed reply consumed valid invitation")
				}
				p[1].conn.(*bufferConn).write.Reset()
				guildDispatch(t, s, p[1], 152, []byte{0})
			default:
				guildDispatch(t, s, p[1], 152, []byte{0})
				if p[1].conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("decline falsely acknowledged joining")
				}
			}
			if got := guildReply(t, p[0], 148); !bytes.Equal(got, append([]byte{0}, guildWireString("角色2")...)) {
				t.Fatalf("decline=%x", got)
			}
			if p[1].guild.incomingInvitation != nil || len(p[0].guild.outgoingInvitations) != 0 {
				t.Fatal("pending consent leaked")
			}
			if mode == "stale_timer" {
				guildInvitationOffer(t, s, p)
				fresh := p[1].guild.incomingInvitation
				q.fire(task, true)
				if p[1].guild.incomingInvitation != fresh || fresh.expired {
					t.Fatal("old timer cancelled replacement")
				}
				guildDispatch(t, s, p[1], 152, []byte{0})
			}
			after, _, _ := repos.Settings.Load(context.Background(), dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejection mutated registry")
			}
		})
	}
}

func TestGuildInvitationRevalidatesAtAcceptance(t *testing.T) {
	for _, mode := range []string{"revoked", "full", "joined_elsewhere", "wrong_inviter_owner", "wrong_target_owner", "target_renamed", "different_channel", "save_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, _ := guildInvitationFixture(t)
			ctx := context.Background()
			guildInvitationOffer(t, s, p)
			r, _ := dnfguild.Load(ctx, repos.Settings)
			want := byte(23)
			switch mode {
			case "revoked":
				want = 36
				r.Guilds[1].GradePermissions = map[byte]uint32{1: 0}
			case "full":
				r.Guilds[1].MemberLimit = 1
				want = 38
			case "joined_elsewhere":
				c, _, _ := repos.Character.Load(ctx, "2")
				if _, err := r.Create(c, "另外公会", s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				want = 39
			case "wrong_inviter_owner":
				p[0].accountID = "intruder"
			case "wrong_target_owner":
				want = 34
				c, _, _ := repos.Character.Load(ctx, "2")
				c.AccountID = "replacement"
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			case "target_renamed":
				c, _, _ := repos.Character.Load(ctx, "2")
				c.Name = "新名字"
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			case "different_channel":
				p[1].channel.ID = 11
				want = 3
			case "save_failure":
				broken := repos
				broken.Guild = guildTextFailUoW{repos.Guild}
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			guildDispatch(t, s, p[1], 152, []byte{1})
			if got := guildReply(t, p[1], 152); !bytes.Equal(got, []byte{0, want}) {
				t.Fatalf("reply=%x want=%d", got, want)
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed acceptance mutated registry")
			}
		})
	}
}

func TestGuildInvitationOfferGuards(t *testing.T) {
	for _, mode := range []string{"wrong_region", "different_channel", "offline", "nonmember", "no_permission", "full", "already_member", "target_busy", "creation_busy", "schedule_failure", "invalid_name", "class_zero", "grant_bit1", "advertise_zero"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, q := guildInvitationFixture(t)
			ctx := context.Background()
			r, _ := dnfguild.Load(ctx, repos.Settings)
			actor := p[0]
			body := append([]byte{1}, guildWireString("角色2")...)
			want := byte(23)
			switch mode {
			case "wrong_region":
				body[0] = 0
				want = 3
			case "different_channel":
				p[1].channel.ID = 11
				want = 3
			case "offline":
				delete(s.gameSessions, 2)
				want = 3
			case "nonmember":
				actor = p[2]
				want = 34
			case "no_permission":
				r.Guilds[1].GradePermissions = map[byte]uint32{1: 0}
				want = 36
			case "full":
				r.Guilds[1].MemberLimit = 1
				want = 38
			case "already_member":
				c, _, _ := repos.Character.Load(ctx, "2")
				_, _ = r.JoinDirect(1, c, q.Now())
				want = 39
			case "target_busy":
				guildInvitationOffer(t, s, p)
			case "creation_busy":
				p[1].guild.createInvitation = &currentGuildCreateInvitation{creator: boundGameSessionCharacter{session: p[2]}, target: boundGameSessionCharacter{session: p[1]}}
			case "schedule_failure":
				q.scheduleErr = errors.New("scheduler unavailable")
			case "invalid_name":
				body = append([]byte{1}, guildWireString("不存在")...)
			case "grant_bit1":
				c, _, _ := repos.Character.Load(ctx, "3")
				_, _ = r.JoinDirect(1, c, q.Now())
				r.Guilds[1].GradePermissions = map[byte]uint32{4: 1 << 1}
				actor = p[2]
				want = 0
			case "advertise_zero":
				s.options.channelAdvertiseID = 0
				body[0] = 0
				want = 0
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, q.Now()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if mode == "class_zero" {
				if handled, err := currentGameplayModules.DispatchUpper(s, actor, 151, 0, body); !handled || err != nil {
					t.Fatal(err)
				}
				if actor.conn.(*bufferConn).write.Len() != 0 || p[1].guild.incomingInvitation != nil {
					t.Fatal("wrong class opened dialog")
				}
			} else {
				guildDispatch(t, s, actor, 151, body)
				expected := []byte{0, want}
				if want == 0 {
					expected = []byte{1}
				}
				if got := guildReply(t, actor, 151); !bytes.Equal(got, expected) {
					t.Fatalf("reply=%x want=%x", got, expected)
				}
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("offer mutated persistent state")
			}
		})
	}
}

func TestGuildInvitationDisconnectSelectionAndGeneration(t *testing.T) {
	for _, mode := range []string{"inviter_close", "target_close", "inviter_select", "target_select", "inviter_generation", "target_generation"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, q := guildInvitationFixture(t)
			guildInvitationOffer(t, s, p)
			old := q.task(t, 0)
			switch mode {
			case "inviter_close":
				s.unbindGameSession(p[0])
			case "target_close":
				s.unbindGameSession(p[1])
			case "inviter_select":
				s.resetGameSessionForCharacterSelect(p[0])
			case "target_select":
				s.resetGameSessionForCharacterSelect(p[1])
			case "inviter_generation":
				s.mu.Lock()
				p[0].characterGeneration++
				s.mu.Unlock()
			case "target_generation":
				s.mu.Lock()
				p[1].characterGeneration++
				s.mu.Unlock()
			}
			if err := s.handleCurrentGuildInvitationReply(p[1], []byte{1}); err != nil {
				t.Fatal(err)
			}
			q.fire(old, true)
			r, err := dnfguild.Load(context.Background(), repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, found := r.GuildForCharacter("2"); found {
				t.Fatal("retired consent joined guild")
			}
			if p[1].guild.incomingInvitation != nil || len(p[0].guild.outgoingInvitations) != 0 {
				t.Fatal("retired consent leaked")
			}
		})
	}
}

func TestGuildInvitationDuplicateAcceptanceAndMultipleTargets(t *testing.T) {
	s, p, repos, _ := guildInvitationFixture(t)
	guildInvitationOffer(t, s, p)
	guildDispatch(t, s, p[0], 151, append([]byte{1}, guildWireString("角色3")...))
	if len(p[0].guild.outgoingInvitations) != 2 {
		t.Fatal("unnecessary one-outgoing invitation cap")
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { errs[i] = s.handleCurrentGuildInvitationReply(p[1], []byte{1}) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	guildDispatch(t, s, p[2], 152, []byte{0})
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Guilds[1].Members) != 2 || len(p[0].guild.outgoingInvitations) != 0 {
		t.Fatal("duplicate acceptance or independent refusal broke membership")
	}
}

func TestGuildInvitationCreationConsentConflict(t *testing.T) {
	for _, mode := range []string{"solo", "creator", "founder"} {
		t.Run(mode, func(t *testing.T) {
			s, p, repos, _ := guildInvitationFixture(t)
			guildInvitationOffer(t, s, p)
			creator, target := p[1], "角色3"
			if mode == "founder" {
				creator, target = p[2], "角色2"
			}
			guildDispatch(t, s, creator, 156, guildWireString("另建公会"))
			guildDispatch(t, s, creator, 739, guildWireString("新公会介绍"))
			if mode == "solo" {
				s.options.guildSoloCreate = true
				guildDispatch(t, s, creator, 156, guildWireString("另建公会"))
				guildDispatch(t, s, creator, 68, []byte{1})
				if guildReply(t, creator, 68)[0] != 0 {
					t.Fatal("created while ordinary consent pending")
				}
			} else {
				guildDispatch(t, s, creator, 741, append([]byte{1}, guildWireString(target)...))
				if guildReply(t, creator, 741)[0] != 0 {
					t.Fatal("creation consent overlapped ordinary consent")
				}
			}
			if p[1].guild.incomingInvitation == nil {
				t.Fatal("creation check destroyed ordinary invitation")
			}
			r, err := dnfguild.Load(context.Background(), repos.Settings)
			if err != nil || len(r.Guilds) != 1 {
				t.Fatal("creation bypassed pending consent")
			}
			c, _, _ := repos.Character.Load(context.Background(), "2")
			if c.Stats["gold"] != 900000 {
				t.Fatal("rejected creation charged gold")
			}
		})
	}
}

func TestGuildInvitationAcceptanceReservesBothGenerations(t *testing.T) {
	s, p, repos, _ := guildInvitationFixture(t)
	guildInvitationOffer(t, s, p)
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	broken := repos
	broken.Guild = guildMemberGradeHookUoW{base: repos.Guild, hook: func(dnfrepo.Group) error { once.Do(func() { close(entered); <-proceed }); return nil }}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
	done := make(chan error, 1)
	go func() { done <- s.handleCurrentGuildInvitationReply(p[1], []byte{1}) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("join did not reach UoW")
	}
	s.mu.Lock()
	both := s.characterAssetReservations[1] != nil && s.characterAssetReservations[2] != nil
	s.mu.Unlock()
	if !both {
		close(proceed)
		t.Fatal("commit did not reserve both participants")
	}
	left := make(chan struct{})
	go func() { s.resetGameSessionForCharacterSelect(p[1]); close(left) }()
	close(proceed)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accept deadlocked")
	}
	select {
	case <-left:
	case <-time.After(3 * time.Second):
		t.Fatal("return-select deadlocked")
	}
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found := r.GuildForCharacter("2"); !found {
		t.Fatal("committed join lost during selection")
	}
	if p[1].guild.incomingInvitation != nil {
		t.Fatal("selection retained consent")
	}
}

func TestGuildInvitationDeadlineInsideTransaction(t *testing.T) {
	s, p, repos, q := guildInvitationFixture(t)
	guildInvitationOffer(t, s, p)
	broken := repos
	broken.Guild = guildMemberGradeHookUoW{base: repos.Guild, hook: func(dnfrepo.Group) error { q.mu.Lock(); q.now = q.now.Add(15 * time.Second); q.mu.Unlock(); return nil }}
	s.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
	guildDispatch(t, s, p[1], 152, []byte{1})
	if got := guildReply(t, p[1], 152); !bytes.Equal(got, []byte{0, 103}) {
		t.Fatalf("late commit=%x", got)
	}
	r, _ := dnfguild.Load(context.Background(), repos.Settings)
	if _, _, found := r.GuildForCharacter("2"); found {
		t.Fatal("SQL lock wait bypassed expiry")
	}
}

func TestGuildInvitationNativeAccept(t *testing.T) {
	s, p, repos, _ := guildInvitationFixture(t)
	guildDispatch(t, s, p[0], 151, append([]byte{1}, guildWireString("角色2")...))
	if got := guildReply(t, p[0], 151); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("invite ACK=%x", got)
	}
	if got := guildReply(t, p[1], 147); !bytes.Contains(got, rosterNameBytes("测试公会")) {
		t.Fatalf("popup=%x", got)
	}
	guildDispatch(t, s, p[1], 152, []byte{1})
	if got := guildReply(t, p[1], 152); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("join ACK=%x", got)
	}
	if got := guildReply(t, p[0], 148); !bytes.Equal(got, append([]byte{1}, guildWireString("角色2")...)) {
		t.Fatalf("result=%x", got)
	}
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if g, m, found := r.GuildForOwner(dnfrepo.GuildOwner{CharacterID: "2", AccountID: "a2"}); !found || g.ID != 1 || m.Grade != 4 {
		t.Fatal("accepted member not saved")
	}
	if p[2].conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("invitation leaked to unrelated player")
	}
}

func TestGuildInvitationUnsolicitedAccept(t *testing.T) {
	s, p, repos, _ := guildInvitationFixture(t)
	guildDispatch(t, s, p[1], 152, []byte{1})
	if got := guildReply(t, p[1], 152); !bytes.Equal(got, []byte{0, 103}) {
		t.Fatalf("unsolicited reply=%x", got)
	}
	r, err := dnfguild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found := r.GuildForCharacter("2"); found {
		t.Fatal("joined without invitation")
	}
}
