# Rebase changes on top of upstream
rebase-upstream:
    jj git fetch --remote=upstream
    jj rebase -s "roots((::main | main::) ~ ::main@upstream)" -o main@upstream --ignore-immutable
