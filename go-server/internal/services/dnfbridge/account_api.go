package dnfbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	"longheng.io/server/internal/modules/dnf/signupbenefits"
)

// 远程部署用的账号 HTTP API。
//
// 本地 Windows 一键端由 DNF90Launcher + loopback 管理端完成「注册/登录 + 按 PID
// 绑定」。远程玩家没有 loopback 管理端、服务端也拿不到客户端进程 PID，
// 因此这里提供一个部署方自己控制的公网 HTTP 入口，让启动器可以：
//
//	POST /api/account/register  {"account","password"}  注册
//	POST /api/account/login     {"account","password"}  登录并签发启动器令牌
//	GET  /api/status                                    健康检查
//
// 服务端只保存 bcrypt 哈希，不保存明文密码。
//
// 注意（2026-09-25 起）：登录响应里的 `token` 是游戏连接认账号的**唯一**依据，
// 启动器必须把它写进 TCLS 启动串的 outer token 字段。这里**不再**把调用方来源 IP
// 绑定到账号 —— 现场取证表明启动器 HTTP 出口 IP 与游戏连接来源 IP 不是同一个
// 地址（见 client_account_ip.go）。last_login_at/last_login_ip 仍然写库，但它们
// 只是审计事实，绝不参与账号判定。

const (
	defaultAccountAPIListen       = ""
	defaultAccountAPITable        = "dnf90_web_accounts"
	defaultAccountAPIMinPassword  = 6
	defaultAccountAPIMaxPassword  = 64
	defaultAccountAPIRatePerMin   = 30
	accountAPIBaseAccountID       = 10000
	accountAPIMaxBodyBytes        = 4096
	accountAPIReadHeaderTimeout   = 5 * time.Second
	accountAPIReadTimeout         = 10 * time.Second
	accountAPIWriteTimeout        = 10 * time.Second
	accountAPIIdleTimeout         = 60 * time.Second
	accountAPIShutdownGracePeriod = 5 * time.Second
)

var accountAPIUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,20}$`)

type accountAPIServer struct {
	moderationMu  sync.RWMutex
	listen        string
	table         string
	allowRegister bool
	minPassword   int

	// 客户端补丁分发（只读，见 account_api_patch.go 与 patch_files.go）。
	// patchDir 是补丁负载目录，patchMaxBytes 是单文件下载上限（测试可覆盖）。
	patchDir      string
	patchMaxBytes int64
	// patchPayloadDir 是 /patch/* 那套端点的负载目录（DNFBRIDGE_PATCH_DIR）。
	patchPayloadDir string

	service *Service
	dsn     string
	db      *sql.DB
	http    *http.Server

	rateMu   sync.Mutex
	rateHits map[string][]time.Time
}

type accountAPIRequest struct {
	boundQQ          string
	NewPassword      string `json:"newPassword"`
	RequireQQBinding bool   `json:"requireQQBinding"`
	Account          string `json:"account"`
	Password         string `json:"password"`
}

type accountAPIResponse struct {
	Code        string   `json:"code,omitempty"`
	RetryAfter  int64    `json:"retryAfter,omitempty"`
	BindingCode string   `json:"bindingCode,omitempty"`
	BoundQQ     string   `json:"boundQQ,omitempty"`
	ExpiresAt   int64    `json:"expiresAt,omitempty"`
	Groups      []string `json:"groups,omitempty"`
	OK          bool     `json:"ok"`
	AccountID   string   `json:"account_id,omitempty"`
	// Token 是本次登录的启动器令牌：启动器把它写进 TCLS 启动串的 outer token
	// 字段，客户端会原样转发，服务端据此精确认账号（见 account_login_token.go）。
	Token   string `json:"token,omitempty"`
	Message string `json:"message,omitempty"`
}

// startAccountAPI 按配置启动公网账号 API；未配置监听地址时直接跳过。
func (s *Service) startAccountAPI(ctx context.Context) error {
	listen := strings.TrimSpace(s.options.accountAPIListen)
	if listen == "" {
		return nil
	}
	dsn := strings.TrimSpace(s.options.accountAPIDSN)
	if dsn == "" {
		return errors.New("dnfbridge account API requires DNFBRIDGE_ACCOUNT_API_DSN")
	}
	table := strings.TrimSpace(s.options.accountAPITable)
	if table == "" {
		table = defaultAccountAPITable
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(table) {
		return fmt.Errorf("dnfbridge account API table name %q is invalid", table)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open account API database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("ping account API database: %w", err)
	}

	api := &accountAPIServer{
		listen:          listen,
		table:           table,
		allowRegister:   s.options.accountAPIRegister,
		minPassword:     defaultAccountAPIMinPassword,
		patchDir:        s.options.patchDistDir,
		patchPayloadDir: s.options.patchPayloadDir,
		service:         s,
		dsn:             dsn,
		db:              db,
		rateHits:        make(map[string][]time.Time),
	}
	if err := api.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", api.handleStatus)
	mux.HandleFunc("/api/account/register", api.handleRegister)
	mux.HandleFunc("/api/account/login", api.handleLogin)
	mux.HandleFunc("/api/account/binding-code", api.handleAccountSecurity)
	mux.HandleFunc("/api/account/password", api.handleAccountSecurity)
	mux.HandleFunc("/api/game/admin", api.handleGameAdmin)
	// 客户端补丁"云端更新"只读端点（复用同一个 7002，不新开端口）。
	// 前缀跟 /api/account/* 一致：GET /api/patch/manifest.json 与
	// GET /api/patch/file?path=<相对路径>；实现见 account_api_patch.go
	// （白名单 + filepath.Clean 前缀校验防穿越 + 单文件上限，清单由
	// /root/make-patch-manifest.sh 生成到 DNFBRIDGE_PATCH_DIST_DIR）。
	mux.HandleFunc("/api/patch/manifest.json", api.handlePatchManifest)
	mux.HandleFunc("/api/patch/file", api.handlePatchFile)
	// 新一套补丁端点（登陆器云端自动更新的**主**端点，实现见 patch_files.go）：
	//	GET /patch/manifest.json      清单（/root/patch-publish.sh 生成）
	//	GET /patch/files/<相对路径>    文件本体，http.ServeFile 自带 Range/206（断点续传）
	// 负载目录 = DNFBRIDGE_PATCH_DIR（默认 /opt/dnf90/patch-payload）；目录不存在时整体 404。
	// 旧的 /api/patch/* 保留：已经发出去的旧登陆器只认那套，删掉会让它们没法自动更新。
	mux.HandleFunc("/patch/", func(w http.ResponseWriter, r *http.Request) {
		if !api.handlePatchFiles(w, r) {
			http.NotFound(w, r)
		}
	})
	api.http = &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: accountAPIReadHeaderTimeout,
		ReadTimeout:       accountAPIReadTimeout,
		WriteTimeout:      accountAPIWriteTimeout,
		IdleTimeout:       accountAPIIdleTimeout,
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("listen account API %s: %w", listen, err)
	}
	s.mu.Lock()
	s.accountAPI = api
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.logInfo("dnfbridge account API listening",
			"addr", listen,
			"table", table,
			"register_enabled", api.allowRegister)
		if serveErr := api.http.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.logWarn("dnfbridge account API stopped unexpectedly", "error", serveErr)
		}
	}()
	return nil
}

func (s *Service) stopAccountAPI() error {
	s.mu.Lock()
	api := s.accountAPI
	s.accountAPI = nil
	s.mu.Unlock()
	if api == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), accountAPIShutdownGracePeriod)
	defer cancel()
	var err error
	if api.http != nil {
		err = api.http.Shutdown(shutdownCtx)
	}
	if api.db != nil {
		err = errors.Join(err, api.db.Close())
	}
	return err
}

func (a *accountAPIServer) ensureSchema(ctx context.Context) error {
	statement := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id BIGINT NOT NULL AUTO_INCREMENT,
		account VARCHAR(64) NOT NULL,
		password_hash VARCHAR(100) NOT NULL,
		account_id VARCHAR(64) NOT NULL,
		created_at DATETIME NOT NULL,
		last_login_at DATETIME NULL,
		last_login_ip VARCHAR(45) NULL,
		PRIMARY KEY (id),
		UNIQUE KEY uniq_account (account),
		UNIQUE KEY uniq_account_id (account_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`, a.table)
	if _, err := a.db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create account API table %s: %w", a.table, err)
	}
	if err := a.ensureAccountSecuritySchema(ctx); err != nil {
		return err
	}
	if err := a.ensureLoginTokenSchema(ctx); err != nil {
		return err
	}
	if err := a.ensureModerationSchema(ctx); err != nil {
		return err
	}
	benefits, err := signupbenefits.NewStore(a.db, a.table)
	if err != nil {
		return err
	}
	if err = benefits.EnsureSchema(ctx); err != nil {
		return err
	}
	if a.service != nil {
		a.service.signupBenefits = benefits
	}
	return nil
}

