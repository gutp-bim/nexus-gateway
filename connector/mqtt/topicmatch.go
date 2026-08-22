// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import "strings"

// filterMatches reports whether topic matches the MQTT topic filter, per the
// MQTT 5.0 spec (§4.7): "+" matches exactly one level, a trailing "#" matches
// that level and everything below it, and neither wildcard matches a topic
// whose first level starts with "$" unless the filter's first level does too.
//
// Used to decide whether a Point-List-synced topic is already covered by a
// static MQTT_SUBSCRIPTIONS wildcard (docs/adr/0008) — a plain equality check
// would miss "sensors/+/temp" covering "sensors/room1/temp".
func filterMatches(filter, topic string) bool {
	if filter == topic {
		return true
	}
	filterLevels := strings.Split(filter, "/")
	topicLevels := strings.Split(topic, "/")

	if strings.HasPrefix(topicLevels[0], "$") && !strings.HasPrefix(filterLevels[0], "$") {
		return false
	}

	for i, fl := range filterLevels {
		if fl == "#" {
			// "#" must be the last filter level (a malformed filter is treated
			// as matching nothing beyond what's already been consumed).
			return i == len(filterLevels)-1
		}
		if i >= len(topicLevels) {
			return false
		}
		if fl != "+" && fl != topicLevels[i] {
			return false
		}
	}
	return len(filterLevels) == len(topicLevels)
}

// hasWildcard reports whether filter contains a "+" or "#" wildcard level.
func hasWildcard(filter string) bool {
	for _, level := range strings.Split(filter, "/") {
		if level == "+" || level == "#" {
			return true
		}
	}
	return false
}
