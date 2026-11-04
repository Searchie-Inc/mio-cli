#!/bin/sh
# Tests for push_bump_branch.sh against real git (file:// remotes and a tiny
# HTTP server that records the Authorization headers it is sent).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d); trap 'kill $srv 2>/dev/null || true; rm -rf "$tmp"' EXIT
fail=0
ok()  { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fail=1; }

# ── lease: first push (branch absent) and update of an existing branch ──────
git init -q --bare "$tmp/remote.git"
git init -q "$tmp/w"; cd "$tmp/w"
git config user.email t@t; git config user.name t
echo a > f; git add f; git commit -q -m a
git remote add origin "file://$tmp/remote.git"
git branch -M main; git push -q origin main
git checkout -q -b bump
if "$here/push_bump_branch.sh" bump "file://$tmp/remote.git" origin >/dev/null 2>&1; then ok "first push (branch absent)"; else bad "first push (branch absent)"; fi
echo b > f; git commit -q -am b
git fetch -q origin
if "$here/push_bump_branch.sh" bump "file://$tmp/remote.git" origin >/dev/null 2>&1; then ok "update of an existing branch via a URL target"; else bad "update of an existing branch via a URL target (the 'stale info' rejection)"; fi
[ "$(git rev-parse bump)" = "$(git --git-dir="$tmp/remote.git" rev-parse bump)" ] && ok "remote tip equals local" || bad "remote tip equals local"
# rewritten history (the watcher force-pushes a regenerated branch)
git reset -q --hard HEAD~1; echo c > f; git commit -q -am c
if "$here/push_bump_branch.sh" bump "file://$tmp/remote.git" origin >/dev/null 2>&1; then ok "force update over rewritten history"; else bad "force update over rewritten history"; fi
# the lease still protects: the branch moved after the tip was read. The script
# reads the expected tip from <read-remote>; point that at a STALE mirror so the
# expected value differs from the real remote tip, as after a concurrent push.
git clone -q --bare "file://$tmp/remote.git" "$tmp/stale.git"
other="$tmp/other"; git clone -q "file://$tmp/remote.git" "$other"; ( cd "$other" && git config user.email o@o && git config user.name o && git checkout -q bump && echo z > z && git add z && git commit -q -m z && git push -q origin bump )
echo d > f; git commit -q -am d
if "$here/push_bump_branch.sh" bump "file://$tmp/remote.git" "file://$tmp/stale.git" >/dev/null 2>&1; then bad "a stale lease must be rejected by the script"; else ok "the script rejects a push when the branch moved since the tip was read"; fi

# ── credentials: the persisted checkout header must not override the URL's ──
cat > "$tmp/srv.py" <<'PY'
import base64, http.server, sys
log = open(sys.argv[1], "w", buffering=1)
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        auth = self.headers.get("Authorization")
        log.write("GET %s auth=%s\n" % (self.path.split("?")[0], auth))
        if auth is None:
            self.send_response(401); self.send_header("WWW-Authenticate", 'Basic realm="x"'); self.end_headers()
        else:
            self.send_response(404); self.end_headers()
    do_POST = do_GET
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[2])), H).serve_forever()
PY
port=$((20000 + $$ % 20000)); python3 "$tmp/srv.py" "$tmp/auth.log" "$port" & srv=$!
sleep 1
git remote add http "http://127.0.0.1:$port/o/r.git" 2>/dev/null || true
# Simulate actions/checkout: persisted GITHUB_TOKEN extraheader for this host.
git config --local "http.http://127.0.0.1:$port/.extraheader" "AUTHORIZATION: basic $(printf 'x-access-token:CHECKOUTTOKEN' | base64)"
# The script blanks the github.com header only (that is the real target); here we
# exercise the same mechanism by pointing it at the test host.
sed "s#http.https://github.com/.extraheader=#http.http://127.0.0.1:$port/.extraheader=#" "$here/push_bump_branch.sh" > "$tmp/push_test_host.sh"; chmod +x "$tmp/push_test_host.sh"
"$tmp/push_test_host.sh" bump "http://x-access-token:APPTOKEN@127.0.0.1:$port/o/r.git" origin >/dev/null 2>&1 || true
if grep -q "CHECKOUTTOKEN" "$tmp/auth.log" 2>/dev/null || grep -q "$(printf 'x-access-token:CHECKOUTTOKEN' | base64)" "$tmp/auth.log" 2>/dev/null; then bad "the persisted checkout token reached the server"; else ok "the persisted checkout token is not sent"; fi
if grep -q "$(printf 'x-access-token:APPTOKEN' | base64)" "$tmp/auth.log" 2>/dev/null; then ok "the URL (App) credentials are what authenticates"; else bad "the URL (App) credentials never reached the server"; fi
# origin target: the persisted header must still be sent (it is origin's only credential)
: > "$tmp/auth.log"
git remote set-url origin "http://127.0.0.1:$port/o/r.git"
sed "s#http.https://github.com/.extraheader=#http.http://127.0.0.1:$port/.extraheader=#" "$here/push_bump_branch.sh" > "$tmp/push_test_host.sh"; chmod +x "$tmp/push_test_host.sh"
"$tmp/push_test_host.sh" bump origin "file://$tmp/remote.git" >/dev/null 2>&1 || true
if grep -q "$(printf 'x-access-token:CHECKOUTTOKEN' | base64)" "$tmp/auth.log" 2>/dev/null; then ok "a push to origin keeps the persisted checkout credential"; else bad "a push to origin lost its only credential (fallback path would fail to push)"; fi
exit $fail
