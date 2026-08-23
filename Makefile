# sqlglot-go — a Go port of sqlglot, verified against the Python reference.
#
#   make doctor     # toolchain
#   make test       # unit tests + the differential run against the reference
#   make oracle     # regenerate testdata/expected from the pinned sqlglot commit
#   make coverage   # print per-dialect coverage against the reference
#   make lint       # vet + golangci-lint
#
# These targets are convenience, and they assume GNU make and a POSIX shell:
# Linux, macOS, or WSL / Git Bash on Windows. They are NOT the build.
#
# The build and the tests are the Go toolchain alone and run natively on
# Linux, macOS and Windows, on both amd64 and arm64 -- CI proves it on five
# platforms, and it invokes `go` directly rather than make for exactly that
# reason. A Windows developer needs `go test ./...` and nothing else; see
# "Working on Windows" in the README.
SQLGLOT ?= $(HOME)/opensource/sqlglot
SERVICE ?= $(HOME)/calvinchengx/emulators/data-agent-service
GOLANGCI = golangci/golangci-lint:v2.13.1
# Windows spells it `python`; POSIX distributions increasingly only ship
# `python3`. Override rather than edit.
PYTHON ?= python3

.PHONY: help doctor test oracle service coverage cover gaps lint clean

help: ## Show the available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

doctor: ## Check the toolchain
	@go version
	@$(PYTHON) --version || echo "$(PYTHON) NOT found (only needed for make oracle and make service)"
	@test -d $(SQLGLOT) && echo "reference: $(SQLGLOT) @ $$(git -C $(SQLGLOT) rev-parse --short HEAD)" || echo "reference NOT found at $(SQLGLOT) (only needed for make oracle)"

test: ## Unit tests and the differential run against the reference
	go test ./...

oracle: ## Regenerate expectations and generated tables from the PINNED reference (refuses any other)
	$(PYTHON) harness/oracle.py --sqlglot $(SQLGLOT) --out testdata/expected
	$(PYTHON) harness/gen_classes.py --sqlglot $(SQLGLOT) > sqlglot/classes_gen.go && gofmt -w sqlglot/classes_gen.go
	$(PYTHON) harness/gen_tokenizer.py --sqlglot $(SQLGLOT) --out sqlglot && gofmt -w sqlglot/tokentype_gen.go sqlglot/dialects_gen.go
	$(PYTHON) harness/gen_parser.py --sqlglot $(SQLGLOT) && gofmt -w sqlglot/parser_gen.go

service: ## Re-extract the corpus of SQL data agent service is held to
	$(PYTHON) harness/gen_service_corpus.py --service $(SERVICE) --sqlglot $(SQLGLOT)

fuzz: ## Fuzz the generator and let the reference judge what it finds (SECONDS=45)
	@rm -f /tmp/sqlglot-go-candidates.txt
	@DAS_FUZZ_COLLECT=/tmp/sqlglot-go-candidates.txt \
		go test ./sqlglot/ -run=XXX -fuzz=FuzzGeneratedSQLCanBeReadBack \
		-fuzztime=$${SECONDS:-45}s
	@if [ -s /tmp/sqlglot-go-candidates.txt ]; then \
		sort -u /tmp/sqlglot-go-candidates.txt -o /tmp/sqlglot-go-candidates.txt; \
		$(PYTHON) harness/adjudicate.py --sqlglot $(SQLGLOT) \
			--candidates /tmp/sqlglot-go-candidates.txt; \
	else \
		echo "the generator wrote nothing the parser could not read back"; \
	fi

gaps: ## Why the port refuses what it refuses, most common first
	@go test ./harness/ -run TestGapReport -v 2>&1 | sed -n 's/^ *gaps_test.go:[0-9]*: //p'

readme: test ## Rewrite the README's tables from what the suite just measured
	$(PYTHON) harness/gen_readme.py

coverage: test ## Per-dialect coverage, against the reference and against the service
	@$(PYTHON) -c 'import json; c=json.load(open("testdata/service/coverage.json")); print("data agent service:"); [print("  %-24s %3d/%d" % (k, v["parsed"], v["total"])) for k, v in sorted(c["by_category"].items())]'
	@$(PYTHON) -c 'import json; c=json.load(open("testdata/coverage.json")); print("reference %s  %d/%d" % (c["reference"][:12], c["matched"], c["total"])); [print("  %-10s %4d/%-4d unparsed %4d mismatched %d" % (d, v["matched"], v["total"], v["unparsed"], v["mismatched"])) for d, v in sorted(c["by_dialect"].items())]'

lint: ## vet and golangci-lint, in a container
	go vet ./...
	docker run --rm -v "$$(pwd):/src" -w /src $(GOLANGCI) golangci-lint run ./...

cover: ## Test coverage of the port
	@go test ./... -coverpkg=./sqlglot/ -coverprofile=cover.out >/dev/null
	@go tool cover -func=cover.out | grep -v " 100.0%$$" || echo "  every statement covered"

clean: ## Remove generated coverage
	rm -f cover.out testdata/coverage.json testdata/token_coverage.json testdata/service/coverage.json
