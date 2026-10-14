package gmtool

import (
	"encoding/json"
	"io"
	"net/http"

	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/onlineevent"
)

func (s *Server) handleActivities(w http.ResponseWriter, r *http.Request) {
	if s.deps.Runtime != nil {
		result, err := s.deps.Runtime.Call(r.Context(), adminapi.Request{Action: "activities"})
		if err != nil || result.Activities == nil {
			writeFailure(w, "游戏活动接口不可用")
			return
		}
		writeOK(w, map[string]any{"activityControls": result.Activities})
		return
	}
	if s.deps.Activities == nil {
		writeFailure(w, "当前服务未接入活动管理")
		return
	}
	writeOK(w, map[string]any{"activityControls": s.deps.Activities.Snapshot()})
}

func (s *Server) handleSaveActivities(w http.ResponseWriter, r *http.Request) {
	if s.deps.Activities == nil && s.deps.Runtime == nil {
		writeFailure(w, "当前服务未接入活动管理")
		return
	}
	var request struct {
		Revision   *uint64 `json:"revision"`
		Activities []struct {
			ID      uint16 `json:"id"`
			Enabled *bool  `json:"enabled"`
		} `json:"activities"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeFailure(w, "活动设置格式无效")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeFailure(w, "活动设置格式无效")
		return
	}
	if request.Revision == nil {
		writeFailure(w, "缺少活动设置版本，请刷新后重试")
		return
	}
	changes := make([]onlineevent.ControlChange, 0, len(request.Activities))
	for _, row := range request.Activities {
		if row.Enabled == nil {
			writeFailure(w, "缺少活动开启状态")
			return
		}
		changes = append(changes, onlineevent.ControlChange{ID: row.ID, Enabled: *row.Enabled})
	}
	var snapshot onlineevent.ControlSnapshot
	var err error
	if s.deps.Runtime != nil {
		var result adminapi.Response
		result, err = s.deps.Runtime.Call(r.Context(), adminapi.Request{Action: "save_activities", Revision: *request.Revision, Changes: changes})
		if err == nil && result.Activities == nil {
			writeFailure(w, "游戏活动接口响应无效")
			return
		}
		if err == nil {
			snapshot = *result.Activities
		}
	} else {
		snapshot, err = s.deps.Activities.Save(r.Context(), *request.Revision, changes)
	}
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	s.cfg.Logger("gm-activity-settings-saved", "revision", snapshot.Revision, "restart_required", snapshot.RestartRequired)
	writeOK(w, map[string]any{"activityControls": snapshot})
}
