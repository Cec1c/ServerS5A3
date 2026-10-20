package skillswitch

// This test pins the answer to the operator-facing question
// "the scope holds several key families - which one is authoritative for the
// loadout resend, and do the others get unioned in?".
//
// Answer pinned here, at the storage layer:
//
//   - the ONLY state family is `slot_<n>`; `Load` reads nothing else
//     (store.go:183-202). One entry per slot, one frame per entry.
//   - `record_<hex>` (v4, 8 hex) / `record_<hex>` (legacy, 12 hex) /
//     `sentinel_<hex>` are write-only forensics written by `Apply`
//     (store.go:414-422). They are counted (`ForensicCount`) and never read.
//   - therefore two differently-encoded forensic keys that resolve to the same
//     slot (`record_0e003e00` vs `record_0e030e00`) cannot duplicate or
//     override a slot: they are not part of the projection at all.
//   - a scope that holds ONLY forensic keys yields ZERO frames: there is no
//     union fallback anywhere.
//
// The fixture values are the live operator rows (char 6, 2026-09-28):
// `slot_12=0c003600` (12 -> 54) and a legacy-encoded sibling.

import (
	"bytes"
	"context"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// seedSingleFamilyScope writes a scope that deliberately mixes every encoding
// the live database holds at once: a v4 state row, a legacy state row, both
// forensic encodings of the same slot, a sentinel and the bookkeeping keys.
func seedSingleFamilyScope(t *testing.T, repos dnfrepo.Group, scope string) {
	t.Helper()
	values := map[string]string{
		// state family, v4 canonical body (8 hex): slot 12 -> value 54
		"slot_12": "0c003600",
		// state family, legacy 6-byte row (12 hex): slot 14 -> value 62
		"slot_14": "00000e003e00",
		// forensic: the v4 form of slot 12 and the legacy form of slot 14
		"record_0c003600":       "wire_prefix=0 slot=12 mode=0 value=54",
		"record_00000e003e00":   "scope=0 slot_a=14 sub_mode=0x0 slot_b=62",
		"record_0e030e00":       "scope=0 slot_a=14 sub_mode=0x3 slot_b=14",
		"record_00000e030e00":   "scope=0 slot_a=14 sub_mode=0x3 slot_b=14",
		"sentinel_0000212cffff": "scope=0 slot_a=33 sub_mode=0x2c slot_b=65535",
		"source":                SourceCurrentEXE,
		"slot_count":            "2",
		"record_count":          "5",
	}
	if err := repos.Settings.Save(context.Background(), dnfrepo.SettingsRecord{
		Scope:  scope,
		Values: values,
	}); err != nil {
		t.Fatalf("seed %s: %v", scope, err)
	}
}

// TestLoadReadsOnlyTheSlotStateFamily - the projection input is exactly one
// entry per slot in one encoding, no matter how many forensic encodings the
// same slot has accumulated across releases.
func TestLoadReadsOnlyTheSlotStateFamily(t *testing.T) {
	repos, _ := newSkillSwitchFixture(t)
	ctx := context.Background()
	seedSingleFamilyScope(t, repos, Scope("6"))

	state, found, err := Load(ctx, repos, "6", LayoutV4)
	if err != nil || !found {
		t.Fatalf("Load found=%t err=%v", found, err)
	}
	if len(state.Records) != 2 {
		t.Fatalf("state records = %d, want 2 (slot_12 + slot_14 only); records=%+v",
			len(state.Records), state.Records)
	}
	if state.Records[0].Slot != 12 || state.Records[1].Slot != 14 {
		t.Fatalf("slots = %d,%d want 12,14", state.Records[0].Slot, state.Records[1].Slot)
	}
	if state.Records[0].Value != 54 || state.Records[1].Value != 62 {
		t.Fatalf("values = %d,%d want 54,62", state.Records[0].Value, state.Records[1].Value)
	}
	// The legacy 6-byte row must be normalized: dual-read, one canonical encoding.
	if state.Records[1].Layout != LayoutV4 {
		t.Fatalf("legacy row normalized layout = %s, want %s", state.Records[1].Layout, LayoutV4)
	}
	if got := state.Records[1].Bytes(); !bytes.Equal(got, []byte{0x0e, 0x00, 0x3e, 0x00}) {
		t.Fatalf("legacy row canonical bytes = %x, want 0e003e00", got)
	}
	// `Load` counts `record_*` only (store.go:185-186); `countEntries` (the
	// persisted bookkeeping counter) also counts `sentinel_*` = 5.
	if state.ForensicCount != 4 {
		t.Fatalf("forensic count = %d, want 4 (counted, never projected)", state.ForensicCount)
	}
	if _, forensic := countEntries(map[string]string{
		"record_0c003600":       "x",
		"record_00000e003e00":   "x",
		"record_0e030e00":       "x",
		"record_00000e030e00":   "x",
		"sentinel_0000212cffff": "x",
	}); forensic != 5 {
		t.Fatalf("countEntries forensic = %d, want 5 (4 record_* + 1 sentinel_*)", forensic)
	}

	frames := state.Frames()
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2 (one per slot, never one per forensic key)", len(frames))
	}
	for index, frame := range frames {
		if len(frame) != 4 {
			t.Fatalf("frame %d = %x (%d bytes), want the 4-byte canonical body",
				index, frame, len(frame))
		}
		if !bytes.Equal(frame, state.Records[index].Bytes()) {
			t.Fatalf("frame %d = %x, want %x", index, frame, state.Records[index].Bytes())
		}
	}
}

// TestLoadNeverUnionsForensicKeysIntoTheLoadout - a scope that holds only
// forensic keys (the shape several releases of history can leave behind)
// projects ZERO frames; there is no fallback that reconstructs state from
// `record_*`.
func TestLoadNeverUnionsForensicKeysIntoTheLoadout(t *testing.T) {
	repos, _ := newSkillSwitchFixture(t)
	ctx := context.Background()
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: Scope("7"),
		Values: map[string]string{
			"record_0c003600":       "wire_prefix=0 slot=12 mode=0 value=54",
			"record_00000c003600":   "scope=0 slot_a=12 sub_mode=0x0 slot_b=54",
			"record_0e003e00":       "wire_prefix=0 slot=14 mode=0 value=62",
			"record_00000e003e00":   "scope=0 slot_a=14 sub_mode=0x0 slot_b=62",
			"sentinel_0000212cffff": "scope=0 slot_a=33 sub_mode=0x2c slot_b=65535",
			"source":                SourceCurrentEXE,
		},
	}); err != nil {
		t.Fatal(err)
	}

	state, found, err := Load(ctx, repos, "7", LayoutV4)
	if err != nil || !found {
		t.Fatalf("Load found=%t err=%v", found, err)
	}
	if len(state.Records) != 0 || len(state.Frames()) != 0 {
		t.Fatalf("forensic-only scope projected %d records / %d frames, want 0/0",
			len(state.Records), len(state.Frames()))
	}
	if state.ForensicCount != 4 {
		t.Fatalf("forensic count = %d, want 4 (`Load` counts record_* only)", state.ForensicCount)
	}
}
