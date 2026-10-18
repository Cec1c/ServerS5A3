package darkknightcombo

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	repomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestEncodeNotificationBodyOrdersNativePageBlocks(t *testing.T) {
	page0 := Page{Index: 0, Roots: []Root{{SkillID: 118, Children: []uint16{8, 77}}}}
	page1 := Page{Index: 1, Roots: []Root{{SkillID: 119, Children: []uint16{5}}}}
	got, err := EncodeNotificationBody([]Page{page1, page0})
	if err != nil {
		t.Fatal(err)
	}
	block0, _ := EncodePageBlock(page0)
	block1, _ := EncodePageBlock(page1)
	want := append([]byte{0, 2}, block0...)
	want = append(want, block1...)
	if !bytes.Equal(got, want) {
		t.Fatalf("notification=%x want=%x", got, want)
	}
}

func TestEncodePreloadNotificationBodyKeepsRosterSlotsAndPageBlocks(t *testing.T) {
	page0 := Page{Index: 0, Roots: []Root{{SkillID: 118, Children: []uint16{8}}}}
	page1 := Page{Index: 1, Roots: []Root{{SkillID: 119, Children: []uint16{5}}}}
	got, err := EncodePreloadNotificationBody([]PreloadEntry{{Slot: 3, Pages: []Page{page1, page0}}})
	if err != nil {
		t.Fatal(err)
	}
	block0, _ := EncodePageBlock(page0)
	block1, _ := EncodePageBlock(page1)
	want := append([]byte{1, 3, 0, 2}, block0...)
	want = append(want, block1...)
	if !bytes.Equal(got, want) {
		t.Fatalf("preload=%x want=%x", got, want)
	}
}

func TestNormalizePageKeepsLastChildOccurrence(t *testing.T) {
	page, err := NormalizePage(Page{Index: 0, Roots: []Root{
		{SkillID: 100, Children: []uint16{10, 11}},
		{SkillID: 101, Children: []uint16{11, 12, 10}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := Page{Index: 0, Roots: []Root{
		{SkillID: 100},
		{SkillID: 101, Children: []uint16{11, 12, 10}},
	}}
	if !reflect.DeepEqual(page, want) {
		t.Fatalf("page=%+v want=%+v", page, want)
	}
}

func TestNormalizePageRejectsMoreThanFiveComboStages(t *testing.T) {
	_, err := NormalizePage(Page{Index: 0, Roots: []Root{{
		SkillID:  118,
		Children: []uint16{1, 2, 3, 4, 5, 6},
	}}})
	if !errors.Is(err, ErrPageBlockInvalid) {
		t.Fatalf("error=%v want=%v", err, ErrPageBlockInvalid)
	}
}

func TestPagesRoundTripThroughSettingsRepository(t *testing.T) {
	repositories := repomemory.NewMemoryGroup()
	ctx := context.Background()
	pages := []Page{{Index: 0, Roots: []Root{{SkillID: 100, Children: []uint16{10, 11}}}}}
	if err := SavePages(ctx, repositories.Settings, "77", pages, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := LoadPages(ctx, repositories.Settings, "77")
	if err != nil || !found || !reflect.DeepEqual(loaded, pages) {
		t.Fatalf("loaded=%+v found=%t err=%v", loaded, found, err)
	}
	page1 := Page{Index: 1, Roots: []Root{{SkillID: 200, Children: []uint16{}}}}
	if err := SavePage(ctx, repositories.Settings, "77", page1, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	loaded, found, err = LoadPages(ctx, repositories.Settings, "77")
	if err != nil || !found || !reflect.DeepEqual(loaded, []Page{pages[0], page1}) {
		t.Fatalf("updated pages=%+v found=%t err=%v", loaded, found, err)
	}
}
