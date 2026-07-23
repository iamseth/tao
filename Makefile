.PHONY: help build clean test coverage-html lint modernize modernize-check install release-check verify verify-no-deps

COVERAGE_FILE := coverage.out
# Cached analyzer results contain source paths, so isolate them per checkout/worktree.
GOLANGCI_LINT_CACHE ?= $(HOME)/.cache/golangci-lint$(CURDIR)

help:
	@printf "Available targets:\n"
	@printf "  %-14s %s\n" "help" "Show this help text."
	@printf "  %-14s %s\n" "build" "Build the tao binary into the bin directory."
	@printf "  %-14s %s\n" "clean" "Remove build and coverage artifacts."
	@printf "  %-14s %s\n" "test" "Run all Go tests with coverage output."
	@printf "  %-14s %s\n" "coverage-html" "Open an HTML coverage report in the browser."
	@printf "  %-14s %s\n" "lint" "Run golangci-lint."
	@printf "  %-14s %s\n" "modernize" "Apply gopls modernize fixes."
	@printf "  %-14s %s\n" "modernize-check" "Check for gopls modernize findings."
	@printf "  %-14s %s\n" "install" "Build and install the tao binary into ~/.bin."
	@printf "  %-14s %s\n" "release-check" "Validate the GoReleaser config (requires goreleaser)."
	@printf "  %-14s %s\n" "verify-no-deps" "Fail if any third-party dependency is present."
	@printf "  %-14s %s\n" "verify" "Run the complete repository verification gate."

build:
	@mkdir -p bin
	@go build -o bin/tao ./cmd/tao

clean:
	@rm -rvf bin $(COVERAGE_FILE)

test:
	@go test -coverprofile=$(COVERAGE_FILE) ./...
	@awk 'NR>1 { \
		n=split($$1,p,"/"); pkg=p[1]; \
		for(i=2;i<n;i++) pkg=pkg"/"p[i]; \
		stmts[pkg]+=$$2; if($$3>0) cov[pkg]+=$$2 \
	} \
	END { for(k in stmts) printf "%6.1f%%  %s\n", 100*cov[k]/stmts[k], k }' \
		$(COVERAGE_FILE) | sort -k2

coverage-html: test
	@go tool cover -html=$(COVERAGE_FILE)

lint: modernize-check
	@GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" golangci-lint run --allow-parallel-runners

modernize:
	@go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@v0.22.0 -fix ./...

modernize-check:
	@go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@v0.22.0 ./...

install: build
	@rm -f ~/.bin/tao; cp ./bin/tao ~/.bin/tao

release-check:
	@goreleaser check

verify: build test lint modernize-check verify-no-deps

verify-no-deps:
	@count=$$(go list -m all | wc -l); \
	if [ "$$count" -ne 1 ]; then \
		echo "verify-no-deps: expected only the main module, found $$count modules:"; \
		go list -m all; \
		exit 1; \
	fi; \
	echo "verify-no-deps: no third-party dependencies."
