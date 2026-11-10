package domain

import "time"

type HyperdriveConfig struct {
	ID           string    `json:"id"`
	Organization string    `json:"-"`
	Name         string    `json:"name"`
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	Database     string    `json:"database"`
	User         string    `json:"user"`
	Revision     int       `json:"-"`
	Created      time.Time `json:"created_at"`
}

type HyperdriveOrigin struct {
	HyperdriveConfig
	Secret EncryptedWorkerSecret `json:"-"`
}
