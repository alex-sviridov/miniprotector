package main

import (
	"fmt"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/config"
)

// selectSender chooses catalogsync's Sender based on configuration:
// LoggingSender when catalog_host is unset (replication intentionally
// disabled), a real GrpcSender otherwise. The GrpcSender is returned
// immediately even if the catalog isn't reachable yet -- it must never
// silently stand in for a real send, since run() persists its cursor only
// after Send succeeds, so a sender that fakes success drops that batch for
// good. NewGrpcSender only errors on a genuine credential-loading problem
// (missing/corrupt certsDir), which a restart won't fix either, so that
// error is returned to the caller to fail startup loudly instead of
// degrading silently.
func selectSender(conf *config.Config, logger *slog.Logger, certsDir string) (Sender, error) {
	if conf.CatalogHost == "" {
		return NewLoggingSender(logger), nil
	}
	grpcSender, err := NewGrpcSender(conf.CatalogHost, conf.CatalogPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		return nil, fmt.Errorf("catalog_host %s:%d configured but sender could not be created: %w",
			conf.CatalogHost, conf.CatalogPort, err)
	}
	logger.Info("catalogsync sending to catalog", "catalog_host", conf.CatalogHost, "catalog_port", conf.CatalogPort)
	return grpcSender, nil
}
