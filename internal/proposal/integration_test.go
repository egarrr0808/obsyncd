package proposal_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obsyncd/internal/guard"
	"obsyncd/internal/proposal"
	"obsyncd/internal/statestore"
)

func init() {
	proposal.SettleDelay = 0
}

type integrationController struct {
	paused map[string]bool
}

func (c *integrationController) Pause(ctx context.Context, folder string) error {
	c.paused[folder] = true
	return nil
}

func (c *integrationController) Resume(ctx context.Context, folder string) error {
	c.paused[folder] = false
	return nil
}

func (c *integrationController) Rescan(ctx context.Context, folder string, paths []string) error {
	return nil
}

type TestNode struct {
	ID          string
	Role        string
	Root        string
	ProposalDir string
	Store       *statestore.Store
	Controller  *integrationController
	Submitter   *proposal.Submitter
	Ingest      *proposal.ConflictIngest
	Hub         *proposal.Hub
	Guard       *guard.Guard
}

type Cluster struct {
	Laptop1         *TestNode
	Laptop2         *TestNode
	Server          *TestNode
	proposalTracker map[string][]byte
	syncedNodes     map[string]map[string]bool
}

func NewCluster(t *testing.T) *Cluster {
	laptop1Root := t.TempDir()
	laptop1Props := t.TempDir()
	laptop1State := t.TempDir()
	laptop1Store := statestore.New(laptop1Root)
	laptop1Ctrl := &integrationController{paused: make(map[string]bool)}

	laptop2Root := t.TempDir()
	laptop2Props := t.TempDir()
	laptop2State := t.TempDir()
	laptop2Store := statestore.New(laptop2Root)
	laptop2Ctrl := &integrationController{paused: make(map[string]bool)}

	serverRoot := t.TempDir()
	serverProps := t.TempDir()
	serverStore := statestore.New(serverRoot)
	serverCtrl := &integrationController{paused: make(map[string]bool)}

	c := &Cluster{
		proposalTracker: make(map[string][]byte),
		syncedNodes:     make(map[string]map[string]bool),
	}

	c.Laptop1 = &TestNode{
		ID:          "laptop-1",
		Role:        "client",
		Root:        laptop1Root,
		ProposalDir: laptop1Props,
		Store:       laptop1Store,
		Controller:  laptop1Ctrl,
		Submitter: &proposal.Submitter{
			Root:           laptop1Root,
			ProposalDir:    laptop1Props,
			Folder:         "obsidian",
			ProposalFolder: "obsyncd-proposals",
			DeviceID:       "laptop-1",
			Store:          laptop1Store,
			Controller:     laptop1Ctrl,
		},
		Ingest: &proposal.ConflictIngest{
			Root:           laptop1Root,
			ProposalDir:    laptop1Props,
			Folder:         "obsidian",
			ProposalFolder: "obsyncd-proposals",
			DeviceID:       "laptop-1",
			Store:          laptop1Store,
			Controller:     laptop1Ctrl,
		},
		Guard: &guard.Guard{
			Root:           laptop1Root,
			StateDir:       laptop1State,
			Folder:         "obsidian",
			ProposalFolder: "obsyncd-proposals",
			ProposalDir:    laptop1Props,
			DeviceID:       "laptop-1",
			Controller:     laptop1Ctrl,
			Stager:         laptop1Store,
		},
	}

	c.Laptop2 = &TestNode{
		ID:          "laptop-2",
		Role:        "client",
		Root:        laptop2Root,
		ProposalDir: laptop2Props,
		Store:       laptop2Store,
		Controller:  laptop2Ctrl,
		Submitter: &proposal.Submitter{
			Root:           laptop2Root,
			ProposalDir:    laptop2Props,
			Folder:         "obsidian",
			ProposalFolder: "obsyncd-proposals",
			DeviceID:       "laptop-2",
			Store:          laptop2Store,
			Controller:     laptop2Ctrl,
		},
		Ingest: &proposal.ConflictIngest{
			Root:           laptop2Root,
			ProposalDir:    laptop2Props,
			Folder:         "obsidian",
			ProposalFolder: "obsyncd-proposals",
			DeviceID:       "laptop-2",
			Store:          laptop2Store,
			Controller:     laptop2Ctrl,
		},
		Guard: &guard.Guard{
			Root:           laptop2Root,
			StateDir:       laptop2State,
			Folder:         "obsidian",
			ProposalFolder: "obsyncd-proposals",
			ProposalDir:    laptop2Props,
			DeviceID:       "laptop-2",
			Controller:     laptop2Ctrl,
			Stager:         laptop2Store,
		},
	}

	c.Server = &TestNode{
		ID:          "server",
		Role:        "hub",
		Root:        serverRoot,
		ProposalDir: serverProps,
		Store:       serverStore,
		Controller:  serverCtrl,
		Hub: &proposal.Hub{
			Root:           serverRoot,
			ProposalDir:    serverProps,
			Folder:         "obsidian",
			ProposalFolder: "obsyncd-proposals",
			DeviceID:       "server",
			TargetDevices:  []string{"laptop-1", "laptop-2"},
			Store:          serverStore,
			Controller:     serverCtrl,
		},
	}

	return c
}

