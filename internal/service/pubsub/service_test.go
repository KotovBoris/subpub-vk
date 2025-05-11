package pubsub_service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	pubsub_service "github.com/KotovBoris/subpub-vk/internal/service/pubsub"
	"github.com/KotovBoris/subpub-vk/internal/subpub"
	pb "github.com/KotovBoris/subpub-vk/pkg/grpc/generated/pubsub"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// Настраивает тестовый сервер и клиент
func setupTestServer(t *testing.T) (pb.PubSubClient, func()) {
	t.Helper()

	sp := subpub.NewSubPub()
	testLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	service := pubsub_service.NewService(testLogger, sp)

	listener := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	pb.RegisterPubSubServer(grpcServer, service)

	go func() {
		if err := grpcServer.Serve(listener); err != nil && err != grpc.ErrServerStopped {
			t.Errorf("Тестовый gRPC сервер завершился с ошибкой: %v", err)
		}
	}()

	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("Ошибка подключения к bufnet: %v", err)
	}

	client := pb.NewPubSubClient(conn)

	cleanupFunc := func() {
		conn.Close()
		grpcServer.Stop()
		sp.Close(context.Background())
		listener.Close()
	}
	return client, cleanupFunc
}

func TestPublish(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()
	_, err := client.Publish(ctx, &pb.PublishRequest{Key: "k1", Data: "d1"})
	if err != nil {
		t.Fatalf("Publish завершился с ошибкой: %v", err)
	}

	// пустой ключ
	_, err = client.Publish(ctx, &pb.PublishRequest{Key: "", Data: "d2"})
	if st, ok := status.FromError(err); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("Publish с пустым ключом: ожидался InvalidArgument, получено: %v (статус: %s)", err, st.Code())
	}
}

func TestSubscribeAndPublish(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	testKey := "key-subpub"
	testData := "event-data"
	numMessages := 1

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second) // Общий таймаут теста
	defer cancel()

	receivedEvents := make(chan *pb.Event, numMessages)
	var wg sync.WaitGroup
	wg.Add(1)

	// Горутина подписчика
	go func() {
		defer wg.Done()
		defer close(receivedEvents)

		stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{Key: testKey})
		if err != nil {
			t.Errorf("Subscribe завершился с ошибкой: %v", err)
			return
		}

		for i := 0; i < numMessages; i++ {
			event, errRecv := stream.Recv()
			if errRecv != nil {
				// Ожидаемые ошибки при завершении контекста или закрытии стрима
				if errors.Is(errRecv, context.Canceled) || errors.Is(errRecv, context.DeadlineExceeded) || errors.Is(errRecv, io.EOF) {
					return
				}
				t.Errorf("Recv завершился с ошибкой: %v", errRecv)
				return
			}
			receivedEvents <- event
		}
	}()

	time.Sleep(50 * time.Millisecond) // Пауза для старта подписчика

	_, pubErr := client.Publish(context.Background(), &pb.PublishRequest{Key: testKey, Data: testData})
	if pubErr != nil {
		t.Fatalf("Publish завершился с ошибкой: %v", pubErr)
	}

	select {
	case event, ok := <-receivedEvents:
		if !ok {
			t.Fatalf("Канал событий неожиданно закрыт")
		}
		if event.GetData() != testData {
			t.Fatalf("Ожидались данные '%s', получено '%s'", testData, event.GetData())
		}
	case <-ctx.Done():
		t.Fatalf("Таймаут теста при ожидании события: %v", ctx.Err())
	}

	cancel() // Явно отменяем контекст для завершения подписчика
	wg.Wait()
}

// Пустой ключ
func TestSubscribe_EmptyKey(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()
	stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{Key: ""})
	if err == nil {
		_, err = stream.Recv()
	}

	if st, ok := status.FromError(err); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("Subscribe с пустым ключом: ожидался InvalidArgument, получено: %v (статус: %s)", err, st.Code())
	}
}
