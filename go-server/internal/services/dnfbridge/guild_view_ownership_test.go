package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildViewsDoNotInheritAnotherAccountsMembership(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	p := peers[29]
	ctx := context.Background()
	r, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	g, member, _ := r.GuildForCharacter("29")
	member.AccountID = "former-owner"
	g.Members["29"] = member
	g.Notice = "private guild notice"
	if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
	for _, op := range []uint16{67, 139, 140, 141, 1451, 744, 1452, 1453, 74, 365, 352} {
		t.Run(strconv.Itoa(int(op)), func(t *testing.T) {
			p.conn.(*bufferConn).write.Reset()
			guildDispatch(t, s, p, op, nil)
			body := guildReply(t, p, op)
			switch op {
			case 352:
				if len(body) < 2 || body[0] != 0 {
					t.Fatalf("former owner's application list accepted: %x", body)
				}
			case 365:
				if len(body) < 4 || binary.LittleEndian.Uint32(body) != 0 {
					t.Fatalf("former owner's application notice: %x", body)
				}
			case 1451, 744:
				if len(body) < 2 || body[0] != 1 || body[1] != 0 {
					t.Fatalf("former owner's account guild index: %x", body)
				}
			case 74:
				if len(body) < 9 || body[0] != 1 || binary.LittleEndian.Uint32(body[1:5]) != 29 || binary.LittleEndian.Uint32(body[5:9]) != 0 {
					t.Fatalf("former owner's guild name: %x", body)
				}
			default:
				if len(body) < 5 || body[0] != 1 || binary.LittleEndian.Uint32(body[1:5]) != 0 {
					t.Fatalf("former owner's roster/grade list: %x", body)
				}
			}
			if bytes.Contains(p.conn.(*bufferConn).write.Bytes(), rosterNameBytes(g.Name)) || bytes.Contains(p.conn.(*bufferConn).write.Bytes(), []byte(g.Notice)) {
				t.Fatal("private guild projection escaped through a response or refresh")
			}
		})
	}
	t.Run("login", func(t *testing.T) {
		p.conn.(*bufferConn).write.Reset()
		p.initialTownLegacySceneReadyAccepted = true
		if err := s.sendCurrentInitialTownGuildStateProjectionLocked(p, "owner-test"); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(guildReply(t, p, 127), []byte{100}) || !bytes.Equal(guildReply(t, p, 308), []byte{0}) {
			t.Fatal("login inherited former owner's guild grade or membership")
		}
	})
	t.Run("already_loaded_management_snapshot", func(t *testing.T) {
		p.conn.(*bufferConn).write.Reset()
		if err := s.sendCurrentGuildManagementState(p, g); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(guildReply(t, p, 127), []byte{100}) || !bytes.Equal(guildReply(t, p, 308), []byte{0}) {
			t.Fatal("management refresh inherited former owner's membership")
		}
	})
	t.Run("scene_nameplate", func(t *testing.T) {
		projection, err := s.currentSceneGuild(ctx, 29)
		if err != nil || projection.id != 0 || projection.name != "" {
			t.Fatalf("scene inherited former owner's nameplate: %+v err=%v", projection, err)
		}
	})
	after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read projections changed persisted membership")
	}
}

func TestGuildAccountProjectionChecksApplicationOwner(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	p := peers[32]
	ctx := context.Background()
	r, _ := dnfguild.Load(ctx, repos.Settings)
	g, _, _ := r.GuildForCharacter("29")
	c, _, _ := repos.Character.Load(ctx, "32")
	if err := r.Apply(g.ID, c, "申请", s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
	for _, valid := range []bool{true, false} {
		a := g.Applications[c.CharacterID]
		if !valid {
			a.AccountID = "former-owner"
			g.Applications[c.CharacterID] = a
		}
		if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
			t.Fatal(err)
		}
		p.conn.(*bufferConn).write.Reset()
		guildDispatch(t, s, p, 744, nil)
		body := guildReply(t, p, 744)
		want := byte(0)
		if valid {
			want = 1
		}
		if len(body) < 2 || body[0] != 1 || body[1] != want {
			t.Fatalf("valid=%v application projection=%x", valid, body)
		}
		body = guildReply(t, p, 1451)
		if len(body) < 2 || body[0] != 1 || body[1] != want {
			t.Fatalf("valid=%v account guild index=%x", valid, body)
		}
	}
}

func TestGuildViewsKeepMatchingOwnerMembership(t *testing.T) {
	s, peers, repos := guildHallFixture(t)
	p := peers[29]
	r, _ := dnfguild.Load(context.Background(), repos.Settings)
	g, _, _ := r.GuildForCharacter("29")
	for _, op := range []uint16{67, 140, 1452} {
		p.conn.(*bufferConn).write.Reset()
		guildDispatch(t, s, p, op, nil)
		body := guildReply(t, p, op)
		if len(body) < 5 || body[0] != 1 || binary.LittleEndian.Uint32(body[1:5]) != g.ID {
			t.Fatalf("matching owner lost op%d guild membership: %x", op, body)
		}
	}
	members, _, guilds, err := s.guildAccountProjection(context.Background(), p)
	if err != nil || len(members) != 1 || len(guilds) != 1 || members[0].GuildID != g.ID {
		t.Fatalf("matching account projection=%+v guilds=%d err=%v", members, len(guilds), err)
	}
	projection, err := s.currentSceneGuild(context.Background(), 29)
	if err != nil || projection.id != g.ID || projection.name != g.Name {
		t.Fatalf("matching owner nameplate=%+v err=%v", projection, err)
	}
}

func TestGuildMembershipMutationRejectsFormerOwner(t *testing.T) {
	for _, operation := range []string{"approve", "deny", "join_retry", "cancel_application"} {
		t.Run(operation, func(t *testing.T) {
			s, p, repos := guildMileageFixture(t)
			ctx := context.Background()
			r, _ := dnfguild.Load(ctx, repos.Settings)
			g, member, _ := r.GuildForCharacter("29")
			applicant, _, _ := repos.Character.Load(ctx, "32")
			if err := r.Apply(g.ID, applicant, "申请", s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			var body []byte
			var op uint16
			if operation == "cancel_application" {
				a := g.Applications["32"]
				a.AccountID = "former-owner"
				g.Applications["32"] = a
				p = s.gameSessions[32]
				op, body = 349, make([]byte, 4)
				binary.LittleEndian.PutUint32(body, g.ID)
			} else {
				member.AccountID = "former-owner"
				g.Members["29"] = member
				switch operation {
				case "approve":
					op, body = 350, make([]byte, 5)
					binary.LittleEndian.PutUint32(body, 32)
				case "deny":
					op, body = 351, make([]byte, 8)
					binary.LittleEndian.PutUint32(body, 32)
				case "join_retry":
					op, body = 348, guildWireString(g.Name)
				}
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, s.gameplayNow()); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			p.conn.(*bufferConn).write.Reset()
			guildDispatch(t, s, p, op, body)
			reply := guildReply(t, p, op)
			if len(reply) < 2 || reply[0] != 0 {
				t.Errorf("former owner's %s accepted: %x", operation, reply)
			}
			after, _, _ := repos.Settings.Load(ctx, dnfguild.RegistryScope)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("former owner's request changed persisted membership/application")
			}
		})
	}
}
