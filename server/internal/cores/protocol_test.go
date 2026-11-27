package cores

import (
	"errors"
	"testing"
)

// A core that only understands config schema 1 cannot decode a schema 2
// snapshot: model_routes[].priority became position and upstream_models[] now
// requires upstream_model_id. It must be refused at the gate with a readable
// compatibility error, never admitted and then fed snapshots it answers 422 to.
func TestSchemaOneCoreIsIncompatible(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta Meta
	}{
		{"schema 1 only", Meta{Protocol: Protocol, SchemaMin: 1, SchemaMax: 1, Features: append([]string{}, features...)}},
		{"max below current", Meta{Protocol: Protocol, SchemaMin: 1, SchemaMax: Schema - 1, Features: append([]string{}, features...)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.meta.Compatible(); !errors.Is(err, ErrCompatibility) {
				t.Fatalf("schema range 1..%d admitted: %v", tc.meta.SchemaMax, err)
			}
		})
	}

	current := Meta{Protocol: Protocol, SchemaMin: Schema, SchemaMax: Schema, Features: append([]string{}, features...)}
	if err := current.Compatible(); err != nil {
		t.Fatalf("current schema refused: %v", err)
	}
	// A core that declares a wider range covering the current schema stays usable.
	wider := Meta{Protocol: Protocol, SchemaMin: 1, SchemaMax: Schema, Features: append([]string{}, features...)}
	if err := wider.Compatible(); err != nil {
		t.Fatalf("overlapping schema range refused: %v", err)
	}
}

// RegisterVersion and Deploy share the same range check, so a stale image cannot
// enter the registry as a deployable target in the first place.
func TestRegisterVersionRejectsSchemaOne(t *testing.T) {
	r := &Runtime{CoreImage: "ghcr.io/example/core"}
	if _, err := r.RegisterVersion(t.Context(), Target{Version: "0.1.0", Protocol: Protocol, SchemaMin: 1, SchemaMax: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("schema 1 version registered: %v", err)
	}
}
