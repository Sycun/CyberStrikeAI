package surfaceingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// parseTypedSurface recognizes nmap XML, nuclei JSONL, httpx JSON/JSONL and
// ffuf JSON before the free-text fallback. ok is false when the text is not
// one of those documents, so prose still uses the existing extractor.
// A recognized document does not fall through, even when every host is out of scope.
func parseTypedSurface(text string, scope surfaceScope) ([]SurfaceHit, bool, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false, false
	}
	switch {
	case strings.Contains(trimmed, "<nmaprun"):
		hits, truncated := readNmapXML(strings.NewReader(trimmed), scope)
		return hits, truncated, true
	case looksLikeFfuf(trimmed):
		hits, truncated := readFfuf(strings.NewReader(trimmed), scope)
		return hits, truncated, true
	case firstJSONLineKind(trimmed) == "nuclei":
		hits, truncated := readJSONL(strings.NewReader(trimmed), scope, "nuclei")
		return hits, truncated, true
	case firstJSONLineKind(trimmed) == "httpx":
		hits, truncated := readJSONL(strings.NewReader(trimmed), scope, "httpx")
		return hits, truncated, true
	default:
		return nil, false, false
	}
}

func looksLikeFfuf(text string) bool {
	var doc struct {
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal([]byte(text), &doc) != nil || len(doc.Results) == 0 {
		return false
	}
	var row struct {
		URL string `json:"url"`
	}
	return json.Unmarshal(doc.Results[0], &row) == nil && strings.TrimSpace(row.URL) != ""
}

func firstJSONLineKind(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			if line != "" {
				return ""
			}
			continue
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &probe) != nil {
			return ""
		}
		if _, ok := probe["template-id"]; ok {
			return "nuclei"
		}
		if _, ok := probe["templateID"]; ok {
			return "nuclei"
		}
		if _, ok := probe["status_code"]; ok {
			if _, hasURL := probe["url"]; hasURL {
				return "httpx"
			}
		}
		return ""
	}
	return ""
}

func (b *surfaceBuilder) note(hit *SurfaceHit, summary string, detail any) {
	if hit == nil {
		return
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = []byte(`{}`)
	}
	quoted := strconv.Quote(string(raw))
	if hit.Summary == "" {
		hit.Summary = summary
	}
	if hit.Detail == "" {
		hit.Detail = quoted
	}
}

type nmapHost struct {
	Status struct {
		State string `xml:"state,attr"`
	} `xml:"status"`
	Addrs []struct {
		Addr string `xml:"addr,attr"`
		Type string `xml:"addrtype,attr"`
	} `xml:"address"`
	Names []struct {
		Name string `xml:"name,attr"`
		Type string `xml:"type,attr"`
	} `xml:"hostnames>hostname"`
	Ports []struct {
		Proto string `xml:"protocol,attr"`
		ID    string `xml:"portid,attr"`
		State struct {
			State string `xml:"state,attr"`
		} `xml:"state"`
		Svc struct {
			Name    string `xml:"name,attr"`
			Product string `xml:"product,attr"`
			Version string `xml:"version,attr"`
			Tunnel  string `xml:"tunnel,attr"`
			Banner  string `xml:"banner,attr"`
		} `xml:"service"`
	} `xml:"ports>port"`
}

func readNmapXML(r io.Reader, scope surfaceScope) ([]SurfaceHit, bool) {
	dec := xml.NewDecoder(r)
	builder := &surfaceBuilder{scope: scope, hits: map[string]*SurfaceHit{}}
	root := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if !root {
				return nil, false
			}
			return builder.finish(), builder.truncated
		}
		if err != nil {
			builder.truncated = true
			if !root {
				return nil, false
			}
			return builder.finish(), true
		}
		se, ok := tok.(xml.StartElement)
		root = root || (ok && se.Name.Local == "nmaprun")
		if !ok || se.Name.Local != "host" {
			continue
		}
		var host nmapHost
		if err = dec.DecodeElement(&host, &se); err != nil {
			builder.truncated = true
			return builder.finish(), true
		}
		addNmapHost(builder, host)
	}
}

