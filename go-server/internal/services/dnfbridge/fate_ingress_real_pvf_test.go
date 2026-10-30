package dnfbridge

import (
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	fateIngressItemIDText      = "10304529"
	fateIngressPVFSHA256       = "94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7"
	fateIngressConsumerPackage = "stackable/10304001/10304528.stk\x00need material"
)

var (
	fateIngressTokenRE          = regexp.MustCompile(`(^|[^0-9])10304529([^0-9]|$)`)
	fateIngressStackableEntryRE = regexp.MustCompile("(?i)(^|\\s)([0-9]+)\\s+`([^`]+\\.stk)`")
	fateIngressNoiseSections    = map[string]bool{
		"name": true, "explain": true, "description": true,
		"icon": true, "field image": true, "comment": true,
	}
	// These are the only stackable documents whose actual configuration fields
	// name 10304529. 10304528 is included as a consumer, not an ingress source.
	fateIngressExpectedPackageHits = map[string]string{
		"stackable/10162001/10162015.stk\x00etc":           "1 10304529 1000 30",
		"stackable/10162001/10162016.stk\x00etc":           "1 10304529 1000 45",
		"stackable/10303001/10303189.stk\x00etc":           "1 10304529 100 60",
		"stackable/10304001/10304474.stk\x00int data":      "10304529 100 30",
		"stackable/10304001/10304475.stk\x00int data":      "10304529 100 45",
		"stackable/10304001/10304476.stk\x00int data":      "10304529 100 60",
		"stackable/10304001/10304477.stk\x00int data":      "10304529 100 80",
		"stackable/10304001/10304478.stk\x00int data":      "10304529 100 100",
		"stackable/10304001/10304528.stk\x00need material": "10304529 40",
		"stackable/10304001/10304669.stk\x00etc":           "1 10304529 1000 80",
		"stackable/10304001/10304674.stk\x00etc":           "1 10304529 1000 100",
		"stackable/490008001/chn_490008037.stk\x00etc":     "1 10304529 5000 100",
	}
	fateIngressExpectedRaidReferences = map[string]string{
		"etc/raid/chn_raidrewardlimit.etc\x00raid common coin limit": "10304529 2500",
		"etc/raid/raidrewardlimit.etc\x00raid common coin limit":     "10304529 2500",
		"etc/raid/raidshopinventory.etc\x00item list":                "10304529 10304409",
	}
	fateIngressExpectedStackableIDs = map[string]string{
		"stackable/10162001/10162015.stk":       "10162015",
		"stackable/10162001/10162016.stk":       "10162016",
		"stackable/10303001/10303189.stk":       "10303189",
		"stackable/10304001/10304474.stk":       "10304474",
		"stackable/10304001/10304475.stk":       "10304475",
		"stackable/10304001/10304476.stk":       "10304476",
		"stackable/10304001/10304477.stk":       "10304477",
		"stackable/10304001/10304478.stk":       "10304478",
		"stackable/10304001/10304528.stk":       "10304528",
		"stackable/10304001/10304529.stk":       "10304529",
		"stackable/10304001/10304669.stk":       "10304669",
		"stackable/10304001/10304674.stk":       "10304674",
		"stackable/490008001/chn_490008037.stk": "490008037",
	}
)

type fateIngressHit struct {
	Path    string
	Section string
	Line    string
	LineNo  int
}

func fateIngressHitKey(hit fateIngressHit) string {
	return hit.Path + "\x00" + hit.Section
}

func fateIngressNormalizeLine(line string) string {
	return strings.Join(strings.Fields(line), " ")
}

func fateIngressIgnored(path string, section string) bool {
	lowPath := strings.ToLower(path)
	return strings.Contains(lowPath, "itemdictionary") || fateIngressNoiseSections[section]
}

func fateIngressTextHits(archivePath, text string) []fateIngressHit {
	section := ""
	var hits []fateIngressHit
	for lineNo, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !strings.HasPrefix(line, "[/") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
		}
		if !fateIngressTokenRE.MatchString(line) || fateIngressIgnored(archivePath, section) {
			continue
		}
		hits = append(hits, fateIngressHit{
			Path: archivePath, Section: section,
			Line: fateIngressNormalizeLine(line), LineNo: lineNo + 1,
		})
	}
	return hits
}

