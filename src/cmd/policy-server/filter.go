package main

import "path"

// Matches reports whether a client with the given hostname and attribute
// labels satisfies this policy's client_filters: an empty hostname pattern
// list matches any hostname; a non-empty list requires at least one glob
// match. Every key/value pair in client_filters.labels must be present in
// labels -- extra labels the client has beyond what's listed don't
// disqualify a match. Both conditions must hold (AND); there is no
// either-hostname-or-labels mode.
func (b PolicyBase) Matches(hostname string, labels map[string]string) bool {
	if !hostnameMatches(b.ClientFilters.Hostnames, hostname) {
		return false
	}
	return labelsMatch(b.ClientFilters.Labels, labels)
}

func hostnameMatches(patterns []string, hostname string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, hostname); ok {
			return true
		}
	}
	return false
}

func labelsMatch(required, actual map[string]string) bool {
	for k, v := range required {
		if actual[k] != v {
			return false
		}
	}
	return true
}
