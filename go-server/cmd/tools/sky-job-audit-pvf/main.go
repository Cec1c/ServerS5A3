package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	absoluteCubePath = "etc/chn_absolute_bind_cube.etc"
	outerBoxID       = 990032001
)

var avatarPartOrder = []string{"hair", "hat", "face", "neck", "coat", "belt", "pants", "shoes"}

var allJobTags = []string{
	"[swordman]", "[demonic swordman]", "[at swordman]",
	"[fighter]", "[at fighter]",
	"[gunner]", "[at gunner]",
	"[mage]", "[creator mage]", "[at mage]",
	"[priest]", "[at priest]",
	"[thief]", "[knight]", "[demonic lancer]", "[gun blader]",
}

var expectedCubeIDs = []int64{
	10007847, 10007848, 990032002,
	10007849, 10007850,
	10008906, 10008907,
	10008908, 10008909, 990032003,
	10007246, 990032004,
	990032005, 990032006,
}

type cubeDefinition struct {
	ID             int64          `json:"id"`
	Path           string         `json:"path"`
	Name           string         `json:"name"`
	Key            int            `json:"key"`
	UsableJobs     []string       `json:"usable_jobs"`
	AliasIDs       []int64        `json:"alias_stk_ids"`
	EffectiveJobs  []string       `json:"effective_alias_jobs"`
	MappedJobs     []string       `json:"mapped_jobs"`
	ColorToken     string         `json:"color_token"`
	ColorVariation int32          `json:"color_variation"`
	MissingParts   []string       `json:"missing_parts,omitempty"`
	InvalidRefs    []string       `json:"invalid_refs,omitempty"`
	MissingJobs    []string       `json:"missing_supported_jobs,omitempty"`
	GatedJobs      []string       `json:"gated_mapping_jobs,omitempty"`
	GatedItemCount int            `json:"gated_item_count,omitempty"`
	CandidateJobs  []string       `json:"unclaimed_family_candidates,omitempty"`
	CoatEvidence   []coatEvidence `json:"same_color_coat_evidence,omitempty"`
}

type coatEvidence struct {
	Job            string           `json:"job"`
	Family         string           `json:"family"`
	ColorVariation int32            `json:"color_variation"`
	EffectIndex    string           `json:"effect_index,omitempty"`
	Complete       bool             `json:"complete_eight_parts"`
	PartIDs        map[string]int64 `json:"part_ids,omitempty"`
	OwnerKeys      []int            `json:"owner_keys,omitempty"`
	Unclaimed      bool             `json:"unclaimed"`
}

type removedBlock struct {
	Key            int      `json:"key"`
	Job            string   `json:"job"`
	ItemIDs        []int64  `json:"item_ids"`
	EquipmentNames []string `json:"equipment_names"`
	CoatName       string   `json:"coat_name"`
	AlsoInSlots    []string `json:"also_in_slots,omitempty"`
}

type outerBoxAudit struct {
	Present          bool     `json:"present"`
	OptionIDs        []int64  `json:"option_ids"`
	ExpectedIDs      []int64  `json:"expected_ids"`
	OptionsExact     bool     `json:"options_exact"`
	AttachFree       bool     `json:"attach_free"`
	AuctionAllowed   bool     `json:"auction_allowed"`
	Explanation      string   `json:"explanation"`
	ExplanationClaim string   `json:"explanation_claim"`
	Errors           []string `json:"errors,omitempty"`
}

type auditReport struct {
	PVFPath                   string           `json:"pvf_path"`
	PVFSHA256                 string           `json:"pvf_sha256"`
	PVFBytes                  int64            `json:"pvf_bytes"`
	FileCount                 int              `json:"file_count"`
	Grade3AvatarFiles         int              `json:"grade3_avatar_files_scanned"`
	RareAvatarCount           int              `json:"family_source_avatar_count"`
	CompleteFamilyCount       int              `json:"complete_family_count"`
	ValidMappedJobBlocks      int              `json:"valid_mapped_job_blocks"`
	FamilyCountsByJob         map[string]int   `json:"family_counts_by_job"`
	AllGrade3FamilyCount      int              `json:"all_grade3_complete_family_count"`
	AllGrade3Candidates       []string         `json:"all_grade3_unclaimed_candidates,omitempty"`
	AvatarSamples             []rareAvatar     `json:"avatar_samples,omitempty"`
	Aliases                   aliasAudit       `json:"stackable_absolute_bind_aliases"`
	Cubes                     []cubeDefinition `json:"cubes"`
	OuterBox                  outerBoxAudit    `json:"outer_box"`
	RemovedBlocks             []removedBlock   `json:"removed_invalid_blocks"`
	MissingSupportedJobs      []string         `json:"missing_supported_jobs,omitempty"`
	GenuineOmissionCandidates []string         `json:"genuine_omission_candidates,omitempty"`
	Errors                    []string         `json:"errors,omitempty"`
	Passed                    bool             `json:"passed"`
}

type patchSpec struct {
	Schema              int            `json:"schema"`
	InputSHA256         string         `json:"input_sha256"`
	OutputSHA256        string         `json:"output_sha256,omitempty"`
	InputBytes          int64          `json:"input_bytes"`
	OutputBytes         int64          `json:"output_bytes,omitempty"`
	FileCountBefore     int            `json:"file_count_before"`
	FileCountAfter      int            `json:"file_count_after,omitempty"`
	UnchangedFiles      int            `json:"unchanged_files_verified"`
	ChangedPaths        []string       `json:"changed_paths"`
	StackableChanges    []string       `json:"stackable_changes"`
	OuterBoxChanges     []string       `json:"outer_box_changes"`
	RemovedBlocks       []removedBlock `json:"removed_invalid_blocks"`
	RemovedJobBlocks    int            `json:"removed_job_blocks"`
	RemovedItemMappings int            `json:"removed_item_mappings"`
	Reason              string         `json:"reason"`
}

type rareAvatar struct {
	ID        int64    `json:"id"`
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	Part      string   `json:"part"`
	Jobs      []string `json:"jobs"`
	Color     int32    `json:"color"`
	Variation int32    `json:"variation"`
	Effect    string   `json:"effect_index,omitempty"`
}

type completeFamily struct {
	Job      string
	Stem     string
	ColorTok string
	Color    int32
	Effect   string
	IDs      map[int64]bool
	PartIDs  map[string]int64
	Items    map[string]rareAvatar
}

