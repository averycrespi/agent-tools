package acceptance

var S5IntegrationTestManifest = []string{
	"TestApprovalVsPolicyAndCapacityIntegration",
	"TestAtomicApprovalAndPostCommitRecoveryIntegration",
	"TestBusyBeyondDeadlineLatchesMutationAcrossRestart",
	"TestCatalogEvidenceReplacementIntegration",
	"TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline",
	"TestCreateVsTargetAndPolicyIntegration",
	"TestCredentialAdmissionOrderingIntegration",
	"TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss",
	"TestRepositoryRetainsNewest4096ByMonotonicSequence",
	"TestRepositoryRollsBackEvictionWhenInsertFails",
	"TestRequestLifecycleIntegration",
	"TestRequestRetentionAndEvidenceLimitsIntegration",
	"TestRequestSchemaMigrationUsesRealSQLite",
	"TestRestoreAcceptedSchemaLineages",
	"TestServicePublishesOneCompleteStaticGenerationAndSafeOperation",
}

var S5StressTestManifest = []string{
	"TestApprovalCancellationPolicyLinearizationStress",
	"TestConcurrentSemanticDeduplicationStress",
	"TestLocalInvocationDrainCleanupStress",
	"TestLostLocalMutationResponseDeduplicatesExplicitRetryStress",
	"TestSyntheticSnapshotPaginationStress",
}

var S5SecurityTestManifest = []string{
	"TestAcceptanceReportSecretSinkBoundaries",
	"TestDurableSecretSinkBoundaries",
	"TestStaticSecretSinkClosure",
}
