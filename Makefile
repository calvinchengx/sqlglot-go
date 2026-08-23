# sqlglot-go — a Go port of sqlglot, verified against the Python reference.
#
#   make doctor     # toolchain
#   make test       # unit tests + the differential run against the reference
#   make oracle     # regenerate testdata/expected from the pinned sqlglot commit
#   make coverage   # print per-dialect coverage against the reference
#   make lint       # vet + golangci-lint
SQLGLOT ?= $(HOME)/opensource/sqlglot
GOLANGCI = golangci/golangci-lint:v2.13.1

.PHONY: help doctor test oracle coverage cover lint clean

help: ## Show the available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

doctor: ## Check the toolchain
	@go version
	@test -d $(SQLGLOT) && echo "reference: $(SQLGLOT) @ $$(git -C $(SQLGLOT) rev-parse --short HEAD)" || echo "reference NOT found at $(SQLGLOT) (only needed for make oracle)"

test: ## Unit tests and the differential run against the reference
	go test ./...

oracle: ## Regenerate expectations and generated tables from the PINNED reference (refuses any other)
	python3 harness/oracle.py --sqlglot $(SQLGLOT) --out testdata/expected
	python3 harness/gen_classes.py > sqlglot/classes_gen.go && gofmt -w sqlglot/classes_gen.go
	python3 harness/gen_tokenizer.py --sqlglot $(SQLGLOT) --out sqlglot && gofmt -w sqlglot/tokentype_gen.go sqlglot/dialects_gen.go

coverage: test ## Per-dialect coverage against the reference
	@python3 -c 'import json; c=json.load(open("testdata/coverage.json")); print(f"reference {c[\"reference\"][:12]}  {c[\"matched\"]}/{c[\"total\"]}"); [print(f"  {d:10} {v[\"matched\"]:4}/{v[\"total\"]:<4} unparsed {v[\"unparsed\"]:4} mismatched {v[\"mismatched\"]}") for d,v in sorted(c["by_dialect"].items())]'

lint: ## vet and golangci-lint, in a container
	go vet ./...
	docker run --rm -v "$$(pwd):/src" -w /src $(GOLANGCI) golangci-lint run ./...

cover: ## Test coverage of the port
	@go test ./... -coverpkg=./sqlglot/ -coverprofile=/tmp/sqlglot-go-cover.out >/dev/null
	@go tool cover -func=/tmp/sqlglot-go-cover.out | grep -v " 100.0%$$" || echo "  every statement covered"

clean: ## Remove generated coverage
	rm -f testdata/coverage.json testdata/token_coverage.json
