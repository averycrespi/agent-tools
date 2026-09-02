package authorization

import "strconv"

// AdmittedSubject is the sealed, immutable identity detached after an acknowledged ALLOW admission.
type AdmittedSubject struct {
	owner                 *Repository
	principalID           string
	principalRevision     string
	credentialID          string
	credentialRevision    string
	authorizationRevision string
}

// PrincipalID returns the stable principal identity.
func (subject AdmittedSubject) PrincipalID() string { return subject.principalID }

// PrincipalRevision returns the principal revision verified at admission.
func (subject AdmittedSubject) PrincipalRevision() string { return subject.principalRevision }

// CredentialID returns the credential identity verified at admission.
func (subject AdmittedSubject) CredentialID() string { return subject.credentialID }

// CredentialRevision returns the credential revision verified at admission.
func (subject AdmittedSubject) CredentialRevision() string { return subject.credentialRevision }

// AuthorizationRevision returns the policy revision evaluated at admission.
func (subject AdmittedSubject) AuthorizationRevision() string { return subject.authorizationRevision }

// OwnsAdmittedSubject reports whether this repository minted the complete safe subject.
func (repository *Repository) OwnsAdmittedSubject(subject AdmittedSubject) bool {
	return repository != nil && subject.validFor(repository)
}

func admittedSubject(repository *Repository, binding CredentialBinding, authorizationRevision string) AdmittedSubject {
	return AdmittedSubject{
		owner: repository, principalID: binding.PrincipalID, principalRevision: binding.PrincipalRevision,
		credentialID: binding.CredentialID, credentialRevision: binding.CredentialRevision,
		authorizationRevision: authorizationRevision,
	}
}

func (subject AdmittedSubject) validFor(repository *Repository) bool {
	return subject.owner == repository && validOpaqueID(subject.principalID) && validOpaqueID(subject.credentialID) &&
		validPositiveSubjectRevision(subject.principalRevision) && validPositiveSubjectRevision(subject.credentialRevision) &&
		validObservedAuthorizationRevision(subject.authorizationRevision) && subject.authorizationRevision != ""
}

func validPositiveSubjectRevision(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}