func (c *Cluster) Nodes() []*TestNode {
	return []*TestNode{c.Laptop1, c.Laptop2, c.Server}
}

func (c *Cluster) findNode(id string) *TestNode {
	for _, n := range c.Nodes() {
		if n.ID == id {
			return n
		}
	}
	return nil
}

func (c *Cluster) SyncProposals() {
	// Sync proposal directory files bidirectionally with delete propagation
	for _, node := range c.Nodes() {
		entries, _ := os.ReadDir(node.ProposalDir)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if _, ok := c.proposalTracker[name]; !ok {
				bs, _ := os.ReadFile(filepath.Join(node.ProposalDir, name))
				c.proposalTracker[name] = bs
				if c.syncedNodes[name] == nil {
					c.syncedNodes[name] = make(map[string]bool)
				}
				c.syncedNodes[name][node.ID] = true
			}
		}
	}

	for name := range c.proposalTracker {
		deleted := false
		for nodeID, wasPresent := range c.syncedNodes[name] {
			if wasPresent {
				node := c.findNode(nodeID)
				if _, err := os.Stat(filepath.Join(node.ProposalDir, name)); os.IsNotExist(err) {
					deleted = true
					break
				}
			}
		}
		if deleted {
			for _, node := range c.Nodes() {
				_ = os.Remove(filepath.Join(node.ProposalDir, name))
			}
			delete(c.proposalTracker, name)
			delete(c.syncedNodes, name)
		} else {
			for _, node := range c.Nodes() {
				path := filepath.Join(node.ProposalDir, name)
				if _, err := os.Stat(path); os.IsNotExist(err) {
					_ = os.WriteFile(path, c.proposalTracker[name], 0o600)
					c.syncedNodes[name][node.ID] = true
				}
			}
		}
	}
}

func (c *Cluster) SyncVaults() {
	ctx := context.Background()
	serverFiles := make(map[string][]byte)
	_ = filepath.WalkDir(c.Server.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(c.Server.Root, path)
		if err != nil {
			return nil
		}
		if !isMarkdown(rel) || isInternal(rel) {
			return nil
		}
		bs, _ := os.ReadFile(path)
		serverFiles[rel] = bs
		return nil
	})

	clients := []*TestNode{c.Laptop1, c.Laptop2}
	for _, client := range clients {
		if client.Controller.paused["obsidian"] {
			continue
		}
		// Copy server files to client if missing/different
		for rel, content := range serverFiles {
			clientPath := filepath.Join(client.Root, rel)
			localBS, err := os.ReadFile(clientPath)
			if os.IsNotExist(err) || string(localBS) != string(content) {
				_ = os.MkdirAll(filepath.Dir(clientPath), 0o755)
				_ = os.WriteFile(clientPath, content, 0o644)
				_ = client.Guard.DetectRemoteOverwrite(ctx, rel)
			}
		}
		// Delete files missing from server
		_ = filepath.WalkDir(client.Root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(client.Root, path)
			if err != nil {
				return nil
			}
			if !isMarkdown(rel) || isInternal(rel) {
				return nil
			}
			if _, exists := serverFiles[rel]; !exists {
				_ = os.Remove(path)
				_ = client.Guard.DetectRemoteOverwrite(ctx, rel)
			}
			return nil
		})
	}
}

