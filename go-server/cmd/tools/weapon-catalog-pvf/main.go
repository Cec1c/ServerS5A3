// weapon-catalog-pvf applies the selected weapon-skin catalog contract to a PVF.
// It verifies the full native catalog content, appends only missing selected
// index rows, applies the Linus fifth-shop tab and two mold prices, and proves
// that no unrelated file changed.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const growEffectTemplate = "character/groweffect.etc"

const (
	equipmentListPath = "equipment/equipment.lst"
	stackableListPath = "stackable/stackable.lst"
	npcLinusPath      = "npc/linus.npc"
	linusShopPath     = "itemshop/equipmentshop1.shp"
	moldMagicPath     = "stackable/cash/chn_490704411.stk"
	moldSecretPath    = "stackable/cash/chn_490704427.stk"
)

type planToken struct {
	Type  byte
	Text  string
	Value int32
}

func (t *planToken) UnmarshalJSON(data []byte) error {
	var f []json.RawMessage
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	if len(f) != 2 {
		return fmt.Errorf("plan token must be [type,value]")
	}
	var kind int
	if err := json.Unmarshal(f[0], &kind); err != nil {
		return err
	}
	t.Type = byte(kind)
	switch kind {
	case 3, 5, 6, 7:
		return json.Unmarshal(f[1], &t.Text)
	case 0, 2:
		return json.Unmarshal(f[1], &t.Value)
	default:
		return fmt.Errorf("unsupported plan token type %d", kind)
	}
}

type contentSpec struct {
	Path   string      `json:"path"`
	SHA256 string      `json:"sha256"`
	Tokens []planToken `json:"tokens"`
}

type listRow struct {
	ID   int32
	Path string
}

func (r *listRow) UnmarshalJSON(data []byte) error {
	var f []json.RawMessage
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	if len(f) != 2 {
		return fmt.Errorf("list row must be [id,path]")
	}
	if err := json.Unmarshal(f[0], &r.ID); err != nil {
		return err
	}
	return json.Unmarshal(f[1], &r.Path)
}

type appendList struct {
	Path string    `json:"path"`
	Rows []listRow `json:"rows"`
}

type npcSpec struct {
	Path         string      `json:"path"`
	RoleMarker   string      `json:"role_marker"`
	InsertTokens []planToken `json:"insert_tokens"`
	AppendTokens []planToken `json:"append_tokens"`
}

type moldPrice struct {
	Path  string `json:"path"`
	Price int32  `json:"price"`
}

type linusSpec struct {
	Path       string      `json:"path"`
	TabName    string      `json:"tab_name"`
	JobIDs     []int32     `json:"job_ids"`
	ItemIDs    []int32     `json:"item_ids"`
	MoldPrices []moldPrice `json:"mold_prices"`
}

type expectedSpec struct {
	FileCountAfter int `json:"file_count_after"`
}

type plan struct {
	Schema                 int           `json:"schema"`
	Recipe                 string        `json:"recipe"`
	InputSHA256            string        `json:"input_sha256"`
	InputBytes             int64         `json:"input_bytes"`
	FileCountBefore        int           `json:"file_count_before"`
	AllowedContentPrefixes []string      `json:"allowed_content_prefixes"`
	Content                []contentSpec `json:"content"`
	AppendLists            []appendList  `json:"append_lists"`
	LinusShop              *linusSpec    `json:"linus_shop"`
	NPC                    *npcSpec      `json:"npc"`
	Expected               expectedSpec  `json:"expected"`
}

