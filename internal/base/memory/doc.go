// Package memory mirrors the semantic memory system of crates/jcode-base
// (see upstream MEMORY_ARCHITECTURE.md):
//
//   - a directed memory graph persisted as ~/.jcode/memory/graph.json
//     (node kinds Memory/Tag/Cluster; edge kinds HasTag/RelatesTo/
//     Supersedes/Contradicts/InCluster/DerivedFrom);
//   - cascade retrieval: embedding top-k → BFS depth-2 with edge weights
//     decayed by 0.7^depth → dedupe/rerank;
//   - local embeddings via a hand-written MiniLM-L6-v2 forward pass
//     (see internal/embedding), no external vector DB.
//
// Acceptance bar: cosine similarity vs upstream embeddings ≥ 0.999 and
// identical top-10 retrieval ordering; gcode must be able to run retrieval
// directly against an existing ~/.jcode/memory directory.
//
// Deferred: scheduled for M5 (outside the approved M0–M4 scope).
package memory
