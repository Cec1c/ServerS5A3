// sky-cubes-pvf adds the selected Tian 2-7 dream avatar cubes only.
// It has no hell-drop, activity, shop or other four-requests changes.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	cubeTemplatePath = "stackable/cash/chn_20150521_Absolute_Bindcube/chn_2015_Absolute_Upper_Bindcube_10007847.stk"
	box990032001Path = "stackable/private_seria_luck/990032001.stk"
	stackableLstPath = "stackable/stackable.lst"
	absoluteCubePath = "etc/chn_absolute_bind_cube.etc"

	updatedBox990032001Explain = "开启后，可以从第2套至第7套全颜色、全套装梦想装扮合成器中选择获得1个。各合成器原有的职业限制和规则保持不变。"
)

var avatarPartOrder = []string{"hair", "hat", "face", "neck", "coat", "belt", "pants", "shoes"}

type cubeSpec struct {
	ItemID  int32                       `json:"item_id"`
	Key     int32                       `json:"key"`
	Path    string                      `json:"path"`
	LstRel  string                      `json:"lst_rel"`
	Name    string                      `json:"name"`
	Explain string                      `json:"explain"`
	IconImg string                      `json:"icon_img"`
	IconIdx int32                       `json:"icon_idx"`
	Jobs    []string                    `json:"jobs"`
	Outputs map[string]map[string]int32 `json:"outputs"`
}

type item1Spec struct {
	NewCubes         []cubeSpec `json:"new_cubes"`
	Box990032001List []int32    `json:"box_990032001_items"`
}

func main() {
	inPath := flag.String("in", "", "baseline Script.pvf")
	outPath := flag.String("out", "", "output candidate Script.pvf")
	specPath := flag.String("spec", "", "path to item1_final_spec.json")
	reportPath := flag.String("report", "", "optional JSON audit report output path")
	wantSHA := flag.String("sha256", "", "required SHA256 of the baseline PVF")
	flag.Parse()
	if err := run(*inPath, *outPath, *specPath, *reportPath, *wantSHA); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func decodeTokens(a *platformpvf.Archive, path string) ([]byte, []platformpvf.ScriptToken, error) {
	raw, err := a.ReadRaw(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read raw %s: %w", path, err)
	}
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, nil, fmt.Errorf("invalid token stream for %s (len=%d)", path, len(raw))
	}
	tokens := make([]platformpvf.ScriptToken, 0, len(raw)/5)
	for pos := 0; pos < len(raw); pos += 5 {
		kind := raw[pos]
		val := int32(binary.LittleEndian.Uint32(raw[pos+1:]))
		tok := platformpvf.ScriptToken{Type: kind, Value: val}
		if kind == 3 || kind == 5 || kind == 6 || kind == 7 {
			tok.Text = a.ResolveString(int(val))
			tok.Value = 0
		}
		tokens = append(tokens, tok)
	}
	return raw, tokens, nil
}

func buildCubeTokens(c cubeSpec) []platformpvf.ScriptToken {
	toks := []platformpvf.ScriptToken{
		{Type: 3, Text: "[name]"},
		{Type: 6, Text: c.Name},
		{Type: 3, Text: "[explain]"},
		{Type: 6, Text: c.Explain},
		{Type: 3, Text: "[grade]"},
		{Type: 0, Value: 1},
		{Type: 3, Text: "[rarity]"},
		{Type: 0, Value: 2},
		{Type: 3, Text: "[usable job]"},
	}
	for _, job := range c.Jobs {
		toks = append(toks, platformpvf.ScriptToken{Type: 6, Text: job})
	}
	toks = append(toks,
		platformpvf.ScriptToken{Type: 3, Text: "[/usable job]"},
		platformpvf.ScriptToken{Type: 3, Text: "[attach type]"},
		platformpvf.ScriptToken{Type: 6, Text: "[account]"},
		platformpvf.ScriptToken{Type: 3, Text: "[minimum level]"},
		platformpvf.ScriptToken{Type: 0, Value: 1},
		platformpvf.ScriptToken{Type: 3, Text: "[price]"},
		platformpvf.ScriptToken{Type: 0, Value: 100000},
		platformpvf.ScriptToken{Type: 3, Text: "[value]"},
		platformpvf.ScriptToken{Type: 0, Value: 0},
		platformpvf.ScriptToken{Type: 3, Text: "[icon]"},
		platformpvf.ScriptToken{Type: 6, Text: c.IconImg},
		platformpvf.ScriptToken{Type: 0, Value: c.IconIdx},
		platformpvf.ScriptToken{Type: 3, Text: "[stackable type]"},
		platformpvf.ScriptToken{Type: 6, Text: "[etc]"},
		platformpvf.ScriptToken{Type: 0, Value: 0},
		platformpvf.ScriptToken{Type: 3, Text: "[action type]"},
		platformpvf.ScriptToken{Type: 6, Text: "[absolute bind cube]"},
		platformpvf.ScriptToken{Type: 0, Value: c.Key},
		platformpvf.ScriptToken{Type: 0, Value: 8},
		platformpvf.ScriptToken{Type: 0, Value: 2},
		platformpvf.ScriptToken{Type: 0, Value: 0},
		platformpvf.ScriptToken{Type: 0, Value: 0},
		platformpvf.ScriptToken{Type: 3, Text: "[/action type]"},
		platformpvf.ScriptToken{Type: 3, Text: "[move wav]"},
		platformpvf.ScriptToken{Type: 6, Text: "SCRAP_TOUCH"},
		platformpvf.ScriptToken{Type: 3, Text: "[impossible contents]"},
		platformpvf.ScriptToken{Type: 6, Text: "gift"},
		platformpvf.ScriptToken{Type: 3, Text: "[/impossible contents]"},
	)
	return toks
}

