package dnfbridge

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"

	"longheng.io/server/internal/modules/dnf/adminapi"
)

// Only runtime-owned facts and operations belong here. GM pages, credentials,
// campaigns and batch jobs live in the separately deployed GM process.
func (a *accountAPIServer) handleGameAdmin(w http.ResponseWriter, r *http.Request) {
	secret := a.service.options.playerAdminSecret
	if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req adminapi.Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || req.Version != adminapi.Version {
		http.Error(w, "invalid management request", http.StatusBadRequest)
		return
	}
	out := adminapi.Response{Version: adminapi.Version, OK: true}
	s := a.service
	switch req.Action {
	case "online":
		if req.CharacterID == 0 {
			out.OK = false
			out.Error = "character required"
			break
		}
		_, out.Online = s.onlineGameSession(req.CharacterID)
	case "catalog":
		var err error
		out.PremiumTypes, err = s.currentGMAdministrativePremiumTypes()
		if err != nil {
			out.OK = false
			out.Error = "premium catalog unavailable"
			break
		}
		catalog, err := s.currentPremiumCatalog()
		if err != nil {
			out.OK = false
			out.Error = "crystal catalog unavailable"
			break
		}
		out.CrystalCubeItemIDs = catalog.crystalCubeIDs
	case "activities", "save_activities":
		if s.activityControls == nil {
			out.OK = false
			out.Error = "activities unavailable"
			break
		}
		snapshot := s.activityControls.Snapshot()
		if req.Action == "save_activities" {
			var err error
			snapshot, err = s.activityControls.Save(r.Context(), req.Revision, req.Changes)
			if err != nil {
				out.OK = false
				out.Error = "activity revision or update invalid"
				break
			}
			s.logInfo("game-admin-activities-saved", "revision", snapshot.Revision)
		}
		out.Activities = &snapshot
	case "encode_stack":
		if req.Stack == nil || req.Slot < 0 || req.Stack.ItemID <= 0 || req.Stack.Count <= 0 {
			out.OK = false
			out.Error = "invalid stack"
			break
		}
		entry := currentItemListEntryFromStack(req.ListType, req.Slot, *req.Stack)
		out.StackRow = append([]byte(nil), entry.data[:]...)
	default:
		out.OK = false
		out.Error = "unknown operation"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
