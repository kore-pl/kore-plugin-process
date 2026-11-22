# kore-plugin-process 构建入口：build / dist / check / clean。
# 确定性纪律：二进制只内嵌静态 manifest.json（不含 size_bytes），故回填 info.json
# 的 size_bytes 不改变二进制，切断"改清单→改二进制→改包体"的自指震荡。
SHELL := /bin/bash

ID := process
BIN := bin/$(ID)
DIST := dist
VERSION := $(shell sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' info.json | head -n 1)
PACKAGE := $(DIST)/$(ID)-$(VERSION).tar.gz

.PHONY: build dist check clean

# 静态构建：G-16 兼容性基线（CGO_ENABLED=0 + GOAMD64=v1 + trimpath + -s -w）。
build:
	CGO_ENABLED=0 GOWORK=off GOAMD64=v1 go build -trimpath -buildvcs=false -ldflags="-s -w" -o $(BIN) .

# 确定性打包：显式条目 info.json bin SHA256SUMS（无 ./ 根条目，tar 首条目 info.json）。
# chmod 归一，size_bytes 迭代到与包体一致（二进制内嵌 manifest.json 故必然收敛）。
# --sort=name + --mtime=@0 + 固定 owner/group + gzip -9n 保证两次 dist sha 一致。
dist: build
	@set -eo pipefail; \
	rm -rf $(DIST)/stage $(PACKAGE) $(PACKAGE).sha256; \
	mkdir -p $(DIST)/stage/bin; \
	chmod 0644 info.json; \
	chmod 0755 $(BIN); \
	for _ in 1 2 3 4 5 6; do \
		cp info.json $(DIST)/stage/info.json; \
		cp $(BIN) $(DIST)/stage/bin/$(ID); \
		( cd $(DIST)/stage && sha256sum info.json bin/$(ID) > SHA256SUMS ); \
		chmod 0644 $(DIST)/stage/info.json $(DIST)/stage/SHA256SUMS; \
		chmod 0755 $(DIST)/stage/bin $(DIST)/stage/bin/$(ID); \
		tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -cf - \
			-C $(DIST)/stage info.json bin SHA256SUMS | gzip -9n > $(PACKAGE); \
		actual=$$(stat -c%s $(PACKAGE)); \
		size=$$(sed -n 's/.*"size_bytes"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' info.json); \
		if [ "$$actual" = "$$size" ]; then break; fi; \
		sed "s/\"size_bytes\": [0-9]*/\"size_bytes\": $$actual/" info.json > $(DIST)/info.json.next; \
		mv $(DIST)/info.json.next info.json; \
		chmod 0644 info.json; \
	done; \
	( cd $(DIST) && sha256sum $(ID)-$(VERSION).tar.gz > $(ID)-$(VERSION).tar.gz.sha256 ); \
	rm -rf $(DIST)/stage $(DIST)/info.json.next; \
	echo "打包完成: $(PACKAGE)（源 size_bytes=$$(sed -n 's/.*\"size_bytes\"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' info.json)，实际包体=$$(stat -c%s $(PACKAGE))）"

# 门禁：gofmt（有输出即失败）→ go vet → go test。
check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt 需要格式化："; echo "$$out"; exit 1; fi; echo "gofmt: clean"
	GOWORK=off go vet ./...
	GOWORK=off go test -timeout 200s ./...

clean:
	rm -rf bin dist
	rm -f go.work.sum
