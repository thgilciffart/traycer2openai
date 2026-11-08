package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config is the .env surface. Every field has a working default so the binary runs
// with no config at all.
type Config struct {
	Bind            string
	Port            int
	LoginToken      string
	APIAuth         bool
	AccountsFile    string
	TraycerServer   string
	TraycerAuthn    string
	ModelsCache     time.Duration
	RequestLogSize  int
	LogRequests     bool
	DefaultAccount  string
	DefaultModel    string
	UpstreamTimeout time.Duration
}

func LoadConfig(dir string) (*Config, error) {
	cfg := &Config{
		Bind:            "127.0.0.1",
		Port:            8787,
		LoginToken:      "",
		APIAuth:         true,
		AccountsFile:    "./accounts.json",
		TraycerServer:   "https://server.traycer.ai",
		TraycerAuthn:    "https://authn.traycer.ai",
		ModelsCache:     300 * time.Second,
		RequestLogSize:  200,
		LogRequests:     true,
		DefaultModel:    "gemini-3.8-flash",
		UpstreamTimeout: 600 * time.Second,
	}

	vals := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err == nil {
		parseDotEnv(string(raw), vals)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read .env: %w", err)
	}
	// Real environment wins over .env, so `PORT=9000 ./traycer2openai` works.
	for _, k := range []string{
		"BIND", "PORT", "LOGIN_TOKEN", "API_AUTH", "ACCOUNTS_FILE", "TRAYCER_SERVER", "TRAYCER_AUTHN",
		"MODELS_CACHE_SECONDS", "REQUEST_LOG_SIZE", "LOG_REQUESTS", "DEFAULT_ACCOUNT",
		"DEFAULT_MODEL", "UPSTREAM_TIMEOUT_SECONDS",
	} {
		if v, ok := os.LookupEnv(k); ok {
			vals[k] = v
		}
	}

	setStr(&cfg.Bind, vals["BIND"])
	setInt(&cfg.Port, vals["PORT"])
	setStr(&cfg.LoginToken, vals["LOGIN_TOKEN"])
	setStr(&cfg.AccountsFile, vals["ACCOUNTS_FILE"])
	setStr(&cfg.TraycerServer, vals["TRAYCER_SERVER"])
	setStr(&cfg.TraycerAuthn, vals["TRAYCER_AUTHN"])
	setStr(&cfg.DefaultAccount, vals["DEFAULT_ACCOUNT"])
	setStr(&cfg.DefaultModel, vals["DEFAULT_MODEL"])
	if v, ok := parseInt(vals["MODELS_CACHE_SECONDS"]); ok {
		cfg.ModelsCache = time.Duration(v) * time.Second
	}
	if v, ok := parseInt(vals["REQUEST_LOG_SIZE"]); ok {
		cfg.RequestLogSize = v
	}
	if v, ok := parseInt(vals["UPSTREAM_TIMEOUT_SECONDS"]); ok {
		cfg.UpstreamTimeout = time.Duration(v) * time.Second
	}
	if v, ok := parseBool(vals["LOG_REQUESTS"]); ok {
		cfg.LogRequests = v
	}
	if v, ok := parseBool(vals["API_AUTH"]); ok {
		cfg.APIAuth = v
	}

	cfg.TraycerServer = strings.TrimRight(cfg.TraycerServer, "/")
	cfg.TraycerAuthn = strings.TrimRight(cfg.TraycerAuthn, "/")
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("PORT %d out of range", cfg.Port)
	}
	return cfg, nil
}

func setStr(dst *string, v string) {
	if v = strings.TrimSpace(v); v != "" {
		*dst = v
	}
}

func setInt(dst *int, v string) {
	if n, ok := parseInt(v); ok {
		*dst = n
	}
}

func parseInt(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// parseDotEnv understands KEY=VALUE, `export KEY=VALUE`, # comments and single or
// double quoted values. That is the whole grammar on purpose.
func parseDotEnv(src string, out map[string]string) {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if k != "" {
			out[k] = v
		}
	}
}

