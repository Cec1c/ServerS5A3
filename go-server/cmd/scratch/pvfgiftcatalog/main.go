package main

import (
	"encoding/json"
	"flag"
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"sort"
	"strings"
)

type giftAudit struct {
	ID       uint32   `json:"id"`
	Path     string   `json:"path"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Group    string   `json:"group"`
	Existing bool     `json:"existing_shop"`
	Sections []string `json:"sections"`
	Data     []int64  `json:"package_data"`
	Text     string   `json:"text"`
}

func main() {
	input := flag.String("pvf", "", "input archive")
	output := flag.String("report", "", "audit JSON")
	legacy := flag.String("legacy-export", "", "actual legacy definition export")
	importOutput := flag.String("import-to", "", "write verified imported archive to this candidate path")
	legacyRootSource := flag.String("legacy-roots", "", "include the authenticated 2019 roots even outside cash paths")
	rewardGraph := flag.String("reward-graph", "", "root audit JSON for nested-reward grammar and fatigue audit")
	cleanGraph := flag.String("clean-graph", "", "audited nested-reward graph whose fatigue rows should be removed")
	cleanOutput := flag.String("clean-to", "", "write fatigue-clean candidate without deleting definitions")
	lookupIDs := flag.String("lookup-ids", "", "JSON IDs to find in actual supported item references or file names")
	compatFamily := flag.String("compat-family", "", "audited gift families to convert without losing legacy selections")
	compatGraph := flag.String("compat-graph", "", "fatigue-clean reward graph for expiry scope")
	compatOutput := flag.String("compat-to", "", "write compatible candidate archive")
	iconRemap := flag.String("icon-remap", "", "verified source icon frame assignments")
	iconOutput := flag.String("icon-to", "", "write candidate with remapped newly imported icons")
	shopPlan := flag.String("shop-plan", "", "audited authentic gift shop rows")
	shopOutput := flag.String("shop-to", "", "write gift shop candidate without altering other definitions")
	shopSortOutput := flag.String("shop-sort-to", "", "sort authentic package shelf newest first and fix paired-class filters")
	years := flag.String("years", "2018,2019", "comma-separated authentic gift years to audit")
	allStackables := flag.Bool("all-stackables", false, "audit year matches outside cash paths as well")
	flag.Parse()
	if *input == "" || *output == "" {
		panic("pvf and report required")
	}
	a, err := platformpvf.Open(*input)
	if err != nil {
		panic(err)
	}
	if *shelfAuditOnly {
		auditCurrentGiftShelves(a, *output)
		return
	}
	if *shopDisplaySortOutput != "" {
		correctNativeGiftDisplayOrder(a, *shopDisplaySortOutput, *output)
		return
	}
	if *shopSortOutput != "" {
		sortGiftShop(a, *shopSortOutput, *output)
		return
	}
	if *lookupIDs != "" {
		lookupActualDefinitions(a, *lookupIDs, *input, *output)
		return
	}
	if *shopPlan != "" {
		if *shopOutput == "" {
			panic("shop-to required")
		}
		appendGiftShopPlan(a, *shopPlan, *shopOutput, *output)
		return
	}
	if *compatFamily != "" {
		if *compatGraph == "" || *compatOutput == "" {
			panic("compat-graph and compat-to required")
		}
		makeLegacyGiftsCompatible(a, *compatFamily, *compatGraph, *compatOutput, *output)
		return
	}
	if *iconRemap != "" {
		if *iconOutput == "" {
			panic("icon-to required")
		}
		remapImportedGiftIcons(a, *iconRemap, *iconOutput, *output)
		return
	}
	if *rewardGraph != "" {
		auditRewardGraph(a, *rewardGraph, *output)
		return
	}
	if *cleanGraph != "" {
		if *cleanOutput == "" {
			panic("clean-to required")
		}
		cleanGiftFatigue(a, *cleanGraph, *cleanOutput, *output)
		return
	}
	if *legacy != "" {
		if *importOutput != "" {
			importLegacyGifts(a, *legacy, *importOutput, *output)
			return
		}
		auditLegacyImport(a, *legacy, *output)
		return
	}
	refs, _, err := loadItemReferences(a)
	if err != nil {
		panic(err)
	}
	raw, err := a.ReadRaw(ceraShopPath)
	if err != nil {
		panic(err)
	}
	begin, end, err := findRawSection(a, raw, "[package]", "[/package]")
	if err != nil {
		panic(err)
	}
	existing, _, err := parseExistingPackageRows(raw[(begin+1)*5 : end*5])
	if err != nil {
		panic(err)
	}
	ids := make([]uint32, 0, len(refs))
	legacyRoots := map[uint32]bool{}
	if *legacyRootSource != "" {
		data, e := os.ReadFile(*legacyRootSource)
		if e != nil {
			panic(e)
		}
		var old legacyGiftExport
		if e = json.Unmarshal(data, &old); e != nil {
			panic(e)
		}
		for _, id := range old.Roots {
			legacyRoots[id] = true
		}
	}
	for id, r := range refs {
		if r.Kind == "stackable" && (*allStackables || strings.Contains(strings.ToLower(r.Path), "cash/") || legacyRoots[id]) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rows := []giftAudit{}
	groups := map[string]int{}
	auditYears := strings.Split(*years, ",")
	for _, year := range auditYears {
		if len(year) != 4 || year < "2000" || year > "2099" {
			panic("invalid audit year")
		}
	}
	errors := []string{}
	for _, id := range ids {
		r := refs[id]
		text, e := a.ReadText(r.Path)
		if e != nil {
			errors = append(errors, r.Path)
			continue
		}
		doc, e := dnfpvf.Parse(r.Path, text)
		if e != nil {
			errors = append(errors, r.Path)
			continue
		}
		name, _ := doc.Text("name")
		typ, _ := doc.Text("stackable type")
		flavor, _ := doc.Text("flavor text")
		key := strings.ToLower(name + " " + r.Path + " " + flavor)
		group := ""
		for _, year := range auditYears {
			if strings.Contains(name+flavor, year) || strings.Contains(strings.ToLower(r.Path), "/chn_"+year) {
				group = year
				break
			}
		}
		switch {
		case legacyRoots[id]:
			group = "2019"
		case group != "":
		case strings.Contains(key, "sao") || strings.Contains(name, "刀剑神域"):
			group = "sao"
		case strings.Contains(key, "fate") || strings.Contains(key, "zoro") || strings.Contains(name, "命运"):
			group = "fate"
		case strings.Contains(key, "fairy") || strings.Contains(name, "妖精"):
			group = "fairy_tail"
		}
		if group == "" {
			continue
		}
		_, found := existing[id]
		sections := []string{}
		for _, section := range doc.Sections {
			sections = append(sections, section.Name)
		}
		rows = append(rows, giftAudit{id, r.Path, name, typ, group, found, sections, doc.Ints("package data"), text})
		groups[group]++
	}
	shopProducts := []packageRow{}
	interior := raw[(begin+1)*5 : end*5]
	for offset := 0; offset < len(interior); offset += packageRowWidth * 5 {
		commodity, _ := intToken(interior[offset : offset+5])
		item, _ := intToken(interior[offset+5 : offset+10])
		price, ok := intToken(interior[offset+20 : offset+25])
		if !ok || price < 0 {
			panic("invalid shop price")
		}
		job, ok := intToken(interior[offset+50 : offset+55])
		if !ok {
			panic("invalid shop job")
		}
		shopProducts = append(shopProducts, packageRow{CommodityID: uint32(commodity), ItemID: uint32(item), Price: uint32(price), JobCode: job})
	}
	payload := map[string]any{"read_only": true, "input": *input, "groups": groups, "items": rows, "errors": errors, "shop_rows": len(interior) / (packageRowWidth * 5), "shop_products": shopProducts}
	bytes, e := json.MarshalIndent(payload, "", "  ")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile(*output, bytes, 0644); e != nil {
		panic(e)
	}
	fmt.Printf("audited=%d groups=%v errors=%d\n", len(rows), groups, len(errors))
}
