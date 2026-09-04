package introspect

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// staticRule is one line-level pattern check.
type staticRule struct {
	substr   string
	severity Severity
	message  string
}

// pythonRules are checked against every non-comment line of every .py file.
// Order matters only for readability; all matching rules fire.
var pythonRules = []staticRule{
	// Dangerous calls — arbitrary code/command execution.
	{"eval(", SeverityCritical, "dangerous call: eval() executes arbitrary expressions"},
	{"exec(", SeverityCritical, "dangerous call: exec() executes arbitrary code"},
	{"os.system", SeverityCritical, "dangerous call: os.system() runs shell commands"},
	{"os.popen", SeverityCritical, "dangerous call: os.popen() runs shell commands"},
	{"subprocess.run", SeverityCritical, "dangerous call: subprocess.run() spawns processes"},
	{"subprocess.Popen", SeverityCritical, "dangerous call: subprocess.Popen() spawns processes"},
	{"subprocess.call", SeverityCritical, "dangerous call: subprocess.call() spawns processes"},
	{"subprocess.check_output", SeverityCritical, "dangerous call: subprocess.check_output() spawns processes"},
	{"__import__(", SeverityCritical, "dangerous call: __import__() loads modules dynamically"},
	{"ctypes.", SeverityCritical, "dangerous call: ctypes invokes native code"},
	// Suspicious imports — capability acquisition worth reviewing.
	{"import subprocess", SeverityWarning, "suspicious import: subprocess (process spawning)"},
	{"import socket", SeverityWarning, "suspicious import: socket (raw network access)"},
	{"from socket", SeverityWarning, "suspicious import: socket (raw network access)"},
	{"import ctypes", SeverityWarning, "suspicious import: ctypes (native code)"},
	{"from ctypes", SeverityWarning, "suspicious import: ctypes (native code)"},
	{"base64.b64decode", SeverityWarning, "obfuscation hint: base64-decoded payload"},
	// Path traversal.
	{"../", SeverityWarning, "path traversal attempt: relative parent path"},
	{"..\\", SeverityWarning, "path traversal attempt: relative parent path"},
}

// docRules run against documentation/manifest lines (SKILL.md, plugin.yaml,
// README.md). These catch only the blatant markers — the deeper semantic
// pass is the LLM audit.
var docRules = []staticRule{
	{"ignore previous instructions", SeverityWarning, "prompt injection marker: 'ignore previous instructions'"},
	{"ignore all previous instructions", SeverityWarning, "prompt injection marker: 'ignore all previous instructions'"},
	{"disregard the above", SeverityWarning, "prompt injection marker: 'disregard the above'"},
	{"disregard previous", SeverityWarning, "prompt injection marker: 'disregard previous'"},
	{"you are now", SeverityWarning, "prompt injection marker: role reassignment ('you are now …')"},
	{"do not tell the user", SeverityWarning, "prompt injection marker: concealment instruction"},
}

// docFiles are the package documents the doc rules apply to (basename match,
// case-insensitive).
var docFiles = map[string]bool{
	"skill.md":    true,
	"plugin.yaml": true,
	"plugin.yml":  true,
	"readme.md":   true,
}

// StaticScan walks dir and returns severity-tagged findings for Python
// sources and package documents. It never errors — unreadable files are
// reported as warnings so a scan can't be dodged with permission tricks.
func StaticScan(dir string) []Finding {
	var findings []Finding
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			findings = append(findings, Finding{
				Check: "static", Severity: SeverityWarning,
				Message: "unreadable entry during scan: " + err.Error(),
			})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			rel = path
		}
		base := strings.ToLower(filepath.Base(path))
		switch {
		case strings.HasSuffix(base, ".py"):
			findings = append(findings, scanFile(path, rel, pythonRules, true)...)
		case docFiles[base]:
			findings = append(findings, scanFile(path, rel, docRules, false)...)
		}
		return nil
	})
	return findings
}

// scanFile applies rules line by line. For Python files, comment lines
// (stripped prefix "#") are skipped so documentation can mention eval()
// without tripping the scanner.
func scanFile(path, rel string, rules []staticRule, skipComments bool) []Finding {
	f, err := os.Open(path)
	if err != nil {
		return []Finding{{
			Check: "static", Severity: SeverityWarning,
			File: rel, Message: "unreadable file during scan: " + err.Error(),
		}}
	}
	defer f.Close()

	var findings []Finding
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if skipComments && strings.HasPrefix(trimmed, "#") {
			continue
		}
		lower := strings.ToLower(line)
		for _, r := range rules {
			if strings.Contains(lower, strings.ToLower(r.substr)) {
				findings = append(findings, Finding{
					Check: "static", Severity: r.severity,
					File: rel, Line: lineNo, Message: r.message,
				})
			}
		}
	}
	return findings
}
