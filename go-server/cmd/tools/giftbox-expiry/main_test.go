package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	pvf "longheng.io/server/internal/platform/pvf"
)

func tag(text string) pvf.ScriptToken { return pvf.ScriptToken{Type: 3, Text: text} }
func str(text string) pvf.ScriptToken { return pvf.ScriptToken{Type: 6, Text: text} }

func TestRewriteGiftBoxDateScopeAndExactTokens(t *testing.T) {
	for _, kind := range []string{"[booster]", "[booster selection]", "[cera booster]", "[cera package]", "[usable cera package]", "[booster random]", "[random upgradable legacy]", "[upgradable legacy]"} {
		t.Run(kind, func(t *testing.T) {
			input := []pvf.ScriptToken{tag("[stackable type]"), str(kind), {Type: 0, Value: 0}, tag("[explain]"), str("Opens after 2018-07-19 06:00:00; reward lasts 200 days"), tag("[expiration date]"), str("2018-07-19 06:00:00"), tag("[package data]"), {Type: 0, Value: 490008481}, {Type: 2, Value: 1065353216}, tag("[/package data]")}
			before := append([]pvf.ScriptToken(nil), input...)
			got, edits, _, err := rewrite("box.stk", input)
			if err != nil || len(edits) != 1 {
				t.Fatalf("edits=%+v err=%v", edits, err)
			}
			want := append([]pvf.ScriptToken(nil), input...)
			want[6].Text = deadline
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(input, before) {
				t.Fatal("changed non-date tokens or mutated input")
			}
			replay, again, _, err := rewrite("box.stk", got)
			if err != nil || len(again) != 0 || !reflect.DeepEqual(replay, got) {
				t.Fatal("not idempotent")
			}
		})
	}
}

// Independently compare every changed raw stream after a real repack. Numeric
// bit patterns and string meanings outside the exact date value must match.
func TestRealGiftBoxExpiryRound16(t *testing.T) {
	source, candidate, receipt := os.Getenv("DNF_GIFTBOX_EXPIRY_SOURCE"), os.Getenv("DNF_GIFTBOX_EXPIRY_CANDIDATE"), os.Getenv("DNF_GIFTBOX_EXPIRY_RECEIPT")
	if source == "" || candidate == "" || receipt == "" {
		t.Skip("set all DNF_GIFTBOX_EXPIRY_* fixture paths")
	}
	a, err := pvf.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pvf.Open(candidate)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Changes       []change `json:"changes"`
		FilesChanged  int      `json:"files_changed"`
		TokensChanged int      `json:"date_tokens_changed"`
		HistoryPath   string   `json:"history_manifest_path"`
	}
	if err := json.Unmarshal(blob, &audit); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]map[int]change{}
	for _, c := range audit.Changes {
		if allowed[c.Path] == nil {
			allowed[c.Path] = map[int]change{}
		}
		if _, duplicate := allowed[c.Path][c.Token]; duplicate {
			t.Fatal("duplicate change")
		}
		allowed[c.Path][c.Token] = c
	}
	paths := make([]string, 0, len(allowed))
	verified := 0
	for path, edits := range allowed {
		paths = append(paths, path)
		old, err := a.ReadRaw(path)
		if err != nil {
			t.Fatal(err)
		}
		next, err := b.ReadRaw(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(old) != len(next) || len(old)%5 != 0 {
			t.Fatalf("%s token count changed", path)
		}
		for pos := 0; pos < len(old); pos += 5 {
			if old[pos] != next[pos] {
				t.Fatalf("%s token kind changed at %d", path, pos/5)
			}
			ov, nv := int32(binary.LittleEndian.Uint32(old[pos+1:pos+5])), int32(binary.LittleEndian.Uint32(next[pos+1:pos+5]))
			if edit, ok := edits[pos/5]; ok {
				if pos < 5 || old[pos-5] != 3 || a.ResolveString(int(int32(binary.LittleEndian.Uint32(old[pos-4:pos])))) != "[expiration date]" || old[pos] != 6 || a.ResolveString(int(ov)) != edit.Old || b.ResolveString(int(nv)) != deadline {
					t.Fatalf("%s unproved date edit %d", path, pos/5)
				}
				verified++
				continue
			}
			if old[pos] == 0 || old[pos] == 2 {
				if ov != nv {
					t.Fatalf("%s numeric bits changed", path)
				}
			} else if a.ResolveString(int(ov)) != b.ResolveString(int(nv)) {
				t.Fatalf("%s non-date string changed at %d", path, pos/5)
			}
		}
	}
	if len(paths) != audit.FilesChanged || verified != audit.TokensChanged || verified == 0 {
		t.Fatal("audit coverage mismatch")
	}
	var added []string
	if audit.HistoryPath != "" {
		if audit.HistoryPath != itemexpiration.GiftBoxHistoryPath {
			t.Fatal("unexpected provenance path")
		}
		added = []string{audit.HistoryPath}
	}
	if unchanged, err := a.VerifyScriptReplacementAndAdditionPreservation(b, paths, added); err != nil || unchanged+len(paths) != a.FileCount() {
		t.Fatalf("unrelated entries: %d %v", unchanged, err)
	}
	const target = "stackable/490008001/chn_490008481.stk"
	if len(allowed[target]) != 1 {
		t.Fatal("requested 490008481 was not extended exactly once")
	}
	t.Logf("verified %d date values across %d files; %d unrelated archive entries preserved", verified, len(paths), a.FileCount()-len(paths))
}

