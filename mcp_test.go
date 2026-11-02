package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mcpTestHandler(call func(context.Context, string, json.RawMessage) (any, error)) http.Handler {
	return newMCPHandler([]Tool{{
		Name: "read", Description: "Read allowlisted content",
		InputSchema: map[string]any{"type": "object"},
		Annotations: ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: true},
	}}, call)
}

func mcpTestRequest(h http.Handler, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func mcpTestEnvelope(t *testing.T, w *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid response %q: %v", w.Body.String(), err)
	}
	if string(response["jsonrpc"]) != `"2.0"` {
		t.Fatalf("missing JSON-RPC version: %s", w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected Content-Type: %s", w.Header().Get("Content-Type"))
	}
	return response
}

func mcpTestError(t *testing.T, w *httptest.ResponseRecorder, status, code int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	response := mcpTestEnvelope(t, w)
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response["error"], &e); err != nil || e.Code != code || e.Message == "" {
		t.Fatalf("want error %d, got %s", code, w.Body.String())
	}
	if _, ok := response["result"]; ok {
		t.Fatalf("response includes both result and error: %s", w.Body.String())
	}
}

func TestMCPInitialize(t *testing.T) {
	h := mcpTestHandler(nil)
	for _, requestedVersion := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "1999-01-01"} {
		t.Run(requestedVersion, func(t *testing.T) {
			w := mcpTestRequest(h, fmt.Sprintf(`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, requestedVersion))
			if w.Code != http.StatusOK {
				t.Fatalf("initialize: %d %s", w.Code, w.Body.String())
			}
			response := mcpTestEnvelope(t, w)
			var result struct {
				ProtocolVersion string                     `json:"protocolVersion"`
				Capabilities    map[string]json.RawMessage `json:"capabilities"`
				ServerInfo      map[string]string          `json:"serverInfo"`
				Instructions    string                     `json:"instructions"`
			}
			if err := json.Unmarshal(response["result"], &result); err != nil {
				t.Fatal(err)
			}
			expected := requestedVersion
			if requestedVersion == "1999-01-01" {
				expected = "2025-11-25"
			}
			if result.ProtocolVersion != expected || string(response["id"]) != `"init"` {
				t.Fatalf("bad negotiation or id: %s", w.Body.String())
			}
			if len(result.Capabilities) != 1 || string(result.Capabilities["tools"]) != "{}" {
				t.Fatalf("unexpected capabilities: %s", w.Body.String())
			}
			if result.ServerInfo["name"] == "" || result.ServerInfo["version"] != version ||
				!strings.Contains(result.Instructions, "untrusted") {
				t.Fatalf("missing server details/security instructions: %s", w.Body.String())
			}
			if w.Header().Get("MCP-Session-Id") != "" {
				t.Fatal("stateless server created a session")
			}
		})
	}
	for _, params := range []string{`{}`, `{"protocolVersion":"2025-11-25"}`, `{"protocolVersion":12,"capabilities":{},"clientInfo":{"name":"x","version":"1"}}`} {
		w := mcpTestRequest(h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+params+`}`)
		mcpTestError(t, w, http.StatusOK, -32602)
	}
}

func TestMCPNotifications(t *testing.T) {
	h := mcpTestHandler(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("notification invoked a tool")
		return nil, nil
	})
	for _, method := range []string{"notifications/initialized", "notifications/cancelled", "notifications/extension", "unknown/extension"} {
		w := mcpTestRequest(h, fmt.Sprintf(`{"jsonrpc":"2.0","method":%q}`, method))
		if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
			t.Fatalf("notification %s: %d %s", method, w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"initialize", "ping", "tools/list", "tools/call"} {
		w := mcpTestRequest(h, fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"name":"read"}}`, method))
		mcpTestError(t, w, http.StatusBadRequest, -32600)
	}
	w := mcpTestRequest(h, `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`)
	mcpTestError(t, w, http.StatusBadRequest, -32600)
}

