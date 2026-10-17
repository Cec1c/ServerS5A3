package itemexpiration

import "strings"

// GiftBoxHistorySection carries local provenance for an extended PVF deadline.
const GiftBoxHistorySection = "local giftbox expiration history"
const GiftBoxHistoryPath = "etc/local/giftbox_expiration_history.etc"
const GiftBoxHistoryDeadline uint32 = 1861826400 // 2028-12-31 06:00 CST

// giftBoxPathScope records the box-shaped archive paths that were reviewed one
// by one outside the canonical [stackable type] set. Every entry is an exact
// (path, declared type) pair: the recorded type must be the one declared on
// that path, and the same path declaring another type stays out of scope, so a
// mixed family ([quest], [random reward item]) is never widened as a whole.
var giftBoxPathScope = map[string]string{
	"stackable/10304001/10304500.stk":                                                                     "[grouped random box]",
	"stackable/cash/chn_20131210_ameba_box/chn_ameba_cerashop_2683281.stk":                                "[random reward item]",
	"stackable/cash/chn_20150129_mysterious_hammer_pocket/chn_avatar_box_10004710.stk":                    "[avatar gift]",
	"stackable/event/chn_choice_dungeon_2013_09/chn_stk_firework_money_02.stk":                            "[random reward item]",
	"stackable/event/chn_choice_dungeon_2014_01/chn_stk_firework_money_02.stk":                            "[random reward item]",
	"stackable/event/chn_choice_dungeon_2014_07/chn_stk_firework_money_02_10003865.stk":                   "[random reward item]",
	"stackable/event/chn_event_20131107_solo_paradise_couple_hell/chn_stk_rose_without_love_10002497.stk": "[random reward item]",
	"stackable/event/chn_event_20141111_solo_day/chn_stk_rose_love_10004383.stk":                          "[random reward item]",
	"stackable/490006001/chn_490006626.stk":                                                               "[random reward item]",
}

// IsGiftBoxType reports the canonical stackable types whose declared deadlines
// are extended on any archive path.
func IsGiftBoxType(kind string) bool {
	switch kind {
	case "[booster]", "[booster selection]", "[cera booster]", "[cera package]", "[usable cera package]", "[booster random]", "[random upgradable legacy]", "[upgradable legacy]":
		return true
	}
	return false
}

// IsGiftBoxPath is the one scope decision shared by the date tool and the
// server bridge: true when this archive path paired with its declared type may
// have its declared deadline extended. A canonical type is in scope on any
// path; everything else needs an exact recorded path/type pair.
func IsGiftBoxPath(path, kind string) bool {
	if IsGiftBoxType(kind) {
		return true
	}
	recorded, ok := giftBoxPathScope[normalizeGiftBoxPath(path)]
	return ok && recorded == kind
}

// normalizeGiftBoxPath makes archive-path lookup independent of separator
// style, a leading separator, and case.
func normalizeGiftBoxPath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimLeft(p, "/")
	return strings.ToLower(p)
}

// Match an exact recorded date, never any timestamp earlier than the new date.
func GiftBoxDeadlineMatches(instance, current uint32, history []uint32) bool {
	if instance == 0 || current == 0 || instance >= current {
		return false
	}
	for _, old := range history {
		if old != 0 && instance == old {
			return true
		}
	}
	return false
}
