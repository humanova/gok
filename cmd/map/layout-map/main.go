package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"gok/internal/mapcuration"
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

func main() {
	nodesPath := flag.String("nodes", "", "Clustered node CSV (required)")
	edgesPath := flag.String("edges", "", "Clustered edge CSV (required)")
	communityRegionsPath := flag.String("community-regions", "", "Community semantic-region JSON fallback (required)")
	nodeRegionsPath := flag.String("node-regions", "", "Audited node semantic-region JSON (required)")
	iterations := flag.Int("iterations", 350, "Force-layout iterations")
	outDir := flag.String("out", "", "Output directory; default: reports/map-layout-YYYYMMDD-HHMMSS")
	flag.Parse()
	if *iterations < 50 || *iterations > 2000 {
		fmt.Fprintln(os.Stderr, "iterations must be between 50 and 2000")
		os.Exit(2)
	}
	if err := mapcuration.RequirePaths(
		mapcuration.RequiredPath{Flag: "--nodes", Value: *nodesPath},
		mapcuration.RequiredPath{Flag: "--edges", Value: *edgesPath},
		mapcuration.RequiredPath{Flag: "--community-regions", Value: *communityRegionsPath},
		mapcuration.RequiredPath{Flag: "--node-regions", Value: *nodeRegionsPath},
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	communityRegions, err := mapcuration.ReadCommunityRegions(*communityRegionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read community regions: %v\n", err)
		os.Exit(1)
	}
	nodeRegions, err := mapcuration.ReadNodeRegions(*nodeRegionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read node regions: %v\n", err)
		os.Exit(1)
	}
	for _, node := range nodes {
		// Node-level review wins; a community label remains a fallback for topics
		// whose individual semantic audit did not supply an assignment.
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
	if err := mapcuration.WriteJSON(filepath.Join(*outDir, "summary.json"), summary); err != nil {
		fmt.Fprintf(os.Stderr, "write summary: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("layout complete: nodes=%d edges=%d ratio=%.3f out=%s\n", len(nodes), len(edges), summary.EdgeToRandomRatio, *outDir)
}

func readNodes(path string) (map[uint64]*layoutNode, error) {
	graphNodes, err := mapcuration.ReadGraphNodes(path)
	if err != nil {
		return nil, err
	}
	nodes := make(map[uint64]*layoutNode, len(graphNodes))
	for _, graphNode := range graphNodes {
		nodes[graphNode.ID] = &layoutNode{ID: graphNode.ID, Title: graphNode.Title, Degree: graphNode.Degree, CommunityID: graphNode.CommunityID}
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
	indexes, err := mapcuration.ColumnIndexes(header, "source_id", "target_id", "weighted_jaccard", "mutual_top_neighbor")
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

func initializePositions(nodes map[uint64]*layoutNode) {
	// Seed by semantic region and behavioral community before physical forces run.
	// A deterministic hash keeps repeated builds comparable without a random seed.
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
	regionAnchors := calculateRegionAnchors(ordered)
	for iteration := 0; iteration < iterations; iteration++ {
		dx := make([]float64, len(ordered))
		dy := make([]float64, len(ordered))
		communityCenters := calculateCommunityCenters(ordered)
		regionCenters := calculateRegionCenters(ordered)

		// Repulsion gives every node readable separation, scaled by its visible degree.
		for left := 0; left < len(ordered); left++ {
			for right := left + 1; right < len(ordered); right++ {
				xDelta, yDelta := ordered[left].X-ordered[right].X, ordered[left].Y-ordered[right].Y
				distanceSquared := xDelta*xDelta + yDelta*yDelta
				minimumDistance := 0.45 + 0.12*math.Sqrt(float64(ordered[left].Degree+ordered[right].Degree+2))
				if distanceSquared < minimumDistance*minimumDistance {
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

		// Strong behavioral links pull topics together. Cross-region links remain
		// informative but are weakened so semantic areas do not collapse together.
		for _, edge := range edges {
			left, right := indexByID[edge.SourceID], indexByID[edge.TargetID]
			xDelta, yDelta := ordered[right].X-ordered[left].X, ordered[right].Y-ordered[left].Y
			distance := math.Hypot(xDelta, yDelta)
			if distance < 0.05 {
				distance = 0.05
			}
			strength := 0.5 + math.Sqrt(20*edge.Weight)
			if ordered[left].Region != ordered[right].Region {
				strength *= 0.30
			}
			desiredDistance := 3.2 / strength
			force := 0.055 * strength * (distance - desiredDistance)
			xForce, yForce := force*xDelta/distance, force*yDelta/distance
			dx[left] += xForce
			dy[left] += yForce
			dx[right] -= xForce
			dy[right] -= yForce
		}

		// Community and region pulls preserve the map's two-level organization.
		// Low-degree topics receive more semantic guidance because their graph signal
		// is weaker than that of densely connected topics.
		for index, node := range ordered {
			center := communityCenters[node.CommunityID]
			regionCenter := regionCenters[node.Region]
			regionAnchor := regionAnchors[node.Region]
			dx[index] += 0.006 * (center.X - node.X)
			dy[index] += 0.006 * (center.Y - node.Y)
			semanticStrength := 0.004 + 0.020/float64(node.Degree+1)
			dx[index] += semanticStrength * (regionCenter.X - node.X)
			dy[index] += semanticStrength * (regionCenter.Y - node.Y)
			dx[index] += 0.0025 * (regionAnchor.X - node.X)
			dy[index] += 0.0025 * (regionAnchor.Y - node.Y)
			dx[index] -= 0.0012 * node.X
			dy[index] -= 0.0012 * node.Y
		}

		// Cooling prevents late iterations from undoing the coarse structure formed
		// by the earlier, larger force updates.
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

func calculateRegionAnchors(nodes []*layoutNode) map[string]layoutNode {
	counts := make(map[string]int)
	for _, node := range nodes {
		counts[node.Region]++
	}
	regions := make([]string, 0, len(counts))
	for region := range counts {
		regions = append(regions, region)
	}
	sort.Strings(regions)
	anchors := make(map[string]layoutNode, len(regions))
	const goldenAngle = 2.399963229728653
	for index, region := range regions {
		populationRadius := 8 * math.Sqrt(float64(counts[region]))
		radius := 22 + 9*math.Sqrt(float64(index)) + populationRadius
		angle := float64(index) * goldenAngle
		anchors[region] = layoutNode{X: radius * math.Cos(angle), Y: radius * math.Sin(angle)}
	}
	return anchors
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

	// Compare retained links with the same number of non-edge pairs. A fixed PRNG
	// seed keeps this quality measurement reproducible between runs.
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
