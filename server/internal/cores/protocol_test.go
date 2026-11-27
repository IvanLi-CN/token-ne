package cores

import (
	"errors"
	"testing"
	"time"
)

const longHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func statusTime() time.Time { return time.Now().UTC() }

func statusWith(schema int) Status {
	now := statusTime()
	off := false
	zero := int64(0)
	return Status{
		Instance:    "0193f0c2-7b1a-7c3e-9f00-0000000000b1",
		Version:     "0.1.0",
		Draining:    &off,
		StartedAt:   now.Add(-time.Hour),
		Features:    append([]string{}, features...),
		ControlledAt: &now,
		Inflight:    &zero,
		Spool:       Spool{Bytes: &zero, Unacked: &zero, AckSeq: &zero, Dropped: &zero},
		Config: &ConfigStatus{
			Revision: 1, Hash: "sha256:" + longHash, Schema: schema,
			Source: "control_plane", Signature: "off", Override: &off,
		},
	}
}

// A rolling upgrade runs two core generations at once. The publisher must accept
// both and render each one the shape it declared; demanding a single version is
// what made the previous implementation drop the whole fleet from Caddy.
func TestSchemaForPicksTheNewestSharedShape(t *testing.T) {
	for _, tc := range []struct {
		name     string
		min, max int
		want     int
	}{
		{"current only", SchemaCurrent, SchemaCurrent, SchemaCurrent},
		{"previous only", SchemaFloor, SchemaFloor, SchemaFloor},
		{"spanning the floor", SchemaFloor, SchemaCurrent, SchemaCurrent},
		{"beyond current clamps down", SchemaFloor, SchemaCurrent + 3, SchemaCurrent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SchemaFor(tc.min, tc.max)
			if err != nil {
				t.Fatalf("range %d..%d refused: %v", tc.min, tc.max, err)
			}
			if got != tc.want {
				t.Fatalf("range %d..%d rendered %d, want %d", tc.min, tc.max, got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name     string
		min, max int
	}{
		{"below the floor", SchemaFloor - 1, SchemaFloor - 1},
		{"above current", SchemaCurrent + 1, SchemaCurrent + 2},
		{"inverted", 3, 2},
	} {
		t.Run("refused "+tc.name, func(t *testing.T) {
			if _, err := SchemaFor(tc.min, tc.max); !errors.Is(err, ErrCompatibility) {
				t.Fatalf("range %d..%d admitted: %v", tc.min, tc.max, err)
			}
		})
	}
}

// Both generations must pass the admission gate while a deployment is mixed.
func TestBothGenerationsAreCompatible(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta Meta
	}{
		{"schema 1 core", Meta{Protocol: Protocol, SchemaMin: 1, SchemaMax: 1, Features: append([]string{}, features...)}},
		{"schema 2 core", Meta{Protocol: Protocol, SchemaMin: SchemaCurrent, SchemaMax: SchemaCurrent, Features: append([]string{}, features...)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.meta.Compatible(); err != nil {
				t.Fatalf("mixed-fleet core refused: %v", err)
			}
		})
	}

	// A core that shares no version with this control plane is still refused.
	gap := Meta{Protocol: Protocol, SchemaMin: SchemaCurrent + 1, SchemaMax: SchemaCurrent + 2, Features: append([]string{}, features...)}
	if err := gap.Compatible(); !errors.Is(err, ErrCompatibility) {
		t.Fatalf("disjoint range admitted: %v", err)
	}
}

// An applied status may report either generation; anything outside the supported
// range is a hard error.
func TestAppliedStatusAcceptsEverySupportedSchema(t *testing.T) {
	now := statusTime()
	for _, schema := range []int{SchemaFloor, SchemaCurrent} {
		s := statusWith(schema)
		if err := s.Validate(now); err != nil {
			t.Fatalf("applied schema %d refused: %v", schema, err)
		}
		// A snapshot rendered for one generation must not claim to match the other.
		if (Snapshot{Revision: 1, Hash: "sha256:" + longHash, Schema: schema}).Matches(s) != true {
			t.Fatalf("snapshot of schema %d did not match its own status", schema)
		}
	}
	if err := statusWith(SchemaCurrent + 1).Validate(now); !errors.Is(err, ErrCompatibility) {
		t.Fatalf("unknown applied schema admitted: %v", err)
	}
}

// The broker's own declaration spans the range it can render.
func TestBrokerMetaSpansTheSupportedRange(t *testing.T) {
	m := BrokerMeta()
	if m.SchemaMin != SchemaFloor || m.SchemaMax != SchemaCurrent {
		t.Fatalf("broker range = %d..%d", m.SchemaMin, m.SchemaMax)
	}
}
