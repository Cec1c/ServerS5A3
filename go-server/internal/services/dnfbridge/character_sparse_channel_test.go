package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCurrentSlotSwapUsesSentCompactRows(t *testing.T) {
	for _, kind := range []string{"sparse", "stale", "missing", "same", "foreign", "no_roster"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			group := memory.NewMemoryGroup()
			left := repo.CharacterRecord{CharacterID: "201", AccountID: "owner", Slot: 2, Name: "left", Job: "0", Level: 1}
			right := repo.CharacterRecord{CharacterID: "202", AccountID: "owner", Slot: 7, Name: "right", Job: "0", Level: 1}
			for _, ch := range []repo.CharacterRecord{left, right} {
				if err := group.Character.Save(ctx, ch); err != nil {
					t.Fatal(err)
				}
			}
			s := &Service{options: options{currentClientVersion: "official-24.9"}, repositoryProvider: func() (repo.Group, bool) { return group, true }}
			p := &gameSession{conn: &bufferConn{}, accountID: "owner", loginPacketSeen: true, accountFromToken: true, loginChannelFlowKey: [32]byte{1}}
			rememberRosterForTest(t, s, p, []repo.CharacterRecord{left, right})
			body := make([]byte, 8)
			binary.LittleEndian.PutUint32(body[4:], 1)
			success := kind == "sparse" || kind == "same"
			switch kind {
			case "stale":
				left.Slot = 3
				if err := group.Character.Save(ctx, left); err != nil {
					t.Fatal(err)
				}
			case "missing":
				binary.LittleEndian.PutUint32(body[4:], 7)
			case "same":
				binary.LittleEndian.PutUint32(body[4:], 0)
			case "foreign":
				p.currentRenameRoster[1].AccountID = "another"
			case "no_roster":
				p.currentRenameRoster = nil
			}
			if err := s.handleChangeCharacterSlot(p, body, true); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, p.conn.(*bufferConn).write.Bytes())
			if bytes.Equal(ack.Body, []byte{1}) != success {
				t.Fatalf("unexpected ack %x", ack.Body)
			}
			a, _, _ := group.Character.Load(ctx, "201")
			b, _, _ := group.Character.Load(ctx, "202")
			if kind == "sparse" {
				if a.Slot != 7 || b.Slot != 2 {
					t.Fatalf("swap %d/%d", a.Slot, b.Slot)
				}
			} else if a.Slot != left.Slot || b.Slot != right.Slot {
				t.Fatal("rejected swap mutated records")
			}
			if success {
				cached := s.cachedCurrentTagTournamentSentRoster(p, 1)
				if len(cached) != 2 || cached[0].CharacterID != p.currentRenameRoster[0].CharacterID {
					t.Fatal("refresh did not update reconnect binding")
				}
			}
		})
	}
}

func TestCurrentChannelSelectionRestoresSparseSentRoster(t *testing.T) {
	for _, kind := range []string{"same_flow", "other_flow", "other_account", "revoked", "stale_slot", "deleted", "out_of_range", "after_compaction"} {
		t.Run(kind, func(t *testing.T) {
			store := seedRosterIndexCharacters([]int{0, 1, 2, 3, 6, 7, 10}, false)
			group := gameDeleteCharacterGroup(store)
			s, source, _ := newGameDeleteCharacterService(group, true)
			source.accountID = rosterIndexTestAccount
			source.loginPacketSeen = true
			source.accountFromToken = true
			source.loginChannelFlowKey = [32]byte{1}
			rows, err := s.listCharacters(context.Background(), group, source.accountID)
			if err != nil {
				t.Fatal(err)
			}
			rememberRosterForTest(t, s, source, rows)
			dest := &gameSession{conn: &bufferConn{}, accountID: source.accountID, loginPacketSeen: true, accountFromToken: true, loginChannelFlowKey: source.loginChannelFlowKey, channelReconnect: true}
			index := 6
			switch kind {
			case "other_flow":
				dest.loginChannelFlowKey = [32]byte{2}
			case "other_account":
				dest.accountID = "other"
			case "revoked":
				dest.loginRevoked.Store(true)
			case "stale_slot":
				ch := store.records["210"]
				ch.Slot = 11
				store.records["210"] = ch
			case "deleted":
				delete(store.records, "210")
			case "out_of_range":
				index = 10
			case "after_compaction":
				delete(store.records, "201")
				s.removeCurrentSelectorRosterCharacter(source, "201")
				index = 5
			}
			id, _, ch, found, _ := s.resolveSelectedCharacter(context.Background(), dest, []byte{byte(index), 0})
			success := kind == "same_flow" || kind == "after_compaction"
			if found != success {
				t.Fatalf("found=%t id=%d", found, id)
			}
			if success && (id != 210 || ch.Slot != 10) {
				t.Fatalf("selected physical-slot decoy: %+v", ch)
			}
		})
	}
}

func TestCurrentDeleteFailedReplyDoesNotPublishCompactedChannelRoster(t *testing.T) {
	t.Setenv(characterDeleteAckEnv, "")
	store := seedRosterIndexCharacters([]int{0, 1, 2}, false)
	group := gameDeleteCharacterGroup(store)
	s, p, _ := newGameDeleteCharacterService(group, true)
	p.accountID = rosterIndexTestAccount
	p.loginPacketSeen, p.accountFromToken = true, true
	p.loginChannelFlowKey = [32]byte{1}
	rows, err := s.listCharacters(context.Background(), group, p.accountID)
	if err != nil {
		t.Fatal(err)
	}
	rememberRosterForTest(t, s, p, rows)
	p.conn = &firstChannelFailedWriteConn{}
	if err := s.handleGameDeleteCharacter(p, gameDeleteCharacterBody(1, "row1")); err == nil {
		t.Fatal("failed reply accepted")
	}
	if fakeCharacterDeleteFlag(store.records["201"]) != characterDeleteDeletedFlag {
		t.Fatal("delete did not reach persistence")
	}
	cached := s.cachedCurrentTagTournamentSentRoster(p, 1)
	if len(cached) != 3 || cached[1].CharacterID != "201" {
		t.Fatal("unacknowledged compaction published")
	}
	dest := &gameSession{conn: &bufferConn{}, accountID: p.accountID, loginPacketSeen: true, accountFromToken: true, loginChannelFlowKey: p.loginChannelFlowKey, channelReconnect: true}
	_, _, _, found, _ := s.resolveSelectedCharacter(context.Background(), dest, []byte{1, 0})
	if found {
		t.Fatal("old index selected a different survivor")
	}
}
