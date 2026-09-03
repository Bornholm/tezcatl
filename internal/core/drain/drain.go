package drain

import (
	"strconv"
	"strings"
	"unicode"
)

// Drain is a Go port of the DRAIN algorithm as implemented by the drain3
// Python project: a fixed-depth prefix tree routing log messages to
// clusters of similar templates, with online template updates.
//
// It is not safe for concurrent use; TemplateMiner adds the locking.
type Drain struct {
	depth                    int // number of prefix tree levels between the root and the leaves
	simTh                    float64
	maxChildren              int
	maxClusters              int
	paramStr                 string
	extraDelimiters          []string
	parametrizeNumericTokens bool
	maxTokens                int

	root            *node
	clusters        *clusterCache
	clustersCounter int64
}

type node struct {
	keyToChild map[string]*node
	clusterIDs []int64
}

func newNode() *node {
	return &node{
		keyToChild: map[string]*node{},
	}
}

type ChangeType string

const (
	ChangeTypeNone                   ChangeType = "none"
	ChangeTypeClusterCreated         ChangeType = "cluster_created"
	ChangeTypeClusterTemplateChanged ChangeType = "cluster_template_changed"
)

type SearchStrategy string

const (
	SearchStrategyNever    SearchStrategy = "never"
	SearchStrategyFallback SearchStrategy = "fallback"
	SearchStrategyAlways   SearchStrategy = "always"
)

func NewDrain(config *Config) *Drain {
	return &Drain{
		depth:                    config.Depth - 2,
		simTh:                    config.SimTh,
		maxChildren:              config.MaxChildren,
		maxClusters:              config.MaxClusters,
		paramStr:                 config.ParamStr,
		extraDelimiters:          config.ExtraDelimiters,
		parametrizeNumericTokens: config.ParametrizeNumericTokens != nil && *config.ParametrizeNumericTokens,
		maxTokens:                config.MaxTokens,
		root:                     newNode(),
		clusters:                 newClusterCache(config.MaxClusters),
	}
}

func (d *Drain) Clusters() []*Cluster {
	return d.clusters.Values()
}

func (d *Drain) tokenize(content string) []string {
	content = strings.TrimSpace(content)
	for _, delimiter := range d.extraDelimiters {
		content = strings.ReplaceAll(content, delimiter, " ")
	}

	tokens := strings.Fields(content)

	// The prefix tree keys its first level on the token count, so two
	// lines of the same shape that differ by one token can never land
	// in the same cluster. That is fine for a message and wrong for a
	// payload: a serialized structure printed into a log line varies in
	// length with its own content, and every variant becomes a template
	// of its own. Cutting the tail folds them back together, and the
	// marker says the shape was cut rather than ends there.
	if d.maxTokens > 0 && len(tokens) > d.maxTokens {
		tokens = append(tokens[:d.maxTokens-1:d.maxTokens-1], TruncationMarker)
	}

	return tokens
}

// AddLogMessage learns from a (masked) log message, either updating the
// matching cluster or creating a new one.
func (d *Drain) AddLogMessage(content string) (*Cluster, ChangeType) {
	return d.addTokens(d.tokenize(content))
}

// addTokens is AddLogMessage on an already tokenized message, letting
// callers reuse the tokens (e.g. for parameter extraction).
func (d *Drain) addTokens(tokens []string) (*Cluster, ChangeType) {
	matched := d.treeSearch(tokens, d.simTh, false)

	if matched == nil {
		d.clustersCounter++

		cluster := &Cluster{
			ID:             d.clustersCounter,
			TemplateTokens: append([]string(nil), tokens...),
			Size:           1,
		}

		d.clusters.Put(cluster.ID, cluster)
		d.addSeqToPrefixTree(cluster)

		return cluster, ChangeTypeClusterCreated
	}

	changeType := ChangeTypeNone

	newTemplate := d.createTemplate(tokens, matched.TemplateTokens)
	if !equalTokens(newTemplate, matched.TemplateTokens) {
		matched.TemplateTokens = newTemplate
		changeType = ChangeTypeClusterTemplateChanged
	}

	matched.Size++
	d.clusters.Touch(matched.ID)

	return matched, changeType
}

// Match looks up the cluster matching a (masked) log message without
// learning anything. Matching requires full identity modulo parameters.
func (d *Drain) Match(content string, strategy SearchStrategy) *Cluster {
	tokens := d.tokenize(content)

	fullSearch := func() *Cluster {
		return d.fastMatch(d.clusters.IDs(), tokens, 1.0, true)
	}

	if strategy == SearchStrategyAlways {
		return fullSearch()
	}

	if matched := d.treeSearch(tokens, 1.0, true); matched != nil {
		return matched
	}

	if strategy == SearchStrategyNever {
		return nil
	}

	return fullSearch()
}

