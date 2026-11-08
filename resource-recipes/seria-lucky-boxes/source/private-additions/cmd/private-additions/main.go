// Private one-off template additions, kept separate from the upstream CLI.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	p "pvfcli/internal/pvf"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

type Plan struct {
	InputSHA256 string             `json:"input_sha256"`
	Additions   []p.ScriptAddition `json:"additions"`
}

func main() {
	in := flag.String("pvf", "", "input PVF")
	planPath := flag.String("plan", "", "addition plan")
	out := flag.String("out", "", "separate output")
	report := flag.String("report", "", "preservation report")
	flag.Parse()
	if *in == "" || *planPath == "" || *out == "" || *report == "" {
		panic("all flags required")
	}
	if _, err := os.Stat(*out); !os.IsNotExist(err) {
		panic("output exists")
	}
	a, err := p.Open(*in)
	must(err)
	b, err := os.ReadFile(*planPath)
	must(err)
	var plan Plan
	must(json.Unmarshal(b, &plan))
	if a.Snapshot().Checksum != plan.InputSHA256 {
		panic("baseline changed")
	}
	changed, added := []string{}, []string{}
	for _, row := range plan.Additions {
		if row.AppendToExisting {
			changed = append(changed, row.Path)
		} else {
			if a.FindFileIndex(row.Path) >= 0 {
				panic("new path collision: " + row.Path)
			}
			added = append(added, row.Path)
		}
	}
	data, err := a.RepackScriptAdditions(plan.Additions)
	must(err)
	next, err := p.OpenBytes(data)
	must(err)
	kept, err := a.VerifyScriptReplacementAndAdditionPreservation(next, changed, added)
	must(err)
	// Check intended full new streams, including numeric bit patterns.
	for _, row := range plan.Additions {
		if !row.ReplaceTokens {
			continue
		}
		f, ok := next.FindFile(row.Path)
		if !ok {
			panic("missing addition")
		}
		raw, err := next.ReadRaw(row.Path)
		must(err)
		doc, err := next.InspectTokens(f, raw)
		must(err)
		if len(doc.Tokens) != len(row.AppendTokens) || doc.TailHex != "" {
			panic("token count changed")
		}
		for i, want := range row.AppendTokens {
			got := doc.Tokens[i]
			if got.Type != want.Type || ((want.Type == 3 || want.Type == 6) && got.Text != want.Text) ||
				((want.Type != 3 && want.Type != 6) && got.Payload != uint32(want.Value)) {
				panic("addition token mismatch")
			}
		}
	}
	must(os.MkdirAll(filepath.Dir(*out), 0755))
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	must(err)
	_, err = f.Write(data)
	must(err)
	must(f.Sync())
	must(f.Close())
	// Reopen the delivered file rather than relying only on in-memory data.
	delivered, err := p.Open(*out)
	must(err)
	_, err = a.VerifyScriptReplacementAndAdditionPreservation(delivered, changed, added)
	must(err)
	evidence := map[string]any{"input_sha256": a.Snapshot().Checksum, "output_sha256": delivered.Snapshot().Checksum, "added_paths": added, "changed_paths": changed, "unchanged_files_preserved": kept, "new_tokens_verified": true}
	b, err = json.MarshalIndent(evidence, "", "  ")
	must(err)
	must(os.WriteFile(*report, b, 0644))
	fmt.Println(string(b))
}