func TestRewritePreservesPermanentLaterAndNonBox(t *testing.T) {
	for _, value := range []string{"", deadline, "2029-01-01 00:00:00"} {
		input := []pvf.ScriptToken{tag("[stackable type]"), str("[booster]"), tag("[expiration date]"), str(value)}
		got, edits, _, err := rewrite("box.stk", input)
		if err != nil || len(edits) != 0 || !reflect.DeepEqual(got, input) {
			t.Fatalf("%q changed: %v", value, err)
		}
	}
	for _, input := range [][]pvf.ScriptToken{
		{tag("[stackable type]"), str("[booster]")},
		{tag("[stackable type]"), str("[waste]"), tag("[expiration date]"), str("2018-07-19 06:00:00")},
	} {
		got, edits, _, err := rewrite("item.stk", input)
		if err != nil || len(edits) != 0 || !reflect.DeepEqual(got, input) {
			t.Fatal("permanent or non-box changed")
		}
	}
}

func TestRewriteExtendsEveryRepeatedDateWithoutChoosingWinner(t *testing.T) {
	input := []pvf.ScriptToken{tag("[stackable type]"), str("[booster selection]"), tag("[expiration date]"), str("2014-11-30 06:00:00"), tag("[expiration date]"), str("2015-01-15 06:00:00")}
	got, edits, _, err := rewrite("duplicate.stk", input)
	if err != nil || len(edits) != 2 || len(got) != len(input) || got[3].Text != deadline || got[5].Text != deadline {
		t.Fatalf("edits=%+v err=%v", edits, err)
	}
}

func TestRewriteRepeatedBoxTypesRetainsBothAndRejectsMixedKinds(t *testing.T) {
	input := []pvf.ScriptToken{tag("[stackable type]"), str("[cera booster]"), tag("[stackable type]"), str("[booster]"), tag("[expiration date]"), str("2015-01-15 06:00:00")}
	got, edits, kind, err := rewrite("repeated-type.stk", input)
	if err != nil || kind != "[cera booster]" || len(edits) != 1 || !reflect.DeepEqual(got[:5], input[:5]) {
		t.Fatalf("%v %+v", err, edits)
	}
	input[1].Text = "[waste]"
	if _, _, _, err := rewrite("mixed.stk", input); err == nil {
		t.Fatal("mixed classification accepted")
	}
}

func TestRewriteRejectsMalformedBoxBeforeRepack(t *testing.T) {
	for name, tail := range map[string][]pvf.ScriptToken{
		"missing":         {},
		"integer":         {{Type: 0, Value: 2018}},
		"invalid date":    {str("2018-02-30 06:00:00")},
		"unparsed suffix": {str("2018-07-19 06:00:00 suffix")},
		"two values":      {str("2018-07-19 06:00:00"), str("2019-01-01 06:00:00")},
	} {
		t.Run(name, func(t *testing.T) {
			input := []pvf.ScriptToken{tag("[stackable type]"), str("[booster]"), tag("[expiration date]")}
			input = append(input, tail...)
			if _, _, _, err := rewrite("bad.stk", input); err == nil {
				t.Fatal("malformed date accepted")
			}
		})
	}
}

