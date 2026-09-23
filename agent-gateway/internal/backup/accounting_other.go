//go:build !darwin && !linux

package backup

import "os"

func openAccountingDirectory(string) (*os.File, error)                { return nil, ErrInvalidArtifact }
func openAccountingChildDirectory(*os.File, string) (*os.File, error) { return nil, ErrInvalidArtifact }
func openAccountingFile(*os.File, string) (*os.File, error)           { return nil, ErrInvalidArtifact }
