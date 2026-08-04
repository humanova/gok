package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type layoutNode struct {
	ID          uint64
	Title       string
	CommunityID int
	Region      string
	Degree      int
	X           float64
	Y           float64
}

type layoutEdge struct {
	SourceID uint64
	TargetID uint64
	Weight   float64
}

type layoutSummary struct {
	GeneratedAt             time.Time `json:"generated_at"`
	Nodes                   int       `json:"nodes"`
	Edges                   int       `json:"edges"`
	Communities             int       `json:"communities"`
	Regions                 int       `json:"regions"`
	Iterations              int       `json:"iterations"`
	WeightedMeanEdgeLength  float64   `json:"weighted_mean_edge_length"`
	MeanRandomPairLength    float64   `json:"mean_random_pair_length"`
	EdgeToRandomRatio       float64   `json:"edge_to_random_ratio"`
	MeanIntraCommunityRange float64   `json:"mean_intra_community_range"`
	Bounds                  bounds    `json:"bounds"`
}

type bounds struct {
	MinX float64 `json:"min_x"`
	MaxX float64 `json:"max_x"`
	MinY float64 `json:"min_y"`
	MaxY float64 `json:"max_y"`
}

type semanticAssignment struct {
	CommunityID int    `json:"community_id"`
	Region      string `json:"region"`
}

type semanticReport struct {
	Assignments []semanticAssignment `json:"assignments"`
}

type nodeSemanticAssignment struct {
	NodeID uint64 `json:"node_id"`
	Region string `json:"region"`
}

func main() {
	nodesPath := flag.String("nodes", "reports/map-clusters-final-20260731/nodes.csv", "Clustered node CSV")
	edgesPath := flag.String("edges", "reports/map-clusters-final-20260731/edges.csv", "Clustered edge CSV")
	communityRegionsPath := flag.String("community-regions", "reports/map-reconciliation-20260731/semantic-regions.json", "Community semantic-region JSON fallback")
	nodeRegionsPath := flag.String("node-regions", "reports/map-node-reconciliation-20260731/node-regions.json", "Audited node semantic-region JSON")
	iterations := flag.Int("iterations", 350, "Force-layout iterations")
	outDir := flag.String("out", "", "Output directory; default: reports/map-layout-YYYYMMDD-HHMMSS")
	flag.Parse()
	if *iterations < 50 || *iterations > 2000 {
		fmt.Fprintln(os.Stderr, "iterations must be between 50 and 2000")
		os.Exit(2)
	}

	now := time.Now().UTC()
	if *outDir == "" {
		*outDir = filepath.Join("reports", "map-layout-"+now.Format("20060102-150405"))
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output directory: %v\n", err)
		os.Exit(1)
	}

	nodes, err := readNodes(*nodesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read nodes: %v\n", err)
		os.Exit(1)
	}
	communityRegions, err := readRegions(*communityRegionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read community regions: %v\n", err)
		os.Exit(1)
	}
	nodeRegions, err := readNodeRegions(*nodeRegionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read node regions: %v\n", err)
		os.Exit(1)
	}
	for _, node := range nodes {
		node.Region = nodeRegions[node.ID]
		if node.Region == "" {
			node.Region = communityRegions[node.CommunityID]
		}
		if node.Region == "" {
			node.Region = fmt.Sprintf("community-%d", node.CommunityID)
		}
	}
	edges, err := readEdges(*edgesPath, nodes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read edges: %v\n", err)
		os.Exit(1)
	}
	if len(nodes) == 0 || len(edges) == 0 {
		fmt.Fprintln(os.Stderr, "layout requires non-empty nodes and retained edges")
		os.Exit(1)
	}

	initializePositions(nodes)
	runLayout(nodes, edges, *iterations)
	summary := measureLayout(now, nodes, edges, *iterations)
	if err := writeLayout(filepath.Join(*outDir, "layout.csv"), nodes); err != nil {
		fmt.Fprintf(os.Stderr, "write layout: %v\n", err)
		os.Exit(1)
	}
	if err := writeJSON(filepath.Join(*outDir, "summary.json"), summary); err != nil {
		fmt.Fprintf(os.Stderr, "write summary: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("layout complete: nodes=%d edges=%d ratio=%.3f out=%s\n", len(nodes), len(edges), summary.EdgeToRandomRatio, *outDir)
}

func readNodes(path string) (map[uint64]*layoutNode, error) {
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
	indexes, err := columnIndexes(header, "topic_id", "title", "retained_degree", "community_id")
	if err != nil {
		return nil, err
	}
	nodes := make(map[uint64]*layoutNode)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		id, err := strconv.ParseUint(record[indexes["topic_id"]], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse topic id: %w", err)
		}
		degree, err := strconv.Atoi(record[indexes["retained_degree"]])
		if err != nil {
			return nil, fmt.Errorf("parse retained degree for %d: %w", id, err)
		}
		communityID, err := strconv.Atoi(record[indexes["community_id"]])
		if err != nil {
			return nil, fmt.Errorf("parse community id for %d: %w", id, err)
		}
		nodes[id] = &layoutNode{ID: id, Title: record[indexes["title"]], Degree: degree, CommunityID: communityID}
	}
	return nodes, nil
}

func readEdges(path string, nodes map[uint64]*layoutNode) ([]layoutEdge, error) {
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
	indexes, err := columnIndexes(header, "source_id", "target_id", "weighted_jaccard", "mutual_top_neighbor")
	if err != nil {
		return nil, err
	}
	edges := make([]layoutEdge, 0)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if record[indexes["mutual_top_neighbor"]] != "true" {
			continue
		}
		sourceID, err := strconv.ParseUint(record[indexes["source_id"]], 10, 64)
		if err != nil {
			return nil, err
		}
		targetID, err := strconv.ParseUint(record[indexes["target_id"]], 10, 64)
		if err != nil {
			return nil, err
		}
		if nodes[sourceID] == nil || nodes[targetID] == nil {
			continue
		}
		weight, err := strconv.ParseFloat(record[indexes["weighted_jaccard"]], 64)
		if err != nil {
			return nil, err
		}
		edges = append(edges, layoutEdge{SourceID: sourceID, TargetID: targetID, Weight: weight})
	}
	return edges, nil
}

