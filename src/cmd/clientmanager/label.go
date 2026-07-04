package main

import (
	"fmt"
	"strings"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

// parseKV splits a "key=value" string, erroring if the shape doesn't match.
func parseKV(s string) (key, value string, err error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", fmt.Errorf("invalid key=value pair: %q", s)
	}
	return parts[0], parts[1], nil
}

func runKVSet(store *clientmanagerstore.Store, kind clientmanagerstore.KVKind, args *Arguments) error {
	for _, pair := range args.KVPairs {
		key, value, err := parseKV(pair)
		if err != nil {
			return err
		}
		if err := store.SetKV(args.Hostname, kind, key, value); err != nil {
			return fmt.Errorf("set %s %s on %s: %w", kind, key, args.Hostname, err)
		}
	}
	return nil
}

func runKVUnset(store *clientmanagerstore.Store, kind clientmanagerstore.KVKind, args *Arguments) error {
	if err := store.UnsetKV(args.Hostname, kind, args.Key); err != nil {
		return fmt.Errorf("unset %s %s on %s: %w", kind, args.Key, args.Hostname, err)
	}
	return nil
}