type tokenBlock struct {
	Key     int
	Job     string
	ItemIDs []int64
	Tokens  []platformpvf.ScriptToken
}

type stkAlias struct {
	ID   int64    `json:"id"`
	Path string   `json:"path"`
	Key  int      `json:"key"`
	Jobs []string `json:"jobs"`
}

type aliasAudit struct {
	ScannedStackables int              `json:"scanned_stackables"`
	Aliases           []stkAlias       `json:"aliases"`
	JobsByKey         map[int][]string `json:"jobs_by_key"`
	ItemsByKey        map[int][]int64  `json:"items_by_key"`
}

var (
	backtickRE        = regexp.MustCompile("`([^`]+)`")
	dreamActionKeyRE  = regexp.MustCompile("(?s)\\[action type\\].*?`\\[absolute bind cube\\]`\\s+(\\d+)\\s+8\\s+2\\s+0\\s+0")
	absoluteBindKeyRE = regexp.MustCompile("(?s)\\[action type\\].*?`\\[absolute bind cube\\]`\\s+(\\d+)\\s+8\\s+\\d+\\s+0\\s+0")
	variationRE       = regexp.MustCompile(`(?ms)^\[variation\]\s*\n\s*(-?\d+)\s+(-?\d+)`)
	slotNumberRE      = regexp.MustCompile(`\d+`)
)

func main() {
	pvfPath := flag.String("pvf", "", "input Script.pvf")
	reportPath := flag.String("report", "", "audit report JSON")
	patchOut := flag.String("patch-out", "", "write patched PVF to this path")
	patchReportPath := flag.String("patch-report", "", "write patch specification/receipt JSON")
	shaPin := flag.String("sha256", "", "required input SHA256 in patch mode; optional audit pin")
	flag.Parse()
	if *pvfPath == "" {
		fmt.Fprintln(os.Stderr, "-pvf is required")
		os.Exit(2)
	}
	if *patchOut != "" && *patchReportPath == "" {
		*patchReportPath = filepath.Join(filepath.Dir(*patchOut), "sky-jobs-patchspec.json")
	}
	if *patchOut == "" && (*patchReportPath != "" || *reportPath == "") {
		fmt.Fprintln(os.Stderr, "audit mode requires -report and does not accept -patch-report")
		os.Exit(2)
	}
	if err := ensureDistinctPaths(map[string]string{"input": *pvfPath, "report": *reportPath, "patch-output": *patchOut, "patch-report": *patchReportPath}); err != nil {
		exitErr(err)
	}
	if *patchOut != "" && len(*shaPin) != 64 {
		exitErr(fmt.Errorf("-sha256 is required for patch mode"))
	}
	if *shaPin != "" {
		data, err := os.ReadFile(*pvfPath)
		if err != nil {
			exitErr(err)
		}
		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), *shaPin) {
			exitErr(fmt.Errorf("input SHA256 mismatch: got %s want %s", hex.EncodeToString(sum[:]), *shaPin))
		}
	}
	if *patchOut != "" {
		if _, err := os.Stat(*patchOut); err == nil {
			exitErr(fmt.Errorf("patch output already exists: %s", *patchOut))
		} else if !os.IsNotExist(err) {
			exitErr(err)
		}
	}

	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: *pvfPath})
	if err != nil {
		exitErr(err)
	}
	report, _, err := auditArchive(archive, *pvfPath)
	if err != nil {
		exitErr(err)
	}
	if *reportPath != "" {
		if err := writeJSON(*reportPath, report); err != nil {
			exitErr(err)
		}
	}

	if *patchOut == "" {
		printAuditSummary(report)
		if !report.Passed {
			os.Exit(2)
		}
		return
	}
	if len(report.Errors) != 0 || len(report.MissingSupportedJobs) != 0 || len(report.GenuineOmissionCandidates) != 0 || len(report.OuterBox.Errors) != 0 {
		exitErr(fmt.Errorf("refusing automatic patch: unresolved missing/candidate/outer-box findings require explicit item mapping review"))
	}
	if len(report.RemovedBlocks) == 0 {
		exitErr(fmt.Errorf("nothing to patch"))
	}
	if _, err := os.Stat(*patchOut); err == nil {
		exitErr(fmt.Errorf("patch output already exists: %s", *patchOut))
	} else if !errors.Is(err, os.ErrNotExist) {
		exitErr(err)
	}

	updated, spec, err := patchPlaceholderMappings(archive, report, *pvfPath)
	if err != nil {
		exitErr(err)
	}
	afterArchive, err := platformpvf.OpenBytes(updated)
	if err != nil {
		exitErr(fmt.Errorf("re-open patched PVF: %w", err))
	}
	afterReport, _, err := auditArchive(afterArchive, *patchOut)
	if err != nil {
		exitErr(err)
	}
	if !afterReport.Passed {
		exitErr(fmt.Errorf("patched PVF still has audit blockers"))
	}
	spec.OutputSHA256 = afterReport.PVFSHA256
	spec.OutputBytes = afterReport.PVFBytes
	spec.FileCountAfter = afterReport.FileCount
	if err := writeFileAtomic(*patchOut, updated, true); err != nil {
		exitErr(err)
	}
	if *patchReportPath != "" {
		if err := writeJSON(*patchReportPath, spec); err != nil {
			exitErr(err)
		}
	}
	fmt.Printf("before_passed=%t removed_blocks=%d removed_mappings=%d after_passed=%t output=%s sha256=%s\n",
		report.Passed, spec.RemovedJobBlocks, spec.RemovedItemMappings, afterReport.Passed, *patchOut, afterReport.PVFSHA256)
}

