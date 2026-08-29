package grpc

import (
	"context"
	"encoding/json"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

// AdminServer exposes the runtime feedback service over gRPC.
type AdminServer struct {
	tezcatlv1.UnimplementedAdminServiceServer

	service *admin.Service
}

func NewAdminServer(service *admin.Service) *AdminServer {
	return &AdminServer{service: service}
}

// Register makes the admin server attachable to the ingestion listener.
func (s *AdminServer) Register(server *grpc.Server) {
	tezcatlv1.RegisterAdminServiceServer(server, s)
}

// streamBuffer is how many events a subscriber may fall behind before
// the server starts dropping its events.
const streamBuffer = 256

// StreamEvents replays the recent events, then follows the live ones
// until the client goes away or the server stops.
func (s *AdminServer) StreamEvents(req *tezcatlv1.StreamEventsRequest, stream tezcatlv1.AdminService_StreamEventsServer) error {
	recent, events, cancel, err := s.service.SubscribeEvents(int(req.GetHistory()), streamBuffer)
	if err != nil {
		return errors.WithStack(err)
	}

	defer cancel()

	for _, event := range recent {
		if err := sendEvent(stream, event); err != nil {
			return errors.WithStack(err)
		}
	}

	if err := stream.Send(&tezcatlv1.EventEnvelope{Ready: true}); err != nil {
		return errors.WithStack(err)
	}

	for {
		select {
		case event, open := <-events:
			if !open {
				return nil
			}

			if err := sendEvent(stream, event); err != nil {
				return errors.WithStack(err)
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

func sendEvent(stream tezcatlv1.AdminService_StreamEventsServer, event model.Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return errors.WithStack(err)
	}

	return errors.WithStack(stream.Send(&tezcatlv1.EventEnvelope{Json: string(encoded)}))
}

// ListEvents returns past events from the server's local event log.
func (s *AdminServer) ListEvents(ctx context.Context, req *tezcatlv1.ListEventsRequest) (*tezcatlv1.ListEventsResponse, error) {
	since, err := parseEventBound(req.GetSince())
	if err != nil {
		return nil, errors.Wrap(err, "malformed since")
	}

	until, err := parseEventBound(req.GetUntil())
	if err != nil {
		return nil, errors.Wrap(err, "malformed until")
	}

	events, err := s.service.ListEvents(since, until, int(req.GetLimit()))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	res := &tezcatlv1.ListEventsResponse{
		Events: make([]*tezcatlv1.EventEnvelope, 0, len(events)),
	}

	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		res.Events = append(res.Events, &tezcatlv1.EventEnvelope{Json: string(encoded)})
	}

	return res, nil
}

func parseEventBound(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}

	bound, err := time.Parse(time.RFC3339Nano, raw)

	return bound, errors.WithStack(err)
}

func (s *AdminServer) MarkTemplate(ctx context.Context, req *tezcatlv1.MarkTemplateRequest) (*tezcatlv1.MarkTemplateResponse, error) {
	if err := s.service.MarkTemplate(req.GetTemplate(), detect.Marking(req.GetMarking())); err != nil {
		return nil, errors.WithStack(err)
	}

	return &tezcatlv1.MarkTemplateResponse{}, nil
}

func (s *AdminServer) ListMetrics(ctx context.Context, req *tezcatlv1.ListMetricsRequest) (*tezcatlv1.ListMetricsResponse, error) {
	series := s.service.Metrics()

	res := &tezcatlv1.ListMetricsResponse{
		Metrics: make([]*tezcatlv1.MetricInfo, 0, len(series)),
	}

	for _, info := range series {
		res.Metrics = append(res.Metrics, &tezcatlv1.MetricInfo{
			Key:     info.Key,
			Samples: info.Samples,
			Mean:    info.Mean,
			StdDev:  info.StdDev,
			Recent:  info.Recent,
			Warmup:  info.Warmup,
		})
	}

	return res, nil
}

func (s *AdminServer) ListTemplates(ctx context.Context, req *tezcatlv1.ListTemplatesRequest) (*tezcatlv1.ListTemplatesResponse, error) {
	templates := s.service.Templates()

	res := &tezcatlv1.ListTemplatesResponse{
		Templates: make([]*tezcatlv1.TemplateInfo, 0, len(templates)),
	}

	for _, template := range templates {
		res.Templates = append(res.Templates, &tezcatlv1.TemplateInfo{
			Partition: template.Partition,
			Id:        template.ID,
			Template:  template.Template,
			Size:      template.Size,
			Marking:   string(template.Marking),
		})
	}

	return res, nil
}