func fateIngressScan(
	archive *platformpvf.Archive,
	prefix string,
	accept func(string) bool,
) ([]fateIngressHit, int, error) {
	var hits []fateIngressHit
	files := 0
	err := archive.ScanTextPrefix(prefix, func(archivePath, text string) error {
		lowPath := strings.ToLower(archivePath)
		if accept != nil && !accept(lowPath) {
			return nil
		}
		files++
		hits = append(hits, fateIngressTextHits(lowPath, text)...)
		return nil
	})
	return hits, files, err
}

func fateIngressRequireExactHits(t *testing.T, label string, hits []fateIngressHit, expected map[string]string) {
	t.Helper()
	actual := make(map[string]string, len(hits))
	for _, hit := range hits {
		key := fateIngressHitKey(hit)
		if _, duplicate := actual[key]; duplicate {
			t.Fatalf("%s duplicate hit path=%s section=%s", label, hit.Path, hit.Section)
		}
		actual[key] = hit.Line
	}
	if len(actual) != len(expected) {
		t.Fatalf("%s hit count=%d, want %d: %#v", label, len(actual), len(expected), actual)
	}
	keys := make([]string, 0, len(expected))
	for key, wantLine := range expected {
		keys = append(keys, key)
		gotLine, ok := actual[key]
		if !ok {
			t.Fatalf("%s missing expected hit %q", label, key)
		}
		if gotLine != fateIngressNormalizeLine(wantLine) {
			t.Fatalf("%s hit %q line=%q, want %q", label, key, gotLine, wantLine)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		path, section, _ := strings.Cut(key, "\x00")
		t.Logf("FATE_INGRESS evidence=%s path=%s section=%s line=%q", label, path, section, actual[key])
	}
}

func fateIngressCountExcludingConsumer(hits []fateIngressHit) int {
	grants := 0
	for _, hit := range hits {
		if fateIngressHitKey(hit) != fateIngressConsumerPackage {
			grants++
		}
	}
	return grants
}

func fateIngressRequireAbsent(t *testing.T, label string, hits []fateIngressHit, scanned int) {
	t.Helper()
	if scanned == 0 {
		t.Fatalf("%s scanned no candidate text files", label)
	}
	if len(hits) != 0 {
		t.Fatalf("%s unexpectedly contains %s in %#v", label, fateIngressItemIDText, hits)
	}
	t.Logf("FATE_INGRESS evidence=%s absent item=%s scanned_text_files=%d", label, fateIngressItemIDText, scanned)
}

func fateIngressStackableIDs(listing string) map[string][]string {
	ids := map[string][]string{}
	// stackable.lst is one long token stream, not one id/path pair per line.
	for _, match := range fateIngressStackableEntryRE.FindAllStringSubmatch(listing, -1) {
		relative := match[3]
		archivePath := "stackable/" + relative
		ids[archivePath] = append(ids[archivePath], match[2])
	}
	return ids
}

func fateIngressSectionValue(text, wanted string) string {
	section := ""
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !strings.HasPrefix(line, "[/") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section == strings.ToLower(wanted) && line != "" {
			return strings.Trim(line, "` ")
		}
	}
	return ""
}

