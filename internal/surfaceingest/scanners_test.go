package surfaceingest

import (
	"strings"
	"testing"
)

func TestTypedScannersStayInScopeAndQuoteFreeText(t *testing.T) {
	seeds := []ScopeSeed{{Domain: "example.com"}, {IP: "203.0.113.10"}}
	excluded := []ScopeSeed{{Domain: "skip.example.com"}}
	nmap := `<?xml version="1.0"?><nmaprun>
<host><status state="up"/><address addr="203.0.113.10" addrtype="ipv4"/>
<ports><port protocol="tcp" portid="80"><state state="open"/><service name="http" product="nginx" banner="IGNORE-ME owned by evil"/></port></ports>
</host>
<host><status state="up"/><address addr="198.51.100.9" addrtype="ipv4"/>
<ports><port protocol="tcp" portid="22"><state state="open"/><service name="ssh"/></port></ports>
</host>
</nmaprun>`
	hits, truncated := Ingest(nmap, seeds, nil)
	if truncated {
		t.Fatal("small nmap was truncated")
	}
	if len(hits) != 1 || hits[0].IP != "203.0.113.10" {
		t.Fatalf("nmap scope: %+v", hits)
	}
	if strings.Contains(hits[0].Summary, "IGNORE-ME") || strings.Contains(hits[0].Domain, "evil") {
		t.Fatalf("banner leaked into summary: %+v", hits[0])
	}
	if !strings.HasPrefix(hits[0].Detail, `"`) || !strings.Contains(hits[0].Detail, "IGNORE-ME") {
		t.Fatalf("detail was not quoted: %s", hits[0].Detail)
	}

	nuclei := `{"template-id":"cve-2024-0001","info":{"name":"do not promote","severity":"high"},"host":"https://user:pass@app.example.com","matched-at":"https://user:pass@app.example.com/admin?token=1","extracted-results":["secret-cookie"]}`
	hits, _ = Ingest(nuclei, seeds, excluded)
	if len(hits) != 1 || hits[0].Domain != "app.example.com" {
		t.Fatalf("nuclei: %+v", hits)
	}
	if strings.Contains(hits[0].Summary, "user:pass") || strings.Contains(hits[0].Summary, "secret-cookie") || strings.Contains(hits[0].Summary, "do not promote") {
		t.Fatalf("nuclei free text leaked: %s", hits[0].Summary)
	}
	if !hasEndpoint(hits[0], "GET", "/admin") {
		t.Fatalf("nuclei path: %+v", hits[0].Endpoints)
	}

	httpx := "{\"url\":\"https://skip.example.com/secret\",\"status_code\":200,\"title\":\"<script>alert(1)</script>\"}\n{\"url\":\"https://api.example.com/v1\",\"status_code\":204,\"title\":\"internal\"}\n"
	hits, _ = Ingest(httpx, seeds, excluded)
	if len(hits) != 1 || hits[0].Domain != "api.example.com" {
		t.Fatalf("httpx exclusion failed: %+v", hits)
	}
	if strings.Contains(hits[0].Summary, "internal") || strings.Contains(hits[0].Summary, "script") {
		t.Fatalf("httpx title leaked: %s", hits[0].Summary)
	}

	ffuf := `{"commandline":"ffuf","results":[{"url":"https://app.example.com/backup","status":200,"length":12,"input":{"FUZZ":"backup"},"redirectlocation":"https://user:pass@app.example.com/next"}]}`
	hits, _ = Ingest(ffuf, seeds, nil)
	if len(hits) != 1 || !hasEndpoint(hits[0], "GET", "/backup") {
		t.Fatalf("ffuf: %+v", hits)
	}
	if strings.Contains(hits[0].Summary, "user:pass") || strings.Contains(hits[0].Detail, "user:pass") {
		t.Fatalf("ffuf kept credentials: summary=%s detail=%s", hits[0].Summary, hits[0].Detail)
	}
}

func TestProseStillUsesThePatternExtractor(t *testing.T) {
	hits, truncated := ExtractSurface("https://app.example.com/login\nhttps://other.example.net/admin", []ScopeSeed{{Domain: "example.com"}})
	if truncated || len(hits) != 1 || hits[0].Domain != "app.example.com" {
		t.Fatalf("prose extract: %+v truncated=%v", hits, truncated)
	}
}

func TestClaimSkipsTheSameReceiptAndKeepsLocalTools(t *testing.T) {
	ResetClaims()
	t.Cleanup(ResetClaims)
	if !Claim("exec-1") || Claim("exec-1") {
		t.Fatal("same execution receipt was claimed twice")
	}
	if !Claim("") || !Claim("") {
		t.Fatal("local tool output must stay eligible")
	}
}

func hasEndpoint(hit SurfaceHit, method, path string) bool {
	for _, endpoint := range hit.Endpoints {
		if endpoint.Method == method && endpoint.Path == path {
			return true
		}
	}
	return false
}
