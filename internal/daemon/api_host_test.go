package daemon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/config"
	"go.kenn.io/roborev/internal/testutil"
)

func TestAPIRejectsForeignHost(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MaxWorkers = 0
	db, _ := testutil.OpenTestDBWithDir(t)
	server := newServerWithLogs(db, cfg, "", newTestErrorLog(), newTestActivityLog())
	t.Cleanup(func() { _ = server.Close() })

	req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req.Host = "rebind.example"
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	body, err := io.ReadAll(w.Result().Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "invalid host")

	ok := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	ok.Host = "127.0.0.1:7373"
	okW := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(okW, ok)
	assert.Equal(t, http.StatusOK, okW.Code)
}

func TestAPIHostAllowlist(t *testing.T) {
	const bind = "127.0.0.1:7373"
	cases := []struct {
		host string
		ok   bool
	}{
		{host: "localhost", ok: true},
		{host: "localhost:7373", ok: true},
		{host: "127.0.0.1", ok: true},
		{host: "127.0.0.1:7373", ok: true},
		{host: "::1", ok: true},
		{host: "[::1]:7373", ok: true},
		{host: bind, ok: true},
		{host: "rebind.example", ok: false},
		{host: "rebind.example:7373", ok: false},
		{host: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			assert.Equal(t, tc.ok, apiHostAllowed(tc.host, bind))
		})
	}
}

func newLocalAPIRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = "127.0.0.1"
	return req
}
