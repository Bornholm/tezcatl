package drain

import (
	"fmt"
	"strings"
	"testing"
)

// mediaConnFrame reproduces the shape of the longest templates the
// dogfooding instance learned, 92 and 85 tokens: a media_conn answer
// carrying one <host> block per CDN edge. The hostnames and tokens are
// made up, the structure and the length are not. The block count varies
// with the answer, so each answer minted a template of its own.
func mediaConnFrame(hosts int) string {
	var frame strings.Builder

	frame.WriteString(`10:31:02.114 [Client/Recv DEBUG] <iq from="s.whatsapp.net" id="1.2-3" type="result"><media_conn auth="AUTH" auth_ttl="21600" max_buckets="12" ttl="300">`)

	for i := range hosts {
		fmt.Fprintf(&frame, `<host fallback_hostname="fallback-%d.example.net" fallback_ip4="10.0.0.%d" fallback_ip6="fd00::%d" hostname="edge-%d.example.net" ip4="10.0.1.%d" ip6="fd00::1:%d" type="primary">`, i, i, i, i, i, i)
		frame.WriteString(`<upload/><download><video/><image/><gif/></download><download_buckets><1/></download_buckets></host>`)
	}

	frame.WriteString(`</media_conn></iq>`)

	return frame.String()
}

func TestTruncationFoldsRunawayPayloads(t *testing.T) {
	frames := []string{mediaConnFrame(6), mediaConnFrame(5)}

	// The cap only bites on lines that clear it, so the fixture has to
	// be as long as the real thing.
	for _, frame := range frames {
		if got := len(strings.Fields(frame)); got <= DefaultMaxTokens {
			t.Fatalf("fixture holds %d tokens, needs more than %d to exercise the cap", got, DefaultMaxTokens)
		}
	}

	mine := func(t *testing.T, maxTokens int) *TemplateMiner {
		t.Helper()

		miner, err := NewTemplateMiner(&Config{MaxTokens: maxTokens})
		if err != nil {
			t.Fatal(err)
		}

		for _, frame := range frames {
			miner.AddLogMessage(frame)
		}

		return miner
	}

	if got := len(mine(t, -1).Clusters()); got != len(frames) {
		t.Fatalf("without the cap the frames should stay apart, got %d clusters", got)
	}

	clusters := mine(t, DefaultMaxTokens).Clusters()
	if len(clusters) != 1 {
		for _, cluster := range clusters {
			t.Logf("cluster: %s", cluster.Template())
		}

		t.Fatalf("the frames should share one template, got %d", len(clusters))
	}

	template := clusters[0].Template()
	if !strings.HasSuffix(template, TruncationMarker) {
		t.Errorf("a cut template must say so, got %q", template)
	}

	if got := len(strings.Fields(template)); got != DefaultMaxTokens {
		t.Errorf("the cut template holds %d tokens, want %d", got, DefaultMaxTokens)
	}
}

// TestTruncationLeavesOrdinaryLinesAlone: 96% of the templates learned
// on the dogfooding instance were under the cap, and none of them may
// change shape.
func TestTruncationLeavesOrdinaryLinesAlone(t *testing.T) {
	miner, err := NewTemplateMiner(&Config{})
	if err != nil {
		t.Fatal(err)
	}

	for _, line := range []string{
		"Accepted publickey for root from 10.0.0.1 port 22 ssh2",
		"pam_unix(cron:session): session closed for user root",
		"agent: appel du modèle en échec, conclusion tentée",
	} {
		result := miner.AddLogMessage(line)
		if template := result.Cluster.Template(); strings.Contains(template, TruncationMarker) {
			t.Errorf("%q was cut: %q", line, template)
		}
	}
}

func TestMaxTokensRefusesAnImpossibleCap(t *testing.T) {
	if _, err := NewTemplateMiner(&Config{MaxTokens: 1}); err == nil {
		t.Error("a cap of one leaves room for the marker only, it must be refused")
	}
}
