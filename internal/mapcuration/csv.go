package mapcuration

import "fmt"

// Builds CSV column positions after checking that every required column exists.
func ColumnIndexes(header []string, required ...string) (map[string]int, error) {
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