func buildUpdatedBox990032001Tokens(orig []platformpvf.ScriptToken, items []int32) ([]platformpvf.ScriptToken, error) {
	out := make([]platformpvf.ScriptToken, 0, len(orig)+len(items)*2)
	explainUpdated := 0
	stackableUpdated := 0
	for i := 0; i < len(orig); i++ {
		tok := orig[i]
		if tok.Type == 3 && tok.Text == "[explain]" {
			if i+1 >= len(orig) || orig[i+1].Type != 6 {
				return nil, fmt.Errorf("990032001.stk unexpected [explain] shape")
			}
			out = append(out, tok, platformpvf.ScriptToken{Type: 6, Text: updatedBox990032001Explain})
			explainUpdated++
			i++
			continue
		}
		if tok.Type == 3 && tok.Text == "[stackable]" {
			out = append(out, tok)
			for _, id := range items {
				out = append(out,
					platformpvf.ScriptToken{Type: 0, Value: id},
					platformpvf.ScriptToken{Type: 0, Value: 1},
				)
			}
			// Skip old entries until [/stackable]
			closed := false
			for j := i + 1; j < len(orig); j++ {
				if orig[j].Type == 3 && orig[j].Text == "[/stackable]" {
					out = append(out, orig[j])
					i = j
					closed = true
					break
				}
			}
			if !closed {
				return nil, fmt.Errorf("990032001.stk missing [/stackable]")
			}
			stackableUpdated++
			continue
		}
		out = append(out, tok)
	}
	if explainUpdated != 1 || stackableUpdated != 1 {
		return nil, fmt.Errorf("990032001.stk expected 1 explain & 1 stackable section, got %d / %d", explainUpdated, stackableUpdated)
	}
	return out, nil
}

