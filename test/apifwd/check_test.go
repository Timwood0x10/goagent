// Package apifwd verifies the M5 forwarding layers keep the full public API
// surface: every symbol and method that existed in api/tools, api/mcp,
// api/service/llm, api/evolution (with genome and mutation subpackages),
// and api/discovery before internalization must remain usable through the
// forwarding layer. The checks are compile-time typed assignments; the test
// body is a no-op.
package apifwd

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/api/discovery"
	"github.com/Timwood0x10/ares/api/evolution"
	"github.com/Timwood0x10/ares/api/evolution/genome"
	"github.com/Timwood0x10/ares/api/evolution/mutation"
	"github.com/Timwood0x10/ares/api/mcp"
	llmsvc "github.com/Timwood0x10/ares/api/service/llm"
	tools "github.com/Timwood0x10/ares/api/tools"
	internaldiscovery "github.com/Timwood0x10/ares/internal/discovery"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	internalmutation "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// api/tools function symbols.
var (
	_ func() *tools.Registry                                       = tools.NewRegistry
	_ func() *tools.Registry                                       = tools.NewEmptyRegistry
	_ func(*tools.Registry) (*tools.Planner, error)                = tools.NewPlanner
	_ func(*tools.Registry, *tools.Planner) (*tools.Bridge, error) = tools.NewBridge
	_ func(*tools.Registry, ...tools.BuiltinToolsOption) error     = tools.RegisterBuiltinTools
	_ func(string) tools.BuiltinToolsOption                        = tools.WithFileSandboxDir
	_ func(string) (string, error)                                 = tools.FilePath
)

// api/tools type symbols.
var (
	_ tools.Result
	_ tools.Tool
	_ tools.ToolFunc
	_ tools.Registry
	_ tools.ToolInfo
	_ tools.RegistryPlannerProvider
	_ tools.Planner
	_ tools.ExecutionPlan
	_ tools.Bridge
	_ tools.BuiltinToolsOption
)

// Registry method set (transmitted via type alias).
var (
	_ func(tools.Tool) error                                              = (*tools.Registry)(nil).Register
	_ func(string) error                                                  = (*tools.Registry)(nil).Unregister
	_ func(string) (tools.Tool, bool)                                     = (*tools.Registry)(nil).Get
	_ func(context.Context, string, map[string]any) (tools.Result, error) = (*tools.Registry)(nil).Execute
	_ func() []string                                                     = (*tools.Registry)(nil).List
	_ func() []tools.ToolInfo                                             = (*tools.Registry)(nil).ListTools
	_ func() []string                                                     = (*tools.Registry)(nil).ListToolNames
	_ func(string) ([]string, error)                                      = (*tools.Registry)(nil).GetToolCapabilities
	_ func() *tools.RegistryPlannerProvider                               = (*tools.Registry)(nil).PlannerProvider
	_ func() (*core.Registry, error)                                      = (*tools.Registry)(nil).CoreRegistry
)

// ToolFunc method set.
var (
	_ func() string                                               = tools.ToolFunc{}.Name
	_ func() string                                               = tools.ToolFunc{}.Description
	_ func() map[string]any                                       = tools.ToolFunc{}.Parameters
	_ func() []string                                             = tools.ToolFunc{}.Capabilities
	_ func(context.Context, map[string]any) (tools.Result, error) = tools.ToolFunc{}.Execute
)

// RegistryPlannerProvider method set.
var (
	_ func() []string                = (*tools.RegistryPlannerProvider)(nil).ListTools
	_ func(string) ([]string, error) = (*tools.RegistryPlannerProvider)(nil).GetToolCapabilities
)

// api/mcp function and type symbols.
var (
	_ func(context.Context, mcp.ServerConfig) (*mcp.Client, error)         = mcp.ConnectFromConfig
	_ func(context.Context, string, string) (*mcp.Client, error)           = mcp.ConnectSSE
	_ func(context.Context, string, string, []string) (*mcp.Client, error) = mcp.ConnectStdio
	_ func(string) []mcp.ServerConfig                                      = mcp.DiscoverServers

	_ mcp.Client
	_ mcp.ToolInfo
	_ mcp.CallResult
	_ mcp.ContentBlock
	_ mcp.ServerConfig
)

// Client method set.
var (
	_ func(context.Context) ([]mcp.ToolInfo, error)                          = (*mcp.Client)(nil).ListTools
	_ func(context.Context, string, map[string]any) (*mcp.CallResult, error) = (*mcp.Client)(nil).CallTool
	_ func() string                                                          = (*mcp.Client)(nil).Name
	_ func() error                                                           = (*mcp.Client)(nil).Close
)

