.PHONY: all build build-fi build-race test vet clean

all: build build-fi build-race

build:
	@mkdir -p bin
	go build -o bin/studentdb ./cmd/studentdb

build-fi:
	@mkdir -p bin
	go build -tags faultinject -o bin/studentdb-fi ./cmd/studentdb

build-race:
	@mkdir -p bin
	go build -race -o bin/studentdb-race ./cmd/studentdb

test:
	go test -v ./...

vet:
	go vet ./...

clean:
	rm -rf bin/

