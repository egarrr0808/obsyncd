package diffmerge

import (
	"strings"
)

type hunk struct {
	start int
	end   int
	repl  []string
}

type Result struct {
	Content     string
	HasConflict bool
}

// Merge performs a conservative three-way line merge. Overlapping edits are
// preserved with Obsidian-hidden obsyncd conflict markers instead of being guessed.
func Merge(base, local, remote string) string {
	return MergeDetailed(base, local, remote).Content
}

func MergeDetailed(base, local, remote string) Result {
	if local == remote {
		return Result{Content: local}
	}
	if base == local {
		return Result{Content: remote}
	}
	if base == remote {
		return Result{Content: local}
	}

	eol := preferredEOL(local, remote, base)
	baseLines := splitLines(base)
	localLines := splitLines(local)
	remoteLines := splitLines(remote)

	localHunks := diffHunks(baseLines, localLines)
	remoteHunks := diffHunks(baseLines, remoteLines)
	if len(localHunks) == 0 {
		return Result{Content: remote}
	}
	if len(remoteHunks) == 0 {
		return Result{Content: local}
	}

	var out []string
	hasConflict := false
	pos := 0
	i, j := 0, 0
	for i < len(localHunks) || j < len(remoteHunks) {
		if i < len(localHunks) && j < len(remoteHunks) && sameInsert(localHunks[i], remoteHunks[j]) {
			h := localHunks[i]
			out = append(out, baseLines[pos:h.start]...)
			out = append(out, mergeInsert(localHunks[i].repl, remoteHunks[j].repl)...)
			pos = h.start
			i++
			j++
			continue
		}
		if j >= len(remoteHunks) || (i < len(localHunks) && localHunks[i].start <= remoteHunks[j].start) {
			h := localHunks[i]
			if j < len(remoteHunks) && hardCollision(h, remoteHunks[j]) {
				var lh, rh []hunk
				groupStart, groupEnd := h.start, h.end
				for i < len(localHunks) && hardTouches(localHunks[i], groupStart, groupEnd) {
					groupStart = min(groupStart, localHunks[i].start)
					groupEnd = max(groupEnd, localHunks[i].end)
					lh = append(lh, localHunks[i])
					i++
				}
				for j < len(remoteHunks) && hardTouches(remoteHunks[j], groupStart, groupEnd) {
					groupStart = min(groupStart, remoteHunks[j].start)
					groupEnd = max(groupEnd, remoteHunks[j].end)
					rh = append(rh, remoteHunks[j])
					j++
				}
				out = append(out, baseLines[pos:groupStart]...)
				left := renderSide(baseLines, lh, groupStart, groupEnd)
				right := renderSide(baseLines, rh, groupStart, groupEnd)
				if strings.Join(left, "") == strings.Join(right, "") {
					out = append(out, left...)
				} else {
					out = append(out, conflict(left, right, eol)...)
					hasConflict = true
				}
				pos = groupEnd
				continue
			}
			out = append(out, baseLines[pos:h.start]...)
			out = append(out, h.repl...)
			pos = h.end
			i++
			continue
		}

		h := remoteHunks[j]
		if i < len(localHunks) && hardCollision(h, localHunks[i]) {
			continue
		}
		out = append(out, baseLines[pos:h.start]...)
		out = append(out, h.repl...)
		pos = h.end
		j++
	}
	out = append(out, baseLines[pos:]...)
	return Result{Content: strings.Join(out, ""), HasConflict: hasConflict}
}

func diffHunks(a, b []string) []hunk {
	m, n := len(a), len(b)
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var hunks []hunk
	i, j := 0, 0
	for i < m || j < n {
		if i < m && j < n && a[i] == b[j] {
			i++
			j++
			continue
		}
		startI := i
		startJ := j
		for i < m || j < n {
			if i < m && j < n && a[i] == b[j] {
				break
			}
			if j >= n || (i < m && lcs[i+1][j] >= lcs[i][j+1]) {
				i++
			} else {
				j++
			}
		}
		hunks = append(hunks, hunk{start: startI, end: i, repl: append([]string(nil), b[startJ:j]...)})
	}
	return hunks
}

func renderSide(base []string, hs []hunk, start, end int) []string {
	var out []string
	pos := start
	for _, h := range hs {
		out = append(out, base[pos:h.start]...)
		out = append(out, h.repl...)
		pos = h.end
	}
	out = append(out, base[pos:end]...)
	return out
}

func conflict(local, remote []string, eol string) []string {
	out := []string{"%%OBSYNCD_CONFLICT_START%%" + eol, "%%OBSYNCD_LOCAL_START%%" + eol}
	out = append(out, ensureTerminated(local, eol)...)
	out = append(out, "%%OBSYNCD_LOCAL_END%%"+eol, "%%OBSYNCD_REMOTE_START%%"+eol)
	out = append(out, ensureTerminated(remote, eol)...)
	out = append(out, "%%OBSYNCD_REMOTE_END%%"+eol, "%%OBSYNCD_CONFLICT_END%%"+eol)
	return out
}

func ConflictBlock(local, remote, eol string) string {
	return strings.Join(conflict(splitLines(local), splitLines(remote), eol), "")
}

func ensureTerminated(lines []string, eol string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := append([]string(nil), lines...)
	if !strings.HasSuffix(out[len(out)-1], "\n") {
		out[len(out)-1] += eol
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if parts[len(parts)-1] == "" {
		return parts[:len(parts)-1]
	}
	return parts
}

func preferredEOL(values ...string) string {
	for _, v := range values {
		if strings.Contains(v, "\r\n") {
			return "\r\n"
		}
	}
	return "\n"
}

func sameInsert(a, b hunk) bool {
	return a.start == a.end && b.start == b.end && a.start == b.start
}

func hardCollision(a, b hunk) bool {
	if a.start == a.end || b.start == b.end {
		return false
	}
	return a.start < b.end && b.start < a.end
}

func hardTouches(h hunk, start, end int) bool {
	if h.start == h.end {
		return false
	}
	return h.start < end && start < h.end
}

func mergeInsert(local, remote []string) []string {
	if strings.Join(local, "") == strings.Join(remote, "") {
		return append([]string(nil), local...)
	}
	out := append([]string(nil), local...)
	return append(out, remote...)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
