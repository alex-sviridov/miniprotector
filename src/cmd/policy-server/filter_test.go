package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPolicy_Matches(t *testing.T) {
	cases := []struct {
		name     string
		filters  ClientFilters
		hostname string
		labels   map[string]string
		want     bool
	}{
		{
			name:     "empty filters match everyone",
			filters:  ClientFilters{},
			hostname: "anything",
			labels:   nil,
			want:     true,
		},
		{
			name:     "hostname glob matches",
			filters:  ClientFilters{Hostnames: []string{"web-*"}},
			hostname: "web-01",
			labels:   nil,
			want:     true,
		},
		{
			name:     "hostname glob does not match",
			filters:  ClientFilters{Hostnames: []string{"web-*"}},
			hostname: "db-01",
			labels:   nil,
			want:     false,
		},
		{
			name:     "all required labels present",
			filters:  ClientFilters{Labels: map[string]string{"env": "prod", "role": "db"}},
			hostname: "any",
			labels:   map[string]string{"env": "prod", "role": "db", "extra": "ignored"},
			want:     true,
		},
		{
			name:     "missing one required label",
			filters:  ClientFilters{Labels: map[string]string{"env": "prod", "role": "db"}},
			hostname: "any",
			labels:   map[string]string{"env": "prod"},
			want:     false,
		},
		{
			name:     "hostname matches but label missing -- AND fails",
			filters:  ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
			hostname: "web-01",
			labels:   map[string]string{},
			want:     false,
		},
		{
			name:     "hostname and label both match -- AND succeeds",
			filters:  ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
			hostname: "web-01",
			labels:   map[string]string{"env": "prod"},
			want:     true,
		},
		{
			name:     "labels match but hostname does not -- AND fails",
			filters:  ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
			hostname: "db-01",
			labels:   map[string]string{"env": "prod"},
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := PolicyBase{ClientFilters: tc.filters}
			assert.Equal(t, tc.want, p.Matches(tc.hostname, tc.labels))
		})
	}
}
