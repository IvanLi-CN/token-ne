package xray

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	proxyman "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/app/router"
	routercmd "github.com/xtls/xray-core/app/router/command"
	core "github.com/xtls/xray-core/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const testProfileID = "0193f0c2-7b1a-7c3e-9f00-000000000008"

// fakeXray is an in-process Xray API. It keeps the same three collections the
// real API exposes and records every mutating call in order, so a test can
// assert the swap sequence rather than just its end state.
type fakeXray struct {
	proxyman.UnimplementedHandlerServiceServer
	routercmd.UnimplementedRoutingServiceServer

	mu        sync.Mutex
	inbounds  map[string]*core.InboundHandlerConfig
	outbounds map[string]*core.OutboundHandlerConfig
	// rules maps rule tag to outbound tag, preserving the order Xray would
	// return. AddRule with ShouldAppend=false replaces the whole set.
	rules     map[string]string
	ruleOrder []string
	calls     []string

	failRemoveOutbound string
	failAddOutbound    string
	// ignoreRulePublish accepts AddRule without applying it, which is how the
	// verification test proves the reconciler trusts the read-back rather than
	// the call result.
	ignoreRulePublish bool
}

func newFakeXray() *fakeXray {
	return &fakeXray{
		inbounds:  map[string]*core.InboundHandlerConfig{},
		outbounds: map[string]*core.OutboundHandlerConfig{},
		rules:     map[string]string{},
	}
}

func (f *fakeXray) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeXray) AddInbound(_ context.Context, r *proxyman.AddInboundRequest) (*proxyman.AddInboundResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("add_inbound:" + r.GetInbound().GetTag())
	f.inbounds[r.GetInbound().GetTag()] = r.GetInbound()
	return &proxyman.AddInboundResponse{}, nil
}

func (f *fakeXray) RemoveInbound(_ context.Context, r *proxyman.RemoveInboundRequest) (*proxyman.RemoveInboundResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("remove_inbound:" + r.GetTag())
	delete(f.inbounds, r.GetTag())
	return &proxyman.RemoveInboundResponse{}, nil
}

func (f *fakeXray) ListInbounds(context.Context, *proxyman.ListInboundsRequest) (*proxyman.ListInboundsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &proxyman.ListInboundsResponse{}
	for tag := range f.inbounds {
		out.Inbounds = append(out.Inbounds, &core.InboundHandlerConfig{Tag: tag})
	}
	return out, nil
}

func (f *fakeXray) AddOutbound(_ context.Context, r *proxyman.AddOutboundRequest) (*proxyman.AddOutboundResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tag := r.GetOutbound().GetTag()
	f.record("add_outbound:" + tag)
	if f.failAddOutbound == tag {
		return nil, status.Error(codes.InvalidArgument, "rejected by test")
	}
	f.outbounds[tag] = r.GetOutbound()
	return &proxyman.AddOutboundResponse{}, nil
}

func (f *fakeXray) RemoveOutbound(_ context.Context, r *proxyman.RemoveOutboundRequest) (*proxyman.RemoveOutboundResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("remove_outbound:" + r.GetTag())
	if f.failRemoveOutbound == r.GetTag() {
		return nil, status.Error(codes.InvalidArgument, "rejected by test")
	}
	delete(f.outbounds, r.GetTag())
	return &proxyman.RemoveOutboundResponse{}, nil
}

func (f *fakeXray) AlterOutbound(context.Context, *proxyman.AlterOutboundRequest) (*proxyman.AlterOutboundResponse, error) {
	return nil, status.Error(codes.Unimplemented, "xray cannot alter an outbound in place")
}

func (f *fakeXray) ListOutbounds(context.Context, *proxyman.ListOutboundsRequest) (*proxyman.ListOutboundsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &proxyman.ListOutboundsResponse{}
	for tag := range f.outbounds {
		out.Outbounds = append(out.Outbounds, &core.OutboundHandlerConfig{Tag: tag})
	}
	return out, nil
}

func (f *fakeXray) AddRule(_ context.Context, r *routercmd.AddRuleRequest) (*routercmd.AddRuleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Decode the replacement rule set the way Xray would, so a rule that does
	// not carry an inbound tag is caught here instead of silently routing every
	// inbound.
	instance, err := r.GetConfig().GetInstance()
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	cfg, ok := instance.(*router.Config)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "rule config is not a router.Config")
	}
	f.record("add_rule")
	if f.ignoreRulePublish {
		return &routercmd.AddRuleResponse{}, nil
	}
	if !r.GetShouldAppend() {
		f.rules = map[string]string{}
		f.ruleOrder = nil
	}
	for _, rule := range cfg.GetRule() {
		var target string
		if tagged, ok := rule.GetTargetTag().(*router.RoutingRule_Tag); ok {
			target = tagged.Tag
		}
		f.rules[rule.GetRuleTag()] = target
		f.ruleOrder = append(f.ruleOrder, rule.GetRuleTag())
	}
	return &routercmd.AddRuleResponse{}, nil
}

