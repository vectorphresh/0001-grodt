package state

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/vectorphresh/0001-grodt/internal/validation"
)

type Options struct {
	// AllowHTTP explicitly enables module-requested HTTP. Disabled by default.
	AllowHTTP bool
	RunID     string
	StartedAt time.Time
	Now       func() time.Time
}
type Store struct {
	snapshot      Snapshot
	journal       []Entry
	definitions   []Definition
	validator     validation.Validator
	now           func() time.Time
	taskSequence  uint64
	closed        bool
	allowHTTP     bool
	hostExhausted bool
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// New validates every default before publishing any state. On success the store
// owns module lifetimes; on failure ownership stays with the caller.
func New(ctx context.Context, definitions []Definition, options Options) (*Store, error) {
	if ctx == nil {
		return nil, errors.New("state context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.RunID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, errors.New("cannot create run identity")
		}
		options.RunID = hex.EncodeToString(id[:])
	}
	if !namePattern.MatchString(options.RunID) {
		return nil, errors.New("invalid run identity")
	}
	if options.StartedAt.IsZero() {
		options.StartedAt = options.Now()
	}
	s := &Store{allowHTTP: options.AllowHTTP, validator: validation.New(), now: options.Now, journal: []Entry{}, snapshot: Snapshot{
		Intrinsic: Intrinsic{RunID: options.RunID, StartedAt: options.StartedAt.UTC()},
		Tasks:     TaskState{Records: map[string]Task{}, Stack: []string{}}, Knowledge: map[string]Partition{},
	}}
	for _, d := range definitions {
		if !namePattern.MatchString(d.Name) || d.Module == nil {
			return nil, errors.New("invalid partition definition")
		}
		if _, exists := s.snapshot.Knowledge[d.Name]; exists {
			return nil, errors.New("duplicate partition definition")
		}
		if len(d.Initial) > MaxValueBytes {
			return nil, errors.New("partition default exceeds limit")
		}
		result, err := s.validator.Validate(ctx, d.Schema, d.Initial)
		if err != nil {
			return nil, fmt.Errorf("partition %q schema: %w", d.Name, err)
		}
		if !result.Valid {
			return nil, fmt.Errorf("partition %q default violates schema", d.Name)
		}
		d.Schema = bytes.Clone(d.Schema)
		d.Initial = bytes.Clone(d.Initial)
		s.definitions = append(s.definitions, d)
		s.snapshot.Knowledge[d.Name] = Partition{Value: bytes.Clone(d.Initial)}
	}
	if err := s.validateTasks(ctx, s.snapshot.Tasks); err != nil {
		return nil, err
	}
	s.appendEntry(Entry{Kind: "initialized", Data: encode(s.snapshot)})
	return s, nil
}
func encode(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("state internal serialization invariant")
	}
	return b
}
func (s *Store) Snapshot() Snapshot {
	var out Snapshot
	_ = json.Unmarshal(encode(s.snapshot), &out)
	return out
}
func (s *Store) JSON() json.RawMessage { return encode(s.snapshot) }
func (s *Store) Journal() []Entry {
	var out []Entry
	_ = json.Unmarshal(encode(s.journal), &out)
	return out
}
func (s *Store) appendEntry(entry Entry) {
	s.snapshot.Intrinsic.JournalSequence++
	entry.Sequence = s.snapshot.Intrinsic.JournalSequence
	entry.Time = s.now().UTC()
	s.journal = append(s.journal, entry)
}
func (s *Store) check(ctx context.Context) error {
	if ctx == nil {
		return errors.New("state context is required")
	}
	if s.closed {
		return errors.New("state is closed")
	}
	return ctx.Err()
}
func (s *Store) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	for _, d := range s.definitions {
		if err := d.Module.Close(ctx); err != nil {
			errs = append(errs, errors.New("state module close failed"))
		}
	}
	return errors.Join(errs...)
}

// Admit accepts host-authored provenance only. Callers must finish all structural
// and downstream acceptance checks before admitting a structured result. Every
// module receives this event before accepted host work executes. Result events
// use the same broadcast path; Admit returns after all resulting work finishes.
func (s *Store) Admit(ctx context.Context, source Source, payload json.RawMessage) (Event, error) {
	e, err := s.admit(ctx, source, payload, nil)
	if err == nil {
		err = s.drainHostWork(ctx)
	}
	return e, err
}

