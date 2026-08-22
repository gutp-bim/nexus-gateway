// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import "testing"

func TestFilterMatches(t *testing.T) {
	cases := []struct {
		filter, topic string
		want          bool
	}{
		{"sensors/room1/temp", "sensors/room1/temp", true},
		{"sensors/room1/temp", "sensors/room1/humidity", false},
		{"sensors/+/temp", "sensors/room1/temp", true},
		{"sensors/+/temp", "sensors/room1/room2/temp", false},
		{"sensors/#", "sensors/room1/temp", true},
		{"sensors/#", "sensors", true}, // MQTT 4.7.1.2: "sport/#" also matches the parent level "sport"
		{"sensors", "sensors", true},
		{"#", "sensors/room1/temp", true},
		{"#", "$SYS/broker/uptime", false},     // "#" alone must not match $ topics
		{"$SYS/#", "$SYS/broker/uptime", true}, // explicit $ prefix in filter does match
		{"+/room1/temp", "sensors/room1/temp", true},
		{"+/room1/temp", "$SYS/room1/temp", false}, // leading + must not match a $ level either
		{"sensors/+", "sensors/room1/temp", false},
		{"sensors/room1/#", "sensors/room1", true},
	}
	for _, c := range cases {
		if got := filterMatches(c.filter, c.topic); got != c.want {
			t.Errorf("filterMatches(%q, %q) = %v, want %v", c.filter, c.topic, got, c.want)
		}
	}
}

func TestHasWildcard(t *testing.T) {
	cases := []struct {
		filter string
		want   bool
	}{
		{"sensors/room1/temp", false},
		{"sensors/+/temp", true},
		{"sensors/#", true},
		{"sensors", false},
	}
	for _, c := range cases {
		if got := hasWildcard(c.filter); got != c.want {
			t.Errorf("hasWildcard(%q) = %v, want %v", c.filter, got, c.want)
		}
	}
}

func TestValidatePoints(t *testing.T) {
	cases := []struct {
		name    string
		points  []PointConfig
		wantErr bool
	}{
		{"valid read-only", []PointConfig{{Topic: "a"}}, false},
		{"valid writable", []PointConfig{{Topic: "a", Writable: true, CommandTopic: "a"}}, false},
		{"empty topic", []PointConfig{{Topic: ""}}, true},
		{"writable without command_topic", []PointConfig{{Topic: "a", Writable: true}}, true},
		{"valid qos 0", []PointConfig{{Topic: "a", QoS: 0}}, false},
		{"valid qos 1", []PointConfig{{Topic: "a", QoS: 1}}, false},
		{"invalid qos 2", []PointConfig{{Topic: "a", QoS: 2}}, true},
		{"invalid command_qos", []PointConfig{{Topic: "a", Writable: true, CommandTopic: "a", CommandQoS: 5}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePoints(c.points)
			if c.wantErr && err == nil {
				t.Errorf("ValidatePoints(%+v) = nil, want error", c.points)
			}
			if !c.wantErr && err != nil {
				t.Errorf("ValidatePoints(%+v) = %v, want nil", c.points, err)
			}
		})
	}
}
