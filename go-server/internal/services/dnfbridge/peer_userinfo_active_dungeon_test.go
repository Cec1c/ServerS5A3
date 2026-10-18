package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// peerUserInfoActiveDungeonFixture creates two currently bound, same-channel
// party members in the same active dungeon run. They intentionally have no town
// area entries, so PeerInSameArea is false exactly like the live report.
type peerUserInfoActiveDungeonFixture struct {
	service      *Service
	source       *gameSession
	target       *gameSession
	repositories dnfrepo.Group
}

func newPeerUserInfoActiveDungeonFixture(t *testing.T) *peerUserInfoActiveDungeonFixture {
	t.Helper()
	t.Setenv(ownerContextUseClientChannelEnv, "on")
	t.Setenv(peerUserInfoIdentityCarrierEnv, peerUserInfoIdentityCarrierLegacy)

	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	for _, account := range []dnfrepo.AccountRecord{
		{AccountID: "dnf:1", State: "active"},
		{AccountID: "dnf:2", State: "active"},
	} {
		if err := repositories.Account.Save(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	for _, character := range []dnfrepo.CharacterRecord{
		{CharacterID: "1001", AccountID: "dnf:1", Name: "source", Job: "0", Level: 90},
		{CharacterID: "1002", AccountID: "dnf:2", Name: "target", Job: "1", Level: 90},
	} {
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
	}

	channel := channelcatalog.Channel{ID: 10}
	sourceConn := &bufferConn{}
	targetConn := &bufferConn{}
	source := &gameSession{
		conn:                            sourceConn,
		channel:                         channel,
		accountID:                       "dnf:1",
		selectedCharacterID:             1001,
		townActorOwnerChannel:           0,
		connectionTownActorOwnerChannel: 10,
	}
	target := &gameSession{
		conn:                            targetConn,
		channel:                         channel,
		accountID:                       "dnf:2",
		selectedCharacterID:             1002,
		townActorOwnerChannel:           0,
		connectionTownActorOwnerChannel: 10,
	}
	service := &Service{
		options: options{
			currentClientVersion: "official-24.9",
			gameUpperHeader:      gameUpperHeaderServer16,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
		},
		onlinePlayers:       newOnlinePlayerManager(),
		adventureGroupTable: loadAdventureGroupTestTables(t),
		repositoryProvider: func() (dnfrepo.Group, bool) {
			return repositories, true
		},
	}
	service.bindGameSessionCharacter(source, 1001)
	service.bindGameSessionCharacter(target, 1002)

	manager := service.runtimePartyManagerForService()
	created := manager.Create(party.RuntimePartyMember{
		UserID:            1001,
		SessionGeneration: service.characterGenerationForTest(source),
		State:             alignedcmd.PartyMemberState{UserID: 1001, UserState: 1, HPPercent: 100, MPPercent: 100},
	}, alignedcmd.PartyState{NameBytes: []byte("party"), MaxMembers: 4, UserState: 1})
	if !created.OK {
		t.Fatalf("runtime party create: %+v", created)
	}
	joined := manager.Join(created.Party.ID, party.RuntimePartyMember{
		UserID:            1002,
		SessionGeneration: service.characterGenerationForTest(target),
		State:             alignedcmd.PartyMemberState{UserID: 1002, UserState: 1, HPPercent: 100, MPPercent: 100},
	})
	if !joined.OK {
		t.Fatalf("runtime party join: %+v", joined)
	}
	service.publishRuntimePartySnapshot(joined.Party)

	for _, session := range []*gameSession{source, target} {
		runtime := &runtimeDungeonState{
			Dungeon:   worldmap.Dungeon{ID: 3},
			MazeIndex: 0,
			Seed:      1234,
			Session:   &worldmap.DungeonSession{},
		}
		session.dungeon.mu.Lock()
		session.dungeon.runtime = runtime
		session.dungeon.mu.Unlock()
	}
	runKey := dungeonPartyRunKey{
		PartyID:   uint16(joined.Party.ID),
		DungeonID: 3,
		MazeIndex: 0,
		RoomX:     0,
		RoomY:     1,
		MapID:     58548,
		Seed:      1234,
	}
	for _, session := range []*gameSession{source, target} {
		session.dungeonPartyRunMu.Lock()
		session.dungeonPartyRun = runKey
		session.dungeonPartyRunValid = true
		session.dungeonPartyRunMu.Unlock()
	}
	return &peerUserInfoActiveDungeonFixture{
		service:      service,
		source:       source,
		target:       target,
		repositories: repositories,
	}
}

func peerUserInfoActiveDungeonRequest(targetID uint16) []byte {
	return []byte{byte(targetID), byte(targetID >> 8), currentPeerUserInfoMode}
}

func assertPeerUserInfoActiveDungeonRejected(t *testing.T, fixture *peerUserInfoActiveDungeonFixture, requestSession *gameSession) {
	t.Helper()
	handled, err := fixture.service.handleCurrentPeerUserInfoRequest(
		requestSession,
		peerUserInfoActiveDungeonRequest(1002),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("mode3 peer request must be consumed")
	}
	if got := requestSession.conn.(*bufferConn).write.Len(); got != 0 {
		t.Fatalf("rejected request wrote %d bytes", got)
	}
}

func mutatePeerUserInfoActiveDungeonRunKey(t *testing.T, session *gameSession, mutate func(*dungeonPartyRunKey)) {
	t.Helper()
	session.dungeonPartyRunMu.Lock()
	defer session.dungeonPartyRunMu.Unlock()
	mutate(&session.dungeonPartyRun)
}

func TestPeerUserInfoActiveDungeonSameRunSendsMode3TargetID(t *testing.T) {
	fixture := newPeerUserInfoActiveDungeonFixture(t)
	handled, err := fixture.service.handleCurrentPeerUserInfoRequest(
		fixture.source,
		peerUserInfoActiveDungeonRequest(1002),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("mode3 dungeon peer request was not consumed")
	}
	packet, rest := splitLongHengGameServerUpperPacket(t, fixture.source.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes: %x", rest)
	}
	if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
		t.Fatalf("response header=%+v", packet.Header)
	}
	body := packet.Body
	if len(body) < 0x17 || body[0] != currentPeerUserInfoMode {
		t.Fatalf("mode3 body head=%x", body[:min(len(body), 32)])
	}
	if body[4] != 10 {
		t.Fatalf("owner context=%d want connection context 10", body[4])
	}
	if got := binary.LittleEndian.Uint16(body[13:15]); got != 1002 {
		t.Fatalf("mode3 object key=%d want target id 1002", got)
	}
}

func TestPeerUserInfoActiveDungeonOwnerUsesConnectionContextAcrossDungeonActorPlans(t *testing.T) {
	for _, mode := range []partyDungeonOwnerMode{partyDungeonOwnerSceneContext, partyDungeonOwnerClientChannel, partyDungeonOwnerMixed} {
		t.Run(mode.String(), func(t *testing.T) {
			fixture := newPeerUserInfoActiveDungeonFixture(t)
			fixture.service.options.partyDungeonOwner = mode
			if got := currentTownRemoteActorOwnerContext(fixture.source, 1002); got != 10 {
				t.Fatalf("town owner=%d want retained channel 10", got)
			}
			_, err := fixture.service.handleCurrentPeerUserInfoRequest(fixture.source, peerUserInfoActiveDungeonRequest(1002))
			if err != nil {
				t.Fatal(err)
			}
			packet, rest := splitLongHengGameServerUpperPacket(t, fixture.source.conn.(*bufferConn).write.Bytes())
			if len(rest) != 0 || len(packet.Body) < 0x17 {
				t.Fatalf("invalid mode3 response: body=%d rest=%d", len(packet.Body), len(rest))
			}
			if got, want := packet.Body[4], byte(10); got != want {
				t.Fatalf("mode3 owner=%d want active connection owner %d", got, want)
			}
		})
	}
}

func TestPeerUserInfoActiveDungeonRejectsNonExactPeer(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture)
	}{
		{
			name: "different_seed",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				mutatePeerUserInfoActiveDungeonRunKey(t, fixture.target, func(key *dungeonPartyRunKey) { key.Seed = 999 })
			},
		},
		{
			name: "different_dungeon",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				mutatePeerUserInfoActiveDungeonRunKey(t, fixture.target, func(key *dungeonPartyRunKey) { key.DungeonID = 4 })
			},
		},
		{
			name: "different_room",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				mutatePeerUserInfoActiveDungeonRunKey(t, fixture.target, func(key *dungeonPartyRunKey) { key.RoomY = 2 })
			},
		},
		{
			name: "different_channel",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				fixture.target.channel.ID = 11
			},
		},
		{
			name: "offline",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				fixture.service.mu.Lock()
				delete(fixture.service.gameSessions, 1002)
				fixture.service.mu.Unlock()
			},
		},
		{
			name: "different_party",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				state := runtimePartyStateSnapshot(fixture.target)
				state.PartyID = 99
				storeRuntimePartyState(fixture.target, state)
			},
		},
		{
			name: "not_party_member",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				state := runtimePartyStateSnapshot(fixture.target)
				state.Members = []alignedcmd.PartyMemberState{{UserID: 1001, Slot: 0}}
				storeRuntimePartyState(fixture.target, state)
			},
		},
		{
			name: "account_mismatch",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				ctx := context.Background()
				character, found, err := fixture.repositories.Character.Load(ctx, "1002")
				if err != nil || !found {
					t.Fatalf("load target character: found=%v err=%v", found, err)
				}
				character.AccountID = "dnf:other"
				if err := fixture.repositories.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid_run_key",
			mutate: func(t *testing.T, fixture *peerUserInfoActiveDungeonFixture) {
				fixture.target.dungeonPartyRunMu.Lock()
				fixture.target.dungeonPartyRunValid = false
				fixture.target.dungeonPartyRunMu.Unlock()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPeerUserInfoActiveDungeonFixture(t)
			test.mutate(t, fixture)
			assertPeerUserInfoActiveDungeonRejected(t, fixture, fixture.source)
		})
	}
}