// Account is one Traycer sign-in. Only the refresh token is long lived; the access
// token is a 15 minute JWT refreshed proactively by the store.
type Account struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email,omitempty"`
	Plan         string    `json:"plan,omitempty"`
	Token        string    `json:"token"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt"`
	TeamID       string    `json:"teamId,omitempty"`
	// CredentialPath is optional. When set, the account borrows Traycer Desktop's
	// own credentials.json and never refreshes: the desktop host owns that token
	// chain, and two independent refreshers would invalidate each other.
	CredentialPath string    `json:"credentialPath,omitempty"`
	Status         string    `json:"status"`
	LastError      string    `json:"lastError,omitempty"`
	AddedAt        time.Time `json:"addedAt"`
}

func (a *Account) AccountContextHeader() string {
	if a.TeamID != "" {
		b, _ := json.Marshal(map[string]string{"type": "TEAM", "teamId": a.TeamID})
		return string(b)
	}
	return `{"type":"PERSONAL"}`
}

func (a *Account) ExpiresIn() int {
	return int(time.Until(a.ExpiresAt).Seconds())
}

const (
	statusOK      = "ok"
	statusExpired = "expired"
	statusError   = "error"
)

// Store owns accounts.json: the account list, which one is active, and token refresh.
type Store struct {
	path string
	up   *Upstream
	cfg  *Config

	mu       sync.RWMutex
	accounts []*Account
	activeID string

	refreshMu sync.Mutex // serialises refresh across goroutines
}

type storeFile struct {
	ActiveID string     `json:"activeId"`
	Accounts []*Account `json:"accounts"`
}

func OpenStore(cfg *Config, up *Upstream) (*Store, error) {
	s := &Store{path: cfg.AccountsFile, up: up, cfg: cfg}
	raw, err := os.ReadFile(cfg.AccountsFile)
	switch {
	case err == nil:
		var f storeFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", cfg.AccountsFile, err)
		}
		s.accounts = f.Accounts
		s.activeID = f.ActiveID
	case errors.Is(err, os.ErrNotExist):
		// first run
	default:
		return nil, fmt.Errorf("read %s: %w", cfg.AccountsFile, err)
	}
	for _, a := range s.accounts {
		a.Status = statusFor(a)
	}
	if s.pick(cfg.DefaultAccount) == nil {
		if len(s.accounts) > 0 {
			s.activeID = s.accounts[0].ID
		}
	}
	return s, nil
}

func statusFor(a *Account) string {
	if a.LastError != "" {
		return statusError
	}
	if a.CredentialPath != "" {
		if exp, _, err := readCredentialFile(a.CredentialPath); err == nil {
			if time.Until(exp) > 0 {
				return statusOK
			}
			return statusExpired
		}
		return statusError
	}
	if a.Token == "" || time.Until(a.ExpiresAt) <= 0 {
		return statusExpired
	}
	return statusOK
}

// readCredentialFile reads a Traycer Desktop credentials.json. The desktop host
// rewrites it roughly every 10 minutes, so reading it per request is always fresher
// than caching a copy here.
func readCredentialFile(path string) (time.Time, string, error) {
	path = expandHome(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, "", err
	}
	var c struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"acquiredAt"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, "", err
	}
	if c.Token == "" {
		return time.Time{}, "", errors.New("credentials.json has no token")
	}
	exp, ok := jwtExpiry(c.Token)
	if !ok {
		return time.Time{}, "", errors.New("credentials.json token is not a JWT")
	}
	return exp, c.Token, nil
}

// expandHome lets the dashboard accept ~/.traycer/... verbatim.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + strings.TrimPrefix(p, "~")
		}
	}
	return p
}

func (s *Store) Save() error {
	s.mu.RLock()
	f := storeFile{ActiveID: s.activeID, Accounts: s.accounts}
	raw, err := json.MarshalIndent(f, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) List() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, len(s.accounts))
	copy(out, s.accounts)
	return out
}

func (s *Store) ByID(id string) *Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (s *Store) ActiveID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeID
}

func (s *Store) SetActive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.accounts {
		if a.ID == id {
			s.activeID = id
			return nil
		}
	}
	return fmt.Errorf("no account %q", id)
}

