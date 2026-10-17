// giftbox-expiry extends declared gift-box deadlines without changing rewards.
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
	"reflect"
	"sort"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const deadline = "2028-12-31 06:00:00"
const dateLayout = "2006-01-02 15:04:05"

var cst = time.FixedZone("CST", 8*60*60)

type change struct {
	Path  string `json:"path"`
	Type  string `json:"stackable_type"`
	Token int    `json:"token_index"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

func main() {
	in := flag.String("in", "", "source PVF")
	want := flag.String("sha256", "", "required source SHA256")
	out := flag.String("out", "", "candidate PVF; must not exist")
	report := flag.String("report", "", "JSON audit; must not exist")
	flag.Parse()
	if err := run(*in, *want, *out, *report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// boxScope is the single scope answer used by both rewrite and the run index
// filter: the canonical box types plus the exact reviewed (path, type) pairs.
// Keeping one helper in front of itemexpiration.IsGiftBoxPath prevents a state
// where rewrite() edits a file that the run() filter would skip.
func boxScope(path, kind string) bool {
	return itemexpiration.IsGiftBoxPath(path, kind)
}

func withHistory(tokens []platformpvf.ScriptToken, edits []change) ([]platformpvf.ScriptToken, error) {
	for i, token := range tokens {
		if token.Type == 3 && (token.Text == "[usable period]" || token.Text == "[daily delete item]") && i+1 < len(tokens) && tokens[i+1].Type == 0 && tokens[i+1].Value > 0 {
			return tokens, nil
		}
	}
	for _, token := range tokens {
		if token.Type == 3 && token.Text == "["+itemexpiration.GiftBoxHistorySection+"]" {
			return nil, fmt.Errorf("input already declares local giftbox history")
		}
	}
	values := map[int64]bool{}
	cutoff := time.Date(2026, 10, 16, 0, 0, 0, 0, cst)
	for _, edit := range edits {
		old, err := time.ParseInLocation(dateLayout, edit.Old, cst)
		if err != nil {
			return nil, err
		}
		values[old.Unix()] = true
		// The existing retired-date policy used this fixed horizon for these
		// already retired source dates. Do not add it for future source dates.
		if old.Before(cutoff) {
			values[time.Date(2028, 1, 1, 0, 0, 0, 0, cst).Unix()] = true
		}
	}
	keys := make([]int64, 0, len(values))
	for v := range values {
		keys = append(keys, v)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := append([]platformpvf.ScriptToken(nil), tokens...)
	result = append(result, platformpvf.ScriptToken{Type: 3, Text: "[" + itemexpiration.GiftBoxHistorySection + "]"})
	for _, v := range keys {
		if v <= 0 || v > 2147483647 {
			return nil, fmt.Errorf("history exceeds signed PVF integer")
		}
		result = append(result, platformpvf.ScriptToken{Type: 0, Value: int32(v)})
	}
	result = append(result, platformpvf.ScriptToken{Type: 3, Text: "[/" + itemexpiration.GiftBoxHistorySection + "]"})
	return result, nil
}

// rewrite only changes the single quoted value immediately following a date
// tag. Repeated date tags are all validated and extended, never collapsed, and
// repeated [stackable type] declarations must every one of them be a box for
// this exact path: the scope decision is per path, not per type family.
func rewrite(path string, tokens []platformpvf.ScriptToken) ([]platformpvf.ScriptToken, []change, string, error) {
	kind := ""
	boxType, otherType := false, false
	for i, t := range tokens {
		if t.Type == 3 && t.Text == "[stackable type]" {
			if i+1 >= len(tokens) || tokens[i+1].Type != 6 {
				return nil, nil, "", fmt.Errorf("%s: malformed stackable type", path)
			}
			declaredKind := tokens[i+1].Text
			if kind == "" {
				kind = declaredKind // Match the runtime document's first declaration.
			}
			inScope := boxScope(path, declaredKind)
			boxType = boxType || inScope
			otherType = otherType || !inScope
		}
	}
	if !boxType {
		return tokens, nil, kind, nil
	}
	if otherType {
		return nil, nil, kind, fmt.Errorf("%s: conflicting box/non-box type declarations", path)
	}
	limit, _ := time.ParseInLocation(dateLayout, deadline, cst)
	result := append([]platformpvf.ScriptToken(nil), tokens...)
	var changes []change
	for i, t := range tokens {
		if t.Type != 3 || t.Text != "[expiration date]" {
			continue
		}
		if i+1 >= len(tokens) || tokens[i+1].Type != 6 || (i+2 < len(tokens) && tokens[i+2].Type != 3) {
			return nil, nil, kind, fmt.Errorf("%s: malformed expiration at token %d", path, i)
		}
		old := tokens[i+1].Text
		if old == "" { // Empty/absent expiration is permanent; do not add a limit.
			continue
		}
		value, err := time.ParseInLocation(dateLayout, old, cst)
		if err != nil || value.Format(dateLayout) != old {
			return nil, nil, kind, fmt.Errorf("%s: invalid expiration %q", path, old)
		}
		if !value.Before(limit) { // An extension must never shorten a later date.
			continue
		}
		result[i+1].Text = deadline
		changes = append(changes, change{path, kind, i + 1, old, deadline})
	}
	return result, changes, kind, nil
}

func readTokens(a *platformpvf.Archive, path string) ([]platformpvf.ScriptToken, error) {
	raw, err := a.ReadRaw(path)
	if err != nil || len(raw)%5 != 0 {
		return nil, fmt.Errorf("read tokens %s: %v", path, err)
	}
	result := make([]platformpvf.ScriptToken, 0, len(raw)/5)
	for pos := 0; pos < len(raw); pos += 5 {
		t := platformpvf.ScriptToken{Type: raw[pos], Value: int32(binary.LittleEndian.Uint32(raw[pos+1 : pos+5]))}
		switch t.Type {
		case 0, 2:
		case 3, 5, 6, 7:
			t.Text = a.ResolveString(int(t.Value))
			t.Value = 0
		default:
			return nil, fmt.Errorf("%s: unsupported token kind %d at %d", path, t.Type, pos/5)
		}
		result = append(result, t)
	}
	return result, nil
}

func run(in, want, out, report string) error {
	if in == "" || len(want) != 64 || out == "" || report == "" {
		return fmt.Errorf("in, sha256, out and report are required")
	}
	for _, path := range []string{out, report} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return fmt.Errorf("output must not exist: %s", path)
		}
	}
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.ToLower(want) {
		return fmt.Errorf("source SHA256 mismatch")
	}
	a, err := platformpvf.OpenBytes(data)
	if err != nil {
		return err
	}
	files := a.Files()
	sort.Slice(files, func(i, j int) bool { return files[i].ArchivePath < files[j].ArchivePath })
	var additions []platformpvf.ScriptAddition
	var paths []string
	var changes []change
	planned := map[string][]platformpvf.ScriptToken{}
	boxCounts := map[string]int{}
	historyTokens := []platformpvf.ScriptToken{{Type: 3, Text: "[version]"}, {Type: 0, Value: 1}, {Type: 3, Text: "[target]"}, {Type: 0, Value: int32(itemexpiration.GiftBoxHistoryDeadline)}}
	historyEntries := 0
	var scanned, permanentOrLater int
	for _, f := range files {
		path := f.ArchivePath
		if !strings.HasPrefix(strings.ToLower(path), "stackable/") || !strings.HasSuffix(strings.ToLower(path), ".stk") || f.DataType != 1 {
			continue
		}
		tokens, err := readTokens(a, path)
		if err != nil {
			return err
		}
		scanned++
		updated, edits, kind, err := rewrite(path, tokens)
		if err != nil {
			return err
		}
		if !boxScope(path, kind) {
			continue
		}
		boxCounts[kind]++
		if len(edits) == 0 {
			permanentOrLater++
			continue
		}
		withOrigins, historyErr := withHistory(updated, edits)
		err = historyErr
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if len(withOrigins) > len(updated) {
			historyTokens = append(historyTokens, platformpvf.ScriptToken{Type: 3, Text: "[entry]"}, platformpvf.ScriptToken{Type: 6, Text: path}, platformpvf.ScriptToken{Type: 6, Text: kind})
			historyTokens = append(historyTokens, withOrigins[len(updated)+1:len(withOrigins)-1]...)
			historyTokens = append(historyTokens, platformpvf.ScriptToken{Type: 3, Text: "[/entry]"})
			historyEntries++
		}
		paths = append(paths, path)
		changes = append(changes, edits...)
		planned[path] = updated
		additions = append(additions, platformpvf.ScriptAddition{Path: path, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: updated})
	}
	if len(paths) == 0 {
		return fmt.Errorf("no earlier declared gift-box dates found")
	}
	additions = append(additions, platformpvf.ScriptAddition{Path: itemexpiration.GiftBoxHistoryPath, TemplatePath: paths[0], ReplaceTokens: true, AppendTokens: historyTokens})
	updated, err := a.RepackScriptAdditions(additions)
	if err != nil {
		return err
	}
	next, err := platformpvf.OpenBytes(updated)
	if err != nil {
		return err
	}
	unchanged, err := a.VerifyScriptReplacementAndAdditionPreservation(next, paths, []string{itemexpiration.GiftBoxHistoryPath})
	if err != nil {
		return err
	}
	if got, err := readTokens(next, itemexpiration.GiftBoxHistoryPath); err != nil || !reflect.DeepEqual(got, historyTokens) {
		return fmt.Errorf("history manifest readback: %v", err)
	}
	for _, path := range paths {
		got, err := readTokens(next, path)
		if err != nil || !reflect.DeepEqual(got, planned[path]) {
			return fmt.Errorf("exact token readback failed %s: %v", path, err)
		}
	}
	for _, path := range []string{out, report} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
	}
	if err := writeNew(out, updated); err != nil {
		return err
	}
	newSHA := sha256.Sum256(updated)
	receipt := map[string]any{"passed": true, "input_sha256": hex.EncodeToString(sum[:]), "output_sha256": hex.EncodeToString(newSHA[:]), "output": out, "deadline_cst": deadline, "scanned_stackable_scripts": scanned, "box_counts": boxCounts, "files_changed": len(paths), "date_tokens_changed": len(changes), "permanent_or_later_unchanged": permanentOrLater, "unchanged_archive_entries": unchanged, "file_count": a.FileCount(), "only_expiration_value_tokens_changed": true, "changes": changes, "deployed": false, "native_acceptance": false}
	receipt["history_manifest_path"] = itemexpiration.GiftBoxHistoryPath
	receipt["history_entries"] = historyEntries
	receipt["only_expiration_value_tokens_changed_in_original_files"] = true
	receipt["added_backend_provenance_files"] = 1
	blob, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(report, append(blob, '\n'))
}

func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
