package config

import (
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Bot struct {
		Token string `yaml:"token"`
	} `yaml:"bot"`

	Database struct {
		URL string `yaml:"url"`
	} `yaml:"database"`

	App struct {
		Timezone string `yaml:"timezone"`
	} `yaml:"app"`
}

func LoadConfig() *Config {
	file, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatal("config.yaml topilmadi:", err)
	}

	var cfg Config
	err = yaml.Unmarshal(file, &cfg)
	if err != nil {
		log.Fatal("config parse error:", err)
	}

	return &cfg
}
