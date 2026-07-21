package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestOpenClawAPIRoutesAreRegistered(t *testing.T) {
	server := &APIServer{}
	mux := http.NewServeMux()
	server.registerRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	paths := []string{
		"/api/v1/windows",
		"/api/v1/windows/open",
		"/api/v1/windows/import",
		"/api/v1/windows/arrange",
		"/api/v1/windows/select-all",
		"/api/v1/windows/close",
		"/api/v1/windows/navigate",
		"/api/v1/tabs/navigate",
		"/api/v1/windows/zoom",
		"/api/v1/sync/status",
		"/api/v1/sync/start",
		"/api/v1/sync/stop",
		"/api/v1/sync/master",
		"/api/v1/tabs/keep-current",
		"/api/v1/tabs/keep-new",
		"/api/v1/input/text",
		"/api/v1/input/random",
		"/api/v1/input/keystroke",
		"/api/v1/input/click",
		"/api/v1/focus/master",
		"/api/v1/input/focus/master",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, path, nil)
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, req)
			if res.Code != http.StatusMethodNotAllowed {
				t.Fatalf("route %s returned %d, want %d", path, res.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

func TestParseTargetNumbersSupportsSkillRanges(t *testing.T) {
	server := &APIServer{}
	got, err := server.parseTargetNumbers("3,1-2,5")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTargetNumbers() = %v, want %v", got, want)
	}

	if _, err := server.parseTargetNumbers("4-2"); err == nil {
		t.Fatal("descending range must be rejected")
	}
}

func TestCDPNavigateDirectUsesVisiblePageTarget(t *testing.T) {
	hiddenNavigations := make(chan string, 1)
	visibleNavigations := make(chan string, 1)
	hiddenServer := newCDPPageTestServer(t, false, hiddenNavigations)
	defer hiddenServer.Close()
	visibleServer := newCDPPageTestServer(t, true, visibleNavigations)
	defer visibleServer.Close()

	listServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]cdpTarget{
			{ID: "hidden", Type: "page", URL: "https://hidden.example", WebSocketDebuggerUrl: httpToWebSocketURL(hiddenServer.URL)},
			{ID: "visible", Type: "page", URL: "https://visible.example", WebSocketDebuggerUrl: httpToWebSocketURL(visibleServer.URL)},
		})
	}))
	defer listServer.Close()

	parsedURL, err := url.Parse(listServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(parsedURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	targetURL := "https://target.example/path"
	server := &APIServer{}
	if err := server.cdpNavigateDirect(port, targetURL); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-visibleNavigations:
		if got != targetURL {
			t.Fatalf("visible target navigated to %q, want %q", got, targetURL)
		}
	case <-time.After(time.Second):
		t.Fatal("visible target did not receive Page.navigate")
	}
	select {
	case got := <-hiddenNavigations:
		t.Fatalf("hidden target unexpectedly navigated to %q", got)
	default:
	}
}

func newCDPPageTestServer(t *testing.T, visible bool, navigations chan<- string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		var request struct {
			ID     int                    `json:"id"`
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			return
		}

		switch request.Method {
		case "Runtime.evaluate":
			_ = websocket.JSON.Send(ws, map[string]interface{}{
				"id": request.ID,
				"result": map[string]interface{}{
					"result": map[string]interface{}{"type": "boolean", "value": visible},
				},
			})
		case "Page.navigate":
			targetURL, _ := request.Params["url"].(string)
			navigations <- targetURL
			_ = websocket.JSON.Send(ws, map[string]interface{}{
				"id":     request.ID,
				"result": map[string]interface{}{"frameId": "frame-1"},
			})
		}
	}))
}

func httpToWebSocketURL(rawURL string) string {
	return "ws" + strings.TrimPrefix(rawURL, "http")
}
