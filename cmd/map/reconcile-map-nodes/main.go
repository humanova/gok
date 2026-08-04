package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gok/internal/config"

	"google.golang.org/genai"
)

const regionDefinitions = `
- football: Futbol kulüpleri, oyuncular, transferler, ligler ve milli takımlar.
- other_sports: Futbol dışı sporlar ve sporcular.
- turkish_politics: Türkiye siyaseti, partiler, liderler ve yerel siyaset.
- world_politics: Uluslararası siyaset, savaşlar, ülkeler ve dış politika.
- relationships: Romantik ilişkiler, flört, evlilik, cinsellik ve toplumsal cinsiyet.
- daily_life: Gündelik hayat, kişisel deneyimler, alışkanlıklar, sorunsallar ve yaşam tavsiyeleri.
- music: Müzik, şarkılar, sanatçılar ve müzik paylaşımı.
- film_tv: Film, dizi, televizyon programları, ünlüler ve popüler eğlence.
- games_tech: Video oyunları, teknoloji, internet ürünleri ve yapay zeka.
- economy: Ekonomi, finans, piyasalar, tüketim ve iş hayatı.
- culture_art: Kitaplar, şiir, tarih, sanat, felsefe ve akademi.
- society_identity: Toplumsal kimlik, din, göç, etik, kültürel tartışmalar ve kolektif meseleler.
- science_health: Bilim, sağlık, eğitim ve pratik bilgi.
- local_life: Şehirler, mekanlar, seyahat, yerel yaşam ve hava durumu.
- media: Haber kuruluşları, yayıncılar, gazeteciler ve medya kişilikleri.
- news_events: Belirli güncel olaylar; yalnızca başka bir bölge daha açıklayıcı değilse.
- other: Güvenli biçimde sınıflandırılamayan veya karma kümeler.
`

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

type communityAssignment struct {
	CommunityID int    `json:"community_id"`
	Region      string `json:"region"`
}

type communityReport struct {
	Assignments []communityAssignment `json:"assignments"`
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
	nodesPath := flag.String("nodes", "reports/map-clusters-final-20260731/nodes.csv", "Clustered node CSV")
	clustersPath := flag.String("clusters", "reports/map-clusters-final-20260731/clusters.csv", "Cluster report CSV")
	communityRegionsPath := flag.String("community-regions", "reports/map-reconciliation-20260731/semantic-regions.json", "Community semantic-region JSON")
	outDir := flag.String("out", "", "Output directory; default: reports/map-node-reconciliation-YYYYMMDD-HHMMSS")
	batchSize := flag.Int("batch-size", 18, "Nodes per Gemini request")
	flag.Parse()
	if *batchSize < 1 || *batchSize > 24 {
		fmt.Fprintln(os.Stderr, "batch-size must be between 1 and 24")
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
	allowed := allowedRegions()
	assignments := make([]nodeAssignment, 0, len(nodes))
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
	report := nodeReport{GeneratedAt: now, Model: config.Config.GeminiModel, Regions: sortedRegions(allowed), Assignments: assignments, TotalNodes: len(nodes)}
	if err := writeReport(filepath.Join(*outDir, "node-regions.json"), report); err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("node reconciliation complete: nodes=%d out=%s\n", len(assignments), *outDir)
}

func readNodes(path string) ([]mapNode, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	index := indexes(header)
	for _, field := range []string{"topic_id", "title", "community_id", "retained_degree"} {
		if _, ok := index[field]; !ok {
			return nil, fmt.Errorf("missing %q column", field)
		}
	}
	nodes := make([]mapNode, 0)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		id, err := strconv.ParseUint(record[index["topic_id"]], 10, 64)
		if err != nil {
			return nil, err
		}
		communityID, err := strconv.Atoi(record[index["community_id"]])
		if err != nil {
			return nil, err
		}
		degree, err := strconv.Atoi(record[index["retained_degree"]])
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, mapNode{ID: id, Title: record[index["title"]], CommunityID: communityID, Degree: degree})
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].ID < nodes[right].ID })
	return nodes, nil
}

