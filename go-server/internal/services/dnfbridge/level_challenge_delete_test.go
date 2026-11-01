package dnfbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const levelChallengeDeleteAccount = "dnf:10001"

type levelChallengeDeleteFixture struct {
	service *Service
	session *gameSession
	conn    *bufferConn
	group   dnfrepo.Group
	account repoAccountSnapshot
	state   onlineevent.LevelChallengeState
}

type repoAccountSnapshot struct {
	metadata string
}

func newLevelChallengeDeleteFixture(t *testing.T, characterRepository dnfrepo.CharacterRepository) *levelChallengeDeleteFixture {
	t.Helper()
	t.Setenv(characterDeleteAckEnv, "")

	group := gameDeleteCharacterGroup(characterRepository)
	service, session, conn := newGameDeleteCharacterService(group, true)
	catalog := &onlineevent.LevelChallengeCatalog{
		AutomaticNewCharacters: true,
		Limit:                  1,
		Job:                    "[all]",
	}
	service.levelChallengeCatalog = catalog
	service.spendTimeCatalogState = &currentSpendTimeRuntimeCatalog{}
	enablePortedActivityTest(t, service, session)
	ctx := context.Background()
	if _, err := service.activityControls.Save(ctx, service.activityControls.Snapshot().Revision, []onlineevent.ControlChange{
		{ID: 222, Enabled: false},
		{ID: 288, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	service.portedActivityNativeReady[288] = true
	session.accountID = levelChallengeDeleteAccount
	session.spendTime.eventInfoSent = true

	now := time.Now().UTC()
	account := dnfrepo.AccountRecord{AccountID: levelChallengeDeleteAccount, Metadata: map[string]string{}}
	for _, item := range []struct {
		id, name  string
		slot      int
		challenge bool
	}{
		{id: "1", name: "ordinary0", slot: 0},
		{id: "3", name: "challenge1", slot: 1, challenge: true},
		{id: "15", name: "challenge2", slot: 2, challenge: true},
		{id: "16", name: "challenge3", slot: 3, challenge: true},
		{id: "17", name: "ordinary4", slot: 4},
	} {
		ch := dnfrepo.CharacterRecord{
			CharacterID: item.id,
			AccountID:   levelChallengeDeleteAccount,
			Slot:        item.slot,
			Name:        item.name,
			Job:         "0",
			Level:       1,
			CreatedAt:   now,
			UpdatedAt:   now,
			Stats:       map[string]int64{"delete_flag": 0},
		}
		if item.challenge {
			if err := catalog.Register(&account, &ch); err != nil {
				t.Fatalf("register %s: %v", item.id, err)
			}
		}
		if err := characterRepository.Save(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	state, err := onlineevent.ReadLevelChallengeState(account)
	if err != nil {
		t.Fatal(err)
	}
	challenge1Progress := state["3"]
	challenge1Progress.Bonus = map[uint32]bool{20: true}
	state["3"] = challenge1Progress
	challenge2Progress := state["15"]
	challenge2Progress.Main = map[uint32]bool{10: true}
	state["15"] = challenge2Progress
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	account.Metadata[onlineevent.LevelChallengeMetadataKey] = string(raw)
	if err := group.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}

	return &levelChallengeDeleteFixture{
		service: service,
		session: session,
		conn:    conn,
		group:   group,
		account: repoAccountSnapshot{metadata: account.Metadata[onlineevent.LevelChallengeMetadataKey]},
		state:   state,
	}
}

func seedLevelChallengeDeleteCharacters() *fakeCharacterStore {
	return &fakeCharacterStore{records: make(map[string]dnfrepo.CharacterRecord), nextID: 400}
}

func (f *levelChallengeDeleteFixture) delete(t *testing.T, slot uint16, name string) error {
	t.Helper()
	return f.service.handleGameCommand(
		f.session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketDeleteCharacter),
		gameDeleteCharacterBody(slot, name),
	)
}

func splitLevelChallengeDeleteAck(t *testing.T, stream []byte) (dnfproto.ChannelPacket, []byte) {
	t.Helper()
	packet, rest := splitGameServerUpperPacket(t, stream)
	if packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.CmdPacketDeleteCharacter) {
		t.Fatalf("delete ack header=%+v", packet.Header)
	}
	return packet, rest
}

func TestLevelChallengeDeleteRefreshesSelectorAfterAck(t *testing.T) {
	for _, test := range []struct {
		name string
		slot uint16
		role string
	}{
		{name: "delete middle challenge", slot: 2, role: "15"},
		{name: "delete first challenge", slot: 1, role: "3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := seedLevelChallengeDeleteCharacters()
			fixture := newLevelChallengeDeleteFixture(t, store)
			if err := fixture.delete(t, test.slot, "challenge"+strconv.Itoa(int(test.slot))); err != nil {
				t.Fatal(err)
			}

			ack, afterAck := splitLevelChallengeDeleteAck(t, fixture.conn.write.Bytes())
			if want := characterDeleteSuccessAckBody(test.slot); !bytes.Equal(ack.Body, want) {
				t.Fatalf("ack=%x, want %x", ack.Body, want)
			}
			selector, tail := splitGameServerUpperPacket(t, afterAck)
			if selector.Header.MsgID != 815 {
				t.Fatalf("packet after ack=%d, want 815", selector.Header.MsgID)
			}
			if want := []byte{3, 2, 1, 0, 0, 0, 2, 0, 0, 0}; !bytes.Equal(selector.Body, want) {
				t.Fatalf("815=%x, want %x", selector.Body, want)
			}
			if len(tail) != 0 {
				t.Fatalf("delete sent a new full roster or extra packet: %x", tail)
			}

			stored, found, err := fixture.group.Account.Load(context.Background(), levelChallengeDeleteAccount)
			if err != nil || !found {
				t.Fatalf("load account found=%t err=%v", found, err)
			}
			if got := stored.Metadata[onlineevent.LevelChallengeMetadataKey]; got != fixture.account.metadata {
				t.Fatalf("delete changed enrollment or reward receipts:\n got %s\nwant %s", got, fixture.account.metadata)
			}
			afterState, err := onlineevent.ReadLevelChallengeState(stored)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterState, fixture.state) {
				t.Fatalf("enrollment state changed: got %+v want %+v", afterState, fixture.state)
			}

			deleted, found, err := store.Load(context.Background(), test.role)
			if err != nil || !found || fakeCharacterDeleteFlag(deleted) != characterDeleteDeletedFlag {
				t.Fatalf("deleted role=%+v found=%t err=%v", deleted, found, err)
			}
			for _, ordinaryID := range []string{"1", "17"} {
				ordinary, found, err := store.Load(context.Background(), ordinaryID)
				if err != nil || !found || ordinary.Stats[onlineevent.LevelChallengeRoleStat] != 0 {
					t.Fatalf("ordinary role %s changed: %+v found=%t err=%v", ordinaryID, ordinary, found, err)
				}
			}
		})
	}
}

