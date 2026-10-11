package dnfbridge

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// This candidate audit exercises the production reward parsers for every
// reachable gift box, rather than accepting raw token grammar as runtime proof.
func TestRealPVFGiftFamilyRuntimeParsers(t *testing.T) {
	path, graphPath, reportPath := os.Getenv("DNFBRIDGE_GIFT_CANDIDATE"), os.Getenv("DNFBRIDGE_GIFT_GRAPH"), os.Getenv("DNFBRIDGE_GIFT_RUNTIME_REPORT")
	if path == "" || graphPath == "" || reportPath == "" {
		t.Skip("set the three DNFBRIDGE_GIFT_* candidate audit paths")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	var graph struct {
		Nodes []struct {
			ID   uint32 `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
	}
	if err = json.Unmarshal(data, &graph); err != nil {
		t.Fatal(err)
	}
	type row struct {
		ID    uint32 `json:"id"`
		Type  string `json:"type"`
		Error string `json:"error,omitempty"`
	}
	rows := make([]row, 0, len(graph.Nodes))
	failed, parsed := 0, 0
	for _, node := range graph.Nodes {
		d, resolveErr := catalog.ResolveItem(node.ID)
		var parseErr error
		if resolveErr != nil {
			parseErr = resolveErr
		} else {
			doc, docErr := parseDungeonCardPVFDocument(catalog.source, d.PVFPath)
			if docErr != nil {
				parseErr = docErr
			} else {
				typ, _ := doc.Text("stackable type")
				switch strings.ToLower(strings.TrimSpace(typ)) {
				case "[usable cera package]":
					_, parseErr = resolveCurrentCeraPackageDefinition(catalog, node.ID)
					parsed++
				case "[booster selection]":
					_, parseErr = resolveCurrentBoosterDefinition(catalog, node.ID, currentBoosterRequestSelection)
					parsed++
				case "[booster]", "[cera booster]", "[booster random]", "[random upgradable legacy]", "[upgradable legacy]":
					_, parseErr = resolveCurrentBoosterDefinition(catalog, node.ID, currentBoosterRequestRandom)
					parsed++
				}
			}
		}
		r := row{ID: node.ID, Type: node.Type}
		if parseErr != nil {
			failed++
			r.Error = parseErr.Error()
		}
		rows = append(rows, r)
	}
	report := struct {
		Passed bool  `json:"passed"`
		Parsed int   `json:"parsed"`
		Failed int   `json:"failed"`
		Rows   []row `json:"rows"`
	}{failed == 0 && parsed > 0, parsed, failed, rows}
	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(reportPath, output, 0600); err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		t.Fatalf("candidate runtime parsers: parsed=%d failed=%d; details in the specified report", parsed, failed)
	}
	t.Logf("candidate runtime parsers accepted %d reachable gift boxes", parsed)
}
