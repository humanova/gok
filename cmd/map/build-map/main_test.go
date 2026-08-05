package main

import "testing"

func TestIsDurableTopic(t *testing.T) {
	tests := []struct {
		name    string
		node    mapNode
		durable bool
	}{
		{
			name:    "accepts balanced recurring discussion",
			node:    mapNode{DistinctAuthors: 30, ReturningAuthors: 6, ActiveMonths: 6, PeakWeekShare: 0.50},
			durable: true,
		},
		{
			name:    "rejects too few returning writers",
			node:    mapNode{DistinctAuthors: 200, ReturningAuthors: 5, ActiveMonths: 24, PeakWeekShare: 0.10},
			durable: false,
		},
		{
			name:    "rejects concentrated short-lived topic",
			node:    mapNode{DistinctAuthors: 200, ReturningAuthors: 20, ActiveMonths: 17, PeakWeekShare: 0.80},
			durable: false,
		},
		{
			name:    "accepts established topic despite exceptional spike",
			node:    mapNode{DistinctAuthors: 30, ReturningAuthors: 15, ActiveMonths: 18, PeakWeekShare: 0.95},
			durable: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isDurableTopic(&test.node); got != test.durable {
				t.Fatalf("isDurableTopic() = %t, want %t", got, test.durable)
			}
		})
	}
}
