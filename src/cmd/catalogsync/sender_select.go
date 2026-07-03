package main

import (
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/config"
)

// selectSender chooses catalogsync's Sender based on configuration: a real
// GrpcSender if catalog_host is set and reachable at startup, LoggingSender
// otherwise — catalog_host unset, or the catalog being temporarily down,
// must never block catalogsync from starting and running.
func selectSender(conf *config.Config, logger *slog.Logger, certsDir string) Sender {
	if conf.CatalogHost == "" {
		return NewLoggingSender(logger)
	}
	grpcSender, err := NewGrpcSender(conf.CatalogHost, conf.CatalogPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Warn("could not connect to catalog at startup, falling back to LoggingSender until next restart",
			"catalog_host", conf.CatalogHost, "catalog_port", conf.CatalogPort, "error", err)
		return NewLoggingSender(logger)
	}
	logger.Info("catalogsync sending to catalog", "catalog_host", conf.CatalogHost, "catalog_port", conf.CatalogPort)
	return grpcSender
}
