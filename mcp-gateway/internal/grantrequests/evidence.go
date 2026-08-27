package grantrequests

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/gowebpki/jcs"
)

var ErrInvalidEvidence = errors.New("grant request descriptor evidence is invalid")

func BuildDescriptorEvidence(source catalog.DurableDescriptor, namespace string, capturedAt time.Time) (contract.DescriptorEvidence, []byte, error) {
	resource := source.Resource
	if !utf8.ValidString(namespace) || len(namespace) < 1 || int64(len(namespace)) > fixedLimit("grant_request_target_bytes") ||
		!utf8.ValidString(resource.UpstreamName) || len(resource.UpstreamName) < 1 || int64(len(resource.UpstreamName)) > fixedLimit("grant_request_target_bytes") ||
		!utf8.ValidString(resource.ExternalName) || len(resource.ExternalName) < 1 || int64(len(resource.ExternalName)) > fixedLimit("grant_request_target_bytes") || capturedAt.IsZero() {
		return contract.DescriptorEvidence{}, nil, ErrInvalidEvidence
	}
	if _, err := contract.ParseDescriptorEvidenceState(string(source.State)); err != nil ||
		!opaqueIDPattern.MatchString(resource.ServerID) || !opaqueIDPattern.MatchString(resource.ID) ||
		(source.State == contract.EvidenceCurrent) != (resource.RetiredAt == nil) {
		return contract.DescriptorEvidence{}, nil, ErrInvalidEvidence
	}
	revision, err := strconv.ParseInt(resource.CatalogRevision, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != resource.CatalogRevision {
		return contract.DescriptorEvidence{}, nil, ErrInvalidEvidence
	}
	descriptorJSON, err := json.Marshal(resource.Descriptor)
	if err != nil {
		return contract.DescriptorEvidence{}, nil, ErrInvalidEvidence
	}
	normalized, err := catalog.NormalizeTool(catalog.RawTool{
		UpstreamName: resource.UpstreamName, ExternalName: resource.ExternalName, Descriptor: descriptorJSON,
	}, catalog.NormalizeOptions{ServerID: resource.ServerID, AllowHeaderBindings: true})
	if err != nil || normalized.Fingerprint != resource.Fingerprint || resource.ID == "" || resource.CatalogRevision == "" {
		return contract.DescriptorEvidence{}, nil, ErrInvalidEvidence
	}
	evidence := contract.DescriptorEvidence{
		ServerID: resource.ServerID, ToolID: resource.ID, Namespace: namespace,
		UpstreamName: resource.UpstreamName, ExternalName: resource.ExternalName,
		CatalogRevision: resource.CatalogRevision, Fingerprint: resource.Fingerprint,
		DurableState: source.State, Descriptor: normalized.Descriptor,
		CapturedAt: capturedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return contract.DescriptorEvidence{}, nil, ErrInvalidEvidence
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil || int64(len(canonical)) > fixedLimit("grant_request_evidence_snapshot_bytes") {
		return contract.DescriptorEvidence{}, nil, ErrInvalidEvidence
	}
	return evidence, append([]byte(nil), canonical...), nil
}