func addNmapHost(b *surfaceBuilder, host nmapHost) {
	if host.Status.State != "up" {
		return
	}
	ip := ""
	for _, addr := range host.Addrs {
		if ip == "" && (addr.Type == "ipv4" || addr.Type == "ipv6") && net.ParseIP(addr.Addr) != nil {
			ip = net.ParseIP(addr.Addr).String()
		}
	}
	allowedIP := ""
	if ip != "" {
		if got, _, ok := b.scope.allows(ip); ok {
			allowedIP = got
		}
	}
	domain := ""
	for _, name := range host.Names {
		if name.Type != "user" {
			continue
		}
		if _, got, ok := b.scope.allows(name.Name); ok && got != "" {
			domain = got
			break
		}
	}
	if allowedIP == "" && domain == "" {
		return
	}
	label := ip
	if label == "" {
		label = domain
	}
	var ports []map[string]any
	for _, port := range host.Ports {
		number := atoiPort(port.ID)
		if number <= 0 || number > 65535 || port.State.State != "open" {
			continue
		}
		if port.Proto != "tcp" && port.Proto != "udp" && port.Proto != "sctp" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(port.Svc.Name))
		scheme := ""
		switch name {
		case "http", "http-alt", "http-proxy":
			scheme = "http"
			if port.Svc.Tunnel == "ssl" {
				scheme = "https"
			}
		case "https", "https-alt":
			scheme = "https"
		}
		if scheme == "" && port.Proto == "tcp" && (number == 80 || number == 443) {
			if number == 443 {
				scheme = "https"
			} else {
				scheme = "http"
			}
		}
		protocol := scheme
		if protocol == "" {
			protocol = port.Proto
		}
		hit := b.add(allowedIP, domain, protocol, number)
		ports = append(ports, map[string]any{"port": fmt.Sprintf("%d/%s", number, port.Proto), "service": name})
		b.note(hit, fmt.Sprintf("nmap: %s %d/%s %s", label, number, port.Proto, name), map[string]any{
			"product": port.Svc.Product, "version": port.Svc.Version, "banner": port.Svc.Banner,
		})
	}
	if len(ports) == 0 {
		hit := b.add(allowedIP, domain, "", 0)
		b.note(hit, "nmap: host "+label, map[string]any{"names": host.Names})
	}
}

