package guild

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestGuildCargoHistorySnapshotsAndBoundsRecentView(t *testing.T) {
	c := &Cargo{Capacity: 3}
	e := CargoHistoryEntry{At: time.Unix(1700000000, 0), CharacterID: "1", CharacterName: "仓库会员", Kind: CargoHistoryDeposit,
		Item: r.ItemStack{ItemID: 123, Count: 3, RawEntry: []byte{1, 2}, Extra: map[string]string{"marker": "before"}}}
	if err := c.AppendHistory(e); err != nil {
		t.Fatal(err)
	}
	e.Item.RawEntry[0] = 9
	e.Item.Extra["marker"] = "after"
	if c.History[0].Item.RawEntry[0] != 1 || c.History[0].Item.Extra["marker"] != "before" {
		t.Fatal("history aliases caller item")
	}
	for i := 1; i < 53; i++ {
		e.Item.ItemID = int64(123 + i)
		if err := c.AppendHistory(e); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.History) != 50 || c.History[0].Item.ItemID != 126 || c.History[49].Item.ItemID != 175 {
		t.Fatal("recent insertion order/limit")
	}
	if err := ValidateCargo(c); err != nil {
		t.Fatal(err)
	}
}

func TestGuildCargoHistoryRejectsInvalidRecordsBeforeAppend(t *testing.T) {
	valid := CargoHistoryEntry{At: time.Unix(1700000000, 0), CharacterID: "1", CharacterName: "会员", Kind: CargoHistoryWithdraw, Item: r.ItemStack{ItemID: 123, Count: 1}}
	for _, name := range []string{"zero_time", "overflow_time", "zero_kind", "unknown_kind", "zero_id", "overflow_id", "zero_count", "overflow_count", "blank_name", "nul_name", "bad_utf8", "blank_character"} {
		t.Run(name, func(t *testing.T) {
			e := valid
			switch name {
			case "zero_time":
				e.At = time.Time{}
			case "overflow_time":
				e.At = time.Unix(math.MaxInt32+1, 0)
			case "zero_kind":
				e.Kind = 0
			case "unknown_kind":
				e.Kind = 3
			case "zero_id":
				e.Item.ItemID = 0
			case "overflow_id":
				e.Item.ItemID = math.MaxUint32 + 1
			case "zero_count":
				e.Item.Count = 0
			case "overflow_count":
				e.Item.Count = math.MaxInt32 + 1
			case "blank_name":
				e.CharacterName = " "
			case "nul_name":
				e.CharacterName = "会\x00员"
			case "bad_utf8":
				e.CharacterName = string([]byte{0xff})
			case "blank_character":
				e.CharacterID = ""
			}
			c := &Cargo{History: []CargoHistoryEntry{valid}}
			if err := c.AppendHistory(e); !errors.Is(err, ErrCargoHistory) || !reflect.DeepEqual(c.History, []CargoHistoryEntry{valid}) {
				t.Fatalf("append changed history: %v", err)
			}
			c.History = append(c.History, e)
			if !errors.Is(ValidateCargo(c), ErrInvalidRegistry) {
				t.Fatal("invalid persisted history accepted")
			}
		})
	}
	if err := (*Cargo)(nil).AppendHistory(valid); !errors.Is(err, ErrCargoUninitialized) {
		t.Fatal(err)
	}
	if err := ValidateCargo(&Cargo{History: make([]CargoHistoryEntry, 51)}); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatal(err)
	}
}
