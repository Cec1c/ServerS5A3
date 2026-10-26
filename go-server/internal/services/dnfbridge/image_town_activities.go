package dnfbridge

import (
	"fmt"

	"longheng.io/server/internal/modules/dnf/onlineevent"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// These are native NPC availability gates, not reward implementations. The
// current Seria map binds the named NPCs; each NPC owns its appearance and roles.
// They stay disabled until the operator saves a policy and restarts the server.
type imageTownActivity struct {
	id                      uint16
	name, path, description string
}

func currentImageTownActivities() []imageTownActivity {
	return []imageTownActivity{
		{157, "闪亮的仙灵（火之仙灵 / 公主殿下）", "npc/2019_Ready_to_Newyear_Dungeon.npc", "图片右上蓝色入口；原生气泡台词为火之仙灵要求守护公主殿下。157为共享活动号，也可能开放其他关联入口；本开关不代替商店、任务、副本及奖励实现。"},
		{2099, "征集令使者", "npc/dnfwithfriend_event.npc", "显示征集令使者入口；同一2099事件还控制真档中的其他征集令使者版本。回归资格、商店与奖励仍按各自规则处理。"},
		{2222, "饺子大师", "npc/chn_newyear_lucky.npc", "显示饺子大师及其原生入口；本开关不代替商店兑换、运势结果或奖励处理。"},
		{2469, "机敏的狗宝宝（资源名：饥饿的狗宝宝）", "npc/chn_event_hungry_forest.npc", "按截图位置与动画匹配贪食之森狗宝宝入口；真档名称为饥饿的狗宝宝，图片中的机敏名称与成长阶段的关联尚待核定。喂养、成长状态与奖励不由入口开关生成。"},
		{2475, "遗迹挖掘点", "npc/event_cardrogue_seria_gate.npc", "显示遗迹挖掘点及进入活动城镇入口；活动城镇准入、小游戏进度与奖励仍需对应玩法支持。"},
	}
}

func currentImageTownActivityDefinitions() []onlineevent.ControlDefinition {
	var definitions []onlineevent.ControlDefinition
	for _, activity := range currentImageTownActivities() {
		definitions = append(definitions, onlineevent.ControlDefinition{
			ID: activity.id, Name: activity.name, Description: activity.description,
			Source: activity.path + " [open visible event]", Group: "图片活动NPC", AvailabilityOnly: true,
		})
	}
	return definitions
}

func validateCurrentImageTownActivity(source dnfpvf.Source, activity imageTownActivity) error {
	if source == nil {
		return fmt.Errorf("%s", currentImageTownActivitySourceUnavailable)
	}
	text, err := source.ReadText(activity.path)
	if err != nil {
		return err
	}
	document, err := dnfpvf.Parse(activity.path, text)
	if err != nil {
		return err
	}
	ids := document.Ints("open visible event")
	if len(ids) != 1 || ids[0] != int64(activity.id) {
		return fmt.Errorf("town activity %d NPC gate mismatch", activity.id)
	}
	return nil
}

// currentImageTownActivitySourceUnavailable is the diagnostic reason used when
// no resource archive at all is available. It is a diagnostic string, never a
// fabricated resource or id.
const currentImageTownActivitySourceUnavailable = "town activity resource unavailable"

// imageTownActivityDiagnostic names one enabled optional entry that was dropped
// because its own resource check failed. It carries the real id and the real
// failure text so the field log can be acted on without guessing.
type imageTownActivityDiagnostic struct {
	id     uint16
	reason string
}

// selectValidImageTownActivities is the minimal resource-producing helper for
// the optional image town-NPC rows. It validates each requested id
// independently against source and returns the ids that pass, in request order,
// followed by one diagnostic per dropped id.
//
// A nil source means the resource archive itself is unavailable: every
// requested id is dropped. Per-entry failures drop only that id. A healthy
// entry is never dropped because a different entry failed, and no id is ever
// invented or substituted. Only the optional ids in ids are considered; the
// always-present catalog rows (2347 spend time, 350 epic road, 181/2365/282/…)
// are not part of this helper and are untouched by it.
func selectValidImageTownActivities(source dnfpvf.Source, ids []uint16) ([]uint16, []imageTownActivityDiagnostic) {
	if len(ids) == 0 {
		return nil, nil
	}
	if source == nil {
		diagnostics := make([]imageTownActivityDiagnostic, 0, len(ids))
		for _, id := range ids {
			diagnostics = append(diagnostics, imageTownActivityDiagnostic{id: id, reason: currentImageTownActivitySourceUnavailable})
		}
		return nil, diagnostics
	}
	valid := make([]uint16, 0, len(ids))
	var diagnostics []imageTownActivityDiagnostic
	for _, id := range ids {
		activity, known := currentImageTownActivity(id)
		if !known {
			// Unreachable through the catalog caller, which builds ids from
			// the same table. Report instead of guessing a document path.
			diagnostics = append(diagnostics, imageTownActivityDiagnostic{id: id, reason: "unregistered optional image town activity"})
			continue
		}
		if err := validateCurrentImageTownActivity(source, activity); err != nil {
			diagnostics = append(diagnostics, imageTownActivityDiagnostic{id: id, reason: err.Error()})
			continue
		}
		valid = append(valid, id)
	}
	if len(valid) == 0 {
		valid = nil
	}
	return valid, diagnostics
}

// currentImageTownActivity looks one optional id up in the real table. It exists
// so the helper never invents a resource path for an id it cannot place.
func currentImageTownActivity(id uint16) (imageTownActivity, bool) {
	for _, activity := range currentImageTownActivities() {
		if activity.id == id {
			return activity, true
		}
	}
	return imageTownActivity{}, false
}

// logCurrentImageTownActivitySkips logs one diagnostic per dropped optional
// entry and returns the ids that passed their own resource gate.
//
// The caller resolves the resource source, so the default all-off path never
// reaches here and never loads the archive:
//   - ids is empty when every optional switch is off;
//   - source is nil when the archive itself is unusable.
//
// It returns no error by design: none of these entries may abort the catalog.
func (s *Service) logCurrentImageTownActivitySkips(activitySource dnfpvf.Source, ids []uint16, session *gameSession, source string) []uint16 {
	if s == nil || len(ids) == 0 {
		return nil
	}
	valid, diagnostics := selectValidImageTownActivities(activitySource, ids)
	for _, diagnostic := range diagnostics {
		s.logGameEvent(session, "game-image-town-activity-skipped",
			"source", source,
			"activity_id", diagnostic.id,
			"error", diagnostic.reason,
			"valid_count", len(valid),
			"skipped_count", len(diagnostics),
			"policy", "skip_unreadable_optional_entry_and_keep_legacy_catalog")
	}
	return valid
}

func validateCurrentImageTownActivityIDs(ids []uint16) error {
	seen := make(map[uint16]bool)
	for _, id := range ids {
		known := false
		for _, activity := range currentImageTownActivities() {
			known = known || id == activity.id
		}
		if !known || seen[id] {
			return fmt.Errorf("unsupported or duplicate town activity %d", id)
		}
		seen[id] = true
	}
	return nil
}
