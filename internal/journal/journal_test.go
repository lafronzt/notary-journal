package journal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "journal.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func example() Object {
	return Object{"source": "contemporaneous", "mode": "in_person", "occurred_at": "2020-01-15T14:30:00-05:00", "act_type": "Acknowledgment", "service_count": 1, "principals": []any{Object{"name": "Fictional Person", "address": "123 Example St, Albany, NY"}}, "notary_name": "Fictional Notary", "commission_id": "TEST-ONLY", "identity_method": "credential", "credential_type": "Driver license", "verification": "Compared credential with principal; fictional test only.", "contemporaneous_attestation": true}
}
func create(t *testing.T, s *Store) Event {
	t.Helper()
	e, err := s.Create(example())
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func requirePending(t *testing.T, s *Store, n int) {
	t.Helper()
	st, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Pending != n {
		t.Fatalf("pending=%d, want %d", st.Pending, n)
	}
}

func TestPythonArchiveCompatibilityAndExistingSchema(t *testing.T) {
	raw, err := os.ReadFile("testdata/python-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := ReadBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyEvents(bundle.Events); err != nil {
		t.Fatalf("Python canonical encoding is incompatible: %v", err)
	}
	s := testStore(t)
	if _, err = s.Restore(bundle.Events); err != nil {
		t.Fatal(err)
	}
	events, err := s.AllEvents()
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range events {
		want, _ := Canonical(bundle.Events[i])
		got, _ := Canonical(event)
		if !bytes.Equal(want, got) {
			t.Fatalf("restored Python event %d changed", i)
		}
	}
	list, err := s.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].AttachmentCount != 1 || list[0].Corrections != 1 {
		t.Fatalf("unexpected legacy projection: %+v", list)
	}
	if !strings.Contains(stringValue(list[0].Data, "notes"), "Correction") {
		t.Fatal("latest view not restored")
	}
	// Reopening the same schema preserves identity and can append a new Go event.
	id, err := s.JournalID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(sPath(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	gotID, _ := second.JournalID()
	if gotID != id {
		t.Fatal("opening existing database changed its identity")
	}
	create(t, second)
	if result, err := second.Verify(); err != nil || result.Events != 4 {
		t.Fatalf("append to legacy journal: %+v %v", result, err)
	}
}
func sPath(t *testing.T, s *Store) string {
	t.Helper()
	var seq int
	var name, path string
	if err := s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImmutableCorrectionsAndCompleteRestore(t *testing.T) {
	s := testStore(t)
	original := create(t, s)
	for _, query := range []string{"UPDATE events SET hash=hash", "DELETE FROM events"} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatal("event mutation accepted")
		}
	}
	correction := example()
	correction["notes"] = "Corrected fictional transcription"
	if _, err := s.Correct(original.EntryID, correction, "Transcription correction"); err != nil {
		t.Fatal(err)
	}
	content := []byte("%PDF-1.4\nFictional source")
	if _, err := s.Attach(original.EntryID, "../source.pdf", "application/pdf", content); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(original.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || stringValue(history[0].Payload, "notes") != "" {
		t.Fatal("original changed")
	}
	if _, ok := history[2].Payload["data"]; ok {
		t.Fatal("history leaked inline attachment content")
	}
	if stringValue(history[2].Payload, "name") != "source.pdf" {
		t.Fatal("basename not sanitized")
	}
	events, _ := s.AllEvents()
	recovered := testStore(t)
	if _, err = recovered.Restore(events); err != nil {
		t.Fatal(err)
	}
	if _, err = recovered.Restore(events); err == nil {
		t.Fatal("nonempty restore allowed")
	}
	recoveredEvents, _ := recovered.AllEvents()
	a, _ := Canonical(events)
	b, _ := Canonical(recoveredEvents)
	if !bytes.Equal(a, b) {
		t.Fatal("roundtrip differs")
	}
	requirePending(t, recovered, 3)
	events[0].Payload["notary_name"] = "Tampered"
	if _, err = VerifyEvents(events); err == nil {
		t.Fatal("tampered record accepted")
	}
	empty := testStore(t)
	if _, err = empty.Restore(events); err == nil {
		t.Fatal("invalid restore accepted")
	}
	requirePending(t, empty, 0)
}
func TestConcurrentAppends(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for range 24 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Create(example()); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Verify()
	if err != nil || result.Events != 24 {
		t.Fatalf("%+v %v", result, err)
	}
	requirePending(t, s, 24)
}
func TestValidationAndUnknownHistoricalTime(t *testing.T) {
	s := testStore(t)
	changes := []Object{{"principals": []any{}}, {"service_count": true}, {"service_count": 1.5}, {"occurred_at": "2020-01-01T10:00"}, {"source": "unknown"}, {"identity_method": "witness", "witnesses": []any{}}, {"contemporaneous_attestation": false}, {"mode": "electronic"}}
	for _, change := range changes {
		d := example()
		for k, v := range change {
			d[k] = v
		}
		if _, err := s.Create(d); err == nil {
			t.Fatalf("invalid entry accepted: %v", change)
		}
	}
	requirePending(t, s, 0)
	d := example()
	d["source"] = "paper_import"
	d["source_reference"] = "Fictional book 1 p2"
	d["time_not_recorded"] = true
	d["occurred_at"] = "2015-02-02"
	e, err := s.Create(d)
	if err != nil {
		t.Fatal(err)
	}
	if stringValue(e.Payload, "occurred_at") != "2015-02-02" {
		t.Fatal("invented act time")
	}
	if stringValue(e.Payload, "retain_until") < "2036" {
		t.Fatal("historical import shortened retention")
	}
	electronic := example()
	electronic["mode"] = "electronic"
	for _, key := range []string{"communication_technology", "certification_authority", "verification_provider", "recording_reference"} {
		electronic[key] = "Fictional platform / reference"
	}
	electronic["recording_retention_attestation"] = true
	if _, err = s.Create(electronic); err != nil {
		t.Fatal(err)
	}
}

type fakeBackend struct {
	objects  map[string][]byte
	fail     bool
	mismatch bool
	checkErr error
}

func (f *fakeBackend) Check(context.Context) error { return f.checkErr }
func (f *fakeBackend) CreateOrRead(_ context.Context, key string, body []byte) (string, []byte, error) {
	if f.fail {
		return "", nil, errors.New("private provider details must not leak")
	}
	if f.mismatch {
		return "1", []byte("mismatch"), nil
	}
	if _, exists := f.objects[key]; !exists {
		f.objects[key] = append([]byte(nil), body...)
	}
	return "1", f.objects[key], nil
}
func (f *fakeBackend) List(_ context.Context, _ string) ([][]byte, error) {
	out := make([][]byte, 0)
	for _, body := range f.objects {
		out = append(out, body)
	}
	return out, nil
}
func (f *fakeBackend) Close() error { return nil }
func TestArchiveFailureRetryAndRecovery(t *testing.T) {
	s := testStore(t)
	e := create(t, s)
	if _, err := s.Attach(e.EntryID, "scan.pdf", "application/pdf", []byte("fictional scan")); err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{objects: map[string][]byte{}, fail: true}
	a := &Archive{Store: s, Backend: backend}
	if _, err := a.Sync(context.Background()); err == nil || strings.Contains(err.Error(), "private provider") {
		t.Fatal("unsanitized or missing archive error")
	}
	requirePending(t, s, 2)
	backend.fail = false
	backend.mismatch = true
	if _, err := a.Sync(context.Background()); err == nil {
		t.Fatal("mismatch accepted")
	}
	requirePending(t, s, 2)
	backend.mismatch = false
	if _, err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	requirePending(t, s, 0)
	list, _ := s.Entries()
	if !list[0].Archived {
		t.Fatal("entry not marked archived")
	}
	// Simulate a crash after cloud upload but before the local receipt committed.
	if _, err := s.db.Exec("UPDATE outbox SET archived_at=NULL,generation=NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(backend.objects) != 2 {
		t.Fatal("retry created duplicates")
	}
	requirePending(t, s, 0)
	id, _ := s.JournalID()
	events, err := a.RestoreEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	recovered := testStore(t)
	if _, err = recovered.Restore(events); err != nil {
		t.Fatal(err)
	}
	if check, err := recovered.Verify(); err != nil || check.Events != 2 {
		t.Fatalf("recovery: %+v %v", check, err)
	}
	if _, err = s.Correct(e.EntryID, example(), "Fictional correction"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Entries()
	if list[0].Archived {
		t.Fatal("new correction was incorrectly archived")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestGCSConditionalCreationAndPinnedReadback(t *testing.T) {
	for _, creationStatus := range []int{200, 412} {
		t.Run(fmt.Sprint(creationStatus), func(t *testing.T) {
			calls := 0
			body := []byte(`{"fictional":"event"}`)
			g := &GCS{Bucket: "fictional-bucket", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				q := r.URL.Query()
				if r.URL.Scheme != "https" || r.URL.Host != "storage.googleapis.com" {
					t.Fatal("incorrect endpoint")
				}
				switch calls {
				case 1:
					if r.Method != "POST" || q.Get("ifGenerationMatch") != "0" || q.Get("name") != "journals/test/events/1.json" {
						t.Fatalf("not a create-only upload: %s", r.URL)
					}
					got, _ := io.ReadAll(r.Body)
					if !bytes.Equal(got, body) {
						t.Fatal("upload changed")
					}
					return response(creationStatus, `{}`), nil
				case 2:
					return response(200, `{"generation":"123"}`), nil
				case 3:
					if q.Get("generation") != "123" || q.Get("ifGenerationMatch") != "123" || q.Get("alt") != "media" {
						t.Fatal("read not pinned")
					}
					return response(200, string(body)), nil
				default:
					t.Fatal("unexpected request")
					return nil, errors.New("unexpected")
				}
			})}}
			generation, got, err := g.CreateOrRead(context.Background(), "journals/test/events/1.json", body)
			if err != nil || generation != "123" || !bytes.Equal(got, body) || calls != 3 {
				t.Fatalf("%s %s %v %d", generation, got, err, calls)
			}
		})
	}
}
func TestGCSBucketPolicyAndPagination(t *testing.T) {
	policies := []struct {
		body  string
		valid bool
	}{
		{`{"iamConfiguration":{"uniformBucketLevelAccess":{"enabled":true},"publicAccessPrevention":"enforced"},"retentionPolicy":{"retentionPeriod":"316224000"}}`, true},
		{`{"iamConfiguration":{"uniformBucketLevelAccess":{"enabled":false},"publicAccessPrevention":"enforced"},"retentionPolicy":{"retentionPeriod":"316224000"}}`, false},
		{`{"iamConfiguration":{"uniformBucketLevelAccess":{"enabled":true},"publicAccessPrevention":"inherited"},"retentionPolicy":{"retentionPeriod":"316224000"}}`, false},
		{`{"iamConfiguration":{"uniformBucketLevelAccess":{"enabled":true},"publicAccessPrevention":"enforced"},"retentionPolicy":{"retentionPeriod":"100"}}`, false},
	}
	for _, policy := range policies {
		g := &GCS{Bucket: "test", client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, policy.body), nil })}}
		err := g.Check(context.Background())
		if (err == nil) != policy.valid {
			t.Fatalf("policy result: %v", err)
		}
	}
	pages := 0
	g := &GCS{Bucket: "test", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("alt") == "media" {
			if r.URL.Query().Get("generation") == "" {
				t.Fatal("unpinned recovery")
			}
			return response(200, `{}`), nil
		}
		pages++
		if pages == 1 {
			return response(200, `{"nextPageToken":"page 2","items":[{"name":"journals/test/1","generation":"1"}]}`), nil
		}
		if r.URL.Query().Get("pageToken") != "page 2" {
			t.Fatal("pagination token not preserved")
		}
		return response(200, `{"items":[{"name":"journals/test/2","generation":"2"}]}`), nil
	})}}
	bodies, err := g.List(context.Background(), "journals/test/")
	if err != nil || len(bodies) != 2 {
		t.Fatalf("pagination: %d %v", len(bodies), err)
	}
}