func boxTypeInput(kind, value string) []pvf.ScriptToken {
	return []pvf.ScriptToken{tag("[stackable type]"), str(kind), tag("[expiration date]"), str(value)}
}

var canonicalBoxTypes = []string{"[booster]", "[booster selection]", "[cera booster]", "[cera package]", "[usable cera package]", "[booster random]", "[random upgradable legacy]", "[upgradable legacy]"}

// reviewedExtraBoxPaths is the round-18 reviewed set: nine box-shaped documents
// whose [stackable type] is outside the canonical family. Each entry is only in
// scope as this exact path/type pair.
var reviewedExtraBoxPaths = map[string]string{
	"stackable/10304001/10304500.stk":                                                                     "[grouped random box]",
	"stackable/490006001/chn_490006626.stk":                                                               "[random reward item]",
	"stackable/cash/chn_20131210_ameba_box/chn_ameba_cerashop_2683281.stk":                                "[random reward item]",
	"stackable/cash/chn_20150129_mysterious_hammer_pocket/chn_avatar_box_10004710.stk":                    "[avatar gift]",
	"stackable/event/chn_choice_dungeon_2013_09/chn_stk_firework_money_02.stk":                            "[random reward item]",
	"stackable/event/chn_choice_dungeon_2014_01/chn_stk_firework_money_02.stk":                            "[random reward item]",
	"stackable/event/chn_choice_dungeon_2014_07/chn_stk_firework_money_02_10003865.stk":                   "[random reward item]",
	"stackable/event/chn_event_20131107_solo_paradise_couple_hell/chn_stk_rose_without_love_10002497.stk": "[random reward item]",
	"stackable/event/chn_event_20141111_solo_day/chn_stk_rose_love_10004383.stk":                          "[random reward item]",
}

// outOfScopeBoxShapedPaths are real archive paths whose names and neighbours
// look like the reviewed set but are deliberately excluded: an unlisted
// [random reward item] firecracker, a firecracker-effect sibling, a discount
// coupon that names a gift box, and the still-unresolved 20-minute box.
var outOfScopeBoxShapedPaths = []struct{ name, path, kind string }{
	{"recorded path declares another type", "stackable/490006001/chn_490006626.stk", "[quest]"},
	{"unlisted firecracker in the recorded directory", "stackable/490006001/chn_490006499.stk", "[random reward item]"},
	{"firecracker effect sibling", "stackable/event/chn_choice_dungeon_2014_01/chn_stk_firework_money_01.stk", "[only effect]"},
	{"coupon naming a gift box", "stackable/event/chn_20131217_no1dnf/chn_cpn_10002763_1.stk", "[coupon]"},
	{"unresolved guardian box", "stackable/chn_box_20min_invitation_10002350.stk", "[quest]"},
}

func TestRewriteExtendsOnlyReviewedExtraBoxPaths(t *testing.T) {
	if len(reviewedExtraBoxPaths) != 9 {
		t.Fatalf("reviewed extra boxes=%d, want 9", len(reviewedExtraBoxPaths))
	}
	for path, kind := range reviewedExtraBoxPaths {
		t.Run(path, func(t *testing.T) {
			input := boxTypeInput(kind, "2014-01-01 06:00:00")
			got, edits, gotKind, err := rewrite(path, input)
			if err != nil || len(edits) != 1 || gotKind != kind || got[3].Text != deadline {
				t.Fatalf("kind=%q edits=%+v err=%v", gotKind, edits, err)
			}
			if edits[0].Old != "2014-01-01 06:00:00" || edits[0].New != deadline {
				t.Fatalf("recorded change=%+v", edits[0])
			}
			replay, again, _, err := rewrite(path, got)
			if err != nil || len(again) != 0 || !reflect.DeepEqual(replay, got) {
				t.Fatal("not idempotent")
			}
		})
	}
}

