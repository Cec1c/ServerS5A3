package guild

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestGuildSearchModesAndTagIntersection(t *testing.T) {
	r := NewRegistry()
	r.Guilds[1] = &Guild{ID: 1, Name: "Alpha公会", MasterName: "其他会长", Level: 4, Tags: []byte{0, 2, 6}}
	r.Guilds[2] = &Guild{ID: 2, Name: "其他公会", MasterName: "Alpha会长", Level: 5, Tags: []byte{0, 1}}
	r.Guilds[3] = &Guild{ID: 3, Name: "第二公会", MasterName: "其他会长", Level: 4, Tags: []byte{2, 6}}
	r.Guilds[4] = &Guild{ID: 4, Name: "无标签会", MasterName: "其他会长", Level: 1}
	before, _ := json.Marshal(r)
	for _, tc := range []struct {
		name   string
		filter SearchFilter
		page   int
		size   int
		total  int
		ids    []uint32
	}{
		{"guild name only", SearchFilter{Mode: SearchGuildName, Query: " aLPha "}, 0, 30, 1, []uint32{1}},
		{"master name only", SearchFilter{Mode: SearchMasterName, Query: "ALPHA"}, 0, 30, 1, []uint32{2}},
		{"empty text", SearchFilter{Mode: SearchGuildName}, 0, 30, 4, []uint32{2, 1, 3, 4}},
		{"no tags", SearchFilter{Mode: SearchTags}, 0, 30, 4, []uint32{2, 1, 3, 4}},
		{"one tag", SearchFilter{Mode: SearchTags, TagMask: 1}, 0, 30, 2, []uint32{2, 1}},
		{"two tags require both", SearchFilter{Mode: SearchTags, TagMask: 5}, 0, 30, 1, []uint32{1}},
		{"three tags", SearchFilter{Mode: SearchTags, TagMask: 0x45}, 0, 30, 1, []uint32{1}},
		{"filter before page", SearchFilter{Mode: SearchTags, TagMask: 0x44}, 1, 1, 2, []uint32{3}},
		{"unmatched", SearchFilter{Mode: SearchTags, TagMask: 8}, 0, 30, 0, []uint32{}},
		{"out of range", SearchFilter{Mode: SearchTags, TagMask: 1}, math.MaxInt, 30, 2, []uint32{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, rows, err := r.SearchDirectory(tc.filter, tc.page, tc.size)
			ids := []uint32{}
			for _, g := range rows {
				ids = append(ids, g.ID)
			}
			if err != nil || total != tc.total || !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("got %d/%v/%v want %d/%v", total, ids, err, tc.total, tc.ids)
			}
		})
	}
	after, _ := json.Marshal(r)
	if string(before) != string(after) {
		t.Fatal("search changed persisted state")
	}
	// The older combined lookup remains unchanged for recommendation/other views.
	if got := r.Search("alpha", 0, 30); len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 {
		t.Fatal("legacy combined search changed")
	}
}

func TestGuildSearchRejectsInvalidFilters(t *testing.T) {
	r := NewRegistry()
	for _, tc := range []struct {
		filter SearchFilter
		page   int
		size   int
	}{
		{SearchFilter{Mode: 3}, 0, 30},
		{SearchFilter{Mode: SearchTags, TagMask: 0x80}, 0, 30},
		{SearchFilter{Mode: SearchTags, TagMask: 15}, 0, 30},
		{SearchFilter{Mode: SearchTags, TagMask: 0x80000000}, 0, 30},
		{SearchFilter{Mode: SearchTags}, -1, 30},
		{SearchFilter{Mode: SearchTags}, 0, 0},
		{SearchFilter{Mode: SearchGuildName, Query: "a\x00b"}, 0, 30},
		{SearchFilter{Mode: SearchGuildName, Query: "\n"}, 0, 30},
		{SearchFilter{Mode: SearchMasterName, Query: "\t"}, 0, 30},
		{SearchFilter{Mode: SearchMasterName, Query: "\xff"}, 0, 30},
		{SearchFilter{Mode: SearchGuildName, Query: strings.Repeat("中", 15)}, 0, 30},
		{SearchFilter{Mode: SearchGuildName, Query: strings.Repeat("𠀀", 8)}, 0, 30},
	} {
		if _, _, err := r.SearchDirectory(tc.filter, tc.page, tc.size); !errors.Is(err, ErrInvalidSearch) {
			t.Fatalf("accepted %+v: %v", tc, err)
		}
	}
	for _, text := range []string{"", " ", strings.Repeat("中", 14), strings.Repeat("𠀀", 7)} {
		if total, rows, err := r.SearchDirectory(SearchFilter{Mode: SearchGuildName, Query: text}, 0, 30); err != nil || total != 0 || len(rows) != 0 {
			t.Fatalf("empty registry / valid boundary %q: %d/%v/%v", text, total, rows, err)
		}
	}
}
