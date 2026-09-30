package keyring

import "context"

type authorityAdmissionKey struct{}

// WithGitAuthorityAdmission supplies the singular authorization owner's gate to
// each Git authority transaction. It is never acquired inside SQL or held over
// provider work; missing admission makes Git authority mutations fail closed.
func WithGitAuthorityAdmission(ctx context.Context, acquire func(context.Context) (func(), error)) context.Context {
	return context.WithValue(ctx, authorityAdmissionKey{}, acquire)
}

func acquireGitAuthority(ctx context.Context, namespace Namespace) (func(), error) {
	if namespace.kind != RecordGitCredential {
		return func() {}, nil
	}
	acquire, ok := ctx.Value(authorityAdmissionKey{}).(func(context.Context) (func(), error))
	if !ok || acquire == nil {
		return nil, ErrNoAuthority
	}
	release, err := acquire(ctx)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, ErrNoAuthority
	}
	return release, nil
}
