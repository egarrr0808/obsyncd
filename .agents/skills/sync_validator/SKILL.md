---
name: sync_validator
description: Validate obsyncd synchronization behavior by simulating simultaneous edits and verifying propagation
---

# Sync Validator Skill

Use this skill when you need to verify if a synchronization feature, conflict resolution path, or deletion semantics behaves correctly across a multi-node topology.

## Walkthrough: Simulating Simultaneous Client Edits

To systematically test client interactions and simultaneous edits, perform the following steps inside a Go test within `internal/proposal/integration_test.go`:

1. **Initialize the Cluster**:
   ```go
   cluster := NewCluster(t)
   ```
   This creates laptop-1, laptop-2, and server (hub) nodes with isolated temporary root vaults and proposal directories.

2. **Setup Base Content**:
   Write the initial file to any client node and run a sync cycle:
   ```go
   _ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "note.md"), []byte("base content"), 0o644)
   cluster.Sync() // Propagates note.md to Server and Laptop-2
   ```

3. **Simulate Divergent Client Modifications**:
   Modify the file simultaneously on both laptops. You must also write to the guard's `SnapshotPath` to simulate that the file was modified locally since the last sync baseline:
   ```go
   _ = os.WriteFile(filepath.Join(cluster.Laptop1.Root, "note.md"), []byte("edit from laptop-1"), 0o644)
   _ = os.WriteFile(cluster.Laptop1.Guard.SnapshotPath("note.md"), []byte("base content"), 0o600)

   _ = os.WriteFile(filepath.Join(cluster.Laptop2.Root, "note.md"), []byte("edit from laptop-2"), 0o644)
   _ = os.WriteFile(cluster.Laptop2.Guard.SnapshotPath("note.md"), []byte("base content"), 0o600)
   ```

4. **Propagate and Verify Conflict Staging**:
   Run `cluster.Sync()`. Check that the clients' folders are successfully paused (indicating conflict detection):
   ```go
   cluster.Sync()
   if !cluster.Laptop1.Controller.paused["obsidian"] || !cluster.Laptop2.Controller.paused["obsidian"] {
       t.Fatal("expected conflict to pause folders")
   }
   ```

5. **Resolve and Confirm**:
   Perform a local or remote resolution on one client and verify that it propagates through the hub to the other client after subsequent sync steps:
   ```go
   _, _ = cluster.Laptop1.Store.Resolve(ctx, "obsidian", "note.md", "local")
   _ = proposal.SubmitContent(ctx, cluster.Laptop1.ProposalDir, "obsidian", "laptop-1", cluster.Laptop1.Store, "note.md", "edit from laptop-1", true)
   
   cluster.Sync() // Hub processes resolution
   cluster.Sync() // Laptop-2 ingests accepted resolution
   ```
