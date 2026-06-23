package tombstone.flags

import future.keywords.if
import future.keywords.in

default allow = false

# Viewers can read anything
allow if {
	input.role in ["viewer", "operator", "owner", "admin"]
	input.method == "GET"
}

# Operators can create, update flags and environments
allow if {
	input.role in ["operator", "owner", "admin"]
	input.method in ["POST", "PATCH"]
	input.path[0] == "api"
}

# Only owners and admins can kill-switch or archive
allow if {
	input.role in ["owner", "admin"]
	input.path[4] in ["kill", "prerequisites", "schedule"]
}

# Admins can do everything
allow if { input.role == "admin" }