func TestPeerUserInfoActiveDungeonRejectsStaleSourceSession(t *testing.T) {
	fixture := newPeerUserInfoActiveDungeonFixture(t)
	stale := fixture.source
	replacement := &gameSession{
		conn:                  &bufferConn{},
		channel:               stale.channel,
		accountID:             stale.accountID,
		selectedCharacterID:   1001,
		townActorOwnerChannel: currentSceneObjectContext,
	}
	fixture.service.mu.Lock()
	fixture.service.gameSessions[1001] = replacement
	fixture.service.mu.Unlock()
	assertPeerUserInfoActiveDungeonRejected(t, fixture, stale)
}

func TestPeerUserInfoActiveDungeonDoesNotWidenTownWithStaleRunKey(t *testing.T) {
	fixture := newPeerUserInfoActiveDungeonFixture(t)
	// Keep the party and run keys intact, but end the active dungeon runtime.
	// This is the stale-key shape a returning town session can briefly hold.
	for _, session := range []*gameSession{fixture.source, fixture.target} {
		session.dungeon.mu.Lock()
		session.dungeon.runtime = nil
		session.dungeon.mu.Unlock()
	}
	assertPeerUserInfoActiveDungeonRejected(t, fixture, fixture.source)
}

func TestPeerUserInfoActiveDungeonRejectsStaleTargetBinding(t *testing.T) {
	fixture := newPeerUserInfoActiveDungeonFixture(t)
	stale := fixture.target
	fixture.service.mu.Lock()
	fixture.service.gameSessions[1002] = &gameSession{
		conn:                  &bufferConn{},
		channel:               stale.channel,
		accountID:             stale.accountID,
		selectedCharacterID:   1002,
		townActorOwnerChannel: currentSceneObjectContext,
	}
	fixture.service.mu.Unlock()
	if fixture.service.currentDungeonPeerUserInfoAllowed(context.Background(), fixture.source, stale, 1001, 1002) {
		t.Fatal("stale target binding must not be accepted")
	}
}