func (d *Drain) treeSearch(tokens []string, simTh float64, includeParams bool) *Cluster {
	current, exists := d.root.keyToChild[lengthKey(len(tokens))]
	if !exists {
		return nil
	}

	if len(tokens) == 0 {
		return d.fastMatch(current.clusterIDs, tokens, simTh, includeParams)
	}

	depth := 1
	for _, token := range tokens {
		if depth >= d.depth || depth >= len(tokens) {
			break
		}

		next, exists := current.keyToChild[token]
		if !exists {
			next, exists = current.keyToChild[d.paramStr]
		}

		if !exists {
			return nil
		}

		current = next
		depth++
	}

	return d.fastMatch(current.clusterIDs, tokens, simTh, includeParams)
}

func (d *Drain) fastMatch(clusterIDs []int64, tokens []string, simTh float64, includeParams bool) *Cluster {
	var (
		matched       *Cluster
		maxSim        = -1.0
		maxParamCount = -1
	)

	for _, clusterID := range clusterIDs {
		// Skip clusters evicted from the LRU cache.
		cluster := d.clusters.Get(clusterID)
		if cluster == nil {
			continue
		}

		sim, paramCount := d.seqDistance(cluster.TemplateTokens, tokens, includeParams)
		if sim > maxSim || (sim == maxSim && paramCount > maxParamCount) {
			maxSim = sim
			maxParamCount = paramCount
			matched = cluster
		}
	}

	if matched == nil || maxSim < simTh {
		return nil
	}

	return matched
}

func (d *Drain) seqDistance(template []string, tokens []string, includeParams bool) (float64, int) {
	if len(template) != len(tokens) {
		return 0, 0
	}

	if len(template) == 0 {
		return 1, 0
	}

	simTokens := 0
	paramCount := 0

	for i, templateToken := range template {
		if templateToken == d.paramStr {
			paramCount++
			continue
		}

		if templateToken == tokens[i] {
			simTokens++
		}
	}

	if includeParams {
		simTokens += paramCount
	}

	return float64(simTokens) / float64(len(template)), paramCount
}

func (d *Drain) createTemplate(tokens []string, template []string) []string {
	newTemplate := append([]string(nil), template...)

	for i, token := range tokens {
		if token != template[i] {
			newTemplate[i] = d.paramStr
		}
	}

	return newTemplate
}

func (d *Drain) addSeqToPrefixTree(cluster *Cluster) {
	tokenCount := len(cluster.TemplateTokens)

	first, exists := d.root.keyToChild[lengthKey(tokenCount)]
	if !exists {
		first = newNode()
		d.root.keyToChild[lengthKey(tokenCount)] = first
	}

	current := first

	if tokenCount == 0 {
		current.clusterIDs = []int64{cluster.ID}
		return
	}

	depth := 1
	for _, token := range cluster.TemplateTokens {
		if depth >= d.depth || depth >= tokenCount {
			// Reached a leaf: register the cluster, dropping ids of
			// evicted clusters on the way.
			clusterIDs := make([]int64, 0, len(current.clusterIDs)+1)
			for _, clusterID := range current.clusterIDs {
				if d.clusters.Get(clusterID) != nil {
					clusterIDs = append(clusterIDs, clusterID)
				}
			}

			current.clusterIDs = append(clusterIDs, cluster.ID)

			break
		}

		if next, exists := current.keyToChild[token]; exists {
			current = next
			depth++

			continue
		}

		if d.parametrizeNumericTokens && hasNumbers(token) {
			next, exists := current.keyToChild[d.paramStr]
			if !exists {
				next = newNode()
				current.keyToChild[d.paramStr] = next
			}

			current = next
			depth++

			continue
		}

		if _, hasWildcard := current.keyToChild[d.paramStr]; hasWildcard {
			if len(current.keyToChild) < d.maxChildren {
				next := newNode()
				current.keyToChild[token] = next
				current = next
			} else {
				current = current.keyToChild[d.paramStr]
			}
		} else {
			switch {
			case len(current.keyToChild)+1 < d.maxChildren:
				next := newNode()
				current.keyToChild[token] = next
				current = next
			case len(current.keyToChild)+1 == d.maxChildren:
				next := newNode()
				current.keyToChild[d.paramStr] = next
				current = next
			default:
				current = current.keyToChild[d.paramStr]
			}
		}

		depth++
	}
}

func lengthKey(length int) string {
	return strconv.Itoa(length)
}

func hasNumbers(token string) bool {
	for _, r := range token {
		if unicode.IsDigit(r) {
			return true
		}
	}

	return false
}

func equalTokens(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
