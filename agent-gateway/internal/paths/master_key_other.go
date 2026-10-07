//go:build !darwin && !linux

package paths

import "errors"

const MasterKeyName = "master-key"

func DurableMasterKey(*Ownership) ([]byte, error) {
	return nil, errors.New("master key storage is unsupported on this platform")
}

func MasterKey(*Ownership, []byte) ([]byte, error) {
	return nil, errors.New("master key storage is unsupported on this platform")
}