func readJSONL(r io.Reader, scope surfaceScope, kind string) ([]SurfaceHit, bool) {
	builder := &surfaceBuilder{scope: scope, hits: map[string]*SurfaceHit{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if kind == "nuclei" {
			addNuclei(builder, line)
		} else {
			addHttpx(builder, line)
		}
	}
	if scanner.Err() != nil {
		builder.truncated = true
	}
	return builder.finish(), builder.truncated
}

func addNuclei(b *surfaceBuilder, line []byte) {
	var row struct {
		TemplateID string `json:"template-id"`
		Template   string `json:"templateID"`
		Info       struct {
			Name     string `json:"name"`
			Severity string `json:"severity"`
		} `json:"info"`
		Host      string   `json:"host"`
		MatchedAt string   `json:"matched-at"`
		Extracted []string `json:"extracted-results"`
		Matcher   string   `json:"matcher-name"`
	}
	if json.Unmarshal(line, &row) != nil {
		return
	}
	template := row.TemplateID
	if template == "" {
		template = row.Template
	}
	template = strings.TrimSpace(template)
	raw := row.MatchedAt
	if strings.TrimSpace(raw) == "" {
		raw = row.Host
	}
	parsed, host := scanURL(raw)
	if template == "" || host == "" {
		return
	}
	ip, domain, ok := b.scope.allows(host)
	if !ok {
		return
	}
	port := 0
	scheme := ""
	path := ""
	if parsed != nil {
		port = surfacePort(parsed)
		scheme = parsed.Scheme
		path = cleanSurfacePath(parsed.EscapedPath())
	}
	hit := b.add(ip, domain, scheme, port)
	if path != "" && path != "/" {
		b.addEndpoint(hit, "GET", path)
	}
	severity := strings.ToLower(strings.TrimSpace(row.Info.Severity))
	switch severity {
	case "info", "low", "medium", "high", "critical":
	default:
		severity = "unknown"
	}
	b.note(hit, fmt.Sprintf("nuclei: template %s (severity %s) matched on %s; unverified scanner result", template, severity, host), map[string]any{
		"name": row.Info.Name, "host": stripUserinfo(row.Host), "matched_at": stripUserinfo(row.MatchedAt),
		"matcher_name": row.Matcher, "extracted_results": row.Extracted,
	})
}

func addHttpx(b *surfaceBuilder, line []byte) {
	var row struct {
		URL      string   `json:"url"`
		Status   int      `json:"status_code"`
		Title    string   `json:"title"`
		Tech     []string `json:"tech"`
		Server   string   `json:"webserver"`
		Host     string   `json:"host"`
		IP       string   `json:"ip"`
		Location string   `json:"location"`
	}
	if json.Unmarshal(line, &row) != nil {
		return
	}
	parsed, host := scanURL(row.URL)
	if parsed == nil || host == "" {
		return
	}
	ip, domain, ok := b.scope.allows(host)
	if !ok {
		return
	}
	if parsedIP := net.ParseIP(strings.TrimSpace(row.IP)); parsedIP != nil {
		if allowed, _, ipOK := b.scope.allows(parsedIP.String()); ipOK && ip == "" {
			ip = allowed
		}
	}
	hit := b.add(ip, domain, parsed.Scheme, surfacePort(parsed))
	if path := cleanSurfacePath(parsed.EscapedPath()); path != "" && path != "/" {
		b.addEndpoint(hit, "GET", path)
	}
	b.note(hit, fmt.Sprintf("httpx: %s responded with status %d", stripUserinfo(parsed.String()), row.Status), map[string]any{
		"title": row.Title, "tech": row.Tech, "webserver": row.Server, "host": stripUserinfo(row.Host), "location": stripUserinfo(row.Location),
	})
}

func readFfuf(r io.Reader, scope surfaceScope) ([]SurfaceHit, bool) {
	dec := json.NewDecoder(r)
	builder := &surfaceBuilder{scope: scope, hits: map[string]*SurfaceHit{}}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			builder.truncated = true
			break
		}
		if key != "results" {
			var skip json.RawMessage
			if err = dec.Decode(&skip); err != nil {
				builder.truncated = true
				break
			}
			continue
		}
		if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
			builder.truncated = true
			break
		}
		for dec.More() {
			var raw json.RawMessage
			if err = dec.Decode(&raw); err != nil {
				builder.truncated = true
				break
			}
			addFfuf(builder, raw)
		}
	}
	return builder.finish(), builder.truncated
}

func addFfuf(b *surfaceBuilder, raw json.RawMessage) {
	var row struct {
		Status   int             `json:"status"`
		Length   int64           `json:"length"`
		URL      string          `json:"url"`
		Redirect string          `json:"redirectlocation"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &row) != nil {
		return
	}
	parsed, host := scanURL(row.URL)
	if parsed == nil || host == "" {
		return
	}
	ip, domain, ok := b.scope.allows(host)
	if !ok {
		return
	}
	hit := b.add(ip, domain, parsed.Scheme, surfacePort(parsed))
	if path := cleanSurfacePath(parsed.EscapedPath()); path != "" && path != "/" {
		b.addEndpoint(hit, "GET", path)
	}
	b.note(hit, fmt.Sprintf("ffuf: %s returned status %d, %d bytes", stripUserinfo(parsed.String()), row.Status, row.Length), map[string]any{
		"input": row.Input, "redirectlocation": stripUserinfo(row.Redirect),
	})
}

func scanURL(raw string) (*url.URL, string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, ""
	}
	parsed.User = nil
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if ip := net.ParseIP(host); ip != nil {
		return parsed, ip.String()
	}
	return parsed, host
}

func stripUserinfo(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		parsed.User = nil
		return parsed.String()
	}
	return raw
}
