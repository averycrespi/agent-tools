package acceptance

var S5IntegrationTestManifest = []string{
	"TestBusyBeyondDeadlineLatchesMutationAcrossRestart",
	"TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline",
	"TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss",
	"TestRepositoryRetainsNewest4096ByMonotonicSequence",
	"TestRepositoryRollsBackEvictionWhenInsertFails",
	"TestRequestSchemaMigrationUsesRealSQLite",
	"TestRestoreAcceptedSchemaLineages",
	"TestS5IntegrationApprovalVsPolicyAndCapacity",
	"TestS5IntegrationAtomicApprovalAndPostCommitRecovery",
	"TestS5IntegrationCatalogEvidenceReplacement",
	"TestS5IntegrationCreateVsTargetAndPolicy",
	"TestS5IntegrationCredentialAdmissionOrdering",
	"TestS5IntegrationRequestLifecycle",
	"TestS5IntegrationRequestRetentionAndEvidenceLimits",
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
