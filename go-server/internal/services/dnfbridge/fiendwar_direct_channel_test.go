package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/channelinfo"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	"longheng.io/server/internal/modules/dnf/raid/fiendwar"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func directFiendWarLoginFixture(t *testing.T, id int) (*Service, *gameSession, dnfrepo.Group, channelcatalog.Channel) {
	t.Helper()
	s, p, repos := raidReturnFixture(t)
	s.options.currentClientVersion = "official-24.9"
	s.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
	s.options.channelInfoFile = filepath.Join("testdata", "channel_info.etc")
	catalog, script, err := s.loadChannelAssets()
	if err != nil {
		t.Fatal(err)
	}
	s.catalog, s.channelScript = catalog, script
	channel, found := catalog.Channel(id)
	if !found {
		t.Fatalf("missing channel %d", id)
	}
	resident, found := catalog.ResidentFor(channel)
	if !found {
		t.Fatal("missing resident channel")
	}
	p.conn = &bufferConn{}
	p.accountID = s.accountID()
	p.channel = channel
	p.residentChannel = resident
	p.selectedCharacterID = 0
	p.characterGeneration = 1
	p.townSceneReadyCharacterID = 0
	p.initialTownRouteCharacterID = 0
	p.initialTownRouteStage = 0
	p.channelReconnect = false
	p.rosterRequested = false
	p.loginPacketSeen = true
	p.accountFromToken = true
	p.loginChannelFlowKey = sha256.Sum256([]byte("fiendwar-direct-no-exit"))
	p.gameEndpointSuccessSent = false
	p.currentChannelResidentNoticeSent = true
	p.coldChannelLanding = nil
	p.channelExitHandoff = nil
	p.connectionTownActorOwnerChannel = byte(resident.ID)
	p.townActorOwnerChannel = byte(resident.ID)
	return s, p, repos, channel
}

func TestCombinedFiendWarDirectChannelNoExitLoginAndSelection(t *testing.T) {
	for _, id := range []int{261, 262, 263} {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			s, p, repos, channel := directFiendWarLoginFixture(t, id)
			if channel.Type != 42 || !strings.EqualFold(strings.Trim(channel.Group, "[]"), "fiendwar") {
				t.Fatalf("raw catalog projection changed: %+v", channel)
			}
			if _, valid := currentChannelHandoffKey(p); !valid || p.gameEndpointSuccessSent || !p.currentChannelResidentNoticeSent || p.characterGeneration != 1 {
				t.Fatalf("fixture precondition valid=%t success=%t notice=%t generation=%d", valid, p.gameEndpointSuccessSent, p.currentChannelResidentNoticeSent, p.characterGeneration)
			}

			if err := s.handleCurrentGameEndpointRequest(p, 1, currentObservedChannelReconnectDisplayProbeSize); err != nil {
				t.Fatal(err)
			}
			if p.channel.ID != id || p.channel.Type != 42 || p.coldChannelLanding != nil || !p.gameEndpointSuccessSent {
				t.Fatalf("no-EXIT direct login was redirected: %+v cold=%+v success=%t", p.channel, p.coldChannelLanding, p.gameEndpointSuccessSent)
			}
			packets := splitAllCurrentGameServerUpperPackets(t, p.conn.(*bufferConn).write.Bytes())
			loginFound := false
			for _, packet := range packets {
				if packet.Header.Classification == 1 && packet.Header.MsgID == 1 {
					loginFound = true
					if len(packet.Body) < 4 || packet.Body[3] != 45 {
						t.Fatalf("client login type=%d, want 45; body=%x", packet.Body[3], packet.Body)
					}
					ipLen := int(binary.LittleEndian.Uint32(packet.Body[9:13]))
					portAt := 13 + ipLen
					if portAt+4 > len(packet.Body) || binary.LittleEndian.Uint32(packet.Body[portAt:portAt+4]) != uint32(channel.Port) {
						t.Fatal("login endpoint does not use raw channel 261/262/263 port")
					}
				}
			}
			if !loginFound {
				t.Fatal("missing native LOGIN success")
			}

			p.rosterRequested = true
			if err := s.sendUpperCSharpSelectInit(p, selectFirstSlotRequest()); err != nil {
				t.Fatal(err)
			}
			if p.selectedCharacterID != 29 || p.channel.ID != id || p.channel.Type != 42 {
				t.Fatalf("selection redirected the dedicated channel: selected=%d channel=%+v", p.selectedCharacterID, p.channel)
			}
			ctx := context.Background()
			character, found, err := repos.Character.Load(ctx, "29")
			if err != nil || !found {
				t.Fatalf("load selected character: found=%t err=%v", found, err)
			}
			projected, townID, areaID, row, path, err := s.currentCharacterListLoginTransition(ctx, p, 29, character)
			if err != nil || townID != 98 || areaID != 0 || !strings.Contains(strings.ToLower(path), "gate_fiend") || projected.Stats["town_id"] != 98 {
				t.Fatalf("direct entry landing=%d/%d path=%q stats=%v err=%v", townID, areaID, path, projected.Stats, err)
			}
			body, err := buildCurrentSceneTransitionBody(townID, areaID, []currentSceneTransitionRow{row})
			if err != nil || len(body) < 2 || body[0] != 98 || body[1] != 0 {
				t.Fatalf("scene commit does not begin at 98/0: %x %v", body, err)
			}
			stored, found, err := repos.Character.Load(ctx, "29")
			if err != nil || !found || stored.Stats["town_id"] != 98 || stored.Stats["area_id"] != 0 || stored.Stats[currentRaidReturnOriginMarker] != 1 {
				t.Fatalf("entry landing/origin was not persisted: found=%t stats=%v err=%v", found, stored.Stats, err)
			}
		})
	}
}

