package asana

import (
	"strings"
	"time"
)

// Ref is the compact form Asana uses when embedding one resource inside
// another.
type Ref struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type User struct {
	GID   string `json:"gid"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Status is a project's current_status.
type Status struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Color string `json:"color"`
}

type Project struct {
	GID         string     `json:"gid"`
	Name        string     `json:"name"`
	Archived    bool       `json:"archived"`
	Completed   bool       `json:"completed"`
	CompletedAt *time.Time `json:"completed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	ModifiedAt  time.Time  `json:"modified_at"`
	// due_on and start_on are calendar dates, not timestamps.
	DueOn         string  `json:"due_on"`
	StartOn       string  `json:"start_on"`
	Notes         string  `json:"notes"`
	Public        bool    `json:"public"`
	PermalinkURL  string  `json:"permalink_url"`
	CurrentStatus *Status `json:"current_status"`
	Owner         *Ref    `json:"owner"`
	Team          *Ref    `json:"team"`
	Members       []Ref   `json:"members"`
	Followers     []Ref   `json:"followers"`
}

// Asana returns only name and gid unless fields are asked for by name, and
// silently ignores names it does not know. gid always comes back, nested refs
// included, so it is never worth requesting. The workspace is left out because
// we only ever read the one we were configured with.
var (
	userFields = strings.Join([]string{
		"name", "email",
	}, ",")

	projectFields = strings.Join([]string{
		"name", "archived", "completed", "completed_at",
		"created_at", "modified_at", "due_on", "start_on",
		"notes", "public", "permalink_url",
		"current_status.title", "current_status.text", "current_status.color",
		"owner.name", "team.name", "members.name", "followers.name",
	}, ",")
)