func auditArchive(archive *platformpvf.Archive, displayPath string) (auditReport, map[int]map[string]tokenBlock, error) {
	report := auditReport{PVFPath: displayPath}
	if err := archive.EnableBoundedReadCaches(64<<20, 128<<20); err != nil {
		return report, nil, err
	}
	snapshot := archive.Snapshot()
	report.PVFSHA256 = snapshot.Checksum
	report.PVFBytes = snapshot.Size
	report.FileCount = snapshot.FileCount
	if data, err := os.ReadFile(displayPath); err == nil {
		sum := sha256.Sum256(data)
		report.PVFSHA256 = hex.EncodeToString(sum[:])
		report.PVFBytes = int64(len(data))
	}

	stackablePaths, err := loadListPaths(archive, "stackable/stackable.lst")
	if err != nil {
		return report, nil, err
	}
	aliases, err := loadAbsoluteBindAliases(archive, stackablePaths)
	if err != nil {
		return report, nil, err
	}
	report.Aliases = aliases

	equipmentPaths, err := loadListPaths(archive, "equipment/equipment.lst")
	if err != nil {
		return report, nil, err
	}
	referencedIDs, err := loadReferencedCubeIDs(archive)
	if err != nil {
		return report, nil, err
	}
	itemsByID, avatars, grade3Count, err := loadRareAvatars(archive, equipmentPaths, referencedIDs)
	if err != nil {
		return report, nil, err
	}
	report.Grade3AvatarFiles = grade3Count
	report.RareAvatarCount = len(avatars)
	families := buildCompleteFamilies(avatars)
	report.CompleteFamilyCount = 0
	report.FamilyCountsByJob = map[string]int{}
	for job, values := range families {
		report.FamilyCountsByJob[job] = len(values)
		report.CompleteFamilyCount += len(values)
	}
	for _, id := range []int64{101520582, 101560758, 413520023, 103520318, 107520260} {
		if item, ok := itemsByID[id]; ok {
			report.AvatarSamples = append(report.AvatarSamples, item)
		}
	}
	if report.Grade3AvatarFiles == 0 {
		report.Errors = append(report.Errors, "zero grade-3 avatar files scanned; aborting zero-match audit")
	}
	if report.RareAvatarCount == 0 {
		report.Errors = append(report.Errors, "zero grade-3 avatar resources parsed; aborting zero-match audit")
	}
	if report.CompleteFamilyCount == 0 {
		report.Errors = append(report.Errors, "zero complete eight-part avatar families parsed; aborting zero-match audit")
	}
	if aliases.ScannedStackables == 0 || len(aliases.Aliases) == 0 {
		report.Errors = append(report.Errors, "zero stackable/absolute-bind aliases parsed; aborting zero-match audit")
	}
	for key := 0; key <= 13; key++ {
		if len(aliases.JobsByKey[key]) == 0 {
			report.Errors = append(report.Errors, fmt.Sprintf("absolute-bind key %d has no parsed STK alias jobs", key))
		}
	}
	claimed := map[string]int{}
	claimed, tokenBlocks, err := collectCubeAudit(archive, stackablePaths, aliases, itemsByID, avatars, families, claimed, &report)
	if err != nil {
		return report, nil, err
	}
	for _, cube := range report.Cubes {
		for _, job := range cube.MappedJobs {
			if !containsFold(cube.GatedJobs, job) {
				report.ValidMappedJobBlocks++
			}
		}
	}
	if report.ValidMappedJobBlocks != 161 {
		report.Errors = append(report.Errors, fmt.Sprintf("valid mapped job blocks=%d, want 161", report.ValidMappedJobBlocks))
	}
	allAvatars := make([]rareAvatar, 0, len(itemsByID))
	for _, item := range itemsByID {
		allAvatars = append(allAvatars, item)
	}
	allFamilies := buildCompleteFamilies(allAvatars)
	for _, values := range allFamilies {
		report.AllGrade3FamilyCount += len(values)
	}
	for _, cube := range report.Cubes {
		usable := toSet(cube.EffectiveJobs)
		for _, job := range allJobTags {
			if usable[job] {
				continue
			}
			if len(unclaimedFamilies(allFamilies[job], cube.ColorToken, cube.ColorVariation, claimed)) != 0 {
				report.AllGrade3Candidates = append(report.AllGrade3Candidates, fmt.Sprintf("%d:%s", cube.ID, job))
			}
		}
	}
	sort.Strings(report.AllGrade3Candidates)
	collectOuterBox(archive, stackablePaths, &report)

	for _, cube := range report.Cubes {
		report.MissingSupportedJobs = append(report.MissingSupportedJobs, qualifiedJobs(cube.ID, cube.MissingJobs)...)
		report.GenuineOmissionCandidates = append(report.GenuineOmissionCandidates, qualifiedJobs(cube.ID, cube.CandidateJobs)...)
	}
	cubeFailures := 0
	for _, cube := range report.Cubes {
		cubeFailures += len(cube.InvalidRefs) + len(cube.MissingParts) + len(cube.MissingJobs) + len(cube.CandidateJobs)
	}
	report.Passed = len(report.Errors) == 0 && cubeFailures == 0 && len(report.MissingSupportedJobs) == 0 &&
		len(report.GenuineOmissionCandidates) == 0 && len(report.OuterBox.Errors) == 0 && len(report.RemovedBlocks) == 0
	return report, tokenBlocks, nil
}

