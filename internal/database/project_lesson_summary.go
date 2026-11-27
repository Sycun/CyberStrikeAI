package database

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/knowledge/taxonomy"
)

var lessonClassificationLabels = map[string]string{
	"access_control": "Access control", "broken_access_control": "Access control",
	"访问控制": "Access control", "越权": "Access control",
	"idor": "Object-level authorization", "bola": "Object-level authorization", "对象级越权": "Object-level authorization",
	"authentication": "Authentication", "身份认证": "Authentication",
	"authentication_bypass": "Authentication bypass", "认证绕过": "Authentication bypass",
	"sql_injection": "SQL injection", "sqli": "SQL injection", "sql注入": "SQL injection", "sql_注入": "SQL injection",
	"cross_site_scripting": "Cross-site scripting", "xss": "Cross-site scripting",
	"cross_site_request_forgery": "Cross-site request forgery", "csrf": "Cross-site request forgery",
	"server_side_request_forgery": "Server-side request forgery", "ssrf": "Server-side request forgery",
	"path_traversal": "Path traversal", "路径遍历": "Path traversal",
	"command_injection": "Command injection", "命令注入": "Command injection",
	"remote_code_execution": "Remote code execution", "rce": "Remote code execution",
	"deserialization": "Unsafe deserialization", "反序列化": "Unsafe deserialization",
	"information_disclosure": "Information disclosure", "信息泄露": "Information disclosure",
	"security_misconfiguration": "Security misconfiguration", "安全配置错误": "Security misconfiguration",
	"weak_password": "Weak credentials", "弱口令": "Weak credentials",
}

// Only known classification and severity values contribute display text.
// The finding title, description, target and evidence fields are not used.
func projectLessonSummary(vulnerabilityType, severity, outcome string) (classification, summary string) {
	class := fingerprintClass(vulnerabilityType, "")
	classification = lessonClassificationLabels[class]
	if classification == "" {
		code := strings.ToUpper(class)
		if strings.HasPrefix(code, "CWE-") && len(code) <= len("CWE-9999") {
			if pack, err := taxonomy.Default(); err == nil {
				if entry, ok := pack.Lookup("cwe", code); ok {
					classification = entry.ID
				}
			}
		}
	}
	if classification == "" {
		return "", ""
	}
	outcomeLabel := map[string]string{
		"confirmed":      "Confirmed",
		"fixed":          "Fixed",
		"false_positive": "False positive",
		"ignored":        "Ignored",
		"inconclusive":   "Inconclusive",
	}[outcome]
	if outcomeLabel == "" {
		return "", ""
	}
	summary = outcomeLabel + " · " + classification
	switch level := strings.ToLower(strings.TrimSpace(severity)); level {
	case "critical", "high", "medium", "low":
		summary += " · " + level + " severity"
	case "info", "informational":
		summary += " · informational"
	}
	return classification, clipEvidenceText(summary, 320)
}

func projectLessonDedupeKey(projectID, kind, cue, action string) string {
	return fmt.Sprintf("v2:%x", sha256.Sum256([]byte(strings.Join([]string{projectID, kind, cue, action}, "\x00"))))
}
