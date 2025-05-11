package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/KotovBoris/subpub-vk/pkg/grpc/generated/pubsub"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// Флаги командной строки
	serverAddr := flag.String("addr", "localhost:50051", "Адрес gRPC сервера")
	action := flag.String("action", "", "Действие: 'publish' или 'subscribe'")
	key := flag.String("key", "", "Ключ для публикации/подписки")
	data := flag.String("data", "", "Данные для публикации")
	flag.Parse()

	if (*action != "publish" && *action != "subscribe") || *key == "" {
		flag.Usage() // Cправка, если флаги неверны
		os.Exit(1)
	}

	// Соединение с сервером
	log.Printf("Подключение к %s...", *serverAddr)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	conn, err := grpc.DialContext(dialCtx, *serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("Ошибка подключения: %v", err)
	}
	defer conn.Close()
	log.Println("Соединение установлено.")

	client := pb.NewPubSubClient(conn)

	// Действие
	switch *action {
	case "publish":
		handlePublish(client, *key, *data)
	case "subscribe":
		handleSubscribe(client, *key)
	}
}

func handlePublish(client pb.PubSubClient, key string, data string) {
	log.Printf("Публикация: key='%s', data='%s'", key, data)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Publish(ctx, &pb.PublishRequest{Key: key, Data: data})
	if err != nil {
		log.Fatalf("Ошибка публикации: %v", err)
	}
	log.Println("Успешно опубликовано.")
}

func handleSubscribe(client pb.PubSubClient, key string) {
	log.Printf("Подписка: key='%s'...", key)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Перехват Ctrl+C для отмены контекста
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stopChan
		log.Println("Получен сигнал остановки, завершение...")
		cancel()
	}()

	stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{Key: key})
	if err != nil {
		log.Fatalf("Ошибка подписки: %v", err)
	}
	log.Println("Подписка активна (Ctrl+C для выхода)")

	for {
		event, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil || err == io.EOF {
				log.Println("Завершение потока.")
				return
			}
			log.Fatalf("Ошибка чтения потока: %v", err)
			return
		}
		log.Printf("<<< Получено: \"%s\"", event.GetData())
	}
}
