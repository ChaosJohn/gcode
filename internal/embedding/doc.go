// Package embedding is the CGO-free replacement for crates/jcode-embedding,
// which runs all-MiniLM-L6-v2 through tract-onnx and the HuggingFace
// tokenizers crate.
//
// Components:
//   - WordPiece tokenizer (hand-written; the model only needs WordPiece,
//     not the full tokenizers crate);
//   - safetensors weight loader (JSON header + raw fp32 body);
//   - a hand-written forward pass: embedding lookup → 6×(multi-head
//     self-attention + FFN + LayerNorm) → mean pooling → L2 normalize,
//     producing 384-dim vectors.
//
// Outputs are not bit-exact vs tract (floating-point reduction order
// differs); acceptance is cosine similarity ≥ 0.999 against upstream and
// identical top-10 retrieval ordering (DECISIONS.md D5).
//
// Deferred: scheduled for M5 (outside the approved M0–M4 scope).
package embedding
