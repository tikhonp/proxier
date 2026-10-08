package pages

import (
	"github.com/a-h/templ"
)

// DashRow is a server on the dashboard.
type DashRow struct {
	ID           int64
	Name, Flag   string
	Kind, Word   string
	Reason, Note string
}

// DashRank is a line of a top-3 list.
type DashRank struct {
	ID          int64
	Name, Value string
}

// DashCheck is an item of the first-run checklist.
type DashCheck struct {
	Label, Href string
	Done        bool
}

// DashboardView is what the servers area shows.
type DashboardView struct {
	Total int
	// Lifecycle counts by state; Health counts the active servers by health.
	Counts map[string]int
	Health map[string]int
	// Home is the reference check's state; HomeSince is already formatted.
	Home, HomeSince string
	Failed          []DashRow
	NotHealthy      []DashRow
	Updates         []DashRow
	TopDisk         []DashRank
	TopTraffic      []DashRank
	Checklist       []DashCheck
}

// DashboardServers is the body of the servers area on the dashboard.
func DashboardServers(v DashboardView) templ.Component { return dashboardServers(v) }
