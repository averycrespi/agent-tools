package acceptance

var S5IntegrationTestManifest = []string{
	"TestAtomicApprovalAndPostCommitRecoveryIntegration",
	"TestBusyBeyondDeadlineLatchesMutationAcrossRestart",
	"TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline",
	"TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss",
	"TestRepositoryRetainsNewest4096ByMonotonicSequence",
	"TestRepositoryRollsBackEvictionWhenInsertFails",
	"TestRequestLifecycleIntegration",
	"TestRequestRetentionAndEvidenceLimitsIntegration",
	"TestRequestSchemaMigrationUsesRealSQLite",
	"TestRestoreAcceptedSchemaLineages",
	"TestS5IntegrationApprovalVsPolicyAndCapacity",
	"TestS5IntegrationCatalogEvidenceReplacement",
	"TestS5IntegrationCreateVsTargetAndPolicy",
	"TestS5IntegrationCredentialAdmissionOrdering",
}

var S5StressTestManifest = []string{
	"TestS5StressApprovalCancellationPolicyLinearization",
	"TestS5StressConcurrentSemanticDeduplication",
	"TestS5StressLocalInvocationDrainCleanup",
	"TestS5StressLostLocalMutationResponseDeduplicatesExplicitRetry",
	"TestS5StressSyntheticSnapshotPagination",
}

var S5SecurityTestManifest = []string{
	"TestS5SecurityAcceptanceReportSinks",
	"TestS5SecurityDurableSinkCanaries",
	"TestS5SecurityStaticSinkClosure",
}
