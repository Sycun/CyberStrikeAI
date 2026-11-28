package webproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

func fixtureInstall(t *testing.T, db *database.DB) string {
	t.Helper()
	id := uuid.NewString()
	_, err := db.Exec(`INSERT INTO plugin_installs(id,plugin_id,source_identity,title,driver,observed_state,created_at,updated_at)
	VALUES(?,?,?,?,?,'healthy',?,?)`, id, id, id, id, "release", time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func issueFixture(t *testing.T, s *Service, parent, id string) Embed {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, parent+"/api/plugins/installs/"+id+"/embed-sessions", nil)
	request.Header.Set("Origin", parent)
	out, err := s.Issue(request, id, "platform-token", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func exchangeFixture(t *testing.T, client *http.Client, embed Embed, parent string, want int) *http.Cookie {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, embed.Action, strings.NewReader(url.Values{"ticket": {embed.Ticket}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", parent)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("exchange status=%d want=%d", resp.StatusCode, want)
	}
	if want != 303 {
		return nil
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != "/" {
		t.Fatal("missing protected plugin session cookie")
	}
	return cookies[0]
}

func TestPluginPortOriginsCookiesStreamsAndRevocation(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "proxy.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, b := fixtureInstall(t, db), fixtureInstall(t, db)
	var parent = "http://127.0.0.1:19000"
	var gen atomic.Int64
	gen.Store(1)
	showStream := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || strings.Contains(r.Header.Get("Cookie"), "platform-secret") {
			http.Error(w, "platform credential forwarded", 500)
			return
		}
		switch r.URL.Path {
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				typ, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if conn.WriteMessage(typ, msg) != nil {
					return
				}
			}
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-showStream:
				_, _ = io.WriteString(w, "data: second\n\n")
			}
		case "/_next/static/app.js":
			_, _ = io.WriteString(w, "fixture bundle")
		case "/api/set":
			http.SetCookie(w, &http.Cookie{Name: "app", Value: "jar-cookie", Path: "/", HttpOnly: true})
			w.WriteHeader(http.StatusNoContent)
		case "/api/get":
			cookie, _ := r.Cookie("app")
			if cookie != nil {
				_, _ = io.WriteString(w, cookie.Value)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	s := New(config.PluginProxyConfig{Mode: "port", PortPool: config.PluginPortPool{Start: 44100, End: 44300}}, db,
		func(context.Context, string) (Target, error) {
			return Target{Endpoint: upstream.URL, Route: "root", EntryPath: "/", Generation: gen.Load()}, nil
		},
		func(_ context.Context, id, token string) bool {
			return (id == a || id == b) && token == "platform-token"
		})
	s.Start("127.0.0.1", 19000, nil)
	defer s.Shutdown()
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	x, y := issueFixture(t, s, parent, a), issueFixture(t, s, parent, b)
	if x.Origin == y.Origin {
		t.Fatal("instances shared a browser origin")
	}
	ca := exchangeFixture(t, client, x, parent, 303)
	cb := exchangeFixture(t, client, y, parent, 303)
	exchangeFixture(t, client, x, parent, 403)
	get := func(origin, path string, cookie *http.Cookie, want int) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, origin+path, nil)
		req.AddCookie(cookie)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: "platform-secret"})
		req.Header.Set("Authorization", "Bearer platform-secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %d: %s", path, resp.StatusCode, body)
		}
		if len(resp.Cookies()) != 0 {
			t.Fatal("upstream cookie leaked to browser")
		}
		if want < 400 && !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors "+parent) {
			t.Fatal("wrong iframe ancestor policy")
		}
		return string(body)
	}
	get(y.Origin, "/api/get", ca, 401)
	get(x.Origin, "/api/set", ca, 204)
	if got := get(x.Origin, "/api/get", ca, 200); got != "jar-cookie" {
		t.Fatalf("lost backend cookie: %s", got)
	}
	if got := get(y.Origin, "/api/get", cb, 200); got != "" {
		t.Fatal("cookie jar crossed instances")
	}
	if got := get(x.Origin, "/_next/static/app.js", ca, 200); got != "fixture bundle" {
		t.Fatal("root asset was not proxied")
	}

	req, _ := http.NewRequest(http.MethodGet, x.Origin+"/events", nil)
	req.AddCookie(ca)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("SSE did not flush: %q %v", line, err)
	}
	resp.Body.Close()

	headers := http.Header{"Origin": {x.Origin}, "Cookie": {ca.String()}}
	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(x.Origin, "http")+"/ws", headers)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := socket.WriteMessage(websocket.TextMessage, []byte("fixture echo")); err != nil {
		t.Fatal(err)
	}
	_, message, err := socket.ReadMessage()
	if err != nil || string(message) != "fixture echo" {
		t.Fatalf("WS proxy: %s %v", message, err)
	}
	gen.Add(1)
	if _, _, err := socket.ReadMessage(); err == nil {
		t.Fatal("stale websocket remained active")
	}
	get(x.Origin, "/api/get", ca, 401)
	if err := s.Retire(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	saved, err := db.PluginWebOrigin(context.Background(), a)
	if err != nil || saved.Retired == nil {
		t.Fatal("retired port was not recorded")
	}
}

