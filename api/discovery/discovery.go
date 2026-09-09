// Package discovery is the DEPRECATED public alias of internal/discoveryapi
// (M5). New code MUST import internal/discoveryapi; this package exists
// only for external consumers and is scheduled for removal.
//
// Two modes:
//   - Active discovery: engine scans providers (config files, binary probe, HTTP).
//   - Passive registration: external services register themselves via Register().
//
// Storage is pluggable via ServiceStore interface.
package discovery

import (
	internaldiscovery "github.com/Timwood0x10/ares/internal/discovery"
	"github.com/Timwood0x10/ares/internal/discoveryapi"
)

// Re-export types.
type (
	ServiceType       = internaldiscovery.ServiceType
	Confidence        = internaldiscovery.Confidence
	ServiceIdentity   = internaldiscovery.ServiceIdentity
	DiscoveryRecord   = internaldiscovery.DiscoveryRecord
	DiscoveredService = internaldiscovery.DiscoveredService
	HealthStatus      = internaldiscovery.HealthStatus
	EventType         = internaldiscovery.EventType
	Event             = internaldiscovery.Event
	ServiceStore      = internaldiscovery.ServiceStore
)

// Re-export constants.
const (
	ServiceTypeMCP    = internaldiscovery.ServiceTypeMCP
	ServiceTypeHTTP   = internaldiscovery.ServiceTypeHTTP
	ServiceTypeGRPC   = internaldiscovery.ServiceTypeGRPC
	ServiceTypeCLI    = internaldiscovery.ServiceTypeCLI
	ServiceTypeDocker = internaldiscovery.ServiceTypeDocker

	ConfidenceLow    = internaldiscovery.ConfidenceLow
	ConfidenceMedium = internaldiscovery.ConfidenceMedium
	ConfidenceHigh   = internaldiscovery.ConfidenceHigh
	ConfidenceMax    = internaldiscovery.ConfidenceMax

	EventServiceAdded      = internaldiscovery.EventServiceAdded
	EventServiceRemoved    = internaldiscovery.EventServiceRemoved
	EventServiceUpdated    = internaldiscovery.EventServiceUpdated
	EventHealthChanged     = internaldiscovery.EventHealthChanged
	EventDiscoveryComplete = internaldiscovery.EventDiscoveryComplete
)

// Re-export constructors for built-in stores.
var NewMemoryStore = internaldiscovery.NewMemoryStore

// EngineConfig configures the discovery engine.
type EngineConfig = discoveryapi.EngineConfig

// Engine is the public handle for the discovery engine.
type Engine = discoveryapi.Engine

// NewEngine creates a discovery engine.
func NewEngine(cfg EngineConfig) *Engine { return discoveryapi.NewEngine(cfg) }

// RegisterRequest is the input for passive service registration.
type RegisterRequest = internaldiscovery.RegisterRequest

// UpdateTagsRequest modifies tags on a service.
type UpdateTagsRequest = internaldiscovery.UpdateTagsRequest
