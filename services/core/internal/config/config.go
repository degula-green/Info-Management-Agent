package config

import "os"

type Config struct {
	HTTPPort           string
	RedisDatabase      string
	RedisURL           string
	DatabaseURL        string
	DatabaseSchema     string
	MinioEndpoint      string
	MinioAccessKey     string
	MinioSecretKey     string
	MinioBucket        string
	MinioSecure        bool
	AvatarURLTTL       string
	OpenFGAURL         string
	OpenFGAStoreID     string
	OpenFGAModelID     string
	FeishuAppID        string
	FeishuAppSecret    string
	FeishuRedirectURI  string
	WebBaseURL         string
	JWTSecret          string
	WechatCollectorURL string
	CollectorToken     string
}

func Load() Config {
	return Config{
		HTTPPort:           env("CORE_HTTP_PORT", "8080"),
		RedisDatabase:      env("CORE_REDIS_DATABASE", "1"),
		RedisURL:           env("CORE_REDIS_URL", "redis://127.0.0.1:6379/1"),
		DatabaseURL:        env("CORE_DATABASE_URL", ""),
		DatabaseSchema:     env("CORE_DATABASE_SCHEMA", "public"),
		MinioEndpoint:      env("CORE_MINIO_ENDPOINT", "127.0.0.1:9000"),
		MinioAccessKey:     env("CORE_MINIO_ACCESS_KEY", ""),
		MinioSecretKey:     env("CORE_MINIO_SECRET_KEY", ""),
		MinioBucket:        env("CORE_MINIO_BUCKET", "info-agent"),
		MinioSecure:        env("CORE_MINIO_SECURE", "false") == "true",
		AvatarURLTTL:       env("CORE_AVATAR_URL_TTL", "15m"),
		OpenFGAURL:         env("CORE_OPENFGA_URL", "http://127.0.0.1:8081"),
		OpenFGAStoreID:     env("CORE_OPENFGA_STORE_ID", ""),
		OpenFGAModelID:     env("CORE_OPENFGA_MODEL_ID", ""),
		FeishuAppID:        env("FEISHU_APP_ID", ""),
		FeishuAppSecret:    env("FEISHU_APP_SECRET", ""),
		FeishuRedirectURI:  env("FEISHU_REDIRECT_URI", "http://localhost:8080/api/connectors/feishu/callback"),
		WebBaseURL:         env("CORE_WEB_BASE_URL", "http://localhost:5174"),
		JWTSecret:          env("CORE_JWT_SECRET", "change-me-in-production"),
		WechatCollectorURL: env("WECHAT_COLLECTOR_URL", "http://127.0.0.1:8100"),
		CollectorToken:     env("COLLECTOR_INTERNAL_TOKEN", "local-development-only"),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