func TestMCPListAndPing(t *testing.T) {
	for _, test := range []struct {
		method string
		h      http.Handler
		count  int
	}{
		{"ping", mcpTestHandler(nil), 0},
		{"tools/list", mcpTestHandler(nil), 1},
		{"tools/list", newMCPHandler(nil, nil), 0},
	} {
		w := mcpTestRequest(test.h, fmt.Sprintf(`{"jsonrpc":"2.0","id":-4,"method":%q}`, test.method))
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		response := mcpTestEnvelope(t, w)
		if string(response["id"]) != "-4" {
			t.Fatal("request id not preserved")
		}
		if test.method == "ping" {
			if string(response["result"]) != "{}" {
				t.Fatal(w.Body.String())
			}
			continue
		}
		var result struct {
			Tools []Tool `json:"tools"`
		}
		if err := json.Unmarshal(response["result"], &result); err != nil || len(result.Tools) != test.count {
			t.Fatalf("bad tools list: %s", w.Body.String())
		}
		if test.count > 0 && (!result.Tools[0].Annotations.ReadOnlyHint || result.Tools[0].Annotations.DestructiveHint) {
			t.Fatal("read-only annotations lost")
		}
		if strings.Contains(w.Body.String(), `"tools":null`) {
			t.Fatal("empty tool list must be an array")
		}
	}
	w := mcpTestRequest(mcpTestHandler(nil), `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"invalid"}}`)
	mcpTestError(t, w, http.StatusOK, -32602)
}

func TestMCPToolCall(t *testing.T) {
	calls := 0
	h := mcpTestHandler(func(ctx context.Context, name string, args json.RawMessage) (any, error) {
		calls++
		if name != "read" || string(args) != `{"value":1}` || ctx == nil {
			t.Fatalf("incorrect tool invocation: %s %s", name, args)
		}
		return map[string]any{"title": "Upstream content", "count": 2}, nil
	})
	w := mcpTestRequest(h, `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"read","arguments":{"value":1}}}`)
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("tool call: %d %s", w.Code, w.Body.String())
	}
	response := mcpTestEnvelope(t, w)
	if string(response["id"]) != "9007199254740993" {
		t.Fatal("large integer id lost precision")
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(response["result"], &result); err != nil ||
		result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("bad tool result: %s", w.Body.String())
	}
	var text map[string]any
	if json.Unmarshal([]byte(result.Content[0].Text), &text) != nil || text["title"] != "Upstream content" {
		t.Fatal("tool text must contain JSON")
	}
	if strings.Contains(w.Body.String(), "structuredContent") {
		t.Fatal("result duplicated as structured content")
	}
	for _, params := range []string{
		`{}`, `{"name":"unknown","arguments":{}}`, `{"name":1}`,
		`{"name":"read","arguments":null}`, `{"name":"read","arguments":[]}`,
		`{"name":"read","arguments":"bad"}`, `{"name":"read","arguments":true}`,
	} {
		w = mcpTestRequest(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+params+`}`)
		mcpTestError(t, w, http.StatusOK, -32602)
	}
	if calls != 1 {
		t.Fatal("invalid request invoked tool callback")
	}
}

func TestMCPToolErrorAndMissingArguments(t *testing.T) {
	h := mcpTestHandler(func(_ context.Context, _ string, args json.RawMessage) (any, error) {
		if string(args) != "{}" {
			t.Fatal("missing arguments did not default to an object")
		}
		return nil, errors.New("Upstream service unavailable")
	})
	w := mcpTestRequest(h, `{"jsonrpc":"2.0","id":"","method":"tools/call","params":{"name":"read"}}`)
	response := mcpTestEnvelope(t, w)
	if w.Code != http.StatusOK || len(response["error"]) != 0 ||
		!strings.Contains(string(response["result"]), `"isError":true`) ||
		!strings.Contains(string(response["result"]), "Upstream service unavailable") {
		t.Fatalf("tool failure must be a tool result: %s", w.Body.String())
	}
}

func TestMCPMalformed(t *testing.T) {
	h := mcpTestHandler(nil)
	for _, body := range []string{"", "{", "{} {}", `{"jsonrpc":`} {
		mcpTestError(t, mcpTestRequest(h, body), http.StatusBadRequest, -32700)
	}
	for _, body := range []string{
		`null`, `[]`, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, `"string"`, `42`,
		`{}`, `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":true,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":{},"method":"ping"}`,
		`{"jsonrpc":"2.0","id":[],"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1e2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1,"method":null}`,
		`{"jsonrpc":"2.0","id":1,"method":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":""}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":[]}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":null}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":42}`,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
	} {
		t.Run(body, func(t *testing.T) {
			mcpTestError(t, mcpTestRequest(h, body), http.StatusBadRequest, -32600)
		})
	}
}