func readCommunities(clustersPath, assignmentsPath string) (map[int]communityInfo, error) {
	file, err := os.Open(clustersPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	index := indexes(header)
	communities := make(map[int]communityInfo)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		id, err := strconv.Atoi(record[index["community_id"]])
		if err != nil {
			return nil, err
		}
		communities[id] = communityInfo{Topics: strings.Split(record[index["representative_topics"]], " | ")}
	}
	assignmentFile, err := os.Open(assignmentsPath)
	if err != nil {
		return nil, err
	}
	defer assignmentFile.Close()
	var report communityReport
	if err := json.NewDecoder(assignmentFile).Decode(&report); err != nil {
		return nil, err
	}
	for _, assignment := range report.Assignments {
		info, ok := communities[assignment.CommunityID]
		if !ok {
			return nil, fmt.Errorf("assignment for unknown community %d", assignment.CommunityID)
		}
		info.Region = assignment.Region
		communities[assignment.CommunityID] = info
	}
	return communities, nil
}

func classifyBatch(ctx context.Context, client *genai.Client, model string, nodes []mapNode, communities map[int]communityInfo, allowed map[string]struct{}) ([]nodeAssignment, error) {
	var prompt strings.Builder
	prompt.WriteString("Ekşi Sözlük konu haritasında HER TEK TEK BAŞLIĞI semantik bölgeye atıyorsun. Davranışsal topluluğun varsayılan bölgesi verilmiştir, ancak başlık kendi anlamı açıkça farklıysa onu düzelt. Örnek: `antalya` futbol başlıklarıyla davranışsal toplulukta olsa bile local_life; Galatasaray football; bir şehir local_life; bir oyuncu football. Belirsiz başlıklarda topluluk bağlamını kullan.\n\nİzin verilen bölgeler:\n")
	prompt.WriteString(regionDefinitions)
	prompt.WriteString("\nSadece JSON: {\"assignments\":[{\"node_id\":1,\"region\":\"football\",\"confidence\":0.0,\"reason\":\"en fazla 10 Türkçe kelime\"}]}. Her node_id tam olarak bir kez dönmeli; region izin verilenlerden biri; confidence 0-1 arası sayı olmalı.\n\nBaşlıklar:\n")
	for _, node := range nodes {
		community := communities[node.CommunityID]
		contextTopics := community.Topics
		if len(contextTopics) > 4 {
			contextTopics = contextTopics[:4]
		}
		prompt.WriteString(fmt.Sprintf("%d | başlık: %s | topluluk bölgesi: %s | topluluk bağlamı: %s\n", node.ID, node.Title, community.Region, strings.Join(contextTopics, " | ")))
	}
	contents := []*genai.Content{genai.NewContentFromText(prompt.String(), genai.RoleUser)}
	response, err := client.Models.GenerateContent(ctx, model, contents, &genai.GenerateContentConfig{ResponseMIMEType: "application/json", Temperature: genai.Ptr(float32(0))})
	if err != nil {
		return nil, err
	}
	if len(response.Candidates) == 0 || response.Candidates[0].Content == nil {
		return nil, fmt.Errorf("empty Gemini response")
	}
	var raw strings.Builder
	for _, part := range response.Candidates[0].Content.Parts {
		raw.WriteString(part.Text)
	}
	var decoded nodeModelResponse
	if err := json.Unmarshal([]byte(raw.String()), &decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
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
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		assignments, err := classifyBatch(ctx, client, model, nodes, communities, allowed)
		if err == nil {
			return assignments, nil
		}
		lastErr = err
		if attempt < 3 {
			fmt.Printf("retrying malformed node batch (attempt %d): %v\n", attempt+1, err)
		}
	}
	return nil, lastErr
}

func indexes(header []string) map[string]int {
	out := make(map[string]int, len(header))
	for i, field := range header {
		out[field] = i
	}
	return out
}
func allowedRegions() map[string]struct{} {
	regions := make(map[string]struct{})
	for _, line := range strings.Split(regionDefinitions, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if key, _, found := strings.Cut(line, ":"); found {
			regions[key] = struct{}{}
		}
	}
	return regions
}
func sortedRegions(regions map[string]struct{}) []string {
	out := make([]string, 0, len(regions))
	for region := range regions {
		out = append(out, region)
	}
	sort.Strings(out)
	return out
}
func writeReport(path string, report nodeReport) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
