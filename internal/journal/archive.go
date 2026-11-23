package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const MinRetention = 3660 * 24 * time.Hour
const maxEventBytes = 90 << 20

// ArchiveBackend keeps cloud transport separate from durable queue semantics.
type ArchiveBackend interface {
	Check(context.Context) error
	CreateOrRead(context.Context, string, []byte) (string, []byte, error)
	List(context.Context, string) ([][]byte, error)
	Close() error
}

// GCS uses the public JSON API and Google's maintained OAuth2 ADC support.
// No custom token, service-account signing, or workload-identity implementation.
type GCS struct {
	Bucket string
	client *http.Client
}

func (g *GCS) clientFor(ctx context.Context) (*http.Client, error) {
	if g.client != nil {
		return g.client, nil
	}
	authCtx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second})
	c, err := google.DefaultClient(authCtx, "https://www.googleapis.com/auth/devstorage.read_write")
	if err != nil {
		return nil, err
	}
	c.Timeout = 120 * time.Second
	g.client = c
	return c, nil
}
func (g *GCS) request(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	client, err := g.clientFor(ctx)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://storage.googleapis.com"+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxEventBytes+1))
	if err != nil {
		return nil, res.StatusCode, err
	}
	if len(data) > maxEventBytes {
		return nil, res.StatusCode, errors.New("GCS response exceeds size limit")
	}
	return data, res.StatusCode, nil
}
func (g *GCS) Check(ctx context.Context) error {
	path := "/storage/v1/b/" + url.PathEscape(g.Bucket) + "?fields=iamConfiguration,retentionPolicy"
	body, status, err := g.request(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("GCS bucket lookup returned HTTP %d", status)
	}
	var b struct {
		IAM struct {
			Uniform struct {
				Enabled bool `json:"enabled"`
			} `json:"uniformBucketLevelAccess"`
			Prevention string `json:"publicAccessPrevention"`
		} `json:"iamConfiguration"`
		Retention struct {
			Period string `json:"retentionPeriod"`
		} `json:"retentionPolicy"`
	}
	if err = json.Unmarshal(body, &b); err != nil {
		return err
	}
	if !b.IAM.Uniform.Enabled {
		return errors.New("GCS requires uniform bucket-level access")
	}
	if b.IAM.Prevention != "enforced" {
		return errors.New("GCS public access prevention must be enforced")
	}
	seconds, err := strconv.ParseInt(b.Retention.Period, 10, 64)
	if err != nil || seconds < int64(MinRetention/time.Second) {
		return errors.New("GCS bucket must retain objects for at least 3660 days")
	}
	return nil
}
func (g *GCS) CreateOrRead(ctx context.Context, key string, body []byte) (string, []byte, error) {
	query := url.Values{"uploadType": {"media"}, "name": {key}, "ifGenerationMatch": {"0"}}
	_, status, err := g.request(ctx, "POST", "/upload/storage/v1/b/"+url.PathEscape(g.Bucket)+"/o?"+query.Encode(), body)
	if err != nil {
		return "", nil, err
	}
	if status != 200 && status != 201 && status != 412 {
		return "", nil, fmt.Errorf("GCS creation returned HTTP %d", status)
	}
	path := "/storage/v1/b/" + url.PathEscape(g.Bucket) + "/o/" + url.PathEscape(key)
	metadata, status, err := g.request(ctx, "GET", path+"?fields=generation", nil)
	if err != nil {
		return "", nil, err
	}
	if status != 200 {
		return "", nil, fmt.Errorf("GCS metadata returned HTTP %d", status)
	}
	var attr struct {
		Generation string `json:"generation"`
	}
	if err = json.Unmarshal(metadata, &attr); err != nil {
		return "", nil, err
	}
	if _, err = strconv.ParseUint(attr.Generation, 10, 64); err != nil {
		return "", nil, errors.New("invalid GCS generation")
	}
	data, err := g.readGeneration(ctx, key, attr.Generation)
	return attr.Generation, data, err
}
func (g *GCS) readGeneration(ctx context.Context, key, generation string) ([]byte, error) {
	query := url.Values{"alt": {"media"}, "generation": {generation}, "ifGenerationMatch": {generation}}
	body, status, err := g.request(ctx, "GET", "/storage/v1/b/"+url.PathEscape(g.Bucket)+"/o/"+url.PathEscape(key)+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("GCS download returned HTTP %d", status)
	}
	return body, nil
}
func (g *GCS) List(ctx context.Context, prefix string) ([][]byte, error) {
	result := make([][]byte, 0)
	page := ""
	seen := map[string]bool{}
	for {
		query := url.Values{"prefix": {prefix}, "fields": {"nextPageToken,items(name,generation)"}, "maxResults": {"1000"}}
		if page != "" {
			query.Set("pageToken", page)
		}
		body, status, err := g.request(ctx, "GET", "/storage/v1/b/"+url.PathEscape(g.Bucket)+"/o?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("GCS list returned HTTP %d", status)
		}
		var listing struct {
			Next  string `json:"nextPageToken"`
			Items []struct {
				Name       string `json:"name"`
				Generation string `json:"generation"`
			} `json:"items"`
		}
		if err = json.Unmarshal(body, &listing); err != nil {
			return nil, err
		}
		for _, item := range listing.Items {
			if !strings.HasPrefix(item.Name, prefix) {
				return nil, errors.New("unexpected GCS object prefix")
			}
			if _, err = strconv.ParseUint(item.Generation, 10, 64); err != nil {
				return nil, errors.New("invalid GCS generation")
			}
			data, err := g.readGeneration(ctx, item.Name, item.Generation)
			if err != nil {
				return nil, err
			}
			result = append(result, data)
		}
		page = listing.Next
		if page == "" {
			break
		}
		if seen[page] {
			return nil, errors.New("repeated GCS page token")
		}
		seen[page] = true
	}
	return result, nil
}
func (g *GCS) Close() error {
	if g.client != nil {
		g.client.CloseIdleConnections()
	}
	return nil
}

