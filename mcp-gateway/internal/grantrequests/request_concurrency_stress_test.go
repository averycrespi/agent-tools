//go:build stress

package grantrequests

import "testing"

func TestConcurrentSemanticDeduplicationStress(t *testing.T) {
	TestSemanticDedupeConcurrentSubmissionNeverQueuesOrDuplicates(t)
}

func TestApprovalCancellationPolicyLinearizationStress(t *testing.T) {
	TestApprovalConditionalBarriersHaveOneWinner(t)
}
