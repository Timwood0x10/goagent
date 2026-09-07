package eval

import "errors"

var (
	ErrNilExecutor          = errors.New("executor is nil")
	ErrInvalidMaxParallel   = errors.New("max_parallel must be >= 1")
	ErrEmptyName            = errors.New("evaluator name must not be empty")
	ErrNilEvaluator         = errors.New("evaluator must not be nil")
	ErrNilLLMClient         = errors.New("llm client is nil")
	ErrInvalidJudgeResponse = errors.New("invalid judge response")
	ErrNilRegistry          = errors.New("evaluator registry is nil")
	ErrEvaluatorNotFound    = errors.New("evaluator not found in registry")
)
