package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gok/internal/model"
)

type mapNode struct {
	TopicID            uint64  `json:"topic_id"`
	Title              string  `json:"title"`
	URL                string  `json:"url"`
	LastEntryAt        int64   `json:"last_entry_at"`
	Entries            int64   `json:"entries"`
	DistinctAuthors    int64   `json:"distinct_authors"`
	ReturningAuthors   int64   `json:"returning_authors"`
	ActiveWeeks        int64   `json:"active_weeks"`
	ActiveMonths       int64   `json:"active_months"`
	PeakWeekShare      float64 `json:"peak_week_share"`
	RecentAuthors      int     `json:"recent_authors"`
	EffectiveAuthors   int     `json:"effective_authors"`
	RetainedDegree     int     `json:"retained_degree"`
	WeightedDegree     float64 `json:"weighted_degree"`
	ConnectedComponent int     `json:"connected_component"`
	CommunityID        int     `json:"community_id"`
}

type authorTopicRow struct {
	Author  string
	TopicID uint64
}

type mapEdge struct {
	SourceID          uint64  `json:"source_id"`
	TargetID          uint64  `json:"target_id"`
	SharedAuthors     int     `json:"shared_authors"`
	WeightedShared    float64 `json:"weighted_shared"`
	WeightedJaccard   float64 `json:"weighted_jaccard"`
	SourceRank        int     `json:"source_rank"`
	TargetRank        int     `json:"target_rank"`
	MutualTopNeighbor bool    `json:"mutual_top_neighbor"`
}

type graphSummary struct {
	GeneratedAt               time.Time `json:"generated_at"`
	EdgeWindowStart           time.Time `json:"edge_window_start"`
	EligibleRecentNodes       int       `json:"eligible_recent_nodes"`
	RecentAuthors             int       `json:"recent_authors"`
	EffectiveAuthors          int       `json:"effective_authors"`
	IgnoredBroadAuthors       int       `json:"ignored_broad_authors"`
	CandidateEdges            int       `json:"candidate_edges"`
	EdgesAfterSharedThreshold int       `json:"edges_after_shared_threshold"`
	MutualEdges               int       `json:"mutual_edges"`
	ConnectedComponents       int       `json:"connected_components"`
	LargestComponent          int       `json:"largest_component"`
	MinSharedAuthors          int       `json:"min_shared_authors"`
	MaxAuthorTopics           int       `json:"max_author_topics"`
	TopNeighbors              int       `json:"top_neighbors"`
	Communities               int       `json:"communities"`
	LargestCommunity          int       `json:"largest_community"`
	CommunityIterations       int       `json:"community_iterations"`
}

type communitySummary struct {
	CommunityID          int      `json:"community_id"`
	Size                 int      `json:"size"`
	InternalEdgeWeight   float64  `json:"internal_edge_weight"`
	RepresentativeTopics []string `json:"representative_topics"`
}

const (
	minDistinctAuthors         = 30
	minReturningAuthors        = 6
	minActiveMonths            = 6
	maxPeakWeekShare           = 0.50
	minSpikeExceptionMonths    = 18
	minSpikeExceptionReturners = 15
)

