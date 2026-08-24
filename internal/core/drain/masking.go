package drain

import (
	"regexp"

	"github.com/pkg/errors"
)

// MaskingInstruction replaces every match of Pattern with the wrapped
// mask name (e.g. <IP>), hiding variable parts from template mining.
// Patterns use the RE2 syntax: lookarounds are not supported, prefer \b
// boundaries.
type MaskingInstruction struct {
	Pattern  string `yaml:"pattern" json:"pattern"`
	MaskWith string `yaml:"mask_with" json:"mask_with"`
}

type Masker struct {
	instructions []compiledInstruction
	prefix       string
	suffix       string
}

type compiledInstruction struct {
	regex       *regexp.Regexp
	replacement string
}

func NewMasker(instructions []MaskingInstruction, prefix string, suffix string) (*Masker, error) {
	masker := &Masker{
		prefix: prefix,
		suffix: suffix,
	}

	for _, instruction := range instructions {
		regex, err := regexp.Compile(instruction.Pattern)
		if err != nil {
			return nil, errors.Wrapf(err, "malformed masking pattern %q", instruction.Pattern)
		}

		masker.instructions = append(masker.instructions, compiledInstruction{
			regex:       regex,
			replacement: prefix + instruction.MaskWith + suffix,
		})
	}

	return masker, nil
}

func (m *Masker) Mask(content string) string {
	for _, instruction := range m.instructions {
		content = instruction.regex.ReplaceAllString(content, instruction.replacement)
	}

	return content
}

// MaskNames returns the wrapped mask names, which appear as parameters in
// mined templates.
func (m *Masker) MaskNames() []string {
	names := make([]string, 0, len(m.instructions))
	for _, instruction := range m.instructions {
		names = append(names, instruction.replacement)
	}

	return names
}