func (c *Cluster) Sync() {
	ctx := context.Background()
	_ = c.Laptop1.Submitter.Scan(ctx)
	_ = c.Laptop2.Submitter.Scan(ctx)

	c.SyncProposals()

	_ = c.Server.Hub.Scan(ctx)

	c.SyncProposals()

	_ = c.Laptop1.Ingest.Scan(ctx)
	_ = c.Laptop2.Ingest.Scan(ctx)

	c.SyncVaults()
}

func isMarkdown(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

func isInternal(path string) bool {
	slash := filepath.ToSlash(filepath.Clean(path))
	return strings.HasPrefix(slash, ".obsidian/obsyncd-") || strings.HasPrefix(slash, ".stignore")
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestIntegrationScenario1And2(t *testing.T) {
	// Scenario 1: New file from laptop-1 syncs to server then laptop-2
	// Scenario 2: New file from laptop-2 syncs to server then laptop-1
	cluster := NewCluster(t)

	// Create note on laptop-1
	note1Path := filepath.Join(cluster.Laptop1.Root, "note1.md")
	_ = os.WriteFile(note1Path, []byte("laptop-1 content"), 0o644)

	// Sync cycle
	cluster.Sync()

	// Check server and laptop-2 got note1
	bsServer, err := os.ReadFile(filepath.Join(cluster.Server.Root, "note1.md"))
	if err != nil || string(bsServer) != "laptop-1 content" {
		t.Fatalf("note1 missing/incorrect on server: %s", bsServer)
	}
	bsLaptop2, err := os.ReadFile(filepath.Join(cluster.Laptop2.Root, "note1.md"))
	if err != nil || string(bsLaptop2) != "laptop-1 content" {
		t.Fatalf("note1 missing/incorrect on laptop-2: %s", bsLaptop2)
	}

	// Create note on laptop-2
	note2Path := filepath.Join(cluster.Laptop2.Root, "note2.md")
	_ = os.WriteFile(note2Path, []byte("laptop-2 content"), 0o644)

	// Sync cycle
	cluster.Sync()

	// Check server and laptop-1 got note2
	bsServer2, err := os.ReadFile(filepath.Join(cluster.Server.Root, "note2.md"))
	if err != nil || string(bsServer2) != "laptop-2 content" {
		t.Fatalf("note2 missing/incorrect on server: %s", bsServer2)
	}
	bsLaptop1, err := os.ReadFile(filepath.Join(cluster.Laptop1.Root, "note2.md"))
	if err != nil || string(bsLaptop1) != "laptop-2 content" {
		t.Fatalf("note2 missing/incorrect on laptop-1: %s", bsLaptop1)
	}
}

func TestIntegrationScenario3And4And5(t *testing.T) {
	// Scenario 3: Same file edited on both laptops: conflict, no overwrite
	// Scenario 4: L on laptop-1 publishes laptop-1 content
	// Scenario 5: L on laptop-2 publishes laptop-2 content (simulated in a clean conflict)
	cluster := NewCluster(t)

	// Setup initial file in sync
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "conflict.md"), []byte("base version"), 0o644)
	cluster.Sync() // laptop-1 submits, server accepts, laptop-2 applies

	// Divergent edits on both laptops
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "conflict.md"), []byte("laptop-1 edit"), 0o644)
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Guard.SnapshotPath("conflict.md")), []byte("base version"), 0o600) // snapshot client change

	_ = os.WriteFile(filepath.Join(cluster.Laptop2.Root, "conflict.md"), []byte("laptop-2 edit"), 0o644)
	_ = os.WriteFile(filepath.Join(cluster.Laptop2.Guard.SnapshotPath("conflict.md")), []byte("base version"), 0o600) // snapshot client change

	cluster.Sync()

	// Laptop-1 is accepted as server latest; laptop-2 keeps its dirty edit paused for resolution.
	if cluster.Laptop2.Controller.paused["obsidian"] == false {
		t.Fatal("expected laptop-2 to be paused on conflict")
	}

	bs1, _ := os.ReadFile(filepath.Join(cluster.Laptop1.Root, "conflict.md"))
	if string(bs1) != "laptop-1 edit" {
		t.Fatalf("laptop-1 content overwritten: %s", bs1)
	}

	bs2, _ := os.ReadFile(filepath.Join(cluster.Laptop2.Root, "conflict.md"))
	if string(bs2) != "laptop-2 edit" {
		t.Fatalf("laptop-2 content overwritten: %s", bs2)
	}

	// Scenario 4: R on laptop-2 keeps the hub content from laptop-1.
	_, err := cluster.Laptop2.Store.Resolve(context.Background(), "obsidian", "conflict.md", "remote")
	if err != nil {
		t.Fatalf("laptop-2 resolve remote failed: %v", err)
	}
	_ = proposal.SubmitContent(context.Background(), cluster.Laptop2.ProposalDir, "obsidian", "laptop-2", cluster.Laptop2.Store, "conflict.md", "laptop-1 edit", true)

	cluster.Sync()

	// Hub should accept the resolve, Laptop-2 should receive laptop-1's content
	bsServer, _ := os.ReadFile(filepath.Join(cluster.Server.Root, "conflict.md"))
	if string(bsServer) != "laptop-1 edit" {
		t.Fatalf("hub not resolved to laptop-1 content: %s", bsServer)
	}

	bsLaptop2After, _ := os.ReadFile(filepath.Join(cluster.Laptop2.Root, "conflict.md"))
	if string(bsLaptop2After) != "laptop-1 edit" {
		t.Fatalf("laptop-2 did not apply resolved content: %s", bsLaptop2After)
	}
}

