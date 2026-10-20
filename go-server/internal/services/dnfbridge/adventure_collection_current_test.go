package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestCollectionSelectionOpcodeAndPackedRosterRegression(t *testing.T) {
	module := adventureGameplayModule()
	if module.LegacyHandlers[1474] == nil || module.UpperHandlers[1474] == nil {
		t.Fatal("current select opcode not registered")
	}
	characters := []dnfrepo.CharacterRecord{{CharacterID: "31", Job: "0", Name: "q7a", Level: 90, Stats: map[string]int64{"grow_type": 0x21}}, {CharacterID: "32", Job: "0", Name: "low", Level: 40, Stats: map[string]int64{"grow_type": 0x22}}}
	body := buildCurrentAdventureInfoBody(31, adventuregroup.Summary{}, characters)
	entry := body[18+currentAdventureInfoRosterOffset : 18+currentAdventureInfoRosterOffset+currentAdventureInfoRosterEntrySize]
	if entry[0] != 0 || entry[1] != 1 || entry[12] != 0 || entry[42] != 1 || binary.LittleEndian.Uint32(entry[48:]) != 90 {
		t.Fatalf("unselected SwordMaster row=%x", entry)
	}
	if binary.LittleEndian.Uint16(body[18+56:]) != 2 {
		t.Fatal("collection filter changed account character count")
	}
}

// BEB447/BEB460 restore a saved card only when its group and honor match
// the fresh op1473 row. Character level is a separate u32 at raw+48.
func TestCollectionSavedCardMatchesLookupAfterReload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source byte
		stored int64
		want   byte
	}{
		{"default source", 1, 0, 1},
		{"configured source", 7, 0, 7},
		{"explicit character group", 7, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dnfrepo.CharacterRecord{CharacterID: "31", Job: "0", Name: "q7a", Level: 90, Stats: map[string]int64{"grow_type": 0x21, "server_group_id": tc.stored, currentHonorExpertLevelStatKey: 7}}
			body := buildCurrentAdventureInfoBodyWithState(31, adventuregroup.Summary{}, []dnfrepo.CharacterRecord{c}, "", 0, adventuregroup.Projection{CollectionSourceServerGroup: tc.source, CollectionSelections: map[string]string{"0_1": "31"}})
			card := body[18+currentAdventureInfoRosterOffset : 18+currentAdventureInfoRosterOffset+52]
			lookup := buildCurrentAdventureRepresentCharactersBodyForServer(0, 1, []dnfrepo.CharacterRecord{c}, tc.source)
			if len(lookup) != 55 || lookup[0] != 1 || lookup[1] != tc.want || lookup[2] != 1 {
				t.Fatalf("lookup header=%x", lookup)
			}
			row := lookup[3:]
			if card[42] != tc.want || row[0] != card[42] || binary.LittleEndian.Uint32(card[4:]) != 7 || binary.LittleEndian.Uint32(row[4:]) != 7 || binary.LittleEndian.Uint32(card[48:]) != 90 || binary.LittleEndian.Uint32(row[8:]) != 90 {
				t.Fatalf("saved card and lookup disagree: card=%x lookup=%x", card, row)
			}
		})
	}
}

