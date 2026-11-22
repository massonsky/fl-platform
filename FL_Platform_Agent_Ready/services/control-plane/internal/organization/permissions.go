package organization

type Permissions struct {
	Read          bool
	Write         bool
	ManageMembers bool
}

// PermissionsFor intersects current identity state with organization membership.
// A global administrator has no membership bypass; a legacy viewer never gains write.
func PermissionsFor(globalRole string, disabled bool, role, membershipStatus, organizationState string) Permissions {
	validGlobal := globalRole == "admin" || globalRole == "researcher" || globalRole == "viewer"
	validRole := role == "owner" || role == "admin" || role == "researcher" || role == "viewer"
	read := validGlobal && !disabled && validRole && membershipStatus == "active" &&
		(organizationState == "active" || organizationState == "suspended")
	write := read && organizationState == "active" && globalRole != "viewer" && role != "viewer"
	return Permissions{Read: read, Write: write, ManageMembers: write && (role == "owner" || role == "admin")}
}

func canChangeMember(actorRole, currentRole, nextRole string) bool {
	return actorRole == "owner" || (actorRole == "admin" && currentRole != "owner" && nextRole != "owner")
}