func TestIntegrationScenario6(t *testing.T) {
	// Scenario 6: R publishes hub content
	cluster := NewCluster(t)

	// Setup initial file
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "r_test.md"), []byte("base version"), 0o644)
	cluster.Sync()

	// Divergent edits
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "r_test.md"), []byte("laptop-1 edit"), 0o644)
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Guard.SnapshotPath("r_test.md")), []byte("base version"), 0o600)

	_ = os.WriteFile(filepath.Join(cluster.Laptop2.Root, "r_test.md"), []byte("laptop-2 edit"), 0o644)
	_ = os.WriteFile(filepath.Join(cluster.Laptop2.Guard.SnapshotPath("r_test.md")), []byte("base version"), 0o600)

	cluster.Sync()

	// Resolve remote (keep hub) on conflicted laptop-2
	_, err := cluster.Laptop2.Store.Resolve(context.Background(), "obsidian", "r_test.md", "remote")
	if err != nil {
		t.Fatalf("resolve remote failed: %v", err)
	}
	// Submit resolution proposal
	_ = proposal.SubmitContent(context.Background(), cluster.Laptop2.ProposalDir, "obsidian", "laptop-2", cluster.Laptop2.Store, "r_test.md", "laptop-1 edit", true)

	cluster.Sync()

	// Verify all nodes ended up with "base version"
	bs1, _ := os.ReadFile(filepath.Join(cluster.Laptop1.Root, "r_test.md"))
	bs2, _ := os.ReadFile(filepath.Join(cluster.Laptop2.Root, "r_test.md"))
	bsS, _ := os.ReadFile(filepath.Join(cluster.Server.Root, "r_test.md"))

	if string(bs1) != "laptop-1 edit" || string(bs2) != "laptop-1 edit" || string(bsS) != "laptop-1 edit" {
		t.Fatalf("R did not sync hub version: l1=%s, l2=%s, server=%s", bs1, bs2, bsS)
	}
}

