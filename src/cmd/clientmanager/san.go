package main

import (
	"fmt"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func runSanAdd(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.AddSAN(args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("add san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}

func runSanRemove(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.RemoveSAN(args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("remove san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}
