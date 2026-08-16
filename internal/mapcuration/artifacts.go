package mapcuration

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

type GraphNode struct {
	ID          uint64
	Title       string
	CommunityID int
	Degree      int
}

type Community struct {
	ID                   int
	Size                 int
	RepresentativeTopics []string
}

type CommunityRegionAssignment struct {
	CommunityID int    `json:"community_id"`
	Region      string `json:"region"`
}

type NodeRegionAssignment struct {
	NodeID uint64 `json:"node_id"`
	Region string `json:"region"`
}

// Reads clustered nodes.csv in a deterministic topic-ID order.
func ReadGraphNodes(path string) ([]GraphNode, error) {
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
	indexes, err := ColumnIndexes(header, "topic_id", "title", "community_id", "retained_degree")
	if err != nil {
		return nil, err
	}

	nodes := make([]GraphNode, 0)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := requireColumns(record, indexes); err != nil {
			return nil, err
		}
		id, err := strconv.ParseUint(record[indexes["topic_id"]], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse topic id: %w", err)
		}
		communityID, err := strconv.Atoi(record[indexes["community_id"]])
		if err != nil {
			return nil, fmt.Errorf("parse community id for %d: %w", id, err)
		}
		degree, err := strconv.Atoi(record[indexes["retained_degree"]])
		if err != nil {
			return nil, fmt.Errorf("parse retained degree for %d: %w", id, err)
		}
		nodes = append(nodes, GraphNode{ID: id, Title: record[indexes["title"]], CommunityID: communityID, Degree: degree})
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].ID < nodes[right].ID })
	return nodes, nil
}

// Preserves clusters.csv source order because it controls Gemini batch composition.
func ReadCommunities(path string) ([]Community, error) {
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
	indexes, err := ColumnIndexes(header, "community_id", "size", "representative_topics")
	if err != nil {
		return nil, err
	}

	communities := make([]Community, 0)
	seen := make(map[int]struct{})
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := requireColumns(record, indexes); err != nil {
			return nil, err
		}
		id, err := strconv.Atoi(record[indexes["community_id"]])
		if err != nil {
			return nil, fmt.Errorf("parse community id: %w", err)
		}
		size, err := strconv.Atoi(record[indexes["size"]])
		if err != nil {
			return nil, fmt.Errorf("parse community size for %d: %w", id, err)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate community %d", id)
		}
		seen[id] = struct{}{}
		communities = append(communities, Community{ID: id, Size: size, RepresentativeTopics: strings.Split(record[indexes["representative_topics"]], " | ")})
	}
	return communities, nil
}

func ReadCommunityRegions(path string) (map[int]string, error) {
	var report struct {
		Assignments []CommunityRegionAssignment `json:"assignments"`
	}
	if err := ReadJSON(path, &report); err != nil {
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

func ReadNodeRegions(path string) (map[uint64]string, error) {
	var report struct {
		Assignments []NodeRegionAssignment `json:"assignments"`
	}
	if err := ReadJSON(path, &report); err != nil {
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

func requireColumns(record []string, indexes map[string]int) error {
	for column, index := range indexes {
		if index >= len(record) {
			return fmt.Errorf("record missing %q column", column)
		}
	}
	return nil
}

func ReadJSON(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(destination)
}