func testServer(t *testing.T, publicURL string) (*Server, *Store) {
	t.Helper()
	s := testStore(t)
	a := NewArchive(s, "")
	app, err := NewServer(s, a, "fictional-password-for-tests", publicURL, "127.0.0.1:8087")
	if err != nil {
		t.Fatal(err)
	}
	return app, s
}
func request(t *testing.T, s *Server, method, path string, data any, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, s.origin+path, body)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if data != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	w := request(t, s, "POST", "/api/login", Object{"password": "fictional-password-for-tests"}, nil, s.origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("session cookie missing")
	}
	return cookies[0]
}
func TestHTTPAuthenticationCSRFAndEndToEndWorkflow(t *testing.T) {
	app, s := testServer(t, "")
	for _, path := range []string{"/api/entries", "/api/export", "/api/attachment/1"} {
		if w := request(t, app, "GET", path, nil, nil, ""); w.Code != 401 {
			t.Fatalf("unprotected: %s", path)
		}
	}
	if w := request(t, app, "POST", "/api/login", Object{"password": "fictional-password-for-tests"}, nil, ""); w.Code != 403 {
		t.Fatal("missing origin accepted")
	}
	cookie := login(t, app)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure {
		t.Fatal("wrong loopback cookie flags")
	}
	if w := request(t, app, "POST", "/api/entries", example(), cookie, "https://evil.example"); w.Code != 403 {
		t.Fatal("CSRF accepted")
	}
	w := request(t, app, "POST", "/api/entries", example(), cookie, app.origin)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var e Event
	if err := decode(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	w = request(t, app, "POST", "/api/attachments", Object{"entry_id": e.EntryID, "name": "scan.pdf", "mime": "application/pdf", "data": base64.StdEncoding.EncodeToString([]byte("test scan"))}, cookie, app.origin)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	w = request(t, app, "GET", "/api/attachment/2", nil, cookie, "")
	if w.Code != 200 || w.Body.String() != "test scan" {
		t.Fatal("file download failed")
	}
	w = request(t, app, "GET", "/api/export", nil, cookie, "")
	bundle, err := ReadBundle(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	restored := testStore(t)
	if _, err = restored.Restore(bundle.Events); err != nil {
		t.Fatal(err)
	}
	requirePending(t, restored, 2)
	if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("security headers missing")
	}
	request(t, app, "POST", "/api/logout", Object{}, cookie, app.origin)
	if w := request(t, app, "GET", "/api/entries", nil, cookie, ""); w.Code != 401 {
		t.Fatal("session still active after logout")
	}
	requirePending(t, s, 2)
}
func TestHTTPSHostValidationProbesAndRateLimit(t *testing.T) {
	app, _ := testServer(t, "https://journal.example.com")
	cookie := login(t, app)
	if !cookie.Secure {
		t.Fatal("HTTPS session lacks secure cookie")
	}
	r := httptest.NewRequest("GET", "http://pod.internal:8087/readyz", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("probe rejected pod host")
	}
	r = httptest.NewRequest("GET", "https://evil.example/api/session", nil)
	r.Header.Set("X-Forwarded-Host", "journal.example.com")
	w = httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted forwarded host accepted")
	}
	for range 10 {
		if w := request(t, app, "POST", "/api/login", Object{"password": "bad"}, nil, app.origin); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := request(t, app, "POST", "/api/login", Object{"password": "fictional-password-for-tests"}, nil, app.origin); w.Code != 429 {
		t.Fatal("rate limit absent")
	}
	if _, err := NewServer(app.store, app.archive, "fictional-password-for-tests", "http://journal.example.com", "0.0.0.0:8087"); err == nil {
		t.Fatal("remote plaintext origin accepted")
	}
	if _, err := NewServer(app.store, app.archive, "fictional-password-for-tests", "", "0.0.0.0:8087"); err == nil {
		t.Fatal("network listener without public origin accepted")
	}
}

func TestNotaryDefaultsEndpoint(t *testing.T) {
	app, _ := testServer(t, "")
	if w := request(t, app, "GET", "/api/config", nil, nil, ""); w.Code != 401 {
		t.Fatal("config exposed without a session")
	}
	app.SetDefaults(Defaults{NotaryName: "  Alex Rivera ", CommissionID: "01XX0000000"})
	w := request(t, app, "GET", "/api/config", nil, login(t, app), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"notary_name":"Alex Rivera"`) || !strings.Contains(w.Body.String(), `"commission_id":"01XX0000000"`) {
		t.Fatalf("unexpected config: %d %s", w.Code, w.Body.String())
	}
}
