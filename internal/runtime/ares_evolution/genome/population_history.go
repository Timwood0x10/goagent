// Package genome provides population management for genetic algorithm evolution.
package genome

func copyRecoveryActions(src map[string]int) map[string]int {
	if src == nil {
		return make(map[string]int)
	}
	dst := make(map[string]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (p *Population) appendHistoryLocked() {
	diversity := p.measureDiversityReportLocked()
	entry := GenerationHistoryEntry{
		Generation:           p.Generation,
		PopulationSize:       len(p.Agents),
		Diversity:            diversity.Overall,
		NumericDiversity:     diversity.Numeric,
		CategoricalDiversity: diversity.Categorical,
		LineageDiversity:     diversity.Lineage,
		DominantLineageShare: diversity.DominantLineageShare,
	}
	if len(p.Agents) > 0 {
		entry.BestScore, entry.AvgScore, entry.WorstScore = p.computeStatsLocked()
	}
	entry.MutationTypes = make(map[string]int)
	parentSet := make(map[string]struct{})
	for _, agent := range p.Agents {
		mt := agent.StrategyMutationType.String()
		if mt == "" {
			mt = "unknown"
		}
		entry.MutationTypes[mt]++
		if agent.ParentID != "" {
			parentSet[agent.ParentID] = struct{}{}
		}
	}
	entry.NumDiverse = len(parentSet)
	entry.RecoveryActions = make(map[string]int, len(p.recoveryActions))
	for k, v := range p.recoveryActions {
		entry.RecoveryActions[k] = v
	}
	p.recoveryActions = make(map[string]int)
	p.history = append(p.history, entry)
	if p.HistoryMaxSize > 0 && len(p.history) > p.HistoryMaxSize {
		p.history = p.history[len(p.history)-p.HistoryMaxSize:]
	}
}

func (p *Population) History() []GenerationHistoryEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.history) == 0 {
		return nil
	}
	cp := make([]GenerationHistoryEntry, len(p.history))
	copy(cp, p.history)
	return cp
}

func (p *Population) HistoryCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.history)
}

func (p *Population) CurrentGeneration() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Generation
}

// HistoryData is a flat-format structure for exporting evolution history
// to external visualization tools (e.g., plotting libraries).
type HistoryData struct {
	Generations     []int     `json:"generations"`
	BestScores      []float64 `json:"best_scores"`
	AvgScores       []float64 `json:"avg_scores"`
	WorstScores     []float64 `json:"worst_scores"`
	Diversities     []float64 `json:"diversities"`
	PopulationSizes []int     `json:"population_sizes"`
}

// ExportHistory returns the evolution history as a flat-format HistoryData
// suitable for JSON export and external plotting. Returns nil when no history
// has been recorded.
func (p *Population) ExportHistory() *HistoryData {
	history := p.History()
	if len(history) == 0 {
		return nil
	}

	data := &HistoryData{
		Generations:     make([]int, 0, len(history)),
		BestScores:      make([]float64, 0, len(history)),
		AvgScores:       make([]float64, 0, len(history)),
		WorstScores:     make([]float64, 0, len(history)),
		Diversities:     make([]float64, 0, len(history)),
		PopulationSizes: make([]int, 0, len(history)),
	}

	for _, h := range history {
		data.Generations = append(data.Generations, h.Generation)
		data.BestScores = append(data.BestScores, h.BestScore)
		data.AvgScores = append(data.AvgScores, h.AvgScore)
		data.WorstScores = append(data.WorstScores, h.WorstScore)
		data.Diversities = append(data.Diversities, h.Diversity)
		data.PopulationSizes = append(data.PopulationSizes, h.PopulationSize)
	}

	return data
}

func (p *Population) StagnantGenerations() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stagnantGens
}

func (p *Population) CurrentMutationRate() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.currentMutationRate
}

func (p *Population) RecordRecoveryAction(action string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.recoveryActions == nil {
		p.recoveryActions = make(map[string]int)
	}
	p.recoveryActions[action]++
}

func (p *Population) RecoveryActions() map[string]int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.recoveryActions) == 0 {
		return nil
	}
	result := make(map[string]int, len(p.recoveryActions))
	for k, v := range p.recoveryActions {
		result[k] = v
	}
	return result
}
