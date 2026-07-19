package config

import (
	"log"

	"github.com/tkanos/gonfig"
)

var (
	Config Configuration
)

func init() {
	err := GetConfig("./configs/config.json", &Config)
	if err != nil {
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
	// EkşiPulse additions
	EmbedderUrl               string // e.g. "http://localhost:8765"
	GeminiApiKey              string
	GeminiModel               string // e.g. "gemini-3.5-flash"
	DigestIntervalMinutes     int    // how often to regenerate precomputed digest (minutes)
	TopicBriefIntervalMinutes int    // how often to regenerate topic radar briefs (minutes)
	ApiPort                   int    // port for gok-api HTTP server
}

func GetConfig(configPath string, config *Configuration) error {
	err := gonfig.GetConf(configPath, config)
	if err != nil {
		return err
	}

	return nil
}
