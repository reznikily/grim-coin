# GrimCoin Makefile

.PHONY: all build build-controller build-wallet build-darwin-arm64 run-controller run-wallet clean test deps help

# Переменные
BINARY_DIR=bin
DARWIN_ARM64_DIR=$(BINARY_DIR)/darwin/arm64
GO_BUILD_FLAGS=-ldflags="-s -w"
CONTROLLER_BINARY=grimcoin-controller
WALLET_BINARY=grimcoin-wallet

# Основные цели
all: deps build

# Установка зависимостей
deps:
	@echo "Installing dependencies..."
	go mod tidy
	go mod download

# Сборка всех компонентов
build: build-controller build-wallet

# Сборка контроллера
build-controller:
	@echo "Building controller..."
	@mkdir -p $(BINARY_DIR)
	CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/$(CONTROLLER_BINARY) ./cmd/controller

# Сборка кошелька
build-wallet:
	@echo "Building wallet..."
	@mkdir -p $(BINARY_DIR)
	CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/$(WALLET_BINARY) ./cmd/wallet

# Сборка для macOS ARM64 (согласно пользовательским правилам)
build-darwin-arm64:
	@echo "Building for darwin/arm64..."
	@mkdir -p $(DARWIN_ARM64_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o $(DARWIN_ARM64_DIR)/$(CONTROLLER_BINARY) ./cmd/controller
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o $(DARWIN_ARM64_DIR)/$(WALLET_BINARY) ./cmd/wallet

# Запуск контроллера
run-controller:
	@echo "Starting controller..."
	go run ./cmd/controller

# Запуск кошелька (требует указания конфига)
run-wallet:
	@echo "Starting wallet with config configs/wallet-1.yaml..."
	go run ./cmd/wallet -config configs/wallet-1.yaml

# Запуск кошелька с указанным ID
run-wallet-1:
	go run ./cmd/wallet -config configs/wallet-1.yaml

run-wallet-2:
	go run ./cmd/wallet -config configs/wallet-2.yaml

run-wallet-3:
	go run ./cmd/wallet -config configs/wallet-3.yaml

# Демонстрация работы системы (запуск контроллера и 3 кошельков в background)
demo:
	@echo "Starting GrimCoin demo..."
	@echo "Starting controller in background..."
	@go run ./cmd/controller &
	@sleep 2
	@echo "Starting wallet 1 in background..."
	@go run ./cmd/wallet -config configs/wallet-1.yaml &
	@sleep 1
	@echo "Starting wallet 2 in background..."
	@go run ./cmd/wallet -config configs/wallet-2.yaml &
	@sleep 1
	@echo "Starting wallet 3 in background..."
	@go run ./cmd/wallet -config configs/wallet-3.yaml &
	@echo "Demo started! Open web/observer.html to view the network state."
	@echo "Press Ctrl+C to stop all processes."

# Остановка всех процессов
stop:
	@echo "Stopping all GrimCoin processes..."
	@pkill -f "grimcoin-controller" || true
	@pkill -f "grimcoin-wallet" || true
	@pkill -f "go run ./cmd/controller" || true
	@pkill -f "go run ./cmd/wallet" || true

# Тестирование
test:
	@echo "Running tests..."
	go test -v ./...

# Линтинг кода
lint:
	@echo "Running linter..."
	golangci-lint run

# Форматирование кода
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Очистка
clean:
	@echo "Cleaning build artifacts and data..."
	rm -rf $(BINARY_DIR)
	rm -rf data/*.json
	go clean

# Создание директорий
setup:
	@echo "Creating directories..."
	@mkdir -p data
	@mkdir -p $(BINARY_DIR)
	@mkdir -p $(DARWIN_ARM64_DIR)

# Проверка состояния системы
status:
	@echo "=== GrimCoin System Status ==="
	@echo "Controller processes:"
	@pgrep -f "grimcoin-controller|go run ./cmd/controller" || echo "No controller processes running"
	@echo "\nWallet processes:"
	@pgrep -f "grimcoin-wallet|go run ./cmd/wallet" || echo "No wallet processes running"
	@echo "\nData files:"
	@ls -la data/ 2>/dev/null || echo "No data directory found"

# Помощь
help:
	@echo "GrimCoin Makefile Commands:"
	@echo ""
	@echo "Building:"
	@echo "  make build              - Build all components"
	@echo "  make build-controller   - Build controller only"
	@echo "  make build-wallet       - Build wallet only"
	@echo "  make build-darwin-arm64 - Build for macOS ARM64"
	@echo ""
	@echo "Running:"
	@echo "  make run-controller     - Run controller"
	@echo "  make run-wallet         - Run wallet with default config"
	@echo "  make run-wallet-N       - Run specific wallet (N=1,2,3)"
	@echo "  make demo               - Start full demo with controller + 3 wallets"
	@echo ""
	@echo "Development:"
	@echo "  make deps               - Install dependencies"
	@echo "  make test               - Run tests"
	@echo "  make lint               - Run linter"
	@echo "  make fmt                - Format code"
	@echo ""
	@echo "Maintenance:"
	@echo "  make clean              - Clean build artifacts and data"
	@echo "  make setup              - Create directories"
	@echo "  make status             - Show system status"
	@echo "  make stop               - Stop all processes"
