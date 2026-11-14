package mysql

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/repository"
)

func TestInventoryItemProjectionUsesOneScopedViewAndPreservesSnapshot(t *testing.T) {
	raw := []byte{0, 1, 255}
	f := &fakeSQLDB{rowsets: []fakeSQLRows{{rows: []fakeSQLRow{
		{values: []any{"auction:history:1:0", int64(6001), int64(2), false, nil, raw, "seller_id", "10"}},
		{values: []any{"auction:history:1:0", int64(6001), int64(2), false, nil, raw, "buyer_id", "20"}},
		{values: []any{"auction:history:2:0", int64(6001), int64(1), true, nil, nil, nil, nil}},
	}}}}
	before := &repository.InventoryRecord{CharacterID: "other", Slots: map[string]repository.ItemStack{"keep": {ItemID: 7}}}
	s := &mysqlBagStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: f, readDBs: []string{"dnf"}, tablePrefix: "dnf"}}, trackSlotChanges: true, loadedSnapshot: before}
	got, err := s.LoadInventorySlotItemPrefix(context.Background(), "escrow", "auction:history:", 6001)
	if err != nil || len(got) != 2 || got["auction:history:1:0"].Extra["buyer_id"] != "20" || got["auction:history:1:0"].Extra["seller_id"] != "10" || !got["auction:history:2:0"].Bind {
		t.Fatalf("projection=%+v err=%v", got, err)
	}
	if s.loadedSnapshot != before || len(f.execs) != 1 {
		t.Fatal("partial view replaced a before-image or performed extra queries")
	}
	raw[0] = 99
	if got["auction:history:1:0"].RawEntry[0] != 0 {
		t.Fatal("projection retained driver-owned bytes")
	}
	call := f.execs[0]
	if !reflect.DeepEqual(call.Args, []any{"escrow", "slots", int64(6001), "auction:history:%"}) {
		t.Fatalf("args=%v", call.Args)
	}
	for _, want := range []string{"INNER JOIN `dnf`.`dnf_inventories`", "e.character_id = i.character_id", "e.collection_name = i.collection_name", "e.entry_key = i.entry_key", "i.item_id = ?", "i.entry_key LIKE ? ESCAPE '!'"} {
		if !strings.Contains(call.Query, want) {
			t.Fatalf("query missing %s: %s", want, call.Query)
		}
	}
}

func TestInventoryItemProjectionRejectsInvalidAndEscapesLiteralPrefix(t *testing.T) {
	f := &fakeSQLDB{}
	s := &mysqlBagStore{mysqlStoreBase: mysqlStoreBase{router: mysqlRouter{db: f, readDBs: []string{"dnf"}, tablePrefix: "dnf"}}, trackSlotChanges: true}
	for _, v := range []struct {
		id, prefix string
		item       int64
	}{{"", "x", 1}, {"owner", "", 1}, {"owner", "x", 0}, {"owner", "x", -1}} {
		if _, err := s.LoadInventorySlotItemPrefix(context.Background(), v.id, v.prefix, v.item); err == nil {
			t.Fatal("invalid projection accepted", v)
		}
	}
	if len(f.execs) != 0 {
		t.Fatal("invalid request queried storage")
	}
	got, err := s.LoadInventorySlotItemPrefix(context.Background(), "missing", "x!_%", 7)
	if err != nil || len(got) != 0 || s.loadedSnapshot != nil || f.execs[0].Args[3] != "x!!!_!%%" {
		t.Fatalf("empty/escaped projection=%v %v calls=%+v", got, err, f.execs)
	}
	failure := errors.New("read failed")
	f.rowsets = []fakeSQLRows{{err: failure}}
	if _, err := s.LoadInventorySlotItemPrefix(context.Background(), "owner", "x", 7); !errors.Is(err, failure) {
		t.Fatalf("failure lost: %v", err)
	}
}
