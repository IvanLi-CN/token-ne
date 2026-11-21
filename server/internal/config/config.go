package config

import "os"

type Config struct {
	HTTPListen    string
	MetricsListen string
	PublicOrigin  string
	DatabaseURL   string
}

func Load() Config {
	return Config{
		HTTPListen:    env("HTTP_LISTEN", "0.0.0.0:8080"),
		MetricsListen: env("METRICS_LISTEN", "0.0.0.0:9090"),
		PublicOrigin:  env("PUBLIC_ORIGIN", "http://127.0.0.1:8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
