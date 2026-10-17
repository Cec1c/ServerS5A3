package dnfbridge

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestEquipRequiredSkillsStayInTheCharactersJobNamespace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		job   byte
		text  string
		want  []int64
		valid bool
	}{
		{"swordman", 0, "`[swordman]` 33 `[demonic swordman]` 33 `[at swordman]` 95", []int64{33}, true},
		{"demonic_swordman", 9, "`[swordman]` 33 `[demonic swordman]` 33 `[at swordman]` 95", []int64{33}, true},
		{"female_swordman", 11, "`[swordman]` 33 `[demonic swordman]` 33 `[at swordman]` 95", []int64{95}, true},
		{"other_job", 1, "`[swordman]` 33 `[at swordman]` 95", nil, true},
		{"exact_tag_only", 0, "`[at swordman]` 95", nil, true},
		{"case_and_spaces", 11, "`  [AT SWORDMAN]  ` 95", []int64{95}, true},
		{"duplicate_job_row", 0, "`[swordman]` 33 `[swordman]` 33", []int64{33}, true},
		{"missing_id", 0, "`[swordman]`", nil, false},
		{"wrong_id_type", 0, "`[swordman]` `33`", nil, false},
		{"wrong_tag_type", 0, "1 33", nil, false},
		{"zero_id", 0, "`[swordman]` 0", nil, false},
		{"overflow_id", 0, "`[swordman]` 65536", nil, false},
		{"unknown_job", 255, "`[swordman]` 33", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := dnfpvf.Parse("audit.equ", "[required job skill]\n"+tc.text+"\n[/required job skill]\n")
			if err != nil {
				t.Fatal(err)
			}
			got, valid := equipRequiredSkillIDsForJob(doc, tc.job)
			if valid != tc.valid || len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("ids=%v valid=%t, want %v valid=%t", got, valid, tc.want, tc.valid)
			}
		})
	}
}

func TestEquipRequirementUnreadablePairsCannotBecomeSatisfiedRows(t *testing.T) {
	source := auditWeaponSource()
	source[strings.ToLower(auditBeamswordPath)] = "[required job skill]\n`[swordman]` 33 `[at swordman]`\n[/required job skill]\n"
	rows, unreadable := wornEquipmentRequiredSkillRows(source, map[string]dnfrepo.EquipmentEntry{
		"12": {SlotIndex: 12, ItemID: auditBeamswordID, Extra: map[string]string{"pvf_path": auditBeamswordPath}},
		"11": {SlotIndex: 11, ItemID: auditClubID, Extra: map[string]string{"pvf_path": auditClubPath}},
	}, 0)
	if len(rows) != 0 || unreadable != 1 {
		t.Fatalf("rows=%v unreadable=%d, want no classified rows and one unknown row", rows, unreadable)
	}
}

func TestEquipRequirementAuditSkipsAnUnsupportedJob(t *testing.T) {
	t.Setenv(skillEquipRequirementAuditEnv, "")
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{packetLog: logger}
	service.auditLoginEquipRequirements(&gameSession{connID: "audit-unknown-job"}, context.Background(),
		dnfrepo.CharacterRecord{CharacterID: "audit-unknown-job"},
		currentSceneSkillInfoProjection{job: 255}, "test_unknown_job")
	log := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(log, skillEquipRequirementSkippedEvent) || !strings.Contains(log, "reason=unsupported_job") || strings.Contains(log, "all_requirements_served") {
		t.Fatalf("unknown job must remain unknown; log=\n%s", log)
	}
}

// A numeric skill ID belongs to one job's namespace. Job 11's skill 33 must
// not satisfy the swordman-33 row when its own lightsaber row requires 95.
func TestRealPVFEquipRequirementAuditDoesNotBorrowAnotherJobsSkillID(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(skillEquipRequirementAuditEnv, "")
	for _, tc := range []struct {
		name       string
		job        byte
		levels     map[int64]int
		missing    bool
		unreadable bool
	}{
		{"female_swordman_other_job_id_only", 11, map[int64]int{33: 10}, true, false},
		{"female_swordman_own_mastery", 11, map[int64]int{95: 1}, false, false},
		{"swordman_other_job_id_only", 0, map[int64]int{95: 1}, true, false},
		{"swordman_own_mastery", 0, map[int64]int{33: 1}, false, false},
		{"unreadable_row_is_not_a_success", 0, map[int64]int{33: 1}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, logPath := newPeerEndpointTestLogger(t)
			repositories := dnfrepomemory.NewMemoryGroup()
			pvfPath := auditBeamswordPath
			if tc.unreadable {
				pvfPath = "equipment/missing-audit-fixture871.equ"
			}
			if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
				CharacterID: "audit-job871",
				Entries: map[string]dnfrepo.EquipmentEntry{
					"12": {SlotIndex: 12, ItemID: auditBeamswordID, Extra: map[string]string{"pvf_path": pvfPath}},
				},
			}); err != nil {
				t.Fatal(err)
			}
			service := &Service{
				packetLog: logger, initialEquipmentArchive: archive,
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
			}
			service.auditLoginEquipRequirements(&gameSession{connID: "audit-job871"}, context.Background(),
				dnfrepo.CharacterRecord{CharacterID: "audit-job871"},
				currentSceneSkillInfoProjection{characterID: "audit-job871", job: tc.job, learnedLevels: tc.levels},
				"test_job_qualified_requirement")
			log := readPeerEndpointTestLog(t, logger, logPath)
			verdict := "all_requirements_served"
			if tc.missing {
				verdict = "requirement_missing_in_served_skill_page"
			}
			if tc.unreadable {
				verdict = "requirement_audit_incomplete"
			}
			if !strings.Contains(log, skillEquipRequirementAuditEvent) || !strings.Contains(log, "verdict="+verdict) {
				t.Fatalf("missing audit verdict %s; log=\n%s", verdict, log)
			}
			if got := strings.Contains(log, skillEquipRequirementMissingEvent); got != tc.missing {
				t.Fatalf("missing=%t want=%t; skill IDs must be evaluated in job %d; log=\n%s", got, tc.missing, tc.job, log)
			}
		})
	}
}
