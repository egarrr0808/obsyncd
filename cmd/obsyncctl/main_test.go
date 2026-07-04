package main

import "testing"

func TestParseConflictBlocks(t *testing.T) {
	input := "before\n" +
		"%%OBSYNCD_CONFLICT_START%%\n" +
		"%%OBSYNCD_LOCAL_START%%\n" +
		"local text\n" +
		"%%OBSYNCD_LOCAL_END%%\n" +
		"%%OBSYNCD_REMOTE_START%%\n" +
		"remote text\n" +
		"%%OBSYNCD_REMOTE_END%%\n" +
		"%%OBSYNCD_CONFLICT_END%%\n" +
		"after\n"
	blocks := parseConflictBlocks(input)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d", len(blocks))
	}
	if blocks[0].Local != "local text\n" || blocks[0].Remote != "remote text\n" {
		t.Fatalf("block = %#v", blocks[0])
	}
}

func TestResolveContent(t *testing.T) {
	input := "a\n" +
		"%%OBSYNCD_CONFLICT_START%%\n" +
		"%%OBSYNCD_LOCAL_START%%\n" +
		"L\n" +
		"%%OBSYNCD_LOCAL_END%%\n" +
		"%%OBSYNCD_REMOTE_START%%\n" +
		"R\n" +
		"%%OBSYNCD_REMOTE_END%%\n" +
		"%%OBSYNCD_CONFLICT_END%%\n" +
		"b\n"
	got, ok := resolveContent(input, "submerge")
	if !ok {
		t.Fatal("expected change")
	}
	want := "a\nL\nR\nb\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
