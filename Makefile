.PHONY: build run e2e test check helm-check image
run: build
	./bin/notary-journal serve
build:
	CGO_ENABLED=0 go build -trimpath -o bin/notary-journal ./cmd/notary-journal

e2e: build
	cd e2e && npm ci && npx playwright install chromium && npx playwright test

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