func (f *fakeXray) RemoveRule(_ context.Context, r *routercmd.RemoveRuleRequest) (*routercmd.RemoveRuleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("remove_rule:" + r.GetRuleTag())
	delete(f.rules, r.GetRuleTag())
	return &routercmd.RemoveRuleResponse{}, nil
}

func (f *fakeXray) ListRule(context.Context, *routercmd.ListRuleRequest) (*routercmd.ListRuleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &routercmd.ListRuleResponse{}
	for _, tag := range f.ruleOrder {
		outbound, ok := f.rules[tag]
		if !ok {
			continue
		}
		out.Rules = append(out.Rules, &routercmd.ListRuleItem{Tag: outbound, RuleTag: tag})
	}
	return out, nil
}

func (f *fakeXray) TestRoute(_ context.Context, r *routercmd.TestRouteRequest) (*routercmd.RoutingContext, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inbound := r.GetRoutingContext().GetInboundTag()
	// RuleTag is derived from the profile id, so map the inbound tag back to its
	// profile and look the rule up the same way the reconciler does.
	id, ok := ProfileIDFromTag(inbound)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "unknown inbound tag "+inbound)
	}
	outbound, ok := f.rules[RuleTag(id)]
	if !ok {
		return &routercmd.RoutingContext{}, nil
	}
	return &routercmd.RoutingContext{InboundTag: inbound, OutboundTag: outbound}, nil
}

func (f *fakeXray) snapshot() (inbounds, outbounds []string, rules map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for tag := range f.inbounds {
		inbounds = append(inbounds, tag)
	}
	for tag := range f.outbounds {
		outbounds = append(outbounds, tag)
	}
	rules = map[string]string{}
	for tag, outbound := range f.rules {
		rules[tag] = outbound
	}
	return inbounds, outbounds, rules
}

func (f *fakeXray) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

// serve starts the fake over bufconn and returns a client bound to it.
func serve(t *testing.T) (*fakeXray, *Client) {
	t.Helper()
	f := newFakeXray()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	proxyman.RegisterHandlerServiceServer(server, f)
	routercmd.RegisterRoutingServiceServer(server, f)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return f, NewClient(conn, 5*time.Second)
}

func desired(protocol Protocol, credentials Credentials) Desired {
	return Desired{
		Profile: Profile{
			ID:          testProfileID,
			Protocol:    protocol,
			Server:      "proxy.example.test",
			Port:        443,
			Transport:   []byte(`{"network":"ws","path":"/t"}`),
			TLS:         []byte(`{"mode":"tls","server_name":"proxy.example.test"}`),
			InboundPort: 15001,
			Enabled:     true,
		},
		Credentials: credentials,
	}
}

func validProfiles() map[Protocol]Credentials {
	return map[Protocol]Credentials{
		ProtocolHTTP:        {Username: "user", Password: "secret"},
		ProtocolSOCKS5:      {Username: "user", Password: "secret"},
		ProtocolShadowsocks: {Cipher: "aes-256-gcm", Password: "secret"},
		ProtocolVMess:       {UUID: "11111111-1111-4111-8111-111111111111"},
		ProtocolVLESS:       {UUID: "11111111-1111-4111-8111-111111111111"},
		ProtocolTrojan:      {Password: "secret"},
	}
}

func TestRenderAcceptsEverySupportedProtocol(t *testing.T) {
	for protocol, credentials := range validProfiles() {
		t.Run(string(protocol), func(t *testing.T) {
			objects, err := Render(desired(protocol, credentials))
			if err != nil {
				t.Fatalf("render %s: %v", protocol, err)
			}
			if got, want := objects.Inbound.GetTag(), InboundTag(testProfileID); got != want {
				t.Fatalf("inbound tag = %q, want %q", got, want)
			}
			if got, want := objects.Outbound.GetTag(), OutboundTag(testProfileID); got != want {
				t.Fatalf("outbound tag = %q, want %q", got, want)
			}
			if objects.Inbound.GetProxySettings() == nil || objects.Outbound.GetProxySettings() == nil {
				t.Fatal("rendered object is missing its proxy settings")
			}
		})
	}
}

