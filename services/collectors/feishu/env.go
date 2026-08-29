package main

import (
    "bufio"
    "os"
    "path/filepath"
    "strings"
)

func loadCoreEnv(redisURL, redisDB **string) {
    candidates := []string{"services/core/.env", filepath.Join("..", "..", "core", ".env")}
    for _, path := range candidates {
        f, err := os.Open(path); if err != nil { continue }; defer f.Close()
        scanner := bufio.NewScanner(f); for scanner.Scan() { line := strings.TrimSpace(scanner.Text()); if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") { continue }; p := strings.SplitN(line, "=", 2); key, value := strings.TrimSpace(p[0]), strings.Trim(strings.TrimSpace(p[1]), "\"'"); if key == "CORE_REDIS_URL" && **redisURL == "" { **redisURL = value }; if key == "CORE_REDIS_DATABASE" && **redisDB == "" { **redisDB = value } }; return
    }
}
