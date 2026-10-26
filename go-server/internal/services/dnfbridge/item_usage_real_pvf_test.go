package dnfbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemusage"
	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// The fixture contains only IDs, archive paths and count metadata from the
// audited archive, not item scripts. It covers every listed daily/weekly item;
// owner support for special action items is a separate contract.
func TestRealPVFItemUsageCensusAndProjection(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires matching real PVF")
	}
	var census struct {
		Hash string `json:"pvf_sha256"`
		Rows []struct {
			ID      uint32 `json:"id"`
			Path    string `json:"path"`
			Daily   int64  `json:"daily"`
			Weekly  int64  `json:"weekly"`
			Reset   int64  `json:"weekly_reset"`
			Fatigue bool   `json:"fatigue"`
		} `json:"rows"`
	}
	raw, err := os.ReadFile("testdata/item_usage_94a254.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &census); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if hex.EncodeToString(hash.Sum(nil)) != census.Hash {
		t.Fatal("PVF changed; regenerate the full count census before claiming coverage")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog := make(fatiguePotionUsageTestCatalog)
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	c := repo.CharacterRecord{CharacterID: "17", AccountID: "audit", Stats: make(map[string]int64)}
	wantD, wantW := map[uint32]byte{}, map[uint32]byte{}
	var definitions []map[string]any
	fatigue := 0
	for _, row := range census.Rows {
		if _, duplicate := catalog[row.ID]; duplicate {
			t.Fatalf("duplicate ID %d", row.ID)
		}
		text, err := archive.ReadText(row.Path)
		if err != nil {
			t.Fatal(row.ID, err)
		}
		doc, err := pvf.Parse(row.Path, text)
		if err != nil {
			t.Fatal(row.ID, err)
		}
		d, err := itemusage.FromPVF(row.ID, doc)
		if err != nil || d == nil || !d.Daily || d.DailyLimit != row.Daily || d.Weekly != (row.Weekly >= 0) || (d.Weekly && (d.WeeklyLimit != row.Weekly || int64(d.WeeklyResetDay) != row.Reset)) {
			t.Fatalf("item=%d definition=%+v err=%v", row.ID, d, err)
		}
		catalog[row.ID] = dungeonDropItemDefinition{ItemID: row.ID, Kind: dungeonDropItemStackable, PVFPath: row.Path}
		if row.Fatigue {
			f, ok := fatiguePotionDefinition(int64(row.ID), row.Path, doc)
			if !ok || f.DailyLimit != row.Daily {
				t.Fatalf("fatigue %d failed", row.ID)
			}
			c.Stats["fatigue_potion_item_"+strconv.FormatUint(uint64(row.ID), 10)] = 1
			c.Stats["fatigue_potion_day_"+f.UsageGroup()] = premium.ServiceDayID(now)
			c.Stats["fatigue_potion_used_"+f.UsageGroup()] = 1
			fatigue++
		} else if err := itemusage.Consume(&c, *d, now); err != nil {
			t.Fatal(row.ID, err)
		}
		wantD[row.ID] = 1
		if d.Weekly {
			wantW[row.ID] = 1
		}
		definitions = append(definitions, map[string]any{"id": row.ID, "daily": d.DailyLimit, "weekly": row.Weekly, "group": d.GroupID})
	}
	if len(catalog) != 662 || fatigue != 318 || len(wantW) != 3 {
		t.Fatalf("census changed: all=%d fatigue=%d weekly=%d", len(catalog), fatigue, len(wantW))
	}
	var scenarios []map[string]any
	for _, hours := range []int{0, 24, 7 * 24} {
		bodies, ok := buildCurrentItemUsageBodies(c, repo.InventoryRecord{CharacterID: c.CharacterID}, now.Add(time.Duration(hours)*time.Hour), catalog, archive)
		if !ok {
			t.Fatal("missing projection")
		}
		gotD, gotW := decodeItemUsagePages(t, bodies)
		if hours > 0 {
			for id := range wantD {
				wantD[id] = 0
			}
		}
		if hours == 7*24 {
			for id := range wantW {
				wantW[id] = 0
			}
		}
		if !reflect.DeepEqual(gotD, wantD) || !reflect.DeepEqual(gotW, wantW) {
			t.Fatalf("census projection hours=%d daily=%d weekly=%v", hours, len(gotD), gotW)
		}
		var hexBodies []string
		for _, body := range bodies {
			hexBodies = append(hexBodies, hex.EncodeToString(body))
		}
		scenarios = append(scenarios, map[string]any{"hours": hours, "bodies": hexBodies, "daily": gotD, "weekly": gotW})
	}
	if output := os.Getenv("DNF_ITEM_USAGE_NATIVE_FIXTURE"); output != "" {
		raw, err := json.MarshalIndent(map[string]any{"pvf_sha256": census.Hash, "definitions": definitions, "scenarios": scenarios}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(raw)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	t.Logf("all %d listed daily/weekly definitions: %d fatigue, %d other; use-history projection, empty inventory/reconnect and day/week refresh", len(catalog), fatigue, len(catalog)-fatigue)
}
