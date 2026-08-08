package main

import (
	"context"
	"fmt"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func runSanAdd(ctx context.Context, store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.AddSAN(ctx, args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("add san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}

func runSanRemove(ctx context.Context, store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.RemoveSAN(ctx, args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("remove san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}
