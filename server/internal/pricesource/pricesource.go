// Package pricesource implements the controlled price-source boundary: fetch,
// validate, normalize, and turn a versioned dataset into reviewable quotes.
// There is deliberately no network path and no administrator-supplied URL.
package pricesource

import (
	"embed"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	BuiltinID   = "builtin"
	BuiltinName = "内置价目表"

	MatchExplicit      = "explicit"
	MatchUpstreamExact = "upstream_exact"
	MatchModelExact    = "model_exact"
	MatchNormalized    = "normalized"
	MatchNone          = "none"

	StatusSafe        = "safe"
	StatusNeedsReview = "needs_review"
	StatusConflict    = "conflict"
	StatusMissing     = "missing"
	StatusIgnored     = "ignored"
)

// ErrSourceNotFound is returned for a source ID that the controlled adapter
// does not expose.
var ErrSourceNotFound = errors.New("price source not found")

//go:embed datasets/builtin.json
var datasets embed.FS

// Billing is one normalized price set. nil means unknown; 0 means free.
type Billing struct {
	Input  *float64 `json:"input_usd_per_mtok"`
	Cached *float64 `json:"cached_input_usd_per_mtok"`
	Output *float64 `json:"output_usd_per_mtok"`
}

// Entry is one source-model row in a versioned dataset.
type Entry struct {
	SourceModelID   string   `json:"source_model_id"`
	Input           *float64 `json:"input_usd_per_mtok"`
	Cached          *float64 `json:"cached_input_usd_per_mtok"`
	Output          *float64 `json:"output_usd_per_mtok"`
	ExplicitModelID *string  `json:"explicit_model_id,omitempty"`
}

func (e Entry) billing() Billing {
	return Billing{Input: e.Input, Cached: e.Cached, Output: e.Output}
}

// Dataset is a versioned, embedded snapshot. FetchedAt is the snapshot time,
// not the time a quote run executes.
type Dataset struct {
	Version   string    `json:"version"`
	SourceID  string    `json:"source_id"`
	Name      string    `json:"name"`
	FetchedAt time.Time `json:"fetched_at"`
	Note      string    `json:"note"`
	Models    []Entry   `json:"models"`
}

// Info is the public description of a controlled source.
type Info struct {
	ID      string
	Name    string
	Enabled bool
}

// Target is one price profile the source may quote. UpstreamID nil means the
// model's global default; otherwise it is the upstream-scoped override.
// Names are the source-visible aliases for this target, used only for
// deterministic normalized suggestions.
type Target struct {
	ModelID    string
	UpstreamID *string
	RevisionID *string
	UpdatedAt  *time.Time
	Locked     bool
	Current    *Billing
	Names      []string
}

// Quote is one generated price suggestion. It carries the target profile
// identity and baseline captured at generation time.
type Quote struct {
	ModelID             string
	UpstreamID          *string
	SourceModelID       *string
	MatchMethod         string
	Status              string
	Locked              bool
	BasePriceRevisionID *string
	BaseUpdatedAt       *time.Time
	Current             *Billing
	Proposed            *Billing
}

// Adapter is the controlled price-source boundary. Implementations must not
// perform runtime network access.
type Adapter interface {
	Sources() []Info
	Fetch(sourceID string) (Dataset, error)
	Validate(Dataset) error
	Normalize(string) string
	Quotes(Dataset, []Target) ([]Quote, error)
}

// Builtin is the only shipped adapter: a versioned JSON dataset embedded in
// the binary.
type Builtin struct{}

func (Builtin) Sources() []Info {
	return []Info{{ID: BuiltinID, Name: BuiltinName, Enabled: true}}
}

func (Builtin) Fetch(sourceID string) (Dataset, error) {
	if sourceID != BuiltinID {
		return Dataset{}, ErrSourceNotFound
	}
	return loadBuiltin()
}

func (Builtin) Validate(d Dataset) error {
	return validateDataset(d)
}

func (Builtin) Normalize(name string) string {
	return Normalize(name)
}

