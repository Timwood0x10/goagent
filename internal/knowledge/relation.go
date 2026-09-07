package knowledge

// Relation connects two KnowledgeObjects with a named relationship.
//
// It serves two roles:
//   - As a graph edge in WorkingGraph (From/To/Name/Properties/Score), and
//   - As a fact-level outgoing relation on KnowledgeObject.Relations
//     (Predicate/ObjectID/ObjectText), where the subject is the owning object.
//
// The 0.2.9 fact-relation fields (Predicate/ObjectID/ObjectText) are populated
// by rule-based extraction (see RelationExtractor). Name is a string (not a
// const enum) so users can register custom relation types like "worked_with",
// "managed_by", "friend_of".
type Relation struct {
	From       string         `json:"from"` // KnowledgeObject ID (graph edge source)
	To         string         `json:"to"`   // KnowledgeObject ID (graph edge target)
	Name       string         `json:"name"` // Relationship name (graph edge)
	Properties map[string]any `json:"properties,omitempty"`
	Score      float64        `json:"score"` // Strength [0, 1]
	Evidence   string         `json:"evidence,omitempty"`
	// Predicate is the relation predicate; must be in AllowedPredicates when
	// populated by RelationExtractor. The subject is the owning object.
	Predicate string `json:"predicate,omitempty"`
	// ObjectID is the target KnowledgeObject ID for a fact-level relation.
	ObjectID string `json:"object_id,omitempty"`
	// ObjectText is the target text when it has not been resolved to an object.
	ObjectText string `json:"object_text,omitempty"`
}

// Built-in relation names.
const (
	RelDependsOn   = "depends_on"
	RelCalls       = "calls"
	RelCauses      = "causes"
	RelFixes       = "fixes"
	RelBelongsTo   = "belongs_to"
	RelUses        = "uses"
	RelImplements  = "implements"
	RelSimilarTo   = "similar_to"
	RelGeneratedBy = "generated_by"
	RelDecidedBy   = "decided_by"
	RelSupersedes  = "supersedes"
	RelLearnsFrom  = "learns_from"
)

// WorkingGraph is a task-specific cognitive graph.
// Lifecycle: Build → Consume → Destroy. Never persisted.
type WorkingGraph struct {
	Nodes map[string]*KnowledgeObject `json:"nodes"`
	Edges []Relation                  `json:"edges"`
}

// RelationKey identifies a graph edge by its endpoints and relationship name.
// Used for duplicate detection when aggregating edges from multiple linkers.
type RelationKey struct {
	From string
	To   string
	Name string
}