func TestPluginHostModeTLSAndRedirectPolicies(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "host.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := fixtureInstall(t, db)
	parent := "https://console.localhost"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			http.SetCookie(w, &http.Cookie{Name: "secure_app", Value: "secure-jar", Path: "/", Secure: true})
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/login":
			c, _ := r.Cookie("secure_app")
			if c != nil {
				_, _ = fmt.Fprint(w, c.Value)
			}
		default:
			http.Redirect(w, r, "https://outside.example/escape", http.StatusFound)
		}
	}))
	defer upstream.Close()
	s := New(config.PluginProxyConfig{Mode: "host", HostSuffix: "localhost", PublicOrigin: parent}, db,
		func(context.Context, string) (Target, error) {
			return Target{Endpoint: upstream.URL, Route: "relative_prefix", EntryPath: "/home", Generation: 7}, nil
		},
		func(context.Context, string, string) bool { return true })
	s.Start("127.0.0.1", 443, nil)
	defer s.Shutdown()
	main := httptest.NewTLSServer(s.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	defer main.Close()
	transport := main.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = main.Certificate().IPAddresses[0].String() // pin the loopback fixture certificate
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, main.Listener.Addr().String())
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	embed := issueFixture(t, s, parent, id)
	if embed.Origin != "https://p-"+id+".localhost" {
		t.Fatalf("host origin: %s", embed.Origin)
	}
	cookie := exchangeFixture(t, client, embed, parent, 303)
	if !cookie.Secure || !strings.HasPrefix(cookie.Name, "__Host-") || cookie.Domain != "" {
		t.Fatal("TLS cookie not scoped")
	}
	get := func(path string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodGet, embed.Origin+routePrefix(id, "relative_prefix")+path, nil)
		req.AddCookie(cookie)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if len(resp.Cookies()) != 0 {
			t.Fatal("backend cookie escaped server jar")
		}
		return resp.StatusCode, resp.Header.Get("Location"), string(body)
	}
	if status, loc, _ := get("/home"); status != 302 || loc != "/apps/"+id+"/login" {
		t.Fatalf("redirect: %d %q", status, loc)
	}
	if status, _, body := get("/login"); status != 200 || body != "secure-jar" {
		t.Fatalf("secure jar: %d %q", status, body)
	}
	if status, _, _ := get("/external"); status != 502 {
		t.Fatalf("external Location passed through: %d", status)
	}
}

func TestPluginBuildBasePathRedirectStaysWithinApplication(t *testing.T) {
	id := uuid.NewString()
	prefix := routePrefix(id, "build_base_path")
	target, _ := url.Parse("http://127.0.0.1:48000")
	request, _ := url.Parse(target.String() + prefix + "/home")
	if location, err := safeLocation(target.String()+prefix+"/login?next=home", request, target,
		"https://plugin.localhost:44320", prefix, "build_base_path"); err != nil || location != prefix+"/login?next=home" {
		t.Fatalf("location within application: %q %v", location, err)
	}
	if _, err := safeLocation("/outside-prefix", request, target, "https://plugin.localhost:44320", prefix, "build_base_path"); err == nil {
		t.Fatal("build-time prefix allowed an escaping redirect")
	}
}
