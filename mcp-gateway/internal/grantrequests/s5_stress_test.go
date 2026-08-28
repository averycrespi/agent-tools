//go:build stress

package grantrequests

import "testing"

func TestS5StressConcurrentSemanticDeduplication(t *testing.T) {
	TestS5SemanticDedupeConcurrentSubmissionNeverQueuesOrDuplicates(t)
}

func TestS5StressApprovalCancellationPolicyLinearization(t *testing.T) {
	TestS5ApprovalConditionalBarriersHaveOneWinner(t)
}