func run(inPath, outPath, specPath, reportPath, wantSHA string) error {
	if inPath == "" || outPath == "" || specPath == "" || len(wantSHA) != 64 {
		return fmt.Errorf("-in, -out, -spec and -sha256 are required")
	}
	paths := map[string]string{"input": inPath, "output": outPath, "spec": specPath}
	if reportPath != "" {
		paths["report"] = reportPath
	}
	if err := ensureDistinct(paths); err != nil {
		return err
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("output already exists: %s", outPath)
		}
		return err
	}
	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	var spec item1Spec
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}
	if len(spec.NewCubes) != 0 && len(spec.NewCubes) != 5 {
		return fmt.Errorf("spec must contain zero (A20-only) or five (generic) new cubes, got %d", len(spec.NewCubes))
	}
	if len(spec.Box990032001List) != 14 {
		return fmt.Errorf("expected 14 cubes in box_990032001_items, got %d", len(spec.Box990032001List))
	}

	inBytes, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read input PVF: %w", err)
	}
	inSHA := sha256.Sum256(inBytes)
	if hex.EncodeToString(inSHA[:]) != strings.ToLower(wantSHA) {
		return fmt.Errorf("input SHA256 mismatch: got %s want %s", hex.EncodeToString(inSHA[:]), wantSHA)
	}
	a, err := platformpvf.OpenBytes(inBytes)
	if err != nil {
		return fmt.Errorf("open input PVF: %w", err)
	}

	// Verify stackable.lst has no collision with new cube IDs or paths.
	_, lstTokens, err := decodeTokens(a, stackableLstPath)
	if err != nil {
		return err
	}
	existingIDs := make(map[int32]bool, len(lstTokens)/2)
	existingLstPaths := make(map[string]bool, len(lstTokens)/2)
	existingPathByID := make(map[int32]string, len(lstTokens)/2)
	for i := 0; i+1 < len(lstTokens); i += 2 {
		if lstTokens[i].Type == 0 && lstTokens[i+1].Type == 6 {
			existingIDs[lstTokens[i].Value] = true
			existingLstPaths[strings.ToLower(lstTokens[i+1].Text)] = true
			existingPathByID[lstTokens[i].Value] = lstTokens[i+1].Text
		}
	}

	// Verify etc/chn_absolute_bind_cube.etc has no collision with new keys.
	_, absTokens, err := decodeTokens(a, absoluteCubePath)
	if err != nil {
		return err
	}
	existingKeys := make(map[int32]bool)
	for i := 0; i+1 < len(absTokens); i++ {
		if absTokens[i].Type == 3 && absTokens[i].Text == "[absolute bind cube]" && absTokens[i+1].Type == 0 {
			existingKeys[absTokens[i+1].Value] = true
		}
	}
	if len(spec.NewCubes) == 0 {
		if len(existingKeys) != 14 {
			return fmt.Errorf("A20 outer update requires existing absolute bind keys 0..13, got %d keys", len(existingKeys))
		}
		for key := int32(0); key <= 13; key++ {
			if !existingKeys[key] {
				return fmt.Errorf("A20 outer update is missing existing absolute bind key %d", key)
			}
		}
	} else {
		if len(existingKeys) != 9 {
			return fmt.Errorf("generic five-cube input must contain absolute bind keys 0..8 exactly, got %d keys", len(existingKeys))
		}
		for key := int32(0); key <= 8; key++ {
			if !existingKeys[key] {
				return fmt.Errorf("generic five-cube input is missing absolute bind key %d", key)
			}
		}
	}

	additions := make([]platformpvf.ScriptAddition, 0, len(spec.NewCubes)+4)
	addedPaths := make([]string, 0, len(spec.NewCubes))
	lstAppend := make([]platformpvf.ScriptToken, 0, len(spec.NewCubes)*2)
	absAppend := make([]platformpvf.ScriptToken, 0, 1200)
	newIDs := make(map[int32]bool, len(spec.NewCubes))

	for _, c := range spec.NewCubes {
		if c.ItemID <= 0 || c.Key <= 0 || c.Path == "" || c.LstRel == "" || c.Name == "" || c.Explain == "" || c.IconImg == "" || len(c.Jobs) == 0 {
			return fmt.Errorf("incomplete cube spec: %+v", c)
		}
		if a.FindFileIndex(c.Path) >= 0 {
			return fmt.Errorf("new cube path already exists in PVF: %s", c.Path)
		}
		if existingIDs[c.ItemID] || newIDs[c.ItemID] {
			return fmt.Errorf("duplicate stackable ID: %d", c.ItemID)
		}
		if existingLstPaths[strings.ToLower(c.LstRel)] {
			return fmt.Errorf("duplicate stackable.lst rel path: %s", c.LstRel)
		}
		if existingKeys[c.Key] {
			return fmt.Errorf("absolute bind cube key already exists: %d", c.Key)
		}
		existingKeys[c.Key] = true
		newIDs[c.ItemID] = true

		additions = append(additions, platformpvf.ScriptAddition{
			Path:          c.Path,
			TemplatePath:  cubeTemplatePath,
			ReplaceTokens: true,
			AppendTokens:  buildCubeTokens(c),
		})
		addedPaths = append(addedPaths, c.Path)
		lstAppend = append(lstAppend,
			platformpvf.ScriptToken{Type: 0, Value: c.ItemID},
			platformpvf.ScriptToken{Type: 6, Text: c.LstRel},
		)

		absAppend = append(absAppend,
			platformpvf.ScriptToken{Type: 3, Text: "[absolute bind cube]"},
			platformpvf.ScriptToken{Type: 0, Value: c.Key},
			platformpvf.ScriptToken{Type: 3, Text: "[absolute bind plus rare]"},
		)
		for _, job := range c.Jobs {
			parts, ok := c.Outputs[job]
			if !ok {
				return fmt.Errorf("key %d missing outputs for job %s", c.Key, job)
			}
			absAppend = append(absAppend, platformpvf.ScriptToken{Type: 6, Text: job})
			for _, part := range avatarPartOrder {
				eqID := parts[part]
				if eqID <= 0 {
					return fmt.Errorf("key %d job %s missing part %s", c.Key, job, part)
				}
				absAppend = append(absAppend,
					platformpvf.ScriptToken{Type: 6, Text: part},
					platformpvf.ScriptToken{Type: 0, Value: eqID},
				)
			}
		}
		absAppend = append(absAppend,
			platformpvf.ScriptToken{Type: 3, Text: "[/absolute bind plus rare]"},
			platformpvf.ScriptToken{Type: 3, Text: "[/absolute bind cube]"},
		)
	}

	seenBox := make(map[int32]bool, len(spec.Box990032001List))
	for _, id := range spec.Box990032001List {
		if seenBox[id] {
			return fmt.Errorf("duplicate ID %d in box_990032001_items", id)
		}
		seenBox[id] = true
		if !existingIDs[id] && !newIDs[id] {
			return fmt.Errorf("box_990032001_items contains unknown stackable ID %d", id)
		}
		if rel, ok := existingPathByID[id]; ok {
			full := strings.Trim(rel, `/\`)
			if !strings.HasPrefix(strings.ToLower(full), "stackable/") {
				full = "stackable/" + full
			}
			if a.FindFileIndex(full) < 0 {
				return fmt.Errorf("box_990032001_items references missing stackable file for %d: %s", id, rel)
			}
		}
	}

	_, boxTokens, err := decodeTokens(a, box990032001Path)
	if err != nil {
		return err
	}
	updatedBoxTokens, err := buildUpdatedBox990032001Tokens(boxTokens, spec.Box990032001List)
	if err != nil {
		return err
	}

	changedPaths := []string{}
	if len(lstAppend) != 0 {
		additions = append(additions, platformpvf.ScriptAddition{Path: stackableLstPath, AppendToExisting: true, AppendTokens: lstAppend})
		changedPaths = append(changedPaths, stackableLstPath)
	}
	if len(absAppend) != 0 {
		additions = append(additions, platformpvf.ScriptAddition{Path: absoluteCubePath, AppendToExisting: true, AppendTokens: absAppend})
		changedPaths = append(changedPaths, absoluteCubePath)
	}
	if !scriptTokensEqual(updatedBoxTokens, boxTokens) {
		additions = append(additions, platformpvf.ScriptAddition{Path: box990032001Path, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: updatedBoxTokens})
		changedPaths = append(changedPaths, box990032001Path)
	}

	updatedBytes := append([]byte(nil), inBytes...)
	next := a
	unchanged := a.FileCount()
	if len(additions) != 0 {
		updatedBytes, err = a.RepackScriptAdditions(additions)
		if err != nil {
			return fmt.Errorf("repack PVF: %w", err)
		}
		next, err = platformpvf.OpenBytes(updatedBytes)
		if err != nil {
			return fmt.Errorf("re-open patched PVF: %w", err)
		}
		unchanged, err = a.VerifyScriptReplacementAndAdditionPreservation(next, changedPaths, addedPaths)
		if err != nil {
			return fmt.Errorf("preservation check failed: %w", err)
		}
	}

	// Verify all new/changed scripts decode cleanly via ReadText.
	for _, p := range append(append([]string(nil), addedPaths...), changedPaths...) {
		txt, err := next.ReadText(p)
		if err != nil || len(txt) == 0 {
			return fmt.Errorf("patched file %s failed ReadText: %w", p, err)
		}
	}

	if err := writeFileAtomic(outPath, updatedBytes, true); err != nil {
		return fmt.Errorf("write output PVF: %w", err)
	}
	outSHA := sha256.Sum256(updatedBytes)

	cubeSummary := make([]map[string]any, 0, len(spec.NewCubes))
	for _, c := range spec.NewCubes {
		cubeSummary = append(cubeSummary, map[string]any{
			"item_id":    c.ItemID,
			"key":        c.Key,
			"path":       c.Path,
			"name":       c.Name,
			"jobs_count": len(c.Jobs),
		})
	}
	report := map[string]any{
		"passed":                  true,
		"input_sha256":            hex.EncodeToString(inSHA[:]),
		"input_bytes":             len(inBytes),
		"output_sha256":           hex.EncodeToString(outSHA[:]),
		"output_bytes":            len(updatedBytes),
		"archive_files_before":    a.FileCount(),
		"archive_files_after":     next.FileCount(),
		"unchanged_archive_files": unchanged,
		"added_paths":             addedPaths,
		"changed_paths":           changedPaths,
		"new_cubes":               cubeSummary,
		"box_990032001_items":     spec.Box990032001List,
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if reportPath != "" {
		if err := writeFileAtomic(reportPath, encoded, false); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}
	fmt.Println(string(encoded))
	return nil
}

func scriptTokensEqual(left, right []platformpvf.ScriptToken) bool {
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
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pvf-write-*")
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
