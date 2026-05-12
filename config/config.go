package config

import (
	"os"
)

type Config struct {
	BotToken string `yaml:"bot_token"`
	DBUrl    string `yaml:"db_url"`
	TZ       string `yaml:"tz"`
}

func Load() Config {
	return Config{
		BotToken: mustGet("bot_token"),
		DBUrl:    mustGet("database_url"),
		TZ:       get("tz", "Asia/Tashkent"),
	}
}

func mustGet(key string) string {
	val := os.Getenv(key)
	if val == "" {
		panic(key + " is empty")
	}
	return val
}

func get(key, def string) string {
	val := os.Getenv(key)
	if val == "" {
		return def
	}
	return val
}
