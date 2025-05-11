package pubsub_service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/KotovBoris/subpub-vk/internal/subpub"
	pb "github.com/KotovBoris/subpub-vk/pkg/grpc/generated/pubsub"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	eventChannelBufferSize = 128
)

type Service struct {
	pb.UnimplementedPubSubServer
	log    *slog.Logger
	subpub subpub.SubPub
}

func NewService(log *slog.Logger, sp subpub.SubPub) *Service {
	return &Service{
		log:    log,
		subpub: sp,
	}
}

func (s *Service) Publish(ctx context.Context, req *pb.PublishRequest) (*emptypb.Empty, error) {
	if req.GetKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "key cannot be empty")
	}

	s.log.Debug("Publishing message", slog.String("key", req.GetKey()), slog.Int("data_len", len(req.GetData())))

	err := s.subpub.Publish(req.GetKey(), req.GetData())
	if err != nil {
		s.log.Error("Publish failed", slog.String("key", req.GetKey()), slog.String("error", err.Error()))
		return nil, status.Errorf(codes.Internal, "failed to publish message: %v", err)
	}

	return &emptypb.Empty{}, nil
}

func (s *Service) Subscribe(req *pb.SubscribeRequest, stream pb.PubSub_SubscribeServer) error {
	if req.GetKey() == "" {
		return status.Error(codes.InvalidArgument, "key cannot be empty")
	}

	key := req.GetKey()
	log := s.log.With(slog.String("key", key))
	log.Info("New subscription request")
	ctx := stream.Context()

	eventsChan := make(chan string, eventChannelBufferSize)

	messageHandler := func(msg interface{}) {
		data, ok := msg.(string)
		if !ok {
			log.Error("Handler received unexpected type", slog.Any("type", fmt.Sprintf("%T", msg)))
			return
		}

		select {
		case <-ctx.Done():
			return
		case eventsChan <- data:
		default: // пропускаем сообщение, если буффер заполнен
			log.Warn("Event channel buffer full, dropping message")
		}
	}

	subscription, err := s.subpub.Subscribe(key, messageHandler)
	if err != nil {
		log.Error("SubPub subscribe failed", slog.String("error", err.Error()))
		return status.Errorf(codes.Internal, "failed to subscribe: %v", err)
	}

	defer func() {
		log.Info("Unsubscribing client")
		subscription.Unsubscribe()
	}()

	log.Info("Subscription established")

	for {
		select {
		case <-ctx.Done(): // клиент отключился
			log.Info("Client context done", slog.String("reason", ctx.Err().Error()))
			return nil

		case eventData, ok := <-eventsChan:
			if !ok {
				log.Error("Event channel closed unexpectedly")
				return status.Error(codes.Internal, "event channel closed unexpectedly")
			}

			err := stream.Send(&pb.Event{Data: eventData})
			if err != nil {
				log.Error("Failed to send event to client", slog.String("error", err.Error()))
				if status.Code(err) == codes.Unavailable || errors.Is(err, io.EOF) {
					return nil // клиент отключился
				}
				return status.Errorf(codes.Internal, "failed to send event: %v", err)
			}
			log.Debug("Event sent to client", slog.Int("data_len", len(eventData)))
		}
	}
}