func TestCombinedFiendWarDirectoryAndLoginSeparateClientProjection(t *testing.T) {
	s, p, _, channel := directFiendWarLoginFixture(t, 261)
	for _, id := range []int{261, 262, 263} {
		raw, found := s.catalog.Channel(id)
		if !found || raw.Type != 42 || !strings.EqualFold(strings.Trim(raw.Group, "[]"), "fiendwar") {
			t.Fatalf("internal catalog %d=%+v", id, raw)
		}
	}
	packet, err := s.buildScriptBody()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptCompressedChannelData(packet, s.aesKey())
	if err != nil {
		t.Fatal(err)
	}
	index, err := channelinfo.Parse(bytes.TrimRight(plain, "\x00"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{261, 262, 263} {
		client, found := index.ServerChannel(0, id)
		if !found || client.Type != 45 {
			t.Fatalf("client directory %d=%+v found=%t", id, client, found)
		}
	}
	if got := s.buildLoginSuccess(channel)[2]; got != 45 {
		t.Fatalf("native LOGIN type=%d want 45", got)
	}
	if route, ok := currentRaidRouteForChannel(int(p.channel.Type)); !ok || route != 2 {
		t.Fatalf("internal route for type42=%d ok=%t", route, ok)
	}
}

func TestRealPVFFiendWarDirectEntryQuestAndTown98(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the read-only runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dnftown.Load(context.Background(), archive, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := fiendwar.LoadFiendWarConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, townCatalog: table, fiendWarConfig: config, initialEquipmentArchive: archive}
	p := &gameSession{accountID: "dnf:1", channel: channelcatalog.Channel{ServerID: 1, ID: 261, Type: 42, Group: "fiendwar"}}
	location, err := s.currentFiendWarLoginLocation(p, 29)
	if err != nil || location.TownID != 98 || location.AreaID != 0 || !strings.Contains(strings.ToLower(location.MapPath), "gate_fiend") {
		t.Fatalf("real PVF direct entry=%+v err=%v", location, err)
	}
	rule, err := dnfquest.LoadSpecialChannelRule(archive)
	if err != nil || rule.FiendWarQuestID != 8641 {
		t.Fatalf("FiendWar entry quest=%d err=%v", rule.FiendWarQuestID, err)
	}
	catalog, err := s.loadQuestCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.QuestRecord{CharacterID: "29", States: map[int64]dnfrepo.QuestState{8641: {Status: "completed"}}}
	if permission := catalog.SpecialChannelPermissionForRecord(rule, record); !permission.FiendWar {
		t.Fatal("completed quest 8641 does not grant FiendWar roster channel permission")
	}
}
