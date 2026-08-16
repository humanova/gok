package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gok/internal/config"
	"gok/internal/mapcuration"

	"google.golang.org/genai"
)

type mapNode struct {
	ID          uint64
	Title       string
	CommunityID int
	Degree      int
}

type communityInfo struct {
	Region string
	Topics []string
}

type nodeAssignment struct {
	NodeID     uint64  `json:"node_id"`
	Region     string  `json:"region"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

type nodeModelResponse struct {
	Assignments []nodeAssignment `json:"assignments"`
}

type nodeReport struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Model       string           `json:"model"`
	Regions     []string         `json:"regions"`
	Assignments []nodeAssignment `json:"assignments"`
	TotalNodes  int              `json:"total_nodes"`
}

func main() {
	nodesPath := flag.String("nodes", "", "Clustered node CSV (required)")
	clustersPath := flag.String("clusters", "", "Cluster report CSV (required)")
	communityRegionsPath := flag.String("community-regions", "", "Community semantic-region JSON (required)")
	outDir := flag.String("out", "", "Output directory; default: reports/map-node-reconciliation-YYYYMMDD-HHMMSS")
	batchSize := flag.Int("batch-size", 18, "Nodes per Gemini request")
	flag.Parse()
	if *batchSize < 1 || *batchSize > 24 {
		fmt.Fprintln(os.Stderr, "batch-size must be between 1 and 24")
		os.Exit(2)
	}
	if err := mapcuration.RequirePaths(
		mapcuration.RequiredPath{Flag: "--nodes", Value: *nodesPath},
		mapcuration.RequiredPath{Flag: "--clusters", Value: *clustersPath},
		mapcuration.RequiredPath{Flag: "--community-regions", Value: *communityRegionsPath},
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if config.Config.GeminiApiKey == "" {
		fmt.Fprintln(os.Stderr, "GeminiApiKey is required for node reconciliation")
		os.Exit(1)
	}

	nodes, err := readNodes(*nodesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read nodes: %v\n", err)
		os.Exit(1)
	}
	communities, err := readCommunities(*clustersPath, *communityRegionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read communities: %v\n", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	if *outDir == "" {
		*outDir = filepath.Join("reports", "map-node-reconciliation-"+now.Format("20060102-150405"))
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output directory: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: config.Config.GeminiApiKey})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create Gemini client: %v\n", err)
		os.Exit(1)
	}
	allowed := mapcuration.AllowedRegions()
	assignments := make([]nodeAssignment, 0, len(nodes))
	// Audit every node rather than blindly inheriting its community label: shared
	// participants can group semantically unrelated topics in one community.
	for start := 0; start < len(nodes); start += *batchSize {
		end := min(start+*batchSize, len(nodes))
		fmt.Printf("reconciling nodes %d-%d of %d\n", start+1, end, len(nodes))
		batch, err := classifyBatchWithRetry(ctx, client, config.Config.GeminiModel, nodes[start:end], communities, allowed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "classify nodes %d-%d: %v\n", start+1, end, err)
			os.Exit(1)
		}
		assignments = append(assignments, batch...)
	}
	sort.Slice(assignments, func(left, right int) bool { return assignments[left].NodeID < assignments[right].NodeID })
	report := nodeReport{GeneratedAt: now, Model: config.Config.GeminiModel, Regions: mapcuration.SortedRegionKeys(allowed), Assignments: assignments, TotalNodes: len(nodes)}
	if err := mapcuration.WriteJSON(filepath.Join(*outDir, "node-regions.json"), report); err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("node reconciliation complete: nodes=%d out=%s\n", len(assignments), *outDir)
}

func readNodes(path string) ([]mapNode, error) {
	graphNodes, err := mapcuration.ReadGraphNodes(path)
	if err != nil {
		return nil, err
	}
	nodes := make([]mapNode, 0, len(graphNodes))
	for _, graphNode := range graphNodes {
		nodes = append(nodes, mapNode{ID: graphNode.ID, Title: graphNode.Title, CommunityID: graphNode.CommunityID, Degree: graphNode.Degree})
	}
	return nodes, nil
}

func readCommunities(clustersPath, assignmentsPath string) (map[int]communityInfo, error) {
	graphCommunities, err := mapcuration.ReadCommunities(clustersPath)
	if err != nil {
		return nil, err
	}
	communities := make(map[int]communityInfo, len(graphCommunities))
	for _, community := range graphCommunities {
		communities[community.ID] = communityInfo{Topics: community.RepresentativeTopics}
	}
	regions, err := mapcuration.ReadCommunityRegions(assignmentsPath)
	if err != nil {
		return nil, err
	}
	for communityID, region := range regions {
		info, ok := communities[communityID]
		if !ok {
			return nil, fmt.Errorf("assignment for unknown community %d", communityID)
		}
		info.Region = region
		communities[communityID] = info
	}
	return communities, nil
}

func classifyBatch(ctx context.Context, client *genai.Client, model string, nodes []mapNode, communities map[int]communityInfo, allowed map[string]struct{}) ([]nodeAssignment, error) {
	var prompt strings.Builder
	prompt.WriteString("Ekşi Sözlük konu haritasında HER TEK TEK BAŞLIĞI semantik bölgeye atıyorsun. Davranışsal topluluğun varsayılan bölgesi verilmiştir, ancak başlık kendi anlamı açıkça farklıysa onu düzelt. Örnek: `antalya` futbol başlıklarıyla davranışsal toplulukta olsa bile local_life; Galatasaray football; bir şehir local_life; bir oyuncu football. Belirsiz başlıklarda topluluk bağlamını kullan.\n\nİzin verilen bölgeler:\n")
	prompt.WriteString(mapcuration.RegionDefinitions())
	prompt.WriteString("\nSadece JSON: {\"assignments\":[{\"node_id\":1,\"region\":\"football\",\"confidence\":0.0,\"reason\":\"en fazla 10 Türkçe kelime\"}]}. Her node_id tam olarak bir kez dönmeli; region izin verilenlerden biri; confidence 0-1 arası sayı olmalı.\n\nBaşlıklar:\n")
	for _, node := range nodes {
		community := communities[node.CommunityID]
		// Limit context to the strongest representative titles so a large community
		// does not crowd the node's own title out of the request.
		contextTopics := community.Topics
		if len(contextTopics) > 4 {
			contextTopics = contextTopics[:4]
		}
		prompt.WriteString(fmt.Sprintf("%d | başlık: %s | topluluk bölgesi: %s | topluluk bağlamı: %s\n", node.ID, node.Title, community.Region, strings.Join(contextTopics, " | ")))
	}
	var decoded nodeModelResponse
	if err := mapcuration.GenerateJSON(ctx, client, model, prompt.String(), &decoded); err != nil {
		return nil, err
	}
	// Require one valid decision for every requested node before persisting a batch.
	wanted := make(map[uint64]struct{}, len(nodes))
	for _, node := range nodes {
		wanted[node.ID] = struct{}{}
	}
	if len(decoded.Assignments) != len(wanted) {
		return nil, fmt.Errorf("expected %d assignments, got %d", len(wanted), len(decoded.Assignments))
	}
	seen := make(map[uint64]struct{}, len(decoded.Assignments))
	for index := range decoded.Assignments {
		assignment := &decoded.Assignments[index]
		if _, ok := wanted[assignment.NodeID]; !ok {
			return nil, fmt.Errorf("unexpected node_id %d", assignment.NodeID)
		}
		if _, duplicate := seen[assignment.NodeID]; duplicate {
			return nil, fmt.Errorf("duplicate node_id %d", assignment.NodeID)
		}
		seen[assignment.NodeID] = struct{}{}
		if _, ok := allowed[assignment.Region]; !ok {
			return nil, fmt.Errorf("invalid region %q for node %d", assignment.Region, assignment.NodeID)
		}
		if assignment.Confidence < 0 || assignment.Confidence > 1 {
			return nil, fmt.Errorf("invalid confidence %.2f for node %d", assignment.Confidence, assignment.NodeID)
		}
		assignment.Reason = strings.TrimSpace(assignment.Reason)
	}
	return decoded.Assignments, nil
}

func classifyBatchWithRetry(ctx context.Context, client *genai.Client, model string, nodes []mapNode, communities map[int]communityInfo, allowed map[string]struct{}) ([]nodeAssignment, error) {
	return mapcuration.Retry(3, func(attempt int) ([]nodeAssignment, error) {
		assignments, err := classifyBatch(ctx, client, model, nodes, communities, allowed)
		if err == nil {
			return assignments, nil
		}
		if attempt < 3 {
			fmt.Printf("retrying malformed node batch (attempt %d): %v\n", attempt+1, err)
		}
		return nil, err
	})
}
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
