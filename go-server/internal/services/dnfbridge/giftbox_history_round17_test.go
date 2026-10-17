package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const giftHistoryOldRound17 uint32 = 1531951200 // 2018-07-19 06:00 CST

func giftHistoryDefinitionRound17() dungeonDropItemDefinition {
	return dungeonDropItemDefinition{ItemID: 490008481, Kind: dungeonDropItemStackable, PVFPath: "stackable/490008001/chn_490008481.stk", StackableType: "[booster selection]", ExpirationDate: time.Unix(int64(itemexpiration.GiftBoxHistoryDeadline), 0).UTC(), GiftBoxExpirationHistory: []uint32{giftHistoryOldRound17, 1830268800}}
}

func giftHistoryStackRound17(expire uint32, extra map[string]string) dnfrepo.ItemStack {
	stack := dnfrepo.ItemStack{ItemID: 490008481, Count: 1, ExpireAt: time.Unix(int64(expire), 0).UTC(), Extra: map[string]string{}, RawEntry: make([]byte, currentItemListEntryWireSize)}
	for k, v := range extra {
		stack.Extra[k] = v
	}
	stack.Extra["expire_time"] = strconv.FormatUint(uint64(expire), 10)
	binary.LittleEndian.PutUint32(stack.RawEntry[currentItemListExpireTimeOffset:], expire)
	return stack
}

func TestGiftBoxHistoryRound17ExactOldDatesAndProtection(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	def := giftHistoryDefinitionRound17()
	for _, test := range []struct {
		name   string
		expire uint32
		extra  map[string]string
		renew  bool
	}{
		{"original date", giftHistoryOldRound17, nil, true},
		{"prior horizon", 1830268800, nil, true},
		{"earlier independent", giftHistoryOldRound17 - 1, nil, false},
		{"later independent", giftHistoryOldRound17 + 1, nil, false},
		{"gm exact collision", giftHistoryOldRound17, map[string]string{"expiration_source": "gm_override", "gm_expire_days": "30"}, false},
		{"relative exact collision", giftHistoryOldRound17, map[string]string{"expiration_source": "runtime_pvf_usable_period_grant"}, false},
		{"rental exact collision", giftHistoryOldRound17, map[string]string{"expiration_source": "rental"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := giftHistoryStackRound17(test.expire, test.extra)
			saved := giftHistoryStackRound17(test.expire, test.extra)
			after, _ := applyCurrentPVFItemExpirationAt(before, def, now)
			want := test.expire
			if test.renew {
				want = itemexpiration.GiftBoxHistoryDeadline
			}
			if got := currentItemListStackExpire(after); got != want {
				t.Fatalf("stack deadline=%d want%d", got, want)
			}
			if !reflect.DeepEqual(before, saved) {
				t.Fatal("projection mutated input")
			}
			row := currentItemListEntryFromStack(0, 9, before)
			catalog := &pvfDungeonDropCatalog{source: dungeonDropCatalogTestSource{}, itemCache: map[uint32]dungeonDropItemDefinition{def.ItemID: def}}
			rows := []currentItemListEntry{row}
			if _, err := (&Service{}).applyCurrentPVFUsePeriodsToEntriesWithCatalog(context.Background(), rows, catalog, now); err != nil {
				t.Fatal(err)
			}
			if got := binary.LittleEndian.Uint32(rows[0].data[currentItemListExpireTimeOffset:]); got != want {
				t.Fatalf("wire=%d want%d", got, want)
			}
			second, _ := applyCurrentPVFItemExpirationAt(after, def, now)
			if !reflect.DeepEqual(second, after) {
				t.Fatal("renewal not idempotent")
			}
		})
	}
}

func TestGiftBoxHistoryRound17AccountAssetCommitAndReplay(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{AccountID: "gift17", CharacterID: "17"}); err != nil {
		t.Fatal(err)
	}
	old := giftHistoryStackRound17(giftHistoryOldRound17, nil)
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "17", Slots: map[string]dnfrepo.ItemStack{"0:9": old}, Warehouse: map[string]dnfrepo.ItemStack{"2:1": old}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "gift17", Slots: map[string]dnfrepo.ItemStack{"12:1": old}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: "17", Entries: map[string]dnfrepo.EquipmentEntry{}}); err != nil {
		t.Fatal(err)
	}
	def := giftHistoryDefinitionRound17()
	catalog := &pvfDungeonDropCatalog{source: dungeonDropCatalogTestSource{}, itemCache: map[uint32]dungeonDropItemDefinition{def.ItemID: def}}
	s := &Service{}
	summary, err := s.reconcileCurrentPVFItemExpirationsWithCatalog(ctx, repos, "17", "gift17", catalog)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Inventory != 1 || summary.Warehouse != 1 || summary.Account != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	inv, _, err := repos.Inventory.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := repos.AccountInventory.Load(ctx, "gift17")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []dnfrepo.ItemStack{inv.Slots["0:9"], inv.Warehouse["2:1"], account.Slots["12:1"]} {
		if got := currentItemListStackExpire(row); got != itemexpiration.GiftBoxHistoryDeadline {
			t.Fatalf("saved=%d", got)
		}
	}
	again, err := s.reconcileCurrentPVFItemExpirationsWithCatalog(ctx, repos, "17", "gift17", catalog)
	if err != nil || again.Total() != 0 {
		t.Fatalf("replay=%+v err=%v", again, err)
	}
}