func readRegions(path string) (map[int]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var report semanticReport
	if err := json.NewDecoder(file).Decode(&report); err != nil {
		return nil, err
	}
	regions := make(map[int]string, len(report.Assignments))
	for _, assignment := range report.Assignments {
		if assignment.CommunityID <= 0 || strings.TrimSpace(assignment.Region) == "" {
			return nil, fmt.Errorf("invalid semantic assignment for community %d", assignment.CommunityID)
		}
		if _, exists := regions[assignment.CommunityID]; exists {
			return nil, fmt.Errorf("duplicate semantic assignment for community %d", assignment.CommunityID)
		}
		regions[assignment.CommunityID] = assignment.Region
	}
	return regions, nil
}

func readNodeRegions(path string) (map[uint64]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var report struct {
		Assignments []nodeSemanticAssignment `json:"assignments"`
	}
	if err := json.NewDecoder(file).Decode(&report); err != nil {
		return nil, err
	}
	regions := make(map[uint64]string, len(report.Assignments))
	for _, assignment := range report.Assignments {
		if assignment.NodeID == 0 || strings.TrimSpace(assignment.Region) == "" {
			return nil, fmt.Errorf("invalid node semantic assignment for %d", assignment.NodeID)
		}
		if _, exists := regions[assignment.NodeID]; exists {
			return nil, fmt.Errorf("duplicate node semantic assignment for %d", assignment.NodeID)
		}
		regions[assignment.NodeID] = assignment.Region
	}
	return regions, nil
}

func columnIndexes(header []string, required ...string) (map[string]int, error) {
	indexes := make(map[string]int, len(header))
	for index, column := range header {
		indexes[column] = index
	}
	for _, column := range required {
		if _, ok := indexes[column]; !ok {
			return nil, fmt.Errorf("missing %q column", column)
		}
	}
	return indexes, nil
}

func initializePositions(nodes map[uint64]*layoutNode) {
	regions := make(map[string]map[int][]*layoutNode)
	regionNames := make([]string, 0)
	for _, node := range nodes {
		if _, ok := regions[node.Region]; !ok {
			regions[node.Region] = make(map[int][]*layoutNode)
			regionNames = append(regionNames, node.Region)
		}
		regions[node.Region][node.CommunityID] = append(regions[node.Region][node.CommunityID], node)
	}
	sort.Strings(regionNames)
	const goldenAngle = 2.399963229728653
	for regionIndex, region := range regionNames {
		regionAngle := float64(regionIndex) * goldenAngle
		regionRadius := 16 + 9*math.Sqrt(float64(regionIndex))
		regionX, regionY := regionRadius*math.Cos(regionAngle), regionRadius*math.Sin(regionAngle)
		communities := regions[region]
		communityIDs := make([]int, 0, len(communities))
		for communityID := range communities {
			communityIDs = append(communityIDs, communityID)
		}
		sort.Ints(communityIDs)
		for communityIndex, communityID := range communityIDs {
			members := communities[communityID]
			communityAngle := float64(communityIndex) * goldenAngle
			communityRadius := 1.5 + 1.75*math.Sqrt(float64(communityIndex))
			centerX := regionX + communityRadius*math.Cos(communityAngle)
			centerY := regionY + communityRadius*math.Sin(communityAngle)
			for _, node := range members {
				nodeAngle := hashUnit(node.ID) * 2 * math.Pi
				nodeRadius := 0.45 + 0.65*math.Sqrt(float64(len(members)))*hashUnit(node.ID^0x9e3779b97f4a7c15)
				node.X = centerX + nodeRadius*math.Cos(nodeAngle)
				node.Y = centerY + nodeRadius*math.Sin(nodeAngle)
			}
		}
	}
}

