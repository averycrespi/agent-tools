#!/bin/bash
# Explicit fresh Linux guest client setup only; never install host/service trust.
# copy_paths: ~/.config/agent-gateway/{agent-token,http-ca.pem}
set +x
set -euo pipefail

_agent_gateway_http_load() {
	unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy ALL_PROXY all_proxy
	unset NO_PROXY no_proxy CURL_CA_BUNDLE REQUESTS_CA_BUNDLE SSL_CERT_FILE NODE_EXTRA_CA_CERTS
	local directory="$HOME/.config/agent-gateway" token='' mode size file
	[[ -d "$HOME/.config" && ! -L "$HOME/.config" && -O "$HOME/.config" ]] || return 1
	mode=$(stat -c '%a' "$HOME/.config") || return 1
	[[ "$mode" =~ ^[0-7]{3,4}$ ]] && (( (8#$mode & 022) == 0 )) || return 1
	[[ -d "$directory" && ! -L "$directory" && -O "$directory" ]] || return 1
	[[ $(stat -c '%a' "$directory") == 700 ]] || return 1
	for file in "$directory/agent-token" "$directory/http-ca.pem" "$directory/http-client-ca.pem"; do
		[[ -f "$file" && ! -L "$file" && -O "$file" && -r "$file" ]] || return 1
		mode=$(stat -c '%a' "$file") || return 1
		[[ "$mode" == 400 || "$mode" == 600 ]] || return 1
	done
	size=$(stat -c '%s' "$directory/agent-token") || return 1
	[[ "$size" == 53 || "$size" == 54 ]] || return 1
	LC_ALL=C grep -IqEx 'mgw_agent_[A-Za-z0-9_-]{43}' "$directory/agent-token" || return 1
	token=$(cat "$directory/agent-token") || return 1
	[[ "$token" =~ ^mgw_agent_[A-Za-z0-9_-]{43}$ ]] || return 1
	export HTTP_PROXY="http://agent:${token}@host.lima.internal:8212"
	export HTTPS_PROXY="$HTTP_PROXY" http_proxy="$HTTP_PROXY" https_proxy="$HTTP_PROXY"
	# No implicit bypass. Explicit exclusions bypass ALL Gateway policy/evidence.
	export NO_PROXY='' no_proxy=''
	export CURL_CA_BUNDLE="$directory/http-client-ca.pem"
	export REQUESTS_CA_BUNDLE="$CURL_CA_BUNDLE" SSL_CERT_FILE="$CURL_CA_BUNDLE"
	export NODE_EXTRA_CA_CERTS="$directory/http-ca.pem"
}

BASHRC="$HOME/.bashrc"
DIRECTORY="$HOME/.config/agent-gateway"
[[ -d "$HOME/.config" && ! -L "$HOME/.config" && -O "$HOME/.config" ]] || { echo 'error: unsafe client configuration parent.' >&2; exit 1; }
mode=$(stat -c '%a' "$HOME/.config")
[[ "$mode" =~ ^[0-7]{3,4}$ ]] && (( (8#$mode & 022) == 0 )) || { echo 'error: unsafe client configuration parent permissions.' >&2; exit 1; }
[[ -d "$DIRECTORY" && ! -L "$DIRECTORY" && -O "$DIRECTORY" && $(stat -c '%a' "$DIRECTORY") == 700 ]] || { echo 'error: prepare the private canonical client directory before provisioning.' >&2; exit 1; }
[[ ! -L "$BASHRC" && ( ! -e "$BASHRC" || ( -f "$BASHRC" && -O "$BASHRC" ) ) ]] || { echo 'error: unsafe .bashrc.' >&2; exit 1; }
# Do not import, remove, or override another proxy configuration.
filter_block() {
	local line ending active=0 seen=0
	while IFS= read -r line; ending=$?; [[ $ending == 0 || -n "$line" ]]; do
		case "$line" in
			'# >>> agent-gateway-http-proxy >>>') [[ $active == 0 && $seen == 0 ]] || return 1; active=1; seen=1 ;;
			'# <<< agent-gateway-http-proxy <<<') [[ $active == 1 ]] || return 1; active=0 ;;
			*)
				case "$line" in *'>>>'*'agent-gateway-http-proxy'*|*'<<<'*'agent-gateway-http-proxy'*) return 1;; esac
				if [[ $active == 0 ]]; then
					case "$line" in *'http-broker'*|*HTTP_PROXY*|*HTTPS_PROXY*|*http_proxy*|*https_proxy*|*ALL_PROXY*|*all_proxy*) return 1;; esac
					printf '%s' "$line"; [[ $ending != 0 ]] || printf '\n'
				fi ;;
		esac
	done
	[[ $active == 0 ]]
}
input=/dev/null
[[ ! -e "$BASHRC" ]] || input="$BASHRC"
if [[ -s "$input" ]] && ! LC_ALL=C grep -Iq '' "$input"; then
	echo 'error: .bashrc must be text without NUL bytes.' >&2; exit 1
fi
if ! filter_block <"$input" >/dev/null; then
	echo 'error: conflicting proxy configuration or malformed managed block; reconcile explicitly before fresh setup.' >&2; exit 1
fi
[[ -f "$DIRECTORY/http-ca.pem" && ! -L "$DIRECTORY/http-ca.pem" && -O "$DIRECTORY/http-ca.pem" ]] || { echo 'error: copy the exported public CA first.' >&2; exit 1; }
[[ -f /etc/ssl/certs/ca-certificates.crt ]] || { echo 'error: install the distribution public CA bundle before setup.' >&2; exit 1; }
command -v openssl >/dev/null || { echo 'error: openssl is required to validate the public CA.' >&2; exit 1; }
# Public certificates only, never a private-key import or a system trust change.
[[ $(grep -c '^-----BEGIN ' "$DIRECTORY/http-ca.pem") == 1 ]] && grep -q '^-----BEGIN CERTIFICATE-----$' "$DIRECTORY/http-ca.pem" || { echo 'error: expected one public certificate.' >&2; exit 1; }
openssl x509 -in "$DIRECTORY/http-ca.pem" -noout -checkend 0 >/dev/null 2>&1 || { echo 'error: invalid or expired public CA.' >&2; exit 1; }
[[ ! -L "$DIRECTORY/http-client-ca.pem" && ( ! -e "$DIRECTORY/http-client-ca.pem" || ( -f "$DIRECTORY/http-client-ca.pem" && -O "$DIRECTORY/http-client-ca.pem" ) ) ]] || exit 1
umask 077
temporary=$(mktemp "$HOME/.bashrc.agent-gateway-http.XXXXXX")
bundle=$(mktemp "$DIRECTORY/.http-client-ca.XXXXXX")
trap 'rm -f "$temporary" "$bundle"' EXIT
cat /etc/ssl/certs/ca-certificates.crt "$DIRECTORY/http-ca.pem" >"$bundle"
mv "$bundle" "$DIRECTORY/http-client-ca.pem"
if ! ( _agent_gateway_http_load ); then
	echo 'error: require private owned canonical agent-token and public CA files; no proxy exports installed.' >&2; exit 1
fi
filter_block <"$input" >"$temporary"
if [[ -s "$temporary" && -n "$(tail -c 1 "$temporary")" ]]; then printf '\n' >>"$temporary"; fi
{
	printf '%s\n' '# >>> agent-gateway-http-proxy >>>' 'set +x'
	declare -f _agent_gateway_http_load
	cat <<'BLOCK'
if ! _agent_gateway_http_load; then
	printf '%s\n' 'Agent Gateway HTTP: no proxy exports loaded; restore private client files and reprovision.' >&2
fi
unset -f _agent_gateway_http_load
# <<< agent-gateway-http-proxy <<<
BLOCK
} >>"$temporary"
mv "$temporary" "$BASHRC"
echo 'Configured explicit Gateway HTTP proxy and client-only CA trust. Open a fresh shell and restart clients.'
