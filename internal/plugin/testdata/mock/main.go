// Mock source plugin used by the host loader tests: emits `count`
// observations for `service`, then keeps the stream open until the host
// closes it.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	sdk "github.com/bornholm/tezcatl/pkg/plugin"
)

func main() {
	sdk.Serve(sdk.SourceFunc(func(ctx context.Context, rawConfig []byte, emit sdk.EmitFunc) error {
		config := struct {
			Count   int    `json:"count"`
			Service string `json:"service"`
		}{}

		if err := json.Unmarshal(rawConfig, &config); err != nil {
			return err
		}

		for i := range config.Count {
			obs := &tezcatlv1.Observation{
				Id:       fmt.Sprintf("mock-%d", i),
				Service:  config.Service,
				Modality: tezcatlv1.Modality_MODALITY_LOG,
				Log:      &tezcatlv1.LogRecord{Raw: fmt.Sprintf("mock line %d", i)},
			}

			if err := emit(obs); err != nil {
				return err
			}
		}

		<-ctx.Done()

		return ctx.Err()
	}))
}
