package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestHighChannelReconnectSelectKeepsRawChannelAndCommittedOwner(t *testing.T) {
	for _, id := range []int{261, 262, 263, 508} {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			s, p, repos, raw := directFiendWarLoginFixture(t, id)
			p.channelReconnect = true
			p.gameEndpointSuccessSent = true
			owner := p.connectionTownActorOwnerChannel
			if owner == 0 || int(owner) != p.residentChannel.ID || int(owner) == id {
				t.Fatal("fixture must use a catalog-derived u8 owner for a high raw channel")
			}
			body := make([]byte, 2)
			binary.LittleEndian.PutUint16(body, 29)
			frame, err := dnfproto.BuildChannelPacket(4, body, 2, dnfproto.DefaultChannelClassification)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameUpper(p, frame); err != nil {
				t.Fatalf("high-channel reconnect disconnected after select: %v", err)
			}
			if p.channel.ID != raw.ID || p.channel.Type != raw.Type || p.channel.Port != raw.Port {
				t.Fatal("special channel was redirected to its owner alias")
			}
			if p.channelReconnect || !p.sceneBootstrapTailSent || p.sceneBootstrapTailDeferred {
				t.Fatal("reconnect route did not finish")
			}
			packets := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
			if id != 508 {
				assertImmediateChannelReconnectOrder(t, packets, owner, false)
			} else {
				assertNoChannelReconnectResidentNotice(t, packets)
				modes := 0
				for _, packet := range packets {
					if packet.Header.Classification == 0 && packet.Header.MsgID == 2 && len(packet.Body) >= 5 {
						modes++
						if packet.Body[3] != 0 || packet.Body[4] != owner {
							t.Fatalf("tower owner mismatch: %x", packet.Body[:5])
						}
					}
				}
				if modes != 3 {
					t.Fatalf("tower actor bootstrap modes=%d", modes)
				}
			}
			character, found, err := repos.Character.Load(context.Background(), "29")
			if err != nil || !found {
				t.Fatal("selected character disappeared")
			}
			transitionFound := false
			for _, packet := range packets {
				if packet.Header.Classification != 0 || packet.Header.MsgID != 24 {
					continue
				}
				transitionFound = true
				if len(packet.Body) < 2 || int64(packet.Body[0]) != character.Stats["town_id"] || int64(packet.Body[1]) != character.Stats["area_id"] {
					t.Fatal("town transition disagrees with the persisted entry location")
				}
				if id != 508 && (packet.Body[0] != 98 || packet.Body[1] != 0) {
					t.Fatal("Fiend War entry no longer uses its own town")
				}
			}
			if !transitionFound {
				t.Fatal("missing town transition")
			}
		})
	}
}

func TestRealPVFHighChannelReconnectTownEntry(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the read-only runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	towns, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := fiendwar.LoadFiendWarConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{261, 262, 263} {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			s, p, repos, _ := directFiendWarLoginFixture(t, id)
			s.townCatalog, s.fiendWarConfig = towns, config
			s.initialEquipmentArchive = archive
			repos = fullHighChannelArchiveRepositories(t, repos, p.accountID)
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
			p.channelReconnect, p.gameEndpointSuccessSent = true, true
			body := make([]byte, 2)
			binary.LittleEndian.PutUint16(body, 29)
			frame, err := dnfproto.BuildChannelPacket(4, body, 2, dnfproto.DefaultChannelClassification)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameUpper(p, frame); err != nil {
				t.Fatal(err)
			}
			character, found, err := repos.Character.Load(context.Background(), "29")
			if err != nil || !found || character.Stats["town_id"] != 98 || character.Stats["area_id"] != 0 {
				t.Fatalf("real PVF reconnect did not enter the Fiend War gate: %+v err=%v", character, err)
			}
			packets := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
			assertNoChannelReconnectResidentNotice(t, packets)
			counter, transition, modes := -1, -1, 0
			for index, packet := range packets {
				if packet.Header.Classification != 0 {
					continue
				}
				switch packet.Header.MsgID {
				case 2:
					modes++
					if len(packet.Body) < 5 || packet.Body[3] != 0 || packet.Body[4] != p.connectionTownActorOwnerChannel {
						t.Fatal("full archive route changed committed actor owner")
					}
				case 812:
					if counter != -1 || len(packet.Body) != 9 {
						t.Fatal("dimension count must be sent once in native shape")
					}
					counter = index
				case 24:
					transition = index
					if len(packet.Body) < 2 || packet.Body[0] != 98 || packet.Body[1] != 0 {
						t.Fatal("full archive transition lost Fiend War town")
					}
				}
			}
			if counter < 0 || transition <= counter || modes != 3 || p.channelReconnect || !p.sceneBootstrapTailSent {
				t.Fatalf("full archive entry unfinished: counter=%d transition=%d modes=%d", counter, transition, modes)
			}
		})
	}
}

