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
func (fakeController) Rescan(context.Context, string, []string) error { return nil }

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
