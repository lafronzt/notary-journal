.PHONY: build run test check helm-check image
run: build
	./bin/notary-journal serve
build:
	CGO_ENABLED=0 go build -trimpath -o bin/notary-journal ./cmd/notary-journal

test:
	go test -race ./...

check:
	go vet ./...
	node --check internal/journal/static/app.js

helm-check:
	helm lint --strict deploy/helm/notary-journal --set auth.existingSecret=journal-auth
	helm template journal deploy/helm/notary-journal --set auth.existingSecret=journal-auth > /dev/null

image:
	docker build -t notary-journal:0.1.0 .