func runLayout(nodes map[uint64]*layoutNode, edges []layoutEdge, iterations int) {
	ordered := orderedNodes(nodes)
	indexByID := make(map[uint64]int, len(ordered))
	for index, node := range ordered {
		indexByID[node.ID] = index
	}
	for iteration := 0; iteration < iterations; iteration++ {
		dx := make([]float64, len(ordered))
		dy := make([]float64, len(ordered))
		communityCenters := calculateCommunityCenters(ordered)
		regionCenters := calculateRegionCenters(ordered)

		for left := 0; left < len(ordered); left++ {
			for right := left + 1; right < len(ordered); right++ {
				xDelta, yDelta := ordered[left].X-ordered[right].X, ordered[left].Y-ordered[right].Y
				distanceSquared := xDelta*xDelta + yDelta*yDelta
				if distanceSquared < 0.0025 {
					xDelta += 0.05
					yDelta += 0.03
					distanceSquared = xDelta*xDelta + yDelta*yDelta
				}
				distance := math.Sqrt(distanceSquared)
				force := 1.8 / distanceSquared
				xForce, yForce := force*xDelta/distance, force*yDelta/distance
				dx[left] += xForce
				dy[left] += yForce
				dx[right] -= xForce
				dy[right] -= yForce
			}
		}

		for _, edge := range edges {
			left, right := indexByID[edge.SourceID], indexByID[edge.TargetID]
			xDelta, yDelta := ordered[right].X-ordered[left].X, ordered[right].Y-ordered[left].Y
			distance := math.Hypot(xDelta, yDelta)
			if distance < 0.05 {
				distance = 0.05
			}
			strength := 0.5 + math.Sqrt(20*edge.Weight)
			if ordered[left].Region != ordered[right].Region && (ordered[left].Degree <= 1 || ordered[right].Degree <= 1) {
				strength *= 0.18
			}
			desiredDistance := 3.2 / strength
			force := 0.055 * strength * (distance - desiredDistance)
			xForce, yForce := force*xDelta/distance, force*yDelta/distance
			dx[left] += xForce
			dy[left] += yForce
			dx[right] -= xForce
			dy[right] -= yForce
		}

		for index, node := range ordered {
			center := communityCenters[node.CommunityID]
			regionCenter := regionCenters[node.Region]
			dx[index] += 0.006 * (center.X - node.X)
			dy[index] += 0.006 * (center.Y - node.Y)
			semanticStrength := 0.004 + 0.020/float64(node.Degree+1)
			dx[index] += semanticStrength * (regionCenter.X - node.X)
			dy[index] += semanticStrength * (regionCenter.Y - node.Y)
			dx[index] -= 0.0012 * node.X
			dy[index] -= 0.0012 * node.Y
		}

		temperature := 0.45*(1-float64(iteration)/float64(iterations)) + 0.025
		for index, node := range ordered {
			step := math.Hypot(dx[index], dy[index])
			if step > temperature {
				dx[index] *= temperature / step
				dy[index] *= temperature / step
			}
			node.X += dx[index]
			node.Y += dy[index]
		}
	}
}

func calculateCommunityCenters(nodes []*layoutNode) map[int]layoutNode {
	centers := make(map[int]layoutNode)
	counts := make(map[int]int)
	for _, node := range nodes {
		center := centers[node.CommunityID]
		center.X += node.X
		center.Y += node.Y
		centers[node.CommunityID] = center
		counts[node.CommunityID]++
	}
	for communityID, center := range centers {
		center.X /= float64(counts[communityID])
		center.Y /= float64(counts[communityID])
		centers[communityID] = center
	}
	return centers
}

func calculateRegionCenters(nodes []*layoutNode) map[string]layoutNode {
	centers := make(map[string]layoutNode)
	counts := make(map[string]int)
	for _, node := range nodes {
		center := centers[node.Region]
		center.X += node.X
		center.Y += node.Y
		centers[node.Region] = center
		counts[node.Region]++
	}
	for region, center := range centers {
		center.X /= float64(counts[region])
		center.Y /= float64(counts[region])
		centers[region] = center
	}
	return centers
}

