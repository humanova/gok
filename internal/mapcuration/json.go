package mapcuration

import (
	"encoding/json"
	"os"
)

// Writes a consistently formatted map pipeline artifact.
func WriteJSON(path string, value any) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