type report struct {
	Passed                 bool     `json:"passed"`
	Recipe                 string   `json:"recipe"`
	InputSHA256            string   `json:"input_sha256"`
	InputBytes             int64    `json:"input_bytes"`
	OutputSHA256           string   `json:"output_sha256"`
	OutputBytes            int64    `json:"output_bytes"`
	FileCountBefore        int      `json:"file_count_before"`
	FileCountAfter         int      `json:"file_count_after"`
	PlanSHA256             string   `json:"plan_sha256"`
	ContentPaths           int      `json:"content_paths"`
	ContentChanged         int      `json:"content_changed"`
	ContentAdded           int      `json:"content_added"`
	ContentSame            int      `json:"content_same"`
	AppendedEquipmentRows  int      `json:"appended_equipment_rows"`
	AppendedStackableRows  int      `json:"appended_stackable_rows"`
	LinusShopChanged       bool     `json:"linus_shop_changed"`
	MoldPriceChanged       bool     `json:"mold_price_changed"`
	NPCChanged             bool     `json:"npc_changed"`
	ChangedPaths           []string `json:"changed_paths"`
	AddedPaths             []string `json:"added_paths"`
	UnchangedFilesVerified int      `json:"unchanged_files_verified"`
	UsedPlanInputSHA       bool     `json:"used_plan_input_sha"`
}

