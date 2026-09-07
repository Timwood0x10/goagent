// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Message represents a single message in a conversation.
type Message struct {
	Role         string
	Content      string
	ToolCallID   string                   `json:"tool_call_id,omitempty"`
	ToolCalls    []map[string]interface{} `json:"tool_calls,omitempty"`
	TurnID       string                   `json:"turn_id,omitempty"`
	EventKind    string                   `json:"event_kind,omitempty"`
	ParentID     string                   `json:"parent_id,omitempty"`
	ArtifactRefs []string                 `json:"artifact_refs,omitempty"`
}

// ExperienceExtractor extracts problem-solution pairs from conversations.
type ExperienceExtractor struct {
	questionDetector *QuestionDetector
	noiseFilter      *NoiseFilter
	enableCrossTurn  bool
}

// NewExperienceExtractor creates a new ExperienceExtractor instance.
func NewExperienceExtractor() *ExperienceExtractor {
	return &ExperienceExtractor{
		questionDetector: NewQuestionDetector(),
		noiseFilter:      NewNoiseFilter(),
		enableCrossTurn:  true,
	}
}

// NewExperienceExtractorWithConfig creates a new ExperienceExtractor instance with custom configuration.
func NewExperienceExtractorWithConfig(enableCrossTurn bool) *ExperienceExtractor {
	return &ExperienceExtractor{
		questionDetector: NewQuestionDetector(),
		noiseFilter:      NewNoiseFilter(),
		enableCrossTurn:  enableCrossTurn,
	}
}

// ExtractExperiences extracts problem-solution pairs from a conversation.
// It supports cross-turn extraction where the solution may appear after 2 messages.
// Also extracts user profiles/preferences from self-introductions.
//
// Args:
//
//	messages - the conversation messages.
//
// Returns:
//
//	[]Experience - extracted experiences.
func (e *ExperienceExtractor) ExtractExperiences(messages []Message) []Experience {
	var experiences []Experience

	// Step 1: Extract user profile from self-introduction (only once per conversation)
	if userProfile := e.extractUserProfile(messages); userProfile != nil {
		experiences = append(experiences, *userProfile)
	}

	// Step 2: Extract problem-solution pairs
	for i := 0; i < len(messages)-1; i++ {
		current := messages[i]
		next := messages[i+1]

		// Only process user messages that are problems
		if current.Role != "user" {
			continue
		}

		// Check if this is a problem/question
		if !e.questionDetector.Detect(current.Content) {
			continue
		}

		// Filter out noise
		if e.noiseFilter.IsNoise(current.Content) {
			continue
		}

		// Check if this is a cross-turn scenario (assistant asks for clarification)
		isCrossTurn := false
		if e.enableCrossTurn && i+3 < len(messages) {
			m2 := messages[i+2]
			m3 := messages[i+3]

			// Check if next message is a question/clarification and m3 is the actual solution
			if next.Role == "assistant" && m2.Role == "user" && m3.Role == "assistant" {
				// If the assistant's response is a question/clarification, skip direct extraction
				lower := strings.ToLower(next.Content)
				clarificationIndicators := []string{
					"?", "can you", "could you", "what", "how", "why",
					"share", "provide", "show", "tell me", "clarify",
				}
				for _, indicator := range clarificationIndicators {
					if strings.Contains(lower, indicator) {
						isCrossTurn = true
						break
					}
				}

				if isCrossTurn {
					// Extract cross-turn experience
					exp := e.extractCrossTurnExperience(current, next, m2, m3)
					if exp != nil {
						experiences = append(experiences, *exp)
					}
					// Skip the direct extraction for this iteration
					continue
				}
			}
		}

		// Direct extraction: user → assistant (when not cross-turn)
		if next.Role == "assistant" {
			exp := e.extractDirectExperience(current, next)
			if exp != nil {
				experiences = append(experiences, *exp)
			}
		}
	}

	return experiences
}

// extractDirectExperience extracts an experience from direct user-assistant pair.
//
// Args:
//
//	user - the user message.
//	assistant - the assistant message.
//
// Returns:
//
//	*Experience - extracted experience, nil if extraction fails.
func (e *ExperienceExtractor) extractDirectExperience(user, assistant Message) *Experience {
	if user.Content == "" || assistant.Content == "" {
		return nil
	}

	// Filter out noise from assistant response
	if e.noiseFilter.IsNoise(assistant.Content) {
		return nil
	}

	// Extract problem and solution
	problem := strings.TrimSpace(user.Content)
	solution := strings.TrimSpace(assistant.Content)

	// Validate extraction
	if problem == "" || solution == "" {
		return nil
	}

	// Extract the core solution (avoid verbose responses)
	solution = e.extractCoreSolution(solution)

	return &Experience{
		Problem:          problem,
		Solution:         solution,
		Confidence:       e.calculateConfidence(problem, solution),
		ExtractionMethod: ExtractionDirect,
	}
}

