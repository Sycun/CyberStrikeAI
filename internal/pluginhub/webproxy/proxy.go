package webproxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"
)

func routePrefix(id, route string) string {
	if route == "build_base_path" || route == "relative_prefix" {
		return "/apps/" + id
	}
	return ""
}

var appTransport = &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 45 * time.Second, ResponseHeaderTimeout: 30 * time.Second}

func (s *Service) Handler(id string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isReady() {
			http.Error(w, "plugin listener unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == SessionPath && r.URL.RawPath == "" {
			s.exchange(w, r, id)
			return
		}
		if !safePath(r.URL) || strings.HasPrefix(r.URL.Path, "/_csai_plugin/") || r.Header.Get("Service-Worker") != "" ||
			r.Header.Get("Sec-Fetch-Dest") == "serviceworker" || r.Method == http.MethodConnect || r.Method == http.MethodTrace {
			http.NotFound(w, r)
			return
		}
		session := s.session(r, id)
		if session == nil || !s.authorize(r.Context(), id, session.mainToken) {
			http.Error(w, "plugin session required", http.StatusUnauthorized)
			return
		}
		ws := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead
		if !requestOrigin(r, session.pluginOrigin, ws || unsafe) {
			http.Error(w, "plugin request origin rejected", http.StatusForbidden)
			return
		}
		target, err := s.resolve(r.Context(), id)
		if err != nil || target.Generation != session.generation {
			http.Error(w, "plugin session expired", http.StatusUnauthorized)
			return
		}
		endpoint, err := loopbackEndpoint(target.Endpoint)
		if err != nil {
			http.Error(w, "invalid plugin endpoint", http.StatusBadGateway)
			return
		}
		prefix := routePrefix(id, target.Route)
		upstreamPath := r.URL.Path
		if prefix != "" {
			if r.URL.Path == prefix {
				http.Redirect(w, r, prefix+"/", http.StatusTemporaryRedirect)
				return
			}
			if !strings.HasPrefix(r.URL.Path, prefix+"/") {
				http.NotFound(w, r)
				return
			}
			if target.Route == "relative_prefix" {
				upstreamPath = strings.TrimPrefix(upstreamPath, prefix)
			}
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(session.ctx, cancel)
		defer stop()
		// Streams and upgraded connections are fenced even if a role or the
		// application generation changes after the request has been admitted.
		go s.watch(ctx, cancel, id, session)
		proxy := s.reverseProxy(endpoint, upstreamPath, prefix, target, session, r)
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Service) session(r *http.Request, id string) *browserSession {
	for _, secure := range []bool{true, false} {
		name := cookieName(id, secure)
		var value string
		for _, cookie := range r.CookiesNamed(name) {
			if value != "" {
				return nil
			}
			value = cookie.Value
		}
		if len(value) != 43 {
			continue
		}
		hash := sha256.Sum256([]byte(value))
		s.mu.Lock()
		s.prune(time.Now())
		session := s.sessions[hash]
		s.mu.Unlock()
		if session != nil && session.install == id &&
			secure == strings.HasPrefix(session.pluginOrigin, "https://") && matchesOrigin(r, session.pluginOrigin) {
			return session
		}
	}
	return nil
}

func (s *Service) watch(ctx context.Context, cancel context.CancelFunc, id string, session *browserSession) {
	tick := time.NewTicker(350 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !s.authorize(ctx, id, session.mainToken) {
				cancel()
				return
			}
			target, err := s.resolve(ctx, id)
			if err != nil || target.Generation != session.generation {
				cancel()
				return
			}
		}
	}
}

func (s *Service) reverseProxy(endpoint *url.URL, upstreamPath, prefix string, target Target, session *browserSession, r *http.Request) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:     appTransport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path, pr.Out.URL.RawPath = upstreamPath, ""
			pr.SetURL(endpoint)
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Proxy-Authorization")
			if bearer := s.applicationBearer(r, target.ApplicationBearer, upstreamPath, session.mainToken); bearer != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+bearer)
			}
			for name := range pr.Out.Header {
				lower := strings.ToLower(name)
				if strings.HasPrefix(lower, "x-csai-") || lower == "x-real-ip" || lower == "x-api-key" {
					pr.Out.Header.Del(name)
				}
			}
			for _, cookie := range session.jar.Cookies(jarURL(pr.Out.URL, session.pluginOrigin)) {
				pr.Out.AddCookie(cookie)
			}
			// Scope the forwarded authority to this validated plugin request.
			pr.Out.Header.Set("X-Forwarded-Host", r.Host)
			pr.Out.Header.Set("X-Forwarded-Proto", strings.SplitN(session.pluginOrigin, ":", 2)[0])
			if prefix != "" {
				pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			session.jar.SetCookies(jarURL(resp.Request.URL, session.pluginOrigin), resp.Cookies())
			resp.Header.Del("Set-Cookie")
			for _, name := range []string{"X-Frame-Options", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Headers", "Access-Control-Allow-Methods", "Report-To", "Reporting-Endpoints", "Clear-Site-Data", "Refresh"} {
				resp.Header.Del(name)
			}
			if loc := resp.Header.Get("Location"); loc != "" {
				rewritten, err := safeLocation(loc, resp.Request.URL, endpoint, session.pluginOrigin, prefix, target.Route)
				if err != nil {
					return err
				}
				resp.Header.Set("Location", rewritten)
			}
			// Multiple CSP policies are intersected. Remove only the upstream
			// framing directive since the outer application owns embedding.
			policies := resp.Header.Values("Content-Security-Policy")
			resp.Header.Del("Content-Security-Policy")
			for _, p := range policies {
				var kept []string
				for d := range strings.SplitSeq(p, ";") {
					d = strings.TrimSpace(d)
					fields := strings.Fields(d)
					if len(fields) > 0 && !strings.EqualFold(fields[0], "frame-ancestors") {
						kept = append(kept, d)
					}
				}
				if len(kept) > 0 {
					resp.Header.Add("Content-Security-Policy", strings.Join(kept, "; "))
				}
			}
			resp.Header.Add("Content-Security-Policy", "frame-ancestors "+session.parentOrigin+"; object-src 'none'")
			resp.Header.Set("Cache-Control", "no-store")
			resp.Header.Set("Cross-Origin-Resource-Policy", "same-origin")
			resp.Header.Set("X-Content-Type-Options", "nosniff")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "plugin upstream unavailable", http.StatusBadGateway)
		},
	}
}

