package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/guild"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func dispatchGuildWarMissionTest(t *testing.T, s *Service, p *gameSession, legacy bool, class byte, body []byte) {
	t.Helper()
	var err error
	if legacy {
		err = s.handleLegacyGamePacket(p, buildLegacyGamePacketForBridgeTest(class, uint16(dnfenum.CmdPacketAgitWarInfo), 0, body))
	} else {
		var raw []byte
		raw, err = dnfproto.BuildChannelPacket(uint16(dnfenum.CmdPacketAgitWarInfo), body, 1, class)
		if err == nil {
			raw[0] = class // BuildChannelPacket defaults zero to command class one.
			err = s.handleGameUpper(p, raw)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestGuildWarMissionsNativeQueryAndRepeatedRefresh(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy", false: "upper"}[legacy], func(t *testing.T) {
			s, peers, repos := guildTextFixture(t)
			s.options.guildEssenceEnabled = true
			p := peers[0]
			before, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
			for i := 0; i < 2; i++ {
				p.conn.(*bufferConn).write.Reset()
				dispatchGuildWarMissionTest(t, s, p, legacy, 1, []byte{2, 0, 0, 0})
				packets := splitAllCurrentUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
				if len(packets) != 1 || packets[0].Header.MsgID != 1283 || packets[0].Header.Classification != 0 || !bytes.Equal(packets[0].Body, make([]byte, 8)) {
					t.Fatalf("mission query must return exactly NOTI1283/two empty progress collections: %v", packets)
				}
				if path := os.Getenv("DNF_GUILD_WAR_MISSION_WIRE"); path != "" && legacy && i == 0 {
					wire, err := json.Marshal(map[string]any{"opcode": 1283, "class": 0, "bodyHex": hex.EncodeToString(packets[0].Body)})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, wire, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			after, _, _ := repos.Settings.Load(context.Background(), guild.RegistryScope)
			if !reflect.DeepEqual(before, after) || peers[1].conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("read-only mission projection changed guild state or broadcast")
			}
		})
	}
}

func TestGuildWarMissionsRequireReadyOwnedMemberAndRequest(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		for _, mode := range []string{"disabled", "unselected", "not_ready", "wrong_account", "stale_generation", "not_member", "stale_member", "deleted", "class_zero", "empty", "short", "trailing", "other_kind"} {
			t.Run(map[bool]string{true: "legacy/", false: "upper/"}[legacy]+mode, func(t *testing.T) {
				s, peers, repos := guildTextFixture(t)
				s.options.guildEssenceEnabled = true
				p := peers[0]
				class, body := byte(1), []byte{2, 0, 0, 0}
				reg, _ := guild.Load(context.Background(), repos.Settings)
				switch mode {
				case "disabled":
					s.options.guildEssenceEnabled = false
				case "unselected":
					p.selectedCharacterID = 0
				case "not_ready":
					p.townSceneReadyCharacterID = 0
				case "wrong_account":
					p.accountID = "wrong"
				case "stale_generation":
					delete(s.gameSessions, 1)
				case "not_member":
					delete(reg.Guilds, 1)
				case "stale_member":
					m := reg.Guilds[1].Members["1"]
					m.AccountID = "former"
					reg.Guilds[1].Members["1"] = m
				case "deleted":
					actor, _, _ := repos.Character.Load(context.Background(), "1")
					actor.Stats["delete_flag"] = 1
					if err := repos.Character.Save(context.Background(), actor); err != nil {
						t.Fatal(err)
					}
				case "class_zero":
					class = 0
				case "empty":
					body = nil
				case "short":
					body = []byte{2}
				case "trailing":
					body = append(body, 0)
				case "other_kind":
					body[0] = 1
				}
				if err := guild.Save(context.Background(), repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
				dispatchGuildWarMissionTest(t, s, p, legacy, class, body)
				if p.conn.(*bufferConn).write.Len() != 0 {
					t.Fatal("invalid mission request produced packets")
				}
			})
		}
	}
}

func TestGuildWarMissionsLegacyKeepsSceneBoundary(t *testing.T) {
	s, peers, _ := guildTextFixture(t)
	s.options.guildEssenceEnabled = true
	p := peers[0]
	p.initialTownRouteCharacterID = p.selectedCharacterID
	p.initialTownRouteStage = currentInitialTownRoutePlayerStateSent
	dispatchGuildWarMissionTest(t, s, p, true, 1, []byte{2, 0, 0, 0})
	if !p.initialTownLegacySceneReadyAccepted {
		t.Fatal("mission routing bypassed the legacy scene-ready boundary")
	}
	if !bytes.Equal(guildReply(t, p, 1283), make([]byte, 8)) {
		t.Fatal("mission list missing after boundary")
	}
}