func collectCubeAudit(
	archive *platformpvf.Archive,
	stackablePaths map[int64]string,
	aliases aliasAudit,
	itemsByID map[int64]rareAvatar,
	avatars []rareAvatar,
	families map[string][]completeFamily,
	claimed map[string]int,
	report *auditReport,
) (map[string]int, map[int]map[string]tokenBlock, error) {
	raw, err := archive.ReadRaw(absoluteCubePath)
	if err != nil {
		return claimed, nil, err
	}
	tokens, err := decodeTokens(archive, raw)
	if err != nil {
		return claimed, nil, err
	}
	blocks, err := parseTokenBlocks(tokens)
	if err != nil {
		return claimed, nil, err
	}
	signatureSlots := map[string][]string{}
	for key, jobs := range blocks {
		for job, block := range jobs {
			signatureSlots[blockSignature(block.ItemIDs)] = append(signatureSlots[blockSignature(block.ItemIDs)], fmt.Sprintf("%d:%s", key, job))
		}
	}

	for _, cubeID := range expectedCubeIDs {
		relation, ok := stackablePaths[cubeID]
		if !ok {
			return claimed, nil, fmt.Errorf("stackable.lst missing cube %d", cubeID)
		}
		cubePath := joinPVFPath("stackable", relation)
		text, err := archive.ReadText(cubePath)
		if err != nil {
			return claimed, nil, fmt.Errorf("read cube %d: %w", cubeID, err)
		}
		key, err := parseActionKey(text)
		if err != nil {
			return claimed, nil, fmt.Errorf("parse cube %d action: %w", cubeID, err)
		}
		usable := normalizeJobs(parseJobs(text))
		effective := append([]string(nil), aliases.JobsByKey[key]...)
		if len(effective) == 0 {
			return claimed, nil, fmt.Errorf("key %d has no stackable alias", key)
		}
		usableSet := toSet(effective)
		cube := cubeDefinition{
			ID:            cubeID,
			Path:          relation,
			Name:          parseSectionName(text, "name"),
			Key:           key,
			UsableJobs:    usable,
			AliasIDs:      append([]int64(nil), aliases.ItemsByKey[key]...),
			EffectiveJobs: effective,
		}
		keyBlocks, ok := blocks[key]
		if !ok {
			cube.InvalidRefs = append(cube.InvalidRefs, fmt.Sprintf("key %d absent from absolute cube table", key))
			report.Cubes = append(report.Cubes, cube)
			continue
		}
		colorCounts := map[string]int{}
		colorValues := map[int32]int{}
		mappedSet := map[string]bool{}
		for job, block := range keyBlocks {
			mappedSet[job] = true
			cube.MappedJobs = append(cube.MappedJobs, job)
			if !usableSet[job] {
				continue
			}
			for _, part := range avatarPartOrder {
				itemID, ok := blockItem(block, part)
				if !ok {
					cube.MissingParts = append(cube.MissingParts, job+":"+part)
					continue
				}
				item, ok := itemsByID[itemID]
				if !ok {
					cube.InvalidRefs = append(cube.InvalidRefs, fmt.Sprintf("%s:%s:%d missing", job, part, itemID))
					continue
				}
				if item.Part != part {
					cube.InvalidRefs = append(cube.InvalidRefs, fmt.Sprintf("%s:%s:%d part=%s", job, part, itemID, item.Part))
				}
				if !containsFold(item.Jobs, job) {
					cube.InvalidRefs = append(cube.InvalidRefs, fmt.Sprintf("%s:%s:%d job-tag-mismatch=%v", job, part, itemID, item.Jobs))
				}
				colorCounts[firstRunes(item.Name, 2)]++
				colorValues[item.Color]++
			}
		}
		cube.MappedJobs = sortedKeys(mappedSet)
		cube.ColorToken = dominantKey(colorCounts)
		cube.ColorVariation = dominantInt(colorValues)
		for job := range mappedSet {
			if !usableSet[job] {
				cube.GatedJobs = append(cube.GatedJobs, job)
				cube.GatedItemCount += len(keyBlocks[job].ItemIDs)
				block := removedBlock{Key: key, Job: job, ItemIDs: append([]int64(nil), keyBlocks[job].ItemIDs...)}
				for _, id := range block.ItemIDs {
					if item, ok := itemsByID[id]; ok {
						block.EquipmentNames = append(block.EquipmentNames, item.Name)
						if item.Part == "coat" {
							block.CoatName = item.Name
						}
					}
				}
				for _, slot := range signatureSlots[blockSignature(block.ItemIDs)] {
					if slot != fmt.Sprintf("%d:%s", key, job) {
						block.AlsoInSlots = append(block.AlsoInSlots, slot)
					}
				}
				sort.Strings(block.AlsoInSlots)
				report.RemovedBlocks = append(report.RemovedBlocks, block)
			}
		}
		for job := range mappedSet {
			coatID, ok := blockItem(keyBlocks[job], "coat")
			if !ok {
				continue
			}
			claimCubeFamily(job, usableSet, coatID, cube.ColorToken, cube.ColorVariation, families[job], claimed, key)
		}
		sort.Strings(cube.GatedJobs)
		for _, job := range effective {
			if _, ok := keyBlocks[job]; !ok {
				cube.MissingJobs = append(cube.MissingJobs, job)
			}
		}
		sort.Strings(cube.InvalidRefs)
		sort.Strings(cube.MissingParts)
		report.Cubes = append(report.Cubes, cube)
	}

	for index := range report.Cubes {
		cube := &report.Cubes[index]
		usableSet := toSet(cube.EffectiveJobs)
		for _, job := range allJobTags {
			if usableSet[job] {
				continue
			}
			if len(unclaimedFamilies(families[job], cube.ColorToken, cube.ColorVariation, claimed)) != 0 {
				cube.CandidateJobs = append(cube.CandidateJobs, job)
			}
			cube.CoatEvidence = append(cube.CoatEvidence, sameColorCoatEvidence(job, cube.ColorToken, cube.ColorVariation, avatars, families, claimed)...)
		}
		sort.Strings(cube.CandidateJobs)
		sort.Slice(cube.CoatEvidence, func(i, j int) bool {
			if cube.CoatEvidence[i].Job != cube.CoatEvidence[j].Job {
				return cube.CoatEvidence[i].Job < cube.CoatEvidence[j].Job
			}
			return cube.CoatEvidence[i].Family < cube.CoatEvidence[j].Family
		})
	}

	// The only authored cube file must contain exactly keys 0..13.
	keys := make([]int, 0, len(blocks))
	for key := range blocks {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	if fmt.Sprint(keys) != fmt.Sprint([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}) {
		report.Errors = append(report.Errors, fmt.Sprintf("absolute cube keys=%v, want 0..13", keys))
	}
	return claimed, blocks, nil
}

func collectOuterBox(archive *platformpvf.Archive, paths map[int64]string, report *auditReport) {
	relation, ok := paths[outerBoxID]
	report.OuterBox.ExpectedIDs = append([]int64(nil), expectedCubeIDs...)
	if !ok {
		report.OuterBox.Errors = append(report.OuterBox.Errors, "outer box missing from stackable.lst")
		return
	}
	text, err := archive.ReadText(joinPVFPath("stackable", relation))
	if err != nil {
		report.OuterBox.Errors = append(report.OuterBox.Errors, err.Error())
		return
	}
	report.OuterBox.Present = true
	optionIDs, optionErr := parseStackableOptionIDs(text)
	if optionErr != nil {
		report.OuterBox.Errors = append(report.OuterBox.Errors, optionErr.Error())
		return
	}
	report.OuterBox.OptionIDs = optionIDs
	report.OuterBox.OptionsExact = fmt.Sprint(report.OuterBox.OptionIDs) == fmt.Sprint(expectedCubeIDs)
	if !report.OuterBox.OptionsExact {
		report.OuterBox.Errors = append(report.OuterBox.Errors, fmt.Sprintf("options=%v", report.OuterBox.OptionIDs))
	}
	report.OuterBox.AttachFree = strings.Contains(text, "[attach type]\n`[free]`")
	report.OuterBox.AuctionAllowed = strings.Contains(text, "[available auction]\n1")
	if !report.OuterBox.AttachFree || !report.OuterBox.AuctionAllowed {
		report.OuterBox.Errors = append(report.OuterBox.Errors, "outer box trade/auction attributes changed")
	}
	report.OuterBox.Explanation = parseSectionName(text, "explain")
	report.OuterBox.ExplanationClaim = "第2套至第7套全颜色、全套装"
	if !strings.Contains(report.OuterBox.Explanation, report.OuterBox.ExplanationClaim) || !strings.Contains(report.OuterBox.Explanation, "原有的职业限制") {
		report.OuterBox.Errors = append(report.OuterBox.Errors, "outer box explanation does not preserve generation/color scope and original job gates")
	}
}

func patchPlaceholderMappings(archive *platformpvf.Archive, before auditReport, inputPath string) ([]byte, patchSpec, error) {
	raw, err := archive.ReadRaw(absoluteCubePath)
	if err != nil {
		return nil, patchSpec{}, err
	}
	tokens, err := decodeTokens(archive, raw)
	if err != nil {
		return nil, patchSpec{}, err
	}
	allowed := map[int]map[string]bool{}
	for _, cube := range before.Cubes {
		allowed[cube.Key] = toSet(cube.EffectiveJobs)
	}
	filtered, err := filterAbsoluteCubeTokens(tokens, allowed)
	if err != nil {
		return nil, patchSpec{}, err
	}
	updated, err := archive.RepackScriptAdditions([]platformpvf.ScriptAddition{{
		Path:                  absoluteCubePath,
		AppendToExisting:      true,
		ReplaceExistingTokens: true,
		AppendTokens:          filtered,
	}})
	if err != nil {
		return nil, patchSpec{}, err
	}
	next, err := platformpvf.OpenBytes(updated)
	if err != nil {
		return nil, patchSpec{}, err
	}
	unchangedFiles, err := archive.VerifyScriptReplacementAndAdditionPreservation(next, []string{absoluteCubePath}, nil)
	if err != nil {
		return nil, patchSpec{}, fmt.Errorf("PVF preservation check: %w", err)
	}
	itemMappings := 0
	for _, block := range before.RemovedBlocks {
		itemMappings += len(block.ItemIDs)
	}
	spec := patchSpec{
		Schema:              1,
		InputSHA256:         before.PVFSHA256,
		InputBytes:          before.PVFBytes,
		FileCountBefore:     before.FileCount,
		UnchangedFiles:      unchangedFiles,
		ChangedPaths:        []string{absoluteCubePath},
		StackableChanges:    []string{},
		OuterBoxChanges:     []string{},
		RemovedBlocks:       before.RemovedBlocks,
		RemovedJobBlocks:    len(before.RemovedBlocks),
		RemovedItemMappings: itemMappings,
		Reason:              "Remove only job-output blocks already excluded by the corresponding cube's [usable job] gate; retain all supported outputs and all STK gates unchanged.",
	}
	_ = inputPath
	return updated, spec, nil
}

func filterAbsoluteCubeTokens(tokens []platformpvf.ScriptToken, allowed map[int]map[string]bool) ([]platformpvf.ScriptToken, error) {
	out := make([]platformpvf.ScriptToken, 0, len(tokens))
	for i := 0; i < len(tokens); {
		if !(tokens[i].Type == 3 && tokens[i].Text == "[absolute bind cube]") {
			out = append(out, tokens[i])
			i++
			continue
		}
		if i+2 >= len(tokens) || tokens[i+1].Type != 0 || tokens[i+2].Type != 3 || tokens[i+2].Text != "[absolute bind plus rare]" {
			return nil, fmt.Errorf("invalid absolute cube header at token %d", i)
		}
		key := int(tokens[i+1].Value)
		jobsAllowed, ok := allowed[key]
		if !ok {
			return nil, fmt.Errorf("no cube gate for key %d", key)
		}
		out = append(out, tokens[i], tokens[i+1], tokens[i+2])
		i += 3
		for i < len(tokens) && !(tokens[i].Type == 3 && tokens[i].Text == "[/absolute bind plus rare]") {
			if tokens[i].Type != 6 {
				return nil, fmt.Errorf("unexpected non-job token in key %d: type=%d text=%q", key, tokens[i].Type, tokens[i].Text)
			}
			job := normalizeJob(tokens[i].Text)
			start := i
			i++
			for i < len(tokens) && tokens[i].Type == 6 && !isAvatarPart(tokens[i].Text) {
				break
			}
			// A block is exactly one job token followed by eight part/value pairs.
			if i+16 > len(tokens) {
				return nil, fmt.Errorf("truncated job block key=%d job=%s", key, job)
			}
			for step := 0; step < 16; step += 2 {
				partTok, valueTok := tokens[i+step], tokens[i+step+1]
				wantPart := avatarPartOrder[step/2]
				if partTok.Type != 6 || normalizePart(partTok.Text) != wantPart || valueTok.Type != 0 {
					return nil, fmt.Errorf("invalid part block key=%d job=%s at step=%s", key, job, wantPart)
				}
			}
			if jobsAllowed[job] {
				out = append(out, tokens[start:i+16]...)
			}
			i += 16
		}
		if i >= len(tokens) || tokens[i].Text != "[/absolute bind plus rare]" {
			return nil, fmt.Errorf("missing absolute bind close for key %d", key)
		}
		out = append(out, tokens[i])
		i++
		if i >= len(tokens) || tokens[i].Type != 3 || tokens[i].Text != "[/absolute bind cube]" {
			return nil, fmt.Errorf("missing absolute cube close for key %d", key)
		}
		out = append(out, tokens[i])
		i++
	}
	return out, nil
}

func parseTokenBlocks(tokens []platformpvf.ScriptToken) (map[int]map[string]tokenBlock, error) {
	result := map[int]map[string]tokenBlock{}
	for i := 0; i < len(tokens); {
		if !(tokens[i].Type == 3 && tokens[i].Text == "[absolute bind cube]") {
			i++
			continue
		}
		if i+2 >= len(tokens) || tokens[i+1].Type != 0 || tokens[i+2].Text != "[absolute bind plus rare]" {
			return nil, fmt.Errorf("invalid token table cube header")
		}
		key := int(tokens[i+1].Value)
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate key %d", key)
		}
		result[key] = map[string]tokenBlock{}
		i += 3
		for i < len(tokens) && tokens[i].Text != "[/absolute bind plus rare]" {
			if tokens[i].Type != 6 {
				return nil, fmt.Errorf("key %d unexpected token %d %q", key, tokens[i].Type, tokens[i].Text)
			}
			job := normalizeJob(tokens[i].Text)
			start := i
			i++
			ids := make([]int64, 0, 8)
			if i+16 > len(tokens) {
				return nil, fmt.Errorf("key %d job %s truncated", key, job)
			}
			for step := 0; step < 16; step += 2 {
				partTok, valueTok := tokens[i+step], tokens[i+step+1]
				if partTok.Type != 6 || normalizePart(partTok.Text) != avatarPartOrder[step/2] || valueTok.Type != 0 {
					return nil, fmt.Errorf("key %d job %s invalid part sequence", key, job)
				}
				ids = append(ids, int64(valueTok.Value))
			}
			result[key][job] = tokenBlock{Key: key, Job: job, ItemIDs: ids, Tokens: append([]platformpvf.ScriptToken(nil), tokens[start:i+16]...)}
			i += 16
		}
		i += 2
	}
	return result, nil
}

