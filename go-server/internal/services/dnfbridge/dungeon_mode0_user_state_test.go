package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func mode0TailUserStateForTest(t *testing.T, body []byte) byte {
	t.Helper()
	_, tailStart, ok := currentSceneObjectLevelForLog(body)
	if !ok || tailStart+currentSceneObjectTailUserStateOffset >= len(body) {
		t.Fatalf("invalid mode0 body len=%d tailStart=%d ok=%t", len(body), tailStart, ok)
	}
	return body[tailStart+currentSceneObjectTailUserStateOffset]
}

func newMode0UserStateTestFixture(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		AccountID:   "dnf:1003",
		Name:        "test1",
		Job:         "0",
		Level:       90,
	}
	if err := repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: character.CharacterID,
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {SlotIndex: 12, ItemID: 101030517},
		},
	}); err != nil {
		t.Fatal(err)
	}
	repositories.Skill = nil

	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:            character.AccountID,
			gameUpperHeader:      gameUpperHeaderChannel13,
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
			currentClientVersion: "official-24.9",
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                            connection,
		connID:                          "dungeon-mode0-user-state",
		accountID:                       character.AccountID,
		selectedCharacterID:             3,
		channel:                         channelcatalog.Channel{ID: 10, Type: 10},
		connectionTownActorOwnerChannel: 10,
		townActorOwnerChannel:           10,
		postStartMapPlayerStateSent:     true,
	}
	return service, session, connection
}

func TestDungeonMode0AppearanceRefreshPreservesGenericUserState(t *testing.T) {
	service, session, connection := newMode0UserStateTestFixture(t)

	session.preDungeonContextPlayerStateSent = true
	session.dungeonUserStateActive.Store(true)
	if err := service.sendSelectedActorMode0AppearanceRefresh(session, "wear_in_dungeon", "test"); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.Classification != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketSetUDPIPPort) {
		t.Fatalf("unexpected dungeon refresh packet=%+v", packet.Header)
	}
	if got := mode0TailUserStateForTest(t, packet.Body); got != currentDungeonPlayerUserState {
		t.Fatalf("dungeon mode0 tail[5] user_state = %d, want %d", got, currentDungeonPlayerUserState)
	}

	captured := captureCurrentRaidRetrySceneGates(session)
	resetDungeonReturnSceneGates(session)
	if session.dungeonUserStateActive.Load() {
		t.Fatal("resetDungeonReturnSceneGates did not clear dungeonUserStateActive")
	}
	session.townActorOwnerChannel = 10
	connection.write.Reset()
	if err := service.sendSelectedActorMode0AppearanceRefresh(session, "wear_in_town", "test"); err != nil {
		t.Fatal(err)
	}
	townPacket, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if got := mode0TailUserStateForTest(t, townPacket.Body); got != 0 {
		t.Fatalf("town mode0 tail[5] user_state = %d, want 0", got)
	}

	captured.restore(session)
	if !session.dungeonUserStateActive.Load() {
		t.Fatal("raid retry gate restore did not restore dungeonUserStateActive")
	}
	connection.write.Reset()
	if err := service.sendSelectedActorMode0AppearanceRefresh(session, "raid_retry_restore", "test"); err != nil {
		t.Fatal(err)
	}
	retryPacket, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if got := mode0TailUserStateForTest(t, retryPacket.Body); got != currentDungeonPlayerUserState {
		t.Fatalf("restored raid retry mode0 tail[5] user_state = %d, want %d", got, currentDungeonPlayerUserState)
	}

	resetDungeonEntrySceneGates(session)
	if session.dungeonUserStateActive.Load() {
		t.Fatal("resetDungeonEntrySceneGates did not clear dungeonUserStateActive")
	}
}
