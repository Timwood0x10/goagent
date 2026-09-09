// Package service adapts the internal KnowledgeRuntime to the canonical
// knowledgeapi.KnowledgeService interface.
//
// Architecture:
//
//	internal/knowledgeapi   (canonical DTOs + KnowledgeService interface)
//	     ↑
//	internal/knowledge/service  (this package: adapter)
//	     ↓
//	internal/knowledge/runtime  (real implementation)
//
// The adapter lives in a sub-package to avoid an import cycle:
// knowledgeapi imports internal/knowledge for DTO aliases,
// so the adapter cannot live in internal/knowledge itself (it would
// need to import knowledgeapi).
//
// Beta: this package is part of the AKG (Autonomous Knowledge Graph)
// subsystem and is currently BETA. The API is not yet stable and may
// change between minor releases. Do not depend on it in production
// without pinning a version. Feedback welcome.
package service
