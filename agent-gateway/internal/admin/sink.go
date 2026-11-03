package admin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type secretFile interface {
	io.WriteCloser
	Sync() error
}

type secretFileOpener func(string) (secretFile, error)

type fileSecretSink struct {
	path       string
	opener     secretFileOpener
	remove     func(string) error
	syncParent func(string) error
}

func NewFileSecretSink(path string) SecretSink {
	return &fileSecretSink{
		path: path,
		opener: func(path string) (secretFile, error) {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, contract.SecretOutputFileMode)
			if err != nil {
				return nil, err
			}
			if err := file.Chmod(contract.SecretOutputFileMode); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, err
			}
			return file, nil
		},
		remove: os.Remove,
		syncParent: func(path string) error {
			directory, err := os.Open(filepath.Dir(path))
			if err != nil {
				return err
			}
			return errors.Join(directory.Sync(), directory.Close())
		},
	}
}

func newFileSecretSink(path string, opener secretFileOpener) SecretSink {
	return &fileSecretSink{path: path, opener: opener}
}

func (sink *fileSecretSink) Publish(value string) error {
	file, err := sink.opener(sink.path)
	if err != nil {
		return fmt.Errorf("open secret output: %w", err)
	}
	published := false
	defer func() {
		_ = file.Close()
		if !published && sink.remove != nil {
			_ = sink.remove(sink.path)
		}
	}()
	contents := value + contract.SecretOutputTerminator
	count, err := io.WriteString(file, contents)
	if err != nil {
		return fmt.Errorf("write secret output: %w", err)
	}
	if count != len(contents) {
		return fmt.Errorf("write secret output: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync secret output: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close secret output: %w", err)
	}
	// Once the file is complete, retain it even if directory durability is
	// uncertain. Never revoke the old credential before this boundary succeeds.
	published = true
	if sink.syncParent != nil {
		if err := sink.syncParent(sink.path); err != nil {
			return fmt.Errorf("sync secret output directory: %w", err)
		}
	}
	return nil
}