func main() {
	in := flag.String("in", "", "input Script.pvf")
	out := flag.String("out", "", "new PVF; must not exist")
	planPath := flag.String("plan", "", "weapon catalog plan JSON or JSON.gz")
	reportPath := flag.String("report", "", "JSON report output")
	wantSHA := flag.String("sha256", "", "required SHA256 of the input PVF")
	acceptExtra := flag.String("accept-input-sha256", "", "comma-separated additional explicitly accepted input hashes")
	flag.Parse()
	if err := run(*in, *out, *planPath, *reportPath, *wantSHA, *acceptExtra); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(inPath, outPath, planPath, reportPath, wantSHA, acceptExtra string) error {
	if inPath == "" || outPath == "" || planPath == "" || reportPath == "" || len(wantSHA) != 64 {
		return fmt.Errorf("-in, -out, -plan, -report and -sha256 are required")
	}
	if err := ensureDistinct(map[string]string{"input": inPath, "output": outPath, "plan": planPath, "report": reportPath}); err != nil {
		return err
	}
	if _, err := os.Stat(outPath); err == nil {
		return fmt.Errorf("output already exists: %s", outPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	decodedPlan, planSHA, err := loadPlan(planPath)
	if err != nil {
		return err
	}
	if err := validatePlanBoundaries(decodedPlan); err != nil {
		return err
	}
	input, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	inputSHA := sha256.Sum256(input)
	inputHex := hex.EncodeToString(inputSHA[:])
	if inputHex != strings.ToLower(wantSHA) {
		return fmt.Errorf("input SHA256 mismatch: got %s want %s", inputHex, wantSHA)
	}
	accepted := map[string]bool{strings.ToLower(decodedPlan.InputSHA256): true}
	for _, value := range strings.Split(acceptExtra, ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			accepted[value] = true
		}
	}
	if !accepted[inputHex] {
		return fmt.Errorf("input SHA256 %s is not the plan base and was not explicitly accepted", inputHex)
	}
	if inputHex == strings.ToLower(decodedPlan.InputSHA256) && decodedPlan.InputBytes != 0 && int64(len(input)) != decodedPlan.InputBytes {
		return fmt.Errorf("input byte count mismatch: got %d want %d", len(input), decodedPlan.InputBytes)
	}
	archive, err := platformpvf.OpenBytes(input)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	if inputHex == strings.ToLower(decodedPlan.InputSHA256) && decodedPlan.FileCountBefore != 0 && archive.FileCount() != decodedPlan.FileCountBefore {
		return fmt.Errorf("input file count mismatch: got %d want %d", archive.FileCount(), decodedPlan.FileCountBefore)
	}

	var additions []platformpvf.ScriptAddition
	var changedPaths, addedPaths []string
	changedSet := map[string]bool{}
	addedSet := map[string]bool{}
	contentSame, contentChanged, contentAdded := 0, 0, 0
	finalContentTokens := make(map[string][]platformpvf.ScriptToken, len(decodedPlan.Content))
	for _, item := range decodedPlan.Content {
		path := normalizePath(item.Path)
		if path == "" || strings.Contains(path, "..") || !allowedPrefix(path, decodedPlan.AllowedContentPrefixes) {
			return fmt.Errorf("invalid or unselected content path %q", item.Path)
		}
		if changedSet[path] || addedSet[path] {
			return fmt.Errorf("duplicate content path in plan: %s", path)
		}
		wantTokens := toScriptTokens(item.Tokens)
		if got := tokenSHA(wantTokens); got != item.SHA256 {
			return fmt.Errorf("plan content SHA256 mismatch for %s: got %s want %s", path, got, item.SHA256)
		}
		if isMoldPath(path) {
			price := int32(1000000)
			if path == moldSecretPath {
				price = 2000000
			}
			finalized, _, err := setPriceValue(wantTokens, price)
			if err != nil {
				return fmt.Errorf("prepare mold overlay %s: %w", path, err)
			}
			finalContentTokens[path] = finalized
			continue
		}
		finalContentTokens[path] = wantTokens
		if archive.FindFileIndex(path) >= 0 {
			raw, err := archive.ReadRaw(path)
			if err != nil {
				return err
			}
			if len(item.Tokens) == 0 {
				if len(raw) != 0 {
					return fmt.Errorf("zero-token plan path has nonzero existing content: %s", path)
				}
				contentSame++
				continue
			}
			current, err := decodeRawTokens(archive, raw, path)
			if err != nil {
				return err
			}
			if tokensEqual(wantTokens, current) {
				contentSame++
				continue
			}
			additions = append(additions, platformpvf.ScriptAddition{Path: path, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: wantTokens})
			changedPaths = append(changedPaths, path)
			changedSet[path] = true
			contentChanged++
			continue
		}
		if len(item.Tokens) == 0 {
			return fmt.Errorf("cannot add a zero-token path through the PVF repack: %s", path)
		}
		additions = append(additions, platformpvf.ScriptAddition{Path: path, TemplatePath: growEffectTemplate, ReplaceTokens: true, AppendTokens: wantTokens})
		addedPaths = append(addedPaths, path)
		addedSet[path] = true
		contentAdded++
	}

	equipmentAppended, stackableAppended, expectedLists, err := applyLists(archive, decodedPlan.AppendLists, &additions, &changedPaths, changedSet)
	if err != nil {
		return err
	}
	npcChanged, expectedNPC, err := applyNPC(archive, decodedPlan.NPC, &additions, &changedPaths, changedSet)
	if err != nil {
		return err
	}
	shopChanged, moldChanged, expectedShop, expectedMolds, err := applyLinus(archive, decodedPlan.LinusShop, &additions, &changedPaths, changedSet)
	if err != nil {
		return err
	}
	for _, overlay := range []map[string][]platformpvf.ScriptToken{expectedLists, expectedNPC, expectedShop} {
		for path, tokens := range overlay {
			if _, ok := finalContentTokens[path]; ok {
				finalContentTokens[path] = tokens
			}
		}
	}
	if len(additions) == 0 {
		return fmt.Errorf("no changes: input already matches the selected catalog plan")
	}

	updated, err := archive.RepackScriptAdditions(additions)
	if err != nil {
		return fmt.Errorf("repack PVF: %w", err)
	}
	next, err := platformpvf.OpenBytes(updated)
	if err != nil {
		return fmt.Errorf("re-open patched PVF: %w", err)
	}
	unchanged, err := archive.VerifyScriptReplacementAndAdditionPreservation(next, changedPaths, addedPaths)
	if err != nil {
		return fmt.Errorf("preservation check failed: %w", err)
	}
	if inputHex == strings.ToLower(decodedPlan.InputSHA256) && decodedPlan.Expected.FileCountAfter != 0 && next.FileCount() != decodedPlan.Expected.FileCountAfter {
		return fmt.Errorf("output file count mismatch: got %d want %d", next.FileCount(), decodedPlan.Expected.FileCountAfter)
	}
	for _, item := range decodedPlan.Content {
		path := normalizePath(item.Path)
		wantTokens := finalContentTokens[path]
		if len(wantTokens) == 0 {
			raw, err := next.ReadRaw(path)
			if err != nil {
				return fmt.Errorf("verify zero-token content %s: %w", path, err)
			}
			if len(raw) != 0 {
				return fmt.Errorf("zero-token plan path has nonzero output content: %s", path)
			}
			if got := tokenSHA(nil); got != item.SHA256 {
				return fmt.Errorf("zero-token content SHA256 mismatch for %s: got %s want %s", path, got, item.SHA256)
			}
			continue
		}
		actual, err := decodeTokens(next, path)
		if err != nil {
			return fmt.Errorf("verify content %s: %w", item.Path, err)
		}
		if got, want := tokenSHA(actual), tokenSHA(wantTokens); got != want {
			return fmt.Errorf("content token SHA256 mismatch for %s: got %s want %s", item.Path, got, want)
		}
	}
	for label, expected := range map[string]map[string][]platformpvf.ScriptToken{"list": expectedLists, "npc": expectedNPC, "shop": expectedShop, "mold": expectedMolds} {
		for path, want := range expected {
			got, err := decodeTokens(next, path)
			if err != nil {
				return fmt.Errorf("verify %s %s: %w", label, path, err)
			}
			if !tokensEqual(got, want) {
				return fmt.Errorf("%s token stream mismatch: %s", label, path)
			}
		}
	}
	outSHA := sha256.Sum256(updated)
	report := report{
		Passed: true, Recipe: decodedPlan.Recipe, InputSHA256: inputHex, InputBytes: int64(len(input)),
		OutputSHA256: hex.EncodeToString(outSHA[:]), OutputBytes: int64(len(updated)),
		FileCountBefore: archive.FileCount(), FileCountAfter: next.FileCount(), PlanSHA256: planSHA,
		ContentPaths: len(decodedPlan.Content), ContentChanged: contentChanged, ContentAdded: contentAdded, ContentSame: contentSame,
		AppendedEquipmentRows: equipmentAppended, AppendedStackableRows: stackableAppended,
		LinusShopChanged: shopChanged, MoldPriceChanged: moldChanged, NPCChanged: npcChanged,
		ChangedPaths: changedPaths, AddedPaths: addedPaths, UnchangedFilesVerified: unchanged,
		UsedPlanInputSHA: inputHex == strings.ToLower(decodedPlan.InputSHA256),
	}
	if err := writeFileAtomic(outPath, updated, true); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if err := writeJSON(reportPath, report); err != nil {
		return err
	}
	fmt.Printf("passed=true input=%s output=%s files=%d->%d changed=%d added=%d content_same=%d\n", inputHex, report.OutputSHA256, report.FileCountBefore, report.FileCountAfter, len(changedPaths), len(addedPaths), contentSame)
	return nil
}

func loadPlan(path string) (*plan, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read plan: %w", err)
	}
	plain := data
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "", fmt.Errorf("open plan gzip: %w", err)
		}
		defer reader.Close()
		plain, err = io.ReadAll(reader)
		if err != nil {
			return nil, "", fmt.Errorf("read plan gzip: %w", err)
		}
	}
	var doc plan
	if err := json.Unmarshal(plain, &doc); err != nil {
		return nil, "", fmt.Errorf("parse plan: %w", err)
	}
	if doc.Schema != 1 || doc.Recipe == "" || len(doc.Content) == 0 || len(doc.InputSHA256) != 64 {
		return nil, "", fmt.Errorf("invalid plan header")
	}
	sum := sha256.Sum256(data)
	return &doc, hex.EncodeToString(sum[:]), nil
}

