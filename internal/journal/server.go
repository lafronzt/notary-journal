package journal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
)

//go:embed static/*
var assets embed.FS

type Server struct {
	store    *Store
	archive  *Archive
	password [32]byte
	origin   string
	hosts    map[string]bool
	secure   bool
	mu       sync.Mutex
	sessions map[string]time.Time
	failures []time.Time
	defaults Defaults
}

// Defaults pre-fills fields for a single-notary deployment. They are
// conveniences only; every entry stores the values actually submitted.
type Defaults struct {
	NotaryName   string `json:"notary_name"`
	CommissionID string `json:"commission_id"`
}

// SetDefaults configures the notary details suggested on new entries.
func (s *Server) SetDefaults(d Defaults) {
	d.NotaryName, d.CommissionID = strings.TrimSpace(d.NotaryName), strings.TrimSpace(d.CommissionID)
	s.defaults = d
}

func NewServer(store *Store, archive *Archive, password, publicURL, listen string) (*Server, error) {
	if len(password) < 16 {
		return nil, errors.New("NOTARY_PASSWORD must contain at least 16 characters")
	}
	s := &Server{store: store, archive: archive, password: sha256.Sum256([]byte(password)), hosts: map[string]bool{}, sessions: map[string]time.Time{}}
	if publicURL == "" {
		host, port, err := net.SplitHostPort(listen)
		if err != nil {
			return nil, err
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return nil, errors.New("PUBLIC_URL is required when listening beyond loopback")
		}
		publicURL = "http://127.0.0.1:" + port
		s.hosts["localhost:"+port] = true
		s.hosts["127.0.0.1:"+port] = true
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("PUBLIC_URL must be an absolute HTTP(S) origin without a path")
	}
	if u.Scheme == "http" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return nil, errors.New("PUBLIC_URL must use HTTPS for non-loopback deployments")
	}
	s.origin = u.Scheme + "://" + u.Host
	s.hosts[u.Host] = true
	s.secure = u.Scheme == "https"
	return s, nil
}
func (s *Server) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie("notary_session")
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.sessions[cookie.Value]
	if ok && time.Now().Before(expiry) {
		return true
	}
	delete(s.sessions, cookie.Value)
	return false
}
func send(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	send(w, status, Object{"error": message})
}
func (s *Server) cookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "notary_session", Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode})
}
func (s *Server) login(w http.ResponseWriter, d Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	kept := s.failures[:0]
	for _, t := range s.failures {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	s.failures = kept
	if len(s.failures) >= 10 {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "too many attempts; try again in a minute")
		return
	}
	password := sha256.Sum256([]byte(stringValue(d, "password")))
	if subtle.ConstantTimeCompare(password[:], s.password[:]) != 1 {
		s.failures = append(s.failures, now)
		fail(w, 401, "incorrect journal password")
		return
	}
	for id, expiry := range s.sessions {
		if !now.Before(expiry) {
			delete(s.sessions, id)
		}
	}
	if len(s.sessions) >= 100 {
		fail(w, 429, "too many active sessions; restart the service to clear sessions")
		return
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		fail(w, 500, "unable to create a session")
		return
	}
	sid := base64.RawURLEncoding.EncodeToString(token[:])
	s.sessions[sid] = now.Add(8 * time.Hour)
	s.cookie(w, sid, 8*3600)
	send(w, 200, Object{"ok": true})
}
func storageError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "record not found")
		return
	}
	var sqliteError *sqlite.Error
	if errors.As(err, &sqliteError) {
		fail(w, 500, "local database operation failed; existing records are preserved")
		return
	}
	fail(w, 400, err.Error())
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if s.secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	// Probes reveal no journal data and use the pod IP Host, not the public origin.
	if (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") && r.Method == http.MethodGet {
		if r.URL.Path == "/readyz" {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if s.store.Ready(ctx) != nil {
				fail(w, 503, "database unavailable")
				return
			}
		}
		send(w, 200, Object{"ok": true})
		return
	}
	if !s.hosts[r.Host] {
		fail(w, 403, "invalid host")
		return
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/", "/app.js", "/style.css":
			name := strings.TrimPrefix(r.URL.Path, "/")
			if name == "" {
				name = "index.html"
			}
			b, err := assets.ReadFile("static/" + name)
			if err != nil {
				fail(w, 500, "application asset unavailable")
				return
			}
			contentType := mime.TypeByExtension("." + strings.Split(name, ".")[1])
			if name == "app.js" {
				contentType = "text/javascript; charset=utf-8"
			}
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(b)
			return
		case "/api/session":
			send(w, 200, Object{"authenticated": s.authenticated(r)})
			return
		}
		if !s.authenticated(r) {
			fail(w, 401, "please unlock your journal")
			return
		}
		s.get(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(w, 405, "method not allowed")
		return
	}
	if r.Header.Get("Origin") != s.origin {
		fail(w, 403, "same-origin requests are required")
		return
	}
	if r.URL.Path != "/api/login" && !s.authenticated(r) {
		fail(w, 401, "please unlock your journal")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "expected application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 90<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, 413, "request exceeds size limit or could not be read")
		return
	}
	var data Object
	if err = decode(body, &data); err != nil || data == nil {
		fail(w, 400, "expected one JSON object")
		return
	}
	if r.URL.Path == "/api/login" {
		s.login(w, data)
		return
	}
	if r.URL.Path == "/api/logout" {
		cookie, _ := r.Cookie("notary_session")
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
		s.cookie(w, "", -1)
		send(w, 200, Object{"ok": true})
		return
	}
	var result any
	switch r.URL.Path {
	case "/api/entries":
		result, err = s.store.Create(data)
	case "/api/corrections":
		entry, ok := data["entry"].(map[string]any)
		if !ok {
			fail(w, 400, "entry is required")
			return
		}
		result, err = s.store.Correct(stringValue(data, "entry_id"), entry, stringValue(data, "reason"))
	case "/api/attachments":
		content, decodeErr := base64.StdEncoding.Strict().DecodeString(stringValue(data, "data"))
		if decodeErr != nil {
			fail(w, 400, "invalid attachment encoding")
			return
		}
		result, err = s.store.Attach(stringValue(data, "entry_id"), stringValue(data, "name"), stringValue(data, "mime"), content)
	case "/api/sync":
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		result, err = s.archive.Sync(ctx)
		if err != nil {
			fail(w, 503, err.Error())
			return
		}
		send(w, 200, result)
		return
	default:
		fail(w, 404, "not found")
		return
	}
	if err != nil {
		storageError(w, err)
		return
	}
	send(w, 201, result)
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	var result any
	var err error
	path := r.URL.Path
	switch {
	case path == "/api/entries":
		result, err = s.store.Entries()
	case path == "/api/config":
		result = s.defaults
	case path == "/api/status":
		result, err = s.archive.Status()
	case strings.HasPrefix(path, "/api/history/"):
		result, err = s.store.History(strings.TrimPrefix(path, "/api/history/"))
	case strings.HasPrefix(path, "/api/attachment/"):
		seq, parseErr := strconv.ParseInt(strings.TrimPrefix(path, "/api/attachment/"), 10, 64)
		if parseErr != nil {
			fail(w, 400, "invalid attachment identifier")
			return
		}
		payload, readErr := s.store.Attachment(seq)
		if readErr != nil {
			storageError(w, readErr)
			return
		}
		content, decodeErr := base64.StdEncoding.Strict().DecodeString(stringValue(payload, "data"))
		if decodeErr != nil {
			fail(w, 500, "attachment integrity error")
			return
		}
		if checksum(content) != stringValue(payload, "sha256") {
			fail(w, 500, "attachment integrity error")
			return
		}
		w.Header().Set("Content-Type", stringValue(payload, "mime"))
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": downloadName(stringValue(payload, "name"), seq)}))
		_, _ = w.Write(content)
		return
	case path == "/api/export":
		var events []Event
		events, err = s.store.AllEvents()
		if err == nil {
			_, err = VerifyEvents(events)
		}
		if err == nil {
			result = Bundle{Schema: Schema, ExportedAt: timestamp(), Events: events}
			w.Header().Set("Content-Disposition", `attachment; filename="notary-journal.json"`)
		}
	case path == "/api/verify":
		result, err = s.store.Verify()
	default:
		fail(w, 404, "not found")
		return
	}
	if err != nil {
		storageError(w, err)
		return
	}
	send(w, 200, result)
}

// downloadName returns a safe download filename that keeps the original
// extension, falling back to a generic name when the stored name is unusable.
func downloadName(name string, seq int64) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == '"' {
			return -1
		}
		return r
	}, name)
	if name = strings.TrimSpace(name); name == "" || name == "." || name == ".." {
		return fmt.Sprintf("attachment-%d", seq)
	}
	return name
}