// loginTokenTable 保存启动器令牌的表名，跟随账号表名便于多实例隔离。
func (a *accountAPIServer) loginTokenTable() string {
	if a == nil {
		return ""
	}
	return a.table + "_tokens"
}

func (a *accountAPIServer) ensureLoginTokenSchema(ctx context.Context) error {
	table := a.loginTokenTable()
	if table == "" {
		return nil
	}
	statement := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		token VARCHAR(64) NOT NULL,
		account VARCHAR(64) NOT NULL,
		account_id VARCHAR(64) NOT NULL,
		created_at DATETIME NOT NULL,
		expires_at DATETIME NOT NULL,
		last_used_at DATETIME NULL,
		last_used_ip VARCHAR(45) NULL,
		use_count BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (token),
		KEY idx_login_token_expires (expires_at),
		KEY idx_login_token_account_id (account_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`, table)
	if _, err := a.db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create account login token table %s: %w", table, err)
	}
	return nil
}

func (a *accountAPIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAccountAPIError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	writeAccountAPIJSON(w, http.StatusOK, accountAPIResponse{
		OK:      true,
		Message: "dnf90 account api ready",
	})
}

func (a *accountAPIServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAccountAPIError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if !a.allowRegister {
		writeAccountAPIError(w, http.StatusForbidden, "本服务器当前关闭了自助注册，请联系管理员开号")
		return
	}
	request, ok := a.decodeRequest(w, r)
	if !ok {
		return
	}
	if err := a.validateCredentials(request); err != nil {
		writeAccountAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), accountAPIWriteTimeout)
	defer cancel()

	var existing string
	query := fmt.Sprintf("SELECT account FROM %s WHERE account = ?", a.table)
	switch err := a.db.QueryRowContext(ctx, query, request.Account).Scan(&existing); {
	case err == nil:
		writeAccountAPIError(w, http.StatusConflict, "该账号名已被注册")
		return
	case !errors.Is(err, sql.ErrNoRows):
		writeAccountAPIError(w, http.StatusInternalServerError, "查询账号失败")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "密码处理失败")
		return
	}

	ip := accountAPIClientIP(r)
	a.moderationMu.RLock()
	defer a.moderationMu.RUnlock()
	if err := a.checkModeration(ctx, "", ip); err != nil {
		writeAccountAPIError(w, http.StatusForbidden, "该 IP 暂不可注册，请联系管理员")
		return
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	defer func() { _ = tx.Rollback() }()

	placeholder := fmt.Sprintf("pending:%d", time.Now().UnixNano())
	insert := fmt.Sprintf(
		"INSERT INTO %s (account, password_hash, account_id, created_at) VALUES (?, ?, ?, ?)",
		a.table)
	result, err := tx.ExecContext(ctx, insert, request.Account, string(hash), placeholder, time.Now())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeAccountAPIError(w, http.StatusConflict, "该账号名已被注册")
			return
		}
		writeAccountAPIError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	accountID := fmt.Sprintf("dnf:%d", accountAPIBaseAccountID+rowID)
	update := fmt.Sprintf("UPDATE %s SET account_id = ? WHERE id = ?", a.table)
	if _, err := tx.ExecContext(ctx, update, accountID, rowID); err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+a.table+"_registration_sources (account_id, ip) VALUES (?, ?)", accountID, ip); err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	if a.service.signupBenefits != nil {
		if err := a.service.signupBenefits.CaptureSignup(ctx, tx, accountID); err != nil {
			writeAccountAPIError(w, http.StatusInternalServerError, "创建账号福利记录失败")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}

	writeAccountAPIJSON(w, http.StatusCreated, accountAPIResponse{
		OK:        true,
		AccountID: accountID,
		Message:   "注册成功，现在可以直接登录进游戏",
	})
}

func (a *accountAPIServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAccountAPIError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	request, ok := a.decodeRequest(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(request.Account) == "" || request.Password == "" {
		writeAccountAPIError(w, http.StatusBadRequest, "请填写账号和密码")
		return
	}

	ip := accountAPIClientIP(r)
	if !a.allowRate(ip) {
		writeAccountAPIError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), accountAPIWriteTimeout)
	defer cancel()

	tx, accountID, storedHash, err := a.credentialTransaction(ctx, request.Account, request.Password)
	if err != nil {
		a.securityFailure(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.securityFailure(w, err)
		return
	}
	if err = a.requireQQBinding(ctx, accountID, request.RequireQQBinding); err != nil {
		a.securityFailure(w, err)
		return
	}
	if err = a.db.QueryRowContext(ctx, "SELECT qq FROM "+a.table+"_qq_bindings WHERE account_id=?", accountID).Scan(&request.boundQQ); err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.securityFailure(w, err)
		return
	}
	a.moderationMu.RLock()
	defer a.moderationMu.RUnlock()
	if err := a.checkModeration(ctx, accountID, ip); err != nil {
		writeAccountAPIError(w, http.StatusForbidden, "该账号或 IP 暂不可登录，请联系管理员")
		return
	}

	if a.service.options.accountAPILoginToken {
		a.handleLoginReplacement(ctx, w, request, accountID, storedHash, ip)
		return
	}

	update := fmt.Sprintf("UPDATE %s SET last_login_at = ?, last_login_ip = ? WHERE account = ?", a.table)
	if _, err := a.db.ExecContext(ctx, update, time.Now(), ip, request.Account); err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "登录失败")
		return
	}

	// Explicit token-disabled mode retains its existing login response.

	writeAccountAPIJSON(w, http.StatusOK, accountAPIResponse{
		OK:        true,
		AccountID: accountID,
		BoundQQ:   request.boundQQ,
		Message:   "登录成功",
	})
}

func (a *accountAPIServer) decodeRequest(w http.ResponseWriter, r *http.Request) (accountAPIRequest, bool) {
	var request accountAPIRequest
	body := http.MaxBytesReader(w, r.Body, accountAPIMaxBodyBytes)
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&request); err != nil {
		writeAccountAPIError(w, http.StatusBadRequest, "请求格式不正确")
		return request, false
	}
	if err := decoder.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
		writeAccountAPIError(w, http.StatusBadRequest, "请求格式不正确")
		return request, false
	}
	request.Account = strings.TrimSpace(request.Account)
	return request, true
}

func (a *accountAPIServer) validateCredentials(request accountAPIRequest) error {
	if !accountAPIUsernamePattern.MatchString(request.Account) {
		return errors.New("账号名只能是 3-20 位字母、数字或下划线")
	}
	if len(request.Password) < a.minPassword {
		return fmt.Errorf("密码至少 %d 位", a.minPassword)
	}
	if len(request.Password) > defaultAccountAPIMaxPassword {
		return fmt.Errorf("密码不能超过 %d 位", defaultAccountAPIMaxPassword)
	}
	return nil
}

func (a *accountAPIServer) allowRate(ip string) bool {
	if ip == "" {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	hits := a.rateHits[ip][:0]
	for _, hit := range a.rateHits[ip] {
		if hit.After(cutoff) {
			hits = append(hits, hit)
		}
	}
	if len(hits) >= defaultAccountAPIRatePerMin {
		a.rateHits[ip] = hits
		return false
	}
	a.rateHits[ip] = append(hits, now)
	return true
}

func accountAPIClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		if idx := strings.Index(forwarded, ","); idx >= 0 {
			forwarded = forwarded[:idx]
		}
		if ip := normalizeClientIP(forwarded); ip != "" {
			return ip
		}
	}
	if r.RemoteAddr == "" {
		return ""
	}
	return normalizeClientIP(r.RemoteAddr)
}

func writeAccountAPIJSON(w http.ResponseWriter, status int, payload accountAPIResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAccountAPIError(w http.ResponseWriter, status int, message string) {
	writeAccountAPIJSON(w, status, accountAPIResponse{OK: false, Message: message})
}