func TestMCPUnsupportedMethods(t *testing.T) {
	h := mcpTestHandler(nil)
	for _, method := range []string{"resources/list", "prompts/list", "logging/setLevel", "unknown"} {
		w := mcpTestRequest(h, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q}`, method))
		mcpTestError(t, w, http.StatusOK, -32601)
	}
	for _, method := range []string{"GET", "DELETE", "HEAD", "OPTIONS", "PUT"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/mcp", nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
			t.Fatalf("%s should return 405 with Allow: POST: %d", method, w.Code)
		}
	}
}

func TestMCPHeaders(t *testing.T) {
	h := mcpTestHandler(nil)
	for _, test := range []struct {
		name, accept, contentType, version string
		status                             int
	}{
		{"valid", "application/json, text/event-stream", "application/json", "", 200},
		{"charset", "application/json, text/event-stream", "application/json; charset=utf-8", "2025-06-18", 200},
		{"quality", "text/event-stream;q=0.1, application/json;q=1", "application/json", "2025-03-26", 200},
		{"missing Accept", "", "application/json", "", 406},
		{"json only", "application/json", "application/json", "", 406},
		{"stream only", "text/event-stream", "application/json", "", 406},
		{"wildcard", "*/*", "application/json", "", 406},
		{"disabled JSON", "application/json;q=0, text/event-stream", "application/json", "", 406},
		{"invalid quality", "application/json;q=NaN, text/event-stream", "application/json", "", 406},
		{"bad content type", "application/json, text/event-stream", "text/plain", "", 415},
		{"missing content type", "application/json, text/event-stream", "", "", 415},
		{"unsupported version", "application/json, text/event-stream", "application/json", "2099-01-01", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
			r.Header.Set("Accept", test.accept)
			r.Header.Set("Content-Type", test.contentType)
			r.Header.Set("MCP-Protocol-Version", test.version)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status %d, want %d: %s", w.Code, test.status, w.Body.String())
			}
		})
	}
}

func TestMCPBodyBound(t *testing.T) {
	h := mcpTestHandler(nil)
	base := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"padding":""}}`
	body := strings.Replace(base, `"padding":""`, `"padding":"`+strings.Repeat("x", maxMCPBody-len(base))+`"`, 1)
	if len(body) != maxMCPBody {
		t.Fatal("test body not exactly at limit")
	}
	if w := mcpTestRequest(h, body); w.Code != http.StatusOK {
		t.Fatalf("body at limit rejected: %d %s", w.Code, w.Body.String())
	}
	mcpTestError(t, mcpTestRequest(h, body+" "), http.StatusRequestEntityTooLarge, -32600)
}

type mcpFailReader struct{}

func (mcpFailReader) Read([]byte) (int, error) { return 0, errors.New("secret upstream details") }

func TestMCPReadAndEncodingErrorsAreSafe(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Body = io.NopCloser(mcpFailReader{})
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mcpTestHandler(nil).ServeHTTP(w, r)
	mcpTestError(t, w, http.StatusBadRequest, -32700)
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("read error leaked")
	}
	h := mcpTestHandler(func(context.Context, string, json.RawMessage) (any, error) {
		return func() {}, nil
	})
	w = mcpTestRequest(h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read"}}`)
	mcpTestError(t, w, http.StatusOK, -32603)
	h = newMCPHandler([]Tool{{Name: "bad", InputSchema: make(chan int)}}, nil)
	w = mcpTestRequest(h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	mcpTestError(t, w, http.StatusInternalServerError, -32603)
}

func FuzzMCPRequest(f *testing.F) {
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`, `null`, `[]`, `{`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":[],"method":{}}`,
	} {
		f.Add(seed)
	}
	h := mcpTestHandler(nil)
	f.Fuzz(func(t *testing.T, body string) {
		w := mcpTestRequest(h, body)
		if w.Code == http.StatusAccepted {
			if w.Body.Len() != 0 {
				t.Fatal("notification has a response body")
			}
		} else if !json.Valid(w.Body.Bytes()) {
			t.Fatalf("invalid response JSON: %q", w.Body.String())
		}
	})
}