// Export real Go packets for executing the authoritative native readers. This
// explicit PVF run also checks save/reload and rejects an account peer's row.
func TestCurrentCollectionSelectWithRealPVFAndNativeFixtures(t *testing.T) {
	pvfPath := os.Getenv("YKCN90_COLLECTION_PVF")
	if pvfPath == "" {
		t.Skip("explicit published-PVF integration run")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	tables, err := adventuregroup.LoadComplete(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if group, ok := tables.Runtime().CollectionGroupFor(11, 1); !ok || group != 1 {
		t.Fatal("published PVF female group does not match native catalog")
	}
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	if err := repos.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:10029", Metadata: map[string]string{"unrelated": "preserve"}}); err != nil {
		t.Fatal(err)
	}
	characters := []dnfrepo.CharacterRecord{{CharacterID: "31", AccountID: "dnf:10029", Job: "0", Name: "q7a", Level: 90, Stats: map[string]int64{"grow_type": 0x21, adventuregroup.RecommendedDungeonClearStatKey: 7}}, {CharacterID: "32", AccountID: "dnf:peer", Job: "0", Name: "peer", Level: 90, Stats: map[string]int64{"grow_type": 0x21}}}
	for _, c := range characters {
		if err := repos.Character.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	conn := &bufferConn{}
	service := &Service{options: options{accountID: "fallback", currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain}, adventureGroupTable: tables, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{conn: conn, accountID: "dnf:10029", accountFromToken: true, loginPacketSeen: true, selectedCharacterID: 31}
	request := []byte{1, 31, 0, 0, 0, 0, 0x21}
	if err := service.handleCurrentAdventureSelectRepresentative(session, request); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 1474 || len(ack.Body) != 64 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:]) != 3 || ack.Body[6] != 1 || binary.LittleEndian.Uint32(ack.Body[8:]) != 52 || ack.Body[13] != 1 {
		t.Fatalf("select ACK=%x", ack.Body)
	}
	refresh, remainder := splitLongHengGameServerUpperPacket(t, rest)
	if len(remainder) != 0 || refresh.Header.MsgID != currentAdventureInfoPushMsgID || refresh.Header.Classification != 0 || len(refresh.Body) != 18+currentAdventureInfoRawLength+4 {
		t.Fatalf("committed selection refresh header=%+v body_len=%d trailing=%d", refresh.Header, len(refresh.Body), len(remainder))
	}
	if refresh.Body[18+currentAdventureInfoBestCollectionJobOffset] != 0 || refresh.Body[18+currentAdventureInfoBestCollectionGrowOffset] != 1 || binary.LittleEndian.Uint32(refresh.Body[18+currentAdventureInfoBestCollectionHonorOffset:]) != 0 || refresh.Body[18+currentAdventureInfoRosterOffset+1] != 1 {
		t.Fatal("selection refresh lost the genuinely selected zero-proficiency representative")
	}
	account, _, err := repos.Account.Load(ctx, "dnf:10029")
	if err != nil {
		t.Fatal(err)
	}
	if adventuregroup.CollectionSelections(account)["0_1"] != "31" || account.Metadata["unrelated"] != "preserve" {
		t.Fatal("selected representative was not saved")
	}
	state, err := service.currentAccountAdventureGroupInfoState(ctx, characters[0], true, session)
	if err != nil {
		t.Fatal(err)
	}
	selected := buildCurrentAdventureInfoBodyWithState(31, state.Summary, state.Characters, "", 0, state.Projection)
	if selected[18+60+1] != 1 || string(selected[18+60+12:18+60+15]) != "q7a" || selected[18+7348+1] != 1 {
		t.Fatal("reload lost base branch, name or count")
	}
	if state.Projection.ContentCounts[0] != 7 || state.Projection.CollectionCounts[0] != 1 {
		t.Fatal("collection projection overwrote durable dungeon activity")
	}
	for _, bad := range [][]byte{{1, 32, 0, 0, 0, 0, 0x21}, {1, 31, 0, 0, 0, 0, 0x22}, {1, 31, 0, 0, 0, 0, 0x11}, {0}} {
		conn.write.Reset()
		if err := service.handleCurrentAdventureSelectRepresentative(session, bad); err != nil {
			t.Fatal(err)
		}
		failure, unexpectedRefresh := splitLongHengGameServerUpperPacket(t, conn.write.Bytes())
		if len(failure.Body) == 0 || failure.Body[0] != 0 || len(unexpectedRefresh) != 0 {
			t.Fatal("invalid representative accepted")
		}
	}
	if dir := os.Getenv("YKCN90_COLLECTION_FIXTURES"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		fixtures := map[string][]byte{"select-request.bin": request, "select-ack.bin": ack.Body[1:], "info-selected.bin": selected, "selection-refresh.bin": refresh.Body, "info-unselected.bin": buildCurrentAdventureInfoBody(31, state.Summary, state.Characters), "lookup.bin": buildCurrentAdventureRepresentCharactersBody(0, 1, state.Characters)}
		for name, data := range fixtures {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
