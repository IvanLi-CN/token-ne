package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/usage"
	"github.com/google/uuid"
)

// The egress pair must survive the ingest round trip and come back out of the
// request query, because the contract makes it part of the usage fact rather
// than logging metadata. A database-backed test is the only way to prove both
// halves line up with the schema constraint from migration 00012.
func TestUsageEgressPairRoundTripsIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	instance := uuid.NewString()
	version := "egress-" + uuid.NewString()[:8]
	versionID := uuid.NewString()
	if _, err = repo.Pool.Exec(ctx, `insert into core_versions(id,version,image_ref,protocol_version,config_schema_min,config_schema_max)
 values($1,$2,'image',1,1,1)`, versionID, version); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Pool.Exec(ctx, `insert into core_instances(id,version_id,container_name,address,state) values($1,$2,$3,$4,'running')`,
		instance, versionID, "egress-test", "egress-test:8080"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = repo.Pool.Exec(cleanup, `delete from usage_events where core_instance=$1::uuid`, instance)
		_, _ = repo.Pool.Exec(cleanup, `delete from core_instances where id=$1`, instance)
		_, _ = repo.Pool.Exec(cleanup, `delete from core_versions where id=$1`, versionID)
	})

	mode, direct := "proxy", "direct"
	proxyID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	makeEvent := func(egressMode, egressProxy *string) usage.Event {
		return usage.Event{
			EventID: uuid.NewString(), RequestID: uuid.NewString(), OccurredAt: now,
			CoreVersion: version, CoreInstance: instance, Endpoint: "/v1/chat/completions",
			Outcome: "success", AttemptCount: 1, EgressMode: egressMode, EgressProxyID: egressProxy,
		}
	}
	batch := usage.Batch{Instance: instance, Events: []usage.Event{
		makeEvent(&mode, &proxyID),
		makeEvent(&direct, nil),
		makeEvent(nil, nil),
	}}
	result, err := NewUsageInserter(repo.Pool).IngestUsage(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Inserted) != 3 {
		t.Fatalf("inserted %d events, want 3 (result: %+v)", len(result.Inserted), result)
	}

	for _, tc := range []struct {
		name      string
		event     usage.Event
		wantMode  *string
		wantProxy *string
	}{
		{"proxy", batch.Events[0], &mode, &proxyID},
		{"direct", batch.Events[1], &direct, nil},
		{"absent", batch.Events[2], nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotMode, gotProxy *string
			if err := repo.Pool.QueryRow(ctx, `select egress_mode,egress_proxy_id::text from usage_events where event_id=$1`,
				tc.event.EventID).Scan(&gotMode, &gotProxy); err != nil {
				t.Fatal(err)
			}
			if !sameOptional(gotMode, tc.wantMode) || !sameOptional(gotProxy, tc.wantProxy) {
				t.Fatalf("stored %s/%s, want %s/%s", displayOptional(gotMode), displayOptional(gotProxy),
					displayOptional(tc.wantMode), displayOptional(tc.wantProxy))
			}
		})
	}

	// The request detail must expose the same pair; the Web surface has no other
	// way to show the egress the contract makes mandatory.
	detail, err := repo.UsageRequest(ctx, batch.Events[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameOptional(detail.EgressMode, &mode) || !sameOptional(detail.EgressProxyID, &proxyID) {
		t.Fatalf("detail egress = %s/%s, want %s/%s", displayOptional(detail.EgressMode),
			displayOptional(detail.EgressProxyID), mode, proxyID)
	}
	if _, err = repo.UsageRequests(ctx, RequestFilter{UsageRange: UsageRange{From: now.Add(-time.Minute), To: now.Add(time.Minute)}, Limit: 100}); err != nil {
		t.Fatalf("list query must tolerate the new columns: %v", err)
	}
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func displayOptional(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
