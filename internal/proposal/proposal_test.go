package proposal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
		t.Fatalf("accepted ack missing: %v", err)
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
		t.Fatalf("accepted ack missing: %v", err)
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
