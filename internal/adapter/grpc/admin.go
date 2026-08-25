package grpc

import (
	"context"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
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
