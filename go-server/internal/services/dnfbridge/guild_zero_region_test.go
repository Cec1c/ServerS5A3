package dnfbridge

import (
	"bytes"
	"context"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"testing"
)

func TestGuildFoundationAdvertisedZeroKeepsConsentAndOneCharge(t *testing.T) {
	s, p, r := guildFoundationFixture(t)
	s.options.channelAdvertiseID = 0
	guildPrepareInvite(t, s, p)
	invitation := guildReply(t, p[1], 685)
	_, next, ok := parseCurrentGuildDSTR(invitation, 0)
	if !ok || next >= len(invitation) || invitation[next] != 0 {
		t.Fatalf("invitation lost advertised region zero: %x", invitation)
	}
	guildDispatch(t, s, p[0], 68, []byte{1})
	if guildReply(t, p[0], 68)[0] != 0 {
		t.Fatal("creation accepted without founder consent")
	}
	c, _, err := r.Character.Load(context.Background(), "1")
	if err != nil || c.Stats["gold"] != 900000 {
		t.Fatal("unaccepted creation charged gold", err)
	}
	guildDispatch(t, s, p[1], 742, []byte{1})
	if !bytes.Equal(guildReply(t, p[0], 686), []byte{1}) {
		t.Fatal("consent did not authorize create")
	}
	for i := 0; i < 3; i++ {
		guildDispatch(t, s, p[0], 68, []byte{1})
		if !bytes.Equal(guildReply(t, p[0], 68), []byte{1}) {
			t.Fatal("create/replay failed")
		}
	}
	c, _, err = r.Character.Load(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := dnfguild.Load(context.Background(), r.Settings)
	if err != nil || c.Stats["gold"] != 600000 || len(registry.Guilds) != 1 || len(registry.Guilds[1].Members) != 2 {
		t.Fatalf("gold=%d registry=%+v err=%v", c.Stats["gold"], registry, err)
	}
}