// api/service/llm symbols and Service method set.
var (
	_ func(*llmsvc.Config) (*llmsvc.Service, error) = llmsvc.NewService

	_ llmsvc.Config
	_ llmsvc.Service

	_ func(context.Context, *llmcore.GenerateRequest) (*llmcore.GenerateResponse, error)   = (*llmsvc.Service)(nil).Generate
	_ func(context.Context, string) (string, error)                                        = (*llmsvc.Service)(nil).GenerateSimple
	_ func(context.Context, *llmcore.EmbeddingRequest) (*llmcore.EmbeddingResponse, error) = (*llmsvc.Service)(nil).GenerateEmbedding
	_ func() *llmcore.LLMConfig                                                            = (*llmsvc.Service)(nil).GetConfig
	_ func() bool                                                                          = (*llmsvc.Service)(nil).IsEnabled
	_ func() llmcore.LLMProvider                                                           = (*llmsvc.Service)(nil).GetProvider
	_ func() string                                                                        = (*llmsvc.Service)(nil).GetModel
	_ func()                                                                               = (*llmsvc.Service)(nil).Close
)

// api/evolution symbols (M5 §8-A7).
var (
	_ evolution.Strategy
	_ evolution.Lineage
	_ evolution.Agent
	_ evolution.CallbackData
	_ evolution.DreamCycle
	_ evolution.DreamCycleConfig
	_ evolution.Population
	_ evolution.PopulationConfig
	_ evolution.ScorerFunc
	_ evolution.Mutator
	_ evolution.MutationConfig
	_ evolution.Promoter
	_ evolution.PromotionCriteria

	_ func() evolution.DreamCycleConfig                                                   = evolution.DefaultDreamCycleConfig
	_ func(any, any, ...any) (evolution.DreamCycle, error)                                = evolution.NewDreamCycle
	_ func() evolution.PopulationConfig                                                   = evolution.DefaultPopulationConfig
	_ func(*evolution.Strategy, evolution.PopulationConfig) (evolution.Population, error) = evolution.NewPopulation
	_ func(string, evolution.MutationConfig) (evolution.Mutator, error)                   = evolution.NewMutator
	_ func() evolution.PromotionCriteria                                                  = evolution.DefaultPromotionCriteria
	_ func(*evolution.PromotionCriteria) evolution.Promoter                               = evolution.NewPromoter
)

// evolution.DreamCycle method set (method expressions: signature pinning
// without evaluating a nil receiver).
var (
	_ func(evolution.DreamCycle, context.Context, evolution.CallbackData) error = evolution.DreamCycle.Run
	_ func(evolution.DreamCycle, bool)                                          = evolution.DreamCycle.SetEnabled
	_ func(evolution.DreamCycle) bool                                           = evolution.DreamCycle.IsEnabled
	_ func(evolution.DreamCycle) int64                                          = evolution.DreamCycle.TaskCount
)

// evolution.Population method set.
var (
	_ func(evolution.Population) []evolution.Agent      = evolution.Population.Agents
	_ func(evolution.Population) int                    = evolution.Population.Size
	_ func(evolution.Population) int                    = evolution.Population.CurrentGeneration
	_ func(evolution.Population) float64                = evolution.Population.BestScore
	_ func(evolution.Population) *evolution.Strategy    = evolution.Population.BestStrategy
	_ func(evolution.Population, evolution.ScorerFunc)  = evolution.Population.ScoreAgents
	_ func(evolution.Population, context.Context) error = evolution.Population.Evolve
)

// evolution.Mutator method set.
var (
	_ func(evolution.Mutator, context.Context, *evolution.Strategy) (*evolution.Strategy, error) = evolution.Mutator.Mutate
)

// evolution.Promoter method set.
var (
	_ func(evolution.Promoter, context.Context, string, float64, float64) (string, error) = evolution.Promoter.Evaluate
	_ func(evolution.Promoter, context.Context, string) error                             = evolution.Promoter.Promote
	_ func(evolution.Promoter, context.Context, string) error                             = evolution.Promoter.Demote
)

