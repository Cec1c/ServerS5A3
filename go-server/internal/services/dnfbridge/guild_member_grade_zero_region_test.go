package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"strconv"
	"testing"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func setGuildMemberGradeRegions(t *testing.T, s *Service, repos dnfrepo.Group, regions map[string]byte) {
	t.Helper()
	ctx := context.Background()
	for id, region := range regions {
		character, found, err := repos.Character.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("load character %s: found=%v err=%v", id, found, err)
		}
		character.Stats["server_group_id"] = int64(region)
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatalf("save character %s: %v", id, err)
		}
	}
	registry, err := dnfguild.Load(ctx, repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	for id, region := range regions {
		guild, member, found := registry.GuildForCharacter(id)
		if !found {
			t.Fatalf("character %s is not in a guild", id)
		}
		member.ServerGroup = region
		guild.Members[id] = member
	}
	if err := dnfguild.Save(ctx, repos.Settings, registry, s.gameplayNow()); err != nil {
		t.Fatal(err)
	}
}

func TestGuildMemberGradeAcceptsRegionZeroAndBoundaries(t *testing.T) {
	for _, region := range []byte{0, 1, 249} {
		region := region
		for _, grade := range []byte{2, 3, 4} {
			grade := grade
			t.Run(strconv.Itoa(int(region))+"/"+strconv.Itoa(int(grade)), func(t *testing.T) {
				s, peers, repos := guildMemberGradeFixture(t)
				setGuildMemberGradeRegions(t, s, repos, map[string]byte{"1": region, "2": region})
				ctx := context.Background()
				beforeCharacter, found, err := repos.Character.Load(ctx, "2")
				if err != nil || !found {
					t.Fatalf("load target before grade: found=%v err=%v", found, err)
				}
				for _, peer := range peers {
					peer.conn.(*bufferConn).write.Reset()
				}

				guildDispatch(t, s, peers[0], 126, guildMemberGradeBody(region, "角色2", grade))

				wantACK := append([]byte{1, 0}, guildMemberGradeBody(region, "角色2", grade)...)
				if got := guildReply(t, peers[0], 126); !bytes.Equal(got, wantACK) {
					t.Fatalf("ACK=%x want=%x", got, wantACK)
				}
				if got := guildReply(t, peers[1], 127); !bytes.Equal(got, []byte{grade}) {
					t.Fatalf("target self grade=%x want=%d", got, grade)
				}
				registry, err := dnfguild.Load(ctx, repos.Settings)
				if err != nil || registry.Guilds[1].Members["2"].Grade != grade || registry.Guilds[1].MasterCharacterID != "1" {
					t.Fatalf("grade did not persist: grade=%d master=%s err=%v", registry.Guilds[1].Members["2"].Grade, registry.Guilds[1].MasterCharacterID, err)
				}
				afterCharacter, found, err := repos.Character.Load(ctx, "2")
				if err != nil || !found || !reflect.DeepEqual(beforeCharacter, afterCharacter) {
					t.Fatalf("grade changed character assets: found=%v err=%v", found, err)
				}
				if peers[2].conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("grade refresh crossed guilds")
				}
			})
		}
	}
}