func TestRewriteLeavesOutOfScopeBoxShapedPathsUntouched(t *testing.T) {
	for _, tc := range outOfScopeBoxShapedPaths {
		t.Run(tc.name, func(t *testing.T) {
			input := boxTypeInput(tc.kind, "2014-01-01 06:00:00")
			before := append([]pvf.ScriptToken(nil), input...)
			got, edits, _, err := rewrite(tc.path, input)
			if err != nil || len(edits) != 0 || !reflect.DeepEqual(got, before) {
				t.Fatalf("out-of-scope path was changed: edits=%+v err=%v", edits, err)
			}
		})
	}
	// The canonical eight keep their per-type behaviour on any archive path.
	for _, kind := range canonicalBoxTypes {
		t.Run(kind+" on an unlisted path", func(t *testing.T) {
			got, edits, _, err := rewrite("stackable/490006001/chn_490006499.stk", boxTypeInput(kind, "2014-01-01 06:00:00"))
			if err != nil || len(edits) != 1 || got[3].Text != deadline {
				t.Fatalf("canonical type lost its path-independent scope: edits=%+v err=%v", edits, err)
			}
		})
	}
}

// The rewrite scope and the run() index filter have to be the same answer, so a
// pair that rewrite() edits can never be skipped by the filter that counts and
// records the file.
func TestRewriteScopeMatchesRunFilterScope(t *testing.T) {
	type pair struct{ path, kind string }
	var pairs []pair
	for path, kind := range reviewedExtraBoxPaths {
		pairs = append(pairs, pair{path, kind})
	}
	for _, tc := range outOfScopeBoxShapedPaths {
		pairs = append(pairs, pair{tc.path, tc.kind})
	}
	for _, kind := range canonicalBoxTypes {
		pairs = append(pairs, pair{"stackable/490006001/chn_490006499.stk", kind}, pair{"box.stk", kind})
	}
	for _, tc := range []pair{
		{"stackable/10304001/10304500.stk", "[random reward item]"},
		{"stackable/cash/chn_20150129_mysterious_hammer_pocket/chn_avatar_box_10004710.stk", "[grouped random box]"},
		{"Stackable/Event/chn_choice_dungeon_2013_09/chn_stk_firework_money_02.stk", "[random reward item]"},
	} {
		pairs = append(pairs, tc)
	}
	for _, tc := range pairs {
		input := boxTypeInput(tc.kind, "2014-01-01 06:00:00")
		_, edits, _, err := rewrite(tc.path, input)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.path, tc.kind, err)
		}
		shared := itemexpiration.IsGiftBoxPath(tc.path, tc.kind)
		if filter := boxScope(tc.path, tc.kind); filter != shared {
			t.Fatalf("%s %s: run filter=%v but the shared bridge predicate=%v", tc.path, tc.kind, filter, shared)
		}
		if shared != (len(edits) == 1) {
			t.Fatalf("%s %s: shared scope=%v but rewrite edits=%d", tc.path, tc.kind, shared, len(edits))
		}
	}
}

func TestRewriteRepeatedExtraTypesUseFirstValueAndValidateEveryDeclaration(t *testing.T) {
	path := "stackable/event/chn_event_20141111_solo_day/chn_stk_rose_love_10004383.stk"
	input := []pvf.ScriptToken{tag("[stackable type]"), str("[random reward item]"), tag("[stackable type]"), str("[random reward item]"), tag("[expiration date]"), str("2014-11-27 06:00:00")}
	got, edits, kind, err := rewrite(path, input)
	if err != nil || kind != "[random reward item]" || len(edits) != 1 || got[5].Text != deadline || !reflect.DeepEqual(got[:4], input[:4]) {
		t.Fatalf("kind=%q edits=%+v err=%v got=%+v", kind, edits, err, got)
	}
	mixed := []pvf.ScriptToken{tag("[stackable type]"), str("[random reward item]"), tag("[stackable type]"), str("[quest]"), tag("[expiration date]"), str("2014-11-27 06:00:00")}
	if _, _, _, err := rewrite(path, mixed); err == nil {
		t.Fatal("a declaration outside the recorded pair was accepted")
	}
	// The same family on an unlisted path stays untouched: the type family
	// itself is never widened, and a non-box file is still skipped silently.
	unlisted := "stackable/490006001/chn_490006499.stk"
	for name, tokens := range map[string][]pvf.ScriptToken{"repeated": input, "mixed": mixed} {
		t.Run(name, func(t *testing.T) {
			before := append([]pvf.ScriptToken(nil), tokens...)
			got, edits, _, err := rewrite(unlisted, tokens)
			if err != nil || len(edits) != 0 || !reflect.DeepEqual(got, before) {
				t.Fatalf("unlisted path changed: edits=%+v err=%v", edits, err)
			}
		})
	}
}
