package dnfbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/raid"
	pvf "longheng.io/server/internal/platform/pvf"
)

// Exports the actual parser results without connecting to or changing a database.
func TestPrivateRaidRewardAudit(t *testing.T) {
	source, out := os.Getenv("DNFBRIDGE_RAID_AUDIT_PVF"), os.Getenv("DNFBRIDGE_RAID_AUDIT_OUT")
	if source == "" || out == "" {
		t.Skip("explicit audit source/output required")
	}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	a, e := pvf.Open(source)
	must(e)
	catalog, e := newPVFDungeonDropCatalog(a)
	must(e)
	must(os.MkdirAll(filepath.Join(out, "items"), 0755))
	configs := map[string]raid.AntonRewardConfiguration{}
	configs["anton"], e = raid.LoadAntonRewardConfiguration(a)
	must(e)
	configs["luke"], e = raid.LoadLukeRewardConfiguration(a)
	must(e)
	configs["fiendwar"], e = raid.LoadFiendWarRewardConfiguration(a)
	must(e)
	seq, e := raid.LoadSingleRaidSequence(a, 0, 0)
	must(e)
	configs["anton_single"], e = loadCurrentSingleAntonRewards(catalog, seq, time.Now())
	must(e)
	seq, e = raid.LoadSingleRaidSequence(a, 1, 0)
	must(e)
	configs["luke_single"], e = loadCurrentSingleLukeRewards(catalog, seq, time.Now())
	must(e)
	type node struct {
		ID        uint32                         `json:"id"`
		Name      string                         `json:"name"`
		Path      string                         `json:"path"`
		Kind      string                         `json:"kind"`
		Type      string                         `json:"type"`
		Explain   string                         `json:"explain,omitempty"`
		Attach    string                         `json:"attach"`
		Box       *alignedcmd.MagicBoxResolution `json:"box,omitempty"`
		Selection *currentBoosterDefinition      `json:"selection,omitempty"`
		Issue     string                         `json:"issue,omitempty"`
	}
	nodes := map[uint32]*node{}
	var visit func(uint32, int)
	visit = func(id uint32, depth int) {
		if _, ok := nodes[id]; ok {
			return
		}
		if depth > 12 {
			t.Fatalf("reward nesting exceeds 12 at %d", id)
		}
		if id == 0 {
			nodes[0] = &node{Name: "金币", Kind: "currency"}
			return
		}
		def, e := catalog.ResolveItem(id)
		must(e)
		n := &node{ID: id, Name: def.Name, Path: def.PVFPath, Kind: string(def.Kind), Type: def.StackableType, Attach: def.AttachType}
		nodes[id] = n
		if def.Kind != dungeonDropItemStackable {
			return
		}
		doc, e := parseDungeonCardPVFDocument(a, def.PVFPath)
		must(e)
		n.Explain = magicBoxDocumentText(doc, "explain")
		switch def.StackableType {
		case "[booster]", "[cera booster]", "[booster random]", "[upgradable legacy]", "[random upgradable legacy]", "[cera package]", "[usable cera package]", "[booster selection]":
			raw, e := a.ReadText(def.PVFPath)
			must(e)
			must(os.WriteFile(filepath.Join(out, "items", itoaAudit(id)+".txt"), []byte(raw), 0644))
			box, e := resolveCurrentMagicBox(catalog, a, int64(id))
			if e != nil {
				n.Issue = e.Error()
				return
			}
			n.Box = &box
			for _, g := range box.Groups {
				for _, row := range g.Entries {
					if row.ItemID >= 0 {
						visit(uint32(row.ItemID), depth+1)
					}
				}
			}
			for _, row := range box.PackageItems {
				if row.ItemID >= 0 {
					visit(uint32(row.ItemID), depth+1)
				}
			}
			if def.StackableType == "[booster selection]" {
				selection, e := resolveCurrentBoosterDefinition(catalog, id, currentBoosterRequestSelection)
				if e != nil {
					n.Issue = e.Error()
					return
				}
				n.Selection = &selection
				for _, row := range selection.Selection {
					visit(row.ItemID, depth+1)
				}
			}
		}
	}
	type pool struct {
		Raid    string                   `json:"raid"`
		Phase   int                      `json:"phase"`
		Rank    byte                     `json:"rank"`
		Type    string                   `json:"type"`
		Total   uint32                   `json:"total"`
		Choices []raid.AntonRewardChoice `json:"choices"`
	}
	pools := []pool{}
	for name, c := range configs {
		for phase, p := range c.Phases {
			types := map[string]bool{}
			for _, r := range p.Choices {
				types[r.Type] = true
				visit(r.ContainerID, 0)
			}
			for typ := range types {
				for rank := byte(0); rank < 4; rank++ {
					rows, total, e := p.Pool(typ, rank)
					must(e)
					pools = append(pools, pool{name, phase, rank, typ, total, rows})
				}
			}
		}
	}
	result := map[string]any{"pvf_sha256": a.Snapshot().Checksum, "configs": configs, "pools": pools, "nodes": nodes, "read_only": true}
	raw, e := json.MarshalIndent(result, "", "  ")
	must(e)
	must(os.WriteFile(filepath.Join(out, "audit.json"), raw, 0644))
	t.Logf("configs=%d pools=%d reachable_items=%d output=%s", len(configs), len(pools), len(nodes), out)
}

func itoaAudit(v uint32) string { b, _ := json.Marshal(v); return string(b) }