func (s *Service) applicationBearer(r *http.Request, allowed []string, path, primaryToken string) string {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return ""
	}
	scheme, token, ok := strings.Cut(headers[0], " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || token == primaryToken ||
		s.isPlatformToken != nil && s.isPlatformToken(token) {
		return ""
	}
	for _, prefix := range allowed {
		if path == prefix || prefix != "/" && strings.HasPrefix(path, strings.TrimRight(prefix, "/")+"/") {
			return token
		}
	}
	return ""
}

func jarURL(upstream *url.URL, browserOrigin string) *url.URL {
	public, _ := url.Parse(browserOrigin)
	copy := *upstream
	copy.Scheme, copy.Host = public.Scheme, public.Host
	return &copy
}

func safePath(u *url.URL) bool {
	escaped := strings.ToLower(u.EscapedPath())
	if !strings.HasPrefix(u.Path, "/") || strings.ContainsAny(u.Path, "\\\x00") || strings.HasPrefix(u.Path, "//") ||
		strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%25") {
		return false
	}
	clean := path.Clean(u.Path)
	return clean == u.Path || clean+"/" == u.Path
}

func loopbackEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Port() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("invalid endpoint")
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() {
		return nil, fmt.Errorf("endpoint is not a managed loopback address")
	}
	u.Path = ""
	return u, nil
}

func safeLocation(raw string, requestURL, upstream *url.URL, browserOrigin, prefix, route string) (string, error) {
	rel, err := url.Parse(raw)
	if err != nil || rel.User != nil || rel.Opaque != "" || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") {
		return "", fmt.Errorf("invalid redirect")
	}
	target := requestURL.ResolveReference(rel)
	public, _ := url.Parse(browserOrigin)
	if target.Host != upstream.Host && !sameAuthority(target, public) {
		return "", fmt.Errorf("non-plugin redirect")
	}
	if target.Scheme != "http" && target.Scheme != "https" || !safePath(target) {
		return "", fmt.Errorf("invalid redirect target")
	}
	result := target.EscapedPath()
	if prefix != "" && result != prefix && !strings.HasPrefix(result, prefix+"/") {
		if route != "relative_prefix" {
			return "", fmt.Errorf("redirect escaped application base path")
		}
		result = prefix + result
	}
	if strings.HasPrefix(result, "/_csai_plugin/") {
		return "", fmt.Errorf("reserved redirect")
	}
	if target.RawQuery != "" {
		result += "?" + target.RawQuery
	}
	if target.Fragment != "" {
		result += "#" + target.EscapedFragment()
	}
	return result, nil
}

func sameAuthority(a, b *url.URL) bool {
	host := func(u *url.URL) string {
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	}
	return a.Scheme == b.Scheme && host(a) == host(b)
}
