//go:build !darwin && !linux

package controlclient

func readBearerFile(string, int) (string, error) {
	return "", bearerSourceError(ErrBearerUnreadable)
}