func (Builtin) Quotes(d Dataset, targets []Target) ([]Quote, error) {
	if err := validateDataset(d); err != nil {
		return nil, err
	}
	return generateQuotes(d, targets), nil
}

var (
	builtinOnce sync.Once
	builtinData Dataset
	builtinErr  error
)

func loadBuiltin() (Dataset, error) {
	builtinOnce.Do(func() {
		raw, err := datasets.ReadFile("datasets/builtin.json")
		if err != nil {
			builtinErr = err
			return
		}
		var d Dataset
		if err = json.Unmarshal(raw, &d); err != nil {
			builtinErr = err
			return
		}
		if err = validateDataset(d); err != nil {
			builtinErr = err
			return
		}
		if d.SourceID != BuiltinID {
			builtinErr = errors.New("builtin dataset source_id mismatch")
			return
		}
		builtinData = d
	})
	return builtinData, builtinErr
}

// Normalize applies exactly the REQ-MUM-008 steps: lowercase, trim, drop one
// leading vendor/ segment, map _ and . to -, and collapse repeated -. No edit
// distance, token similarity, or stop-word removal is performed.
func Normalize(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexByte(s, '/'); i > 0 {
		s = s[i+1:]
	}
	var b strings.Builder
	b.Grow(len(s))
	dash := false
	for _, r := range s {
		if r == '_' || r == '.' {
			r = '-'
		}
		if r == '-' {
			if dash {
				continue
			}
			dash = true
		} else {
			dash = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func validateDataset(d Dataset) error {
	if !validText(d.SourceID, 128) || d.SourceID != strings.TrimSpace(d.SourceID) {
		return errors.New("price source id must be 1 to 128 non-space characters")
	}
	if !validText(d.Name, 128) {
		return errors.New("price source name must be 1 to 128 characters")
	}
	if !validText(d.Version, 64) {
		return errors.New("price source dataset version is required")
	}
	if d.FetchedAt.IsZero() {
		return errors.New("price source fetched_at is required")
	}
	if !validText(d.Note, 1024) {
		return errors.New("price source note is required")
	}
	seen := map[string]bool{}
	for _, m := range d.Models {
		if !validText(m.SourceModelID, 256) {
			return errors.New("price source model id must be 1 to 256 characters")
		}
		if seen[m.SourceModelID] {
			return errors.New("price source contains a duplicate source_model_id: " + m.SourceModelID)
		}
		seen[m.SourceModelID] = true
		if m.ExplicitModelID != nil && !validText(*m.ExplicitModelID, 256) {
			return errors.New("price source explicit_model_id must be 1 to 256 characters")
		}
		if m.Input == nil && m.Cached == nil && m.Output == nil {
			return errors.New("price source model has no prices: " + m.SourceModelID)
		}
		for _, p := range []*float64{m.Input, m.Cached, m.Output} {
			if p != nil && (*p < 0 || *p >= 1e8 || math.IsNaN(*p) || math.IsInf(*p, 0)) {
				return errors.New("price source prices must be nonnegative numeric(14,6) values")
			}
		}
	}
	return nil
}

func validText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}

func generateQuotes(d Dataset, targets []Target) []Quote {
	quotes := make([]Quote, len(targets))
	ambiguousNormalized := make(map[int]bool, len(d.Models))
	for i := range d.Models {
		if d.Models[i].ExplicitModelID != nil {
			continue
		}
		norm := Normalize(d.Models[i].SourceModelID)
		if norm == "" {
			continue
		}
		counts := map[bool]int{}
		for _, t := range targets {
			if normalizedMatches(norm, t) {
				global := t.UpstreamID == nil
				counts[global]++
				if counts[global] > 1 {
					ambiguousNormalized[i] = true
				}
			}
		}
	}

	for ti, t := range targets {
		q := Quote{
			ModelID:             t.ModelID,
			UpstreamID:          t.UpstreamID,
			MatchMethod:         MatchNone,
			Status:              StatusMissing,
			Locked:              t.Locked,
			BasePriceRevisionID: t.RevisionID,
			BaseUpdatedAt:       t.UpdatedAt,
			Current:             cloneBilling(t.Current),
		}
		method, chosen := exactMatches(d.Models, t)
		if len(chosen) > 0 {
			sort.Slice(chosen, func(a, b int) bool { return chosen[a].SourceModelID < chosen[b].SourceModelID })
			q.SourceModelID = new(chosen[0].SourceModelID)
			q.MatchMethod = method
			same := samePrices(chosen)
			if same {
				q.Proposed = new(chosen[0].billing())
			}
			switch {
			case !same:
				q.Status = StatusConflict
			case sameBilling(t.Current, chosen[0].billing()):
				q.Status = StatusIgnored
			case t.Locked:
				q.Status = StatusConflict
			default:
				q.Status = StatusSafe
			}
			quotes[ti] = q
			continue
		}

		normalized := normalizedMatchesFor(d.Models, t)
		if len(normalized) > 0 {
			sort.Slice(normalized, func(a, b int) bool { return normalized[a].SourceModelID < normalized[b].SourceModelID })
			q.SourceModelID = new(normalized[0].SourceModelID)
			q.MatchMethod = MatchNormalized
			ambiguous := false
			for _, m := range normalized {
				for i := range d.Models {
					if d.Models[i].SourceModelID == m.SourceModelID && ambiguousNormalized[i] {
						ambiguous = true
					}
				}
			}
			same := samePrices(normalized)
			if !ambiguous && same {
				q.Proposed = new(normalized[0].billing())
			}
			switch {
			case ambiguous || !same:
				q.Status = StatusConflict
			case sameBilling(t.Current, normalized[0].billing()):
				q.Status = StatusIgnored
			case t.Locked:
				q.Status = StatusConflict
			default:
				q.Status = StatusNeedsReview
			}
			quotes[ti] = q
		}
		quotes[ti] = q
	}
	return quotes
}

func exactMatches(entries []Entry, t Target) (string, []Entry) {
	bestTier := 99
	var best []Entry
	method := MatchNone
	for _, e := range entries {
		tier := 99
		switch {
		case t.UpstreamID == nil && e.ExplicitModelID != nil && *e.ExplicitModelID == t.ModelID:
			tier = 0
		case t.UpstreamID == nil && e.SourceModelID == t.ModelID:
			tier = 2
		case t.UpstreamID != nil && matchesAny(e.SourceModelID, t.Names):
			tier = 1
		}
		if tier == 99 {
			continue
		}
		if tier < bestTier {
			bestTier, best = tier, []Entry{e}
		} else if tier == bestTier {
			best = append(best, e)
		}
	}
	switch bestTier {
	case 0:
		method = MatchExplicit
	case 1:
		method = MatchUpstreamExact
	case 2:
		method = MatchModelExact
	}
	return method, best
}

func normalizedMatchesFor(entries []Entry, t Target) []Entry {
	out := []Entry{}
	for _, e := range entries {
		if e.ExplicitModelID != nil {
			continue
		}
		norm := Normalize(e.SourceModelID)
		if norm == "" {
			continue
		}
		if normalizedMatches(norm, t) {
			out = append(out, e)
		}
	}
	return out
}

func normalizedMatches(norm string, t Target) bool {
	for _, name := range t.Names {
		if Normalize(name) == norm {
			return true
		}
	}
	return false
}

func matchesAny(value string, names []string) bool {
	for _, name := range names {
		if name == value {
			return true
		}
	}
	return false
}

func samePrices(entries []Entry) bool {
	if len(entries) == 0 {
		return false
	}
	first := entries[0].billing()
	for _, e := range entries[1:] {
		if !sameBilling(&first, e.billing()) {
			return false
		}
	}
	return true
}

func sameBilling(a *Billing, b Billing) bool {
	if a == nil {
		return false
	}
	return samePrice(a.Input, b.Input) && samePrice(a.Cached, b.Cached) && samePrice(a.Output, b.Output)
}

func samePrice(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Round(*a*1e6) == math.Round(*b*1e6)
}

func cloneBilling(b *Billing) *Billing {
	if b == nil {
		return nil
	}
	return new(*b)
}
