package model

import "time"

type Container struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Color             string     `json:"color"`
	Icon              string     `json:"icon,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	LastLaunchedAt    *time.Time `json:"lastLaunchedAt,omitempty"`
	Temporary         bool       `json:"temporary"`
	PendingCleanup    bool       `json:"pendingCleanup"`
	ProfilePath       string     `json:"profilePath"`
	BrowserType       string     `json:"browserType"`
	BrowserExecutable string     `json:"browserExecutable,omitempty"`
	PID               int        `json:"pid,omitempty"`
	Running           bool       `json:"running"`
}

type Database struct {
	Version    int         `json:"version"`
	Containers []Container `json:"containers"`
}
