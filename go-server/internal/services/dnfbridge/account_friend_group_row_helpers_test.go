package dnfbridge

import (
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/accountfriend"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Character ids are an ORDERING key here, never a validity gate. Production
// already decides which characters may be projected through its own u16 checks
// (currentAccountFriendOnlineChannel / currentAccountFriendRowForCharacter), so
// the helper accepts every decimal spelling - including ids above 65535 - and
// must not reject anything.
func TestAccountFriendCharacterIDNumberDomain(t *testing.T) {
	numeric := map[string]uint64{"1": 1, " 5 ": 5, "01": 1, "9": 9, "65535": 65535, "70000": 70000, "4294967295": 4294967295, "0": 0}
	for raw, want := range numeric {
		got, ok := accountFriendCharacterIDNumber(raw)
		if !ok || got != want {
			t.Fatalf("number(%q)=(%d,%v) want (%d,true)", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", " ", "abc", "1a", "5x", "0x10"} {
		if _, ok := accountFriendCharacterIDNumber(raw); ok {
			t.Fatalf("number(%q) must not be numeric", raw)
		}
	}
	// strconv.ParseUint takes no sign: a signed spelling is not a decimal id.
	if _, err := strconv.ParseUint("+1", 10, 64); err == nil {
		t.Fatal("ParseUint must reject \"+1\"")
	}
	if _, err := strconv.ParseUint("-1", 10, 64); err == nil {
		t.Fatal("ParseUint must reject \"-1\"")
	}
	for _, raw := range []string{"+1", "-1"} {
		if _, ok := accountFriendCharacterIDNumber(raw); ok {
			t.Fatalf("number(%q) must not be numeric", raw)
		}
	}
}

// Single ordered key: full pairwise trichotomy/antisymmetry plus all-triple
// transitivity over the exact set whose mixed numeric/string rule used to cycle
// (9 < 10 < "1a" < 9).
func TestAccountFriendCharacterIDOrderIsTotal(t *testing.T) {
	ids := []string{"9", "10", "1a", "01", "1", "65535", "70000", "0", "abc", "", " 5 ", "5", "+1"}
	for _, a := range ids {
		for _, b := range ids {
			ab, ba := compareAccountFriendCharacterID(a, b), compareAccountFriendCharacterID(b, a)
			if ab != -ba {
				t.Fatalf("antisymmetry broken: cmp(%q,%q)=%d cmp(%q,%q)=%d", a, b, ab, b, a, ba)
			}
		}
	}
	for _, a := range ids {
		for _, b := range ids {
			for _, c := range ids {
				if compareAccountFriendCharacterID(a, b) < 0 && compareAccountFriendCharacterID(b, c) < 0 {
					if !(compareAccountFriendCharacterID(a, c) < 0) {
						t.Fatalf("transitivity broken: %q < %q < %q but %q !< %q", a, b, c, a, c)
					}
				}
				if compareAccountFriendCharacterID(a, b) == 0 && compareAccountFriendCharacterID(b, c) == 0 {
					if compareAccountFriendCharacterID(a, c) != 0 {
						t.Fatalf("equality not transitive: %q=%q %q=%q but %q != %q", a, b, b, c, a, c)
					}
				}
			}
		}
	}
	if !(compareAccountFriendCharacterID("9", "10") < 0) {
		t.Fatal("9 must sort before 10 (numeric, not lexicographic)")
	}
	if !(compareAccountFriendCharacterID("70000", "999") > 0) {
		t.Fatal("70000 must sort after 999 numerically, not before it lexicographically")
	}
	if !(compareAccountFriendCharacterID("10", "1a") < 0) {
		t.Fatal("decimal class must sort before the non-decimal class")
	}
	if !(compareAccountFriendCharacterID("1a", "9") > 0) {
		t.Fatal("1a must not sort before 9: the class rule breaks the old cycle")
	}
}

func permuteStrings(values []string) [][]string {
	if len(values) <= 1 {
		return [][]string{append([]string(nil), values...)}
	}
	var out [][]string
	for i := range values {
		rest := append(append([]string(nil), values[:i]...), values[i+1:]...)
		for _, tail := range permuteStrings(rest) {
			out = append(out, append([]string{values[i]}, tail...))
		}
	}
	return out
}

// Any permutation of the same online set must select the same representative,
// including sets that mix decimal and non-decimal ids. Nothing is rejected.
func TestAccountFriendGroupRepresentativeIsPermutationInvariant(t *testing.T) {
	cases := []struct {
		ids  []string
		want string
	}{
		{[]string{"25", "9", "10"}, "9"},
		{[]string{"70000", "999"}, "999"},
		{[]string{"abc", "9"}, "9"},
	}
	for _, tc := range cases {
		for _, perm := range permuteStrings(tc.ids) {
			online := make([]repo.CharacterRecord, 0, len(perm))
			for _, id := range perm {
				online = append(online, repo.CharacterRecord{CharacterID: id})
			}
			idx := selectAccountFriendRepresentative(accountfriend.Entry{}, online)
			if idx < 0 || idx >= len(online) {
				t.Fatalf("perm %v: idx=%d", perm, idx)
			}
			if got := online[idx].CharacterID; got != tc.want {
				t.Fatalf("perm %v selected %q, want %q", perm, got, tc.want)
			}
		}
	}
	if idx := selectAccountFriendRepresentative(accountfriend.Entry{}, nil); idx != -1 {
		t.Fatalf("empty set must return -1, got %d", idx)
	}
	// Equal numeric value with different spellings still has a defined tie-break.
	online := []repo.CharacterRecord{{CharacterID: "1"}, {CharacterID: "01"}}
	if idx := selectAccountFriendRepresentative(accountfriend.Entry{}, online); idx != 1 {
		t.Fatalf("tie-break idx=%d want index of \"01\"", idx)
	}
}

// The 1371 body carries at most 255 rows; the sender splits at 255. Both the
// boundary and the overflow must keep failing closed rather than truncating.
func TestAccountFriendGroupRowBuilderPaginationBoundary(t *testing.T) {
	rows := make([]currentAccountFriendNativeRow, 0, 256)
	for i := 0; i < 255; i++ {
		rows = append(rows, currentAccountFriendNativeRow{
			AdventureName: "Beta", Level: 90, Job: 1, Grow: 0, Channel: 42,
			CharacterName: "ch" + strconv.Itoa(i),
		})
	}
	body, err := buildCurrentAccountFriendInfoBody(rows)
	if err != nil {
		t.Fatalf("255 rows must encode: %v", err)
	}
	if len(body) == 0 || int(body[0]) != 255 {
		t.Fatalf("255 row count byte: %v", body[:1])
	}
	rows = append(rows, currentAccountFriendNativeRow{
		AdventureName: "Beta", Level: 90, Job: 1, Grow: 0, Channel: 42, CharacterName: "ch255",
	})
	if _, err := buildCurrentAccountFriendInfoBody(rows); err == nil {
		t.Fatal("256 rows must be rejected, not truncated")
	}
}
