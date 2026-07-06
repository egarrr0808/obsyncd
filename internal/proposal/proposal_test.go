package proposal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obsyncd/internal/statestore"
)

type fakeController struct{}

func (fakeController) Pause(context.Context, string) error            { return nil }
func (fakeController) Resume(context.Context, string) error           { return nil }
func (fakeController) Rescan(context.Context, string, []string) error { return nil }

type countingController struct {
	resume int
}

func (*countingController) Pause(context.Context, string) error            { return nil }
func (c *countingController) Resume(context.Context, string) error         { c.resume++; return nil }
func (*countingController) Rescan(context.Context, string, []string) error { return nil }

func TestHubAcceptsFreshProposal(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := Hub{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "hub", Store: statestore.New(root), Controller: fakeController{},
	}
	p := Proposal{
		Type: "proposal", ID: "one", Device: "client", Path: "note.md",
		BaseHash: hashString("base\n"), ContentHash: hashString("next\n"), Content: "next\n",
	}
	pp := filepath.Join(proposals, "proposal-one.json")
	if err := writeJSON(pp, p); err != nil {
		t.Fatal(err)
	}
	if err := hub.handle(context.Background(), pp, p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "next\n" {
		t.Fatalf("content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(proposals, "accepted-one.json")); err != nil {
		if _, err := os.Stat(filepath.Join(proposals, "accepted-"+deviceKey("client")+"-one.json")); err != nil {
			t.Fatalf("accepted ack missing: %v", err)
		}
	}
}

func TestHubConflictsStaleProposal(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := Hub{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "hub", Store: statestore.New(root), Controller: fakeController{},
	}
	p := Proposal{
		Type: "proposal", ID: "two", Device: "client", Path: "note.md",
		BaseHash: hashString("old\n"), ContentHash: hashString("client\n"), Content: "client\n",
	}
	pp := filepath.Join(proposals, "proposal-two.json")
	if err := writeJSON(pp, p); err != nil {
		t.Fatal(err)
	}
	if err := hub.handle(context.Background(), pp, p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "server\n" {
		t.Fatalf("server was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(proposals, "conflict-two.json")); err != nil {
		t.Fatalf("conflict job missing: %v", err)
	}
}

func TestHubConflictsSameBaseCompetingProposals(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := Hub{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "hub", Store: statestore.New(root), Controller: fakeController{},
	}
	first := Proposal{
		Type: "proposal", ID: "first", Device: "client-a", Path: "note.md",
		BaseHash: hashString("base\n"), ContentHash: hashString("one\n"), Content: "one\n",
		CreatedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano),
	}
	second := Proposal{
		Type: "proposal", ID: "second", Device: "client-b", Path: "note.md",
		BaseHash: hashString("base\n"), ContentHash: hashString("two\n"), Content: "two\n",
		CreatedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano),
	}
	firstPath := filepath.Join(proposals, "proposal-first.json")
	secondPath := filepath.Join(proposals, "proposal-second.json")
	if err := writeJSON(firstPath, first); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(secondPath, second); err != nil {
		t.Fatal(err)
	}
	if err := hub.handle(context.Background(), firstPath, first); err != nil {
		t.Fatal(err)
	}
	if err := hub.handle(context.Background(), secondPath, second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "base\n" {
		t.Fatalf("hub file changed: %q", got)
	}
	for _, name := range []string{"conflict-first.json", "conflict-second.json"} {
		if _, err := os.Stat(filepath.Join(proposals, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
}

func TestHubConfirmsAlreadyMatchingProposal(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := Hub{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "hub", Store: statestore.New(root), Controller: fakeController{},
	}
	p := Proposal{
		Type: "proposal", ID: "three", Device: "client", Path: "note.md",
		BaseHash: "", ContentHash: hashString("same\n"), Content: "same\n",
	}
	pp := filepath.Join(proposals, "proposal-three.json")
	if err := writeJSON(pp, p); err != nil {
		t.Fatal(err)
	}
	if err := hub.handle(context.Background(), pp, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(proposals, "accepted-three.json")); err != nil {
		if _, err := os.Stat(filepath.Join(proposals, "accepted-"+deviceKey("client")+"-three.json")); err != nil {
			t.Fatalf("accepted ack missing: %v", err)
		}
	}
}

func TestHubWritesAcceptedForEveryClient(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := Hub{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "hub", TargetDevices: []string{"client-a", "client-b"}, Store: statestore.New(root), Controller: fakeController{},
	}
	p := Proposal{
		Type: "proposal", ID: "fanout", Device: "client-a", Path: "note.md",
		BaseHash: hashString("base\n"), ContentHash: hashString("next\n"), Content: "next\n",
	}
	pp := filepath.Join(proposals, "proposal-fanout.json")
	if err := writeJSON(pp, p); err != nil {
		t.Fatal(err)
	}
	if err := hub.handle(context.Background(), pp, p); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"accepted-" + deviceKey("client-a") + "-fanout.json", "accepted-" + deviceKey("client-b") + "-fanout.json"} {
		if _, err := os.Stat(filepath.Join(proposals, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	count := 0
	entries, err := os.ReadDir(proposals)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "accepted-") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("accepted count = %d", count)
	}
}

func TestConflictIngestStoresServerBase(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("client\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ingest := ConflictIngest{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "client", Store: store, Controller: fakeController{},
	}
	job := Conflict{
		Type: "conflict", ID: "four", TargetDevice: "client", Path: "note.md",
		ServerContent: "server\n", ClientContent: "client\n",
	}
	jp := filepath.Join(proposals, "conflict-four.json")
	if err := writeJSON(jp, job); err != nil {
		t.Fatal(err)
	}
	if err := ingest.handle(context.Background(), jp, job); err != nil {
		t.Fatal(err)
	}
	base, ok, err := store.Base(context.Background(), "obsidian", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || base != "server\n" {
		t.Fatalf("base = %q %t", base, ok)
	}
}

func TestAcceptedRemovesOriginalProposal(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(proposals, "proposal-five.json"), Proposal{Type: "proposal", ID: "five"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(proposals, "accepted-five.json"), Accepted{
		Type: "accepted", ID: "five", TargetDevice: "client", Path: "note.md", ContentHash: hashString("done\n"),
	}); err != nil {
		t.Fatal(err)
	}
	ingest := ConflictIngest{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "client", Store: store, Controller: fakeController{},
	}
	if err := ingest.handleAccepted(context.Background(), filepath.Join(proposals, "accepted-five.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(proposals, "proposal-five.json")); !os.IsNotExist(err) {
		t.Fatalf("proposal remains: %v", err)
	}
}

func TestAcceptedAppliesResolutionToDivergentLocalFile(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pendingArtifact := filepath.Join(root, "server.tmp")
	if err := os.WriteFile(pendingArtifact, []byte("server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stage(context.Background(), "obsidian", "note.md", pendingArtifact); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(proposals, "accepted-six.json"), Accepted{
		Type: "accepted", ID: "six", TargetDevice: "client", Path: "note.md",
		ContentHash: hashString("resolved\n"), Content: "resolved\n",
	}); err != nil {
		t.Fatal(err)
	}
	ingest := ConflictIngest{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "client", Store: store, Controller: fakeController{},
	}
	if err := ingest.handleAccepted(context.Background(), filepath.Join(proposals, "accepted-six.json")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "resolved\n" {
		t.Fatalf("content = %q", got)
	}
	pending, err := store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending remains: %#v", pending)
	}
}

func TestAcceptedDoesNotResumeWithOtherPendingConflict(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	controller := &countingController{}
	if err := os.WriteFile(filepath.Join(root, "done.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pendingArtifact := filepath.Join(root, "remote.tmp")
	if err := os.WriteFile(pendingArtifact, []byte("server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stage(context.Background(), "obsidian", "blocked.md", pendingArtifact); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(proposals, "accepted-nine.json"), Accepted{
		Type: "accepted", ID: "nine", TargetDevice: "client", Path: "done.md", ContentHash: hashString("done\n"),
	}); err != nil {
		t.Fatal(err)
	}
	ingest := ConflictIngest{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "client", Store: store, Controller: controller,
	}
	if err := ingest.handleAccepted(context.Background(), filepath.Join(proposals, "accepted-nine.json")); err != nil {
		t.Fatal(err)
	}
	if controller.resume != 0 {
		t.Fatalf("resumed with pending conflict")
	}
}

func TestAcceptedDoesNotResumeWithOtherLocalProposal(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	controller := &countingController{}
	if err := os.WriteFile(filepath.Join(root, "done.md"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(proposals, "proposal-other.json"), Proposal{
		Type: "proposal", ID: "other", Device: "client", Path: "other.md",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(proposals, "accepted-ten.json"), Accepted{
		Type: "accepted", ID: "ten", TargetDevice: "client", Path: "done.md", ContentHash: hashString("done\n"),
	}); err != nil {
		t.Fatal(err)
	}
	ingest := ConflictIngest{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "client", Store: store, Controller: controller,
	}
	if err := ingest.handleAccepted(context.Background(), filepath.Join(proposals, "accepted-ten.json")); err != nil {
		t.Fatal(err)
	}
	if controller.resume != 0 {
		t.Fatalf("resumed with another local proposal")
	}
}

func TestLocalPendingListsOwnProposals(t *testing.T) {
	proposals := t.TempDir()
	if err := writeJSON(filepath.Join(proposals, "proposal-one.json"), Proposal{
		Type: "proposal", ID: "one", Device: "client", Path: "note.md",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(proposals, "proposal-two.json"), Proposal{
		Type: "proposal", ID: "two", Device: "other", Path: "other.md",
	}); err != nil {
		t.Fatal(err)
	}
	paths, err := LocalPending(proposals, "client")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "note.md" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestCleanupTransportFilesRemovesOnlyStaleAcksAndTemps(t *testing.T) {
	dir := t.TempDir()
	stale := time.Now().Add(-time.Hour)
	files := []string{
		"accepted-old.json",
		".syncthing.tmp",
		"proposal-keep.json",
		"conflict-keep.json",
		"accepted-new.json",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"accepted-old.json", ".syncthing.tmp"} {
		if err := os.Chtimes(filepath.Join(dir, name), stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	cleanupTransportFiles(dir)
	for _, name := range []string{"accepted-old.json", ".syncthing.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", name, err)
		}
	}
	for _, name := range []string{"proposal-keep.json", "conflict-keep.json", "accepted-new.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s removed: %v", name, err)
		}
	}
}

func TestConflictIngestIgnoresStaleConflict(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := Conflict{
		Type: "conflict", ID: "six", TargetDevice: "client", Path: "note.md",
		ProposalHash: hashString("old-client\n"), ServerContent: "server\n", ClientContent: "old-client\n",
	}
	jp := filepath.Join(proposals, "conflict-six.json")
	if err := writeJSON(jp, job); err != nil {
		t.Fatal(err)
	}
	ingest := ConflictIngest{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "client", Store: store, Controller: fakeController{},
	}
	if err := ingest.handle(context.Background(), jp, job); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jp); !os.IsNotExist(err) {
		t.Fatalf("stale conflict remains: %v", err)
	}
	pending, err := store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("stale conflict staged: %#v", pending)
	}
}

func TestConflictIngestIgnoresSupersededConflict(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBase(context.Background(), "obsidian", "note.md", "resolved\n"); err != nil {
		t.Fatal(err)
	}
	job := Conflict{
		Type: "conflict", ID: "superseded", TargetDevice: "client", Path: "note.md",
		ServerHash: hashString("old-server\n"), ProposalHash: hashString("local\n"),
		ServerContent: "old-server\n", ClientContent: "local\n",
	}
	jp := filepath.Join(proposals, "conflict-superseded.json")
	if err := writeJSON(jp, job); err != nil {
		t.Fatal(err)
	}
	ingest := ConflictIngest{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "client", Store: store, Controller: fakeController{},
	}
	if err := ingest.handle(context.Background(), jp, job); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jp); !os.IsNotExist(err) {
		t.Fatalf("superseded conflict remains: %v", err)
	}
	pending, err := store.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("superseded conflict staged: %#v", pending)
	}
}

func TestHubRemovesAcceptedConflict(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflict := Conflict{Type: "conflict", ID: "seven", TargetDevice: "client", Path: "note.md"}
	if err := writeJSON(filepath.Join(proposals, "conflict-seven.json"), conflict); err != nil {
		t.Fatal(err)
	}
	hub := Hub{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "hub", Store: store, Controller: fakeController{},
	}
	p := Proposal{
		Type: "proposal", ID: "eight", Device: "client", Path: "note.md",
		BaseHash: hashString("server\n"), ContentHash: hashString("resolved\n"), Content: "resolved\n",
		Resolve: true,
	}
	pp := filepath.Join(proposals, "proposal-eight.json")
	if err := writeJSON(pp, p); err != nil {
		t.Fatal(err)
	}
	if err := hub.handle(context.Background(), pp, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(proposals, "conflict-seven.json")); !os.IsNotExist(err) {
		t.Fatalf("conflict remains: %v", err)
	}
}

func TestHubRemovesAllConflictsForAcceptedPath(t *testing.T) {
	root := t.TempDir()
	proposals := t.TempDir()
	store := statestore.New(root)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, conflict := range []Conflict{
		{Type: "conflict", ID: "a", TargetDevice: "client-a", Path: "note.md"},
		{Type: "conflict", ID: "b", TargetDevice: "client-b", Path: "note.md"},
		{Type: "conflict", ID: "other", TargetDevice: "client-b", Path: "other.md"},
	} {
		if err := writeJSON(filepath.Join(proposals, "conflict-"+conflict.ID+".json"), conflict); err != nil {
			t.Fatal(err)
		}
	}
	hub := Hub{
		Root: root, ProposalDir: proposals, Folder: "obsidian", ProposalFolder: "obsyncd-proposals",
		DeviceID: "hub", Store: store, Controller: fakeController{},
	}
	p := Proposal{
		Type: "proposal", ID: "accepted-path", Device: "client-a", Path: "note.md",
		BaseHash: hashString("server\n"), ContentHash: hashString("resolved\n"), Content: "resolved\n",
		Resolve: true,
	}
	pp := filepath.Join(proposals, "proposal-accepted-path.json")
	if err := writeJSON(pp, p); err != nil {
		t.Fatal(err)
	}
	if err := hub.handle(context.Background(), pp, p); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"conflict-a.json", "conflict-b.json"} {
		if _, err := os.Stat(filepath.Join(proposals, name)); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(proposals, "conflict-other.json")); err != nil {
		t.Fatalf("unrelated conflict removed: %v", err)
	}
}

func TestGlobalConflictsListsSharedJobs(t *testing.T) {
	proposals := t.TempDir()
	for _, conflict := range []Conflict{
		{Type: "conflict", ID: "one", TargetDevice: "client-a", Path: "dir/../note.md", ServerContent: "server\n"},
		{Type: "conflict", ID: "two", TargetDevice: "client-a", Path: "note.md", ServerContent: "server\n"},
		{Type: "conflict", ID: "three", TargetDevice: "client-b", Path: "note.md", ServerContent: "server\n"},
	} {
		if err := writeJSON(filepath.Join(proposals, "conflict-"+conflict.ID+".json"), conflict); err != nil {
			t.Fatal(err)
		}
	}
	conflicts, err := GlobalConflicts(proposals)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 2 {
		t.Fatalf("conflicts = %#v", conflicts)
	}
}
