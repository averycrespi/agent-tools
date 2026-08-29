//go:build stress

package grantrequests

import "testing"

func TestS5StressConcurrentSemanticDeduplication(t *testing.T) {
	TestSemanticDedupeConcurrentSubmissionNeverQueuesOrDuplicates(t)
}

func TestS5StressApprovalCancellationPolicyLinearization(t *testing.T) {
	TestApprovalConditionalBarriersHaveOneWinner(t)
}
