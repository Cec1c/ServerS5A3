package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestLukeAliasRangeDeclarationBoundary(t *testing.T) {
	alias := lukeSingleAliases()[95310]
	for _, tc := range []struct {
		name, positions, suffix string
		valid                   bool
	}{
		{"absent_uses_native_defaults", "24 249 1292 316 703 158 572 603", "", true},
		{"explicit_eight", "24 249 1292 316 703 158 572 603", "[pathgate recognize range]\n0 170 0 170 0 0 0 0", true},
		{"explicit_nonpositive_defaults", "24 249 1292 316 703 158 572 603", "[pathgate recognize range]\n-1 0 0 -2 -3 0 0 -4", true},
		{"explicit_seven_rejected", "24 249 1292 316 703 158 572 603", "[pathgate recognize range]\n0 170 0 170 0 0 0", false},
		{"explicit_empty_rejected", "24 249 1292 316 703 158 572 603", "[pathgate recognize range]\n", false},
		{"explicit_eight_and_string_rejected", "24 249 1292 316 703 158 572 603", "[pathgate recognize range]\n0 170 0 170 0 0 0 0 `extra`", false},
		{"explicit_noninteger_rejected", "24 249 1292 316 703 158 572 603", "[pathgate recognize range]\n0 170 0 170 0 0 0 `0`", false},
		{"missing_position_rejected", "24 249 1292 316 703 158", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "[dungeon]\n5331\n[/dungeon]\n[greed]\n`FF FF`\n[pathgate pos]\n" + tc.positions + "\n" + tc.suffix
			doc, err := dnfpvf.Parse(alias.file, text)
			if err != nil {
				t.Fatal(err)
			}
			if err = validateLukeAlias(95310, alias, doc); (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
}

func TestRealPVFLukeMissingRangeAndAuditRemainUnaccepted(t *testing.T) {
	input := os.Getenv("DNF_RAID_SINGLE_INDEX_REAL_PVF")
	if input == "" {
		t.Skip("requires pinned fixed PVF")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	want := "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06"
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
		t.Fatal("unexpected fixed PVF")
	}
	a, err := platformpvf.OpenBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{95310, 95311} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			alias := lukeSingleAliases()[id]
			text, err := a.ReadText(alias.file)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := dnfpvf.Parse(alias.file, text)
			if err != nil {
				t.Fatal(err)
			}
			if _, present := doc.Section("pathgate recognize range"); present {
				t.Fatal("real missing-range evidence changed")
			}
			if err := validateLukeAlias(id, alias, doc); err != nil {
				t.Fatal(err)
			}
			// Mutate text only: an explicitly truncated table must still fail.
			doc, err = dnfpvf.Parse(alias.file, text+"\n[pathgate recognize range]\n0 170 0 170 0 0 0\n")
			if err != nil {
				t.Fatal(err)
			}
			if err := validateLukeAlias(id, alias, doc); err == nil {
				t.Fatal("real MAP with explicit seven-value range accepted")
			}
		})
	}
	t.Run("audit_only", func(t *testing.T) {
		report := filepath.Join(t.TempDir(), "audit.json")
		if err := run(input, "", report, 1); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(report)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		if err = json.Unmarshal(b, &result); err != nil {
			t.Fatal(err)
		}
		if string(result["native_ready"]) != "false" || string(result["read_only"]) != "true" || string(result["unchanged_asset_contents"]) != "true" {
			t.Fatal("audit is not native acceptance", string(b))
		}
		var issues []string
		if err = json.Unmarshal(result["native_door_issues"], &issues); err != nil || len(issues) != 0 {
			t.Fatal("missing range incorrectly blocks index output", issues, err)
		}
		var maps, dungeons []indexEntry
		if err = json.Unmarshal(result["map_entries"], &maps); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(result["dungeon_entries"], &dungeons); err != nil {
			t.Fatal(err)
		}
		if len(maps) != 58 || len(dungeons) != 12 {
			t.Fatal("index-only proposal changed", len(maps), len(dungeons))
		}
		if _, exists := result["output"]; exists {
			t.Fatal("audit unexpectedly wrote candidate")
		}
	})
	after, err := os.ReadFile(input)
	if err != nil || sha256.Sum256(after) != sha256.Sum256(raw) {
		t.Fatal("input archive changed", err)
	}
}
