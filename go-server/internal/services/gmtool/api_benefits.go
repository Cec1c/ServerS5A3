package gmtool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/testbenefits"
)

func (s *Server) benefitsReady(w http.ResponseWriter) bool {
	if s.deps.Benefits == nil {
		writeFailure(w, "当前服务未接入测试福利（需要账号注册服务）")
		return false
	}
	return true
}

func benefitsDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeFailure(w, "福利请求格式无效")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeFailure(w, "福利请求格式无效")
		return false
	}
	return true
}

func (s *Server) handleBenefits(w http.ResponseWriter, r *http.Request) {
	if !s.benefitsReady(w) {
		return
	}
	p, err := s.deps.Benefits.Policy(r.Context())
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	jobs, err := s.deps.Benefits.Jobs(r.Context())
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	campaigns, err := s.deps.Benefits.Campaigns(r.Context())
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, map[string]any{"policy": p, "jobs": jobs, "campaigns": campaigns})
}

func (s *Server) handleBenefitsCampaignCreate(w http.ResponseWriter, r *http.Request) {
	if !s.benefitsReady(w) {
		return
	}
	var req struct {
		ItemID            int64  `json:"itemId"`
		Count             int64  `json:"count"`
		Title             string `json:"title"`
		Body              string `json:"body"`
		AutoNewCharacters bool   `json:"autoNewCharacters"`
	}
	if !benefitsDecode(w, r, &req) {
		return
	}
	reward := testbenefits.Job{Title: req.Title, Body: req.Body, Attachments: []repository.MailAttachment{{ItemID: req.ItemID, Count: req.Count}}}
	if err := reward.Validate(); err != nil {
		writeFailure(w, err.Error())
		return
	}
	entry, found := s.items.lookup(uint32(req.ItemID))
	if !found || entry.Invalid {
		writeFailure(w, "物品 ID 在当前 PVF 中不存在或不可用，请先在物品库核对")
		return
	}
	if entry.Kind == grantItemKindEquipment && req.Count != 1 {
		writeFailure(w, "装备类物品每封邮件数量必须为 1")
		return
	}
	reward.Attachments[0].Extra = grantAttachmentExtra(entry, s.grantPVFDurability(entry))
	c, err := s.deps.Benefits.CreateCampaign(r.Context(), reward, entry.Name, req.AutoNewCharacters)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	s.cfg.Logger("gm-benefit-rule-created", "campaign_id", c.ID, "item_id", req.ItemID, "count", req.Count)
	writeOK(w, map[string]any{"campaign": c})
}

func (s *Server) handleBenefitsCampaignState(w http.ResponseWriter, r *http.Request) {
	if !s.benefitsReady(w) {
		return
	}
	var req struct {
		Revision *uint64 `json:"revision"`
		Enabled  *bool   `json:"enabled"`
		Confirm  bool    `json:"confirm"`
	}
	if !benefitsDecode(w, r, &req) {
		return
	}
	if req.Revision == nil || req.Enabled == nil || !req.Confirm {
		writeFailure(w, "请核对规则并确认启用或停用")
		return
	}
	c, err := s.deps.Benefits.EnableCampaign(r.Context(), r.PathValue("id"), *req.Revision, *req.Enabled)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	s.cfg.Logger("gm-benefit-rule-state", "campaign_id", c.ID, "enabled", c.Enabled, "revision", c.Revision)
	writeOK(w, map[string]any{"campaign": c})
}

func (s *Server) handleBenefitsCampaignPreview(w http.ResponseWriter, r *http.Request) {
	if !s.benefitsReady(w) {
		return
	}
	var req struct{}
	if !benefitsDecode(w, r, &req) {
		return
	}
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	j, err := s.deps.Benefits.PreviewCampaign(r.Context(), group, r.PathValue("id"))
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, map[string]any{"job": j})
}

func (s *Server) handleBenefitsPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.benefitsReady(w) {
		return
	}
	var req struct {
		Revision      *uint64 `json:"revision"`
		AccountCera   *int64  `json:"accountCera"`
		CharacterGold *int64  `json:"characterGold"`
	}
	if !benefitsDecode(w, r, &req) {
		return
	}
	if req.Revision == nil || req.AccountCera == nil || req.CharacterGold == nil {
		writeFailure(w, "缺少额度或设置版本，请刷新重试")
		return
	}
	p, err := s.deps.Benefits.SavePolicy(r.Context(), testbenefits.Policy{Revision: *req.Revision, AccountCera: *req.AccountCera, CharacterGold: *req.CharacterGold})
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	s.cfg.Logger("gm-test-benefits-policy-saved", "revision", p.Revision)
	writeOK(w, map[string]any{"policy": p})
}

func (s *Server) handleBenefitsPreview(w http.ResponseWriter, r *http.Request) {
	if !s.benefitsReady(w) {
		return
	}
	var req struct {
		Gold  int64  `json:"gold"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !benefitsDecode(w, r, &req) {
		return
	}
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	job, err := s.deps.Benefits.Preview(r.Context(), group, testbenefits.Job{Gold: req.Gold, Title: req.Title, Body: req.Body})
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	writeOK(w, map[string]any{"job": job})
}

func (s *Server) handleBenefitsRun(w http.ResponseWriter, r *http.Request) {
	if !s.benefitsReady(w) {
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
		Retry   bool `json:"retry"`
	}
	if !benefitsDecode(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeFailure(w, "请先核对预览并确认发放")
		return
	}
	group, err := s.repositories()
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	job, err := s.deps.Benefits.Run(ctx, group, r.PathValue("id"), req.Retry)
	if err != nil {
		writeFailure(w, err.Error())
		return
	}
	s.cfg.Logger("gm-test-benefits-mail-progress", "job_id", job.ID)
	writeOK(w, map[string]any{"job": job})
}