// extractCrossTurnExperience extracts an experience from multi-turn conversation.
// This handles cases where the solution appears after additional clarification.
//
// Args:
//
//	user - the original user message.
//	a1 - first assistant response (clarification).
//	m2 - second user message (clarification details).
//	a2 - second assistant response (final solution).
//
// Returns:
//
//	*Experience - extracted experience, nil if extraction fails.
func (e *ExperienceExtractor) extractCrossTurnExperience(user, a1, m2, a2 Message) *Experience {
	if user.Content == "" || a2.Content == "" {
		return nil
	}

	// Filter out noise
	if e.noiseFilter.IsNoise(a2.Content) {
		return nil
	}

	// Combine problem context with proper sentence boundary
	problem := user.Content
	if m2.Role == "user" && !e.noiseFilter.IsNoise(m2.Content) {
		// Add sentence separator to prevent words joining at boundary
		if !strings.HasSuffix(problem, ". ") && !strings.HasSuffix(problem, "? ") && !strings.HasSuffix(problem, "! ") {
			problem += ". "
		}
		problem += m2.Content
	}

	// Extract solution
	solution := strings.TrimSpace(a2.Content)

	// Validate extraction
	if problem == "" || solution == "" {
		return nil
	}

	// Extract the core solution
	solution = e.extractCoreSolution(solution)

	return &Experience{
		Problem:          strings.TrimSpace(problem),
		Solution:         solution,
		Confidence:       e.calculateConfidence(problem, solution),
		ExtractionMethod: ExtractionCrossTurn,
	}
}

// extractCoreSolution extracts the core solution from a potentially verbose response.
// It removes common conversational fillers and focuses on actionable content.
//
// Args:
//
//	solution - the full solution text.
//
// Returns:
//
//	string - the core solution.
func (e *ExperienceExtractor) extractCoreSolution(solution string) string {
	// Remove common prefixes
	prefixes := []string{
		"here's how to fix it:", "to fix this:", "the solution is:",
		"you can fix it by:", "try this:", "here's the solution:",
	}

	lower := strings.ToLower(solution)
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			solution = strings.TrimSpace(solution[len(prefix):])
			break
		}
	}

	// Remove common suffixes
	suffixes := []string{
		"let me know if this helps", "hope this helps", "let me know if you need more help",
	}

	lower = strings.ToLower(solution)
	for _, suffix := range suffixes {
		if strings.HasSuffix(lower, suffix) {
			solution = strings.TrimSpace(solution[:len(solution)-len(suffix)])
			break
		}
	}

	// If solution is too long (measured in runes so multi-byte characters
	// such as CJK count as one character), truncate to the first few
	// sentences. Truncation operates on runes and never splits a rune.
	if len([]rune(solution)) > 500 {
		runes := []rune(solution)
		// Find first period, newline, or end of first block.
		// Leave room for "..." suffix (3 chars).
		truncateAt := 497
		for i, c := range runes {
			if i > 200 && (c == '.' || c == '\n') {
				truncateAt = i + 1
				break
			}
		}
		// Ensure we don't exceed 500 characters total including "...".
		if truncateAt > 497 {
			truncateAt = 497
		}
		if truncateAt >= len(runes) {
			return solution
		}
		solution = string(runes[:truncateAt]) + "..."
	}

	return solution
}

// calculateConfidence calculates the confidence score for an extracted experience.
// It considers factors like solution length, keyword presence, and structure.
//
// Args:
//
//	problem - the problem description.
//	solution - the solution description.
//
// Returns:
//
//	float64 - confidence score between 0 and 1.
func (e *ExperienceExtractor) calculateConfidence(problem, solution string) float64 {
	confidence := 0.5

	// Bonus for longer solutions (more detailed)
	if len(solution) > 100 {
		confidence += 0.1
	}
	if len(solution) > 200 {
		confidence += 0.1
	}

	// Bonus for specific action verbs
	actionVerbs := []string{"restart", "run", "execute", "install", "configure", "update", "delete", "create"}
	lower := strings.ToLower(solution)
	for _, verb := range actionVerbs {
		if strings.Contains(lower, verb) {
			confidence += 0.05
			break
		}
	}

	// Penalty for very short solutions
	if len(solution) < 20 {
		confidence -= 0.2
	}

	// Ensure confidence is within valid range
	if confidence > 1.0 {
		confidence = 1.0
	}
	if confidence < 0.0 {
		confidence = 0.0
	}

	return confidence
}

