package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"cyberstrike-ai/internal/config"
)

const MaxArchiveBytes int64 = 512 << 20

type Egress struct {
	client *http.Client
}

// NewEgress constrains plugin metadata and artifact traffic to approved HTTPS
// authorities. The token for GitHub metadata is added by its separate inspector.
func NewEgress(cfg config.PluginEgress) (*Egress, error) {
	hosts := map[string]bool{
		"api.github.com": true, "github.com": true, "codeload.github.com": true,
		"release-assets.githubusercontent.com": true, "objects.githubusercontent.com": true,
		"registry-1.docker.io": true, "auth.docker.io": true, "ghcr.io": true,
		"pkg-containers.githubusercontent.com": true,
		"production.cloudfront.docker.com":     true, "production.cloudflare.docker.com": true,
		"registry.librechat.ai": true,
	}
	for _, host := range cfg.AdditionalHosts {
		hosts[host] = true
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	switch cfg.ProxyMode {
	case "direct":
		transport.Proxy = nil
	case "fixed":
		proxy, err := url.Parse(cfg.ProxyURL)
		if err != nil || proxy.Host == "" {
			return nil, fmt.Errorf("invalid plugin outbound proxy")
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	// Keep default dialing for the operator's proxy. Direct artifact connections
	// resolve once, reject non-public targets, and dial the validated address.
	direct := transport.Clone()
	direct.Proxy = nil
	dialer := net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}
	direct.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, address := range addrs {
			ip := address.Unmap()
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
				return nil, fmt.Errorf("non-public plugin egress address")
			}
		}
		for _, ip := range addrs {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
		}
		return nil, fmt.Errorf("plugin egress endpoint unavailable")
	}
	roundTrip := egressTransport{direct: direct, proxied: transport, hosts: hosts}
	return &Egress{client: &http.Client{
		Transport: roundTrip,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 4 || req.URL.Scheme != "https" || !hosts[req.URL.Host] {
				return fmt.Errorf("plugin egress redirect not approved")
			}
			req.Header.Del("Authorization")
			return nil
		},
	}}, nil
}

type egressTransport struct {
	direct, proxied *http.Transport
	hosts           map[string]bool
}

func (t egressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.User != nil || !t.hosts[req.URL.Host] {
		return nil, fmt.Errorf("plugin egress destination not approved")
	}
	if t.proxied.Proxy != nil {
		proxy, err := t.proxied.Proxy(req)
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			return t.proxied.RoundTrip(req)
		}
	}
	return t.direct.RoundTrip(req)
}

func (e *Egress) Transport() http.RoundTripper {
	return e.client.Transport
}

func (e *Egress) Download(ctx context.Context, address string, dst io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", fmt.Errorf("invalid artifact URL")
	}
	res, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("artifact download could not connect")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.ContentLength > MaxArchiveBytes {
		return "", fmt.Errorf("artifact response rejected (HTTP %d)", res.StatusCode)
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, sum), io.LimitReader(res.Body, MaxArchiveBytes+1))
	if err != nil || n == 0 || n > MaxArchiveBytes {
		return "", fmt.Errorf("artifact size or transfer invalid")
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func normalizeRegistry(repository string) (host, path string) {
	first, rest, split := strings.Cut(repository, "/")
	if split && (strings.ContainsAny(first, ".:") || first == "localhost") {
		if first == "docker.io" || first == "index.docker.io" {
			return "registry-1.docker.io", rest
		}
		return first, rest
	}
	if split {
		return "registry-1.docker.io", repository
	}
	return "registry-1.docker.io", "library/" + repository
}