func validatePlanBoundaries(p *plan) error {
	if p == nil || len(p.Content) != 40032 {
		return fmt.Errorf("weapon plan content boundary must contain exactly 40032 selected paths")
	}
	rowCounts := map[string]int{equipmentListPath: 13329, stackableListPath: 26660}
	seenLists := map[string]bool{}
	for _, list := range p.AppendLists {
		path := normalizePath(list.Path)
		want, ok := rowCounts[path]
		if !ok || seenLists[path] {
			return fmt.Errorf("unexpected or duplicate append-list path: %s", list.Path)
		}
		if len(list.Rows) != want {
			return fmt.Errorf("append-list %s rows=%d want=%d", path, len(list.Rows), want)
		}
		seenLists[path] = true
	}
	for path := range rowCounts {
		if !seenLists[path] {
			return fmt.Errorf("required append-list path missing: %s", path)
		}
	}
	if p.NPC == nil || normalizePath(p.NPC.Path) != npcLinusPath || p.NPC.RoleMarker != "[/role]" || len(p.NPC.InsertTokens) == 0 || len(p.NPC.AppendTokens) == 0 {
		return fmt.Errorf("NPC boundary must target %s with [/role] and nonempty tokens", npcLinusPath)
	}
	if p.LinusShop == nil || normalizePath(p.LinusShop.Path) != linusShopPath {
		return fmt.Errorf("shop boundary must target %s", linusShopPath)
	}
	if p.LinusShop.TabName != "幻化" || !int32SlicesEqual(p.LinusShop.JobIDs, []int32{0, 11, 7, 1, 5, 2, 8, 3, 4, 14, 6, 9, 10, 12, 13, 15}) || !int32SlicesEqual(p.LinusShop.ItemIDs, []int32{490704411, 490704427}) {
		return fmt.Errorf("Linus shop tab/job/item boundary mismatch")
	}
	if len(p.LinusShop.MoldPrices) != 2 {
		return fmt.Errorf("mold boundary must contain exactly two entries")
	}
	if normalizePath(p.LinusShop.MoldPrices[0].Path) != moldMagicPath || p.LinusShop.MoldPrices[0].Price != 1000000 {
		return fmt.Errorf("magic mold boundary mismatch")
	}
	if normalizePath(p.LinusShop.MoldPrices[1].Path) != moldSecretPath || p.LinusShop.MoldPrices[1].Price != 2000000 {
		return fmt.Errorf("secret mold boundary mismatch")
	}
	return nil
}

