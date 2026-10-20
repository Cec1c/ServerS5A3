package dnfbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFAntonSingleRewardDependencyInventory(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current production PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{}
	for _, file := range archive.Files() {
		path := strings.ToLower(file.ArchivePath)
		if (strings.Contains(path, "single") || strings.Contains(path, "anton")) && (strings.HasSuffix(path, ".etc") || strings.HasSuffix(path, ".lst")) {
			if !strings.HasPrefix(path, "etc/") {
				continue
			}
			text, err := archive.ReadText(file.ArchivePath)
			if err != nil {
				t.Fatal(err)
			}
			texts[file.ArchivePath] = text
			t.Logf("current PVF dependency=%s", file.ArchivePath)
		}
	}
	if out := os.Getenv("DNF90_ANTON_NATIVE_DIR"); out != "" {
		encoded, err := json.MarshalIndent(texts, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(out, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "actual-single-related-etc-scripts.json"), encoded, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRealPVFAntonRewardContainerDependencies(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current production PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := raid.LoadAntonRewardConfiguration(archive)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint32]bool{}
	for _, phase := range config.Phases {
		for _, choice := range phase.Choices {
			if seen[choice.ContainerID] {
				continue
			}
			seen[choice.ContainerID] = true
			item, err := catalog.ResolveItem(choice.ContainerID)
			if err != nil {
				t.Fatal("configured reward template missing", choice.ContainerID, err)
			}
			doc, err := parseDungeonCardPVFDocument(archive, item.PVFPath)
			if err != nil {
				t.Fatal(err)
			}
			box, err := resolveCurrentMagicBox(catalog, archive, int64(choice.ContainerID))
			if err != nil {
				t.Fatal("configured reward expansion invalid", choice.ContainerID, err)
			}
			rows := 0
			for _, group := range box.Groups {
				for _, entry := range group.Entries {
					rows++
					if entry.ItemID <= 0 || entry.Count <= 0 {
						t.Fatal("reward amount needs explicit currency owner", choice.ContainerID, entry)
					}
					if _, err := catalog.ResolveItem(uint32(entry.ItemID)); err != nil {
						t.Fatal("reward content template missing", choice.ContainerID, entry.ItemID, err)
					}
				}
			}
			t.Logf("configured=%d flags=%d type=%s expansion=%s groups=%d rows=%d int_data=%v", choice.ContainerID, choice.Flags, magicBoxDocumentText(doc, "stackable type"), box.Kind, len(box.Groups), rows, doc.Ints("int data"))
		}
	}
}
