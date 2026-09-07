// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"strings"
)

// MemoryClassifier classifies experiences into memory types.
type MemoryClassifier struct{}

// NewMemoryClassifier creates a new MemoryClassifier instance.
func NewMemoryClassifier() *MemoryClassifier {
	return &MemoryClassifier{}
}

// ClassifyMemory determines the memory type based on the experience content.
// It uses keyword-based classification to categorize memories into one of four types.
//
// Args:
//
//	experience - the experience to classify.
//
// Returns:
//
//	MemoryType - the classified memory type.
func (c *MemoryClassifier) ClassifyMemory(experience *Experience) MemoryType {
	if experience == nil {
		return MemoryKnowledge // Default fallback
	}

	content := strings.ToLower(experience.Problem + " " + experience.Solution)

	// Check for user profile first (highest priority for self-introductions)
	if c.isUserProfile(experience.Problem, experience.Solution) {
		return MemoryProfile
	}

	// Check for solution type
	if c.isSolution(content) {
		return MemoryInteraction
	}

	// Check for preference type
	if c.isPreference(content) {
		return MemoryPreference
	}

	// Check for rule type
	if c.isRule(content) {
		return MemoryKnowledge
	}

	// Default to fact type
	return MemoryKnowledge
}

// isSolution determines if the content represents a solution.
func (c *MemoryClassifier) isSolution(content string) bool {
	solutionKeywords := []string{
		"fix", "solution", "error", "issue", "problem", "resolve",
		"debug", "troubleshoot", "workaround", "patch",
		"error:", "exception", "fail", "failure", "bug",
		"restart", "update", "change", "modify", "adjust",
		"correct", "repair", "heal", "recover",
	}

	for _, keyword := range solutionKeywords {
		if strings.Contains(content, keyword) {
			return true
		}
	}

	return false
}

// isPreference determines if the content represents a user preference.
func (c *MemoryClassifier) isPreference(content string) bool {
	preferenceKeywords := []string{
		"prefer", "like", "usually", "want", "would like",
		"favorite", "choose", "opt for", "rather", "instead",
		"enjoy", "love", "dislike", "hate", "avoid",
	}

	for _, keyword := range preferenceKeywords {
		if strings.Contains(content, keyword) {
			return true
		}
	}

	return false
}

// isRule determines if the content represents a system rule or constraint.
func (c *MemoryClassifier) isRule(content string) bool {
	ruleKeywords := []string{
		"configuration", "config", "setting", "policy", "rule",
		"constraint", "requirement", "must", "should", "system",
		"framework", "architecture", "pattern", "convention",
		"standard", "style guide", "guideline", "specification",
		"protocol", "format", "structure", "schema",
		"best practice", "convention", "coding standard",
	}

	for _, keyword := range ruleKeywords {
		if strings.Contains(content, keyword) {
			return true
		}
	}

	return false
}

// GetMemoryTypeFromString converts a string to MemoryType.
// Returns MemoryKnowledge as default for invalid input.
func GetMemoryTypeFromString(s string) MemoryType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fact":
		return MemoryKnowledge
	case "preference":
		return MemoryPreference
	case "solution":
		return MemoryInteraction
	case "rule":
		return MemoryKnowledge
	case "profile":
		return MemoryProfile
	default:
		return MemoryKnowledge
	}
}

// isUserProfile determines if the content represents user profile information.
// It checks for profile-related keywords and patterns in both problem and solution.
//
// Args:
//
//	problem - the problem or context description.
//	solution - the solution or extracted information.
//
// Returns:
//
//	true if the content represents user profile information.
func (c *MemoryClassifier) isUserProfile(problem, solution string) bool {
	// Check if problem indicates this is profile information
	profileProblemPatterns := []string{
		"user profile", "user information", "user details",
		"用户画像", "用户信息", "用户详情",
	}

	lowerProblem := strings.ToLower(problem)
	for _, pattern := range profileProblemPatterns {
		if strings.Contains(lowerProblem, pattern) {
			return true
		}
	}

	// Check solution for profile indicators
	lowerSolution := strings.ToLower(solution)

	// Profile indicators (English and Chinese)
	profileIndicators := []string{
		// English indicators
		"name:", "profession:", "skills:", "background:",
		"developer", "engineer", "programmer", "student",
		// Chinese indicators
		"姓名:", "职业:", "技能:", "背景:",
		"developer", "engineer", "programmer", "student",
	}

	for _, indicator := range profileIndicators {
		if strings.Contains(lowerSolution, indicator) {
			return true
		}
	}

	// Check for self-introduction patterns in solution
	selfIntroPatterns := []string{
		"i'm ", "i am ", "my name is ",
		"我叫", "我是",
	}

	for _, pattern := range selfIntroPatterns {
		if strings.Contains(lowerSolution, pattern) {
			return true
		}
	}

	return false
}