func int32SlicesEqual(left, right []int32) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func applyLists(a *platformpvf.Archive, specs []appendList, additions *[]platformpvf.ScriptAddition, changedPaths *[]string, changedSet map[string]bool) (int, int, map[string][]platformpvf.ScriptToken, error) {
	expected := map[string][]platformpvf.ScriptToken{}
	equipmentAppended, stackableAppended := 0, 0
	for _, spec := range specs {
		path := normalizePath(spec.Path)
		if path == "" || changedSet[path] {
			return 0, 0, nil, fmt.Errorf("invalid or duplicate append-list path %q", spec.Path)
		}
		current, err := decodeTokens(a, path)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("read list %s: %w", path, err)
		}
		expected[path] = append([]platformpvf.ScriptToken(nil), current...)
		ids, paths := listMaps(current)
		seen := map[int32]bool{}
		var appendTokens []platformpvf.ScriptToken
		for _, row := range spec.Rows {
			if row.Path == "" || seen[row.ID] {
				return 0, 0, nil, fmt.Errorf("invalid or duplicate row %d in %s", row.ID, path)
			}
			seen[row.ID] = true
			if old, ok := ids[row.ID]; ok {
				if !strings.EqualFold(old, row.Path) {
					return 0, 0, nil, fmt.Errorf("list id %d already maps to %s, not %s", row.ID, old, row.Path)
				}
				continue
			}
			if oldID, ok := paths[strings.ToLower(row.Path)]; ok && oldID != row.ID {
				return 0, 0, nil, fmt.Errorf("list path %s already maps to %d, not %d", row.Path, oldID, row.ID)
			}
			appendTokens = append(appendTokens, platformpvf.ScriptToken{Type: 0, Value: row.ID}, platformpvf.ScriptToken{Type: 6, Text: row.Path})
		}
		if len(appendTokens) == 0 {
			continue
		}
		*additions = append(*additions, platformpvf.ScriptAddition{Path: path, AppendToExisting: true, AppendTokens: appendTokens})
		*changedPaths = append(*changedPaths, path)
		changedSet[path] = true
		expected[path] = append(expected[path], appendTokens...)
		if path == "equipment/equipment.lst" {
			equipmentAppended = len(appendTokens) / 2
		} else if path == "stackable/stackable.lst" {
			stackableAppended = len(appendTokens) / 2
		}
	}
	return equipmentAppended, stackableAppended, expected, nil
}

