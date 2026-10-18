package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

// The paired client's NOTI147 blacklist branch must emit op152/decision0.
// Local x86 emulation separately verifies that the EXE now emits that payload;
// this test covers its effect on the server's pending consent and next guild.
func TestGuildInvitationBlacklistDeclineAllowsNextGuild(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "immediate"
		if late {
			name = "after_server_timeout"
		}
		t.Run(name, func(t *testing.T) {
			s, p, repos, q := guildInvitationFixture(t)
			ctx := context.Background()
			r, err := dnfguild.Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			other, _, _ := repos.Character.Load(ctx, "3")
			g, err := r.Create(other, "另一个公会", q.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := dnfguild.Save(ctx, repos.Settings, r, q.Now()); err != nil {
				t.Fatal(err)
			}
			guildInvitationOffer(t, s, p)
			old := q.task(t, 0)
			if late {
				q.fire(old, false)
			}
			p[1].conn.(*bufferConn).write.Reset()
			// Blacklisted inviter: no dialog, but the client sends a refusal.
			guildDispatch(t, s, p[1], 152, []byte{0})
			if p[1].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("automatic refusal displayed a joined/error ACK")
			}
			if p[1].guild.incomingInvitation != nil {
				t.Fatal("blacklisted invitation still blocks future invitations")
			}
			if got := guildReply(t, p[0], 148); !bytes.Equal(got, append([]byte{0}, guildWireString("角色2")...)) {
				t.Fatalf("refusal=%x", got)
			}
			guildDispatch(t, s, p[2], 151, append([]byte{1}, guildWireString("角色2")...))
			if got := guildReply(t, p[2], 151); !bytes.Equal(got, []byte{1}) {
				t.Fatalf("next invitation=%x", got)
			}
			fresh := p[1].guild.incomingInvitation
			q.fire(old, true)
			if p[1].guild.incomingInvitation != fresh || fresh.expired {
				t.Fatal("old rejected invitation expired the new guild's dialog")
			}
			guildDispatch(t, s, p[1], 152, []byte{1})
			r, err = dnfguild.Load(ctx, repos.Settings)
			if err != nil {
				t.Fatal(err)
			}
			joined, _, found := r.GuildForCharacter("2")
			if !found || joined.ID != g.ID || len(r.Guilds[1].Members) != 1 || len(joined.Members) != 2 {
				t.Fatal("refusal/next acceptance joined the wrong guild")
			}
		})
	}
}