func loadRareAvatars(archive *platformpvf.Archive, equipmentPaths map[int64]string, referencedIDs map[int64]bool) (map[int64]rareAvatar, []rareAvatar, int, error) {
	items := map[int64]rareAvatar{}
	avatars := make([]rareAvatar, 0, 2500)
	grade3Count := 0
	idsByPath := map[string]int64{}
	for id, path := range equipmentPaths {
		idsByPath[normalizePVFPath(path)] = id
	}
	for _, file := range archive.Files() {
		lowerPath := strings.ToLower(file.ArchivePath)
		if !strings.Contains(lowerPath, "avatar/") || !strings.HasSuffix(lowerPath, ".equ") ||
			strings.Contains(filepath.Base(lowerPath), "gm_") || strings.Contains(filepath.Base(lowerPath), "clone") {
			continue
		}
		id, ok := idsByPath[normalizePVFPath(file.ArchivePath)]
		if !ok || id <= 0 {
			continue
		}
		text, err := archive.ReadText(file.ArchivePath)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("read rare avatar %s: %w", file.ArchivePath, err)
		}
		jobs := normalizeJobs(parseJobs(text))
		part := normalizePart(parseEquipmentPart(text))
		grade, _ := strconv.Atoi(strings.TrimSpace(parseSection(text, "grade")))
		if grade != 3 || len(jobs) == 0 || part == "" {
			continue
		}
		grade3Count++
		variation, color := parseVariation(text)
		effect := strings.TrimSpace(parseSection(text, "effect part set index"))
		a := rareAvatar{ID: id, Path: file.ArchivePath, Name: parseSectionName(text, "name"), Part: part, Jobs: jobs, Variation: variation, Color: color, Effect: effect}
		items[id] = a
		if strings.Contains(lowerPath, "chn_equ_rare_avatar_") || referencedIDs[id] {
			avatars = append(avatars, a)
		}
	}
	return items, avatars, grade3Count, nil
}

