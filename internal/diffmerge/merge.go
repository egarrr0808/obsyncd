package diffmerge

import (
	"strings"
)

type hunk struct {
	start int
	end   int
	repl  []string
}

// Merge performs a conservative three-way line merge. Overlapping edits are
// preserved with Obsidian-hidden obsyncd conflict markers instead of being guessed.
func Merge(base, local, remote string) string {
	if local == remote {
		return local
	}
	if base == local {
		return remote
	}
	if base == remote {
		return local
	}

	eol := preferredEOL(local, remote, base)
	baseLines := splitLines(base)
	localLines := splitLines(local)
	remoteLines := splitLines(remote)

	localHunks := diffHunks(baseLines, localLines)
	remoteHunks := diffHunks(baseLines, remoteLines)
	if len(localHunks) == 0 {
		return remote
	}
	if len(remoteHunks) == 0 {
		return local
	}

	var out []string
	pos := 0
	i, j := 0, 0
	for i < len(localHunks) || j < len(remoteHunks) {
		if j >= len(remoteHunks) || (i < len(localHunks) && localHunks[i].start <= remoteHunks[j].start) {
			h := localHunks[i]
			if j < len(remoteHunks) && overlapsOrSameInsert(h, remoteHunks[j]) {
				var lh, rh []hunk
				groupStart, groupEnd := h.start, h.end
				for i < len(localHunks) && touches(localHunks[i], groupStart, groupEnd) {
					groupStart = min(groupStart, localHunks[i].start)
					groupEnd = max(groupEnd, localHunks[i].end)
					lh = append(lh, localHunks[i])
					i++
				}
				for j < len(remoteHunks) && touches(remoteHunks[j], groupStart, groupEnd) {
					groupStart = min(groupStart, remoteHunks[j].start)
					groupEnd = max(groupEnd, remoteHunks[j].end)
					rh = append(rh, remoteHunks[j])
					j++
				}
				out = append(out, baseLines[pos:groupStart]...)
				out = append(out, conflict(renderSide(baseLines, lh, groupStart, groupEnd), renderSide(baseLines, rh, groupStart, groupEnd), eol)...)
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
		if i < len(localHunks) && overlapsOrSameInsert(h, localHunks[i]) {
			continue
		}
		out = append(out, baseLines[pos:h.start]...)
		out = append(out, h.repl...)
		pos = h.end
		j++
	}
	out = append(out, baseLines[pos:]...)
	return strings.Join(out, "")
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

func overlapsOrSameInsert(a, b hunk) bool {
	if a.start == a.end && b.start == b.end {
		return a.start == b.start
	}
	return a.start < b.end && b.start < a.end
}

func touches(h hunk, start, end int) bool {
	if h.start == h.end && start == end {
		return h.start == start
	}
	return h.start <= end && start <= h.end
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