func measureLayout(now time.Time, nodes map[uint64]*layoutNode, edges []layoutEdge, iterations int) layoutSummary {
	ordered := orderedNodes(nodes)
	weightTotal, edgeLengthTotal := 0.0, 0.0
	edgeSet := make(map[[2]uint64]struct{}, len(edges))
	for _, edge := range edges {
		source, target := edge.SourceID, edge.TargetID
		if source > target {
			source, target = target, source
		}
		edgeSet[[2]uint64{source, target}] = struct{}{}
		length := distance(nodes[edge.SourceID], nodes[edge.TargetID])
		weightTotal += edge.Weight
		edgeLengthTotal += edge.Weight * length
	}

	randomLengthTotal := 0.0
	randomPairs := 0
	seed := uint64(0x6a09e667f3bcc909)
	for randomPairs < len(edges) {
		seed = nextRandom(seed)
		left := int(seed % uint64(len(ordered)))
		seed = nextRandom(seed)
		right := int(seed % uint64(len(ordered)))
		if left == right {
			continue
		}
		source, target := ordered[left].ID, ordered[right].ID
		if source > target {
			source, target = target, source
		}
		if _, exists := edgeSet[[2]uint64{source, target}]; exists {
			continue
		}
		randomLengthTotal += distance(ordered[left], ordered[right])
		randomPairs++
	}

	communityRanges := make(map[int]float64)
	communityCounts := make(map[int]int)
	centers := calculateCommunityCenters(ordered)
	for _, node := range ordered {
		center := centers[node.CommunityID]
		communityRanges[node.CommunityID] += distance(node, &center)
		communityCounts[node.CommunityID]++
	}
	meanCommunityRange := 0.0
	for communityID, total := range communityRanges {
		meanCommunityRange += total / float64(communityCounts[communityID])
	}
	meanCommunityRange /= float64(len(communityRanges))

	layoutBounds := bounds{MinX: ordered[0].X, MaxX: ordered[0].X, MinY: ordered[0].Y, MaxY: ordered[0].Y}
	for _, node := range ordered[1:] {
		layoutBounds.MinX = math.Min(layoutBounds.MinX, node.X)
		layoutBounds.MaxX = math.Max(layoutBounds.MaxX, node.X)
		layoutBounds.MinY = math.Min(layoutBounds.MinY, node.Y)
		layoutBounds.MaxY = math.Max(layoutBounds.MaxY, node.Y)
	}
	regions := make(map[string]struct{})
	for _, node := range ordered {
		regions[node.Region] = struct{}{}
	}
	return layoutSummary{GeneratedAt: now, Nodes: len(nodes), Edges: len(edges), Communities: len(communityRanges), Regions: len(regions), Iterations: iterations,
		WeightedMeanEdgeLength: edgeLengthTotal / weightTotal, MeanRandomPairLength: randomLengthTotal / float64(randomPairs),
		EdgeToRandomRatio:       edgeLengthTotal / weightTotal / (randomLengthTotal / float64(randomPairs)),
		MeanIntraCommunityRange: meanCommunityRange, Bounds: layoutBounds}
}

func orderedNodes(nodes map[uint64]*layoutNode) []*layoutNode {
	ordered := make([]*layoutNode, 0, len(nodes))
	for _, node := range nodes {
		ordered = append(ordered, node)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	return ordered
}

func hashUnit(value uint64) float64 {
	value ^= value >> 30
	value *= 0xbf58476d1ce4e5b9
	value ^= value >> 27
	value *= 0x94d049bb133111eb
	value ^= value >> 31
	return float64(value>>11) / (1 << 53)
}

func nextRandom(value uint64) uint64 {
	return value*6364136223846793005 + 1442695040888963407
}

func distance(left, right *layoutNode) float64 {
	return math.Hypot(left.X-right.X, left.Y-right.Y)
}

func writeLayout(path string, nodes map[uint64]*layoutNode) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"topic_id", "title", "community_id", "region", "retained_degree", "x", "y"}); err != nil {
		return err
	}
	for _, node := range orderedNodes(nodes) {
		if err := writer.Write([]string{fmt.Sprint(node.ID), node.Title, fmt.Sprint(node.CommunityID), node.Region, fmt.Sprint(node.Degree), fmt.Sprintf("%.8f", node.X), fmt.Sprintf("%.8f", node.Y)}); err != nil {
			return err
		}
	}
	return writer.Error()
}

func writeJSON(path string, value any) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
