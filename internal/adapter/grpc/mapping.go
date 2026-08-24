package grpc

import (
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toProtoObservation(obs *model.Observation) *tezcatlv1.Observation {
	proto := &tezcatlv1.Observation{
		Id:         obs.ID,
		Source:     obs.Source,
		Modality:   toProtoModality(obs.Modality),
		Attributes: obs.Attributes,
	}

	if !obs.Timestamp.IsZero() {
		proto.Timestamp = timestamppb.New(obs.Timestamp)
	}

	if obs.Log != nil {
		proto.Log = &tezcatlv1.LogRecord{Raw: obs.Log.Raw}
	}

	if obs.Metric != nil {
		proto.Metric = &tezcatlv1.MetricSample{
			Name:   obs.Metric.Name,
			Value:  obs.Metric.Value,
			Labels: obs.Metric.Labels,
		}
	}

	return proto
}

func fromProtoObservation(proto *tezcatlv1.Observation, ingestedAt time.Time) model.Observation {
	obs := model.Observation{
		ID:         proto.GetId(),
		Source:     proto.GetSource(),
		Modality:   fromProtoModality(proto.GetModality()),
		IngestedAt: ingestedAt,
		Attributes: proto.GetAttributes(),
	}

	if timestamp := proto.GetTimestamp(); timestamp != nil {
		obs.Timestamp = timestamp.AsTime()
	}

	if log := proto.GetLog(); log != nil {
		obs.Log = &model.LogRecord{Raw: log.GetRaw()}
	}

	if metric := proto.GetMetric(); metric != nil {
		obs.Metric = &model.MetricSample{
			Name:   metric.GetName(),
			Value:  metric.GetValue(),
			Labels: metric.GetLabels(),
		}
	}

	return obs
}

func toProtoModality(modality model.Modality) tezcatlv1.Modality {
	switch modality {
	case model.ModalityLog:
		return tezcatlv1.Modality_MODALITY_LOG
	case model.ModalityMetric:
		return tezcatlv1.Modality_MODALITY_METRIC
	case model.ModalityTrace:
		return tezcatlv1.Modality_MODALITY_TRACE
	default:
		return tezcatlv1.Modality_MODALITY_UNSPECIFIED
	}
}

func fromProtoModality(modality tezcatlv1.Modality) model.Modality {
	switch modality {
	case tezcatlv1.Modality_MODALITY_LOG:
		return model.ModalityLog
	case tezcatlv1.Modality_MODALITY_METRIC:
		return model.ModalityMetric
	case tezcatlv1.Modality_MODALITY_TRACE:
		return model.ModalityTrace
	default:
		return ""
	}
}
