package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "s3cret"

// The bearer check is the only thing between an address to listen on and
// everyone who can reach the port.
func TestRequireBearer(t *testing.T) {
	t.Parallel()

	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusTeapot)
	})

	for _, c := range []struct {
		name       string
		token      string
		header     string
		wantStatus int
		wantThru   bool
	}{
		{"no token configured lets everything through", "", "", http.StatusTeapot, true},
		{"no token configured ignores a header", "", "Bearer anything", http.StatusTeapot, true},
		{"the right token passes", testToken, "Bearer " + testToken, http.StatusTeapot, true},
		{"a missing header is refused", testToken, "", http.StatusUnauthorized, false},
		{"the wrong token is refused", testToken, "Bearer nope", http.StatusUnauthorized, false},
		{"the bare token without the scheme is refused", testToken, testToken, http.StatusUnauthorized, false},
		{"a prefix of the token is refused", testToken, "Bearer s3cre", http.StatusUnauthorized, false},
		{"the token with more after it is refused", testToken, "Bearer s3cretXX", http.StatusUnauthorized, false},
		{"the scheme is case sensitive", testToken, "bearer " + testToken, http.StatusUnauthorized, false},
	} {
		reached = false
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, Path, http.NoBody)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		rec := httptest.NewRecorder()
		requireBearer(c.token, "zzyzx-mcp", next).ServeHTTP(rec, req)

		if rec.Code != c.wantStatus {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.wantStatus)
		}
		if reached != c.wantThru {
			t.Errorf("%s: handler reached = %v, want %v", c.name, reached, c.wantThru)
		}
		if c.wantStatus == http.StatusUnauthorized {
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="zzyzx-mcp"` {
				t.Errorf("%s: WWW-Authenticate = %q, want a Bearer challenge naming the tool", c.name, got)
			}
		}
	}
}

// The health probe has to stay outside the bearer check, or a container
// with a token configured never becomes healthy.
func TestHandlerRoutes(t *testing.T) {
	t.Parallel()

	srv := mcp.NewServer(&mcp.Implementation{Name: "zzyzx-mcp", Version: "test"}, nil)
	h := Handler(srv, Options{AuthToken: testToken, Name: "zzyzx-mcp"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, HealthPath, http.NoBody))
	if rec.Code != http.StatusOK {
		t.Errorf("%s with a token configured = %d, want 200", HealthPath, rec.Code)
	}
	if body := rec.Body.String(); strings.TrimSpace(body) != "ok" {
		t.Errorf("%s body = %q", HealthPath, body)
	}

	// and the MCP endpoint is not
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, Path, strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("%s without a token = %d, want 401", Path, rec.Code)
	}

	// the health probe is a GET route, so another method must not reach it
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, HealthPath, http.NoBody))
	if rec.Code == http.StatusOK {
		t.Errorf("POST %s answered 200; the route is GET only", HealthPath)
	}
}

// An address to listen on with no bearer token is an open port, so it is
// refused unless the operator said so in as many words, and the refusal
// names the tool's own variables.
func TestCheckAuth(t *testing.T) {
	t.Parallel()

	err := CheckAuth(Options{EnvPrefix: "ZZYZX"})
	if err == nil {
		t.Fatal("no token and no allow-no-auth was not refused")
	}
	for _, want := range []string{"--auth-token (ZZYZX_AUTH_TOKEN)", "--allow-no-auth (ZZYZX_ALLOW_NO_AUTH=true)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say how to fix it: %v, want %q in it", err, want)
		}
	}
	if err := CheckAuth(Options{AllowNoAuth: true}); err != nil {
		t.Errorf("allow-no-auth was refused: %v", err)
	}
	if err := CheckAuth(Options{AuthToken: testToken}); err != nil {
		t.Errorf("a token was refused: %v", err)
	}
	// and Run does not listen without one
	srv := mcp.NewServer(&mcp.Implementation{Name: "zzyzx-mcp", Version: "test"}, nil)
	if err := Run(t.Context(), srv, Options{Listen: "127.0.0.1:0", EnvPrefix: "ZZYZX"}); err == nil || !strings.Contains(err.Error(), "ZZYZX_AUTH_TOKEN") {
		t.Errorf("Run with an address and no token: %v", err)
	}
}

// bearer sends every request with the token.
type bearer struct{ token string }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)

	return http.DefaultTransport.RoundTrip(req)
}

type echoIn struct {
	Say string `json:"say"`
}

type echoOut struct {
	Said string `json:"said"`
}

// Served over HTTP, a client with the token calls a tool, and a stop does
// not wait on the event stream that client holds open: the sessions are
// closed, and the server is down well inside its own grace.
func TestServeAnswersAndStopsWithAClientConnected(t *testing.T) {
	t.Parallel()

	srv := mcp.NewServer(&mcp.Implementation{Name: "zzyzx-mcp", Version: "test"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "says it back"}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Said: in.Say}, nil
	})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	served := make(chan error, 1)
	go func() {
		served <- serve(ctx, srv, ln, Options{AuthToken: testToken, Name: "zzyzx-mcp", EnvPrefix: "ZZYZX"})
	}()

	endpoint := "http://" + ln.Addr().String() + Path
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)

	// without the token the client is sent away
	if _, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint, MaxRetries: -1}, nil); err == nil {
		t.Error("a client with no token connected")
	}

	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearer{testToken}}}, nil)
	if err != nil {
		t.Fatalf("connecting with the token: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"say": "zzyzx"}})
	if err != nil || res.IsError {
		t.Fatalf("calling the tool: %v %+v", err, res)
	}
	if said, ok := res.StructuredContent.(map[string]any); !ok || said["said"] != "zzyzx" {
		t.Errorf("the tool answered %v", res.StructuredContent)
	}

	// the client is still connected, its stream open
	stop()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("the server stopped with %v", err)
		}
	case <-time.After(shutdownTimeout - 2*time.Second):
		t.Fatal("the server was still up eight seconds after it was told to stop: it is waiting on the client's open stream")
	}
}