func TestRenderRejectsUnknownOptionsWithFieldPath(t *testing.T) {
	base := desired(ProtocolVMess, validProfiles()[ProtocolVMess])
	cases := []struct {
		name     string
		mutate   func(*Desired)
		wantPath string
	}{
		{"unknown protocol", func(d *Desired) { d.Profile.Protocol = "wireguard" }, "protocol"},
		{"unknown transport network", func(d *Desired) { d.Profile.Transport = []byte(`{"network":"quic"}`) }, "transport.network"},
		{"unknown transport field", func(d *Desired) { d.Profile.Transport = []byte(`{"network":"tcp","sni":"x"}`) }, "transport.sni"},
		{"unknown tls mode", func(d *Desired) { d.Profile.TLS = []byte(`{"mode":"mtls"}`) }, "tls.mode"},
		{"unknown tls field", func(d *Desired) { d.Profile.TLS = []byte(`{"mode":"none","ca":"x"}`) }, "tls.ca"},
		{"missing uuid", func(d *Desired) { d.Credentials.UUID = "" }, "credentials.uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := base
			tc.mutate(&d)
			_, err := Render(d)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if !IsValidationError(err) {
				t.Fatalf("error is not a ValidationError: %v", err)
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("cannot unwrap ValidationError: %v", err)
			}
			if validation.Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", validation.Path, tc.wantPath)
			}
		})
	}
}

func TestEnsureStableCreatesObjectsAndRoutesOnce(t *testing.T) {
	fake, client := serve(t)
	engine := NewEngine(client)
	summary := engine.ReconcileAll(context.Background(), []Target{{Desired: desired(ProtocolVMess, validProfiles()[ProtocolVMess])}})
	if summary.Unreachable {
		t.Fatalf("unexpected unreachable: %s", summary.Error)
	}
	if len(summary.Results) != 1 || summary.Results[0].Status != StatusReady {
		t.Fatalf("results = %+v, want one ready profile", summary.Results)
	}
	inbounds, outbounds, rules := fake.snapshot()
	if len(inbounds) != 1 || len(outbounds) != 1 {
		t.Fatalf("inbounds = %v, outbounds = %v, want one of each", inbounds, outbounds)
	}
	if rules[RuleTag(testProfileID)] != OutboundTag(testProfileID) {
		t.Fatalf("rules = %v, want the stable outbound", rules)
	}
	// A second pass is idempotent: no extra objects and no extra rule
	// publications, because Xray has no incremental replace.
	before := len(fake.callLog())
	summary = engine.ReconcileAll(context.Background(), []Target{{Desired: desired(ProtocolVMess, validProfiles()[ProtocolVMess])}})
	if summary.Results[0].Status != StatusUnchanged {
		t.Fatalf("second pass = %+v, want unchanged", summary.Results[0])
	}
	if got := fake.callLog()[before:]; len(got) != 0 {
		t.Fatalf("second pass wrote to Xray: %v", got)
	}
}

func TestSwapFollowsTheFixedOrderAndNeverLeavesStagedInService(t *testing.T) {
	fake, client := serve(t)
	id := testProfileID
	stable, staged := OutboundTag(id), StagedOutboundTag(id)
	// Pre-existing state from a policy already in service.
	fake.inbounds[InboundTag(id)] = &core.InboundHandlerConfig{Tag: InboundTag(id)}
	fake.outbounds[stable] = &core.OutboundHandlerConfig{Tag: stable}
	fake.rules[RuleTag(id)] = stable
	fake.ruleOrder = []string{RuleTag(id)}

	d := desired(ProtocolVMess, validProfiles()[ProtocolVMess])
	d.Profile.RuntimeState = "swapping"
	summary := NewEngine(client).ReconcileAll(context.Background(), []Target{{Desired: d}})
	if summary.Unreachable {
		t.Fatalf("unexpected unreachable: %s", summary.Error)
	}
	if got := summary.Results[0]; got.Status != StatusReady || got.Phase != "" {
		t.Fatalf("result = %+v, want ready with no lingering phase", got)
	}

	want := []string{
		"add_outbound:" + staged,
		"add_rule",
		"remove_outbound:" + stable,
		"add_outbound:" + stable,
		"add_rule",
		"remove_outbound:" + staged,
	}
	got := fake.callLog()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	// After the sequence the route is on the stable tag and nothing references
	// the staged one.
	_, outbounds, rules := fake.snapshot()
	if rules[RuleTag(id)] != stable {
		t.Fatalf("rule = %q, want the stable outbound", rules[RuleTag(id)])
	}
	for _, outbound := range outbounds {
		if outbound == staged {
			t.Fatal("staged outbound survived the swap")
		}
	}
}

