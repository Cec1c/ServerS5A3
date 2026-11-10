package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/protocol"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// The required native stage executes Begin and every writer and verifies these
// exact body fixtures. Begin's outer13B header is not inserted into the body.
func weddingGuestNativeBody(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/wedding_guest_native_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Name   string `json:"name"`
		Hex    string `json:"payload_hex"`
		Length int    `json:"length"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == name {
			body, err := hex.DecodeString(row.Hex)
			if err != nil || len(body) != row.Length {
				t.Fatal("invalid native fixture")
			}
			return body
		}
	}
	t.Fatalf("missing native fixture %s", name)
	return nil
}

// Traverse packet splitting, the real upper dispatcher and module registration;
// direct owner calls alone cannot prove that a native UI command reaches it.
func dispatchWeddingGuestPacket(t *testing.T, s *Service, p *gameSession, op uint16, body []byte, seq uint16) {
	t.Helper()
	wire, err := protocol.BuildChannelPacket(op, body, seq, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, rest, skipped, err := protocol.SplitLatestGameStream(wire, 65535)
	if err != nil || len(packets) != 1 || len(rest) != 0 || skipped != 0 {
		t.Fatalf("guest packet split %d/%d/%d/%v", len(packets), len(rest), skipped, err)
	}
	if err := s.handleGameStreamPacket(p, packets[0]); err != nil {
		t.Fatal(err)
	}
}

func TestWeddingGuestNativePaymentThroughRealDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		balance   int64
		expired   bool
		result    int32
	}{
		{"Chinese-wish", "785634121027000008000000d0c2bbe9bfecc0d6", 20000, false, 0},
		{"empty-wish", "785634121027000000000000", 20000, false, 0},
		{"insufficient-gold", "785634121027000000000000", 9999, false, -8},
		{"expired-letter", "785634121027000000000000", 20000, true, -4},
		{"invalid-text-length", "785634121027000001000000", 20000, false, -1},
		{"zero-run-key", "000000001027000000000000", 20000, false, -1},
		{"duplicated-transport-header", "00000000000000000000000000785634121027000000000000", 20000, false, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWeddingChurchLoadingFixture(t)
			s, ctx := f.s, context.Background()
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			t.Cleanup(func() {
				if t.Failed() {
					t.Log(logs.String())
				}
			})
			s.wedding.guestRules = &wedding.GuestRules{MinimumGold: 10000, MaximumGold: 400000000, Capacity: map[byte]int{0: 1}}
			index, _ := json.Marshal(wedding.GuestIndex{Version: 1, Next: 0x12345677, Runs: map[uint32]wedding.GuestRunReference{}})
			if err := f.repos.Settings.Save(ctx, r.SettingsRecord{Scope: r.WeddingGuestIndexScope, Values: map[string]string{"index": string(index)}}); err != nil {
				t.Fatal(err)
			}
			guest := addWeddingRandomGuest(t, f, 31, f.first.channel.ID)
			if err := f.repos.Skill.Save(ctx, r.SkillRecord{CharacterID: "31", Skills: map[int64]r.SkillState{46: {Level: 1, Enabled: true}}, Points: r.SkillPointState{TotalSP: 20, RemainingSP: 20, SyncedLevel: 90}}); err != nil {
				t.Fatal(err)
			}
			c, _, _ := f.repos.Character.Load(ctx, "31")
			c.Stats["gold"] = tc.balance
			if err := r.SaveCharacterFields(ctx, f.repos.Character, c, r.CharacterFieldStats); err != nil {
				t.Fatal(err)
			}
			q := make([]byte, 21)
			q[13] = 1
			dispatchWeddingGuestPacket(t, s, f.first, 1038, q, 1)
			letter := guildReply(t, guest, 1005)
			if len(letter) != 82 || binary.LittleEndian.Uint32(letter) != 0x12345678 || letter[77] != 0 {
				t.Fatalf("native invitation %x", letter)
			}
			if tc.expired {
				f.now = time.Unix(f.at, 0)
			}
			body, err := hex.DecodeString(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "Chinese-wish" {
				body = weddingGuestNativeBody(t, "gift-chinese")
			}
			if tc.name == "empty-wish" || tc.name == "insufficient-gold" || tc.name == "expired-letter" {
				body = weddingGuestNativeBody(t, "gift-empty")
			}
			dispatchWeddingGuestPacket(t, s, guest, 1034, body, 2)
			ack := guildReply(t, guest, 1034)
			length := 5
			if tc.result == 0 {
				length = 9
			}
			if len(ack) != length || ack[0] != 1 || int32(binary.LittleEndian.Uint32(ack[1:])) != tc.result {
				t.Fatalf("native gift result %x", ack)
			}
			stored, _, _ := f.repos.Character.Load(ctx, "31")
			wanted := tc.balance
			if tc.result == 0 {
				wanted -= 10000
			}
			if stored.Stats["gold"] != wanted {
				t.Fatalf("wallet %d wanted %d", stored.Stats["gold"], wanted)
			}
			owner, _ := s.currentWeddingOwner()
			ledger, _, err := owner.ReadGuestLedger(ctx, []string{"29", "30"}, f.at)
			if err != nil || ledger.Invitations["31"].Gold != tc.balance-wanted {
				t.Fatalf("gift ledger %+v/%v", ledger, err)
			}
			if tc.result != 0 {
				return
			}
			if binary.LittleEndian.Uint32(ack[5:]) != 0x12345678 {
				t.Fatal("paid-model ACK lost invitation key")
			}
			if err := s.sendInitialCurrentWeddingGuestLetters(guest); err != nil {
				t.Fatal(err)
			}
			if replay := guildReply(t, guest, 1005); replay[77] != 1 {
				t.Fatal("paid model was not replayed")
			}
			dispatchWeddingGuestPacket(t, s, guest, 1034, body, 3)
			if duplicate := guildReply(t, guest, 1034); len(duplicate) != 5 || int32(binary.LittleEndian.Uint32(duplicate[1:])) != -3 {
				t.Fatalf("duplicate %x", duplicate)
			}
			stored, _, _ = f.repos.Character.Load(ctx, "31")
			if stored.Stats["gold"] != wanted {
				t.Fatal("duplicate charged again")
			}
			// Seed only presence ownership; the request still traverses the real
			// teleport transaction and Church instance readback.
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 31, AccountID: guest.accountID, TownID: 39, AreaID: 0, Session: guest})
			entry := make([]byte, 22)
			binary.LittleEndian.PutUint32(entry[13:], 0x12345678)
			binary.LittleEndian.PutUint32(entry[17:], 8)
			entry[21] = 1
			dispatchWeddingGuestPacket(t, s, guest, 1039, entry, 4)
			if got := guildReply(t, guest, 1039); len(got) != 5 || int32(binary.LittleEndian.Uint32(got[1:])) != 0 {
				t.Fatalf("paid guest entry %x", got)
			}
		})
	}
}