func TestRealPVFFateIngressAcquisitionSources(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the fixed Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := archive.Snapshot().Checksum; !strings.EqualFold(got, fateIngressPVFSHA256) {
		t.Fatalf("PVF sha256=%s, want audited %s", got, fateIngressPVFSHA256)
	}
	t.Logf("FATE_INGRESS evidence=pvf sha256=%s item=%s", fateIngressPVFSHA256, fateIngressItemIDText)

	mobHits, mobFiles, err := fateIngressScan(archive, "monster/", func(archivePath string) bool {
		ext := path.Ext(archivePath)
		return ext == ".mob" || ext == ".gob"
	})
	if err != nil {
		t.Fatal(err)
	}
	fateIngressRequireAbsent(t, "ground_monster_mob_drops", mobHits, mobFiles)

	questHits, questFiles, err := fateIngressScan(archive, "n_quest/", func(archivePath string) bool {
		return path.Ext(archivePath) == ".qst"
	})
	if err != nil {
		t.Fatal(err)
	}
	fateIngressRequireAbsent(t, "quest_qst_rewards", questHits, questFiles)

	shopHits, shopFiles, err := fateIngressScan(archive, "itemshop/", func(archivePath string) bool {
		ext := path.Ext(archivePath)
		return ext == ".shp" || ext == ".lst"
	})
	if err != nil {
		t.Fatal(err)
	}
	cashShopHits, cashShopFiles, err := fateIngressScan(archive, "cashshop/", nil)
	if err != nil {
		t.Fatal(err)
	}

	packageHits, packageFiles, err := fateIngressScan(archive, "stackable/", func(archivePath string) bool {
		return path.Ext(archivePath) == ".stk"
	})
	if err != nil {
		t.Fatal(err)
	}
	fateIngressRequireExactHits(t, "stackable_fate_config_references", packageHits, fateIngressExpectedPackageHits)
	if grants := fateIngressCountExcludingConsumer(packageHits); grants != len(fateIngressExpectedPackageHits)-1 {
		t.Fatalf("stackable direct grant package count=%d, want %d; consumer is separate", grants, len(fateIngressExpectedPackageHits)-1)
	} else {
		t.Logf("FATE_INGRESS evidence=stackable_role_split direct_grant_documents=%d consumer_documents=1", grants)
	}

	eventHits, eventFiles, err := fateIngressScan(archive, "event/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := archive.FindFile("etc/onlinequest.etc"); !found {
		t.Fatal("online-duration source etc/onlinequest.etc is missing")
	}
	etcHits, etcFiles, err := fateIngressScan(archive, "etc/", nil)
	if err != nil {
		t.Fatal(err)
	}

	onlineHits := append([]fateIngressHit(nil), eventHits...)
	for _, hit := range etcHits {
		if hit.Path == "etc/onlinequest.etc" {
			onlineHits = append(onlineHits, hit)
		}
	}
	fateIngressRequireAbsent(t, "online_duration_events", onlineHits, eventFiles+1)

	directProductHits := append([]fateIngressHit(nil), shopHits...)
	directProductHits = append(directProductHits, cashShopHits...)
	for _, hit := range etcHits {
		base := path.Base(hit.Path)
		if strings.Contains(base, "cashshop") || strings.Contains(base, "cerashop") ||
			strings.Contains(base, "mileageshop") || strings.Contains(base, "secretshop") {
			directProductHits = append(directProductHits, hit)
		}
	}
	fateIngressRequireAbsent(t, "cash_or_point_direct_products", directProductHits, shopFiles+cashShopFiles+etcFiles)

	var raidReferences []fateIngressHit
	for _, hit := range etcHits {
		if strings.HasPrefix(hit.Path, "etc/raid/") {
			raidReferences = append(raidReferences, hit)
		}
	}
	fateIngressRequireExactHits(t, "raid_limit_and_shop_references", raidReferences, fateIngressExpectedRaidReferences)

	// The stackable source list is the ID/path authority for every package hit.
	listing, err := archive.ReadText("stackable/stackable.lst")
	if err != nil {
		t.Fatal(err)
	}
	stackableIDs := fateIngressStackableIDs(listing)
	stackablePaths := make([]string, 0, len(fateIngressExpectedStackableIDs))
	for archivePath := range fateIngressExpectedStackableIDs {
		stackablePaths = append(stackablePaths, archivePath)
	}
	sort.Strings(stackablePaths)
	for _, archivePath := range stackablePaths {
		wantID := fateIngressExpectedStackableIDs[archivePath]
		ids := stackableIDs[archivePath]
		if len(ids) != 1 || ids[0] != wantID {
			t.Fatalf("stackable.lst path=%s ids=%v, want exactly %s", archivePath, ids, wantID)
		}
		text, err := archive.ReadText(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("FATE_INGRESS stackable_index id=%s path=%s name=%q", ids[0], archivePath, fateIngressSectionValue(text, "name"))
	}

	t.Logf("FATE_INGRESS evidence_scope monster_mob_files=%d quest_qst_files=%d itemshop_files=%d cashshop_text_files=%d stackable_stk_files=%d event_text_files=%d etc_text_files=%d",
		mobFiles, questFiles, shopFiles, cashShopFiles, packageFiles, eventFiles, etcFiles)
}