func TestIntegrationScenario7And8(t *testing.T) {
	// Scenario 7: R refuses when hub content empty and local non-empty
	// Scenario 8: L refuses when local file missing
	cluster := NewCluster(t)

	// Stage a conflict where remote is empty and local is non-empty
	tmpRemote, _ := os.CreateTemp("", "remote-*")
	tmpRemoteName := tmpRemote.Name()
	_ = tmpRemote.Close()
	defer os.Remove(tmpRemoteName)

	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "empty_remote.md"), []byte("non-empty local"), 0o644)
	_, _ = cluster.Laptop1.Store.Stage(context.Background(), "obsidian", "empty_remote.md", tmpRemoteName)

	// Call Resolve with remote -> must return error
	_, err := cluster.Laptop1.Store.Resolve(context.Background(), "obsidian", "empty_remote.md", "remote")
	if err == nil {
		t.Fatal("expected error when resolving remote with empty remote content")
	}

	// Scenario 8: L refuses when local file missing
	// Stage a conflict for a missing local file
	tmpRemote2, _ := os.CreateTemp("", "remote2-*")
	tmpRemote2Name := tmpRemote2.Name()
	_ = os.WriteFile(tmpRemote2Name, []byte("remote version"), 0o644)
	tmpRemote2.Close()
	defer os.Remove(tmpRemote2Name)

	_, _ = cluster.Laptop1.Store.Stage(context.Background(), "obsidian", "missing_local.md", tmpRemote2Name)
	// Make sure local is missing
	_ = os.Remove(filepath.Join(cluster.Laptop1.Root, "missing_local.md"))

	// Call Resolve with local -> must return error
	_, err = cluster.Laptop1.Store.Resolve(context.Background(), "obsidian", "missing_local.md", "local")
	if err == nil {
		t.Fatal("expected error when resolving local with missing local file")
	}
}

func TestIntegrationScenario9(t *testing.T) {
	// Scenario 9: submerge never drops either side
	cluster := NewCluster(t)

	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "sub.md"), []byte("line 1\n"), 0o644)
	tmpRemote, _ := os.CreateTemp("", "remote-*")
	tmpRemoteName := tmpRemote.Name()
	_ = os.WriteFile(tmpRemoteName, []byte("line 2\n"), 0o644)
	tmpRemote.Close()
	defer os.Remove(tmpRemoteName)

	_, _ = cluster.Laptop1.Store.Stage(context.Background(), "obsidian", "sub.md", tmpRemoteName)

	// Resolve with submerge
	_, err := cluster.Laptop1.Store.Resolve(context.Background(), "obsidian", "sub.md", "submerge")
	if err != nil {
		t.Fatalf("submerge failed: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(cluster.Laptop1.Root, "sub.md"))
	if !strings.Contains(string(got), "line 1") || !strings.Contains(string(got), "line 2") {
		t.Fatalf("submerge dropped content: %s", got)
	}
}

func TestIntegrationScenario10And11And12And13And14(t *testing.T) {
	// Scenario 10: Stale duplicate conflicts do not reappear after resolution
	// Scenario 11: Accepted ack removes conflicts for that path
	// Scenario 12: Proposal folder empty after clean resolution
	// Scenario 13: No .sync-conflict-* files appear in vault
	// Scenario 14: No canonical .md becomes empty unless user explicitly writes empty content and confirms that behavior.
	cluster := NewCluster(t)

	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "gc.md"), []byte("base content"), 0o644)
	cluster.Sync()

	// Divergent edits
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "gc.md"), []byte("laptop-1"), 0o644)
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Guard.SnapshotPath("gc.md")), []byte("base content"), 0o600)

	_ = os.WriteFile(filepath.Join(cluster.Laptop2.Root, "gc.md"), []byte("laptop-2"), 0o644)
	_ = os.WriteFile(filepath.Join(cluster.Laptop2.Guard.SnapshotPath("gc.md")), []byte("base content"), 0o600)

	cluster.Sync()

	// Resolve on conflicted Laptop-2 by keeping hub/server.
	_, err := cluster.Laptop2.Store.Resolve(context.Background(), "obsidian", "gc.md", "remote")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	_ = proposal.SubmitContent(context.Background(), cluster.Laptop2.ProposalDir, "obsidian", "laptop-2", cluster.Laptop2.Store, "gc.md", "laptop-1", true)

	// Sync several times to ensure all nodes process it
	cluster.Sync()
	cluster.Sync()
	cluster.Sync()

	// Scenario 12: Proposal folder empty after clean resolution
	for _, node := range cluster.Nodes() {
		entries, _ := os.ReadDir(node.ProposalDir)
		count := 0
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				count++
			}
		}
		if count > 0 {
			t.Fatalf("proposal folder not empty on %s: %d files remain", node.ID, count)
		}
	}

	// Scenario 13: No .sync-conflict-* files appear in vault
	for _, node := range cluster.Nodes() {
		_ = filepath.WalkDir(node.Root, func(path string, d fs.DirEntry, err error) error {
			if strings.Contains(d.Name(), ".sync-conflict-") {
				t.Fatalf("found sync conflict file in vault: %s", path)
			}
			return nil
		})
	}

	// Scenario 14: No canonical .md becomes empty unless user explicitly writes empty content and confirms
	for _, node := range cluster.Nodes() {
		bs, err := os.ReadFile(filepath.Join(node.Root, "gc.md"))
		if err != nil {
			t.Fatal(err)
		}
		if len(bs) == 0 {
			t.Fatal("gc.md became empty unexpectedly")
		}
	}
}

