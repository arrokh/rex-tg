STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@v0.8.2
BIN := bin/rex-tg
INSTALL_DIR ?= $(HOME)/.local/bin

.PHONY: build test core check fmt install e2e clean

build:
	go build -o $(BIN) ./cmd/rex-tg

test:
	go test ./...

# core runs one daemon test per core flow: pairing, listing and opening a
# topic, typing a message, the safety check, answers and progress, an alert,
# pi-vim and Claude Code vim, at-most-once sends, a restart, a stopped agent,
# and a deleted topic. It fails when a name here no longer matches a test.
CORE_TESTS := TestPairing TestAgentsListsPiPane TestOpenTopicWithIntroAndBackfill \
	TestMessageWaitsForIdleThenTypes TestRefusedWhenShellInFront TestProgressMessageBecomesSummary \
	TestPermissionAlert TestPiVimReachesInsertBeforeThePaste TestClaudeVisualModeIsLeftBeforeThePaste \
	TestUnknownReplyIsCheckedAndSentOnce TestFailedTypingIsNeverRetried TestRestartRecovery \
	TestPaneClosedTakesTopicOffline TestAgentDyingMidTurnEndsTheTurn TestDeletedTopicIsMadeAgainFromAgents \
	TestNewStartsAnAgentAndOpensItsTopic TestFinalAnswerImageIsSentAfterTheAnswer
empty :=
space := $(empty) $(empty)
CORE_RUN := ^($(subst $(space),|,$(strip $(CORE_TESTS))))$$

core:
	@found=$$(go test -list '$(CORE_RUN)' ./internal/daemon/ | grep -c '^Test'); \
	if [ "$$found" -ne $(words $(CORE_TESTS)) ]; then \
		echo "make core: found $$found of $(words $(CORE_TESTS)) tests; fix CORE_TESTS in the Makefile"; exit 1; \
	fi
	go test -count=1 -run '$(CORE_RUN)' ./internal/daemon/

check:
	@test -z "$$(gofmt -l . | grep -v '^vendor/')" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	$(STATICCHECK) ./...
	go test -race ./...

fmt:
	gofmt -w .

install: build
	install -d $(INSTALL_DIR)
	install -m 0755 $(BIN) $(INSTALL_DIR)/rex-tg

e2e:
	go test -tags e2e -count=1 ./e2e/...

clean:
	rm -rf bin
