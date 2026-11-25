package xray

import (
	"context"
	"time"

	proxyman "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/app/router"
	routercmd "github.com/xtls/xray-core/app/router/command"
	"github.com/xtls/xray-core/common/serial"
	core "github.com/xtls/xray-core/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// DialOptions tunes the client. The zero value uses DefaultDialTimeout and
// DefaultCallTimeout.
type DialOptions struct {
	// DialTimeout bounds how long Dial waits for a ready connection.
	DialTimeout time.Duration
	// CallTimeout bounds one API call, so a hung Xray cannot stall the
	// caller's reconcile tick.
	CallTimeout time.Duration
	// GRPCDialOptions are appended to the defaults; tests use them to install a
	// bufconn dialer.
	GRPCDialOptions []grpc.DialOption
}

// Client talks to the Xray gRPC API. It holds no credentials, starts no
// goroutines and does no retries: one call is one RPC.
type Client struct {
	conn        grpc.ClientConnInterface
	handler     proxyman.HandlerServiceClient
	routing     routercmd.RoutingServiceClient
	callTimeout time.Duration
	close       func() error
}

// Dial connects to XRAY_API_URL. There is no authentication: the contract
// relies on the xray-control network isolation. A connection that is not ready
// within the dial timeout is reported as *UnreachableError.
func Dial(ctx context.Context, address string, opts DialOptions) (*Client, error) {
	if address == "" {
		return nil, &UnreachableError{Err: errNoAddress}
	}
	timeout := opts.DialTimeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	dialOpts = append(dialOpts, opts.GRPCDialOptions...)
	conn, err := grpc.NewClient(address, dialOpts...)
	if err != nil {
		return nil, &UnreachableError{Err: err}
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn.Connect()
	for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
		if !conn.WaitForStateChange(dialCtx, state) {
			conn.Close()
			err := dialCtx.Err()
			if err == nil {
				err = context.DeadlineExceeded
			}
			return nil, &UnreachableError{Err: err}
		}
	}
	return newClient(conn, opts.CallTimeout, conn.Close), nil
}

// NewClient wraps an existing connection. Callers that manage the connection
// lifecycle (and tests over bufconn) use it instead of Dial.
func NewClient(conn grpc.ClientConnInterface, callTimeout time.Duration) *Client {
	return newClient(conn, callTimeout, nil)
}

func newClient(conn grpc.ClientConnInterface, callTimeout time.Duration, closeFn func() error) *Client {
	if callTimeout <= 0 {
		callTimeout = DefaultCallTimeout
	}
	return &Client{
		conn:        conn,
		handler:     proxyman.NewHandlerServiceClient(conn),
		routing:     routercmd.NewRoutingServiceClient(conn),
		callTimeout: callTimeout,
		close:       closeFn,
	}
}

// Close releases the connection Dial created. It is a no-op for NewClient.
func (c *Client) Close() error {
	if c.close == nil {
		return nil
	}
	return c.close()
}

// observed is one read of the Xray runtime state.
type observed struct {
	inbounds  map[string]bool
	outbounds map[string]bool
	// rules maps rule tag to the outbound tag it targets.
	rules map[string]string
}

func (c *Client) observe(ctx context.Context) (observed, error) {
	obs := observed{inbounds: map[string]bool{}, outbounds: map[string]bool{}, rules: map[string]string{}}
	inbounds, err := c.handler.ListInbounds(ctx, &proxyman.ListInboundsRequest{IsOnlyTags: true})
	if err != nil {
		return obs, classify(err)
	}
	for _, ib := range inbounds.GetInbounds() {
		obs.inbounds[ib.GetTag()] = true
	}
	outbounds, err := c.handler.ListOutbounds(ctx, &proxyman.ListOutboundsRequest{})
	if err != nil {
		return obs, classify(err)
	}
	for _, ob := range outbounds.GetOutbounds() {
		obs.outbounds[ob.GetTag()] = true
	}
	rules, err := c.routing.ListRule(ctx, &routercmd.ListRuleRequest{})
	if err != nil {
		return obs, classify(err)
	}
	for _, rule := range rules.GetRules() {
		obs.rules[rule.GetRuleTag()] = rule.GetTag()
	}
	return obs, nil
}

func (c *Client) addInbound(ctx context.Context, in *core.InboundHandlerConfig) error {
	_, err := c.handler.AddInbound(ctx, &proxyman.AddInboundRequest{Inbound: in})
	return classify(err)
}

func (c *Client) removeInbound(ctx context.Context, tag string) error {
	_, err := c.handler.RemoveInbound(ctx, &proxyman.RemoveInboundRequest{Tag: tag})
	return classify(err)
}

func (c *Client) addOutbound(ctx context.Context, out *core.OutboundHandlerConfig) error {
	_, err := c.handler.AddOutbound(ctx, &proxyman.AddOutboundRequest{Outbound: out})
	return classify(err)
}

func (c *Client) removeOutbound(ctx context.Context, tag string) error {
	_, err := c.handler.RemoveOutbound(ctx, &proxyman.RemoveOutboundRequest{Tag: tag})
	return classify(err)
}

// publishRules replaces the whole routing rule set in one call. Xray has no
// incremental "replace this rule" operation, and appending would duplicate
// rules across reconciles, so every publication is a complete replacement.
func (c *Client) publishRules(ctx context.Context, rules []*router.RoutingRule) error {
	cfg := &router.Config{Rule: rules}
	_, err := c.routing.AddRule(ctx, &routercmd.AddRuleRequest{
		Config:       serial.ToTypedMessage(cfg),
		ShouldAppend: false,
	})
	return classify(err)
}

// testRoute asks the router what outbound an inbound tag resolves to.
func (c *Client) testRoute(ctx context.Context, inboundTag string) (string, error) {
	resp, err := c.routing.TestRoute(ctx, &routercmd.TestRouteRequest{
		RoutingContext: &routercmd.RoutingContext{InboundTag: inboundTag},
		FieldSelectors: []string{"outbound"},
	})
	if err != nil {
		return "", classify(err)
	}
	return resp.GetOutboundTag(), nil
}

// verifyRoute confirms through ListRule and TestRoute that the profile's
// inbound really resolves to want before the caller reports the profile ready.
func (c *Client) verifyRoute(ctx context.Context, profileID, want string) error {
	var ruleTag string
	err := c.withCall(ctx, func(call context.Context) error {
		rules, err := c.routing.ListRule(call, &routercmd.ListRuleRequest{})
		if err != nil {
			return classify(err)
		}
		ruleTag = RuleTag(profileID)
		found := false
		for _, rule := range rules.GetRules() {
			if rule.GetRuleTag() == ruleTag {
				found = true
				if rule.GetTag() != want {
					return &RejectedError{ProfileID: profileID, Step: "verify_rule", Reason: "rule points at " + rule.GetTag() + ", want " + want}
				}
			}
		}
		if !found {
			return &RejectedError{ProfileID: profileID, Step: "verify_rule", Reason: "routing rule is missing"}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var got string
	err = c.withCall(ctx, func(call context.Context) error {
		got, err = c.testRoute(call, InboundTag(profileID))
		return err
	})
	if err != nil {
		return err
	}
	if got != want {
		return &RejectedError{ProfileID: profileID, Step: "verify_route", Reason: "inbound resolves to " + got + ", want " + want}
	}
	return nil
}

// verifyRoutes verifies every profile in ids resolves to its expected outbound.
func (c *Client) verifyRoutes(ctx context.Context, ids []string, want map[string]string) error {
	for _, id := range ids {
		target := want[id]
		if target == "" {
			continue
		}
		if err := c.verifyRoute(ctx, id, target); err != nil {
			return err
		}
	}
	return nil
}

// outboundExists reads the outbound list to confirm one tag is live.
func (c *Client) outboundExists(ctx context.Context, tag string) (bool, error) {
	var exists bool
	err := c.withCall(ctx, func(call context.Context) error {
		outbounds, err := c.handler.ListOutbounds(call, &proxyman.ListOutboundsRequest{})
		if err != nil {
			return classify(err)
		}
		for _, ob := range outbounds.GetOutbounds() {
			if ob.GetTag() == tag {
				exists = true
				break
			}
		}
		return nil
	})
	return exists, err
}

func (c *Client) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.callTimeout)
}

// withCall wraps one RPC in the per-call deadline.
func (c *Client) withCall(ctx context.Context, fn func(context.Context) error) error {
	callCtx, cancel := c.callContext(ctx)
	defer cancel()
	return fn(callCtx)
}

// classify maps transport failures onto *UnreachableError and leaves semantic
// rejections as plain errors so the caller can tell "Xray is down" from "Xray
// refused this config".
func classify(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return &UnreachableError{Err: err}
	}
	return err
}
