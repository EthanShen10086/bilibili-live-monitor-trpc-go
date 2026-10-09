.PHONY: tools fmt fmt-check lint test test-integration verify hooks build smoke quality-test

tools:
	python3 scripts/quality.py tools
fmt:
	python3 scripts/quality.py fmt
fmt-check:
	python3 scripts/quality.py fmt-check
lint:
	python3 scripts/quality.py lint
test:
	python3 scripts/quality.py test
test-integration:
	python3 scripts/quality.py integration
verify:
	python3 scripts/quality.py verify
hooks:
	python3 scripts/hooks.py install
build:
	CGO_ENABLED=0 go build -trimpath -o dist/monitor ./cmd/monitor
smoke: build
	python3 scripts/smoke.py
quality-test:
	python3 -m unittest discover -s scripts/tests -v
