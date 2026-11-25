package pricesource

import (
	"testing"
	"time"
)

func TestNormalizeFollowsSpecExactly(t *testing.T) {
	for in, want := range map[string]string{
		"OpenAI/GPT_4o":            "gpt-4o",
		"  GPT_4o  ":               "gpt-4o",
		"gpt..4__o":                "gpt-4-o",
		"vendor/sub/name":          "sub/name",
		"vendor/Model.Name":        "model-name",
		"a---b___c":                "a-b-c",
		"gpt-4o":                   "gpt-4o",
		"gpt-4o-mini":              "gpt-4o-mini",
		"vendor/":                  "",
		"/leading":                 "/leading",
		"UPPER/CASE":               "case",
		"OpenAI/GPT-4o-2024-08-06": "gpt-4o-2024-08-06",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if Normalize("gpt-4o") == Normalize("gpt-4o-mini") {
		t.Fatal("normalization must not collapse gpt-4o into gpt-4o-mini")
	}
}

func TestNormalizedMatchIsEqualityOnly(t *testing.T) {
	d := Dataset{
		Version: "v1", SourceID: "test", Name: "test", FetchedAt: time.Now().UTC(), Note: "note",
		Models: []Entry{{SourceModelID: "OpenAI/GPT_4o", Input: new(2.5), Output: new(10.0)}},
	}
	quotes, err := Builtin{}.Quotes(d, []Target{
		{ModelID: "gpt-4o", Names: []string{"gpt-4o"}},
		{ModelID: "gpt-4o-mini", Names: []string{"gpt-4o-mini"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if quotes[0].MatchMethod != MatchNormalized || quotes[0].Status != StatusNeedsReview {
		t.Fatalf("gpt-4o quote = %+v", quotes[0])
	}
	if quotes[1].MatchMethod != MatchNone || quotes[1].Status != StatusMissing {
		t.Fatalf("gpt-4o-mini quote = %+v", quotes[1])
	}
}

// A single source row may normalize to the same name as more than one target
// profile (for example two upstream capabilities sharing an upstream model
// name). The row is not ambiguous about its price: every target still gets a
// reviewable proposal instead of an inert conflict with no proposed value.
func TestNormalizedQuoteAppliesToEveryTargetSharingAName(t *testing.T) {
	now := time.Now().UTC()
	rev := "00000000-0000-0000-0000-000000000001"
	upstreamA := "00000000-0000-0000-0000-000000000002"
	upstreamB := "00000000-0000-0000-0000-000000000003"
	d := Dataset{
		Version: "v1", SourceID: "test", Name: "test", FetchedAt: now, Note: "note",
		Models: []Entry{{SourceModelID: "OpenAI/GPT_4o", Input: new(2.5), Output: new(10.0)}},
	}
	quotes, err := Builtin{}.Quotes(d, []Target{
		{ModelID: "gpt-4o", RevisionID: &rev, UpdatedAt: &now, Names: []string{"gpt-4o"}},
		{ModelID: "gpt-4o", UpstreamID: &upstreamA, UpdatedAt: &now, Names: []string{"gpt-4o"}},
		{ModelID: "gpt-4o", UpstreamID: &upstreamB, UpdatedAt: &now, Names: []string{"gpt-4o"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, quote := range quotes {
		if quote.MatchMethod != MatchNormalized || quote.Status != StatusNeedsReview {
			t.Fatalf("quote %d = method %s status %s, want normalized/needs_review: %+v", i, quote.MatchMethod, quote.Status, quote)
		}
		if quote.Proposed == nil || quote.Proposed.Input == nil || *quote.Proposed.Input != 2.5 {
			t.Fatalf("quote %d must carry the source price: %+v", i, quote)
		}
	}
}

func TestQuoteGenerationStatuses(t *testing.T) {
	now := time.Now().UTC()
	rev := "00000000-0000-0000-0000-000000000001"
	d := Dataset{
		Version: "v1", SourceID: "test", Name: "test", FetchedAt: now, Note: "note",
		Models: []Entry{
			{SourceModelID: "gpt-4o-mini", Input: new(0.15), Cached: new(0.075), Output: new(0.6)},
			{SourceModelID: "OpenAI/GPT_4o", Input: new(2.5), Cached: new(1.25), Output: new(10.0)},
			{SourceModelID: "openai/gpt-4.1", ExplicitModelID: new("gpt-4.1"), Input: new(2.0), Output: new(8.0)},
			{SourceModelID: "meta-llama/Llama-3.3-70B-Instruct", Input: new(0.59), Output: new(0.79)},
			{SourceModelID: "locked-model", Input: new(9.0), Output: new(9.0)},
			{SourceModelID: "foo_bar", Input: new(1.0), Output: new(2.0)},
			{SourceModelID: "FOO.BAR", Input: new(3.0), Output: new(4.0)},
		},
	}
	upstreamID := "00000000-0000-0000-0000-000000000002"
	quotes, err := Builtin{}.Quotes(d, []Target{
		{ModelID: "gpt-4o", RevisionID: &rev, UpdatedAt: &now, Names: []string{"gpt-4o"},
			Current: &Billing{Input: new(1.0), Cached: new(1.0), Output: new(1.0)}},
		{ModelID: "gpt-4o-mini", RevisionID: &rev, UpdatedAt: &now, Names: []string{"gpt-4o-mini"},
			Current: &Billing{Input: new(0.15), Cached: new(0.075), Output: new(0.6)}},
		{ModelID: "gpt-4.1", RevisionID: &rev, UpdatedAt: &now, Names: []string{"gpt-4.1"},
			Current: &Billing{Input: new(1.0), Output: new(1.0)}},
		{ModelID: "llama-3.3-70b", UpstreamID: &upstreamID, UpdatedAt: &now,
			Names: []string{"meta-llama/Llama-3.3-70B-Instruct"}},
		{ModelID: "locked-model", RevisionID: &rev, UpdatedAt: &now, Locked: true,
			Names: []string{"locked-model"}, Current: &Billing{Input: new(1.0)}},
		{ModelID: "foo-bar", RevisionID: &rev, UpdatedAt: &now, Names: []string{"foo-bar"},
			Current: &Billing{Input: new(1.0)}},
		{ModelID: "gpt-4-1", RevisionID: &rev, UpdatedAt: &now, Names: []string{"gpt-4-1"},
			Current: &Billing{Input: new(1.0)}},
		{ModelID: "unmatched", RevisionID: &rev, UpdatedAt: &now, Names: []string{"unmatched"},
			Current: &Billing{Input: new(1.0)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		method string
		status string
	}{
		{MatchNormalized, StatusNeedsReview},
		{MatchModelExact, StatusIgnored},
		{MatchExplicit, StatusSafe},
		{MatchUpstreamExact, StatusSafe},
		{MatchModelExact, StatusConflict},
		{MatchNormalized, StatusConflict},
		{MatchNone, StatusMissing},
		{MatchNone, StatusMissing},
	} {
		if quotes[i].MatchMethod != tc.method || quotes[i].Status != tc.status {
			t.Fatalf("quote %d %s = method %s status %s, want %s/%s: %+v",
				i, quotes[i].ModelID, quotes[i].MatchMethod, quotes[i].Status, tc.method, tc.status, quotes[i])
		}
	}
	if quotes[5].Proposed != nil {
		t.Fatalf("disagreeing source rows must not carry a single proposed price: %+v", quotes[5])
	}
	if quotes[6].MatchMethod != MatchNone {
		t.Fatalf("explicit mapping leaked into normalized suggestions: %+v", quotes[6])
	}
	if quotes[4].Locked && quotes[4].Status != StatusConflict {
		t.Fatalf("locked target must be a conflict: %+v", quotes[4])
	}
}

func TestDatasetValidation(t *testing.T) {
	valid := Dataset{Version: "v1", SourceID: "test", Name: "test", FetchedAt: time.Now().UTC(), Note: "note",
		Models: []Entry{{SourceModelID: "a", Input: new(1.0)}}}
	if err := (Builtin{}).Validate(valid); err != nil {
		t.Fatalf("valid dataset: %v", err)
	}
	cases := map[string]Dataset{
		"missing source":  {Version: "v1", Name: "test", FetchedAt: time.Now().UTC(), Note: "note"},
		"missing version": {SourceID: "test", Name: "test", FetchedAt: time.Now().UTC(), Note: "note"},
		"missing time":    {Version: "v1", SourceID: "test", Name: "test", Note: "note"},
		"duplicate row":   {Version: "v1", SourceID: "test", Name: "test", FetchedAt: time.Now().UTC(), Note: "note", Models: []Entry{{SourceModelID: "a", Input: new(1.0)}, {SourceModelID: "a", Input: new(2.0)}}},
		"empty prices":    {Version: "v1", SourceID: "test", Name: "test", FetchedAt: time.Now().UTC(), Note: "note", Models: []Entry{{SourceModelID: "a"}}},
		"negative price":  {Version: "v1", SourceID: "test", Name: "test", FetchedAt: time.Now().UTC(), Note: "note", Models: []Entry{{SourceModelID: "a", Input: new(-1.0)}}},
	}
	for name, d := range cases {
		if err := (Builtin{}).Validate(d); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}

func TestBuiltinDatasetLoads(t *testing.T) {
	adapter := Builtin{}
	d, err := adapter.Fetch(BuiltinID)
	if err != nil {
		t.Fatal(err)
	}
	if d.SourceID != BuiltinID || d.Version == "" || d.FetchedAt.IsZero() || d.Note == "" || len(d.Models) == 0 {
		t.Fatalf("incomplete builtin dataset: %+v", d)
	}
	if _, err = adapter.Fetch("https://example.test/prices.json"); err != ErrSourceNotFound {
		t.Fatalf("arbitrary source fetch = %v, want ErrSourceNotFound", err)
	}
}
