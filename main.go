package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const version = "0.1.0"

//go:embed web
var webAssets embed.FS

type Server struct {
	cfg      *Config
	store    *Store
	up       *Upstream
	log      *ReqLog
	started  time.Time
	inflight atomic.Int64

	modelsMu  sync.Mutex
	models    []Model
	modelsAt  time.Time
	modelsErr error

	creditsMu sync.Mutex
	credits   map[string]*creditsState

	devices *deviceAttempts
	sigs    *sigCache

	logger *log.Logger
}

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)
	cfg, err := LoadConfig(".")
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	up := NewUpstream(cfg.TraycerServer, cfg.TraycerAuthn, cfg.UpstreamTimeout)
	store, err := OpenStore(cfg, up)
	if err != nil {
		logger.Fatalf("accounts: %v", err)
	}
	srv := &Server{
		cfg:     cfg,
		store:   store,
		up:      up,
		log:     NewReqLog(cfg.RequestLogSize),
		started: time.Now(),
		devices: newDeviceAttempts(),
		sigs:    newSigCache(),
		credits: map[string]*creditsState{},
		logger:  logger,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go store.RefreshLoop(ctx)
	go srv.rotationLoop(ctx)

	handler := srv.routes()
	httpSrv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Bind, itoa(cfg.Port)),
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: completions stream for minutes.
	}

	banner(cfg, len(store.List()))
	errc := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		logger.Fatalf("listen: %v", err)
	case <-ctx.Done():
		logger.Println("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}
}

func banner(cfg *Config, accounts int) {
	base := fmt.Sprintf("http://%s:%d", cfg.Bind, cfg.Port)
	fmt.Printf("\ntraycer2openai %s\n", version)
	fmt.Printf("  dashboard  %s\n", base)
	fmt.Printf("  base url   %s/v1\n", base)
	if cfg.LoginToken == "" {
		fmt.Printf("  api key    (LOGIN_TOKEN unset: dashboard and API are open)\n")
	} else if cfg.APIAuth {
		fmt.Printf("  api key    %s\n", maskToken(cfg.LoginToken))
	} else {
		fmt.Printf("  api key    not required (API_AUTH=false, dashboard still needs the token)\n")
	}
	if !isLoopback(cfg.Bind) {
		if !cfg.APIAuth || cfg.LoginToken == "" {
			fmt.Printf("  warning    bound to %s without auth: anyone who can reach this port can spend your credits\n", cfg.Bind)
		}
	}
	fmt.Printf("  accounts   %s (%d)\n", cfg.AccountsFile, accounts)
	fmt.Printf("  upstream   %s\n\n", cfg.TraycerServer)
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return strings.Repeat("*", len(t))
	}
	return t[:4] + strings.Repeat("*", len(t)-8) + t[len(t)-4:]
}

func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// OpenAI-compatible surface.
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /health", s.handleHealth)

	// Dashboard API.
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/state", s.admin(s.handleState))
	mux.HandleFunc("POST /api/accounts", s.admin(s.handleAddAccount))
	mux.HandleFunc("POST /api/accounts/{id}/active", s.admin(s.handleSetActive))
	mux.HandleFunc("DELETE /api/accounts/{id}", s.admin(s.handleRemoveAccount))
	mux.HandleFunc("POST /api/accounts/{id}/refresh", s.admin(s.handleRefreshAccount))
	mux.HandleFunc("POST /api/models/refresh", s.admin(s.handleRefreshModels))
	mux.HandleFunc("POST /api/auth/device/start", s.admin(s.handleDeviceStart))
	mux.HandleFunc("GET /api/auth/device/status", s.admin(s.handleDeviceStatus))
	mux.HandleFunc("POST /api/auth/device/cancel", s.admin(s.handleDeviceCancel))

	// Embedded dashboard.
	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// Unknown path: hand back the app shell.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/index.html"
			w.Header().Set("Cache-Control", "no-store")
			static.ServeHTTP(w, r2)
			return
		}
		if p == "index.html" {
			w.Header().Set("Cache-Control", "no-store")
		}
		static.ServeHTTP(w, r)
	})
	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Traycer-Account")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- auth helpers

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// clientAuth guards the OpenAI surface. An unset LOGIN_TOKEN leaves it open, which
// is only sane on loopback.
// clientAuth guards the OpenAI surface. Auth is skipped entirely when API_AUTH is
// off, or when no LOGIN_TOKEN was configured at all (open, loopback-only setup).
func (s *Server) clientAuth(r *http.Request) bool {
	if !s.cfg.APIAuth || s.cfg.LoginToken == "" {
		return true
	}
	return tokenEqual(bearer(r), s.cfg.LoginToken)
}

