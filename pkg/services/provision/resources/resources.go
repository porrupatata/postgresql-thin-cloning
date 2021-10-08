/*
2020 © Postgres.ai
*/

// Package resources defines models used for provisioning.
package resources

import (
	"time"
)

// Session defines clone provision information and connection info.
type Session struct {
	ID   string `json:"id"`
	Pool string `json:"pool"`

	// Database.
	Port          uint              `json:"port"`
	User          string            `json:"user"`
	SocketHost    string            `json:"socket_host"`
	EphemeralUser EphemeralUser     `json:"ephemeral_user"`
	ExtraConfig   map[string]string `json:"extra_config"`
}

// Disk defines disk status.
// TODO(anatoly): Merge with disk from models?
type Disk struct {
	Size     uint64
	Free     uint64
	Used     uint64
	DataSize uint64
}

// EphemeralUser describes an ephemeral database user defined by Database Lab users.
type EphemeralUser struct {
	// TODO(anatoly): Were private fields. How to keep them private?
	Name        string `json:"name"`
	Password    string `json:"password"`
	Restricted  bool   `json:"restricted"`
	AvailableDB string `json:"available_db"`
}

// Snapshot defines snapshot of the data with related meta-information.
type Snapshot struct {
	ID          string
	CreatedAt   time.Time
	DataStateAt time.Time
}

// SessionState defines current state of a Session.
type SessionState struct {
	CloneDiffSize uint64
}
