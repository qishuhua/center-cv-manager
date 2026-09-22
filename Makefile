.PHONY: web build dev test clean

web:
	cd web && npm install && npm run build
	powershell -NoProfile -Command "Remove-Item -Recurse -Force cmd/server/web_dist -ErrorAction SilentlyContinue; Copy-Item -Recurse web/dist cmd/server/web_dist"

build: web
	go build -o build/center-cv-manager.exe ./cmd/server

dev:
	go run ./cmd/server --listen 127.0.0.1:8099 --data ./data

test:
	go test ./...
	cd web && npm run build

clean:
	powershell -NoProfile -Command "Remove-Item -Recurse -Force build,web/dist -ErrorAction SilentlyContinue"