func applyNPC(a *platformpvf.Archive, spec *npcSpec, additions *[]platformpvf.ScriptAddition, changedPaths *[]string, changedSet map[string]bool) (bool, map[string][]platformpvf.ScriptToken, error) {
	expected := map[string][]platformpvf.ScriptToken{}
	if spec == nil {
		return false, expected, nil
	}
	path := normalizePath(spec.Path)
	if path == "" || changedSet[path] {
		return false, nil, fmt.Errorf("invalid npc path")
	}
	current, err := decodeTokens(a, path)
	if err != nil {
		return false, nil, fmt.Errorf("read npc %s: %w", path, err)
	}
	insert := toScriptTokens(spec.InsertTokens)
	appendTokens := toScriptTokens(spec.AppendTokens)
	markers := matchingIndexes(current, platformpvf.ScriptToken{Type: 3, Text: spec.RoleMarker})
	if len(markers) != 1 {
		return false, nil, fmt.Errorf("npc role marker %q count=%d", spec.RoleMarker, len(markers))
	}
	marker := markers[0]
	alreadyInsert := marker >= len(insert) && tokensEqual(current[marker-len(insert):marker], insert)
	alreadyAppend := len(current) >= len(appendTokens) && tokensEqual(current[len(current)-len(appendTokens):], appendTokens)
	if containsTokens(current, insert) && !alreadyInsert {
		return false, nil, fmt.Errorf("npc insertion exists at an unexpected position")
	}
	if containsTokens(current, appendTokens) && !alreadyAppend {
		return false, nil, fmt.Errorf("npc append block exists at an unexpected position")
	}
	if alreadyInsert && alreadyAppend {
		return false, expected, nil
	}
	next := append([]platformpvf.ScriptToken(nil), current...)
	if !alreadyInsert {
		tail := append([]platformpvf.ScriptToken(nil), next[marker:]...)
		next = append(next[:marker], insert...)
		next = append(next, tail...)
	}
	if !alreadyAppend {
		next = append(next, appendTokens...)
	}
	*additions = append(*additions, platformpvf.ScriptAddition{Path: path, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: next})
	*changedPaths = append(*changedPaths, path)
	changedSet[path] = true
	expected[path] = next
	return true, expected, nil
}

