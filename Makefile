cephplayground: ; mkdir -p bin && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/cephplayground ./cmd/cephplayground
.PHONY: cephplayground
