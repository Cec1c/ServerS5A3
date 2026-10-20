package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestCurrentBoosterFailuresReportActualReasonWithoutMutation(t *testing.T) {
	catalog := mustCurrentBoosterTestCatalog(t)
	for _, name := range []string{"malformed", "missing", "forged", "full"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
				t.Fatal(err)
			}
			slots := map[string]dnfrepo.ItemStack{"0:75": {ItemID: 500, Count: 2}, "0:76": {ItemID: 501, Count: 1}}
			body := []byte{75, 0}
			want := byte(23)
			switch name {
			case "malformed":
				body = []byte{75}
			case "missing":
				body = []byte{74, 0}
				want = 17
			case "forged":
				body = make([]byte, 10)
				binary.LittleEndian.PutUint16(body, 76)
				binary.LittleEndian.PutUint32(body[4:], 701)
			case "full":
				want = 4
				for slot := 0; slot <= 1023; slot++ {
					slots["0:"+strconv.Itoa(slot)] = dnfrepo.ItemStack{ItemID: 601, Count: 999}
				}
				// The retained source stack cannot free a slot during this use.
				slots["0:75"] = dnfrepo.ItemStack{ItemID: 500, Count: 2}
			}
			before := dnfrepo.InventoryRecord{CharacterID: "19", Slots: slots}
			if err := repos.Inventory.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			s := currentBoosterTestService(catalog, repos)
			s.options.currentClientVersion = "official-24.9"
			if err := s.handleCurrentBoosterItem(&gameSession{conn: conn, selectedCharacterID: 19}, body); err != nil {
				t.Fatal(err)
			}
			packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
			if len(packets) != 1 || !reflect.DeepEqual(packets[0].Body, []byte{0, want}) {
				t.Fatalf("ack=%+v want failure=%d", packets, want)
			}
			after, found, err := repos.Inventory.Load(ctx, "19")
			if err != nil || !found || !reflect.DeepEqual(after.Slots, before.Slots) {
				t.Fatalf("rejection mutated inventory: %+v %v", after, err)
			}
			if dir := os.Getenv("DNF_BOOSTER_FAILURE_NATIVE_EXPORT"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name+".bin"), packets[0].Body, 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