func TestLevelChallengeDeleteFailuresDoNotRefreshOrDeleteWrongRole(t *testing.T) {
	t.Run("name mismatch", func(t *testing.T) {
		store := seedLevelChallengeDeleteCharacters()
		fixture := newLevelChallengeDeleteFixture(t, store)
		before := snapshotLevelChallengeDeleteRecords(t, store)
		if err := fixture.delete(t, 2, "challenge3"); err != nil {
			t.Fatal(err)
		}
		ack, tail := splitLevelChallengeDeleteAck(t, fixture.conn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{characterDeleteAckNameMismatch}) {
			t.Fatalf("ack=%x", ack.Body)
		}
		if len(tail) != 0 {
			t.Fatalf("failed delete sent activity selector: %x", tail)
		}
		assertLevelChallengeDeleteRecords(t, store, before)
	})

	t.Run("write failure", func(t *testing.T) {
		store := &failingSaveCharacterStore{fakeCharacterStore: seedLevelChallengeDeleteCharacters()}
		fixture := newLevelChallengeDeleteFixture(t, store)
		store.saveErr = errors.New("injected delete failure")
		before := snapshotLevelChallengeDeleteRecords(t, store.fakeCharacterStore)
		if err := fixture.delete(t, 2, "challenge2"); err != nil {
			t.Fatal(err)
		}
		ack, tail := splitLevelChallengeDeleteAck(t, fixture.conn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{characterDeleteAckBlocked}) {
			t.Fatalf("ack=%x", ack.Body)
		}
		if len(tail) != 0 {
			t.Fatalf("failed delete sent activity selector: %x", tail)
		}
		assertLevelChallengeDeleteRecords(t, store.fakeCharacterStore, before)
	})
}

func TestLevelChallengeDeleteAckWriteFailureDoesNotRefreshSelector(t *testing.T) {
	store := seedLevelChallengeDeleteCharacters()
	fixture := newLevelChallengeDeleteFixture(t, store)
	failing := &failLevelChallengeDeleteWriteConn{}
	fixture.session.conn = failing

	if err := fixture.delete(t, 2, "challenge2"); err == nil {
		t.Fatal("delete succeeded despite acknowledging failure")
	}
	if failing.writes != 1 {
		t.Fatalf("connection writes=%d, want only the failed delete ack", failing.writes)
	}
	deleted, found, err := store.Load(context.Background(), "15")
	if err != nil || !found || fakeCharacterDeleteFlag(deleted) != characterDeleteDeletedFlag {
		t.Fatalf("committed soft delete=%+v found=%t err=%v", deleted, found, err)
	}
}

type failLevelChallengeDeleteWriteConn struct {
	bufferConn
	writes int
}

func (c *failLevelChallengeDeleteWriteConn) Write([]byte) (int, error) {
	c.writes++
	return 0, errors.New("injected ack write failure")
}

func snapshotLevelChallengeDeleteRecords(t *testing.T, store *fakeCharacterStore) map[string]dnfrepo.CharacterRecord {
	t.Helper()
	out := make(map[string]dnfrepo.CharacterRecord, len(store.records))
	for id, record := range store.records {
		out[id] = dnfrepo.CloneCharacter(record)
	}
	return out
}

func assertLevelChallengeDeleteRecords(t *testing.T, store *fakeCharacterStore, want map[string]dnfrepo.CharacterRecord) {
	t.Helper()
	if got := snapshotLevelChallengeDeleteRecords(t, store); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed delete changed character rows:\n got %+v\nwant %+v", got, want)
	}
}
