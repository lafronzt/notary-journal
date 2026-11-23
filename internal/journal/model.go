package journal

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

const Schema = "ny-notary-v1"
const MaxAttachment = 64 << 20
const zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

type Object = map[string]any

type Event struct {
	Seq          int64  `json:"seq"`
	ID           string `json:"id"`
	JournalID    string `json:"journal_id"`
	EntryID      string `json:"entry_id"`
	Kind         string `json:"kind"`
	RecordedAt   string `json:"recorded_at"`
	PreviousHash string `json:"previous_hash"`
	Payload      Object `json:"payload"`
	Hash         string `json:"hash"`
}

type Bundle struct {
	Schema     string  `json:"schema"`
	ExportedAt string  `json:"exported_at,omitempty"`
	Events     []Event `json:"events"`
}

type Integrity struct {
	Events    int    `json:"events"`
	Head      string `json:"head"`
	JournalID string `json:"journal_id"`
}

func decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

// Canonical matches the original Python v1 archive encoding, including literal
// Unicode, sorted object keys, and no HTML or U+2028 escaping.
func Canonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err = decode(raw, &normalized); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	var write func(any) error
	writeString := func(s string) {
		b.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if r < 0x20 {
					fmt.Fprintf(&b, `\u%04x`, r)
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	}
	write = func(v any) error {
		switch x := v.(type) {
		case nil:
			b.WriteString("null")
		case bool:
			b.WriteString(strconv.FormatBool(x))
		case string:
			writeString(x)
		case json.Number:
			b.WriteString(x.String())
		case []any:
			b.WriteByte('[')
			for i, item := range x {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := write(item); err != nil {
					return err
				}
			}
			b.WriteByte(']')
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				writeString(k)
				b.WriteByte(':')
				if err := write(x[k]); err != nil {
					return err
				}
			}
			b.WriteByte('}')
		default:
			return fmt.Errorf("unsupported JSON type %T", v)
		}
		return nil
	}
	if err := write(normalized); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func checksum(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func eventHash(e Event) (string, error) {
	body := Object{"seq": e.Seq, "id": e.ID, "journal_id": e.JournalID, "entry_id": e.EntryID, "kind": e.Kind, "recorded_at": e.RecordedAt, "previous_hash": e.PreviousHash, "payload": e.Payload}
	data, err := Canonical(body)
	if err != nil {
		return "", err
	}
	return checksum(data), nil
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func timestamp() string                       { return time.Now().UTC().Format(time.RFC3339Nano) }
func stringValue(d Object, key string) string { s, _ := d[key].(string); return s }
func flag(d Object, key string) bool          { b, _ := d[key].(bool); return b }
func required(d Object, key string) error {
	s, ok := d[key].(string)
	if !ok || strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s is required", key)
	}
	if len(s) > 20000 {
		return fmt.Errorf("%s is too long", key)
	}
	return nil
}
func integer(value any) (int64, bool) {
	switch n := value.(type) {
	case json.Number:
		i, e := n.Int64()
		return i, e == nil
	case int:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

func Validate(input Object) (Object, error) {
	raw, err := Canonical(input)
	if err != nil {
		return nil, err
	}
	var d Object
	if err = decode(raw, &d); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errors.New("entry must be an object")
	}
	source, mode := stringValue(d, "source"), stringValue(d, "mode")
	if source != "contemporaneous" && source != "paper_import" {
		return nil, errors.New("choose contemporaneous entry or paper import")
	}
	if mode != "in_person" && mode != "electronic" {
		return nil, errors.New("choose an appearance mode")
	}
	for _, key := range []string{"occurred_at", "act_type", "verification", "notary_name", "commission_id"} {
		if err := required(d, key); err != nil {
			return nil, err
		}
		d[key] = strings.TrimSpace(stringValue(d, key))
	}
	var act time.Time
	if source == "paper_import" && flag(d, "time_not_recorded") {
		act, err = time.Parse("2006-01-02", stringValue(d, "occurred_at"))
	} else {
		act, err = time.Parse(time.RFC3339Nano, stringValue(d, "occurred_at"))
	}
	if err != nil || act.After(time.Now()) {
		return nil, errors.New("use a past act timestamp with UTC offset, or a date-only paper import with time marked not recorded")
	}
	if n, ok := integer(d["service_count"]); !ok || n < 1 || n > 10000 {
		return nil, errors.New("service_count must be an integer between 1 and 10000")
	}
	people, ok := d["principals"].([]any)
	if !ok || len(people) < 1 || len(people) > 50 {
		return nil, errors.New("provide between 1 and 50 principals")
	}
	for _, person := range people {
		p, ok := person.(map[string]any)
		if !ok {
			return nil, errors.New("invalid principal")
		}
		for _, k := range []string{"name", "address"} {
			if err := required(p, k); err != nil {
				return nil, err
			}
		}
	}
	switch stringValue(d, "identity_method") {
	case "credential":
		if err := required(d, "credential_type"); err != nil {
			return nil, err
		}
	case "personal_knowledge":
	case "witness":
		witnesses, ok := d["witnesses"].([]any)
		if !ok || len(witnesses) == 0 || len(witnesses) > 50 {
			return nil, errors.New("provide witness names and identity basis (maximum 50)")
		}
		for _, w := range witnesses {
			p, ok := w.(map[string]any)
			if !ok {
				return nil, errors.New("invalid witness")
			}
			for _, key := range []string{"name", "identity_basis"} {
				if err := required(p, key); err != nil {
					return nil, err
				}
			}
		}
	default:
		return nil, errors.New("choose an identity verification method")
	}
	if source == "paper_import" {
		if err := required(d, "source_reference"); err != nil {
			return nil, err
		}
	} else if !flag(d, "contemporaneous_attestation") {
		return nil, errors.New("confirm this record is being made contemporaneously")
	}
	if mode == "electronic" {
		for _, key := range []string{"communication_technology", "certification_authority", "verification_provider", "recording_reference"} {
			if err := required(d, key); err != nil {
				return nil, err
			}
		}
		if !flag(d, "recording_retention_attestation") {
			return nil, errors.New("confirm separate audio/video retention arrangements")
		}
	}
	now := time.Now().UTC()
	until := now.AddDate(10, 0, 0)
	// AddDate normalizes Feb 29 to March 1 in non-leap years; conservatively longer.
	d["retain_until"] = until.Format(time.RFC3339Nano)
	return d, nil
}

func ReadBundle(body []byte) (Bundle, error) {
	var bundle Bundle
	if err := decode(body, &bundle); err != nil {
		return bundle, err
	}
	if bundle.Schema != Schema {
		return bundle, errors.New("unsupported archive schema")
	}
	if bundle.Events == nil {
		return bundle, errors.New("archive must include an events array")
	}
	return bundle, nil
}
