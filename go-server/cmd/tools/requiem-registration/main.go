// requiem-registration repairs two missing references in the official resource
// snapshot and writes a separate archive. It never edits runtime files.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"path/filepath"
)

const inputSHA = "95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c"
const minimapPath = "region/minimap/shonan.mm"
const minimapSHA = "3e3e109df4473f67528abe3fa60b4b867aa5e06c103d971efea82e940c9fbfbe"
const listPath = "itemshop/itemshop.lst"
const listSHA = "465f3ebce19f9c2b9be01bd90f3963efaed0f1ee83524470af4c0a75ceda8f21"
const shopPath = "itemshop/iris2.shp"
const shopSHA = "53069a94a2cf5387032e46e4e582782abbf67a8303897d1a2fdc6ffba8d321a3"

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func repair(data []byte) ([]byte, int, error) {
	if digest(data) != inputSHA {
		return nil, 0, fmt.Errorf("official resources-selected-20261101 PVF required")
	}
	a, e := archive.OpenBytes(data)
	if e != nil {
		return nil, 0, e
	}
	raw, e := a.ReadRaw(minimapPath)
	if e != nil {
		return nil, 0, e
	}
	if digest(raw) != minimapSHA {
		return nil, 0, fmt.Errorf("unreviewed minimap")
	}
	mid, n, e := repairMinimap(a)
	if e != nil {
		return nil, 0, e
	}
	if n != a.FileCount()-1 {
		return nil, 0, fmt.Errorf("minimap preservation count mismatch")
	}
	b, e := archive.OpenBytes(mid)
	if e != nil {
		return nil, 0, e
	}
	out, n, e := repairIrisShop(b)
	if e != nil {
		return nil, 0, e
	}
	if n != a.FileCount()-1 {
		return nil, 0, fmt.Errorf("shop preservation count mismatch")
	}
	return out, a.FileCount() - 2, nil
}
func writeNew(p string, b []byte) error {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	c := f.Close()
	if e != nil {
		return e
	}
	return c
}
func run(in, out, report string) error {
	seen := map[string]bool{}
	for _, p := range []string{in, out, report} {
		if p == "" {
			return fmt.Errorf("input, separate output and report required")
		}
		a, e := filepath.Abs(p)
		if e != nil {
			return e
		}
		if seen[a] {
			return fmt.Errorf("paths must differ")
		}
		seen[a] = true
	}
	for _, p := range []string{out, report} {
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			return fmt.Errorf("refuse existing output: %s", p)
		}
	}
	data, e := os.ReadFile(in)
	if e != nil {
		return e
	}
	output, count, e := repair(data)
	if e != nil {
		return e
	}
	proof, e := json.MarshalIndent(map[string]any{"input_release": "resources-selected-20261101", "input_sha256": digest(data), "output_sha256": digest(output), "changed_paths": []string{minimapPath, listPath}, "unchanged_archive_files": count, "restored_town": 13, "restored_area": 0, "restored_shop": 63, "npc": 132, "existing_string_offsets_preserved": true, "other_archive_files_preserved": true, "matching_client_and_server_pvf_required": true, "official_output_gameplay_accepted": false, "deployed": false}, "", "  ")
	if e != nil {
		return e
	}
	if e = writeNew(out, output); e != nil {
		return e
	}
	return writeNew(report, append(proof, '\n'))
}
func main() {
	in := flag.String("in", "", "official PVF")
	out := flag.String("out", "", "new candidate PVF")
	report := flag.String("report", "", "new audit JSON")
	flag.Parse()
	if e := run(*in, *out, *report); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
