.PHONY: build run-controller run-wallet clean

build:
	@echo "Building controller..."
	@go build -o bin/controller cmd/controller/main.go
	@echo "Building wallet..."
	@go build -o bin/wallet cmd/wallet/main.go
	@echo "Build complete!"

run-controller: build
	@echo "Starting controller..."
	@./bin/controller

run-wallet: build
	@echo "Starting wallet..."
	@./bin/wallet

clean:
	@echo "Cleaning..."
	@rm -rf bin/
	@rm -f .wallet_profile.json
	@echo "Clean complete!"

deps:
	@echo "Installing dependencies..."
	@go mod download
	@go mod tidy
	@echo "Dependencies installed!"
