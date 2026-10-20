// anton-minimum changes only the explicitly authorized normal Anton minimum.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const target = "etc/raid/anton.etc"

func main() {
	in := flag.String("in", "", "current PVF")
	out := flag.String("out", "", "new separate PVF")
	report := flag.String("report", "", "audit JSON")
	flag.Parse()
	if err := run(*in, *out, *report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(in, out, report string) error {
	if in == "" || out == "" || report == "" || strings.EqualFold(in, out) {
		return fmt.Errorf("separate input, output and report required")
	}
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	oldSHA := sha256.Sum256(data)
	if hex.EncodeToString(oldSHA[:]) != "91fba6fd494838aa06c263904e6a99c505d6f9389e5b7fc275df8f4485b7cc12" {
		return fmt.Errorf("current v93 PVF SHA mismatch")
	}
	a, err := platformpvf.OpenBytes(data)
	if err != nil {
		return err
	}
	before, err := raid.LoadAdmissionRule(a, target)
	if err != nil {
		return err
	}
	if before.MinimumMembers != 10 || before.SoloMinimumMembers != 1 || before.MaximumMembers != 20 || before.ItemID != 10096296 || before.Count != 1 {
		return fmt.Errorf("unexpected original admission rule: %+v", before)
	}
	raw, err := a.ReadRaw(target)
	if err != nil {
		return err
	}
	if len(raw)%5 != 0 {
		return fmt.Errorf("invalid token stream")
	}
	tokens := []platformpvf.ScriptToken{}
	changed, byteOffset := 0, -1
	for pos := 0; pos < len(raw); pos += 5 {
		kind := raw[pos]
		value := int32(binary.LittleEndian.Uint32(raw[pos+1:]))
		token := platformpvf.ScriptToken{Type: kind, Value: value}
		if kind == 3 || kind == 5 || kind == 6 || kind == 7 {
			token.Text, token.Value = a.ResolveString(int(value)), 0
		}
		if kind == 3 && token.Text == "[RAID START MINIMUM MEMBER]" {
			if pos+10 > len(raw) || raw[pos+5] != 0 || binary.LittleEndian.Uint32(raw[pos+6:]) != 10 {
				return fmt.Errorf("unexpected minimum token")
			}
			byteOffset, changed = pos+6, changed+1
		}
		if pos+1 == byteOffset {
			token.Value = 2
		}
		tokens = append(tokens, token)
	}
	if changed != 1 {
		return fmt.Errorf("expected exactly one minimum section, got %d", changed)
	}
	updated, err := a.RepackScriptAdditions([]platformpvf.ScriptAddition{{Path: target, AppendToExisting: true, ReplaceExistingTokens: true, AppendTokens: tokens}})
	if err != nil {
		return err
	}
	b, err := platformpvf.OpenBytes(updated)
	if err != nil {
		return err
	}
	unchanged, err := a.VerifyScriptReplacementPreservation(b, []string{target})
	if err != nil {
		return err
	}
	newRaw, err := b.ReadRaw(target)
	if err != nil {
		return err
	}
	expected := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(expected[byteOffset:], 2)
	if !bytes.Equal(newRaw, expected) {
		return fmt.Errorf("unplanned token changes")
	}
	after, err := raid.LoadAdmissionRule(b, target)
	if err != nil {
		return err
	}
	want := before
	want.MinimumMembers = 2
	if after != want {
		return fmt.Errorf("unexpected new rule: %+v", after)
	}
	oldLuke, err := a.ReadRaw("etc/raid/luke/luke.etc")
	if err != nil {
		return err
	}
	newLuke, err := b.ReadRaw("etc/raid/luke/luke.etc")
	if err != nil || !bytes.Equal(oldLuke, newLuke) {
		return fmt.Errorf("Luke rule changed")
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
	newSHA := sha256.Sum256(updated)
	proof := map[string]any{"passed": true, "input_sha256": hex.EncodeToString(oldSHA[:]), "output_sha256": hex.EncodeToString(newSHA[:]), "output_bytes": len(updated), "changed_path": target, "minimum_before": 10, "minimum_after": 2, "single_minimum": 1, "maximum_members": 20, "ticket_id": after.ItemID, "ticket_count_each": after.Count, "script_changed_bytes": 1, "script_byte_offset": byteOffset, "unchanged_archive_files": unchanged, "archive_file_count": a.FileCount(), "existing_encrypted_chunks_preserved": true, "luke_unchanged": true, "physical_ui_accepted": false}
	encoded, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(report, encoded, 0644); err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
