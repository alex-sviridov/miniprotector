package common

import (
	"os"
)

type contextKey string

const HostnameContextKey contextKey = "hostname"

func GetHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}
