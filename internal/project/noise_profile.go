package project

import "fmt"

type noiseSpec struct {
	rps        int
	concurrent int
	nmapTiming string
	nmapRetry  int
	sqlDelay   string
	summary    string
}

var noiseSpecs = map[string]noiseSpec{
	"quiet": {
		rps: 2, concurrent: 1, nmapTiming: "-T2", nmapRetry: 2, sqlDelay: "0.5",
		summary: "优先使用已有证据。网络请求串行进行。",
	},
	"balanced": {
		rps: 5, concurrent: 2, nmapTiming: "-T3", nmapRetry: 2, sqlDelay: "0.2",
		summary: "遵守项目明确批准的请求速率和并发上限。",
	},
	"aggressive": {
		rps: 20, concurrent: 5, nmapTiming: "-T4", nmapRetry: 1, sqlDelay: "0.05",
		summary: "仅在批准的维护窗口内操作，并遵守批准的请求速率和并发上限。",
	},
}

// ValidNoiseProfile reports whether profile is a supported setting (empty or off means disabled).
func ValidNoiseProfile(profile string) bool {
	if profile == "" || profile == "off" {
		return true
	}
	_, ok := noiseSpecs[profile]
	return ok
}

// BuildNoiseProfileBlock renders deterministic rate and concurrency flags for supported scanners.
// Empty or "off" returns an empty string so legacy prompts stay byte-identical.
func BuildNoiseProfileBlock(profile string) string {
	spec, ok := noiseSpecs[profile]
	if !ok {
		return ""
	}
	return fmt.Sprintf("\n[Noise Profile] %s\n%s\n"+
		"调用扫描与探测工具时严格按以下速率上限传参（单主机上限 %d RPS、最多 %d 并发）：\n"+
		"- nmap: %s --max-rate %d --max-retries %d\n"+
		"- nuclei: -rate-limit %d -c %d\n"+
		"- httpx: -rl %d -t %d\n"+
		"- katana: -rl %d -c %d\n"+
		"- ffuf: -rate %d -t %d\n"+
		"- dalfox: --rate-limit %d --workers %d\n"+
		"- sqlmap: --delay=%s --threads=1\n"+
		"遇到 HTTP 429 或 503 时暂停请求，遵守 Retry-After，持续异常时停止并反馈。\n",
		profile, spec.summary, spec.rps, spec.concurrent,
		spec.nmapTiming, spec.rps, spec.nmapRetry,
		spec.rps, spec.concurrent,
		spec.rps, spec.concurrent,
		spec.rps, spec.concurrent,
		spec.rps, spec.concurrent,
		spec.rps, spec.concurrent,
		spec.sqlDelay,
	)
}

func buildNoiseProfileBlock(profile string) string {
	return BuildNoiseProfileBlock(profile)
}
