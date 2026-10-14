package gmtool

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/testbenefits"
)

func TestBenefitsRequiresAuthentication(t *testing.T) {
	s := &Server{www: http.NotFoundHandler(), sessions: map[string]time.Time{}}
	for _, endpoint := range []struct{ method, path string }{{"GET", "/api/benefits"}, {"POST", "/api/benefits/policy"}, {"POST", "/api/benefits/preview"}, {"POST", "/api/benefits/jobs/abc/run"}, {"POST", "/api/benefits/campaigns"}, {"POST", "/api/benefits/campaigns/abc/state"}, {"POST", "/api/benefits/campaigns/abc/preview"}} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(`{}`)))
		if w.Code != 401 {
			t.Fatalf("%s = %d", endpoint.path, w.Code)
		}
	}
}

func TestBenefitsAPIMySQL(t *testing.T) {
	dsn := os.Getenv("DNFBRIDGE_PLAYER_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	table := fmt.Sprintf("benefit_api_%d", time.Now().UnixNano())
	ctx := context.Background()
	store, err := testbenefits.NewStore(db, table)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, suffix := range []string{"_benefit_policy", "_benefit_signup", "_benefit_jobs", "_benefit_campaigns", ""} {
			db.Exec("DROP TABLE IF EXISTS " + table + suffix)
		}
	}()
	if _, err = db.Exec("CREATE TABLE " + table + " (id INT PRIMARY KEY,account_id VARCHAR(64))"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO " + table + " VALUES (1,'fixture')"); err != nil {
		t.Fatal(err)
	}
	g := memory.NewMemoryGroup()
	if err = g.Account.Save(ctx, repository.AccountRecord{AccountID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err = g.Character.Save(ctx, repository.CharacterRecord{CharacterID: "1", AccountID: "fixture", Name: "fixture"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{Logger: func(string, ...any) {}}, deps: Deps{Benefits: store, Repos: func() (repository.Group, bool) { return g, true }}, www: http.NotFoundHandler(), sessions: map[string]time.Time{}}
	token, err := s.startSession()
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, body string) map[string]any {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, body := range []string{`{}`, `{"revision":0,"accountCera":-1,"characterGold":0}`, `{"revision":0,"accountCera":1,"characterGold":0,"unknown":0}`, `{"revision":0,"accountCera":1,"characterGold":0} {}`} {
		if call("/api/benefits/policy", body)["success"] != false {
			t.Fatal(body)
		}
	}
	if call("/api/benefits/policy", `{"revision":0,"accountCera":123,"characterGold":456}`)["success"] != true {
		t.Fatal("policy save")
	}
	preview := call("/api/benefits/preview", `{"gold":789,"title":"fixture","body":"gold"}`)
	if preview["success"] != true {
		t.Fatal(preview)
	}
	id := preview["job"].(map[string]any)["id"].(string)
	path := "/api/benefits/jobs/" + id + "/run"
	for _, body := range []string{`{}`, `{"confirm":false}`, `{"confirm":true,"gold":999}`} {
		if call(path, body)["success"] != false {
			t.Fatal("unconfirmed/tampered run accepted")
		}
	}
	m, _, err := g.Mailbox.Load(ctx, "1")
	if err != nil || len(m.Mails) != 0 {
		t.Fatal("preview wrote mailbox")
	}
	for range 2 {
		if got := call(path, `{"confirm":true}`); got["success"] != true {
			t.Fatal(got)
		}
	}
	m, _, err = g.Mailbox.Load(ctx, "1")
	if err != nil || len(m.Mails) != 1 || m.Mails["1"].Gold != 789 {
		t.Fatal("wrong mail delivery")
	}
	s.items = &itemIndex{ready: true, entries: []itemEntry{{ID: 490009735, Name: "fixture gift", Kind: "stackable", PVFPath: "fixture.stk"}, {ID: 1, Name: "fixture equipment", Kind: "equipment"}}, byID: map[uint32]int{490009735: 0, 1: 1}}
	for _, body := range []string{`{"itemId":2,"count":1,"title":"test"}`, `{"itemId":490009735,"count":0,"title":"test"}`, `{"itemId":1,"count":2,"title":"test"}`, `{"itemId":490009735,"count":1,"title":"test","attachments":[]}`} {
		if got := call("/api/benefits/campaigns", body); got["success"] != false {
			t.Fatal("invalid item rule accepted", got)
		}
	}
	created := call("/api/benefits/campaigns", `{"itemId":490009735,"count":1,"title":"test","autoNewCharacters":true}`)
	if created["success"] != true {
		t.Fatal(created)
	}
	c := created["campaign"].(map[string]any)
	if c["enabled"] != false || c["itemName"] != "fixture gift" {
		t.Fatal(c)
	}
	base := "/api/benefits/campaigns/" + c["id"].(string)
	for _, body := range []string{`{}`, `{"revision":0,"enabled":true}`, `{"revision":0,"enabled":true,"confirm":false}`} {
		if call(base+"/state", body)["success"] != false {
			t.Fatal("unconfirmed enable accepted")
		}
	}
	if got := call(base+"/state", `{"revision":0,"enabled":true,"confirm":true}`); got["success"] != true {
		t.Fatal(got)
	}
	for range 2 {
		p := call(base+"/preview", `{}`)
		if p["success"] != true {
			t.Fatal(p)
		}
		id := p["job"].(map[string]any)["id"].(string)
		if got := call("/api/benefits/jobs/"+id+"/run", `{"confirm":true}`); got["success"] != true {
			t.Fatal(got)
		}
	}
	m, _, err = g.Mailbox.Load(ctx, "1")
	if err != nil || len(m.Mails) != 2 || m.Mails["2"].Attachments[0].Extra["mailbox_pvf_kind"] != "stackable" {
		t.Fatal("wrong or duplicate item mail", err, m)
	}
}
