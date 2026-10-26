package dnfbridge

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// Exercise the production sender across subscriber identities. The optional
// export feeds the current EXE's 583 consumer; this test alone is wire evidence.
func TestRealPVFR190BarrierSubscriberLifecycle(t *testing.T) {
	f, session, now := realAntonProjectionFixture(t, 1)
	s := f.service
	b := f.state.Battle
	program, err := raid.LoadAntonProgram(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 4; n++ {
		next, replay, err := b.Anton.ApplyBarrierDamage(fmt.Sprintf("r190-open-%d", n), raid.AntonLargeTransmission, 218, b.Anton.Epoch(), now)
		if err != nil || replay {
			t.Fatalf("barrier fixture: %v/%t", err, replay)
		}
		b.Anton = next
	}
	b.Dungeons, b.ProgressVersion = b.Anton.Dungeons(), b.ProgressVersion+1
	type sample struct {
		Name     string   `json:"name"`
		Bodies   []string `json:"bodies"`
		WantOpen uint32   `json:"want_open"`
		HasOpen  bool     `json:"has_open"`
	}
	var samples []sample
	store := map[uint32]uint32{111: 1, 9999: 123}
	check := func(name string, resubscribe bool, want uint32, has bool) {
		t.Helper()
		f.conn.write.Reset()
		if err := s.sendCurrentRaidProgressProjection(session, cloneRuntimeRaidState(*f.state), false, resubscribe); err != nil {
			t.Fatal(err)
		}
		entry := sample{Name: name, WantOpen: want, HasOpen: has}
		resets := 0
		stream := append([]byte(nil), f.conn.write.Bytes()...)
		for len(stream) > 0 {
			packet, rest := splitGameServerUpperPacket(t, stream)
			stream = rest
			if packet.Header.MsgID != 583 {
				continue
			}
			entry.Bodies = append(entry.Bodies, hex.EncodeToString(packet.Body))
			for _, pair := range antonProjectionWrites(t, packet.Body) {
				if pair.ID == 0 && pair.Value == 0 {
					if len(entry.Bodies) != 1 || len(packet.Body) != 9 {
						t.Fatal("reset is not the first standalone symbol frame")
					}
					store = map[uint32]uint32{}
					resets++
				} else {
					store[pair.ID] = pair.Value
				}
			}
		}
		value, found := store[111]
		if resets != 1 || value != want || found != has || store[9999] != 0 {
			t.Fatalf("%s reset=%d 111=%d/%t want=%d/%t stale=%d", name, resets, value, found, want, has, store[9999])
		}
		samples = append(samples, entry)
	}
	check("open_restore", false, 1, true)
	check("same_instance_resubscribe", true, 1, true)
	retired := cloneRuntimeRaidState(*f.state)
	// A later raid uses a new domain instance, not the previous phase's state.
	progress, err := program.BeginPhase(0, now.Add(time.Second), 19001)
	if err != nil {
		t.Fatal(err)
	}
	b.InstanceID, b.Phase, b.Anton = "r190-new-instance", 0, progress
	b.Dungeons, b.ProgressVersion = progress.Dungeons(), b.ProgressVersion+1
	check("new_instance_phase0", false, 0, false)
	f.conn.write.Reset()
	if err := s.sendCurrentRaidProgressProjection(session, retired, false, true); err == nil || f.conn.write.Len() != 0 {
		t.Fatal("retired open instance wrote to current connection")
	}
	progress, err = program.BeginPhase(1, now.Add(2*time.Second), 19002)
	if err != nil {
		t.Fatal(err)
	}
	b.Phase, b.Anton = 1, progress
	b.Dungeons, b.ProgressVersion = progress.Dungeons(), b.ProgressVersion+1
	check("new_phase1_closed", false, 0, true)
	if dir := os.Getenv("DNF90_R190_NATIVE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(samples, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lifecycle-bodies.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
