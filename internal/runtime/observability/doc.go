// Package observability is the ARES telemetry service.
//
// It covers metrics, tracers (OTel/no-op), cost dashboards, and the flight
// recorder (flight/ — per-task execution genealogy for inspect/replay).
// Library code reports through injected loggers and event sinks; this
// package aggregates. It is read-only with respect to execution: observing
// a task must never change its outcome.
package observability