const sessionCookie = "t2o"

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.LoginToken == "" {
			next(w, r)
			return
		}
		ok := tokenEqual(bearer(r), s.cfg.LoginToken)
		if !ok {
			if c, err := r.Cookie(sessionCookie); err == nil && tokenEqual(c.Value, s.cfg.LoginToken) {
				ok = true
			}
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "not signed in"})
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------- openai surface

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !s.clientAuth(r) {
		oaiError(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		oaiError(w, http.StatusBadRequest, "invalid_request_error", "could not read request body")
		return
	}
	var meta struct {
		Model string `json:"model"`
	}
	// Decode the first JSON value rather than requiring the whole body to be
	// clean: a stray trailing byte should not silently downgrade the model used
	// for logging and the response echo.
	if dec := json.NewDecoder(bytes.NewReader(body)); dec.Decode(&meta) != nil || meta.Model == "" {
		_ = json.Unmarshal(body, &meta)
	}
	model := meta.Model
	if model == "" {
		model = s.cfg.DefaultModel
	}

	out, stream, err := ChatToResponses(body)
	if err != nil {
		oaiError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	acct, err := s.store.Pick(r.Header.Get("X-Traycer-Account"))
	if err != nil {
		oaiError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	ctx := r.Context()
	if err := s.store.EnsureToken(ctx, acct); err != nil {
		s.record(acct, model, stream, http.StatusUnauthorized, start, 0, 0, err.Error())
		oaiError(w, http.StatusUnauthorized, "account_unavailable", "account "+acct.Name+": "+err.Error())
		return
	}

	// Gemini models are served on Traycer's native Gemini route, not the OpenAI
	// proxy, and are billed differently. Route them before translating.
	if s.useGemini(ctx, model) {
		s.handleGeminiChat(w, r, start, acct, model, body, stream)
		return
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if !stream {
		callCtx, cancel = context.WithTimeout(ctx, s.cfg.UpstreamTimeout)
		defer cancel()
	}

	resp, err := s.up.Responses(callCtx, acct, out)
	if err != nil {
		s.record(acct, model, stream, http.StatusBadGateway, start, 0, 0, err.Error())
		oaiError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, code := readUpstreamError(resp.Body)
		acct.Status = statusOK
		s.record(acct, model, stream, resp.StatusCode, start, 0, 0, msg)
		oaiError(w, resp.StatusCode, code, msg)
		return
	}

	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		in, outTok, err := StreamResponsesToChat(w, resp.Body, model)
		if err != nil {
			s.record(acct, model, true, resp.StatusCode, start, in, outTok, err.Error())
			return
		}
		s.record(acct, model, true, resp.StatusCode, start, in, outTok, "")
		return
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		s.record(acct, model, false, http.StatusBadGateway, start, 0, 0, err.Error())
		oaiError(w, http.StatusBadGateway, "upstream_error", "could not read upstream response")
		return
	}
	chat, err := ResponsesToChat(raw, model)
	if err != nil {
		s.record(acct, model, false, http.StatusBadGateway, start, 0, 0, err.Error())
		oaiError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	in, outTok := usageOf(raw)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(chat)
	s.record(acct, model, false, http.StatusOK, start, in, outTok, "")
}

// handleResponses is a raw passthrough for clients that already speak the
// Responses API; nothing is translated.
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !s.clientAuth(r) {
		oaiError(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		oaiError(w, http.StatusBadRequest, "invalid_request_error", "could not read request body")
		return
	}
	var meta struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &meta)

	acct, err := s.store.Pick(r.Header.Get("X-Traycer-Account"))
	if err != nil {
		oaiError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if err := s.store.EnsureToken(r.Context(), acct); err != nil {
		oaiError(w, http.StatusUnauthorized, "account_unavailable", err.Error())
		return
	}
	resp, err := s.up.Responses(r.Context(), acct, body)
	if err != nil {
		s.record(acct, meta.Model, meta.Stream, http.StatusBadGateway, start, 0, 0, err.Error())
		oaiError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)

	if strings.Contains(ct, "event-stream") {
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 32<<10)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					break
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if rerr != nil {
				break
			}
		}
		s.record(acct, meta.Model, true, resp.StatusCode, start, 0, 0, "")
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	_, _ = w.Write(raw)
	in, outTok := usageOf(raw)
	s.record(acct, meta.Model, false, resp.StatusCode, start, in, outTok, "")
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.clientAuth(r) {
		oaiError(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
		return
	}
	models, err := s.catalog(r.Context(), false)
	if err != nil {
		oaiError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	type oaiModel struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	now := time.Now().Unix()
	data := make([]oaiModel, 0, len(models))
	for _, m := range models {
		data = append(data, oaiModel{ID: m.ID, Object: "model", Created: now, OwnedBy: m.Provider})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"version":  version,
		"accounts": len(s.store.List()),
		"uptime":   int(time.Since(s.started).Seconds()),
	})
}

// ---------------------------------------------------------------- dashboard api

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected {\"token\":\"...\"}"})
		return
	}
	if s.cfg.LoginToken != "" && !tokenEqual(strings.TrimSpace(in.Token), s.cfg.LoginToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid token"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.cfg.LoginToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	models, _ := s.catalog(r.Context(), false)
	accs := s.store.List()
	active := s.store.ActiveID()

	type accView struct {
		ID               string   `json:"id"`
		Name             string   `json:"name"`
		Email            string   `json:"email"`
		Handle           string   `json:"handle"`
		Plan             string   `json:"plan"`
		Status           string   `json:"status"`
		LastError        string   `json:"lastError"`
		ExpiresInSeconds int      `json:"expiresInSeconds"`
		AddedAt          string   `json:"addedAt"`
		Active           bool     `json:"active"`
		Credits          *Credits `json:"credits,omitempty"`
	}
	// Billing snapshots are cached server-side (CREDITS_CACHE_SECONDS), so the
	// 2 s dashboard poll does not hammer authn.traycer.ai; fetch all accounts
	// concurrently so one slow account cannot stall the whole state response.
	creditOf := make(map[string]*Credits, len(accs))
	var cmu sync.Mutex
	var cwg sync.WaitGroup
	for _, a := range accs {
		cwg.Add(1)
		go func(a *Account) {
			defer cwg.Done()
			c := s.creditsForAccount(r.Context(), a)
			cmu.Lock()
			creditOf[a.ID] = c
			cmu.Unlock()
		}(a)
	}
	cwg.Wait()
	av := make([]accView, 0, len(accs))
	for _, a := range accs {
		st := a.Status
		if st == "" {
			st = statusFor(a)
		}
		if st == statusOK && time.Until(a.ExpiresAt) <= 0 {
			st = statusExpired
		}
		av = append(av, accView{
			ID: a.ID, Name: a.Name, Email: a.Email, Handle: a.Handle, Plan: a.Plan, Status: st,
			LastError: a.LastError, ExpiresInSeconds: a.ExpiresIn(), AddedAt: a.AddedAt.UTC().Format(time.RFC3339),
			Active: a.ID == active, Credits: creditOf[a.ID],
		})
	}
	if models == nil {
		models = []Model{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version,
		"config": map[string]any{
			"bind":          s.cfg.Bind,
			"port":          s.cfg.Port,
			"server":        s.cfg.TraycerServer,
			"accountsFile":  s.cfg.AccountsFile,
			"defaultModel":    s.cfg.DefaultModel,
			"apiAuth":         s.cfg.APIAuth && s.cfg.LoginToken != "",
			"uptimeSeconds":   int(time.Since(s.started).Seconds()),
			"autoRotate":      s.cfg.AutoRotate,
			"rotateThreshold": s.cfg.RotateThreshold,
		},
		"activeId": active,
		"accounts": av,
		"models":   s.modelViews(models),
		"requests": s.log.Snapshot(),
	})
}

// modelsSnapshot returns the currently cached catalog without touching the network.
func (s *Server) modelsSnapshot() []Model {
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	return s.models
}

func (s *Server) modelViews(models []Model) []map[string]any {
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		efforts := m.Efforts
		if efforts == nil {
			efforts = []string{}
		}
		out = append(out, map[string]any{
			"id": m.ID, "name": m.Name, "provider": m.Provider,
			"contextWindow": m.ContextWindow, "maxOutputTokens": m.MaxOutputTokens,
			"supportsImages": m.SupportsImages, "efforts": efforts,
		})
	}
	return out
}

