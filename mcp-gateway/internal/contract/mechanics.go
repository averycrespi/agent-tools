package contract

import "fmt"

func ServerETag(serverID, desiredRevision string) string {
	return fmt.Sprintf(`"server-%s-%s"`, serverID, desiredRevision)
}

func MatchesServerETag(value, serverID, desiredRevision string) bool {
	return value == ServerETag(serverID, desiredRevision)
}

func PrincipalETag(principalID, revision string) string {
	return fmt.Sprintf(`"principal-%s-%s"`, principalID, revision)
}

func MatchesPrincipalETag(value, principalID, revision string) bool {
	return value == PrincipalETag(principalID, revision)
}

func GrantRequestETag(requestID, revision string) string {
	return fmt.Sprintf(`"grant-request-%s-%s"`, requestID, revision)
}

func MatchesGrantRequestETag(value, requestID, revision string) bool {
	return value == GrantRequestETag(requestID, revision)
}

func AdminAuthorityETag(revision string) string {
	return fmt.Sprintf(`"admin-authority-%s"`, revision)
}

func MatchesAdminAuthorityETag(value, revision string) bool {
	return value == AdminAuthorityETag(revision)
}
