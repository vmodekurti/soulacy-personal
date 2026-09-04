// Package codeclass statically classifies the capabilities an inline Python
// "Custom Python" node (Studio) requires, by scanning its source for telltale
// imports and calls. The result drives the per-case consent model (§13 of
// docs/STUDIO_PYTHON_TOOLS.md): ReadOnly code (no matches) runs inside the
// guardrails without consent; anything that touches the system or the network,
// or uses dynamic execution the scanner can't see through, is "beyond
// guardrails" and must be consented case by case.
//
// This is deliberately a CONSERVATIVE GUARDRAIL, not a security boundary: it
// errs toward flagging (a `subprocess` mention in a comment still trips
// `system`), and it cannot see through `eval`/`exec`/`__import__`/`getattr`
// indirection — which is exactly why those set Dynamic, raising the warning.
// Real isolation (container/nsjail) is the actual boundary (design §5.5).
package codeclass

import (
	"regexp"
	"sort"
)

// Capability tokens a code node may require.
const (
	CapSystem  = "system"  // runs commands / writes files / touches the OS
	CapNetwork = "network" // opens sockets / makes outbound requests
)

// Result is the classification of one code blob.
type Result struct {
	// Requires is the sorted, de-duplicated set of capabilities the code needs
	// (subset of {system, network}). Empty = ReadOnly (inside guardrails).
	Requires []string
	// Dynamic is true when the code uses execution the static scanner cannot
	// follow (eval/exec/__import__/compile/importlib/pickle.loads/getattr).
	// Treated as needs-review: it raises the consent warning level.
	Dynamic bool
}

// Beyond reports whether the code requires anything beyond the ReadOnly
// guardrails (any capability, or dynamic execution).
func (r Result) Beyond() bool { return len(r.Requires) > 0 || r.Dynamic }

var (
	reNetwork = regexp.MustCompile(`\b(socket|ssl|http\.client|httplib|urllib|urllib2|urllib3|requests|httpx|aiohttp|websocket|websockets|ftplib|smtplib|imaplib|poplib|telnetlib|paramiko|grpc)\b`)
	reSystem  = regexp.MustCompile(`(\bsubprocess\b|\bos\.system\b|\bos\.popen\b|\bos\.exec[lv]|\bos\.spawn|\bos\.fork\b|\bpty\b|\bshutil\b|\bos\.remove\b|\bos\.unlink\b|\bos\.rmdir\b|\bos\.removedirs\b|\bos\.rename\b|\bos\.replace\b|\bos\.mkdir\b|\bos\.makedirs\b|\bos\.chmod\b|\bos\.chown\b|\bctypes\b|\bmmap\b|\bsignal\b)`)
	// open(path, 'w'|'a'|'x'|'wb'|…): a write/append/exclusive mode means a
	// filesystem mutation -> system.
	reWriteOpen = regexp.MustCompile(`open\s*\([^)]*,\s*['"][^'"]*[wax]`)
	reDynamic   = regexp.MustCompile(`(\beval\s*\(|\bexec\s*\(|\b__import__\s*\(|\bcompile\s*\(|\bimportlib\b|\bmarshal\b|\bpickle\.loads\b|\bgetattr\s*\()`)
)

// Classify scans Python source and returns its capability requirements.
func Classify(code string) Result {
	if code == "" {
		return Result{}
	}
	set := map[string]bool{}
	if reNetwork.MatchString(code) {
		set[CapNetwork] = true
	}
	if reSystem.MatchString(code) || reWriteOpen.MatchString(code) {
		set[CapSystem] = true
	}
	var req []string
	for c := range set {
		req = append(req, c)
	}
	sort.Strings(req)
	return Result{Requires: req, Dynamic: reDynamic.MatchString(code)}
}
