// Mesure hors ligne : ce que les nouvelles règles de rareté et de
// disparition changent sur une journée réelle de l'instance.
package detect

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/bornholm/tezcatl/internal/core/drain"
	"github.com/bornholm/tezcatl/internal/core/model"
)

// instanceMasking mirrors the masking of the dogfooding instance, so a
// replay mines the same templates it does. The order matters: IP and
// UUID before NUM, NUM before HEX, or an epoch made of valid hex digits
// becomes a HEX.
func instanceMasking() *drain.Config {
	return &drain.Config{
		Masking: []drain.MaskingInstruction{
			{Pattern: `\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`, MaskWith: "IP"},
			{Pattern: `\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`, MaskWith: "UUID"},
			{Pattern: `\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`, MaskWith: "EMAIL"},
			{Pattern: `\b\d+\b`, MaskWith: "NUM"},
			{Pattern: `\b[0-9a-fA-F]{8,}\b`, MaskWith: "HEX"},
			{Pattern: `"Mozilla/[^"]*"`, MaskWith: "UA"},
		},
	}
}

type capturedLine struct {
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
}

// TestMeasureOnInstanceLogs replays a journal capture through the whole
// mining and detection path, once per configuration, and prints what
// each rule costs. The capture stays outside the repository: it is
// production log lines, and they belong on the machine that produced
// them. Point TEZCATL_LOGS_CAPTURE at one.
func TestMeasureOnInstanceLogs(t *testing.T) {
	path := os.Getenv("TEZCATL_LOGS_CAPTURE")
	if path == "" {
		t.Skip("set TEZCATL_LOGS_CAPTURE to a JSONL capture of log lines to run this")
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	lines := []capturedLine{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		var line capturedLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}

		if line.Message == "" || line.Timestamp.IsZero() {
			continue
		}

		lines = append(lines, line)
	}

	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	t.Logf("replaying %d lines", len(lines))

	replay := func(t *testing.T, configure func(*LogConfig)) (map[string]int, int) {
		t.Helper()

		config := DefaultLogConfig()
		configure(config)

		miner := drain.NewPartitionedMiner(instanceMasking())
		detector := NewLogDetector(config)

		counts := map[string]int{}

		for _, line := range lines {
			partition, err := miner.Partition(line.Source)
			if err != nil {
				t.Fatal(err)
			}

			result := partition.AddLogMessage(line.Message)

			for _, signal := range detector.Detect(&model.Observation{
				ID:        model.NewID(),
				Source:    line.Source,
				Modality:  model.ModalityLog,
				Timestamp: line.Timestamp,
				Log: &model.LogRecord{
					Raw:        line.Message,
					Template:   result.Cluster.Template(),
					TemplateID: result.Cluster.Template(),
				},
				Attributes: map[string]string{
					"drain.change_type": string(result.ChangeType),
				},
			}) {
				counts[signal.Kind]++
			}
		}

		return counts, len(miner.Partitions())
	}

	// Both runs expect every regular template back: the capture has no
	// markings, and with the default scope the disappearance column
	// would read zero without measuring anything. It is the floor and
	// the rarity rule being measured here, not the scope.
	before, partitions := replay(t, func(config *LogConfig) {
		config.DisappearanceScope = DisappearanceScopeAll
		config.RareMaxInterval = 0
		config.DisappearanceMinSilence = 0
	})

	after, _ := replay(t, func(config *LogConfig) {
		config.DisappearanceScope = DisappearanceScopeAll
	})

	t.Logf("partitions: %d", partitions)

	for _, kind := range []string{SignalLogNewTemplate, SignalLogRareTemplate, SignalLogFrequencySpike, SignalLogMissingTemplate} {
		t.Logf("%-28s before %4d  after %4d", kind, before[kind], after[kind])
	}
}