func TestPeerUserInfoActiveDungeonUsesRetainedConnectionOwner(t *testing.T) {
	for _, channelID := range []int{10, 508} {
		t.Run(fmt.Sprint(channelID), func(t *testing.T) {
			fixture := newPeerUserInfoActiveDungeonFixture(t)
			t.Setenv(ownerContextUseClientChannelEnv, "off")
			fixture.source.channel.ID = channelID
			fixture.target.channel.ID = channelID
			fixture.source.townActorOwnerChannel = 0
			fixture.target.townActorOwnerChannel = 0
			fixture.source.connectionTownActorOwnerChannel = 10
			fixture.target.connectionTownActorOwnerChannel = 10
			_, err := fixture.service.handleCurrentPeerUserInfoRequest(fixture.source, peerUserInfoActiveDungeonRequest(1002))
			if err != nil {
				t.Fatal(err)
			}
			packet, rest := splitLongHengGameServerUpperPacket(t, fixture.source.conn.(*bufferConn).write.Bytes())
			if len(rest) != 0 || len(packet.Body) < 15 || packet.Body[4] != 10 {
				t.Fatalf("retained owner response=%x rest=%d", packet.Body[:min(15, len(packet.Body))], len(rest))
			}
		})
	}
}

func TestPeerUserInfoActiveDungeonRejectsUnknownContextAndLegacyProfile(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			fixture := newPeerUserInfoActiveDungeonFixture(t)
			if legacy {
				fixture.service.options.currentClientVersion = "legacy"
			} else {
				t.Setenv(ownerContextUseClientChannelEnv, "off")
				fixture.source.connectionTownActorOwnerChannel = 0
			}
			assertPeerUserInfoActiveDungeonRejected(t, fixture, fixture.source)
		})
	}
}