type giftHistorySourceRound17 map[string]string

func (s giftHistorySourceRound17) ReadText(p string) (string, error) {
	v, ok := s[p]
	if !ok {
		return "", platformpvf.ErrFileNotFound
	}
	return v, nil
}
func (s giftHistorySourceRound17) FindFile(p string) (platformpvf.File, bool) {
	_, ok := s[p]
	return platformpvf.File{ArchivePath: p}, ok
}

func TestGiftBoxHistoryRound17ManifestValidation(t *testing.T) {

	valid := fmt.Sprintf("[version]\n1\n[target]\n%d\n[entry]\n`stackable/box.stk` `[booster]` %d\n[/entry]\n", itemexpiration.GiftBoxHistoryDeadline, giftHistoryOldRound17)
	for name, body := range map[string]string{"valid": valid, "wrong target": "[version] 1\n[target] 1\n", "duplicate": valid + valid, "missing close": strings.ReplaceAll(valid, "[/entry]", ""), "extra close": valid + "[/entry]\n", "wrong order": strings.ReplaceAll(valid, "[version]\n1\n", "") + "[version]\n1\n", "path escape": fmt.Sprintf("[version] 1\n[target] %d\n[entry]\n`stackable/../bad.stk` `[booster]` 1\n[/entry]", itemexpiration.GiftBoxHistoryDeadline)} {
		t.Run(name, func(t *testing.T) {
			rows, err := loadCurrentGiftBoxExpirationHistory(giftHistorySourceRound17{itemexpiration.GiftBoxHistoryPath: body})
			if name == "valid" {
				if err != nil || len(rows) != 1 {
					t.Fatalf("%v %+v", err, rows)
				}
			} else if err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestRealGiftBoxHistoryRound17(t *testing.T) {
	p := os.Getenv("DNF_GIFTBOX_HISTORY_CANDIDATE")
	if p == "" {
		t.Skip("set DNF_GIFTBOX_HISTORY_CANDIDATE")
	}
	a, err := platformpvf.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	def, err := catalog.ResolveItem(490008481)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(def.GiftBoxExpirationHistory, []uint32{giftHistoryOldRound17, 1830268800}) {
		t.Fatalf("actual history=%v", def.GiftBoxExpirationHistory)
	}
	got, _ := applyCurrentPVFItemExpirationAt(giftHistoryStackRound17(giftHistoryOldRound17, nil), def, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	if currentItemListStackExpire(got) != itemexpiration.GiftBoxHistoryDeadline {
		t.Fatal("real target did not renew")
	}
	// Match manifest paths the way the runtime finds them: run the production
	// document selection (root join, case-insensitive archive lookup and the
	// "(r)" revised-file fallback) for every listed item and use the document
	// path it actually selected as the manifest key. Joining a literal
	// "stackable/" prefix onto the list path cannot see an already rooted path,
	// a revised sibling or a differently cased stored name.
	selected, itemReadErrors := giftHistoryProductionReadPathsRound17(catalog)
	stored := map[string]string{}
	for _, file := range a.Files() {
		stored[strings.ToLower(file.ArchivePath)] = file.ArchivePath
	}
	// Raw list evidence: a manifest path may still be named by the item list
	// under an id the catalog cannot use, which the selected-path view hides.
	listedPaths, listedOwners, err := giftHistoryListedStackablePathsRound17(catalog)
	if err != nil {
		t.Fatal(err)
	}
	naive := map[string]bool{}
	for _, ref := range catalog.itemRefs {
		naive[strings.ToLower(string(ref.kind)+"/"+cleanDungeonDropPath(ref.path))] = true
	}
	// An optional reference archive proves whether an item-list index is absent
	// in the candidate only, or absent in both. Selection differences between
	// the two archives are measured, never assumed.
	var referenceSelected map[string][]uint32
	if reference := strings.TrimSpace(os.Getenv("DNF_GIFTBOX_HISTORY_REFERENCE")); reference != "" {
		referenceArchive, err := platformpvf.Open(reference)
		if err != nil {
			t.Fatal(err)
		}
		referenceCatalog, err := newPVFDungeonDropCatalog(referenceArchive)
		if err != nil {
			t.Fatal(err)
		}
		referenceSelected, _ = giftHistoryProductionReadPathsRound17(referenceCatalog)
	}

	report := giftHistoryPathAuditReport{
		Candidate:                p,
		ManifestPaths:            len(catalog.giftBoxHistory),
		ItemListReadErrors:       len(itemReadErrors),
		UnlistedByClassification: map[string]int{},
		NaiveKeyMissesByReason:   map[string]int{},
	}
	for key, row := range catalog.giftBoxHistory {
		entry := giftHistoryPathAudit{Path: key, ManifestKind: row.kind, ManifestHistory: append([]uint32(nil), row.history...), StoredName: stored[key]}
		entry.ListedItemIDs = listedPaths[strings.TrimPrefix(key, "stackable/")]
		entry.ReferenceItemIDs = referenceSelected[key]
		if id := giftHistoryTrailingIDRound17(path.Base(key)); id > 0 {
			entry.ListedPathForBasenameID = listedOwners[id]
		}
		if _, ok := naive[key]; !ok {
			report.NaiveKeyMisses++
			report.NaiveKeyMissesByReason[giftHistoryNaiveKeyMissReasonRound17(entry, stored, selected, listedPaths, catalog)]++
		}
		if ids := selected[key]; len(ids) > 0 {
			report.ItemListReachable++
			entry.Classification = "item_list_reachable"
			entry.ItemIDs = append([]uint32(nil), ids...)
			for _, id := range ids {
				item, err := catalog.ResolveItem(id)
				if err != nil {
					t.Fatalf("manifest path %s item %d does not resolve: %v", key, id, err)
				}
				if item.Kind != dungeonDropItemStackable || item.StackableType != row.kind || !reflect.DeepEqual(item.GiftBoxExpirationHistory, row.history) {
					t.Fatalf("manifest mismatch item=%d path=%s kind=%q type=%q history=%v", id, key, item.Kind, item.StackableType, item.GiftBoxExpirationHistory)
				}
			}
		} else {
			// Not reached through the two item lists the catalog resolves from.
			// That is not a global "no reference" claim, so verify the script
			// itself instead of skipping it.
			report.Unlisted++
			entry.Classification = giftHistoryUnlistedClassificationRound17(entry, stored, selected, listedPaths, catalog.itemRefs)
			report.UnlistedByClassification[entry.Classification]++
			if entry.StoredName == "" {
				t.Errorf("manifest path %s has no stored script in the candidate archive", key)
			} else if err := giftHistoryVerifyUnlistedScriptRound17(catalog, entry.StoredName, row); err != nil {
				t.Fatalf("unlisted manifest path %s: %v", key, err)
			}
		}
		report.Entries = append(report.Entries, entry)
	}
	sort.Slice(report.Entries, func(i, j int) bool { return report.Entries[i].Path < report.Entries[j].Path })
	if out := strings.TrimSpace(os.Getenv("DNF_GIFTBOX_HISTORY_PATH_AUDIT")); out != "" {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, append(body, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real manifest=%d paths; production item-list reachable=%d; unlisted=%d %v; naive prefix-key misses=%d %v; item list read errors=%d; 490008481 old/current dates verified",
		report.ManifestPaths, report.ItemListReachable, report.Unlisted, report.UnlistedByClassification, report.NaiveKeyMisses, report.NaiveKeyMissesByReason, report.ItemListReadErrors)
}

type giftHistoryPathAudit struct {
	Path                    string   `json:"path"`
	Classification          string   `json:"classification"`
	ManifestKind            string   `json:"manifest_kind"`
	ManifestHistory         []uint32 `json:"manifest_history"`
	ItemIDs                 []uint32 `json:"item_ids,omitempty"`
	ListedItemIDs           []int64  `json:"listed_item_ids,omitempty"`
	ListedPathForBasenameID string   `json:"listed_path_for_basename_id,omitempty"`
	ReferenceItemIDs        []uint32 `json:"reference_item_ids,omitempty"`
	StoredName              string   `json:"stored_name,omitempty"`
}

type giftHistoryPathAuditReport struct {
	Candidate                string                 `json:"candidate"`
	ManifestPaths            int                    `json:"manifest_paths"`
	ItemListReachable        int                    `json:"item_list_reachable"`
	Unlisted                 int                    `json:"unlisted"`
	UnlistedByClassification map[string]int         `json:"unlisted_by_classification"`
	NaiveKeyMisses           int                    `json:"naive_prefix_key_misses"`
	NaiveKeyMissesByReason   map[string]int         `json:"naive_prefix_key_misses_by_reason"`
	ItemListReadErrors       int                    `json:"item_list_read_errors"`
	Entries                  []giftHistoryPathAudit `json:"entries"`
}

// giftHistoryProductionReadPathsRound17 returns the manifest key each listed
// item actually resolves to, using the production read selection.
func giftHistoryProductionReadPathsRound17(catalog *pvfDungeonDropCatalog) (map[string][]uint32, map[string]int) {
	selected := map[string][]uint32{}
	readErrors := map[string]int{}
	for id, ref := range catalog.itemRefs {
		docPath, _, err := readDungeonDropText(catalog.source, string(ref.kind), ref.path)
		if err != nil {
			readErrors[string(ref.kind)+"/"+cleanDungeonDropPath(ref.path)]++
			continue
		}
		key := strings.ToLower(docPath)
		selected[key] = append(selected[key], id)
	}
	for key := range selected {
		ids := selected[key]
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	return selected, readErrors
}

// giftHistoryNaiveKeyMissReasonRound17 explains a manifest path the plain
// "stackable/" + list-path key does not match.
func giftHistoryNaiveKeyMissReasonRound17(entry giftHistoryPathAudit, stored map[string]string, selected map[string][]uint32, listed map[string][]int64, catalog *pvfDungeonDropCatalog) string {
	key := entry.Path
	if len(selected[key]) > 0 {
		if strings.Contains(strings.ToLower(path.Base(key)), "(r)") {
			return "production_selected_revised_sibling"
		}
		for _, ref := range catalog.itemRefs {
			if ref.kind == dungeonDropItemStackable && strings.ToLower(cleanDungeonDropPath(ref.path)) == key {
				return "production_selected_already_rooted_list_path"
			}
		}
		return "production_selected_differently_cased_path"
	}
	return giftHistoryUnlistedClassificationRound17(entry, stored, selected, listed, catalog.itemRefs)
}

// giftHistoryListedStackablePathsRound17 indexes every raw stackable.lst row,
// including rows the catalog drops because of an unusable or already claimed id.
func giftHistoryListedStackablePathsRound17(catalog *pvfDungeonDropCatalog) (map[string][]int64, map[int64]string, error) {
	text, err := catalog.source.ReadText(dungeonDropStackableList)
	if err != nil {
		return nil, nil, err
	}
	document, err := dnfpvf.Parse(dungeonDropStackableList, text)
	if err != nil {
		return nil, nil, err
	}
	out := map[string][]int64{}
	owners := map[int64]string{}
	for _, entry := range dnfpvf.ParseList(document) {
		cleaned := strings.ToLower(cleanDungeonDropPath(entry.Path))
		if cleaned == "" {
			continue
		}
		out[cleaned] = append(out[cleaned], entry.ID)
		if _, exists := owners[entry.ID]; !exists {
			owners[entry.ID] = cleaned
		}
	}
	return out, owners, nil
}

// giftHistoryTrailingIDRound17 reads the item id an archive basename carries,
// either as the whole name ("10000210.stk") or as a trailing suffix
// ("chn_stk_rose_love_10004383.stk"). It only explains an index miss; it never
// inserts an id into the catalog.
func giftHistoryTrailingIDRound17(base string) int64 {
	base = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(base)), ".stk")
	digits := base
	if idx := strings.LastIndexFunc(base, func(r rune) bool { return r < '0' || r > '9' }); idx >= 0 {
		digits = base[idx+1:]
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func giftHistoryUnlistedClassificationRound17(entry giftHistoryPathAudit, stored map[string]string, selected map[string][]uint32, listed map[string][]int64, itemRefs map[uint32]dungeonDropItemReference) string {
	key := entry.Path
	if stored[key] == "" {
		return "stored_script_absent"
	}
	if len(selected[strings.TrimPrefix(key, "stackable/")]) > 0 {
		return "same_script_selected_without_root"
	}
	ids := listed[strings.TrimPrefix(key, "stackable/")]
	if len(ids) == 0 {
		if entry.ListedPathForBasenameID != "" && entry.ListedPathForBasenameID != strings.TrimPrefix(key, "stackable/") {
			return "listed_id_points_to_other_document"
		}
		return "not_listed_in_stackable_list"
	}
	for _, id := range ids {
		if id <= 0 || id > int64(^uint32(0)) {
			return "listed_with_unusable_item_id"
		}
	}
	return "listed_path_lost_to_earlier_same_id_entry"
}

// giftHistoryVerifyUnlistedScriptRound17 checks an unreachable script through
// the real parse interface: it must still be the declared gift box kind with
// the extended deadline, and not a daily-delete or usable-period variant.
func giftHistoryVerifyUnlistedScriptRound17(catalog *pvfDungeonDropCatalog, storedName string, row currentGiftBoxHistoryRow) error {
	text, err := catalog.source.ReadText(storedName)
	if err != nil {
		return err
	}
	document, err := dnfpvf.Parse(storedName, text)
	if err != nil {
		return err
	}
	stackableType, _ := document.Text("stackable type")
	// The scope decision is the shared one, so a reviewed extra path outside
	// the canonical type set stays in scope here exactly as it does in the
	// bridge. The manifest must still name the type the script declares.
	if !itemexpiration.IsGiftBoxPath(storedName, stackableType) {
		return fmt.Errorf("stored %s stackable type %q is out of gift box scope", storedName, stackableType)
	}
	if stackableType != row.kind {
		return fmt.Errorf("stored %s stackable type %q want manifest %q", storedName, stackableType, row.kind)
	}
	expiration, err := parseCurrentPVFExpirationDate(document)
	if err != nil {
		return err
	}
	if got := currentPVFExpirationUnix(expiration); got != itemexpiration.GiftBoxHistoryDeadline {
		return fmt.Errorf("stored %s expiration %d want %d", storedName, got, itemexpiration.GiftBoxHistoryDeadline)
	}
	if flag, found := document.Int("daily delete item"); found && flag == 1 {
		return fmt.Errorf("stored %s is a daily delete item", storedName)
	}
	if days, found := document.Int("usable period"); found && days > 0 {
		return fmt.Errorf("stored %s declares usable period %d", storedName, days)
	}
	return nil
}

// TestGiftBoxHistoryRound17PathSelectionFollowsProductionReads pins the two
// selection gaps the real candidate does not currently exercise: a list path
// that already carries the root prefix and a script that only exists under the
// "(r)" revised basename. The plain "stackable/" + list-path key misses both,
// while the production read selection reaches them.
func TestGiftBoxHistoryRound17PathSelectionFollowsProductionReads(t *testing.T) {
	source := giftHistorySourceRound17{
		"stackable/x/y.stk":    "doc",
		"stackable/x/(r)z.stk": "doc",
		"x/dup.stk":            "doc",
		"stackable/x/dup.stk":  "doc",
	}
	catalog := &pvfDungeonDropCatalog{
		source: source,
		itemRefs: map[uint32]dungeonDropItemReference{
			1: {kind: dungeonDropItemStackable, path: "stackable/x/y.stk"},
			2: {kind: dungeonDropItemStackable, path: "x/z.stk"},
			3: {kind: dungeonDropItemStackable, path: "x/dup.stk"},
		},
	}
	selected, readErrors := giftHistoryProductionReadPathsRound17(catalog)
	if len(readErrors) != 0 {
		t.Fatalf("unexpected read errors %v", readErrors)
	}
	for key, wantID := range map[string]uint32{"stackable/x/y.stk": 1, "stackable/x/(r)z.stk": 2, "x/dup.stk": 3} {
		ids := selected[key]
		if len(ids) != 1 || ids[0] != wantID {
			t.Fatalf("production read selection for %s = %v want [%d]", key, ids, wantID)
		}
	}
	naive := map[string]bool{}
	for _, ref := range catalog.itemRefs {
		naive[strings.ToLower(string(ref.kind)+"/"+cleanDungeonDropPath(ref.path))] = true
	}
	for _, key := range []string{"stackable/x/y.stk", "stackable/x/(r)z.stk"} {
		if naive[key] {
			t.Fatalf("plain prefix key unexpectedly matched %s; the fixture no longer covers the selection gap", key)
		}
	}
	// A script reachable both with and without the root prefix selects the
	// unrooted document, so a manifest entry stored under the rooted name would
	// never be consulted. The classification keeps that separate from a script
	// no item list names at all.
	stored := map[string]string{"x/dup.stk": "x/dup.stk", "stackable/x/dup.stk": "stackable/x/dup.stk"}
	entry := giftHistoryPathAudit{Path: "stackable/x/dup.stk"}
	if got := giftHistoryUnlistedClassificationRound17(entry, stored, selected, map[string][]int64{}, catalog.itemRefs); got != "same_script_selected_without_root" {
		t.Fatalf("duplicate-root classification=%q", got)
	}
}
