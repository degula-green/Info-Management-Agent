package server

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"info-agent/core/internal/config"
	"info-agent/core/internal/httpapi"
	"info-agent/core/internal/redisstore"
)

func New(pool *pgxpool.Pool, cfg config.Config, redisClient *redisstore.Client) *gin.Engine {
	return httpapi.NewRouter(pool, cfg, redisClient)
}
