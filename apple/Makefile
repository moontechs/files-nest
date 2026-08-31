PROJECT := macos/FilesNest/FilesNest.xcodeproj
SCHEME := FilesNest
DESTINATION := platform=macOS

.PHONY: help build core-test test-app test-ui test

help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "  build      Build the macOS Debug app"
	@echo "  core-test  Run host-free FilesNestCore tests"
	@echo "  test-app   Run app-hosted macOS unit tests"
	@echo "  test-ui    Run macOS end-to-end UI tests"
	@echo "  test       Run core and app-hosted unit tests"

build:
	xcodebuild -project $(PROJECT) -scheme $(SCHEME) -configuration Debug -destination '$(DESTINATION)' build

core-test:
	swift test --package-path FilesNestCore

test-app:
	xcodebuild -project $(PROJECT) -scheme $(SCHEME) -destination '$(DESTINATION)' -only-testing:FilesNestTests test

test-ui:
	xcodebuild -project $(PROJECT) -scheme $(SCHEME) -destination '$(DESTINATION)' -only-testing:FilesNestUITests test

test: core-test test-app
