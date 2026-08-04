package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type MapNode struct {
	ID          uint64  `json:"id"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	CommunityID int     `json:"community_id"`
	Region      string  `json:"region"`
	Degree      int     `json:"degree"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
}

type MapEdge struct {
	Source uint64  `json:"source"`
	Target uint64  `json:"target"`
	Weight float64 `json:"weight"`
}

type MapCluster struct {
	ID                   int      `json:"id"`
	Size                 int      `json:"size"`
	RepresentativeTopics []string `json:"representative_topics"`
}

type MapSnapshot struct {
	Available   bool         `json:"available"`
	GeneratedAt time.Time    `json:"generated_at,omitempty"`
	Nodes       []MapNode    `json:"nodes,omitempty"`
	Edges       []MapEdge    `json:"edges,omitempty"`
	Clusters    []MapCluster `json:"clusters,omitempty"`
}

func loadMapSnapshot(layoutDir, graphDir string) (*MapSnapshot, error) {
	nodes, err := loadMapNodes(filepath.Join(graphDir, "nodes.csv"), filepath.Join(layoutDir, "layout.csv"))
	if err != nil {
		return nil, err
	}
	edges, err := loadMapEdges(filepath.Join(graphDir, "edges.csv"))
	if err != nil {
		return nil, err
	}
	clusters, err := loadMapClusters(filepath.Join(graphDir, "clusters.csv"))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(filepath.Join(layoutDir, "layout.csv"))
	if err != nil {
		return nil, err
	}
	return &MapSnapshot{Available: true, GeneratedAt: info.ModTime().UTC(), Nodes: nodes, Edges: edges, Clusters: clusters}, nil
}

func loadMapNodes(graphPath, layoutPath string) ([]MapNode, error) {
	type graphNode struct {
		title       string
		url         string
		communityID int
		degree      int
	}
	metadata := make(map[uint64]graphNode)
	if _, err := readCSV(graphPath, func(record []string, index map[string]int) error {
		id, err := parseUint(record, index, "topic_id")
		if err != nil {
			return err
		}
		communityID, err := parseInt(record, index, "community_id")
		if err != nil {
			return err
		}
		degree, err := parseInt(record, index, "retained_degree")
		if err != nil {
			return err
		}
		metadata[id] = graphNode{title: record[index["title"]], url: record[index["url"]], communityID: communityID, degree: degree}
		return nil
	}); err != nil {
		return nil, err
	}

	nodes := make([]MapNode, 0, len(metadata))
	if _, err := readCSV(layoutPath, func(record []string, index map[string]int) error {
		id, err := parseUint(record, index, "topic_id")
		if err != nil {
			return err
		}
		meta, ok := metadata[id]
		if !ok {
			return nil
		}
		region, ok := index["region"]
		if !ok {
			return fmt.Errorf("missing %q column", "region")
		}
		x, err := parseFloat(record, index, "x")
		if err != nil {
			return err
		}
		y, err := parseFloat(record, index, "y")
		if err != nil {
			return err
		}
		nodes = append(nodes, MapNode{ID: id, Title: meta.title, URL: meta.url, CommunityID: meta.communityID, Region: record[region], Degree: meta.degree, X: x, Y: y})
		return nil
	}); err != nil {
		return nil, err
	}
	return nodes, nil
}

func loadMapEdges(path string) ([]MapEdge, error) {
	edges := make([]MapEdge, 0)
	_, err := readCSV(path, func(record []string, index map[string]int) error {
		if record[index["mutual_top_neighbor"]] != "true" {
			return nil
		}
		source, err := parseUint(record, index, "source_id")
		if err != nil {
			return err
		}
		target, err := parseUint(record, index, "target_id")
		if err != nil {
			return err
		}
		weight, err := parseFloat(record, index, "weighted_jaccard")
		if err != nil {
			return err
		}
		edges = append(edges, MapEdge{Source: source, Target: target, Weight: weight})
		return nil
	})
	return edges, err
}

func loadMapClusters(path string) ([]MapCluster, error) {
	clusters := make([]MapCluster, 0)
	_, err := readCSV(path, func(record []string, index map[string]int) error {
		id, err := parseInt(record, index, "community_id")
		if err != nil {
			return err
		}
		size, err := parseInt(record, index, "size")
		if err != nil {
			return err
		}
		topics := strings.Split(record[index["representative_topics"]], " | ")
		clusters = append(clusters, MapCluster{ID: id, Size: size, RepresentativeTopics: topics})
		return nil
	})
	return clusters, err
}

func readCSV(path string, visit func([]string, map[string]int) error) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return 0, err
	}
	index := make(map[string]int, len(header))
	for i, column := range header {
		index[column] = i
	}
	count := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		if err := visit(record, index); err != nil {
			return count, fmt.Errorf("%s row %d: %w", path, count+2, err)
		}
		count++
	}
}

func parseUint(record []string, index map[string]int, column string) (uint64, error) {
	position, ok := index[column]
	if !ok {
		return 0, fmt.Errorf("missing %q column", column)
	}
	return strconv.ParseUint(record[position], 10, 64)
}

func parseInt(record []string, index map[string]int, column string) (int, error) {
	position, ok := index[column]
	if !ok {
		return 0, fmt.Errorf("missing %q column", column)
	}
	return strconv.Atoi(record[position])
}

func parseFloat(record []string, index map[string]int, column string) (float64, error) {
	position, ok := index[column]
	if !ok {
		return 0, fmt.Errorf("missing %q column", column)
	}
	return strconv.ParseFloat(record[position], 64)
}
