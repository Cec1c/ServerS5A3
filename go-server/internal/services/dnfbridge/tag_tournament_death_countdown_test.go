package dnfbridge

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// V195's 1466 success receiver starts the shared defeat countdown and sends
// op42 after 10 seconds. A single death must be answered by 1402 alone.
func TestTagDeathCountdownOnlyAfterFullDefeat(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, rootFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse=%t/rootFirst=%t", reverse, rootFirst), func(t *testing.T) {
				s, p, g := tagSecondaryReturnFixture(t, tagtournament.Normal, reverse)
				b := p.tagTournamentBattle
				if rootFirst {
					now := b.run.Snapshot().NextTag.Add(time.Second)
					b.now = func() time.Time { return now }
					owner, err := tagtournament.NewOwnerWithClock(s.tagTournamentCatalog, g, b.now)
					if err != nil {
						t.Fatal(err)
					}
					b.owner = owner
					if err := s.handleCurrentTagTournamentTag(p, []byte{0}); err != nil {
						t.Fatal(err)
					}
					if b.nativeRoleIDs[b.run.Snapshot().Active] != 0 {
						t.Fatal("fixture did not switch to root")
					}
				}
				type frame struct {
					Opcode uint16 `json:"opcode"`
					Class  byte   `json:"class"`
					Body   string `json:"body"`
				}
				wire := map[string][]frame{}
				dead := b.run.Snapshot().Active
				for _, stage := range []string{"single", "singleDuplicate", "allDead", "allDeadDuplicate"} {
					if stage == "allDead" {
						dead = b.run.Snapshot().Active
					}
					p.conn.(*bufferConn).write.Reset()
					if err := s.handleCurrentTagTournamentDeath(p, []byte{b.nativeRoleIDs[dead]}); err != nil {
						t.Fatal(err)
					}
					counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes())
					for rest := p.conn.(*bufferConn).write.Bytes(); len(rest) > 0; {
						packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
						rest = tail
						wire[stage] = append(wire[stage], frame{packet.Header.MsgID, byte(packet.Header.Classification), hex.EncodeToString(packet.Body)})
						if packet.Header.MsgID == 1466 && (!bytes.Equal(packet.Body, []byte{1}) || packet.Header.Classification != 1) {
							t.Fatal("wrong defeat reply", stage, packet)
						}
					}
					if stage == "single" || stage == "singleDuplicate" {
						if counts[1466] != 0 || b.run.Snapshot().Phase != tagtournament.Fighting || counts[24] != 0 {
							t.Fatal("survivor was declared defeated", stage, counts)
						}
						want := 0
						if stage == "single" {
							want = 1
						}
						if counts[1402] != want {
							t.Fatal("handoff count", stage, counts)
						}
					} else if counts[1466] != 1 || counts[1402] != 0 || b.run.Snapshot().Phase != tagtournament.Defeated {
						t.Fatal("full defeat did not retain countdown", stage, counts)
					}
				}
				if dir := os.Getenv("DNFBRIDGE_COUNTDOWN_WIRE"); dir != "" {
					data, err := json.MarshalIndent(wire, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("wire-%t-%t.json", reverse, rootFirst)), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				// Actual native timeout produces op42; its valid all-dead route remains.
				p.conn.(*bufferConn).write.Reset()
				if err := s.handleDungeonGiveupGame(p, nil); err != nil {
					t.Fatal(err)
				}
				if counts := tagActionPacketCounts(t, p.conn.(*bufferConn).write.Bytes()); counts[24] != 1 {
					t.Fatal("defeat timeout could not return", counts)
				}
			})
		}
	}
}