func TestPeerUserInfoActiveDungeonStaleTownPresenceCannotBypassRunGate(t *testing.T) {
	fixture := newPeerUserInfoActiveDungeonFixture(t)
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1001, TownID: 1, AreaID: 2, Session: fixture.source})
	fixture.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 1002, TownID: 1, AreaID: 2, Session: fixture.target})
	if !fixture.service.onlinePlayers.PeerInSameArea(1001, 1002) {
		t.Fatal("fixture must retain same-area entries")
	}
	mutatePeerUserInfoActiveDungeonRunKey(t, fixture.target, func(key *dungeonPartyRunKey) { key.RoomY++ })
	assertPeerUserInfoActiveDungeonRejected(t, fixture, fixture.source)
}

func TestPeerUserInfoActiveDungeonBuffCarrierAndDetailUseSameActiveActorKey(t *testing.T) {
	fixture := newPeerUserInfoActiveDungeonFixture(t)
	t.Setenv(buffSwapReapplyEnv, "on")
	_, err := fixture.service.handleCurrentPeerUserInfoRequest(fixture.source, peerUserInfoActiveDungeonRequest(1002))
	if err != nil {
		t.Fatal(err)
	}
	carrier, rest := splitLongHengGameServerUpperPacket(t, fixture.source.conn.(*bufferConn).write.Bytes())
	detail, tail := splitLongHengGameServerUpperPacket(t, rest)
	if len(tail) != 0 || len(carrier.Body) < 23 || len(detail.Body) < 15 || carrier.Body[0] != 1 || detail.Body[0] != 3 {
		t.Fatalf("carrier=%d detail=%d tail=%d", len(carrier.Body), len(detail.Body), len(tail))
	}
	if carrier.Body[4] != 10 || detail.Body[4] != 10 || binary.LittleEndian.Uint16(carrier.Body[21:23]) != 1002 || binary.LittleEndian.Uint16(detail.Body[13:15]) != 1002 {
		t.Fatalf("carrier/detail actor context mismatch")
	}
}
