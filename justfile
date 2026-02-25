# Default target
default: run

# Run jjui locally
run *args:
    go run ./cmd/jjui {{args}}

# Build jjui binary
build:
    go build ./cmd/jjui

# Rebase changes on top of upstream
rebase-upstream:
    jj git fetch --remote=upstream
    jj rebase -s "roots((::main | main::) ~ ::main@upstream)" -o main@upstream --ignore-immutable