func applyLinus(a *platformpvf.Archive, spec *linusSpec, additions *[]platformpvf.ScriptAddition, changedPaths *[]string, changedSet map[string]bool) (bool, bool, map[string][]platformpvf.ScriptToken, map[string][]platformpvf.ScriptToken, error) {
	expectedShop := map[string][]platformpvf.ScriptToken{}
	expectedMolds := map[string][]platformpvf.ScriptToken{}
	if spec == nil {
		return false, false, expectedShop, expectedMolds, nil
	}
	shopPath := normalizePath(spec.Path)
	if shopPath == "" || changedSet[shopPath] {
		return false, false, nil, nil, fmt.Errorf("invalid shop path")
	}
	shop, err := decodeTokens(a, shopPath)
	if err != nil {
		return false, false, nil, nil, fmt.Errorf("read shop: %w", err)
	}
	tab := buildShopTab(spec)
	if !containsTokens(shop, tab) {
		markers := matchingIndexes(shop, platformpvf.ScriptToken{Type: 3, Text: "[/sell info]"})
		if len(markers) != 1 {
			return false, false, nil, nil, fmt.Errorf("shop [/sell info] count=%d", len(markers))
		}
		if containsTabName(shop, spec.TabName) {
			return false, false, nil, nil, fmt.Errorf("shop tab %q exists with different contents", spec.TabName)
		}
		nextShop := append([]platformpvf.ScriptToken(nil), shop[:markers[0]]...)
		nextShop = append(nextShop, tab...)
		nextShop = append(nextShop, shop[markers[0]:]...)
		*additions = append(*additions, platformpvf.ScriptAddition{Path: shopPath, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: nextShop})
		*changedPaths = append(*changedPaths, shopPath)
		changedSet[shopPath] = true
		expectedShop[shopPath] = nextShop
	}
	moldChanged := false
	for _, mold := range spec.MoldPrices {
		path := normalizePath(mold.Path)
		if path == "" || changedSet[path] {
			return false, false, nil, nil, fmt.Errorf("invalid mold path %q", mold.Path)
		}
		current, err := decodeTokens(a, path)
		if err != nil {
			return false, false, nil, nil, fmt.Errorf("read mold %s: %w", path, err)
		}
		next, changed, err := setPriceValue(current, mold.Price)
		if err != nil {
			return false, false, nil, nil, fmt.Errorf("mold %s: %w", path, err)
		}
		expectedMolds[path] = next
		if !changed {
			continue
		}
		moldChanged = true
		*additions = append(*additions, platformpvf.ScriptAddition{Path: path, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: next})
		*changedPaths = append(*changedPaths, path)
		changedSet[path] = true
	}
	return len(expectedShop) != 0, moldChanged, expectedShop, expectedMolds, nil
}

func buildShopTab(spec *linusSpec) []platformpvf.ScriptToken {
	out := []platformpvf.ScriptToken{{Type: 3, Text: "[tab]"}, {Type: 6, Text: spec.TabName}}
	for _, job := range spec.JobIDs {
		out = append(out, platformpvf.ScriptToken{Type: 3, Text: "[category entry]"}, platformpvf.ScriptToken{Type: 3, Text: "[id]"}, platformpvf.ScriptToken{Type: 0, Value: job}, platformpvf.ScriptToken{Type: 3, Text: "[item list]"})
		for _, item := range spec.ItemIDs {
			out = append(out, platformpvf.ScriptToken{Type: 0, Value: item})
		}
		out = append(out, platformpvf.ScriptToken{Type: 3, Text: "[/item list]"}, platformpvf.ScriptToken{Type: 3, Text: "[/category entry]"})
	}
	return append(out, platformpvf.ScriptToken{Type: 3, Text: "[/tab]"})
}

func setPriceValue(tokens []platformpvf.ScriptToken, price int32) ([]platformpvf.ScriptToken, bool, error) {
	priceIdx := matchingIndexes(tokens, platformpvf.ScriptToken{Type: 3, Text: "[price]"})
	valueIdx := matchingIndexes(tokens, platformpvf.ScriptToken{Type: 3, Text: "[value]"})
	if len(priceIdx) == 0 && len(valueIdx) == 0 {
		next := append(append([]platformpvf.ScriptToken(nil), tokens...), platformpvf.ScriptToken{Type: 3, Text: "[price]"}, platformpvf.ScriptToken{Type: 0, Value: price}, platformpvf.ScriptToken{Type: 3, Text: "[value]"}, platformpvf.ScriptToken{Type: 0, Value: 0})
		return next, true, nil
	}
	if len(priceIdx) != 1 || len(valueIdx) != 1 || priceIdx[0]+1 >= len(tokens) || valueIdx[0]+1 >= len(tokens) {
		return nil, false, fmt.Errorf("partial or duplicate [price]/[value]")
	}
	if tokens[priceIdx[0]+1].Type != 0 || tokens[priceIdx[0]+1].Value != price || tokens[valueIdx[0]+1].Type != 0 || tokens[valueIdx[0]+1].Value != 0 {
		return nil, false, fmt.Errorf("existing [price]/[value] differs from plan")
	}
	return tokens, false, nil
}

func decodeTokens(a *platformpvf.Archive, path string) ([]platformpvf.ScriptToken, error) {
	raw, err := a.ReadRaw(path)
	if err != nil {
		return nil, err
	}
	return decodeRawTokens(a, raw, path)
}

