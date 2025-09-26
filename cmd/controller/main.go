package main

import (
	"context"
	"grim-coin/internal/controller"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	log.Println("=== GrimCoin Controller ===")
	log.Println("Starting controller with WebSocket servers:")
	log.Println("- P1 (wallets): :8000")
	log.Println("- P2 (observers): :8080")

	// Создаем контроллер
	server := controller.NewServer()

	// Создаем контекст для graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())

	// Обрабатываем сигналы для graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal")
		cancel()
	}()

	// Запускаем сервер
	if err := server.Start(ctx); err != nil {
		log.Fatalf("Controller failed: %v", err)
	}

	log.Println("Controller shutdown complete")
}