func loadAbsoluteBindAliases(archive *platformpvf.Archive, stackablePaths map[int64]string) (aliasAudit, error) {
	result := aliasAudit{
		ScannedStackables: len(stackablePaths),
		Aliases:           []stkAlias{},
		JobsByKey:         map[int][]string{},
		ItemsByKey:        map[int][]int64{},
	}
	jobSets := map[int]map[string]bool{}
	ids := make([]int64, 0, len(stackablePaths))
	for id := range stackablePaths {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		relation := stackablePaths[id]
		text, err := archive.ReadText(joinPVFPath("stackable", relation))
		if err != nil {
			return result, fmt.Errorf("read stackable %d for absolute-bind aliases: %w", id, err)
		}
		action := parseSection(text, "action type")
		if !strings.Contains(action, "[absolute bind cube]") {
			continue
		}
		key, err := parseAbsoluteBindKey(text)
		if err != nil {
			return result, fmt.Errorf("parse absolute-bind stackable %d: %w", id, err)
		}
		if key < 0 || key > 13 {
			return result, fmt.Errorf("stackable %d references out-of-range absolute-bind key %d", id, key)
		}
		jobs := normalizeJobs(parseJobs(text))
		if len(jobs) == 0 {
			return result, fmt.Errorf("stackable %d key %d has no usable job", id, key)
		}
		result.Aliases = append(result.Aliases, stkAlias{ID: id, Path: relation, Key: key, Jobs: jobs})
		if jobSets[key] == nil {
			jobSets[key] = map[string]bool{}
		}
		for _, job := range jobs {
			jobSets[key][job] = true
		}
		result.ItemsByKey[key] = append(result.ItemsByKey[key], id)
	}
	for key, jobs := range jobSets {
		result.JobsByKey[key] = sortedKeys(jobs)
	}
	return result, nil
}

func buildCompleteFamilies(avatars []rareAvatar) map[string][]completeFamily {
	byJob := map[string][]rareAvatar{}
	for _, item := range avatars {
		for _, job := range item.Jobs {
			byJob[job] = append(byJob[job], item)
		}
	}
	result := map[string][]completeFamily{}
	seen := map[string]bool{}
	for job, rows := range byJob {
		for _, coat := range rows {
			if coat.Part != "coat" {
				continue
			}
			stem := trimLastRunes(coat.Name, 2)
			if utf8.RuneCountInString(stem) <= 2 {
				continue
			}
			parts := map[string]rareAvatar{}
			ids := map[int64]bool{}
			partIDs := map[string]int64{}
			for _, item := range rows {
				if item.Color != coat.Color || !strings.HasPrefix(item.Name, stem) {
					continue
				}
				if _, exists := parts[item.Part]; exists {
					continue
				}
				parts[item.Part] = item
				ids[item.ID] = true
				partIDs[item.Part] = item.ID
			}
			if len(parts) != len(avatarPartOrder) {
				continue
			}
			key := fmt.Sprintf("%s\x00%s\x00%d", job, stem, coat.Color)
			if seen[key] {
				continue
			}
			seen[key] = true
			result[job] = append(result[job], completeFamily{
				Job: job, Stem: stem, ColorTok: firstRunes(stem, 2), Color: coat.Color, Effect: coat.Effect, IDs: ids, PartIDs: partIDs, Items: parts,
			})
		}
	}
	for job := range result {
		sort.Slice(result[job], func(i, j int) bool { return result[job][i].Stem < result[job][j].Stem })
	}
	return result
}

