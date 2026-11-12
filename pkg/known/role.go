package known

// Role constants define the access control roles used throughout the
// application. User accounts are granted roles through the authorization
// (casbin) grouping policy mechanism to enforce access control and
// permission checks.

const (
	// RoleUser is the default role assigned to all authenticated users.
	// This role grants access to basic application functionality.
	RoleUser = "user"

	// RoleAdmin is the elevated administrative role granting access to
	// administrative and management operations.
	RoleAdmin = "admin"
)
