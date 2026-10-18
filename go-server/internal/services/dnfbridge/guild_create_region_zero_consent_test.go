package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/platform/config"
)

func TestGuildRegionZeroCreationRequiresAcceptedFounder(t *testing.T) {
	for _, policy := range []string{"", "false"} {
		t.Run("solo="+policy, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_GUILD_SOLO_CREATE", policy)
			s, peers, repos := guildFoundationFixture(t)
			s.options.guildSoloCreate = optionsFromConfig(config.ServiceConfig{}).guildSoloCreate
			s.options.channelAdvertiseID = 0
			ctx := context.Background()
			assertNotCreated := func() {
				t.Helper()
				guildDispatch(t, s, peers[0], 68, []byte{1})
				if reply := guildReply(t, peers[0], 68); len(reply) == 0 || reply[0] != 0 {
					t.Fatalf("creation without accepted founder succeeded: %x", reply)
				}
				registry, err := dnfguild.Load(ctx, repos.Settings)
				if err != nil || len(registry.Guilds) != 0 || len(registry.Creations) != 0 {
					t.Fatalf("unapproved creation persisted: %+v err=%v", registry, err)
				}
				for _, id := range []string{"1", "2"} {
					character, found, err := repos.Character.Load(ctx, id)
					if err != nil || !found || character.Stats["gold"] != 900000 {
						t.Fatalf("unapproved creation charged participant %s: %+v err=%v", id, character, err)
					}
				}
			}
			guildDispatch(t, s, peers[0], 156, guildWireString("双人认证"))
			guildDispatch(t, s, peers[0], 739, guildWireString("共同冒险"))
			assertNotCreated()
			guildDispatch(t, s, peers[0], 741, append([]byte{0}, guildWireString("角色2")...))
			if reply := guildReply(t, peers[0], 741); !bytes.Equal(reply, []byte{1}) {
				t.Fatalf("region zero invitation rejected: %x", reply)
			}
			if reply := guildReply(t, peers[1], 685); !bytes.Contains(reply, rosterNameBytes("双人认证")) {
				t.Fatalf("founder invitation missing: %x", reply)
			}
			assertNotCreated()
			guildDispatch(t, s, peers[1], 742, []byte{1})
			if reply := guildReply(t, peers[0], 686); !bytes.Equal(reply, []byte{1}) {
				t.Fatalf("accepted founder did not enable creation: %x", reply)
			}
			for attempt := 0; attempt < 3; attempt++ {
				guildDispatch(t, s, peers[0], 68, []byte{1})
				if reply := guildReply(t, peers[0], 68); !bytes.Equal(reply, []byte{1}) {
					t.Fatalf("accepted creation or replay failed: %x", reply)
				}
			}
			registry, err := dnfguild.Load(ctx, repos.Settings)
			if err != nil || len(registry.Guilds) != 1 || len(registry.Creations) != 1 || len(registry.Guilds[1].Members) != 2 {
				t.Fatalf("expected two members and one receipt: %+v err=%v", registry, err)
			}
			creator, _, _ := repos.Character.Load(ctx, "1")
			founder, _, _ := repos.Character.Load(ctx, "2")
			if creator.Stats["gold"] != 600000 || founder.Stats["gold"] != 900000 {
				t.Fatalf("expected one creator payment: creator=%d founder=%d", creator.Stats["gold"], founder.Stats["gold"])
			}
		})
	}
}
