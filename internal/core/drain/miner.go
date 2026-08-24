package drain

import (
	"strings"
	"sync"

	"github.com/pkg/errors"
)

type Config struct {
	Depth                    int                  `yaml:"depth" json:"depth"`
	SimTh                    float64              `yaml:"sim_th" json:"sim_th"`
	MaxChildren              int                  `yaml:"max_children" json:"max_children"`
	MaxClusters              int                  `yaml:"max_clusters" json:"max_clusters"`
	ParamStr                 string               `yaml:"param_str" json:"param_str"`
	ExtraDelimiters          []string             `yaml:"extra_delimiters" json:"extra_delimiters"`
	ParametrizeNumericTokens *bool                `yaml:"parametrize_numeric_tokens" json:"parametrize_numeric_tokens"`
	MaskPrefix               string               `yaml:"mask_prefix" json:"mask_prefix"`
	MaskSuffix               string               `yaml:"mask_suffix" json:"mask_suffix"`
	Masking                  []MaskingInstruction `yaml:"masking" json:"masking"`
}

func DefaultConfig() *Config {
	parametrize := true

	return &Config{
		Depth:                    4,
		SimTh:                    0.4,
		MaxChildren:              100,
		ParamStr:                 "<*>",
		ParametrizeNumericTokens: &parametrize,
		MaskPrefix:               "<",
		MaskSuffix:               ">",
	}
}

// normalized returns a copy with defaults applied to zero values.
func (c *Config) normalized() (*Config, error) {
	normalized := *c
	defaults := DefaultConfig()

	if normalized.Depth == 0 {
		normalized.Depth = defaults.Depth
	}

	if normalized.Depth < 3 {
		return nil, errors.Errorf("depth must be at least 3, got %d", normalized.Depth)
	}

	if normalized.SimTh == 0 {
		normalized.SimTh = defaults.SimTh
	}

	if normalized.MaxChildren == 0 {
		normalized.MaxChildren = defaults.MaxChildren
	}

	if normalized.ParamStr == "" {
		normalized.ParamStr = defaults.ParamStr
	}

	if normalized.ParametrizeNumericTokens == nil {
		normalized.ParametrizeNumericTokens = defaults.ParametrizeNumericTokens
	}

	if normalized.MaskPrefix == "" {
		normalized.MaskPrefix = defaults.MaskPrefix
	}

	if normalized.MaskSuffix == "" {
		normalized.MaskSuffix = defaults.MaskSuffix
	}

	return &normalized, nil
}

type Result struct {
	Cluster    *Cluster
	ChangeType ChangeType
}

// TemplateMiner combines masking and the DRAIN tree, exactly like
// drain3's TemplateMiner. It is safe for concurrent use, though within a
// pipeline each partition owns its own miner.
type TemplateMiner struct {
	mu     sync.Mutex
	config *Config
	masker *Masker
	drain  *Drain
}

func NewTemplateMiner(config *Config) (*TemplateMiner, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	masker, err := NewMasker(normalized.Masking, normalized.MaskPrefix, normalized.MaskSuffix)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &TemplateMiner{
		config: normalized,
		masker: masker,
		drain:  NewDrain(normalized),
	}, nil
}

// AddLogMessage learns from a raw log message: masking is applied, then
// the DRAIN tree is updated.
func (m *TemplateMiner) AddLogMessage(message string) Result {
	m.mu.Lock()
	defer m.mu.Unlock()

	cluster, changeType := m.drain.AddLogMessage(m.masker.Mask(message))

	return Result{
		Cluster:    cluster,
		ChangeType: changeType,
	}
}

// Match finds the cluster matching a raw log message without learning.
func (m *TemplateMiner) Match(message string, strategy SearchStrategy) *Cluster {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.drain.Match(m.masker.Mask(message), strategy)
}

// Clusters returns the known clusters, from least to most recently used.
func (m *TemplateMiner) Clusters() []*Cluster {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.drain.Clusters()
}

// ExtractParameters returns the values of the template parameters
// (wildcards and masked values) for a message matching the cluster, by
// aligning the masked token sequences.
func (m *TemplateMiner) ExtractParameters(cluster *Cluster, message string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	tokens := m.drain.tokenize(m.masker.Mask(message))
	if len(tokens) != len(cluster.TemplateTokens) {
		return nil
	}

	maskNames := map[string]struct{}{}
	for _, name := range m.masker.MaskNames() {
		maskNames[name] = struct{}{}
	}

	parameters := []string{}
	for i, templateToken := range cluster.TemplateTokens {
		if templateToken == m.config.ParamStr {
			parameters = append(parameters, tokens[i])
			continue
		}

		if _, isMask := maskNames[templateToken]; isMask {
			parameters = append(parameters, tokens[i])
		}
	}

	return parameters
}

// Mask exposes the configured masking, mostly for tests.
func (m *TemplateMiner) Mask(message string) string {
	return m.masker.Mask(message)
}

func (m *TemplateMiner) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	clusters := m.drain.Clusters()

	var b strings.Builder
	for _, cluster := range clusters {
		b.WriteString(cluster.Template())
		b.WriteString("\n")
	}

	return b.String()
}