// Full PVF initialization uses online-event and mailbox aggregate transactions,
// whereas the lightweight route fixture needs only location transactions.
func fullHighChannelArchiveRepositories(t *testing.T, source dnfrepo.Group, account string) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	target := memory.NewMemoryGroup()
	if row, found, err := source.Account.Load(ctx, account); err != nil {
		t.Fatal(err)
	} else if found {
		if err := target.Account.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if row, found, err := source.Character.Load(ctx, "29"); err != nil {
		t.Fatal(err)
	} else if found {
		if err := target.Character.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if row, found, err := source.Inventory.Load(ctx, "29"); err != nil {
		t.Fatal(err)
	} else if found {
		if err := target.Inventory.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if row, found, err := source.Equipment.Load(ctx, "29"); err != nil {
		t.Fatal(err)
	} else if found {
		if err := target.Equipment.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if row, found, err := source.Skill.Load(ctx, "29"); err != nil {
		t.Fatal(err)
	} else if found {
		if err := target.Skill.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if row, found, err := source.Quest.Load(ctx, "29"); err != nil {
		t.Fatal(err)
	} else if found {
		if err := target.Quest.Save(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func TestHighChannelReconnectRejectsUncommittedIdentityBeforeTownWrites(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Service, *gameSession)
	}{
		{"missing_notice", func(s *Service, p *gameSession) { p.currentChannelResidentNoticeSent = false }},
		{"missing_catalog", func(s *Service, p *gameSession) { s.catalog = nil }},
		{"unknown_raw_channel", func(s *Service, p *gameSession) { p.channel.ID = 999 }},
		{"wrong_raw_server", func(s *Service, p *gameSession) { p.channel.ServerID++ }},
		{"wrong_raw_type", func(s *Service, p *gameSession) { p.channel.Type++ }},
		{"wrong_raw_port", func(s *Service, p *gameSession) { p.channel.Port++ }},
		{"wrong_resident", func(s *Service, p *gameSession) { p.residentChannel.ID++ }},
		{"wrong_resident_server", func(s *Service, p *gameSession) { p.residentChannel.ServerID++ }},
		{"truncated_owner", func(s *Service, p *gameSession) { p.connectionTownActorOwnerChannel = 5 }},
		{"zero_owner", func(s *Service, p *gameSession) { p.connectionTownActorOwnerChannel = 0 }},
		{"ordinary_channel_mismatch", func(s *Service, p *gameSession) { p.channel.ID = 11 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p, repos, _ := directFiendWarLoginFixture(t, 261)
			p.channelReconnect = true
			p.selectedCharacterID = 29
			before, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(s, p)
			ownerBefore := p.townActorOwnerChannel
			if err := s.sendCurrentChannelReconnectTownEntry(p); err == nil {
				t.Fatal("accepted an uncommitted channel identity")
			}
			if p.conn.(*bufferConn).write.Len() != 0 || p.townActorOwnerChannel != ownerBefore {
				t.Fatal("rejected identity emitted town packets or changed its owner")
			}
			after, _, err := repos.Character.Load(context.Background(), "29")
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"town_id", "area_id", "position_x", "position_y"} {
				if before.Stats[key] != after.Stats[key] {
					t.Fatalf("rejected identity changed %s", key)
				}
			}
		})
	}
}
