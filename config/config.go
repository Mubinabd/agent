package config

import (
	"os"
)

type Config struct {
	BotToken string
	DBUrl    string
	TZ       string
}

func Load() Config {
	return Config{
		BotToken: mustGet("BOT_TOKEN"),
		DBUrl:    mustGet("DATABASE_URL"),
		TZ:       get("TZ", "Asia/Tashkent"),
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
