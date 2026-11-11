package config

import (
	"errors"
	"log"
	"os"

	"github.com/tkanos/gonfig"
)

var (
	Config Configuration
)

func init() {
	err := GetConfig("./configs/config.json", &Config)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Panicf("couldn't get/parse the config : %v", err)
	}
}

type Configuration struct {
	ScrapeInterval            int // minutes
	DbName                    string
	DbHost                    string
	DbPort                    int
	DbUser                    string
	DbPassword                string
	DbSSLMode                 string
	EntryCollectorDelay       int64
	EntryCollectorRandomDelay int64
	// EksiRadar additions
	EmbedderUrl               string // e.g. "http://localhost:8765"
	GeminiApiKey              string
	GeminiModel               string
	DigestIntervalMinutes     int    // how often to regenerate precomputed digest (minutes); 0 disables it
	TopicBriefIntervalMinutes int    // how often to regenerate topic radar briefs (minutes)
	ApiPort                   int    // port for gok-api HTTP server
	StoryIntervalMinutes      int    // how often to link new topics into story threads (minutes); 0 disables it
	StoryModel                string // judge model for story links, default gemini-3.5-flash
	StoryEmbeddingModel       string // embedding model for story candidates, default gemini-embedding-2
}

// StoryModels returns the configured story judge and embedding models, with defaults.
func (c Configuration) StoryModels() (judge, embedding string) {
	judge, embedding = c.StoryModel, c.StoryEmbeddingModel
	if judge == "" {
		judge = "gemini-3.5-flash"
	}
	if embedding == "" {
		embedding = "gemini-embedding-2"
	}
	return judge, embedding
}

func GetConfig(configPath string, config *Configuration) error {
	err := gonfig.GetConf(configPath, config)
	if err != nil {
		return err
	}

	return nil
}
