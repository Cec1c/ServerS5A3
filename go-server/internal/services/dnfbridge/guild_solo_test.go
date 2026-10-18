package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildSoloNativeNameCreateAndReplay(t *testing.T) {
	s, p, r := guildFoundationFixture(t)
	s.options.guildSoloCreate = true
	guildDispatch(t, s, p[0], 68, []byte{1})
	if guildReply(t, p[0], 68)[0] != 0 {
		t.Fatal("created before name check")
	}
	guildDispatch(t, s, p[0], 156, guildWireString("单人公会"))
	if !bytes.Equal(guildReply(t, p[0], 156), []byte{1}) || !bytes.Equal(guildReply(t, p[0], 686), []byte{1}) {
		t.Fatal("native create button not enabled")
	}
	for i := 0; i < 3; i++ {
		guildDispatch(t, s, p[0], 68, []byte{1})
		if !bytes.Equal(guildReply(t, p[0], 68), []byte{1}) {
			t.Fatal("solo create/replay failed")
		}
	}
	ctx := context.Background()
	c, _, _ := r.Character.Load(ctx, "1")
	reg, err := dnfguild.Load(ctx, r.Settings)
	if err != nil || c.Stats["gold"] != 600000 || len(reg.Guilds) != 1 || len(reg.Guilds[1].Members) != 1 {
		t.Fatalf("gold=%d registry=%+v err=%v", c.Stats["gold"], reg, err)
	}
	if p[1].conn.(*bufferConn).write.Len() != 0 || p[0].guild.createInvitation != nil {
		t.Fatal("solo created an invitation")
	}
	fresh := &Service{options: s.options, repositoryProvider: s.repositoryProvider, gameSessions: s.gameSessions}
	if err := fresh.sendCurrentGuildAccountProjection(ctx, p[0]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(guildReply(t, p[0], 1451), rosterNameBytes("单人公会")) {
		t.Fatal("reloaded guild missing")
	}
}

func TestGuildSoloCancelAndInvalidNameRevokeCreate(t *testing.T) {
	for _, op := range []uint16{156, 743} {
		s, p, r := guildFoundationFixture(t)
		s.options.guildSoloCreate = true
		guildDispatch(t, s, p[0], 156, guildWireString("单人公会"))
		guildDispatch(t, s, p[0], op, nil)
		guildDispatch(t, s, p[0], 68, []byte{1})
		c, _, _ := r.Character.Load(context.Background(), "1")
		if guildReply(t, p[0], 68)[0] != 0 || c.Stats["gold"] != 900000 {
			t.Fatal("revoked name still created guild")
		}
	}
}