func unclaimedFamilies(families []completeFamily, colorToken string, colorValue int32, claimed map[string]int) []completeFamily {
	out := []completeFamily{}
	for _, family := range families {
		if family.ColorTok != colorToken || family.Color != colorValue {
			continue
		}
		claimedAny := false
		for id := range family.IDs {
			if _, ok := claimed[fmt.Sprintf("%s:%d", family.Job, id)]; ok {
				claimedAny = true
				break
			}
		}
		if !claimedAny {
			out = append(out, family)
		}
	}
	return out
}

func claimCubeFamily(job string, usableSet map[string]bool, coatID int64, colorToken string, colorValue int32, families []completeFamily, claimed map[string]int, key int) {
	if !usableSet[job] {
		return
	}
	for _, family := range families {
		if family.ColorTok != colorToken || family.Color != colorValue || family.Items["coat"].ID != coatID {
			continue
		}
		for id := range family.IDs {
			claimed[fmt.Sprintf("%s:%d", job, id)] = key
		}
	}
}

func sameColorCoatEvidence(job, colorToken string, colorValue int32, avatars []rareAvatar, families map[string][]completeFamily, claimed map[string]int) []coatEvidence {
	completeStems := map[string]bool{}
	for _, family := range families[job] {
		completeStems[family.Stem] = true
	}
	seen := map[string]bool{}
	out := []coatEvidence{}
	for _, item := range avatars {
		if item.Part != "coat" || !containsFold(item.Jobs, job) || firstRunes(item.Name, 2) != colorToken || item.Color != colorValue {
			continue
		}
		stem := trimLastRunes(item.Name, 2)
		if stem == "" || seen[stem] {
			continue
		}
		seen[stem] = true
		owners := map[int]bool{}
		partIDs := map[string]int64{}
		effect := ""
		for _, family := range families[job] {
			if family.Stem != stem {
				continue
			}
			for id := range family.IDs {
				if owner, ok := claimed[fmt.Sprintf("%s:%d", job, id)]; ok {
					owners[owner] = true
				}
			}
			partIDs = family.PartIDs
			effect = family.Effect
		}
		keys := make([]int, 0, len(owners))
		for owner := range owners {
			keys = append(keys, owner)
		}
		sort.Ints(keys)
		out = append(out, coatEvidence{Job: job, Family: stem, ColorVariation: colorValue, EffectIndex: effect, Complete: completeStems[stem], PartIDs: partIDs, OwnerKeys: keys, Unclaimed: len(keys) == 0})
	}
	return out
}

func decodeTokens(archive *platformpvf.Archive, raw []byte) ([]platformpvf.ScriptToken, error) {
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, fmt.Errorf("invalid token stream length %d", len(raw))
	}
	out := make([]platformpvf.ScriptToken, 0, len(raw)/5)
	for pos := 0; pos < len(raw); pos += 5 {
		kind := raw[pos]
		value := int32(binary.LittleEndian.Uint32(raw[pos+1:]))
		token := platformpvf.ScriptToken{Type: kind, Value: value}
		if kind == 3 || kind == 5 || kind == 6 || kind == 7 {
			token.Text = archive.ResolveString(int(value))
			token.Value = 0
		}
		out = append(out, token)
	}
	return out, nil
}

