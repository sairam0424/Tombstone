package tombstone.audit

import future.keywords.if
import future.keywords.in

default allow = false

allow if {
	input.role in ["viewer", "operator", "owner", "admin"]
	input.method == "GET"
}
