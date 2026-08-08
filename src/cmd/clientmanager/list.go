package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

const timeLayout = "2006-01-02 15:04:05"

func runList(ctx context.Context, store *clientmanagerstore.Store, out io.Writer) error {
	clients, err := store.ListClients(ctx)
	if err != nil {
		return fmt.Errorf("list clients: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOSTNAME\tADDED_AT\tREVOKED\tLAST_SEEN")
	for _, c := range clients {
		revoked := "no"
		if c.Revoked {
			revoked = "yes"
		}
		lastSeen := "never"
		if c.LastSeenAt != nil {
			lastSeen = c.LastSeenAt.Format(timeLayout)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Hostname, c.AddedAt.Format(timeLayout), revoked, lastSeen)
	}
	return tw.Flush()
}

func runShow(ctx context.Context, store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	client, err := store.GetClient(ctx, args.Hostname)
	if err != nil {
		return fmt.Errorf("show %s: %w", args.Hostname, err)
	}
	fmt.Fprintf(out, "hostname:   %s\n", client.Hostname)
	fmt.Fprintf(out, "added_at:   %s\n", client.AddedAt.Format(timeLayout))
	sans := client.SANsList()
	if len(sans) == 0 {
		fmt.Fprintln(out, "sans:       (none)")
	} else {
		fmt.Fprintf(out, "sans:       %s\n", strings.Join(sans, ", "))
	}
	fmt.Fprintf(out, "revoked:    %v\n", client.Revoked)
	if client.RevokedAt != nil {
		fmt.Fprintf(out, "revoked_at: %s\n", client.RevokedAt.Format(timeLayout))
	}
	if client.LastSeenAt != nil {
		fmt.Fprintf(out, "last_seen:  %s\n", client.LastSeenAt.Format(timeLayout))
	} else {
		fmt.Fprintln(out, "last_seen:  never")
	}

	descs, err := store.KV(ctx, args.Hostname, clientmanagerstore.KindDescription)
	if err != nil {
		return fmt.Errorf("show %s: load descriptions: %w", args.Hostname, err)
	}
	fmt.Fprintln(out, "descriptions:")
	for _, d := range descs {
		fmt.Fprintf(out, "  %s=%s\n", d.Key, d.Value)
	}

	attrs, err := store.KV(ctx, args.Hostname, clientmanagerstore.KindAttribute)
	if err != nil {
		return fmt.Errorf("show %s: load attributes: %w", args.Hostname, err)
	}
	fmt.Fprintln(out, "attributes:")
	for _, a := range attrs {
		fmt.Fprintf(out, "  %s=%s\n", a.Key, a.Value)
	}
	return nil
}

func runRevoke(ctx context.Context, store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.SetRevoked(ctx, args.Hostname, true, time.Now()); err != nil {
		return fmt.Errorf("revoke %s: %w", args.Hostname, err)
	}
	return nil
}

func runUnrevoke(ctx context.Context, store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.SetRevoked(ctx, args.Hostname, false, time.Now()); err != nil {
		return fmt.Errorf("unrevoke %s: %w", args.Hostname, err)
	}
	return nil
}
