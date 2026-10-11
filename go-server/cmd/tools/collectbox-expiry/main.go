// collectbox-expiry performs a guarded, single-script PVF maintenance change.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const target = "localization/collectbox/collectboxinfo.etc"
const expiry = "2028-12-31 06:00:00"

func main() {
	in := flag.String("in", "", "current PVF")
	out := flag.String("out", "", "new PVF; must not exist")
	wantSHA := flag.String("sha256", "", "required source SHA256")
	report := flag.String("report", "", "JSON audit output")
	flag.Parse()
	if err := run(*in, *out, *wantSHA, *report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(in, out, wantSHA, report string) error {
	if in == "" || out == "" || len(wantSHA) != 64 || report == "" {
		return fmt.Errorf("in, out, sha256 and report are required")
	}
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.ToLower(wantSHA) {
		return fmt.Errorf("source SHA256 mismatch")
	}
	a, err := platformpvf.OpenBytes(data)
	if err != nil {
		return err
	}
	text, err := a.ReadText(target)
	if err != nil {
		return err
	}
	doc, err := dnfpvf.Parse(target, text)
	if err != nil {
		return err
	}
	dates := regexp.MustCompile(`20\d{2}年\d{1,2}月\d{1,2}日(?:维护前)?`)
	changes := []platformpvf.ScriptStringChange{}
	seen := map[string]bool{}
	base, max, explanations, boxes := 0, 0, 0, 0
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		if name == "collect box info" {
			boxes++
		}
		if name != "base expiration date" && name != "max expiration date" && name != "box explain" {
			continue
		}
		for _, token := range doc.Tokens[section.Start:section.End] {
			if token.Kind != dnfpvf.TokenString {
				continue
			}
			old := token.Value
			updated := expiry
			if name == "box explain" {
				if !dates.MatchString(old) {
					return fmt.Errorf("missing date in box explanation")
				}
				updated = dates.ReplaceAllString(old, "2028年12月31日06:00")
				updated = strings.ReplaceAll(updated, "2月1日以后", "2028年12月31日06:00以后")
				explanations++
			} else if name == "base expiration date" {
				base++
			} else {
				max++
			}
			if old != updated && !seen[old] {
				changes = append(changes, platformpvf.ScriptStringChange{Old: old, New: updated})
				seen[old] = true
			}
		}
	}
	if boxes != 5 || base != 5 || max != 5 || explanations != 5 {
		return fmt.Errorf("unexpected catalog counts: boxes=%d base=%d max=%d explain=%d", boxes, base, max, explanations)
	}
	updated, err := a.RepackScriptAdditions([]platformpvf.ScriptAddition{{Path: target, AppendToExisting: true, Strings: changes}})
	if err != nil {
		return err
	}
	next, err := platformpvf.OpenBytes(updated)
	if err != nil {
		return err
	}
	unchanged, err := a.VerifyScriptReplacementPreservation(next, []string{target})
	if err != nil {
		return err
	}
	newText, err := next.ReadText(target)
	if err != nil {
		return err
	}
	expected := text
	for _, change := range changes {
		expected = strings.ReplaceAll(expected, "`"+change.Old+"`", "`"+change.New+"`")
	}
	if newText != expected || strings.Count(newText, "`"+expiry+"`") != 10 {
		return fmt.Errorf("replacement text differs from exact planned changes")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = f.Write(updated)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	newSum := sha256.Sum256(updated)
	proof := map[string]any{"source_sha256": wantSHA, "output_sha256": hex.EncodeToString(newSum[:]), "output_size": len(updated), "changed_path": target, "unchanged_files": unchanged, "file_count": a.FileCount(), "boxes": boxes, "expiry_utc8": expiry, "base_dates": base, "max_dates": max, "explanations": explanations, "changes": changes, "verified": true}
	encoded, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(report, encoded, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(report+".txt", []byte(newText), 0644); err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
