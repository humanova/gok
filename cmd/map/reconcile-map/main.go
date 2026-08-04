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
- news_events: Belirli güncel olaylar; yalnızca başka bir bölge daha açıklayıcı değilse.
- other: Güvenli biçimde sınıflandırılamayan veya karma kümeler.
`

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
	clustersPath := flag.String("clusters", "reports/map-clusters-final-20260731/clusters.csv", "Cluster report CSV")
	outDir := flag.String("out", "", "Output directory; default: reports/map-reconciliation-YYYYMMDD-HHMMSS")
	batchSize := flag.Int("batch-size", 18, "Communities per Gemini request")
	maxTopics := flag.Int("topics-per-community", 8, "Representative topics supplied per community")
	flag.Parse()
	if *batchSize < 1 || *batchSize > 30 || *maxTopics < 1 || *maxTopics > 12 {
		fmt.Fprintln(os.Stderr, "invalid reconciliation parameters")
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

	allowed := allowedRegions()
	allAssignments := make([]assignment, 0, len(clusters))
	for start := 0; start < len(clusters); start += *batchSize {
		end := min(start+*batchSize, len(clusters))
		fmt.Printf("reconciling communities %d-%d of %d\n", start+1, end, len(clusters))
		assignments, err := classifyBatch(ctx, client, config.Config.GeminiModel, clusters[start:end], allowed)
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
		Regions:       sortedRegions(allowed),
		Assignments:   allAssignments,
		TotalClusters: len(clusters),
	}
	if err := writeReport(filepath.Join(*outDir, "semantic-regions.json"), report); err != nil {
		fmt.Fprintf(os.Stderr, "write reconciliation report: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("semantic reconciliation complete: communities=%d out=%s\n", len(allAssignments), *outDir)
}

func readClusters(path string, maxTopics int) ([]clusterInput, error) {
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
	index := make(map[string]int, len(header))
	for i, column := range header {
		index[column] = i
	}
	for _, required := range []string{"community_id", "size", "representative_topics"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("missing %q column", required)
		}
	}
	clusters := make([]clusterInput, 0)
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
		size, err := strconv.Atoi(record[index["size"]])
		if err != nil {
			return nil, err
		}
		topics := strings.Split(record[index["representative_topics"]], " | ")
		if len(topics) > maxTopics {
			topics = topics[:maxTopics]
		}
		clusters = append(clusters, clusterInput{ID: id, Size: size, Topics: topics})
	}
	return clusters, nil
}

func classifyBatch(ctx context.Context, client *genai.Client, model string, clusters []clusterInput, allowed map[string]struct{}) ([]assignment, error) {
	var prompt strings.Builder
	prompt.WriteString("Ekşi Sözlük topluluk haritasında ikinci seviye semantik bölgeler oluşturuyorsun. Her topluluk davranışsal olarak oluşmuştur; başlıkların konu alanını en uygun BİR bölgeye ata. Aynı alanın alt topluluklarını aynı bölgeye koy: örneğin Galatasaray, Beşiktaş, Fenerbahçe, milli takım ve dünya futbolu football olmalıdır. Tek başına kalan topluluklar da en yakın anlamlı bölgeye atanmalıdır. Başlıkların dışındaki olayları varsayma.\n\n")
	prompt.WriteString("İzin verilen bölgeler:\n")
	prompt.WriteString(regionDefinitions)
	prompt.WriteString("\nSadece geçerli JSON döndür: {\"assignments\":[{\"community_id\":1,\"region\":\"football\",\"confidence\":0.0,\"reason\":\"en fazla 12 Türkçe kelime\"}]}. Her verilen community_id tam olarak bir kez dönecek. confidence 0 ile 1 arasında sayı olmalı.\n\nTopluluklar:\n")
	for _, cluster := range clusters {
		prompt.WriteString(fmt.Sprintf("%d (boyut %d): %s\n", cluster.ID, cluster.Size, strings.Join(cluster.Topics, " | ")))
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
	var decoded modelResponse
	if err := json.Unmarshal([]byte(raw.String()), &decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
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
	keys := make([]string, 0, len(regions))
	for key := range regions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeReport(path string, report reconciliationReport) error {
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