func decodeRawTokens(a *platformpvf.Archive, raw []byte, path string) ([]platformpvf.ScriptToken, error) {
	offset := 0
	if len(raw) >= 2 && raw[0] == 0xb0 && raw[1] == 0xd0 {
		offset = 2
	}
	if len(raw)-offset == 0 || (len(raw)-offset)%5 != 0 {
		return nil, fmt.Errorf("invalid token stream for %s (len=%d)", path, len(raw))
	}
	out := make([]platformpvf.ScriptToken, 0, (len(raw)-offset)/5)
	for pos := offset; pos < len(raw); pos += 5 {
		kind := raw[pos]
		value := int32(binary.LittleEndian.Uint32(raw[pos+1:]))
		token := platformpvf.ScriptToken{Type: kind, Value: value}
		if kind == 3 || kind == 5 || kind == 6 || kind == 7 {
			token.Text = a.ResolveString(int(value))
			token.Value = 0
		}
		out = append(out, token)
	}
	return out, nil
}

func tokenSHA(tokens []platformpvf.ScriptToken) string {
	h := sha256.New()
	for _, token := range tokens {
		h.Write([]byte{token.Type})
		if token.Type == 3 || token.Type == 5 || token.Type == 6 || token.Type == 7 {
			h.Write([]byte(token.Text))
			h.Write([]byte{0})
		} else {
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(token.Value))
			h.Write(buf[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func listMaps(tokens []platformpvf.ScriptToken) (map[int32]string, map[string]int32) {
	ids := map[int32]string{}
	paths := map[string]int32{}
	for i := 0; i+1 < len(tokens); i += 2 {
		if (tokens[i].Type == 0 || tokens[i].Type == 2) && (tokens[i+1].Type == 5 || tokens[i+1].Type == 6 || tokens[i+1].Type == 7) {
			ids[tokens[i].Value] = tokens[i+1].Text
			paths[strings.ToLower(tokens[i+1].Text)] = tokens[i].Value
		}
	}
	return ids, paths
}

func allowedPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isMoldPath(path string) bool {
	return path == moldMagicPath || path == moldSecretPath
}

func normalizePath(value string) string {
	return strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
}

func toScriptTokens(tokens []planToken) []platformpvf.ScriptToken {
	out := make([]platformpvf.ScriptToken, len(tokens))
	for i, token := range tokens {
		out[i] = platformpvf.ScriptToken{Type: token.Type, Text: token.Text, Value: token.Value}
	}
	return out
}

func tokensEqual(left, right []platformpvf.ScriptToken) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func containsTokens(haystack, needle []platformpvf.ScriptToken) bool {
	if len(needle) == 0 {
		return true
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if tokensEqual(haystack[start:start+len(needle)], needle) {
			return true
		}
	}
	return false
}

func containsTabName(tokens []platformpvf.ScriptToken, name string) bool {
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].Type == 3 && tokens[i].Text == "[tab]" && tokens[i+1].Type == 6 && tokens[i+1].Text == name {
			return true
		}
	}
	return false
}

func matchingIndexes(tokens []platformpvf.ScriptToken, want platformpvf.ScriptToken) []int {
	out := []int{}
	for i, token := range tokens {
		if token == want {
			out = append(out, i)
		}
	}
	return out
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, false)
}

func ensureDistinct(paths map[string]string) error {
	seen := map[string]string{}
	for name, path := range paths {
		if path == "" {
			continue
		}
		canonical, err := canonicalPath(path)
		if err != nil {
			return err
		}
		for other, otherPath := range seen {
			if samePath(canonical, otherPath) {
				return fmt.Errorf("%s and %s paths must be distinct", name, other)
			}
		}
		seen[name] = canonical
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func writeFileAtomic(path string, data []byte, exclusive bool) error {
	if exclusive {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("output already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pvf-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !exclusive {
		_ = os.Remove(path)
	}
	return os.Rename(tmpName, path)
}