func TestSwapFailureLeavesResumablePhaseAndLiveRoute(t *testing.T) {
	fake, client := serve(t)
	id := testProfileID
	stable := OutboundTag(id)
	fake.inbounds[InboundTag(id)] = &core.InboundHandlerConfig{Tag: InboundTag(id)}
	fake.outbounds[stable] = &core.OutboundHandlerConfig{Tag: stable}
	fake.rules[RuleTag(id)] = stable
	fake.ruleOrder = []string{RuleTag(id)}
	// Failing to delete the old stable outbound stops the sequence at
	// route_staged, where the route already points at the live :staged one.
	fake.failRemoveOutbound = stable

	d := desired(ProtocolVMess, validProfiles()[ProtocolVMess])
	d.Profile.RuntimeState = "swapping"
	summary := NewEngine(client).ReconcileAll(context.Background(), []Target{{Desired: d, PreviouslyEffectiveEnabled: true}})
	result := summary.Results[0]
	if result.Status != StatusSwapping {
		t.Fatalf("status = %q, want swapping", result.Status)
	}
	if result.Phase != PhaseRouteStaged {
		t.Fatalf("phase = %q, want %q", result.Phase, PhaseRouteStaged)
	}
	// The profile stays eligible: the route points at the staged outbound,
	// which exists.
	if !result.EffectiveEnabled || result.EffectiveEnabledChanged {
		t.Fatalf("effective = %v changed = %v, want true and unchanged", result.EffectiveEnabled, result.EffectiveEnabledChanged)
	}
	_, _, rules := fake.snapshot()
	if got := rules[RuleTag(id)]; got != StagedOutboundTag(id) {
		t.Fatalf("route = %q, want the staged outbound so traffic keeps flowing", got)
	}
	if _, ok := fake.outbounds[StagedOutboundTag(id)]; !ok {
		t.Fatal("route points at a missing outbound")
	}
}

func TestDisableRemovesEverythingButKeepsThePort(t *testing.T) {
	fake, client := serve(t)
	id := testProfileID
	d := desired(ProtocolVMess, validProfiles()[ProtocolVMess])
	if summary := NewEngine(client).ReconcileAll(context.Background(), []Target{{Desired: d}}); summary.Unreachable {
		t.Fatalf("setup failed: %s", summary.Error)
	}
	before := len(fake.callLog())
	d.Profile.Enabled = false
	summary := NewEngine(client).ReconcileAll(context.Background(), []Target{{Desired: d, PreviouslyEffectiveEnabled: true}})
	result := summary.Results[0]
	if result.Status != StatusDisabled || result.EffectiveEnabled {
		t.Fatalf("result = %+v, want disabled and ineligible", result)
	}
	inbounds, outbounds, rules := fake.snapshot()
	if len(inbounds) != 0 || len(outbounds) != 0 {
		t.Fatalf("inbounds = %v, outbounds = %v, want both empty", inbounds, outbounds)
	}
	if _, ok := rules[RuleTag(id)]; ok {
		t.Fatalf("rules = %v, want no rule for a disabled profile", rules)
	}
	if d.Profile.InboundPort != 15001 {
		t.Fatal("the test mutated the port, which the reconciler must never do")
	}
	if got := fake.callLog()[before:]; len(got) == 0 {
		t.Fatal("disable wrote nothing")
	}
}

