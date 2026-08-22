package keyring

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const (
	generationFormatVersion = 1
	generationEntropyBytes  = 32
	generationHandlePrefix  = "mgw_kg1_"
)

var (
	ErrIncompleteGeneration = errors.New("keyring generation is incomplete")

	secretMaximumBytes      = int(mustFixedLimit("keyring_secret_bytes"))
	storedChunkMaximumBytes = int(mustFixedLimit("keyring_chunk_bytes"))
	rawChunkMaximumBytes    = storedChunkMaximumBytes / 4 * 3
	maximumGenerationChunks = (secretMaximumBytes + rawChunkMaximumBytes - 1) / rawChunkMaximumBytes
)

type Handle string

func NewHandle(entropy io.Reader) (Handle, error) {
	value := make([]byte, generationEntropyBytes)
	if _, err := io.ReadFull(entropy, value); err != nil {
		return "", fmt.Errorf("generate keyring handle: %w", err)
	}
	return Handle(generationHandlePrefix + base64.RawURLEncoding.EncodeToString(value)), nil
}

func ParseHandle(value string) (Handle, error) {
	if !strings.HasPrefix(value, generationHandlePrefix) {
		return "", fmt.Errorf("invalid keyring handle")
	}
	encoded := strings.TrimPrefix(value, generationHandlePrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != generationEntropyBytes || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return "", fmt.Errorf("invalid keyring handle")
	}
	return Handle(value), nil
}