func main() {
	edgeDays := flag.Int("edge-days", 548, "Recent writer-overlap window in days")
	minSharedAuthors := flag.Int("min-shared-authors", 3, "Minimum shared writers before ranking edges")
	maxAuthorTopics := flag.Int("max-author-topics", 60, "Ignore authors active in more eligible topics than this")
	topNeighbors := flag.Int("top-neighbors", 8, "Retain only mutual top-N neighbors per topic")
	profilePath := flag.String("profile", "reports/map-profile-all-history-20260731/topics.csv", "All-history topic profile CSV")
	outDir := flag.String("out", "", "Output directory; default: reports/map-edges-YYYYMMDD-HHMMSS")
	flag.Parse()

	if *edgeDays < 30 || *minSharedAuthors < 1 || *maxAuthorTopics < 2 || *topNeighbors < 1 {
		fmt.Fprintln(os.Stderr, "invalid graph parameters")
		os.Exit(2)
	}
	if err := model.InitDb(); err != nil {
		slog.Error("couldn't connect to database", "error", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	edgeSince := now.AddDate(0, 0, -*edgeDays).Unix()
	if *outDir == "" {
		*outDir = filepath.Join("reports", "map-edges-"+now.Format("20060102-150405"))
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		slog.Error("couldn't create output directory", "error", err)
		os.Exit(1)
	}

	slog.Info("loading durable, recently active topics", "edge_since", edgeSince)
	nodes, err := loadEligibleNodes(*profilePath, edgeSince)
	if err != nil {
		slog.Error("couldn't load map nodes", "error", err)
		os.Exit(1)
	}
	if len(nodes) == 0 {
		slog.Error("no eligible nodes found")
		os.Exit(1)
	}

	rows, err := loadRecentAuthorTopics(nodes, edgeSince)
	if err != nil {
		slog.Error("couldn't load writer-topic pairs", "error", err)
		os.Exit(1)
	}
	edges, effectiveAuthors, ignoredBroadAuthors := buildEdges(rows, nodes, *minSharedAuthors, *maxAuthorTopics)
	applyMutualRanks(edges, *topNeighbors)
	components, largestComponent := annotateNodes(nodes, edges)
	communities, communityIterations := detectCommunities(nodes, edges, 50)

	if err := writeNodes(filepath.Join(*outDir, "nodes.csv"), nodes); err != nil {
		slog.Error("couldn't write node report", "error", err)
		os.Exit(1)
	}
	if err := writeEdges(filepath.Join(*outDir, "edges.csv"), edges, nodes); err != nil {
		slog.Error("couldn't write edge report", "error", err)
		os.Exit(1)
	}
	if err := writeCommunities(filepath.Join(*outDir, "clusters.csv"), communities); err != nil {
		slog.Error("couldn't write community report", "error", err)
		os.Exit(1)
	}

	mutualEdges := 0
	for _, edge := range edges {
		if edge.MutualTopNeighbor {
			mutualEdges++
		}
	}
	summary := graphSummary{
		GeneratedAt:               now,
		EdgeWindowStart:           time.Unix(edgeSince, 0).UTC(),
		EligibleRecentNodes:       len(nodes),
		RecentAuthors:             countAuthors(rows),
		EffectiveAuthors:          effectiveAuthors,
		IgnoredBroadAuthors:       ignoredBroadAuthors,
		CandidateEdges:            countCandidateEdges(rows, *maxAuthorTopics),
		EdgesAfterSharedThreshold: len(edges),
		MutualEdges:               mutualEdges,
		ConnectedComponents:       components,
		LargestComponent:          largestComponent,
		MinSharedAuthors:          *minSharedAuthors,
		MaxAuthorTopics:           *maxAuthorTopics,
		TopNeighbors:              *topNeighbors,
		Communities:               len(communities),
		LargestCommunity:          communities[0].Size,
		CommunityIterations:       communityIterations,
	}
	if err := writeJSON(filepath.Join(*outDir, "summary.json"), summary); err != nil {
		slog.Error("couldn't write summary", "error", err)
		os.Exit(1)
	}
	slog.Info("map edge profile complete", "nodes", len(nodes), "edges", len(edges), "mutual_edges", mutualEdges, "communities", len(communities), "out", *outDir)
}

// loadEligibleNodes applies the durable-topic policy from a prior all-history
// profile, then keeps nodes active in the current writer-overlap window. Reusing
// the profile avoids recomputing an expensive all-history aggregation per run.
func loadEligibleNodes(profilePath string, edgeSince int64) (map[uint64]*mapNode, error) {
	file, err := os.Open(profilePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	if _, err := reader.Read(); err != nil {
		return nil, fmt.Errorf("read profile header: %w", err)
	}
	nodes := make(map[uint64]*mapNode)
	for {
		record, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("read profile row: %w", err)
		}
		if len(record) != 19 {
			return nil, fmt.Errorf("unexpected profile column count: %d", len(record))
		}
		node, err := parseProfileNode(record)
		if err != nil {
			return nil, err
		}
		if isDurableTopic(node) && node.LastEntryAt >= edgeSince {
			nodes[node.TopicID] = node
		}
	}
	return nodes, nil
}

func isDurableTopic(node *mapNode) bool {
	if node.DistinctAuthors < minDistinctAuthors {
		return false
	}
	if node.ReturningAuthors >= minReturningAuthors && node.ActiveMonths >= minActiveMonths && node.PeakWeekShare <= maxPeakWeekShare {
		return true
	}
	return node.ReturningAuthors >= minSpikeExceptionReturners && node.ActiveMonths >= minSpikeExceptionMonths
}

func parseProfileNode(record []string) (*mapNode, error) {
	parseUint := func(index int) (uint64, error) { return strconv.ParseUint(record[index], 10, 64) }
	parseInt := func(index int) (int64, error) { return strconv.ParseInt(record[index], 10, 64) }
	parseFloat := func(index int) (float64, error) { return strconv.ParseFloat(record[index], 64) }

	topicID, err := parseUint(0)
	if err != nil {
		return nil, fmt.Errorf("parse topic id %q: %w", record[0], err)
	}
	lastEntryAt, err := parseInt(4)
	if err != nil {
		return nil, fmt.Errorf("parse last entry timestamp for %d: %w", topicID, err)
	}
	entries, err := parseInt(6)
	if err != nil {
		return nil, fmt.Errorf("parse entry count for %d: %w", topicID, err)
	}
	distinctAuthors, err := parseInt(7)
	if err != nil {
		return nil, fmt.Errorf("parse author count for %d: %w", topicID, err)
	}
	returningAuthors, err := parseInt(8)
	if err != nil {
		return nil, fmt.Errorf("parse returning author count for %d: %w", topicID, err)
	}
	activeWeeks, err := parseInt(10)
	if err != nil {
		return nil, fmt.Errorf("parse active week count for %d: %w", topicID, err)
	}
	activeMonths, err := parseInt(11)
	if err != nil {
		return nil, fmt.Errorf("parse active month count for %d: %w", topicID, err)
	}
	peakWeekShare, err := parseFloat(15)
	if err != nil {
		return nil, fmt.Errorf("parse peak week share for %d: %w", topicID, err)
	}
	return &mapNode{TopicID: topicID, Title: record[1], URL: record[2], LastEntryAt: lastEntryAt, Entries: entries,
		DistinctAuthors: distinctAuthors, ReturningAuthors: returningAuthors, ActiveWeeks: activeWeeks,
		ActiveMonths: activeMonths, PeakWeekShare: peakWeekShare}, nil
}

func loadRecentAuthorTopics(nodes map[uint64]*mapNode, edgeSince int64) ([]authorTopicRow, error) {
	ids := make([]uint64, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	const query = `SELECT DISTINCT author, topic_id FROM entries
		WHERE deleted_at IS NULL AND author <> '' AND timestamp >= ? AND topic_id IN ?`
	var rows []authorTopicRow
	if err := model.DB().Raw(query, edgeSince, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func buildEdges(rows []authorTopicRow, nodes map[uint64]*mapNode, minSharedAuthors, maxAuthorTopics int) ([]*mapEdge, int, int) {
	authorTopics := make(map[string][]uint64)
	for _, row := range rows {
		authorTopics[row.Author] = append(authorTopics[row.Author], row.TopicID)
		nodes[row.TopicID].RecentAuthors++
	}

	type edgeKey struct{ source, target uint64 }
	edgesByKey := make(map[edgeKey]*mapEdge)
	topicWeights := make(map[uint64]float64, len(nodes))
	effectiveAuthors := 0
	ignoredBroadAuthors := 0
	for _, topicIDs := range authorTopics {
		if len(topicIDs) < 2 {
			continue
		}
		if len(topicIDs) > maxAuthorTopics {
			ignoredBroadAuthors++
			continue
		}
		effectiveAuthors++
		weight := 1 / math.Log1p(float64(len(topicIDs)))
		for _, topicID := range topicIDs {
			topicWeights[topicID] += weight
			nodes[topicID].EffectiveAuthors++
		}
		for left := 0; left < len(topicIDs); left++ {
			for right := left + 1; right < len(topicIDs); right++ {
				source, target := topicIDs[left], topicIDs[right]
				if source > target {
					source, target = target, source
				}
				key := edgeKey{source: source, target: target}
				edge := edgesByKey[key]
				if edge == nil {
					edge = &mapEdge{SourceID: source, TargetID: target}
					edgesByKey[key] = edge
				}
				edge.SharedAuthors++
				edge.WeightedShared += weight
			}
		}
	}

	edges := make([]*mapEdge, 0, len(edgesByKey))
	for _, edge := range edgesByKey {
		if edge.SharedAuthors < minSharedAuthors {
			continue
		}
		unionWeight := topicWeights[edge.SourceID] + topicWeights[edge.TargetID] - edge.WeightedShared
		if unionWeight <= 0 {
			continue
		}
		edge.WeightedJaccard = edge.WeightedShared / unionWeight
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(left, right int) bool {
		if edges[left].WeightedJaccard == edges[right].WeightedJaccard {
			if edges[left].SourceID == edges[right].SourceID {
				return edges[left].TargetID < edges[right].TargetID
			}
			return edges[left].SourceID < edges[right].SourceID
		}
		return edges[left].WeightedJaccard > edges[right].WeightedJaccard
	})
	return edges, effectiveAuthors, ignoredBroadAuthors
}

func applyMutualRanks(edges []*mapEdge, topNeighbors int) {
	byNode := make(map[uint64][]*mapEdge)
	for _, edge := range edges {
		byNode[edge.SourceID] = append(byNode[edge.SourceID], edge)
		byNode[edge.TargetID] = append(byNode[edge.TargetID], edge)
	}
	for nodeID, incident := range byNode {
		sort.Slice(incident, func(left, right int) bool {
			if incident[left].WeightedJaccard == incident[right].WeightedJaccard {
				return otherNode(incident[left], nodeID) < otherNode(incident[right], nodeID)
			}
			return incident[left].WeightedJaccard > incident[right].WeightedJaccard
		})
		for index, edge := range incident {
			if edge.SourceID == nodeID {
				edge.SourceRank = index + 1
			} else {
				edge.TargetRank = index + 1
			}
		}
	}
	for _, edge := range edges {
		edge.MutualTopNeighbor = edge.SourceRank <= topNeighbors && edge.TargetRank <= topNeighbors
	}
}

func annotateNodes(nodes map[uint64]*mapNode, edges []*mapEdge) (int, int) {
	adjacency := make(map[uint64][]uint64, len(nodes))
	for _, edge := range edges {
		if !edge.MutualTopNeighbor {
			continue
		}
		adjacency[edge.SourceID] = append(adjacency[edge.SourceID], edge.TargetID)
		adjacency[edge.TargetID] = append(adjacency[edge.TargetID], edge.SourceID)
		nodes[edge.SourceID].RetainedDegree++
		nodes[edge.TargetID].RetainedDegree++
		nodes[edge.SourceID].WeightedDegree += edge.WeightedJaccard
		nodes[edge.TargetID].WeightedDegree += edge.WeightedJaccard
	}

	componentID := 0
	largestComponent := 0
	for nodeID, node := range nodes {
		if node.ConnectedComponent != 0 {
			continue
		}
		componentID++
		queue := []uint64{nodeID}
		node.ConnectedComponent = componentID
		size := 0
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			size++
			for _, neighbor := range adjacency[current] {
				if nodes[neighbor].ConnectedComponent != 0 {
					continue
				}
				nodes[neighbor].ConnectedComponent = componentID
				queue = append(queue, neighbor)
			}
		}
		if size > largestComponent {
			largestComponent = size
		}
	}
	return componentID, largestComponent
}

// detectCommunities uses weighted label propagation on the pruned graph. Labels
// move only along mutual-neighbor edges, so broad weak relationships cannot pull
// a node across the graph. Iteration order and tie-breaking are fixed for stable
// report output.
func detectCommunities(nodes map[uint64]*mapNode, edges []*mapEdge, maxIterations int) ([]communitySummary, int) {
	type neighbor struct {
		id     uint64
		weight float64
	}
	adjacency := make(map[uint64][]neighbor, len(nodes))
	for _, edge := range edges {
		if !edge.MutualTopNeighbor {
			continue
		}
		adjacency[edge.SourceID] = append(adjacency[edge.SourceID], neighbor{id: edge.TargetID, weight: edge.WeightedJaccard})
		adjacency[edge.TargetID] = append(adjacency[edge.TargetID], neighbor{id: edge.SourceID, weight: edge.WeightedJaccard})
	}

	nodeIDs := make([]uint64, 0, len(nodes))
	labels := make(map[uint64]uint64, len(nodes))
	for nodeID := range nodes {
		nodeIDs = append(nodeIDs, nodeID)
		labels[nodeID] = nodeID
	}
	sort.Slice(nodeIDs, func(left, right int) bool { return nodeIDs[left] < nodeIDs[right] })

	iterations := 0
	for ; iterations < maxIterations; iterations++ {
		changed := false
		for _, nodeID := range nodeIDs {
			if len(adjacency[nodeID]) == 0 {
				continue
			}
			weights := make(map[uint64]float64)
			for _, adjacent := range adjacency[nodeID] {
				weights[labels[adjacent.id]] += adjacent.weight
			}
			bestLabel := labels[nodeID]
			bestWeight := weights[bestLabel]
			for label, weight := range weights {
				if weight > bestWeight || (weight == bestWeight && label < bestLabel) {
					bestLabel, bestWeight = label, weight
				}
			}
			if bestLabel != labels[nodeID] {
				labels[nodeID] = bestLabel
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	byLabel := make(map[uint64][]*mapNode)
	for nodeID, label := range labels {
		byLabel[label] = append(byLabel[label], nodes[nodeID])
	}
	groups := make([][]*mapNode, 0, len(byLabel))
	for _, group := range byLabel {
		sort.Slice(group, func(left, right int) bool {
			if group[left].WeightedDegree == group[right].WeightedDegree {
				return group[left].TopicID < group[right].TopicID
			}
			return group[left].WeightedDegree > group[right].WeightedDegree
		})
		groups = append(groups, group)
	}
	sort.Slice(groups, func(left, right int) bool {
		if len(groups[left]) == len(groups[right]) {
			return groups[left][0].TopicID < groups[right][0].TopicID
		}
		return len(groups[left]) > len(groups[right])
	})

	communities := make([]communitySummary, 0, len(groups))
	for index, group := range groups {
		communityID := index + 1
		members := make(map[uint64]struct{}, len(group))
		representatives := make([]string, 0, 8)
		for memberIndex, node := range group {
			node.CommunityID = communityID
			members[node.TopicID] = struct{}{}
			if memberIndex < 8 {
				representatives = append(representatives, node.Title)
			}
		}
		internalWeight := 0.0
		for _, edge := range edges {
			if edge.MutualTopNeighbor {
				if _, sourceInGroup := members[edge.SourceID]; sourceInGroup {
					if _, targetInGroup := members[edge.TargetID]; targetInGroup {
						internalWeight += edge.WeightedJaccard
					}
				}
			}
		}
		communities = append(communities, communitySummary{CommunityID: communityID, Size: len(group), InternalEdgeWeight: internalWeight, RepresentativeTopics: representatives})
	}
	return communities, iterations + 1
}

func otherNode(edge *mapEdge, nodeID uint64) uint64 {
	if edge.SourceID == nodeID {
		return edge.TargetID
	}
	return edge.SourceID
}

func countAuthors(rows []authorTopicRow) int {
	authors := make(map[string]struct{})
	for _, row := range rows {
		authors[row.Author] = struct{}{}
	}
	return len(authors)
}

func countCandidateEdges(rows []authorTopicRow, maxAuthorTopics int) int {
	authorTopics := make(map[string]int)
	for _, row := range rows {
		authorTopics[row.Author]++
	}
	total := 0
	for _, topicCount := range authorTopics {
		if topicCount >= 2 && topicCount <= maxAuthorTopics {
			total += topicCount * (topicCount - 1) / 2
		}
	}
	return total
}

func writeNodes(path string, nodes map[uint64]*mapNode) error {
	ordered := make([]*mapNode, 0, len(nodes))
	for _, node := range nodes {
		ordered = append(ordered, node)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].WeightedDegree == ordered[right].WeightedDegree {
			return ordered[left].TopicID < ordered[right].TopicID
		}
		return ordered[left].WeightedDegree > ordered[right].WeightedDegree
	})

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"topic_id", "title", "url", "last_entry_at", "entries", "distinct_authors", "returning_authors", "active_weeks", "active_months", "peak_week_share", "recent_authors", "effective_authors", "retained_degree", "weighted_degree", "connected_component", "community_id"}); err != nil {
		return err
	}
	for _, node := range ordered {
		if err := writer.Write([]string{fmt.Sprint(node.TopicID), node.Title, node.URL, fmt.Sprint(node.LastEntryAt), fmt.Sprint(node.Entries), fmt.Sprint(node.DistinctAuthors), fmt.Sprint(node.ReturningAuthors), fmt.Sprint(node.ActiveWeeks), fmt.Sprint(node.ActiveMonths), fmt.Sprintf("%.6f", node.PeakWeekShare), fmt.Sprint(node.RecentAuthors), fmt.Sprint(node.EffectiveAuthors), fmt.Sprint(node.RetainedDegree), fmt.Sprintf("%.8f", node.WeightedDegree), fmt.Sprint(node.ConnectedComponent), fmt.Sprint(node.CommunityID)}); err != nil {
			return err
		}
	}
	return writer.Error()
}

func writeCommunities(path string, communities []communitySummary) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"community_id", "size", "internal_edge_weight", "representative_topics"}); err != nil {
		return err
	}
	for _, community := range communities {
		if err := writer.Write([]string{fmt.Sprint(community.CommunityID), fmt.Sprint(community.Size), fmt.Sprintf("%.8f", community.InternalEdgeWeight), strings.Join(community.RepresentativeTopics, " | ")}); err != nil {
			return err
		}
	}
	return writer.Error()
}

func writeEdges(path string, edges []*mapEdge, nodes map[uint64]*mapNode) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"source_id", "source_title", "target_id", "target_title", "shared_authors", "weighted_shared", "weighted_jaccard", "source_rank", "target_rank", "mutual_top_neighbor"}); err != nil {
		return err
	}
	for _, edge := range edges {
		if err := writer.Write([]string{fmt.Sprint(edge.SourceID), nodes[edge.SourceID].Title, fmt.Sprint(edge.TargetID), nodes[edge.TargetID].Title, fmt.Sprint(edge.SharedAuthors), fmt.Sprintf("%.8f", edge.WeightedShared), fmt.Sprintf("%.8f", edge.WeightedJaccard), fmt.Sprint(edge.SourceRank), fmt.Sprint(edge.TargetRank), fmt.Sprint(edge.MutualTopNeighbor)}); err != nil {
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
