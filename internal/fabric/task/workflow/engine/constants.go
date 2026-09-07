package engine

import "time"

// Default configuration constants for workflow engine.
const (
	// DefaultMaxParallel is the default maximum number of parallel steps.
	DefaultMaxParallel = 10

	// DefaultStepTimeout is the default timeout for individual step execution.
	DefaultStepTimeout = 10 * time.Second

	// DefaultInitialDelay is the default initial delay for retry.
	DefaultInitialDelay = 10 * time.Millisecond

	// DefaultMaxDelay is the default maximum delay for retry.
	DefaultMaxDelay = 100 * time.Millisecond

	// DefaultRetryAttempts is the default number of retry attempts.
	DefaultRetryAttempts = 3

	// DefaultWorkflowTimeout is the default timeout for workflow execution.
	DefaultWorkflowTimeout = 5 * time.Minute

	// DefaultStepWaitDuration is the default duration to wait between step checks.
	DefaultStepWaitDuration = 100 * time.Millisecond

	// DefaultDAGTraversalTimeout is the default timeout for DAG traversal.
	DefaultDAGTraversalTimeout = 1 * time.Minute

	// DefaultMaxWorkflowSize is the default maximum number of steps in a workflow.
	DefaultMaxWorkflowSize = 100

	// DefaultMaxDependencies is the default maximum number of dependencies per step.
	DefaultMaxDependencies = 10

	// DefaultExecutorStepTimeout is the default step-level timeout used by the Executor.
	DefaultExecutorStepTimeout = 5 * time.Minute

	// DefaultDeadlockTimeout is the default time to wait before declaring a deadlock.
	DefaultDeadlockTimeout = 5 * time.Second

	// DefaultRecoveryPollInterval is the default interval to poll for recovery signals.
	DefaultRecoveryPollInterval = 10 * time.Millisecond
)
