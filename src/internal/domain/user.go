package domain

import "time"

type User struct {
	ID        string // OIDC Subject ID
	Email     string
	CreatedAt time.Time
	LastSeen  time.Time
}

type WatchProgress struct {
	UserID    string
	MediaID   string
	Position  float64 // Seconds
	Finished  bool    // > 90% watched
	UpdatedAt time.Time
}
