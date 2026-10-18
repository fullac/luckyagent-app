package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yurika0211/luckyagent-runtime/mcpclient"
)

func TestMCPManagementHTTP(t *testing.T) {
	remote := mcp.NewServer(&mcp.Implementation{Name: "admin-test", Version: "1"}, nil)
	h := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer h.Close()
	mgr := mcpclient.NewManager(mcpclient.Config{Servers: map[string]mcpclient.ServerConfig{"configured": {URL: h.URL}}}, nil)
	defer mgr.Close()
	server := &Server{mcpAdmin: mgr}
	for _, test := range []struct {
		method, body string
		status       int
	}{
		{http.MethodGet, "", http.StatusOK},
		{http.MethodPost, `{"action":"connect","name":"configured"}`, http.StatusOK},
		{http.MethodPost, `{"action":"refresh","name":"configured"}`, http.StatusOK},
		{http.MethodPost, `{"action":"disconnect","name":"configured"}`, http.StatusOK},
		{http.MethodPost, `{"action":"connect","name":"missing"}`, http.StatusBadGateway},
		{http.MethodPost, `{"action":"refresh","name":"missing"}`, http.StatusBadGateway},
		{http.MethodPost, `{"action":"execute","name":"missing"}`, http.StatusBadRequest},
		{http.MethodPost, `{"action":"connect","name":"missing","command":"sh"}`, http.StatusBadRequest},
		{http.MethodDelete, "", http.StatusMethodNotAllowed},
	} {
		r := httptest.NewRequest(test.method, "/api/v1/mcp/servers", strings.NewReader(test.body))
		w := httptest.NewRecorder()
		server.handleMCPServers(w, r)
		if w.Code != test.status {
			t.Fatalf("%s %s: got %d %s", test.method, test.body, w.Code, w.Body.String())
		}
	}
}