func TestReconcileReapsOrphansButKeepsSwappingStaged(t *testing.T) {
	fake, client := serve(t)
	swappingID := testProfileID
	orphanID := "0193f0c2-7b1a-7c3e-9f00-000000000099"

	d := desired(ProtocolVMess, validProfiles()[ProtocolVMess])
	d.Profile.RuntimeState = "swapping"
	fake.inbounds[InboundTag(swappingID)] = &core.InboundHandlerConfig{Tag: InboundTag(swappingID)}
	fake.outbounds[OutboundTag(swappingID)] = &core.OutboundHandlerConfig{Tag: OutboundTag(swappingID)}
	fake.rules[RuleTag(swappingID)] = OutboundTag(swappingID)
	fake.ruleOrder = []string{RuleTag(swappingID)}
	// An object left behind by a deleted profile.
	fake.inbounds[InboundTag(orphanID)] = &core.InboundHandlerConfig{Tag: InboundTag(orphanID)}
	fake.outbounds[OutboundTag(orphanID)] = &core.OutboundHandlerConfig{Tag: OutboundTag(orphanID)}
	// An unrelated bootstrap object must be left alone.
	fake.inbounds["api-in"] = &core.InboundHandlerConfig{Tag: "api-in"}
	fake.outbounds["blocked"] = &core.OutboundHandlerConfig{Tag: "blocked"}

	// Fail the swap after the route moved to :staged, so the pass ends with a
	// staged outbound that is still in service. Reaping runs first, which is
	// exactly the window where a naive cleanup would delete it and cut traffic.
	fake.failAddOutbound = OutboundTag(swappingID)
	summary := NewEngine(client).ReconcileAll(context.Background(), []Target{{Desired: d}})
	if summary.Unreachable {
		t.Fatalf("unexpected unreachable: %s", summary.Error)
	}
	if got := summary.Results[0].Status; got != StatusSwapping {
		t.Fatalf("status = %q, want swapping (the swap was made to fail)", got)
	}
	inbounds, outbounds, _ := fake.snapshot()
	for _, tag := range append(inbounds, outbounds...) {
		if id, owned := ProfileIDFromTag(tag); owned && id == orphanID {
			t.Fatalf("orphaned object survived: %s", tag)
		}
	}
	if _, ok := fake.outbounds[StagedOutboundTag(swappingID)]; !ok {
		t.Fatal("reaping removed the staged outbound of a profile mid-swap")
	}
	if _, ok := fake.inbounds["api-in"]; !ok {
		t.Fatal("reaping removed a bootstrap inbound")
	}
	if _, ok := fake.outbounds["blocked"]; !ok {
		t.Fatal("reaping removed the blackhole outbound")
	}
}

func TestReconcileReportsDegradedWhenTheRouteVerificationFails(t *testing.T) {
	fake, client := serve(t)
	// AddRule is accepted but has no effect, so the inbound resolves nowhere:
	// verification must catch it rather than trusting the call result.
	fake.ignoreRulePublish = true
	summary := NewEngine(client).ReconcileAll(context.Background(), []Target{{Desired: desired(ProtocolVMess, validProfiles()[ProtocolVMess])}})
	if summary.Unreachable {
		t.Fatalf("unexpected unreachable: %s", summary.Error)
	}
	result := summary.Results[0]
	if result.Status != StatusDegraded {
		t.Fatalf("status = %q, want degraded", result.Status)
	}
	if result.EffectiveEnabled {
		t.Fatal("a degraded profile must not stay eligible")
	}
}

func TestRulePublicationReplacesInsteadOfAppending(t *testing.T) {
	fake, client := serve(t)
	engine := NewEngine(client)
	routeTarget := map[string]string{testProfileID: OutboundTag(testProfileID)}
	for range 3 {
		if err := engine.publishRoute(context.Background(), testProfileID, OutboundTag(testProfileID), routeTarget); err != nil {
			t.Fatal(err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.rules) != 1 || len(fake.ruleOrder) != 1 {
		t.Fatalf("rules = %v order = %v, want exactly one rule after three publications", fake.rules, fake.ruleOrder)
	}
}

func TestUnreachableXrayIsDistinctFromARejectedProfile(t *testing.T) {
	// Dial to a closed port: the connectivity wait must expire and report the
	// unreachable class the caller alerts on.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	client, err := Dial(ctx, "127.0.0.1:1", DialOptions{DialTimeout: 50 * time.Millisecond})
	if err == nil {
		_ = client.Close()
		t.Fatal("dial to a closed port succeeded")
	}
	if !IsUnreachable(err) {
		t.Fatalf("error = %v, want UnreachableError", err)
	}
}

func TestProfileIDFromTagOnlyAcceptsOwnedTags(t *testing.T) {
	cases := []struct {
		tag  string
		want string
		ok   bool
	}{
		{InboundTag(testProfileID), testProfileID, true},
		{OutboundTag(testProfileID), testProfileID, true},
		{StagedOutboundTag(testProfileID), testProfileID, true},
		{"api-in", "", false},
		{"blocked", "", false},
		{"egress:", "", false},
	}
	for _, tc := range cases {
		got, ok := ProfileIDFromTag(tc.tag)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("ProfileIDFromTag(%q) = (%q, %v), want (%q, %v)", tc.tag, got, ok, tc.want, tc.ok)
		}
	}
}