type Archive struct {
	Store     *Store
	Backend   ArchiveBackend
	mu        sync.Mutex
	statusMu  sync.RWMutex
	lastError *string
}

func NewArchive(store *Store, bucket string) *Archive {
	a := &Archive{Store: store}
	if bucket != "" {
		a.Backend = &GCS{Bucket: bucket}
	}
	return a
}
func (a *Archive) Configured() bool { return a.Backend != nil }
func (a *Archive) LastError() *string {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.lastError == nil {
		return nil
	}
	s := *a.lastError
	return &s
}
func (a *Archive) setError(message *string) {
	a.statusMu.Lock()
	a.lastError = message
	a.statusMu.Unlock()
}
func (a *Archive) Status() (Status, error) {
	s, err := a.Store.Status()
	s.GCSConfigured = a.Configured()
	s.ArchiveError = a.LastError()
	return s, err
}
func (a *Archive) Sync(ctx context.Context) (Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Backend == nil {
		return Status{}, errors.New("GCS is not configured; set GCS_BUCKET and Application Default Credentials")
	}
	fail := func(message string) (Status, error) {
		a.setError(&message)
		_ = a.Store.archiveFailed(message)
		return Status{}, errors.New(message)
	}
	if err := a.Backend.Check(ctx); err != nil {
		// Only fixed bucket-policy messages are exposed; provider errors may contain secrets.
		switch err.Error() {
		case "GCS requires uniform bucket-level access", "GCS public access prevention must be enforced", "GCS bucket must retain objects for at least 3660 days":
			return fail(err.Error())
		}
		return fail("GCS archive failed; check credentials, bucket permissions and connectivity")
	}
	for {
		e, err := a.Store.NextPending()
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return fail("unable to read the local archive queue")
		}
		body, err := Canonical(e)
		if err != nil {
			return fail("unable to encode an archive event")
		}
		key := fmt.Sprintf("journals/%s/events/%012d-%s.json", e.JournalID, e.Seq, e.Hash)
		generation, remote, err := a.Backend.CreateOrRead(ctx, key, body)
		if err != nil {
			return fail("GCS upload verification failed; event remains pending for retry")
		}
		if !bytes.Equal(body, remote) {
			return fail("remote archive content differs from local event")
		}
		if err = a.Store.markArchived(e.Seq, generation); err != nil {
			return fail("cloud upload succeeded but local receipt could not be saved; retry is safe")
		}
	}
	a.setError(nil)
	return a.Status()
}
func (a *Archive) RestoreEvents(ctx context.Context, id string) ([]Event, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Validate a UUID-shaped identifier before building a cloud prefix.
	if len(id) != 36 {
		return nil, errors.New("a journal UUID is required")
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r == '-') {
			return nil, errors.New("invalid journal UUID")
		}
	}
	if a.Backend == nil {
		return nil, errors.New("GCS is not configured")
	}
	// Recovery intentionally does not depend on retention policy still being correct.
	bodies, err := a.Backend.List(ctx, "journals/"+id+"/events/")
	if err != nil {
		return nil, errors.New("GCS recovery failed; check credentials, permissions and connectivity")
	}
	if len(bodies) == 0 {
		return nil, errors.New("no archive events found")
	}
	events := make([]Event, 0, len(bodies))
	for _, body := range bodies {
		var e Event
		if err = decode(body, &e); err != nil {
			return nil, errors.New("invalid cloud event JSON")
		}
		if e.JournalID != id {
			return nil, errors.New("journal identity mismatch")
		}
		events = append(events, e)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	if _, err = VerifyEvents(events); err != nil {
		return nil, err
	}
	return events, nil
}
func (a *Archive) Run(ctx context.Context) {
	if !a.Configured() {
		return
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		syncCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, _ = a.Sync(syncCtx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (a *Archive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Backend != nil {
		return a.Backend.Close()
	}
	return nil
}