func (s *Store) admit(ctx context.Context, source Source, payload json.RawMessage, correlation *Correlation) (Event, error) {
	if err := s.check(ctx); err != nil {
		return Event{}, err
	}
	if s.hostExhausted {
		return Event{}, ErrHostBudget
	}
	if source.ID == "" || (source.Kind != "user" && source.Kind != "llm" && source.Kind != "runtime" && source.Kind != "http" && source.Kind != "mcp") {
		return Event{}, errors.New("invalid event source")
	}
	if !json.Valid(payload) || len(payload) > MaxValueBytes {
		return Event{}, errors.New("invalid or oversized event payload")
	}
	s.snapshot.Intrinsic.EventSequence++
	e := Event{ID: fmt.Sprintf("%s/event/%d", s.snapshot.Intrinsic.RunID, s.snapshot.Intrinsic.EventSequence), Sequence: s.snapshot.Intrinsic.EventSequence, Time: s.now().UTC(), Source: source, Payload: bytes.Clone(payload)}
	if active, ok := s.Active(); ok {
		e.TaskID = active.ID
	}
	e.Correlation = correlation
	s.appendEntry(Entry{Kind: "event", EventID: e.ID, Data: encode(e)})
	for _, d := range s.definitions {
		if err := ctx.Err(); err != nil {
			return e, err
		}
		p := s.snapshot.Knowledge[d.Name]
		privateEvent := e
		privateEvent.Payload = bytes.Clone(e.Payload)
		if e.Correlation != nil {
			c := *e.Correlation
			privateEvent.Correlation = &c
		}
		raw, err := d.Module.Process(ctx, bytes.Clone(p.Value), privateEvent)
		if err != nil {
			code := "execution_failed"
			var runtimeErr *RuntimeError
			if errors.As(err, &runtimeErr) {
				switch runtimeErr.Code {
				case "trap", "timeout", "resource_limit", "malformed_response", "abi_mismatch", "instantiate":
					code = runtimeErr.Code
				}
			}
			s.fail(d.Name, e, "runtime_error", code)
			if ctx.Err() != nil {
				return e, ctx.Err()
			}
			continue
		}
		if ctx.Err() != nil {
			s.fail(d.Name, e, "runtime_error", "cancelled")
			return e, ctx.Err()
		}
		r, err := decodeResult(raw)
		if err != nil {
			s.fail(d.Name, e, "runtime_error", "malformed_response")
			continue
		}
		if err := s.checkRequests(d.Name, e, r.Requests); err != nil {
			s.fail(d.Name, e, "runtime_error", "invalid_host_request")
			continue
		}
		switch r.Status {
		case "ignored":
			s.appendEntry(Entry{Kind: "ignored", EventID: e.ID, Partition: d.Name, Data: json.RawMessage(`{}`)})
		case "error":
			s.fail(d.Name, e, "module_error", "processing_failed")
		case "processed", "mutation":
			if r.Status == "mutation" {
				candidate, err := apply(p.Value, r)
				if err != nil {
					s.fail(d.Name, e, "mutation_rejected", "invalid_mutation")
					continue
				}
				checked, err := s.validator.Validate(ctx, d.Schema, candidate)
				if err != nil || !checked.Valid {
					s.fail(d.Name, e, "mutation_rejected", "invalid_partition")
					if ctx.Err() != nil {
						return e, ctx.Err()
					}
					continue
				}
				p.Value = candidate
				p.Metadata.Version++
				p.Metadata.LastUpdateSequence = e.Sequence
				t := e.Time
				p.Metadata.LastUpdateAt = &t
			}
			p.Metadata.LastProcessedSequence = e.Sequence
			p.Metadata.Stale = false
			p.Metadata.ConsecutiveFailures = 0
			p.Metadata.LastError = nil
			s.snapshot.Knowledge[d.Name] = p
			s.appendEntry(Entry{Kind: r.Status, EventID: e.ID, Partition: d.Name, Data: encode(struct {
				Result   json.RawMessage `json:"result"`
				Metadata Metadata        `json:"metadata"`
			}{raw, p.Metadata})})
		}
		if err := s.queueRequests(ctx, d.Name, e, r.Requests); err != nil {
			return e, err
		}
	}
	return e, nil
}
func (s *Store) fail(name string, e Event, kind, code string) {
	p := s.snapshot.Knowledge[name]
	p.Metadata.Stale = true
	p.Metadata.ConsecutiveFailures++
	p.Metadata.LastError = &Failure{Kind: kind, Code: code, EventID: e.ID}
	s.snapshot.Knowledge[name] = p
	s.appendEntry(Entry{Kind: "partition_failure", EventID: e.ID, Partition: name, Data: encode(p.Metadata)})
}

// Diagnostic records a host-selected outcome without broadcasting a new event.
func (s *Store) Diagnostic(code string) {
	if code != "operation_cancelled" {
		code = "operation_failed"
	}
	s.appendEntry(Entry{Kind: "runtime_failure", Data: encode(map[string]string{"code": code})})
}
