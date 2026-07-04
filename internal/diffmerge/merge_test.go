package diffmerge

import (
	"strings"
	"testing"
)

func TestMergeTrivialSides(t *testing.T) {
	if got := Merge("a\n", "a\n", "b\n"); got != "b\n" {
		t.Fatalf("remote-only merge = %q", got)
	}
	if got := Merge("a\n", "b\n", "a\n"); got != "b\n" {
		t.Fatalf("local-only merge = %q", got)
	}
	if got := Merge("a\n", "b\n", "b\n"); got != "b\n" {
		t.Fatalf("same edit merge = %q", got)
	}
}

func TestMergeNonOverlapping(t *testing.T) {
	base := "title\nbody\nend\n"
	local := "title local\nbody\nend\n"
	remote := "title\nbody\nend remote\n"
	want := "title local\nbody\nend remote\n"
	if got := Merge(base, local, remote); got != want {
		t.Fatalf("merge = %q want %q", got, want)
	}
}

func TestMergeOverlapConflict(t *testing.T) {
	got := Merge("a\nb\nc\n", "a\nlocal\nc\n", "a\nremote\nc\n")
	for _, part := range []string{
		"%%OBSYNCD_CONFLICT_START%%\n",
		"%%OBSYNCD_LOCAL_START%%\n",
		"local\n",
		"%%OBSYNCD_LOCAL_END%%\n",
		"%%OBSYNCD_REMOTE_START%%\n",
		"remote\n",
		"%%OBSYNCD_REMOTE_END%%\n",
		"%%OBSYNCD_CONFLICT_END%%\n",
	} {
		if !strings.Contains(got, part) {
			t.Fatalf("merge missing %q in %q", part, got)
		}
	}
}

func TestMergeOverlapConflictExactSyntax(t *testing.T) {
	got := Merge("base\n", "local\n", "remote\n")
	want := "" +
		"%%OBSYNCD_CONFLICT_START%%\n" +
		"%%OBSYNCD_LOCAL_START%%\n" +
		"local\n" +
		"%%OBSYNCD_LOCAL_END%%\n" +
		"%%OBSYNCD_REMOTE_START%%\n" +
		"remote\n" +
		"%%OBSYNCD_REMOTE_END%%\n" +
		"%%OBSYNCD_CONFLICT_END%%\n"
	if got != want {
		t.Fatalf("merge = %q want %q", got, want)
	}
}

func TestMergePreservesCRLFMarkers(t *testing.T) {
	got := Merge("a\r\nb\r\n", "a\r\nlocal\r\n", "a\r\nremote\r\n")
	if !strings.Contains(got, "%%OBSYNCD_CONFLICT_START%%\r\n") {
		t.Fatalf("expected CRLF markers in %q", got)
	}
}
