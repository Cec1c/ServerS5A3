package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
)

// Paired EXE 2af985b6: mode0's native local-actor branch compares the
// advertised server/channel with the two row-prefix bytes. The captured
// regression announced server 1 but sent actor rows with server 0/channel 10.
func TestAdvertisedServerOwnsNativeActorBootstrap(t *testing.T) {
	for _, serverID := range []int{0, 1, 249} {
		t.Run(fmt.Sprint(serverID), func(t *testing.T) {
			s, session, repos := newTownMoveTest(t)
			s.options.channelAdvertiseID = serverID
			session.selectedCharacterID = 29
			ctx := context.Background()
			id, name, character, found := s.selectedCharacterForEnter(ctx, session)
			if !found {
				t.Fatal("missing fixture character")
			}
			// Runtime scene identity must not be taken from persistent social identity.
			character.Stats["server_group_id"] = 0
			reader := csharpLegacyUserInfoReader{ctx: ctx, service: s, session: session, characterID: "29"}
			for _, channel := range []byte{0, 10, 11} {
				mode0, err := s.buildCurrentSceneObjectListBodyForSessionInContextStrict(ctx, session, id, name, character, true, channel)
				if err != nil {
					t.Fatal(err)
				}
				bodies := map[string][]byte{
					"appearance": mode0,
					"binding":    reader.buildCurrentUserInfoMode1WithEquipmentInContext(character, true, id, false, 0, channel),
					"state":      reader.buildCurrentUserInfoMode3InContext(character, true, id, 0, channel),
				}
				for kind, body := range bodies {
					if len(body) < 7 || binary.LittleEndian.Uint16(body[1:3]) != 1 {
						t.Fatalf("%s malformed actor row", kind)
					}
					if body[3] != byte(serverID) || body[4] != channel {
						t.Errorf("%s native actor identity=%d/%d, announced=%d/%d; client takes foreign-actor path", kind, body[3], body[4], serverID, channel)
					}
				}
				frames := map[string][]byte{
					"display": s.buildCurrentSceneOp9ActorDisplayBodyInContext(id, character, true, name, channel),
					"remove":  buildCurrentSceneOp9ActorRemovalBodyInContext(id, channel, s.currentSceneActorRoute()),
				}
				for kind, body := range frames {
					if binary.LittleEndian.Uint16(body[4:6]) != id || body[6] != byte(serverID) || body[7] != channel {
						t.Errorf("%s scene frame does not address the same native actor: %x", kind, body[:10])
					}
				}
				// The nested kind0 flags are not the actor's server/channel key.
				if frames["display"][10] != 0 || frames["display"][11] != 0 {
					t.Fatal("rewrote kind0 discriminator")
				}
			}
			stored, ok, err := repos.Character.Load(ctx, "29")
			if err != nil || !ok {
				t.Fatalf("reload: found=%t err=%v", ok, err)
			}
			if stored.Stats["server_group_id"] != 0 {
				t.Fatal("scene projection changed durable social identity")
			}
		})
	}
}

func TestSceneActorServerRejectsUnrepresentableAnnouncementBeforeListening(t *testing.T) {
	for _, id := range []int{250, 255, 256, 65536} {
		s := &Service{options: options{channelAdvertiseID: id}}
		if err := s.Start(context.Background()); err == nil {
			t.Fatalf("accepted invalid server %d", id)
		}
	}
}
