package cores

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
)

type Client struct {
	Token string
	HTTP  *http.Client
}

func NewClient(token string) *Client {
	return &Client{Token: token, HTTP: &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) request(ctx context.Context, address, method, path, instance string, snapshot Snapshot, dst any) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, bytes.NewReader(snapshot.Body))
	if err != nil {
		return err
	}
	if path != "/healthz" && path != "/readyz" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set(ProtocolHeader, "1")
		req.Header.Set(RequestHeader, id.String())
		if instance != "" {
			req.Header.Set(InstanceHeader, instance)
		}
	}
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(RevisionHeader, strconv.FormatInt(snapshot.Revision, 10))
		if snapshot.Signature != "" {
			req.Header.Set(SignatureHeader, snapshot.Signature)
		}
	}
	for attempt := 0; ; attempt++ {
		err = c.send(req, dst)
		if err == nil || method != http.MethodPut || attempt == 3 || !errors.Is(err, errRetry) {
			return err
		}
		timer := time.NewTimer(time.Second << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		req = req.Clone(ctx)
		req.Body, err = req.GetBody()
		if err != nil {
			return err
		}
	}
}

var errRetry = errors.New("core temporarily unavailable")

func (c *Client) send(req *http.Request, dst any) error {
	r, err := c.HTTP.Do(req)
	if err != nil {
		return errRetry
	}
	defer r.Body.Close()
	if r.StatusCode >= 500 {
		return errRetry
	}
	if r.StatusCode != http.StatusOK {
		var reply struct{ Error struct{ Code string } }
		_ = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&reply)
		if r.StatusCode == 409 {
			switch reply.Error.Code {
			case "override_active":
				return ErrOverride
			case "stale_revision":
				return ErrStaleRevision
			case "incompatible_version":
				return ErrCompatibility
			case "unsupported_feature":
				return ErrFeature
			}
		}
		return fmt.Errorf("core returned HTTP %d", r.StatusCode)
	}
	if dst == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	if err != nil || len(b) > 65536 || json.Unmarshal(b, dst) != nil {
		return errors.New("invalid core response")
	}
	return nil
}

func (c *Client) Meta(ctx context.Context, address string) (m Meta, e error) {
	e = c.request(ctx, address, http.MethodGet, "/internal/v1/meta", "", Snapshot{}, &m)
	if e == nil {
		e = m.Compatible()
	}
	return
}
func (c *Client) Live(ctx context.Context, address string) bool {
	return c.request(ctx, address, http.MethodGet, "/healthz", "", Snapshot{}, nil) == nil
}
func (c *Client) Ready(ctx context.Context, address string) bool {
	return c.request(ctx, address, http.MethodGet, "/readyz", "", Snapshot{}, nil) == nil
}
func (c *Client) Status(ctx context.Context, address, instance string) (s Status, e error) {
	e = c.request(ctx, address, http.MethodGet, "/internal/v1/status", instance, Snapshot{}, &s)
	if e == nil && s.Instance != instance {
		e = ErrInvalid
	}
	if e == nil {
		e = s.Validate(time.Now())
	}
	return
}
func (c *Client) Push(ctx context.Context, address, instance string, s Snapshot) error {
	var ack struct {
		Protocol  int    `json:"protocol_version"`
		Instance  string `json:"instance_id"`
		Revision  int64  `json:"applied_revision"`
		Schema    int    `json:"config_schema_version"`
		Signature string `json:"signature_state"`
	}
	if err := c.request(ctx, address, http.MethodPut, "/internal/v1/config", instance, s, &ack); err != nil {
		return err
	}
	// The core echoes the schema it applied; a mixed fleet applies different ones.
	if ack.Protocol != Protocol || ack.Schema != s.Schema {
		return ErrCompatibility
	}
	if ack.Instance != instance || ack.Revision != s.Revision {
		return ErrInvalid
	}
	switch ack.Signature {
	case "valid", "invalid", "missing", "off":
		return nil
	}
	return ErrInvalid
}
