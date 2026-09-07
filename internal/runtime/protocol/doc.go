// Package protocol groups the ARES external-protocol adapters
// (convergence Phase 3). Each subpackage keeps its own package identity;
// this directory only maps the territory:
//
//	mcp    – MCP client/server, tool bridging (package ares_mcp)
//	skills – skill catalog, loaders, experience-weighted selection (package ares_skills)
//	ahp    – agent wire protocol: messages, queues, heartbeats (package ahp)
//
// Adapters translate the outside world into fabric-native calls. They never
// schedule, never store: dispatch stays in the kernel, state stays in the
// owning service.
package protocol
