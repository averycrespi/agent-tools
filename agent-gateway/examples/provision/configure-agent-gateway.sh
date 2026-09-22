#!/bin/bash
# Configure a Linux guest's Pi client, not the host Gateway service.
# copy_paths: ["~/.config/agent-gateway/agent-token"]
# Never copy administrator credentials or the Gateway data root.
# Both export pairs remain supported from the canonical current token file.
# The legacy token path is inspected only to reject stale/conflicting authority.

set +x
set -euo pipefail

# This exact function is also installed in the managed block. Validate again at
# startup: copy_paths can refresh a credential without changing the block.
_agent_gateway_load() {
	unset AGENT_GATEWAY_ENDPOINT AGENT_GATEWAY_AGENT_TOKEN MCP_GATEWAY_ENDPOINT MCP_GATEWAY_AGENT_TOKEN
	local directory file mode size current selected='' token=''
	local endpoint='http://host.lima.internal:8210/mcp'
	[[ -d "$HOME/.config" && ! -L "$HOME/.config" && -O "$HOME/.config" ]] || return 1
	mode=$(stat -c '%a' "$HOME/.config" 2>/dev/null) || mode=$(stat -f '%Lp' "$HOME/.config" 2>/dev/null) || return 1
	[[ "$mode" =~ ^[0-7]{3,4}$ ]] && (( (8#$mode & 022) == 0 )) || return 1
	for directory in "$HOME/.config/agent-gateway" "$HOME/.config/mcp-gateway"; do
		# An absent leaf must not hide an unsafe or dangling parent path.
		[[ ! -L "$directory" ]] || return 1
		[[ -e "$directory" ]] || continue
		[[ -d "$directory" && -O "$directory" ]] || return 1
		mode=$(stat -c '%a' "$directory" 2>/dev/null) || mode=$(stat -f '%Lp' "$directory" 2>/dev/null) || return 1
		[[ "$mode" == 700 ]] || return 1
		file="$directory/agent-token"
		if [[ ! -e "$file" && ! -L "$file" ]]; then
			continue
		fi
		[[ ! -L "$file" && -f "$file" && -O "$file" && -r "$file" ]] || return 1
		mode=$(stat -c '%a' "$file" 2>/dev/null) || mode=$(stat -f '%Lp' "$file" 2>/dev/null) || return 1
		[[ "$mode" == 400 || "$mode" == 600 ]] || return 1
		size=$(stat -c '%s' "$file" 2>/dev/null) || size=$(stat -f '%z' "$file" 2>/dev/null) || return 1
		[[ "$size" == 53 || "$size" == 54 ]] || return 1
		LC_ALL=C grep -IqEx 'mgw_agent_[A-Za-z0-9_-]{43}' "$file" || return 1
		current=$(cat "$file" 2>/dev/null) || return 1
		[[ "$current" =~ ^mgw_agent_[A-Za-z0-9_-]{43}$ ]] || return 1
		if [[ -n "$selected" && "$current" != "$token" ]]; then
			return 1
		fi
		if [[ -z "$selected" ]]; then
			selected="$file"
			token="$current"
		fi
	done
	[[ "$selected" == "$HOME/.config/agent-gateway/agent-token" ]] || return 1
	export AGENT_GATEWAY_ENDPOINT="$endpoint"
	export AGENT_GATEWAY_AGENT_TOKEN="$token"
	export MCP_GATEWAY_ENDPOINT="$AGENT_GATEWAY_ENDPOINT"
	export MCP_GATEWAY_AGENT_TOKEN="$AGENT_GATEWAY_AGENT_TOKEN"
}

if ! ( _agent_gateway_load ); then
	echo 'error: reconcile copy_paths: require an owner-private valid ~/.config/agent-gateway/agent-token; the retired ~/.config/mcp-gateway/agent-token cannot be a fallback and any retained copy must agree. See the access-control guide. No credential was changed.' >&2
	exit 1
fi

BASHRC="$HOME/.bashrc"
if [[ -L "$BASHRC" || ( -e "$BASHRC" && ( ! -f "$BASHRC" || ! -O "$BASHRC" || ! -r "$BASHRC" ) ) ]]; then
	echo 'error: .bashrc must be an owned regular file, not a symlink.' >&2
	exit 1
fi

# Validate the entire layout before creating a replacement. Multiple blocks
# (including one of each name) require explicit reconciliation, not a guess.
filter_blocks() {
	local line ending active='' seen=0 name
	while IFS= read -r line; ending=$?; [[ $ending == 0 || -n "$line" ]]; do
		case "$line" in
			'# >>> agent-gateway >>>'|'# >>> mcp-gateway >>>')
				[[ -z "$active" && $seen == 0 ]] || return 1
				active="$line"; seen=1 ;;
			'# <<< agent-gateway <<<'|'# <<< mcp-gateway <<<')
				name=${line/'# <<< '/'# >>> '}; name=${name/' <<<'/' >>>'}
				[[ "$active" == "$name" ]] || return 1
				active='' ;;
			'# >>> agent-gateway-http-proxy >>>'|'# <<< agent-gateway-http-proxy <<<')
				# The explicit HTTP client script owns this independent block.
				[[ -z "$active" ]] || return 1
				printf '%s' "$line"; [[ $ending != 0 ]] || printf '\n' ;;
			*)
				case "$line" in
					*'>>>'*'agent-gateway'*|*'<<<'*'agent-gateway'*|*'>>>'*'mcp-gateway'*|*'<<<'*'mcp-gateway'*) return 1 ;;
				esac
				if [[ -z "$active" ]]; then
					printf '%s' "$line"
					[[ $ending != 0 ]] || printf '\n'
				fi ;;
		esac
	done
	[[ -z "$active" ]]
}

input=/dev/null
[[ ! -e "$BASHRC" ]] || input="$BASHRC"
if [[ -s "$input" ]] && ! LC_ALL=C grep -Iq '' "$input"; then
	echo 'error: .bashrc must be a text file without NUL bytes.' >&2
	exit 1
fi
if ! filter_blocks <"$input" >/dev/null; then
	echo 'error: malformed or multiple Gateway managed blocks; reconcile .bashrc before provisioning.' >&2
	exit 1
fi

umask 077
temporary=$(mktemp "$HOME/.bashrc.agent-gateway.XXXXXX")
trap 'rm -f "$temporary"' EXIT
filter_blocks <"$input" >"$temporary"
if [[ -s "$temporary" && -n "$(tail -c 1 "$temporary")" ]]; then
	printf '\n' >>"$temporary"
fi
{
	printf '%s\n' '# >>> agent-gateway >>>' 'set +x'
	declare -f _agent_gateway_load
	cat <<'BLOCK'
if ! _agent_gateway_load; then
	printf '%s\n' 'Agent Gateway: no client credentials exported; restore the private canonical agent-token, reconcile any retained legacy copy and reprovision.' >&2
fi
unset -f _agent_gateway_load
# <<< agent-gateway <<<
BLOCK
} >>"$temporary"
mv "$temporary" "$BASHRC"
echo 'Configured both Agent Gateway and compatibility exports. Open a fresh Bash shell and restart Pi.'