// Add registers an account. The token is verified against the live model catalog
// so a bad paste fails here instead of on the first request.
func (s *Store) Add(name, token, refresh, teamID, credPath string) (*Account, error) {
	name = strings.TrimSpace(name)
	token = strings.TrimSpace(token)
	refresh = strings.TrimSpace(refresh)
	credPath = strings.TrimSpace(credPath)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if credPath == "" && token == "" {
		return nil, errors.New("access token is required")
	}

	if credPath != "" {
		// Borrow the desktop app's live credential; the host keeps it warm.
		exp, live, err := readCredentialFile(credPath)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", credPath, err)
		}
		a := &Account{
			ID: newID(), Name: name, Token: live, ExpiresAt: exp,
			CredentialPath: credPath, TeamID: strings.TrimSpace(teamID),
			Status: statusOK, AddedAt: time.Now().UTC(),
		}
		if err := s.up.VerifyToken(context.Background(), a); err != nil {
			return nil, fmt.Errorf("token rejected by Traycer: %w", err)
		}
		s.mu.Lock()
		s.accounts = append(s.accounts, a)
		if s.activeID == "" {
			s.activeID = a.ID
		}
		s.mu.Unlock()
		if err := s.Save(); err != nil {
			return nil, err
		}
		return a, nil
	}
	exp, ok := jwtExpiry(token)
	if !ok {
		exp = time.Now().Add(15 * time.Minute)
	}
	a := &Account{
		ID:           newID(),
		Name:         name,
		Token:        token,
		RefreshToken: refresh,
		ExpiresAt:    exp,
		TeamID:       strings.TrimSpace(teamID),
		Status:       statusOK,
		AddedAt:      time.Now().UTC(),
	}
	if err := s.up.VerifyToken(context.Background(), a); err != nil {
		return nil, fmt.Errorf("token rejected by Traycer: %w", err)
	}
	s.mu.Lock()
	s.accounts = append(s.accounts, a)
	if s.activeID == "" {
		s.activeID = a.ID
	}
	s.mu.Unlock()
	if err := s.Save(); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Store) Remove(id string) error {
	s.mu.Lock()
	idx := -1
	for i, a := range s.accounts {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no account %q", id)
	}
	s.accounts = append(s.accounts[:idx], s.accounts[idx+1:]...)
	if s.activeID == id {
		s.activeID = ""
		if len(s.accounts) > 0 {
			s.activeID = s.accounts[0].ID
		}
	}
	s.mu.Unlock()
	return s.Save()
}

// pick resolves an account by id or name, else the active one, else any healthy one.
func (s *Store) pick(want string) *Account {
	want = strings.TrimSpace(want)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if want != "" {
		for _, a := range s.accounts {
			if a.ID == want || strings.EqualFold(a.Name, want) {
				return a
			}
		}
		return nil
	}
	if s.activeID != "" {
		for _, a := range s.accounts {
			if a.ID == s.activeID {
				return a
			}
		}
	}
	if s.cfg != nil && s.cfg.DefaultAccount != "" {
		for _, a := range s.accounts {
			if strings.EqualFold(a.Name, s.cfg.DefaultAccount) {
				return a
			}
		}
	}
	for _, a := range s.accounts {
		if a.Status == statusOK {
			return a
		}
	}
	if len(s.accounts) > 0 {
		return s.accounts[0]
	}
	return nil
}

func (s *Store) Pick(want string) (*Account, error) {
	a := s.pick(want)
	if a == nil {
		if want != "" {
			return nil, fmt.Errorf("no account named %q", want)
		}
		return nil, errors.New("no accounts configured; add one in the dashboard")
	}
	return a, nil
}

const refreshAhead = 2 * time.Minute

// EnsureToken guarantees a usable access token, refreshing ahead of expiry. The
// refresh endpoint authenticates with the *current* token, so refreshing after it
// has expired is impossible: this must run early, never on 401.
func (s *Store) EnsureToken(ctx context.Context, a *Account) error {
	if a.CredentialPath != "" {
		exp, tok, err := readCredentialFile(a.CredentialPath)
		if err != nil {
			s.mu.Lock()
			a.LastError = "credentials.json unreadable: " + err.Error()
			s.mu.Unlock()
			return err
		}
		s.mu.Lock()
		a.Token, a.ExpiresAt, a.LastError = tok, exp, ""
		a.Status = statusOK
		s.mu.Unlock()
		if time.Until(exp) <= 0 {
			return errors.New("desktop credentials.json holds an expired token; is the Traycer app running?")
		}
		return nil
	}

	s.mu.RLock()
	fresh := a.Token != "" && time.Until(a.ExpiresAt) > refreshAhead
	hasRefresh := a.RefreshToken != ""
	s.mu.RUnlock()
	if fresh {
		return nil
	}
	if !hasRefresh {
		s.markError(a, "access token expired and no refresh token was stored")
		return errors.New("access token expired and no refresh token was stored")
	}
	return s.Refresh(ctx, a, false)
}

