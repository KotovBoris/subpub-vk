package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/KotovBoris/subpub-vk/internal/config"
	pubsub_service "github.com/KotovBoris/subpub-vk/internal/service/pubsub"
	"github.com/KotovBoris/subpub-vk/internal/subpub"
	pb "github.com/KotovBoris/subpub-vk/pkg/grpc/generated/pubsub"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func Run(configPath string) error {
	cfg := config.MustLoad(configPath)

	log := setupLogger(cfg.Logger.Level)
	log.Info("Logger initialized", slog.String("level", cfg.Logger.Level))
	log.Info("Configuration loaded", slog.String("path", configPath))

	log.Debug("Config dump", slog.Any("config", cfg))

	sp := subpub.NewSubPub()
	log.Info("SubPub system initialized")

	pubSubService := pubsub_service.NewService(log, sp)
	log.Info("PubSub gRPC service implementation created")

	grpcServerErrChan := make(chan error, 1)
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)

	grpcServer := grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
	)
	pb.RegisterPubSubServer(grpcServer, pubSubService)
	log.Info("gRPC service registered")

	go func() {
		addr := fmt.Sprintf(":%s", cfg.GRPCServer.Port)
		log.Info("Starting gRPC server", slog.String("address", addr))

		lis, err := net.Listen("tcp", addr)
		if err != nil {
			log.Error("Failed to listen", slog.String("error", err.Error()))
			grpcServerErrChan <- fmt.Errorf("failed to listen on %s: %w", addr, err)
			return
		}

		if err := grpcServer.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			log.Error("gRPC server failed", slog.String("error", err.Error()))
			grpcServerErrChan <- fmt.Errorf("gRPC server failed: %w", err)
		} else {
			log.Info("gRPC server stopped gracefully.")
			close(grpcServerErrChan)
		}
	}()

	// Ожидание сигнала остановки или ошибки сервера
	select {
	case sig := <-stopChan:
		log.Info("Received stop signal", slog.String("signal", sig.String()))
	case err := <-grpcServerErrChan:
		if err != nil {
			log.Error("gRPC server start failed", slog.String("error", err.Error()))

			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.GRPCServer.ShutdownTimeout)
			defer cancel()
			if closeErr := sp.Close(shutdownCtx); closeErr != nil {
				log.Error("SubPub system close error on server failure", slog.String("error", closeErr.Error()))
			}
			return err
		}
		log.Info("gRPC server already stopped")
	}

	// Graceful Shutdown
	log.Info("Shutting down server...")

	grpcServer.GracefulStop()
	log.Info("gRPC server GracefulStop completed.")

	log.Info("Closing SubPub system...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.GRPCServer.ShutdownTimeout)
	defer cancel()

	if err := sp.Close(shutdownCtx); err != nil {
		log.Error("SubPub system close error", slog.String("error", err.Error()))
		return fmt.Errorf("subpub close error: %w", err)
	}
	log.Info("SubPub system closed.")

	return nil
}

func setupLogger(level string) *slog.Logger {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	return log
}