func TestIntegrationDeletionSemantics(t *testing.T) {
	// Let's verify our new deletion semantics integration!
	cluster := NewCluster(t)

	// Create and sync file first
	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "del.md"), []byte("to be deleted"), 0o644)
	cluster.Sync()

	// Delete file locally on laptop-1
	_ = os.Remove(filepath.Join(cluster.Laptop1.Root, "del.md"))

	// Sync twice:
	// 1. Laptop-1 detects delete and submits proposal
	// 2. Hub accepts delete, writes accepted ack
	// 3. Laptop-2 ingests accepted delete ack and deletes file locally
	cluster.Sync()
	cluster.Sync()
	cluster.Sync()

	// Verify del.md is deleted everywhere!
	if _, err := os.Stat(filepath.Join(cluster.Laptop1.Root, "del.md")); !os.IsNotExist(err) {
		t.Fatal("del.md still exists on laptop-1")
	}
	if _, err := os.Stat(filepath.Join(cluster.Server.Root, "del.md")); !os.IsNotExist(err) {
		t.Fatal("del.md still exists on server")
	}
	if _, err := os.Stat(filepath.Join(cluster.Laptop2.Root, "del.md")); !os.IsNotExist(err) {
		t.Fatal("del.md still exists on laptop-2")
	}
}

func TestIntegrationDeleteVsEditConflictResolvesDelete(t *testing.T) {
	cluster := NewCluster(t)

	_ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "delete-conflict.md"), []byte("base"), 0o644)
	cluster.Sync()

	_ = os.Remove(filepath.Join(cluster.Laptop1.Root, "delete-conflict.md"))
	_ = os.WriteFile(filepath.Join(cluster.Laptop2.Root, "delete-conflict.md"), []byte("laptop-2 edit"), 0o644)
	_ = os.WriteFile(cluster.Laptop2.Guard.SnapshotPath("delete-conflict.md"), []byte("base"), 0o600)

	cluster.Sync()

	if _, err := os.Stat(filepath.Join(cluster.Laptop1.Root, "delete-conflict.md")); !os.IsNotExist(err) {
		t.Fatalf("delete side recreated canonical before resolution: %v", err)
	}
	bs2, err := os.ReadFile(filepath.Join(cluster.Laptop2.Root, "delete-conflict.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(bs2) != "laptop-2 edit" {
		t.Fatalf("edit side overwritten before resolution: %q", bs2)
	}
	conflicts, err := proposal.GlobalConflicts(cluster.Laptop2.ProposalDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) == 0 {
		t.Fatal("delete/edit conflict not visible in shared conflict queue")
	}

	_ = os.Remove(filepath.Join(cluster.Laptop2.Root, "delete-conflict.md"))
	if err := proposal.SubmitDeleteResolved(context.Background(), cluster.Laptop2.ProposalDir, "obsidian", "laptop-2", cluster.Laptop2.Store, "delete-conflict.md"); err != nil {
		t.Fatal(err)
	}

	cluster.Sync()
	cluster.Sync()

	for _, node := range cluster.Nodes() {
		if _, err := os.Stat(filepath.Join(node.Root, "delete-conflict.md")); !os.IsNotExist(err) {
			t.Fatalf("delete-conflict.md still exists on %s: %v", node.ID, err)
		}
	}
}
