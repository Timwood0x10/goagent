// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"strings"
)

// IsProblem detects if a user message represents a genuine problem or question.
// It checks for problem-related keywords and question marks to avoid extracting
// non-problematic conversations like "thanks", "ok", "got it".
//
// Args:
//
//	text - the user message to analyze.
//
// Returns:
//
//	true if the text appears to be a problem or question, false otherwise.
func IsProblem(text string) bool {
	if text == "" {
		return false
	}

	lower := strings.TrimSpace(strings.ToLower(text))

	// Negative keywords - these should NOT be treated as problems.
	// English acknowledgments use whitespace boundaries; Chinese has no
	// word separators, so unambiguous Chinese acknowledgment phrases are
	// matched as substrings. Polite-prefix tokens (e.g. Chinese "please") are deliberately excluded: they
	// introduce genuine questions rather than acknowledgments.
	englishNegativeKeywords := []string{
		// English acknowledgments
		"thanks", "thank you", "ok", "okay", "got it", "understood",
		"alright", "sure", "fine", "good", "great", "perfect",
		"awesome", "excellent", "yes", "no", "maybe", "right",
		"correct", "agree", "cool", "nice", "sounds good",
		"that works", "makes sense", "got it, thanks", "thanks for the",
		"you're welcome", "glad i could", "appreciate", "welcome",
		"show me", "tell me", "what's happening", "what is this",
		"hi", "hello",
	}
	// Multi-character Chinese acknowledgment phrases that never appear
	// inside genuine questions; matched as substrings.
	chineseNegativeKeywords := []string{
		"谢谢", "感谢", "没问题", "明白了", "知道了", "太好了",
		"听起来不错", "有道理", "收到了", "不用谢", "再见", "拜拜",
	}
	// Short Chinese acknowledgment words that double as common content
	// words or greeting openers inside real questions (e.g. "Hello, please...",
	// "Welcome to..."); matched only as a whole message.
	chineseExactNegativeKeywords := []string{
		"好的", "行", "可以", "不错", "很棒", "完美", "优秀",
		"是的", "对", "正确", "同意", "酷", "很好", "你好", "欢迎",
	}

	// Check English negative keywords with whitespace boundaries.
	for _, keyword := range englishNegativeKeywords {
		if lower == keyword || strings.HasPrefix(lower, keyword+" ") || strings.HasSuffix(lower, " "+keyword) {
			return false
		}
	}

	// Check Chinese acknowledgment phrases by substring match.
	for _, keyword := range chineseNegativeKeywords {
		if strings.Contains(lower, keyword) {
			return false
		}
	}

	// Check short Chinese acknowledgment words by exact match only.
	for _, keyword := range chineseExactNegativeKeywords {
		if lower == keyword {
			return false
		}
	}

	// Problem-related keywords (must be more specific than casual terms) - English and Chinese
	problemKeywords := []string{
		// English problem keywords
		"error", "issue", "problem", "fix", "help", "unable",
		"cannot", "can't", "fail", "failed", "broken", "wrong",
		"not working", "doesn't work", "won't work", "won't start",
		"won't", "bug", "crash", "exception", "panic",
		"stack trace", "leak", "timeout", "refused", "denied",
		"missing", "undefined", "null", "invalid",
		// Chinese problem keywords
		"错误", "问题", "故障", "怎么", "如何", "怎么办",
		"无法", "不能", "失败", "崩溃", "异常", "恐慌",
		"超时", "拒绝", "缺失", "未定义", "无效", "出错",
		"修复", "解决", "调试", "排查", "帮忙", "求助",
		"为什么", "为何", "怎样", "怎么弄", "怎么做",
		"什么", "哪里", "哪个", "如何", "是否", "有没有",
		"为什么", "为啥", "为啥子",
		// Additional Chinese question patterns
		"介绍", "是什么", "什么是", "都有啥", "有啥", "是啥",
		"什么是", "什么叫", "啥是", "啥叫做", "介绍一下",
		"介绍下", "介绍一", "来介绍", "给我介绍",
		"怎么弄", "怎么搞", "怎么做", "怎么实现",
		"怎么配置", "怎么用", "如何使用", "如何用",
		"告诉我", "说说", "讲一讲", "讲一下", "说一下",
		"有哪", "有哪些", "哪些", "分别", "各自",
		"有什么", "有什么特点", "有什么特性", "有什么功能",
		"特点", "功能", "用途", "作用", "区别", "差异",
		"对比", "比较", "选哪个", "选什么", "推荐",
	}

	for _, keyword := range problemKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}

	// Check for question mark (but filter out rhetorical questions) - supports both English and Chinese
	if strings.Contains(text, "?") || strings.Contains(text, "？") {
		// Filter out questions that are just acknowledgments
		questionExclusions := []string{
			// English exclusions
			"can you?", "could you?", "would you?", "right?",
			"ok?", "sure?", "yes?", "no?",
			// Chinese exclusions
			"好吗？", "可以吗？", "是吗？", "对吗？", "没错？",
			"明白吗？", "知道吗？", "好的？", "行吗？",
		}
		for _, exclusion := range questionExclusions {
			if strings.HasSuffix(lower, exclusion) {
				return false
			}
		}
		return true
	}

	return false
}

// QuestionDetector detects questions in conversations.
type QuestionDetector struct {
	// Configurable sensitivity (0.0 to 1.0)
	sensitivity float64
}

// NewQuestionDetector creates a new QuestionDetector with default sensitivity.
func NewQuestionDetector() *QuestionDetector {
	return &QuestionDetector{
		sensitivity: 0.7,
	}
}

// Detect checks if a message is a question.
//
// Args:
//
//	text - the message to analyze.
//
// Returns:
//
//	true if the message is likely a question.
func (d *QuestionDetector) Detect(text string) bool {
	return IsProblem(text)
}
