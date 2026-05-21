package rag

import (
"math"
"sort"

"gok/internal/model"
)

// ExtractViewpoints clusters entries by cosine similarity of their embeddings
// and returns up to maxClusters representative viewpoints.
// Falls back to positional splitting if embeddings are missing.
func ExtractViewpoints(entries []model.Entry, maxClusters int) []model.Viewpoint {
if len(entries) == 0 {
return nil
}
if maxClusters <= 0 {
maxClusters = 3
}

hasEmbeddings := false
for _, e := range entries {
if len(e.Embedding.Slice()) > 0 {
hasEmbeddings = true
break
}
}

if hasEmbeddings {
return clusterByEmbedding(entries, maxClusters)
}
return splitPositional(entries, maxClusters)
}

// clusterByEmbedding uses greedy cosine clustering (no external lib).
func clusterByEmbedding(entries []model.Entry, k int) []model.Viewpoint {
type cluster struct {
centroid []float64
members  []model.Entry
}

clusters := make([]cluster, 0, k)

for _, e := range entries {
raw := e.Embedding.Slice()
if len(raw) == 0 {
continue
}
vec := toFloat64(raw)

bestCluster := -1
bestSim := 0.7 // minimum similarity threshold to join a cluster

for ci, c := range clusters {
sim := cosineSim(vec, c.centroid)
if sim > bestSim {
bestSim = sim
bestCluster = ci
}
}

if bestCluster >= 0 {
clusters[bestCluster].members = append(clusters[bestCluster].members, e)
n := float64(len(clusters[bestCluster].members))
for d := range clusters[bestCluster].centroid {
clusters[bestCluster].centroid[d] = (clusters[bestCluster].centroid[d]*(n-1) + vec[d]) / n
}
} else if len(clusters) < k {
clusters = append(clusters, cluster{
centroid: vec,
members:  []model.Entry{e},
})
}
}

sort.Slice(clusters, func(i, j int) bool {
return len(clusters[i].members) > len(clusters[j].members)
})

stanceLabels := []string{"görüş 1", "görüş 2", "görüş 3", "görüş 4", "görüş 5"}
viewpoints := make([]model.Viewpoint, 0, len(clusters))
for i, c := range clusters {
if len(c.members) == 0 {
continue
}
label := stanceLabels[0]
if i < len(stanceLabels) {
label = stanceLabels[i]
}
rep := longestEntry(c.members)
viewpoints = append(viewpoints, model.Viewpoint{
Stance:              label,
RepresentativeQuote: truncate(rep.Text, 300),
Author:              rep.Author,
EntryCount:          len(c.members),
})
}
return viewpoints
}

// splitPositional divides the entry list into k roughly equal segments.
func splitPositional(entries []model.Entry, k int) []model.Viewpoint {
stanceLabels := []string{"görüş 1", "görüş 2", "görüş 3"}
size := len(entries) / k
if size == 0 {
size = 1
}
viewpoints := make([]model.Viewpoint, 0, k)
for i := 0; i < k && i*size < len(entries); i++ {
end := (i + 1) * size
if end > len(entries) {
end = len(entries)
}
chunk := entries[i*size : end]
rep := longestEntry(chunk)
label := stanceLabels[0]
if i < len(stanceLabels) {
label = stanceLabels[i]
}
viewpoints = append(viewpoints, model.Viewpoint{
Stance:              label,
RepresentativeQuote: truncate(rep.Text, 300),
Author:              rep.Author,
EntryCount:          len(chunk),
})
}
return viewpoints
}

func cosineSim(a, b []float64) float64 {
if len(a) != len(b) {
return 0
}
var dot, normA, normB float64
for i := range a {
dot += a[i] * b[i]
normA += a[i] * a[i]
normB += b[i] * b[i]
}
if normA == 0 || normB == 0 {
return 0
}
return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func toFloat64(v []float32) []float64 {
out := make([]float64, len(v))
for i, f := range v {
out[i] = float64(f)
}
return out
}

func longestEntry(entries []model.Entry) model.Entry {
best := entries[0]
for _, e := range entries[1:] {
if len(e.Text) > len(best.Text) {
best = e
}
}
return best
}

func truncate(s string, max int) string {
runes := []rune(s)
if len(runes) <= max {
return s
}
return string(runes[:max]) + "\u2026"
}