type generationManifest struct {
	Version int    `json:"version"`
	Owner   string `json:"owner"`
	Kind    string `json:"kind"`
	Handle  string `json:"handle"`
	Chunks  int    `json:"chunks"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}

func (provider *Provider) WriteGeneration(
	ctx context.Context,
	namespace Namespace,
	handle Handle,
	secret []byte,
) error {
	if len(secret) == 0 {
		return fmt.Errorf("keyring secret is empty")
	}
	if len(secret) > secretMaximumBytes {
		return ErrSecretTooLarge
	}
	if _, err := ParseHandle(string(handle)); err != nil {
		return err
	}
	if err := provider.validate(namespace, generationManifestItem(namespace, handle)); err != nil {
		return err
	}
	release, err := provider.acquireWork()
	if err != nil {
		return err
	}
	defer release()
	if err := provider.requireReady(ctx); err != nil {
		return err
	}

	chunks := (len(secret) + rawChunkMaximumBytes - 1) / rawChunkMaximumBytes
	written := 0
	for index := range chunks {
		if err := ctx.Err(); err != nil {
			provider.cleanupWrittenGeneration(namespace, handle, written)
			return &CapabilityError{Capability: capabilityForError(err)}
		}
		start := index * rawChunkMaximumBytes
		end := min(start+rawChunkMaximumBytes, len(secret))
		encoded := base64.RawURLEncoding.EncodeToString(secret[start:end])
		if len(encoded) > storedChunkMaximumBytes {
			return fmt.Errorf("encoded keyring chunk exceeds the fixed limit")
		}
		if err := provider.setReady(generationChunkItem(namespace, handle, index), encoded); err != nil {
			provider.cleanupWrittenGeneration(namespace, handle, written)
			return err
		}
		written++
	}

	digest := sha256.Sum256(secret)
	manifestValue, err := marshalGenerationManifest(generationManifest{
		Version: generationFormatVersion,
		Owner:   namespace.owner,
		Kind:    string(namespace.kind),
		Handle:  string(handle),
		Chunks:  chunks,
		Bytes:   len(secret),
		SHA256:  fmt.Sprintf("%x", digest),
	})
	if err != nil {
		provider.cleanupWrittenGeneration(namespace, handle, written)
		return err
	}
	if err := ctx.Err(); err != nil {
		provider.cleanupWrittenGeneration(namespace, handle, written)
		return &CapabilityError{Capability: capabilityForError(err)}
	}
	if err := provider.setReady(generationManifestItem(namespace, handle), manifestValue); err != nil {
		provider.cleanupWrittenGeneration(namespace, handle, written)
		return err
	}
	return nil
}

func (provider *Provider) ReadGeneration(
	ctx context.Context,
	namespace Namespace,
	handle Handle,
) ([]byte, error) {
	if _, err := ParseHandle(string(handle)); err != nil {
		return nil, err
	}
	manifestItem := generationManifestItem(namespace, handle)
	if err := provider.validate(namespace, manifestItem); err != nil {
		return nil, err
	}
	release, err := provider.acquireWork()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := provider.requireReady(ctx); err != nil {
		return nil, err
	}
	manifestValue, err := provider.getReady(manifestItem)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if _, chunkErr := provider.getReady(generationChunkItem(namespace, handle, 0)); chunkErr == nil {
			return nil, ErrIncompleteGeneration
		} else if !errors.Is(chunkErr, ErrNotFound) {
			return nil, chunkErr
		}
		return nil, ErrNotFound
	}
	manifest, err := parseGenerationManifest(manifestValue)
	if err != nil || !validGenerationManifest(manifest, namespace, handle) {
		return nil, ErrIncompleteGeneration
	}

	secret := make([]byte, 0, manifest.Bytes)
	for index := range manifest.Chunks {
		if err := ctx.Err(); err != nil {
			return nil, &CapabilityError{Capability: capabilityForError(err)}
		}
		value, getErr := provider.getReady(generationChunkItem(namespace, handle, index))
		if getErr != nil {
			if errors.Is(getErr, ErrNotFound) {
				return nil, ErrIncompleteGeneration
			}
			return nil, getErr
		}
		chunk, decodeErr := base64.RawURLEncoding.DecodeString(value)
		if decodeErr != nil || len(chunk) > rawChunkMaximumBytes {
			return nil, ErrIncompleteGeneration
		}
		secret = append(secret, chunk...)
	}
	if len(secret) != manifest.Bytes {
		return nil, ErrIncompleteGeneration
	}
	digest := sha256.Sum256(secret)
	if fmt.Sprintf("%x", digest) != manifest.SHA256 {
		return nil, ErrIncompleteGeneration
	}
	return secret, nil
}

func (provider *Provider) DeleteGeneration(ctx context.Context, namespace Namespace, handle Handle) error {
	if _, err := ParseHandle(string(handle)); err != nil {
		return err
	}
	manifestItem := generationManifestItem(namespace, handle)
	if err := provider.validate(namespace, manifestItem); err != nil {
		return err
	}
	release, err := provider.acquireWork()
	if err != nil {
		return err
	}
	defer release()
	if err := provider.requireReady(ctx); err != nil {
		return err
	}

	chunkCount := maximumGenerationChunks
	manifestValue, err := provider.getReady(manifestItem)
	if err == nil {
		manifest, parseErr := parseGenerationManifest(manifestValue)
		if parseErr == nil && validGenerationManifest(manifest, namespace, handle) {
			chunkCount = manifest.Chunks
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	var failures []error
	if err := provider.deleteReady(manifestItem); err != nil && !errors.Is(err, ErrNotFound) {
		failures = append(failures, err)
	}
	for index := range chunkCount {
		if err := ctx.Err(); err != nil {
			failures = append(failures, &CapabilityError{Capability: capabilityForError(err)})
			break
		}
		if err := provider.deleteReady(generationChunkItem(namespace, handle, index)); err != nil && !errors.Is(err, ErrNotFound) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (provider *Provider) cleanupWrittenGeneration(namespace Namespace, handle Handle, written int) {
	for index := range written {
		_ = provider.adapter.Delete(provider.service, generationChunkItem(namespace, handle, index))
	}
}

func (provider *Provider) setReady(item, value string) error {
	return classifyOperationError(provider.adapter.Set(provider.service, item, value))
}

func (provider *Provider) getReady(item string) (string, error) {
	value, err := provider.adapter.Get(provider.service, item)
	if err != nil {
		return "", classifyOperationError(err)
	}
	if value == "" {
		return "", ErrIncompleteGeneration
	}
	return value, nil
}

func (provider *Provider) deleteReady(item string) error {
	return classifyOperationError(provider.adapter.Delete(provider.service, item))
}

func generationManifestItem(namespace Namespace, handle Handle) string {
	return "candidate." + namespace.owner + "." + string(namespace.kind) + "." + string(handle) + ".manifest"
}

func generationChunkItem(namespace Namespace, handle Handle, index int) string {
	return fmt.Sprintf("candidate.%s.%s.%s.chunk.%03d", namespace.owner, namespace.kind, handle, index)
}

func marshalGenerationManifest(manifest generationManifest) (string, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode keyring generation manifest: %w", err)
	}
	if len(encoded) > storedChunkMaximumBytes {
		return "", fmt.Errorf("keyring generation manifest exceeds the fixed limit")
	}
	return string(encoded), nil
}

func parseGenerationManifest(value string) (generationManifest, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var manifest generationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return generationManifest{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return generationManifest{}, fmt.Errorf("keyring generation manifest has trailing data")
	}
	return manifest, nil
}

func validGenerationManifest(manifest generationManifest, namespace Namespace, handle Handle) bool {
	return manifest.Version == generationFormatVersion &&
		manifest.Owner == namespace.owner &&
		manifest.Kind == string(namespace.kind) &&
		manifest.Handle == string(handle) &&
		manifest.Chunks > 0 && manifest.Chunks <= maximumGenerationChunks &&
		manifest.Bytes > 0 && manifest.Bytes <= secretMaximumBytes &&
		len(manifest.SHA256) == sha256.Size*2
}

func mustFixedLimit(name string) int64 {
	limit, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing fixed keyring limit: " + name)
	}
	return limit.Maximum
}
