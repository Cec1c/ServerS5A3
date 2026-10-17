package itemexpiration

import (
	"path"
	"strings"
	"testing"
)

var canonicalGiftBoxTypes = []string{"[booster]", "[booster selection]", "[cera booster]", "[cera package]", "[usable cera package]", "[booster random]", "[random upgradable legacy]", "[upgradable legacy]"}

// The reviewed extra documents are in scope only as exact (path, type) pairs,
// and every recorded pair must stay loadable by the bridge manifest validator.
func TestIsGiftBoxPathScope(t *testing.T) {
	recorded := map[string]string{
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
	if len(giftBoxPathScope) != len(recorded) {
		t.Fatalf("recorded extra box paths=%d, want %d", len(giftBoxPathScope), len(recorded))
	}
	for p, kind := range recorded {
		if got, ok := giftBoxPathScope[p]; !ok || got != kind {
			t.Fatalf("%s: recorded pair missing or changed", p)
		}
		if IsGiftBoxType(kind) {
			t.Fatalf("%s: %q is already canonical and needs no path entry", p, kind)
		}
		// loadCurrentGiftBoxExpirationHistory rejects a provenance key that is
		// not already lower-cased, clean and stackable/*.stk.
		if p != strings.ToLower(p) || path.Clean(p) != p || !strings.HasPrefix(p, "stackable/") || !strings.HasSuffix(p, ".stk") {
			t.Fatalf("%s: path would fail the provenance identity check", p)
		}
		if !IsGiftBoxPath(p, kind) {
			t.Fatalf("%s %s not in scope", p, kind)
		}
	}
	for _, kind := range canonicalGiftBoxTypes {
		if !IsGiftBoxType(kind) {
			t.Fatalf("%q must stay canonical", kind)
		}
		if !IsGiftBoxPath("stackable/490006001/chn_490006499.stk", kind) {
			t.Fatalf("%q must stay in scope on any path", kind)
		}
	}
}

func TestIsGiftBoxPathRejectsWrongPathAndWrongType(t *testing.T) {
	for name, tc := range map[string]struct{ path, kind string }{
		"recorded path with another type":         {"stackable/490006001/chn_490006626.stk", "[quest]"},
		"recorded path with a canonical type":     {"stackable/490006001/chn_490006626.stk", "[waste]"},
		"unlisted firecracker of the same family": {"stackable/490006001/chn_490006499.stk", "[random reward item]"},
		"firecracker effect sibling":              {"stackable/event/chn_choice_dungeon_2014_01/chn_stk_firework_money_01.stk", "[only effect]"},
		"coupon naming a gift box":                {"stackable/event/chn_20131217_no1dnf/chn_cpn_10002763_1.stk", "[coupon]"},
		"unresolved 20-minute guardian box":       {"stackable/chn_box_20min_invitation_10002350.stk", "[quest]"},
		"same file name one directory up":         {"stackable/event/chn_stk_firework_money_02.stk", "[random reward item]"},
		"empty path":                              {"", "[random reward item]"},
		"avatar gift on another cash box path":    {"stackable/cash/chn_20131210_ameba_box/chn_ameba_cerashop_2683281.stk", "[avatar gift]"},
	} {
		t.Run(name, func(t *testing.T) {
			if IsGiftBoxPath(tc.path, tc.kind) {
				t.Fatalf("%s %s must stay out of scope", tc.path, tc.kind)
			}
		})
	}
}

func TestIsGiftBoxPathAcceptsArchivePathShapeVariants(t *testing.T) {
	const exact = "stackable/event/chn_choice_dungeon_2013_09/chn_stk_firework_money_02.stk"
	for _, p := range []string{exact, "/" + exact, strings.ToUpper(exact), strings.ReplaceAll(exact, "/", "\\"), "  " + exact + "  "} {
		if !IsGiftBoxPath(p, "[random reward item]") {
			t.Fatalf("%q was not recognized", p)
		}
	}
}
