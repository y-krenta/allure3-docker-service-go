package config

import (
	"log"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port                 string        // Flask/Waitress listen port
	SecurityEnable       bool          // Enable JWT auth
	KeepHistory          bool          // Preserve Allure history across runs
	KeepHistoryLatest    int           // How many history entries to retain
	CheckResultsInterval time.Duration // Auto-generate interval
	OptimizeStorage      bool          // Strip large attachments
	TLS                  bool          // Enable HTTPS
	DevMode              bool          // Enables Flask debug reloader

}

func Load() Config {
	var config Config
	config.Port = os.Getenv("PORT")
	if config.Port == "" {
		config.Port = "5050"
	}
	config.SecurityEnable = getEnvAsBool("SECURITY_ENABLED", false)
	config.KeepHistory = getEnvAsBool("KEEP_HISTORY", false)
	config.KeepHistoryLatest = getEnvAsInt("KEEP_HISTORY_LATEST", 25)
	config.CheckResultsInterval = getEnvAsDurationSeconds("CHECK_RESULTS_EVERY_SECONDS", 0)
	config.OptimizeStorage = getEnvAsBool("OPTIMIZE_STORAGE", false)
	config.TLS = getEnvAsBool("TLS", false)
	config.DevMode = getEnvAsBool("DEV_MODE", false)

	return config

}

func getEnvAsBool(key string, defaultValue bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return defaultValue
	}
	val, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("Ошибка парсинга %s: %v, используем значение по умолчанию: %v", key, err, defaultValue)
		return defaultValue
	}
	return val
}

func getEnvAsInt(key string, defaultValue int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return defaultValue
	}

	val, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("[WARN] %s=%q неверный формат, используем %d", key, raw, defaultValue)
		return defaultValue
	}

	if val < 0 {
		log.Printf("[WARN] %s=%d отрицательное, используем %d", key, val, defaultValue)
		return defaultValue
	}

	return val
}

func getEnvAsDurationSeconds(key string, defaultValue time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}

	sec, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("[WARN] %s=%q не число, используем %v", key, v, defaultValue)
		return defaultValue
	}

	if sec <= 0 {
		log.Printf("[WARN] %s=%d <= 0, используем %v", key, sec, defaultValue)
		return defaultValue
	}

	return time.Duration(sec) * time.Second
}
