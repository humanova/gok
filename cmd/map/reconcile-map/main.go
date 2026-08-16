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

type clusterInput struct {
	ID     int
	Size   int
	Topics []string
}

type assignment struct {
	CommunityID int     `json:"community_id"`
	Region      string  `json:"region"`
	Confidence  float64 `json:"confidence"`
	Reason      string  `json:"reason"`
}

type modelResponse struct {
	Assignments []assignment `json:"assignments"`
}

type reconciliationReport struct {
	GeneratedAt   time.Time    `json:"generated_at"`
	Model         string       `json:"model"`
	Regions       []string     `json:"regions"`
	Assignments   []assignment `json:"assignments"`
	TotalClusters int          `json:"total_clusters"`
}

func main() {
	clustersPath := flag.String("clusters", "", "Cluster report CSV (required)")
	outDir := flag.String("out", "", "Output directory; default: reports/map-reconciliation-YYYYMMDD-HHMMSS")
	batchSize := flag.Int("batch-size", 18, "Communities per Gemini request")
	maxTopics := flag.Int("topics-per-community", 8, "Representative topics supplied per community")
	flag.Parse()
	if *batchSize < 1 || *batchSize > 30 || *maxTopics < 1 || *maxTopics > 12 {
		fmt.Fprintln(os.Stderr, "invalid reconciliation parameters")
		os.Exit(2)
	}
	if err := mapcuration.RequirePaths(mapcuration.RequiredPath{Flag: "--clusters", Value: *clustersPath}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if config.Config.GeminiApiKey == "" {
		fmt.Fprintln(os.Stderr, "GeminiApiKey is required for semantic reconciliation")
		os.Exit(1)
	}

	clusters, err := readClusters(*clustersPath, *maxTopics)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read clusters: %v\n", err)
		os.Exit(1)
	}
	if len(clusters) == 0 {
		fmt.Fprintln(os.Stderr, "cluster report is empty")
		os.Exit(1)
	}

	now := time.Now().UTC()
	if *outDir == "" {
		*outDir = filepath.Join("reports", "map-reconciliation-"+now.Format("20060102-150405"))
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
	allAssignments := make([]assignment, 0, len(clusters))
	// Keep batches small enough for reliable structured output while validating
	// every batch independently before it becomes part of the report.
	for start := 0; start < len(clusters); start += *batchSize {
		end := min(start+*batchSize, len(clusters))
		fmt.Printf("reconciling communities %d-%d of %d\n", start+1, end, len(clusters))
		assignments, err := classifyBatchWithRetry(ctx, client, config.Config.GeminiModel, clusters[start:end], allowed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "classify communities %d-%d: %v\n", start+1, end, err)
			os.Exit(1)
		}
		allAssignments = append(allAssignments, assignments...)
	}

	sort.Slice(allAssignments, func(left, right int) bool {
		return allAssignments[left].CommunityID < allAssignments[right].CommunityID
	})
	report := reconciliationReport{
		GeneratedAt:   now,
		Model:         config.Config.GeminiModel,
		Regions:       mapcuration.SortedRegionKeys(allowed),
		Assignments:   allAssignments,
		TotalClusters: len(clusters),
	}
	if err := mapcuration.WriteJSON(filepath.Join(*outDir, "semantic-regions.json"), report); err != nil {
		fmt.Fprintf(os.Stderr, "write reconciliation report: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("semantic reconciliation complete: communities=%d out=%s\n", len(allAssignments), *outDir)
}

func readClusters(path string, maxTopics int) ([]clusterInput, error) {
	communities, err := mapcuration.ReadCommunities(path)
	if err != nil {
		return nil, err
	}
	clusters := make([]clusterInput, 0, len(communities))
	for _, community := range communities {
		topics := community.RepresentativeTopics
		if len(topics) > maxTopics {
			topics = topics[:maxTopics]
		}
		clusters = append(clusters, clusterInput{ID: community.ID, Size: community.Size, Topics: topics})
	}
	return clusters, nil
}

func classifyBatch(ctx context.Context, client *genai.Client, model string, clusters []clusterInput, allowed map[string]struct{}) ([]assignment, error) {
	var prompt strings.Builder
	prompt.WriteString("Ekşi Sözlük topluluk haritasında ikinci seviye semantik bölgeler oluşturuyorsun. Her topluluk davranışsal olarak oluşmuştur; başlıkların konu alanını en uygun BİR bölgeye ata. Aynı alanın alt topluluklarını aynı bölgeye koy: örneğin Galatasaray, Beşiktaş, Fenerbahçe, milli takım ve dünya futbolu football olmalıdır. Tek başına kalan topluluklar da en yakın anlamlı bölgeye atanmalıdır. Başlıkların dışındaki olayları varsayma.\n\n")
	prompt.WriteString("İzin verilen bölgeler:\n")
	prompt.WriteString(mapcuration.RegionDefinitions())
	prompt.WriteString("\nSadece geçerli JSON döndür: {\"assignments\":[{\"community_id\":1,\"region\":\"football\",\"confidence\":0.0,\"reason\":\"en fazla 12 Türkçe kelime\"}]}. Her verilen community_id tam olarak bir kez dönecek. confidence 0 ile 1 arasında sayı olmalı.\n\nTopluluklar:\n")
	for _, cluster := range clusters {
		prompt.WriteString(fmt.Sprintf("%d (boyut %d): %s\n", cluster.ID, cluster.Size, strings.Join(cluster.Topics, " | ")))
	}

	var decoded modelResponse
	if err := mapcuration.GenerateJSON(ctx, client, model, prompt.String(), &decoded); err != nil {
		return nil, err
	}
	// The model may produce valid JSON that is still incomplete or misaligned.
	// Require a bijection so no community is silently omitted or substituted.
	wanted := make(map[int]struct{}, len(clusters))
	for _, cluster := range clusters {
		wanted[cluster.ID] = struct{}{}
	}
	if len(decoded.Assignments) != len(wanted) {
		return nil, fmt.Errorf("expected %d assignments, got %d", len(wanted), len(decoded.Assignments))
	}
	seen := make(map[int]struct{}, len(decoded.Assignments))
	for index := range decoded.Assignments {
		assignment := &decoded.Assignments[index]
		if _, ok := wanted[assignment.CommunityID]; !ok {
			return nil, fmt.Errorf("unexpected community_id %d", assignment.CommunityID)
		}
		if _, duplicate := seen[assignment.CommunityID]; duplicate {
			return nil, fmt.Errorf("duplicate community_id %d", assignment.CommunityID)
		}
		seen[assignment.CommunityID] = struct{}{}
		if _, ok := allowed[assignment.Region]; !ok {
			return nil, fmt.Errorf("invalid region %q for community %d", assignment.Region, assignment.CommunityID)
		}
		if assignment.Confidence < 0 || assignment.Confidence > 1 {
			return nil, fmt.Errorf("invalid confidence %.2f for community %d", assignment.Confidence, assignment.CommunityID)
		}
		assignment.Reason = strings.TrimSpace(assignment.Reason)
	}
	return decoded.Assignments, nil
}

func classifyBatchWithRetry(ctx context.Context, client *genai.Client, model string, clusters []clusterInput, allowed map[string]struct{}) ([]assignment, error) {
	return mapcuration.Retry(3, func(attempt int) ([]assignment, error) {
		assignments, err := classifyBatch(ctx, client, model, clusters, allowed)
		if err == nil {
			return assignments, nil
		}
		if attempt < 3 {
			fmt.Printf("retrying malformed community batch (attempt %d): %v\n", attempt+1, err)
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
