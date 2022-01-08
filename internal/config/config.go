package config

import (
	"github.com/tkanos/gonfig"
	"log"
)

var (
	Config Configuration
)

func init () {
	err := GetConfig("./configs/config.json", &Config)
	if err != nil {
		log.Panicf("couldn't get/parse the config : %v", err)
	}
}

type Configuration struct {
	ScrapeInterval   		  int    // minutes
	DbName           		  string
	DbHost           		  string
	DbPort           		  int
	DbUser           		  string
	DbPassword       		  string
	DbSSLMode        		  string
	EntryCollectorDelay 	  int64
	EntryCollectorRandomDelay int64
}

func GetConfig(configPath string, config *Configuration) error {
	err := gonfig.GetConf(configPath, config)
	if err != nil {
		return err
	}

	return nil
}