func loadListPaths(archive *platformpvf.Archive, path string) (map[int64]string, error) {
	raw, err := archive.ReadRaw(path)
	if err != nil {
		return nil, err
	}
	position := 0
	if len(raw) >= 2 && raw[0] == 0xb0 && raw[1] == 0xd0 {
		position = 2
	}
	currentID := int64(-1)
	out := map[int64]string{}
	for position+5 <= len(raw) {
		kind := raw[position]
		value := int32(binary.LittleEndian.Uint32(raw[position+1:]))
		position += 5
		switch {
		case kind == 0 || kind == 2:
			currentID = int64(value)
		case (kind == 5 || kind == 6 || kind == 7) && currentID >= 0:
			out[currentID] = archive.ResolveString(int(value))
			currentID = -1
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("list file %s produced no id/path pairs", path)
	}
	return out, nil
}

func loadReferencedCubeIDs(archive *platformpvf.Archive) (map[int64]bool, error) {
	raw, err := archive.ReadRaw(absoluteCubePath)
	if err != nil {
		return nil, err
	}
	tokens, err := decodeTokens(archive, raw)
	if err != nil {
		return nil, err
	}
	blocks, err := parseTokenBlocks(tokens)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	for _, jobs := range blocks {
		for _, block := range jobs {
			for _, id := range block.ItemIDs {
				ids[id] = true
			}
		}
	}
	return ids, nil
}

func parseJobs(text string) []string {
	section := parseSection(text, "usable job")
	matches := backtickRE.FindAllStringSubmatch(section, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}

func parseActionKey(text string) (int, error) {
	match := dreamActionKeyRE.FindStringSubmatch(text)
	if match == nil {
		return 0, fmt.Errorf("absolute bind cube action missing")
	}
	return strconv.Atoi(match[1])
}

func parseAbsoluteBindKey(text string) (int, error) {
	match := absoluteBindKeyRE.FindStringSubmatch(text)
	if match == nil {
		return 0, fmt.Errorf("absolute bind cube action missing")
	}
	return strconv.Atoi(match[1])
}

func parseSectionName(text, tag string) string {
	return strings.Trim(parseSection(text, tag), "` \t\r\n")
}

func parseSection(text, tag string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	marker := "[" + strings.ToLower(tag) + "]"
	for index, line := range lines {
		if strings.ToLower(strings.TrimSpace(line)) != marker {
			continue
		}
		var body []string
		for next := index + 1; next < len(lines); next++ {
			trimmed := strings.TrimSpace(lines[next])
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			body = append(body, lines[next])
		}
		return strings.TrimSpace(strings.Join(body, "\n"))
	}
	return ""
}

func parseEquipmentPart(text string) string {
	value := parseSection(text, "equipment type")
	if match := backtickRE.FindStringSubmatch(value); match != nil {
		value = match[1]
	}
	value = strings.ToLower(value)
	switch {
	case strings.Contains(value, "hair"):
		return "hair"
	case strings.Contains(value, "hat") || strings.Contains(value, "cap"):
		return "hat"
	case strings.Contains(value, "face"):
		return "face"
	case strings.Contains(value, "breast") || strings.Contains(value, "neck"):
		return "neck"
	case strings.Contains(value, "coat"):
		return "coat"
	case strings.Contains(value, "waist") || strings.Contains(value, "belt"):
		return "belt"
	case strings.Contains(value, "pants"):
		return "pants"
	case strings.Contains(value, "shoes"):
		return "shoes"
	default:
		return ""
	}
}

func parseVariation(text string) (int32, int32) {
	match := variationRE.FindStringSubmatch(text)
	if match == nil {
		return 0, 0
	}
	first, _ := strconv.ParseInt(match[1], 10, 32)
	second, _ := strconv.ParseInt(match[2], 10, 32)
	return int32(first), int32(second)
}

func parseStackableOptionIDs(text string) ([]int64, error) {
	section := parseSection(text, "stackable")
	values := slotNumberRE.FindAllString(section, -1)
	if len(values) == 0 || len(values)%2 != 0 {
		return nil, fmt.Errorf("outer box stackable must contain item/count pairs, got %d numbers", len(values))
	}
	out := make([]int64, 0, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		id, idErr := strconv.ParseInt(values[index], 10, 64)
		count, countErr := strconv.ParseInt(values[index+1], 10, 64)
		if idErr != nil || countErr != nil {
			return nil, fmt.Errorf("invalid outer box pair %q %q", values[index], values[index+1])
		}
		if count != 1 {
			return nil, fmt.Errorf("outer box item %d count=%d, want 1", id, count)
		}
		out = append(out, id)
	}
	return out, nil
}

func joinPVFPath(prefix, relation string) string {
	clean := strings.Trim(relation, "/\\")
	if strings.HasPrefix(strings.ToLower(clean), strings.ToLower(prefix)+"/") {
		return clean
	}
	return prefix + "/" + clean
}

func normalizePVFPath(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.Trim(value, "/\\"), "\\", "/"))
	return strings.TrimPrefix(value, "equipment/")
}

func normalizeJobs(jobs []string) []string {
	set := map[string]bool{}
	for _, job := range jobs {
		normalized := normalizeJob(job)
		if normalized != "" {
			set[normalized] = true
		}
	}
	return sortedKeys(set)
}

func normalizeJob(job string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(job), "`"))
}

func normalizePart(part string) string {
	part = strings.ToLower(strings.TrimSpace(part))
	part = strings.ReplaceAll(part, "`", "")
	part = strings.ReplaceAll(part, "[", "")
	part = strings.ReplaceAll(part, "]", "")
	part = strings.TrimSuffix(part, " avatar")
	if part == "breast" {
		return "neck"
	}
	if part == "waist" {
		return "belt"
	}
	if part == "cap" {
		return "hat"
	}
	return part
}

func isAvatarPart(value string) bool {
	part := normalizePart(value)
	for _, allowed := range avatarPartOrder {
		if part == allowed {
			return true
		}
	}
	return false
}

func blockItem(block tokenBlock, part string) (int64, bool) {
	for index, name := range avatarPartOrder {
		if name == part && index < len(block.ItemIDs) {
			return block.ItemIDs[index], true
		}
	}
	return 0, false
}

func toSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[normalizeJob(value)] = true
	}
	return out
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func firstRunes(value string, count int) string {
	runes := []rune(value)
	if len(runes) <= count {
		return value
	}
	return string(runes[:count])
}

func trimLastRunes(value string, count int) string {
	runes := []rune(value)
	if len(runes) <= count {
		return ""
	}
	return string(runes[:len(runes)-count])
}

func dominantKey(values map[string]int) string {
	best, bestCount := "", -1
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if values[key] > bestCount {
			best, bestCount = key, values[key]
		}
	}
	return best
}

func dominantInt(values map[int32]int) int32 {
	best, bestCount := int32(0), -1
	keys := make([]int32, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, key := range keys {
		if values[key] > bestCount {
			best, bestCount = key, values[key]
		}
	}
	return best
}

func qualifiedJobs(cubeID int64, jobs []string) []string {
	out := make([]string, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, fmt.Sprintf("%d:%s", cubeID, job))
	}
	return out
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, false)
}

func printAuditSummary(report auditReport) {
	fmt.Printf("passed=%t cubes=%d rare_avatars=%d complete_families=%d invalid_placeholder_blocks=%d mappings=%d missing_supported=%d candidate_omissions=%d outer_errors=%d\n",
		report.Passed, len(report.Cubes), report.RareAvatarCount, report.CompleteFamilyCount,
		len(report.RemovedBlocks), countMappings(report.RemovedBlocks), len(report.MissingSupportedJobs),
		len(report.GenuineOmissionCandidates), len(report.OuterBox.Errors))
}

func countMappings(blocks []removedBlock) int {
	total := 0
	for _, block := range blocks {
		total += len(block.ItemIDs)
	}
	return total
}

func blockSignature(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func ensureDistinctPaths(paths map[string]string) error {
	seen := map[string]string{}
	for name, path := range paths {
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		abs = filepath.Clean(abs)
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		for other, otherPath := range seen {
			same := abs == otherPath
			if runtime.GOOS == "windows" {
				same = strings.EqualFold(abs, otherPath)
			}
			if same {
				return fmt.Errorf("%s and %s paths must be distinct", name, other)
			}
		}
		seen[name] = abs
	}
	return nil
}

func writeFileAtomic(path string, data []byte, exclusive bool) error {
	if exclusive {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("output already exists: %s", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pvf-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !exclusive {
		_ = os.Remove(path)
	}
	return os.Rename(tmpName, path)
}
