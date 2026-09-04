// diff.go — a small, dependency-free line diff used to preview what a repair
// (or any workflow edit) changes in the SOUL.yaml before the user saves it.
//
// Output is a simple unified-style listing: unchanged lines prefixed with a
// space, removals with "-", additions with "+". It is pure and unit-tested.
package studio

import "strings"

// DiffLine is one line of a rendered diff.
type DiffLine struct {
	Op   string `json:"op"`   // " " (context), "-" (removed), "+" (added)
	Text string `json:"text"` // the line content (without trailing newline)
}

// DiffStats summarizes a diff.
type DiffStats struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// DiffYAML computes a line-level diff between two texts (typically two SOUL.yaml
// serializations) using a longest-common-subsequence backtrace, and returns both
// the structured lines and a plain unified-style string.
func DiffYAML(before, after string) ([]DiffLine, DiffStats, string) {
	a := splitLines(before)
	b := splitLines(after)
	lines := lcsDiff(a, b)

	var stats DiffStats
	var sb strings.Builder
	for _, l := range lines {
		switch l.Op {
		case "+":
			stats.Added++
		case "-":
			stats.Removed++
		}
		sb.WriteString(l.Op)
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return lines, stats, sb.String()
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// lcsDiff produces a diff by computing the longest common subsequence of a and b
// via the classic dynamic-programming table, then backtracking.
func lcsDiff(a, b []string) []DiffLine {
	n, m := len(a), len(b)
	// dp[i][j] = LCS length of a[i:] and b[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var out []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{Op: " ", Text: a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, DiffLine{Op: "-", Text: a[i]})
			i++
		default:
			out = append(out, DiffLine{Op: "+", Text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{Op: "-", Text: a[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{Op: "+", Text: b[j]})
	}
	return out
}