// api/evolution/genome symbols.
var (
	_ genome.CrossoverType
	_ genome.PromptCrossoverMode
	_ genome.Crosser
	_ genome.CrosserConfig

	_ = genome.CrossoverUniform
	_ = genome.CrossoverSinglePoint
	_ = genome.CrossoverTwoPoint
	_ = genome.CrossoverScattered

	_ = genome.PromptInherit
	_ = genome.PromptHalfSplit
	_ = genome.PromptUniform

	_ func(genome.CrosserConfig) (*genome.Crosser, error) = genome.NewCrosser
)

// genome.Crosser method set.
var (
	_ func(context.Context, *mutation.Strategy, *mutation.Strategy) (*mutation.Strategy, error)                       = (*genome.Crosser)(nil).Crossover
	_ func(context.Context, *mutation.Strategy, *mutation.Strategy, genome.CrossoverType) (*mutation.Strategy, error) = (*genome.Crosser)(nil).CrossWithType
)

// api/evolution/mutation symbols.
var (
	_ mutation.MutationType
	_ mutation.Strategy
	_ mutation.Mutator
	_ mutation.MutatorConfig

	_ = mutation.MutationParameter
	_ = mutation.MutationPrompt
	_ = mutation.MutationTool
	_ = mutation.MutationCrossover
	_ = mutation.MutationRoot

	_ func(*internalmutation.Strategy) *mutation.Strategy     = mutation.FromInternal
	_ func(mutation.MutatorConfig) (*mutation.Mutator, error) = mutation.NewMutator
	_ func(*mutation.Strategy) *internalmutation.Strategy     = (*mutation.Strategy).ToInternal
)

// mutation.Mutator method set.
var (
	_ func(context.Context, *mutation.Strategy) (*mutation.Strategy, error)        = (*mutation.Mutator)(nil).Mutate
	_ func(context.Context, *mutation.Strategy, int) ([]*mutation.Strategy, error) = (*mutation.Mutator)(nil).MutateN
)

// api/discovery symbols (M5 §8-A7).
var (
	_ func(discovery.EngineConfig) *discovery.Engine = discovery.NewEngine
	_ func() discovery.EngineConfig                  = func() discovery.EngineConfig { return discovery.EngineConfig{} }

	_ discovery.ServiceType
	_ discovery.Confidence
	_ discovery.ServiceIdentity
	_ discovery.DiscoveryRecord
	_ discovery.DiscoveredService
	_ discovery.HealthStatus
	_ discovery.EventType
	_ discovery.Event
	_ discovery.ServiceStore
	_ discovery.RegisterRequest
	_ discovery.UpdateTagsRequest
	_ discovery.EngineConfig
	_ discovery.Engine

	_ = discovery.ServiceTypeMCP
	_ = discovery.ServiceTypeHTTP
	_ = discovery.ServiceTypeGRPC
	_ = discovery.ServiceTypeCLI
	_ = discovery.ServiceTypeDocker

	_ = discovery.ConfidenceLow
	_ = discovery.ConfidenceMedium
	_ = discovery.ConfidenceHigh
	_ = discovery.ConfidenceMax

	_ = discovery.EventServiceAdded
	_ = discovery.EventServiceRemoved
	_ = discovery.EventServiceUpdated
	_ = discovery.EventHealthChanged
	_ = discovery.EventDiscoveryComplete

	_ = discovery.NewMemoryStore
)

// discovery.Engine method set.
var (
	_ func(context.Context, time.Duration)                                = (*discovery.Engine)(nil).Start
	_ func(context.Context) error                                         = (*discovery.Engine)(nil).DiscoverNow
	_ func(context.Context) error                                         = (*discovery.Engine)(nil).CheckHealth
	_ func(context.Context, discovery.RegisterRequest) error              = (*discovery.Engine)(nil).Register
	_ func(context.Context, string) error                                 = (*discovery.Engine)(nil).Unregister
	_ func(context.Context, string, discovery.UpdateTagsRequest) error    = (*discovery.Engine)(nil).UpdateTags
	_ func(context.Context) ([]*discovery.DiscoveredService, error)       = (*discovery.Engine)(nil).List
	_ func(context.Context, string) (*discovery.DiscoveredService, error) = (*discovery.Engine)(nil).Get
	_ func(func(discovery.Event))                                         = (*discovery.Engine)(nil).OnEvent
	_ func(internaldiscovery.DiscoveryProvider)                           = (*discovery.Engine)(nil).AddProvider
)

// TestForwardingSurface is a compile-time check; nothing to run.
func TestForwardingSurface(t *testing.T) {
	t.Log("forwarding surface verified at compile time")
}
