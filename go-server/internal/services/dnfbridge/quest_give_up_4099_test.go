package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFQuest4099GiveUpRecovery(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching real PVF required")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), a, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	const id uint16 = 4099
	for _, count := range []int64{0, 5, 12, 36} {
		t.Run(strconv.Itoa(int(count)), func(t *testing.T) {
			r := dnfrepomemory.NewMemoryGroup()
			ctx := context.Background()
			if err := r.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Level: 90}); err != nil {
				t.Fatal(err)
			}
			if err := r.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "19", States: map[int64]dnfrepo.QuestState{int64(id): {Status: "active", ProgressValue: 1}, 999: {Status: "completed"}}, Progress: map[int64]dnfrepo.QuestState{int64(id): {Status: "active", ProgressValue: 1}}}); err != nil {
				t.Fatal(err)
			}
			bag := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:121": {ItemID: 10092844, Count: 35}}}
			if count > 0 {
				bag.Slots["0:161"] = dnfrepo.ItemStack{ItemID: 4730, Count: count}
			}
			if err := r.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			c := &bufferConn{}
			s := &Service{options: options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain}, questCatalog: catalog, pvfItemCatalog: items, repositoryProvider: func() (dnfrepo.Group, bool) { return r, true }}
			se := &gameSession{conn: c, selectedCharacterID: 19}
			body := make([]byte, 4)
			binary.LittleEndian.PutUint16(body, 32)
			binary.LittleEndian.PutUint16(body[2:], id)
			if err := s.handleGameCommand(se, byte(dnfenum.GameCmdCommand), 32, body); err != nil {
				t.Fatal(err)
			}
			if c.write.Len() == 0 {
				t.Fatal("legacy give-up produced no response")
			}
			ack, rest := splitGameServerUpperPacket(t, c.write.Bytes())
			if count > 0 {
				var deleted currentItemListEntry
				deleted.patchCore(161, ^uint32(0), 0)
				if ack.Header.MsgID != 14 || !reflect.DeepEqual(ack.Body, buildCurrentItemUpdateBody(0, []currentItemListEntry{deleted})) {
					t.Fatalf("recovery update=%+v", ack)
				}
				ack, rest = splitGameServerUpperPacket(t, rest)
			}
			if len(rest) != 0 || ack.Header.MsgID != 32 || len(ack.Body) != 3 || ack.Body[0] != 1 || binary.LittleEndian.Uint16(ack.Body[1:]) != id {
				t.Fatalf("give-up ack=%x", ack.Body)
			}
			stored, _, _ := r.Quest.Load(ctx, "19")
			if _, ok := stored.States[int64(id)]; ok {
				t.Fatal("quest remains active")
			}
			if _, ok := stored.Progress[int64(id)]; ok {
				t.Fatal("legacy progress remains active")
			}
			if stored.States[999].Status != "completed" {
				t.Fatal("unrelated quest changed")
			}
			after, _, _ := r.Inventory.Load(ctx, "19")
			delete(bag.Slots, "0:161")
			if !reflect.DeepEqual(after, bag) {
				t.Fatal("ordinary materials were reclaimed")
			}
			c.write.Reset()
			if err := s.handleGameCommand(se, byte(dnfenum.GameCmdCommand), 32, body); err != nil {
				t.Fatal(err)
			}
			if c.write.Len() != 0 {
				t.Fatal("duplicate give-up emitted unsafe second ACK")
			}
		})
	}
}
