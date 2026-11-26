package main

import (
	"os"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestEnsureDistinctRejectsAlias(t *testing.T) {
	dir := t.TempDir()
	path := dir + string(os.PathSeparator) + "candidate.pvf"
	if err := ensureDistinct(map[string]string{"input": path, "output": path}); err == nil {
		t.Fatal("expected input/output alias rejection")
	}
}

func TestScriptTokensEqual(t *testing.T) {
	if !scriptTokensEqual(nil, nil) {
		t.Fatal("nil streams should match")
	}
}

func TestBuildUpdatedBoxPreservesNonItemMetadata(t *testing.T) {
	orig := []platformpvf.ScriptToken{
		{Type: 3, Text: "[name]"}, {Type: 6, Text: "box"},
		{Type: 3, Text: "[explain]"}, {Type: 6, Text: "old explain"},
		{Type: 3, Text: "[attach type]"}, {Type: 6, Text: "[free]"},
		{Type: 3, Text: "[available auction]"}, {Type: 0, Value: 1},
		{Type: 3, Text: "[stackable]"},
		{Type: 0, Value: 10007847}, {Type: 0, Value: 1},
		{Type: 0, Value: 10007848}, {Type: 0, Value: 1},
		{Type: 0, Value: 10007849}, {Type: 0, Value: 1},
		{Type: 3, Text: "[/stackable]"},
		{Type: 3, Text: "[price]"}, {Type: 0, Value: 123},
	}
	items := []int32{10007847, 10007848, 990032002, 10007849, 10007850, 10008906, 10008907, 10008908, 10008909, 990032003, 10007246, 990032004, 990032005, 990032006}
	got, err := buildUpdatedBox990032001Tokens(orig, items)
	if err != nil {
		t.Fatal(err)
	}
	find := func(tag string) int {
		for i, token := range got {
			if token.Type == 3 && token.Text == tag {
				return i
			}
		}
		return -1
	}
	explain := find("[explain]")
	if explain < 0 || explain+1 >= len(got) || got[explain+1].Text != updatedBox990032001Explain {
		t.Fatalf("explain not updated: %+v", got)
	}
	if attach := find("[attach type]"); attach < 0 || attach+1 >= len(got) || got[attach+1] != (platformpvf.ScriptToken{Type: 6, Text: "[free]"}) {
		t.Fatalf("attach/free metadata lost")
	}
	if auction := find("[available auction]"); auction < 0 || auction+1 >= len(got) || got[auction+1].Value != 1 {
		t.Fatalf("auction metadata lost")
	}
	if price := find("[price]"); price < 0 || price+1 >= len(got) || got[price+1].Value != 123 {
		t.Fatalf("unrelated price metadata lost")
	}
	stack := find("[stackable]")
	if stack < 0 {
		t.Fatal("stackable section missing")
	}
	ids := make([]int32, 0, len(items))
	for i := stack + 1; i < len(got); {
		if got[i].Type == 3 && got[i].Text == "[/stackable]" {
			break
		}
		if got[i].Type != 0 || i+1 >= len(got) || got[i+1].Type != 0 || got[i+1].Value != 1 {
			t.Fatalf("bad stackable entry at %d: %+v", i, got[i:])
		}
		ids = append(ids, got[i].Value)
		i += 2
	}
	if len(ids) != len(items) {
		t.Fatalf("stackable count=%d want=%d", len(ids), len(items))
	}
	for i := range items {
		if ids[i] != items[i] {
			t.Fatalf("stackable order[%d]=%d want=%d", i, ids[i], items[i])
		}
	}
}
