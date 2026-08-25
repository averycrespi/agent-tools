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
