package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func runAdd(conf *config.Config, certsDir string, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	if _, err := store.GetClient(args.Hostname); err == nil {
		return fmt.Errorf("client %q already exists; use re-enroll or description/attribute set instead", args.Hostname)
	} else if !errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return fmt.Errorf("check existing client: %w", err)
	}

	token, err := mint(conf, certsDir, args.Hostname, args.SANs)
	if err != nil {
		return fmt.Errorf("add %s: %w", args.Hostname, err)
	}

	if err := store.AddClient(args.Hostname, args.SANs, time.Now()); err != nil {
		return fmt.Errorf("record client %s: %w", args.Hostname, err)
	}

	fmt.Fprintln(out, token)
	return nil
}

func runReEnroll(conf *config.Config, certsDir string, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	client, err := store.GetClient(args.Hostname)
	if err != nil {
		return fmt.Errorf("re-enroll %s: %w", args.Hostname, err)
	}

	sans := args.SANs
	if len(sans) == 0 {
		sans = client.SANsList()
	}

	token, err := mint(conf, certsDir, args.Hostname, sans)
	if err != nil {
		return fmt.Errorf("re-enroll %s: %w", args.Hostname, err)
	}

	fmt.Fprintln(out, token)
	return nil
}