func (s *Server) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name           string `json:"name"`
		AccessToken    string `json:"accessToken"`
		RefreshToken   string `json:"refreshToken"`
		TeamID         string `json:"teamId"`
		CredentialPath string `json:"credentialPath"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	// Accept a pasted credentials.json as well as a bare token.
	if strings.HasPrefix(strings.TrimSpace(in.AccessToken), "{") {
		var cred struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refreshToken"`
			OwnerUserID  string `json:"ownerUserId"`
		}
		if err := json.Unmarshal([]byte(in.AccessToken), &cred); err == nil && cred.Token != "" {
			in.AccessToken = cred.Token
			if in.RefreshToken == "" {
				in.RefreshToken = cred.RefreshToken
			}
			if strings.TrimSpace(in.Name) == "" && cred.OwnerUserID != "" {
				id := cred.OwnerUserID
				if len(id) > 8 {
					id = id[:8]
				}
				in.Name = id
			}
		}
	}
	a, err := s.store.Add(in.Name, in.AccessToken, in.RefreshToken, in.TeamID, in.CredentialPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": a.ID, "name": a.Name, "status": a.Status,
		"expiresInSeconds": a.ExpiresIn(),
	})
}

func (s *Server) handleSetActive(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SetActive(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	if err := s.store.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Remove(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRefreshAccount(w http.ResponseWriter, r *http.Request) {
	a := s.store.ByID(r.PathValue("id"))
	if a == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such account"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.store.Refresh(ctx, a, true); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expiresInSeconds": a.ExpiresIn()})
}

func (s *Server) handleRefreshModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.catalog(r.Context(), true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": s.modelViews(models)})
}

// ---------------------------------------------------------------- catalog cache

func (s *Server) catalog(ctx context.Context, force bool) ([]Model, error) {
	s.modelsMu.Lock()
	if !force && len(s.models) > 0 && time.Since(s.modelsAt) < s.cfg.ModelsCache {
		out := s.models
		s.modelsMu.Unlock()
		return out, nil
	}
	s.modelsMu.Unlock()

	accs := s.store.List()
	var lastErr error
	for _, a := range accs {
		if err := s.store.EnsureToken(ctx, a); err != nil {
			lastErr = err
			continue
		}
		models, err := s.up.Models(ctx, a)
		if err != nil {
			lastErr = err
			continue
		}
		s.modelsMu.Lock()
		s.models, s.modelsAt, s.modelsErr = models, time.Now(), nil
		s.modelsMu.Unlock()
		return models, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no accounts configured")
	}
	s.modelsMu.Lock()
	s.modelsErr = lastErr
	s.modelsMu.Unlock()
	return nil, lastErr
}

// ---------------------------------------------------------------- helpers

func (s *Server) record(a *Account, model string, stream bool, status int, start time.Time, in, out int, errMsg string) {
	e := ReqEntry{
		TS: time.Now().UTC(), Account: a.Name, Model: model, Status: status,
		MS: time.Since(start).Milliseconds(), Stream: stream,
		InTokens: in, OutTokens: out, Error: errMsg,
	}
	s.log.Add(e)
	if s.cfg.LogRequests {
		mark := "ok"
		if status >= 400 || errMsg != "" {
			mark = "!!"
		}
		suffix := ""
		if errMsg != "" {
			suffix = " error=" + truncate(errMsg, 160)
		}
		s.logger.Printf("%s %s %d %dms acct=%s model=%s stream=%t%s",
			mark, "chat", status, e.MS, a.Name, model, stream, suffix)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func oaiError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "traycer_error",
			"code":    code,
		},
	})
}

// readUpstreamError pulls Traycer's error code out of its envelope so clients see
// something actionable (OUT_OF_CREDITS, UNAUTHORIZED) instead of a bare 402.
func readUpstreamError(r io.Reader) (msg, code string) {
	raw, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	var env struct {
		Error any    `json:"error"`
		Code  string `json:"code"`
		Msg   string `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err == nil {
		code = env.Code
		switch v := env.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				msg = m
			}
			if c, ok := v["code"].(string); ok && code == "" {
				code = c
			}
		}
		if msg == "" {
			msg = env.Msg
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if msg == "" {
		msg = "upstream returned an error"
	}
	if code == "" {
		code = "upstream_error"
	}
	return msg, code
}

func usageOf(respBody []byte) (in, out int) {
	var env struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &env); err == nil {
		return env.Usage.InputTokens, env.Usage.OutputTokens
	}
	return 0, 0
}
