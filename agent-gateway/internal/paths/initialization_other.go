//go:build !darwin && !linux

package paths

import "os"

func openInitializationFile(string) (*os.File, error)   { return nil, ErrUnsafePath }
func initializationRootIdentity(string) (string, error) { return "", ErrUnsafePath }
