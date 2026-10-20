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
	Account  string `json:"account"`
	Password string `json:"password"`
	Device   string `json:"device"`
}

type accountAPIResponse struct {
	OK        bool   `json:"ok"`
	AccountID string `json:"account_id,omitempty"`
	// Token 是本次登录的启动器令牌：启动器把它写进 TCLS 启动串的 outer token
	// 字段，客户端会原样转发，服务端据此精确认账号（见 account_login_token.go）。
	Token   string `json:"token,omitempty"`
	Message string `json:"message,omitempty"`
}

// startAccountAPI 按配置启动公网账号 API；未配置监听地址时直接跳过。
func (s *Service) startAccountAPI(ctx context.Context) error {
	if s.options.playerStrict && (!s.options.accountAPILoginToken || len(s.options.playerProxySecret) < 32) {
		return errors.New("strict player mode requires token authentication and a proxy key of at least 32 characters")
	}
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
	mux.HandleFunc("/api/player/admin", api.handlePlayerAdmin)
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
	if err := a.ensureLoginTokenSchema(ctx); err != nil {
		return err
	}
	if err := a.ensurePlayerSchema(ctx); err != nil {
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

// issueLoginToken 为一次成功登录签发令牌并落库。
func (a *accountAPIServer) issueLoginToken(ctx context.Context, account string, accountID string, ip string) (string, error) {
	token, err := newLoginToken()
	if err != nil {
		return "", fmt.Errorf("generate login token: %w", err)
	}
	table := a.loginTokenTable()
	now := time.Now()
	expiresAt := now.Add(loginTokenTTL)
	insert := fmt.Sprintf(
		"INSERT INTO %s (token, account, account_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		table)
	if _, err := a.db.ExecContext(ctx, insert, token, account, accountID, now, expiresAt); err != nil {
		return "", fmt.Errorf("store login token: %w", err)
	}
	// 顺手清理过期令牌，避免表无限增长。
	cleanup := fmt.Sprintf("DELETE FROM %s WHERE expires_at < ?", table)
	if _, err := a.db.ExecContext(ctx, cleanup, now); err != nil && a.service != nil {
		a.service.logWarn("dnfbridge login token cleanup failed", "error", err)
	}
	if a.service != nil {
		a.service.loginTokens.remember(token, accountID, expiresAt)
	}
	return token, nil
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
	ip := a.clientIP(r)
	if (a.service.options.playerStrict && ip == "") || !a.allowRate(ip) {
		writeAccountAPIError(w, http.StatusTooManyRequests, "请从新版登录器注册，或稍后重试")
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

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := a.takeRegistrationSlot(ctx, tx, ip); err != nil {
		writeAccountAPIError(w, http.StatusForbidden, "该 IP 已达到注册上限，或注册服务暂不可用")
		return
	}

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
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+a.table+"_registration_sources (account_id, ip) VALUES (?, ?)", accountID, ip); err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	update := fmt.Sprintf("UPDATE %s SET account_id = ? WHERE id = ?", a.table)
	if _, err := tx.ExecContext(ctx, update, accountID, rowID); err != nil {
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

	ip := a.clientIP(r)
	if a.service.options.playerStrict && (ip == "" || !playerDevicePattern.MatchString(request.Device)) {
		writeAccountAPIError(w, http.StatusBadRequest, "请使用新版登录器登录")
		return
	}
	if !a.allowRate(ip) {
		writeAccountAPIError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), accountAPIWriteTimeout)
	defer cancel()

	var storedHash, accountID string
	query := fmt.Sprintf("SELECT password_hash, account_id FROM %s WHERE account = ?", a.table)
	if err := a.db.QueryRowContext(ctx, query, request.Account).Scan(&storedHash, &accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAccountAPIError(w, http.StatusUnauthorized, "账号不存在或密码错误")
			return
		}
		writeAccountAPIError(w, http.StatusInternalServerError, "登录失败")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(request.Password)); err != nil {
		writeAccountAPIError(w, http.StatusUnauthorized, "账号不存在或密码错误")
		return
	}
	if a.service.options.playerStrict {
		a.service.playerAdmission.mu.Lock()
		defer a.service.playerAdmission.mu.Unlock()
		var disabled bool
		var currentHash string
		if err := a.db.QueryRowContext(ctx, "SELECT a.password_hash, COALESCE(p.disabled,0) FROM "+a.table+" a LEFT JOIN "+a.table+"_player_accounts p ON BINARY p.account_id=BINARY a.account_id WHERE a.account_id=?", accountID).Scan(&currentHash, &disabled); err != nil || disabled || currentHash != storedHash {
			writeAccountAPIError(w, http.StatusForbidden, "该账号暂不可登录，请联系管理员")
			return
		}
		if err := a.service.playerAdmission.available(accountID, request.Device, a.service.options.playerMaxOnline, time.Now()); err != nil {
			writeAccountAPIError(w, http.StatusConflict, err.Error())
			return
		}
	}

	update := fmt.Sprintf("UPDATE %s SET last_login_at = ?, last_login_ip = ? WHERE account = ?", a.table)
	if _, err := a.db.ExecContext(ctx, update, time.Now(), ip, request.Account); err != nil {
		writeAccountAPIError(w, http.StatusInternalServerError, "登录失败")
		return
	}

	// 曾经这里还会把调用方来源 IP 绑到账号，供游戏连接按 IP 认账号。
	// 2026-09-25 现场取证后那条通道已删除（见 client_account_ip.go）：
	// 玩家的启动器 HTTP 登录出口与游戏客户端 TCP 来源 IP 不是同一个地址。
	// 现在登录只做两件事：记录 last_login_at/last_login_ip 作为审计事实，
	// 并签发下面这条启动器令牌 —— 令牌是游戏连接上唯一权威的账号来源。

	// 签发本次登录的令牌。启动器把它交给客户端，服务端就能在游戏连接上精确认账号。
	// 签发失败必须拒绝登录，不能回落到共享账号。
	// DNFBRIDGE_ACCOUNT_API_LOGIN_TOKEN=0 时整体关闭令牌，便于即时回退。
	token := ""
	if a.service.options.accountAPILoginToken {
		issued, err := a.issueLoginToken(ctx, request.Account, accountID, ip)
		if err != nil {
			a.service.logWarn("dnfbridge login token issue failed",
				"account_id", accountID, "client_ip", ip, "error", err)
			writeAccountAPIError(w, http.StatusServiceUnavailable, "登录服务暂不可用，请稍后重试")
			return
		} else {
			token = issued
		}
	}
	if a.service.options.playerStrict {
		if _, err := a.db.ExecContext(ctx, "INSERT INTO "+a.table+"_player_tokens (token, device) VALUES (?, ?)", token, request.Device); err != nil {
			writeAccountAPIError(w, http.StatusServiceUnavailable, "登录服务暂不可用")
			return
		}
		// Previous tokens cannot reopen a different launched client after this login.
		if _, err := a.db.ExecContext(ctx, "DELETE FROM "+a.loginTokenTable()+" WHERE account_id=? AND token<>?", accountID, token); err != nil {
			writeAccountAPIError(w, http.StatusServiceUnavailable, "登录服务暂不可用")
			return
		}
		a.service.playerAdmission.reserve(accountID, request.Device, token, time.Now())
	}

	writeAccountAPIJSON(w, http.StatusOK, accountAPIResponse{
		OK:        true,
		AccountID: accountID,
		Token:     token,
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
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
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
