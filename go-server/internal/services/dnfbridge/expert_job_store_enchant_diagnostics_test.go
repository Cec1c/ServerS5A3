package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func TestEnchant601EarlyRejectDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name         string
		owner        uint16
		mutate       func([]byte) []byte
		areaMismatch bool
		parseFailure bool
	}{
		{name: "invalid_length", owner: 20, parseFailure: true, mutate: func([]byte) []byte { return []byte("private-payload-marker") }},
		{name: "invalid_values", owner: 20, parseFailure: true, mutate: func(body []byte) []byte { body[6] = 3; return body }},
		{name: "self_without_town_registration", owner: 19},
		{name: "other_without_entered_store", owner: 20},
		{name: "existing_store_wrong_area", owner: 20, areaMismatch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "packet.log")
			logger, err := openPacketLogger(logPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = logger.Close() })
			conn := &bufferConn{}
			session := &gameSession{conn: conn, connID: "enchant-diagnostics-test", selectedCharacterID: 19, accountID: "private-account-marker"}
			service := &Service{packetLog: logger, onlinePlayers: newOnlinePlayerManager(), repositoryProvider: func() (dnfrepo.Group, bool) {
				t.Fatal("early rejection accessed repository")
				return dnfrepo.Group{}, false
			}}
			service.options.gameUpperHeader = gameUpperHeaderChannel13
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: session, TownID: 2, AreaID: 5})
			if test.name == "self_without_town_registration" {
				service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 19, Session: session, TownID: 0, AreaID: 5})
			}
			if test.areaMismatch {
				service.expertJobStores = map[uint16]*currentExpertJobStore{test.owner: {
					Kind: currentExpertJobEnchantStoreKind, OwnerCharacterID: test.owner,
					OwnerSession: &gameSession{selectedCharacterID: test.owner}, TownID: 2, AreaID: 6,
				}}
				service.expertJobVisitors = map[*gameSession]uint16{session: test.owner}
				service.expertJobStores[test.owner].AreaID = 5
				if _, ok := service.currentExpertJobEnteredStore(session, test.owner, currentExpertJobEnchantStoreKind); !ok {
					t.Fatal("matching-area fixture must pass entered-store validation")
				}
				service.expertJobStores[test.owner].AreaID = 6
			}
			body := make([]byte, 13)
			binary.LittleEndian.PutUint16(body[0:2], test.owner)
			binary.LittleEndian.PutUint32(body[2:6], 1)
			body[6] = 2
			binary.LittleEndian.PutUint16(body[8:10], 28)
			binary.LittleEndian.PutUint16(body[11:13], 237)
			if test.mutate != nil {
				body = test.mutate(body)
			}
			if err := service.handleCurrentExpertJobStoreEnchant(session, 601, body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 601 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{0, 19}) || len(rest) != 0 {
				t.Fatalf("early rejection changed: header=%+v body=%x extra=%x", ack.Header, ack.Body, rest)
			}
			if !service.expertJobStoreOpMu.TryLock() {
				t.Fatal("early rejection retained operation lock")
			}
			service.expertJobStoreOpMu.Unlock()
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic string
			count := 0
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, "kind=game-expert-job-store-enchant-blocked") {
					diagnostic = line
					count++
				}
			}
			if count != 1 {
				t.Fatalf("want one enchant rejection diagnostic, got %d\n%s", count, raw)
			}
			want := []string{"conn_id=enchant-diagnostics-test", "msg_id=601", "requester_id=19"}
			if test.parseFailure {
				want = append(want, "reason=invalid_request", "body_len=", "error=")
				if strings.Contains(diagnostic, "owner_id=") {
					t.Fatal("parsed owner logged for invalid request")
				}
			} else {
				ownerField := "owner_id=20"
				if test.owner == 19 {
					ownerField = "owner_id=19"
				}
				want = append(want, "reason=entered_store_context_invalid", ownerField, "in_dungeon=false")
			}
			for _, field := range want {
				if !strings.Contains(diagnostic, field) {
					t.Fatalf("missing %q: %s", field, diagnostic)
				}
			}
			for _, forbidden := range []string{"private-payload-marker", "private-account-marker", "account_id=", "body=", "recipe_id=", "target_slot=", "card_slot="} {
				if strings.Contains(diagnostic, forbidden) {
					t.Fatalf("unexpected diagnostic data %q: %s", forbidden, diagnostic)
				}
			}
			t.Log(diagnostic)
		})
	}
}
