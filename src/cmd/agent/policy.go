package main

import "time"

// Policy is a single reconcilable unit: run Binary with Args whenever more
// than Interval has elapsed since the last successful run. v1 has exactly
// one, compiled in here — no policy is fetched over the network yet.
type Policy struct {
	ID       string
	Binary   string
	Args     []string
	Interval time.Duration
}

var policies = []Policy{
	{ID: "cert-refresh", Binary: "certclient", Interval: 5 * time.Minute},
}
