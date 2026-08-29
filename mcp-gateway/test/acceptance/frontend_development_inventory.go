package acceptance

import "time"

const (
	FrontendDevelopmentInventoryVersion = 1
	FrontendDevelopmentTarget           = "test-frontend-development"
	FrontendDevelopmentAggregatePhase   = "M4 combined checkpoint"
	FrontendDevelopmentAggregateRuns    = 1
	FrontendDevelopmentStructuralRuns   = 1
)

const FrontendDevelopmentBudget = 150 * time.Second

type FrontendDevelopmentLeaf struct {
	ID              string
	Command         string
	Timeout         time.Duration
	ExpectedMatches int
	GatewayStarts   int
	ChromiumStarts  int
}

var FrontendDevelopmentLeaves = []FrontendDevelopmentLeaf{
	{
		ID:              "development.node-proxy-matrix",
		Command:         "npm --prefix .. run ui:test-dev",
		Timeout:         30 * time.Second,
		ExpectedMatches: 15,
	},
	{
		ID:              "development.browser-workflows",
		Command:         "go test -race -count=1 -tags=e2e,browser -timeout=2m -run '^TestFrontendDevelopment(LiveReload|ControlPlane)$' ./test/e2e",
		Timeout:         2 * time.Minute,
		ExpectedMatches: 2,
		GatewayStarts:   2,
		ChromiumStarts:  2,
	},
}