// extractUserProfile extracts user profile information from conversation.
// It identifies self-introductions and extracts key information like name,
// profession, skills, and preferences.
//
// Args:
//
//	messages - the conversation messages.
//
// Returns:
//
//	*Experience - extracted user profile, nil if not found.
func (e *ExperienceExtractor) extractUserProfile(messages []Message) *Experience {
	// Look for self-introduction in early messages (first 3 user messages)
	userMessageCount := 0
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}

		userMessageCount++
		// Only check first 3 user messages for self-introduction
		if userMessageCount > 3 {
			break
		}

		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}

		lower := strings.ToLower(content)

		// Self-introduction patterns (English and Chinese)
		selfIntroPatterns := []string{
			// English patterns
			"i'm ", "i am ", "my name is ", "call me ",
			// Chinese patterns
			"我叫", "我是", "我是的",
		}

		isSelfIntro := false
		for _, pattern := range selfIntroPatterns {
			if strings.Contains(lower, pattern) {
				isSelfIntro = true
				break
			}
		}

		if !isSelfIntro {
			continue
		}

		// Extract user profile information
		profile := e.parseUserProfile(content)
		if profile == "" {
			continue
		}

		// Create experience with profile as solution
		// Problem is "User profile" to indicate this is profile information
		return &Experience{
			Problem:          "User profile information",
			Solution:         profile,
			Confidence:       0.9, // High confidence for self-introduction
			ExtractionMethod: ExtractionDirect,
		}
	}

	return nil
}

// parseUserProfile parses user profile from self-introduction text.
// It extracts name, profession, skills, and preferences.
//
// Args:
//
//	text - the self-introduction text.
//
// Returns:
//
//	string - formatted user profile.
func (e *ExperienceExtractor) parseUserProfile(text string) string {
	profile := strings.TrimSpace(text)

	// Remove common greetings
	greetings := []string{
		"hello ", "hi ", "hey ", // English
		"你好", "您好", // Chinese
		"nice to meet you", "pleased to meet you", // English
		"很高兴认识你", "幸会", // Chinese
	}

	lower := strings.ToLower(profile)
	for _, greeting := range greetings {
		if strings.HasPrefix(lower, strings.ToLower(greeting)) {
			profile = strings.TrimSpace(profile[len(greeting):])
			lower = strings.ToLower(profile)
			break
		}
	}

	// Extract and format profile components
	var components []string

	// Extract name (simple pattern matching)
	namePatterns := []struct {
		pattern string
		label   string
	}{
		{"i'm ", "Name: "},
		{"i am ", "Name: "},
		{"my name is ", "Name: "},
		{"call me ", "Name: "},
		{"我叫", "姓名: "},
		{"我是", "姓名: "},
	}

	for _, np := range namePatterns {
		if idx := strings.Index(lower, np.pattern); idx != -1 {
			namePart := profile[idx+len(np.pattern):]
			// Extract name until comma, period, or space followed by profession
			for i, c := range namePart {
				if c == ',' || c == '，' || c == '。' {
					namePart = namePart[:i]
					break
				}
				if c == ' ' && i > 2 {
					// Check if next word is a profession
					rest := strings.ToLower(namePart[i:])
					professions := []string{"developer", "engineer", "programmer", "student", "teacher"}
					for _, prof := range professions {
						if strings.HasPrefix(rest, " "+prof) || strings.HasPrefix(rest, " a "+prof) {
							namePart = namePart[:i]
							break
						}
					}
					break
				}
			}
			if namePart := strings.TrimSpace(namePart); namePart != "" {
				components = append(components, np.label+namePart)
				break
			}
		}
	}

	// Extract profession/skills
	professionPatterns := []string{
		"developer", "engineer", "programmer", "architect", "designer",
		"manager", "analyst", "consultant", "specialist", "expert",
	}

	lower = strings.ToLower(profile)
	for _, prof := range professionPatterns {
		if strings.Contains(lower, prof) {
			components = append(components, "Profession: "+cases.Title(language.English).String(prof))
			break
		}
	}

	// Extract skills/tech stack
	skillsPatterns := []string{
		"like ", "love ", "prefer ", "use ", "work with ",
	}

	for _, sp := range skillsPatterns {
		if idx := strings.Index(lower, sp); idx != -1 {
			skillsPart := profile[idx+len(sp):]
			// Extract skills until end or punctuation
			for i, c := range skillsPart {
				if c == ',' || c == '.' || c == '。' {
					skillsPart = skillsPart[:i]
					break
				}
			}
			if skillsPart := strings.TrimSpace(skillsPart); len(skillsPart) > 3 {
				components = append(components, "Skills: "+skillsPart)
				break
			}
		}
	}

	// If we extracted components, return formatted profile
	if len(components) > 0 {
		return strings.Join(components, " | ")
	}

	// Fallback: return original text if it looks like a profile
	if len(profile) > 20 {
		return profile
	}

	return ""
}

// FormatExperience formats an experience as a compact string representation.
//
// Args:
//
//	exp - the experience to format.
//
// Returns:
//
//	string - formatted experience string.
func FormatExperience(exp *Experience) string {
	return fmt.Sprintf("%s → %s", exp.Problem, exp.Solution)
}
