#!/usr/bin/env bash
# UserPromptSubmit hook — installed into a project by /create-dev-project.
#
# Injects ONLY the house conventions relevant to the current prompt, so the right
# rules are always in context without ever loading the whole ./conventions/ set.
# This is the "guaranteed" half of the system; the CLAUDE.md router index is the
# recall backstop for anything the keyword detection below doesn't catch.
#
# Detection is deliberately engine-free (just `tr` + bash `case`) so it behaves the
# same under BSD grep, GNU grep, ugrep, or none. Tune the needle lists per project.
set -eo pipefail

DIR="${CLAUDE_PROJECT_DIR:-$PWD}/conventions"
[ -d "$DIR" ] || exit 0

input="$(cat)"
if command -v jq >/dev/null 2>&1; then
  prompt="$(printf '%s' "$input" | jq -r '.prompt // empty' 2>/dev/null || true)"
else
  prompt="$input"
fi
[ -n "$prompt" ] || exit 0
case "$prompt" in /*) exit 0 ;; esac   # ignore slash commands — they manage their own context

# Lowercase; collapse every char except [a-z0-9.] to a space; pad with spaces. Then a
# space-bounded substring test (" go test", ".go ") is precise without a regex engine.
norm=" $(printf '%s' "$prompt" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9.' ' ') "

want=()
hit() { # <filename> <needle>...   append file if any needle is a substring of $norm
  local file="$1"; shift
  [ -f "$DIR/$file" ] || return 0
  local n
  for n in "$@"; do
    case "$norm" in *"$n"*) want+=("$file"); return 0 ;; esac
  done
}

hit golang-conventions.md   " golang " " goroutine " ".go " " go func" " go function" " go handler" " go struct" " go interface" " go module" " go package" " go mod " " go test" " go code" " go file"
hit python-conventions.md   " python " " pytest " " pyproject " " ruff " " asyncio " ".py "
hit vuejs-conventions.md    " vue " " vue3 " " pinia " " composition api " ".vue "
hit tailwind-conventions.md " tailwind " " tailwindcss "
hit docker-conventions.md   " docker " " compose " " dockerfile " " container " " containers " ".dockerignore "
hit database-conventions.md " sql " " postgres " " postgresql " " migration " " migrations " " database " ".sql "
hit testing-conventions.md  " test " " tests " " spec " " specs " " tdd " " pytest " " vitest " " jest "
hit commit-conventions.md   " commit " " commits "
hit justfile-conventions.md " justfile " " just recipe " " task runner "
hit readme-conventions.md   " readme "

[ "${#want[@]}" -gt 0 ] || exit 0

ctx="House coding conventions that apply to this request — follow them precisely:"
for f in "${want[@]}"; do
  ctx="$ctx"$'\n\n===== conventions/'"$f"$' =====\n'"$(cat "$DIR/$f")"
done

if command -v jq >/dev/null 2>&1; then
  jq -nc --arg c "$ctx" '{hookSpecificOutput:{hookEventName:"UserPromptSubmit",additionalContext:$c}}'
else
  printf '%s\n' "$ctx"
fi
exit 0