// Refresh exchanges the refresh token for a new pair. force skips the freshness check.
func (s *Store) Refresh(ctx context.Context, a *Account, force bool) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	// Re-check under the lock: a second caller that queued up behind an in-flight
	// refresh must see the new token instead of starting another one.
	if !force {
		s.mu.RLock()
		fresh := a.Token != "" && time.Until(a.ExpiresAt) > refreshAhead
		s.mu.RUnlock()
		if fresh {
			return nil
		}
	}

	token, refresh, err := s.up.Refresh(ctx, a)
	if err != nil {
		var authErr *AuthError
		switch {
		case errors.As(err, &authErr):
			// Needs a human: sign in again in the desktop app and re-add.
			s.markError(a, "refresh refused ("+authErr.Reason+"): sign in again in Traycer")
		case IsRetryable(err):
			s.mu.Lock()
			a.LastError = "refresh failed, retrying: " + err.Error()
			s.mu.Unlock()
		default:
			s.markError(a, "refresh failed: "+err.Error())
		}
		_ = s.Save()
		return err
	}

	exp, ok := jwtExpiry(token)
	if !ok {
		exp = time.Now().Add(15 * time.Minute)
	}
	s.mu.Lock()
	a.Token = token
	if refresh != "" {
		a.RefreshToken = refresh
	}
	a.ExpiresAt = exp
	a.Status = statusOK
	a.LastError = ""
	s.mu.Unlock()
	return s.Save()
}

func (s *Store) markError(a *Account, msg string) {
	s.mu.Lock()
	a.LastError = msg
	a.Status = statusError
	s.mu.Unlock()
}

// RefreshLoop keeps every account's token alive for as long as the proxy runs.
func (s *Store) RefreshLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, a := range s.List() {
			if a.CredentialPath != "" {
				continue // the desktop host refreshes those
			}
			if a.RefreshToken == "" || time.Until(a.ExpiresAt) > 10*time.Minute {
				continue
			}
			if a.Status == statusError && a.LastError != "" && !strings.Contains(a.LastError, "retrying") {
				continue // burned account: stop hammering until the user re-adds it
			}
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := s.Refresh(rctx, a, false)
			cancel()
			if err != nil {
				continue
			}
		}
	}
}

func jwtExpiry(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// ReqLog is the dashboard's request table: a bounded ring, newest first.
type ReqLog struct {
	mu   sync.Mutex
	max  int
	ring []ReqEntry
	next int
	full bool
	n    int
}

type ReqEntry struct {
	TS        time.Time `json:"ts"`
	Account   string    `json:"account"`
	Model     string    `json:"model"`
	Status    int       `json:"status"`
	MS        int64     `json:"ms"`
	Stream    bool      `json:"stream"`
	InTokens  int       `json:"inTokens"`
	OutTokens int       `json:"outTokens"`
	Error     string    `json:"error,omitempty"`
}

func NewReqLog(max int) *ReqLog {
	if max <= 0 {
		return &ReqLog{max: 0}
	}
	return &ReqLog{max: max, ring: make([]ReqEntry, max)}
}

func (l *ReqLog) Add(e ReqEntry) {
	if l == nil || l.max == 0 {
		return
	}
	l.mu.Lock()
	l.ring[l.next] = e
	l.next = (l.next + 1) % l.max
	l.n++
	if l.n > l.max {
		l.n = l.max
	}
	l.mu.Unlock()
}

func (l *ReqLog) Snapshot() []ReqEntry {
	if l == nil || l.max == 0 {
		return []ReqEntry{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ReqEntry, 0, l.n)
	for i := 1; i <= l.n; i++ {
		idx := (l.next - i + l.max*2) % l.max
		out = append(out, l.ring[idx])
	}
	return out
}
