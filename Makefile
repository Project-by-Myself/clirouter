.PHONY: build web go test bench run clean

build: web go

web:
	cd web && npm ci && npm run build

go:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/clirouter ./cmd/clirouter

test:
	go vet ./... && go test -race ./...

bench:
	go test -run xxx -bench . -benchmem ./internal/...

run: go
	./bin/clirouter

# Cross-compile release binaries (dashboard must be built first).
release: web
	for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=$$( [ $$os = windows ] && echo .exe ); \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o bin/clirouter-$$os-$$arch$$ext ./cmd/clirouter; \
	done

clean:
	rm -rf bin web/dist/assets
